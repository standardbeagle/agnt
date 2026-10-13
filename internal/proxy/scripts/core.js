// Core DevTool instrumentation module
// Handles WebSocket connection, messaging, error/performance tracking
//
// INDUSTRIAL-STRENGTH ERROR HANDLING:
// - Top-level error boundary
// - All APIs feature-detected
// - All operations wrapped in try-catch
// - Graceful degradation on failures

(function() {
  'use strict';

  var frameContext = window.__devtool_context;

  // Top-level error boundary - if anything fails, log and abort cleanly
  try {
    // Feature detection
    var hasWebSocket = typeof WebSocket !== 'undefined';
    var hasSessionStorage = (function() {
      try {
        var test = '__test__';
        sessionStorage.setItem(test, test);
        sessionStorage.removeItem(test);
        return true;
      } catch (e) {
        return false;
      }
    })();
    var hasPerformance = typeof window.performance !== 'undefined' &&
                         typeof window.performance.now === 'function';
    var hasLocation = typeof window.location !== 'undefined' &&
                      typeof window.location.protocol === 'string';

    if (!hasLocation) {
      console.error('[DevTool] Critical: window.location unavailable');
      return;
    }

    if (!hasWebSocket) {
      console.warn('[DevTool] WebSocket not supported - metrics disabled');
    }

    // Configuration
    // Use window.location.host for WebSocket URL to automatically match proxy port
    var protocol = (function() {
      try {
        return window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      } catch (e) {
        return 'ws:';
      }
    })();

    var WS_URL = protocol + '//' + window.location.host + '/__devtool_metrics';
    var ws = null;
    var reconnectAttempts = 0;
    var reconnectTimer = null;
    // Reconnect never gives up: a proxy or daemon restart that outlasts a
    // fixed attempt budget would otherwise leave an open page with no
    // telemetry and no exec channel until someone reloads it. The delay
    // doubles from RECONNECT_BASE_MS and is capped at RECONNECT_MAX_MS, so a
    // long outage costs one attempt every RECONNECT_MAX_MS.
    var RECONNECT_BASE_MS = 1000;
    var RECONNECT_MAX_MS = 30000;
    // Set once init decides this frame owns a control transport; the re-arm
    // hooks (online / visibilitychange) do nothing in frames that never
    // connect.
    var transportWanted = false;

    // Session ID - unique per browser tab/window, persists across page navigations
    var COOKIE_NAME = '__devtool_sid';
    var SESSION_STORAGE_KEY = '__devtool_session_id';
    var sessionId = null;

    // Safe cookie operations
    function getCookie(name) {
      if (typeof name !== 'string') return null;

      try {
        if (!document.cookie) return null;

        var cookies = document.cookie.split(';');
        for (var i = 0; i < cookies.length; i++) {
          var cookie = cookies[i].trim();
          if (cookie.indexOf(name + '=') === 0) {
            return cookie.substring(name.length + 1);
          }
        }
      } catch (e) {
        console.error('[DevTool] getCookie failed:', e);
      }
      return null;
    }

    function setCookie(name, value) {
      if (typeof name !== 'string' || typeof value !== 'string') return false;

      try {
        document.cookie = name + '=' + value + '; path=/; SameSite=Lax';
        return true;
      } catch (e) {
        console.error('[DevTool] setCookie failed:', e);
        return false;
      }
    }

    function getOrCreateSessionId() {
      if (sessionId) return sessionId;

      try {
        // Try sessionStorage first (tab-specific)
        if (hasSessionStorage) {
          try {
            sessionId = sessionStorage.getItem(SESSION_STORAGE_KEY);
            if (!sessionId) {
              sessionId = generateSessionId();
              sessionStorage.setItem(SESSION_STORAGE_KEY, sessionId);
            }
          } catch (e) {
            console.warn('[DevTool] sessionStorage access failed:', e);
            sessionId = null;
          }
        }

        // Fallback to cookie
        if (!sessionId) {
          sessionId = getCookie(COOKIE_NAME);
          if (!sessionId) {
            sessionId = generateSessionId();
          }
        }

        // Always sync to cookie (for HTTP request tracking)
        if (sessionId) {
          setCookie(COOKIE_NAME, sessionId);
        }
      } catch (e) {
        console.error('[DevTool] Session ID generation failed:', e);
        sessionId = generateSessionId(); // Last resort
      }

      return sessionId || 'sess-fallback';
    }

    function generateSessionId() {
      try {
        var timestamp = Date.now().toString(36);
        var random = Math.random().toString(36).slice(2, 11);
        return 'sess-' + timestamp + '-' + random;
      } catch (e) {
        return 'sess-' + Date.now() + '-fallback';
      }
    }

    // Initialize session ID
    try {
      getOrCreateSessionId();
    } catch (e) {
      console.error('[DevTool] Session initialization failed:', e);
    }

    // Native console refs captured BEFORE setupConsoleOverride replaces them.
    // Internal DevTool errors must log through these: routing them through the
    // overridden console.error would re-enter the override, land in the page
    // error buffers, and be re-sent to the server — noise amplification of our
    // own diagnostics.
    var nativeConsoleError = console.error.bind(console);
    var nativeConsoleLog = console.log.bind(console);

    // Error reporting to server. Deliberately does NOT go through the console
    // override or the consolidated error buffer — internal instrumentation
    // failures are sent once to the server, tagged, and kept out of the
    // page-error surfaces the agent reads.
    function reportInternalError(context, error) {
      try {
        nativeConsoleError('[DevTool][INTERNAL]', context, error);

        // Try to send to server if connection is available
        if (ws && ws.readyState === WebSocket.OPEN) {
          safeWebSocketSend(ws, JSON.stringify({
            type: 'error',
            data: {
              message: '[DevTool Internal] ' + context,
              error: error ? error.toString() : 'Unknown error',
              stack: error && error.stack ? error.stack : '',
              module: 'core',
              timestamp: Date.now()
            },
            url: safeGetUrl(),
            session_id: getOrCreateSessionId(),
            frame_id: frameContext.frameId()
          }));
        }
      } catch (e) {
        // Last resort - just console (native ref; never re-enter the override)
        nativeConsoleError('[DevTool] Error reporting failed:', e);
      }
    }

    // Safe WebSocket send
    function safeWebSocketSend(socket, data) {
      if (!socket || socket.readyState !== WebSocket.OPEN) {
        return false;
      }

      try {
        socket.send(data);
        return true;
      } catch (e) {
        // Native ref: a send failure logged through the overridden
        // console.error would itself be captured and re-sent — feedback loop.
        nativeConsoleError('[DevTool] WebSocket send failed:', e);
        return false;
      }
    }

    // Safe URL getter
    function safeGetUrl() {
      try {
        return window.location.href || '';
      } catch (e) {
        return '';
      }
    }

    // Connection-state fan-out.
    //
    // Every UI surface that reflects transport health (the indicator status
    // dot, panel send button) is driven from here, so the notifier must fire
    // on *every* edge — open, close, and error — not just on open. A notify
    // that only fires on open leaves the dot showing whatever colour it had
    // when it was created, which is the "grey while actually connected" bug.
    var connectedCallbacks = [];

    // 'connected' | 'connecting' | 'disconnected'. Derived from the live
    // socket rather than cached, so it cannot go stale behind an edge we
    // failed to observe.
    function connectionState() {
      try {
        if (ws) {
          if (ws.readyState === WebSocket.OPEN) return 'connected';
          if (ws.readyState === WebSocket.CONNECTING) return 'connecting';
        }
        // Socket is closed/absent but a retry is armed: still "trying".
        if (reconnectTimer) return 'connecting';
        return 'disconnected';
      } catch (e) {
        return 'disconnected';
      }
    }

    function notifyConnectionState() {
      var st = connectionState();
      for (var i = 0; i < connectedCallbacks.length; i++) {
        try { connectedCallbacks[i](st === 'connected', st); } catch (e) { /* one bad sink must not stop the rest */ }
      }
    }

    // The WebSocket wiring lives in connect() further down, next to the
    // transport diagnostics it records.

    // Message handlers for other modules
    var messageHandlers = [];

    // Handle messages from server
    function handleServerMessage(message) {
      if (!message || typeof message !== 'object') return;

      try {
        // Handle execution requests. Under the always-wrap model an exec may be
        // addressed to a specific content frame (message.frame_id); run only
        // when untargeted (legacy / all content frames) or addressed to this
        // frame, so a multi-frame page does not produce duplicate replies.
        // See docs/responsive-canonical-target.md §5.3.
        if (message.type === 'execute' && message.code) {
          var targetFrame = message.frame_id || '';
          var myFrame = frameContext.frameId();
          // '@chrome' is a role token addressing the outer shell regardless of its
          // id, so the agent can run a REPL in the outer frame ("outer" target)
          // without knowing the per-page shell id.
          var myRole = frameContext.role;
          var match = (targetFrame === '' && myRole !== 'chrome') ||
            targetFrame === myFrame ||
            (targetFrame === '@chrome' && myRole === 'chrome');
          if (match) {
            executeJavaScript(message.id, message.code);
          }
        }

        // Handle proxy diagnostics from server
        if (message.type === 'proxy_diagnostic' && message.payload) {
          var diag = message.payload;
          // Add to diagnostics buffer so it appears in __devtool_transport_diag
          addDiagnostic('proxy', diag.event || 'unknown', {
            level: diag.level,
            category: diag.category,
            message: diag.message,
            request_id: diag.request_id,
            method: diag.method,
            url: diag.url,
            target: diag.target,
            timestamp: diag.timestamp,
            data: diag.data
          });

          // Also add to consolidated error stream for errors
          if (diag.level === 'error' || diag.level === 'warning') {
            addConsolidatedError({
              source: 'proxy',
              level: diag.level,
              event: diag.event,
              message: diag.message,
              url: diag.url,
              target: diag.target,
              timestamp: diag.timestamp || Date.now(),
              data: diag.data
            });
          }
        }

        // Notify registered handlers
        for (var i = 0; i < messageHandlers.length; i++) {
          try {
            if (typeof messageHandlers[i] === 'function') {
              messageHandlers[i](message);
            }
          } catch (e) {
            reportInternalError('message_handler_failed', e);
          }
        }
      } catch (e) {
        reportInternalError('handleServerMessage_failed', e);
      }
    }

    // Register a callback for WebSocket connection-state changes. The callback
    // receives (connected, state) where state is
    // 'connected' | 'connecting' | 'disconnected'.
    //
    // The current state is replayed synchronously at registration: core.js
    // connects during its own init, so a subscriber that loads later (the
    // indicator does) would otherwise miss the open edge entirely and render
    // a permanently stale status.
    function onConnected(cb) {
      if (typeof cb !== 'function') return;
      connectedCallbacks.push(cb);
      var st = connectionState();
      try { cb(st === 'connected', st); } catch (e) { /* defensive */ }
    }

    // Register a message handler
    function onMessage(handler) {
      if (typeof handler !== 'function') {
        console.warn('[DevTool] onMessage: handler must be a function');
        return false;
      }

      try {
        messageHandlers.push(handler);
        return true;
      } catch (e) {
        reportInternalError('onMessage_failed', e);
        return false;
      }
    }

    // Execute JavaScript sent from server
    function executeJavaScript(execId, code) {
      if (typeof code !== 'string') {
        console.warn('[DevTool] executeJavaScript: invalid code type');
        return;
      }

      var startTime = hasPerformance ? performance.now() : Date.now();

      function sendResult(result, error) {
        try {
          var duration = hasPerformance ?
            (performance.now() - startTime) :
            (Date.now() - startTime);

          send('execution', {
            exec_id: execId || '',
            result: result || '',
            error: error || '',
            duration: duration,
            timestamp: Date.now()
          });
        } catch (e) {
          reportInternalError('sendResult_failed', e);
        }
      }

      // Format result - uses compact JSON by default to reduce token usage
      // Pass {__pretty: true} in result object to get pretty-printed output
      function formatResult(val) {
        try {
          if (val === undefined) {
            return 'undefined';
          } else if (val === null) {
            return 'null';
          } else if (typeof val === 'function') {
            return val.toString();
          } else if (typeof val === 'object') {
            try {
              // Check for explicit pretty-print request. Shallow-copy before
              // dropping the marker — the caller's object must not be mutated
              // (it may be live page state).
              var pretty = val && val.__pretty === true;
              if (pretty) {
                val = Object.assign({}, val);
                delete val.__pretty;
              }
              // Default to compact JSON (no indentation) for token efficiency
              return JSON.stringify(val, null, pretty ? 2 : 0);
            } catch (e) {
              return String(val);
            }
          } else {
            return String(val);
          }
        } catch (e) {
          return '[Formatting Error]';
        }
      }

      try {
        var result = eval(code);

        // Check if result is a Promise
        if (result && typeof result.then === 'function') {
          result.then(function(resolved) {
            sendResult(formatResult(resolved), '');
          }).catch(function(err) {
            try {
              var error = err.toString();
              if (err.stack) {
                error += '\n' + err.stack;
              }
              sendResult('', error);
            } catch (e) {
              sendResult('', 'Promise error formatting failed');
            }
          });
        } else {
          sendResult(formatResult(result), '');
        }
      } catch (err) {
        try {
          var error = err.toString();
          if (err.stack) {
            error += '\n' + err.stack;
          }
          sendResult('', error);
        } catch (e) {
          sendResult('', 'Execution error formatting failed');
        }
      }
    }

    /**
     * Send a message to the DevTool server via WebSocket.
     * See documentation comment in original for full type listing.
     */
    function send(type, data) {
      if (!hasWebSocket) return false;
      if (!ws || ws.readyState !== WebSocket.OPEN) return false;
      if (typeof type !== 'string') return false;
      if (!data || typeof data !== 'object') return false;

      try {
        var message = JSON.stringify({
          type: type,
          data: data,
          url: safeGetUrl(),
          session_id: getOrCreateSessionId(),
          // Attribute telemetry to the emitting content frame (always-wrap
          // model). Empty for unframed/legacy pages. See
          // docs/responsive-canonical-target.md §5.2.
          frame_id: frameContext.frameId()
        });

        return safeWebSocketSend(ws, message);
      } catch (e) {
        reportInternalError('send_failed', e);
        return false;
      }
    }

    // Send binary data to server (for audio streaming)
    function sendBinary(data) {
      if (!hasWebSocket) return false;
      if (!ws || ws.readyState !== WebSocket.OPEN) return false;
      if (!data) return false;

      return safeWebSocketSend(ws, data);
    }

    // Error tracking - wrap in try-catch to prevent handler errors
    function setupErrorTracking() {
      try {
        if (typeof window.addEventListener !== 'function') return;

        window.addEventListener('error', function(event) {
          try {
            if (!event) return;

            send('error', {
              message: event.message || 'Unknown error',
              source: event.filename || '',
              lineno: event.lineno || 0,
              colno: event.colno || 0,
              error: event.error ? event.error.toString() : '',
              stack: event.error && event.error.stack ? event.error.stack : '',
              timestamp: Date.now()
            });
          } catch (e) {
            reportInternalError('error_event_handler_failed', e);
          }
        });

        window.addEventListener('unhandledrejection', function(event) {
          try {
            if (!event) return;

            var reason = event.reason || 'Unknown rejection';
            send('error', {
              message: 'Unhandled Promise Rejection: ' + reason,
              source: '',
              lineno: 0,
              colno: 0,
              error: reason.toString ? reason.toString() : String(reason),
              stack: reason.stack || '',
              timestamp: Date.now()
            });
          } catch (e) {
            reportInternalError('rejection_handler_failed', e);
          }
        });
      } catch (e) {
        reportInternalError('error_tracking_setup_failed', e);
      }
    }

    // Error buffer management for diagnostics panel
    var MAX_ERROR_ENTRIES = 100;
    var jsErrorBuffer = [];
    var consoleErrorBuffer = [];
    var consoleWarningBuffer = [];

    // The proxy wraps a top-level navigation in a chrome shell whose body is a
    // content <iframe> with the real page. Error capture (setupErrorTrackingWith
    // Buffer) runs only in the content frame, but the indicator's Errors tab
    // reads window.__devtool_errors in the SHELL — a separate window. Forward
    // each buffered error up to the shell (same-origin direct reach; the proxy
    // serves both frames) so the shell's buffers, and thus the Errors tab +
    // badge, reflect the page. kind is 'js' | 'console' | 'warning'.
    function forwardErrorToShell(kind, entry) {
      if (!frameContext.isContent()) { return; }
      try {
        var ingest = frameContext.shellExport('__devtool_errors_ingest');
        if (typeof ingest === 'function') {
          ingest(kind, entry);
        }
      } catch (e) { /* cross-origin / shell gone — best-effort */ }
    }

    // Shell-side sink for errors forwarded by content frames. Terminal: pushes
    // into the shell's own per-kind buffers (does not re-forward), so getDedup-
    // licatedErrors / getStats / the Errors tab all see content-frame errors
    // unchanged. Deliberately does NOT feed the consolidated stream
    // (addConsolidatedError) — no shell-side consumer reads getConsolidated*
    // today. If one is added, forwarded errors must also land there.
    function ingestForwardedError(kind, entry) {
      if (!entry) { return; }
      if (kind === 'js') { addToBuffer(jsErrorBuffer, entry); }
      else if (kind === 'console') { addToBuffer(consoleErrorBuffer, entry); }
      else if (kind === 'warning') { addToBuffer(consoleWarningBuffer, entry); }
    }

    // Consolidated error stream - combines errors from all sources
    // Sources: proxy (server-side errors), js (JavaScript errors), console (console.error),
    //          http (HTTP errors like 4xx/5xx), process (stdout/stderr from processes)
    var MAX_CONSOLIDATED_ENTRIES = 200;
    var consolidatedErrorBuffer = [];
    var consolidatedErrorListeners = [];

    function addConsolidatedError(entry) {
      var normalized = {
        id: 'err-' + Date.now() + '-' + Math.random().toString(36).slice(2, 8),
        timestamp: entry.timestamp || Date.now(),
        source: entry.source || 'unknown',  // proxy, js, console, http, process
        level: entry.level || 'error',      // error, warning, info
        event: entry.event || 'error',      // event type (e.g., connection_refused, js_error)
        message: entry.message || '',
        url: entry.url || '',
        data: entry.data || {}
      };

      consolidatedErrorBuffer.push(normalized);
      if (consolidatedErrorBuffer.length > MAX_CONSOLIDATED_ENTRIES) {
        consolidatedErrorBuffer.shift();
      }

      // Notify listeners
      for (var i = 0; i < consolidatedErrorListeners.length; i++) {
        try {
          consolidatedErrorListeners[i](normalized);
        } catch (e) {
          // Don't let listener errors break the stream
        }
      }

      // Log to console in debug mode
      if (window.__devtool_debug) {
        var levelIcon = normalized.level === 'error' ? '\u274c' : (normalized.level === 'warning' ? '\u26a0\ufe0f' : '\u2139\ufe0f');
        console.log('[DevTool Error Stream]', levelIcon, normalized.source + ':', normalized.message);
      }
    }

    function addToBuffer(buffer, entry) {
      buffer.push(entry);
      if (buffer.length > MAX_ERROR_ENTRIES) {
        buffer.shift();
      }
    }

    function getDeduplicatedErrors(buffer) {
      var errors = {};

      buffer.forEach(function(entry) {
        // Group by message (first 100 chars for deduplication)
        var key = entry.message.substring(0, 100);
        if (!errors[key]) {
          errors[key] = {
            message: entry.message,
            count: 0,
            firstSeen: entry.timestamp,
            lastSeen: entry.timestamp,
            source: entry.source || '',
            lineno: entry.lineno || 0,
            stack: entry.stack || '',
            examples: []
          };
        }
        errors[key].count++;
        errors[key].lastSeen = entry.timestamp;
        if (errors[key].examples.length < 3) {
          errors[key].examples.push({
            timestamp: entry.timestamp,
            source: entry.source,
            lineno: entry.lineno,
            colno: entry.colno
          });
        }
      });

      // Convert to array and sort by count
      var result = [];
      for (var key in errors) {
        result.push(errors[key]);
      }
      result.sort(function(a, b) {
        return b.count - a.count;
      });

      return result;
    }

    // Signatures of framework console.warn messages worth forwarding to the
    // server. Vue/Svelte/Solid reactivity + hydration warnings only — not the
    // entire warning stream. Kept in sync with the Go diagnostic classifier
    // (internal/tools/currentpage_diagnostics.go).
    var FRAMEWORK_WARN_RE = /\[vue warn\]|avoid mutating a prop|target is readonly|hydrat|\[svelte\]|unknown prop|createroot|non-reactive|reactiv/i;

    // Most characters kept from one console argument, and most object nodes
    // serialised for one argument. console.error(hugeStateObject) must cost a
    // bounded amount of work and a bounded WebSocket frame.
    var MAX_CONSOLE_ARG_CHARS = 2000;
    var MAX_CONSOLE_ARG_NODES = 500;

    function isErrorLike(v) {
      return !!v && typeof v === 'object' &&
        (v instanceof Error || (typeof v.message === 'string' && typeof v.stack === 'string'));
    }

    // describeError renders an Error as "Name: message" followed by its stack.
    // JSON.stringify(new Error('boom')) is "{}" — name, message and stack are
    // not enumerable — so an Error must never go through JSON.
    function describeError(err) {
      var head = '';
      try { head = String(err); } catch (e) { head = 'Error'; }
      var stack = '';
      try { stack = typeof err.stack === 'string' ? err.stack : ''; } catch (e) { stack = ''; }
      if (!stack) return head;
      // V8 stacks already start with "Name: message"; others do not.
      return stack.indexOf(head) === 0 ? stack : head + '\n' + stack;
    }

    function truncateConsoleArg(s) {
      if (s.length <= MAX_CONSOLE_ARG_CHARS) return s;
      return s.substring(0, MAX_CONSOLE_ARG_CHARS) +
        '... [truncated ' + (s.length - MAX_CONSOLE_ARG_CHARS) + ' chars]';
    }

    function formatConsoleArg(arg) {
      var out;
      try {
        if (isErrorLike(arg)) {
          out = describeError(arg);
        } else if (typeof arg === 'object' && arg !== null) {
          var budget = MAX_CONSOLE_ARG_NODES;
          try {
            out = JSON.stringify(arg, function(key, value) {
              if (isErrorLike(value)) return describeError(value);
              if (typeof value === 'object' && value !== null) {
                // Past the budget, stop descending: the rest of a large graph
                // is summarised instead of walked.
                if (budget-- <= 0) return '[...]';
              }
              return value;
            });
          } catch (e) {
            out = String(arg);
          }
          if (out === undefined) out = String(arg);
        } else {
          out = String(arg);
        }
      } catch (e) {
        out = '[unprintable]';
      }
      return truncateConsoleArg(out);
    }

    // formatConsoleArgs joins the arguments of one console call and returns
    // the stack of the first Error among them, so the captured entry keeps it
    // as a field rather than only inside the message text.
    function formatConsoleArgs(args) {
      var parts = [];
      var stack = '';
      for (var i = 0; i < args.length; i++) {
        parts.push(formatConsoleArg(args[i]));
        if (!stack && isErrorLike(args[i])) {
          try { stack = truncateConsoleArg(String(args[i].stack || '')); } catch (e) { stack = ''; }
        }
      }
      return { message: parts.join(' '), stack: stack };
    }

    // Override console.error to capture console errors
    function setupConsoleOverride() {
      try {
        var originalConsoleError = console.error;
        var originalConsoleWarn = console.warn;

        console.error = function() {
          // Call original console.error
          originalConsoleError.apply(console, arguments);

          // Capture error
          try {
            var formatted = formatConsoleArgs(arguments);
            var message = formatted.message;

            var entry = {
              message: message,
              timestamp: Date.now(),
              source: 'console.error',
              lineno: 0,
              colno: 0,
              stack: formatted.stack
            };

            addToBuffer(consoleErrorBuffer, entry);
            forwardErrorToShell('console', entry);

            // Add to consolidated error stream
            addConsolidatedError({
              source: 'console',
              level: 'error',
              event: 'console_error',
              message: message,
              url: safeGetUrl()
            });

            // Also send to server
            send('error', {
              message: 'Console Error: ' + message,
              source: 'console',
              lineno: 0,
              colno: 0,
              error: message,
              stack: formatted.stack,
              timestamp: Date.now()
            });
          } catch (e) {
            reportInternalError('console_error_override_failed', e);
          }
        };

        console.warn = function() {
          // Call original console.warn
          originalConsoleWarn.apply(console, arguments);

          // Capture warning
          try {
            var formatted = formatConsoleArgs(arguments);
            var message = formatted.message;

            var entry = {
              message: message,
              timestamp: Date.now(),
              source: 'console.warn',
              lineno: 0,
              colno: 0,
              stack: formatted.stack
            };

            addToBuffer(consoleWarningBuffer, entry);
            forwardErrorToShell('warning', entry);

            // Add to consolidated error stream
            addConsolidatedError({
              source: 'console',
              level: 'warning',
              event: 'console_warning',
              message: message,
              url: safeGetUrl()
            });

            // Forward framework reactivity/hydration warnings to the server so
            // the currentpage diagnostic classifier can see them. Vue, Svelte,
            // and Solid emit their highest-value runtime diagnostics via
            // console.warn (not console.error), so without this they are blind
            // to the agent. Gated by a signature allowlist to keep generic
            // deprecation/dev noise out of the error buffer.
            if (FRAMEWORK_WARN_RE.test(message)) {
              send('error', {
                message: 'Console Warning: ' + message,
                source: 'console',
                lineno: 0,
                colno: 0,
                error: message,
                stack: formatted.stack,
                timestamp: Date.now()
              });
            }
          } catch (e) {
            reportInternalError('console_warn_override_failed', e);
          }
        };
      } catch (e) {
        reportInternalError('console_override_setup_failed', e);
      }
    }

    // Enhance error tracking to also capture to buffer
    function setupErrorTrackingWithBuffer() {
      setupErrorTracking();
      setupConsoleOverride();

      // Add to existing error listener to also capture to buffer
      try {
        if (typeof window.addEventListener !== 'function') return;

        window.addEventListener('error', function(event) {
          try {
            if (!event) return;

            var entry = {
              message: event.message || 'Unknown error',
              source: event.filename || '',
              lineno: event.lineno || 0,
              colno: event.colno || 0,
              error: event.error ? event.error.toString() : '',
              stack: event.error && event.error.stack ? event.error.stack : '',
              timestamp: Date.now()
            };

            addToBuffer(jsErrorBuffer, entry);
            forwardErrorToShell('js', entry);

            // Add to consolidated error stream
            addConsolidatedError({
              source: 'js',
              level: 'error',
              event: 'js_error',
              message: entry.message,
              url: entry.source,
              data: {
                lineno: entry.lineno,
                colno: entry.colno,
                stack: entry.stack
              }
            });
          } catch (e) {
            reportInternalError('error_buffer_capture_failed', e);
          }
        });
      } catch (e) {
        reportInternalError('error_buffer_setup_failed', e);
      }
    }

    // Performance tracking
    function sendPageLoad() {
      // The chrome shell opens a control WebSocket for the indicator + panel
      // but is not a telemetry context — it must not report its own page load
      // or performance (the content frame owns that).
      if (frameContext.isChrome()) { return; }
      try {
        if (document.readyState === 'complete') {
          capturePerformance();
        } else {
          if (typeof window.addEventListener === 'function') {
            window.addEventListener('load', function() {
              try {
                capturePerformance();
              } catch (e) {
                reportInternalError('load_event_handler_failed', e);
              }
            });
          }
        }
      } catch (e) {
        reportInternalError('sendPageLoad_failed', e);
      }
    }

    function capturePerformance() {
      // Use setTimeout to ensure all metrics are available
      setTimeout(function() {
        try {
          if (!hasPerformance) {
            console.warn('[DevTool] Performance API not available');
            return;
          }

          var perf = window.performance;
          if (!perf || !perf.timing) {
            console.warn('[DevTool] Performance timing not available');
            return;
          }

          var timing = perf.timing;
          if (!timing.navigationStart) {
            console.warn('[DevTool] Navigation timing not available');
            return;
          }

          var metrics = {
            navigation_start: timing.navigationStart || 0,
            dom_content_loaded: (timing.domContentLoadedEventEnd || 0) - (timing.navigationStart || 0),
            load_event_end: (timing.loadEventEnd || 0) - (timing.navigationStart || 0),
            dom_interactive: (timing.domInteractive || 0) - (timing.navigationStart || 0),
            dom_complete: (timing.domComplete || 0) - (timing.navigationStart || 0),
            timestamp: Date.now()
          };

          // Page context: document title + page/viewport dimensions. The
          // backend promotes page_title onto the session and surfaces the
          // dimensions in the currentpage summary.
          try {
            metrics.page_title = document.title || '';
            var docEl = document.documentElement || {};
            metrics.viewport_width = window.innerWidth || docEl.clientWidth || 0;
            metrics.viewport_height = window.innerHeight || docEl.clientHeight || 0;
            metrics.page_width = docEl.scrollWidth || 0;
            metrics.page_height = docEl.scrollHeight || 0;
          } catch (e) {
            // Defensive: never let page-context capture break perf reporting.
          }

          // Paint timing (if available)
          try {
            if (perf.getEntriesByType) {
              var paintEntries = perf.getEntriesByType('paint');
              if (paintEntries && paintEntries.length) {
                for (var i = 0; i < paintEntries.length; i++) {
                  var entry = paintEntries[i];
                  if (entry.name === 'first-paint') {
                    metrics.first_paint = Math.round(entry.startTime);
                  } else if (entry.name === 'first-contentful-paint') {
                    metrics.first_contentful_paint = Math.round(entry.startTime);
                  }
                }
              }

              // Resource timing (summary)
              var resources = perf.getEntriesByType('resource');
              if (resources && resources.length > 0) {
                metrics.resources = [];
                var limit = Math.min(50, resources.length);
                for (var j = 0; j < limit; j++) {
                  var r = resources[j];
                  metrics.resources.push({
                    name: r.name || '',
                    duration: Math.round(r.duration || 0),
                    size: r.transferSize || 0
                  });
                }
              }
            }
          } catch (e) {
            reportInternalError('paint_timing_failed', e);
          }

          send('performance', metrics);
        } catch (e) {
          reportInternalError('capturePerformance_failed', e);
        }
      }, 100);
    }

    // ============================================
    // Web Vitals accumulator
    // Buffered PerformanceObservers registered at injection time so audit
    // agents can read window.__devtool_vitals at any point later:
    //   { lcp, cls, inp, longTasks: [...], longTasksCapped }
    // Each observer type is feature-detected independently; unsupported
    // metrics stay null (LCP/INP) or empty (longTasks).
    // ============================================
    var VITALS_LONGTASK_CAP = 50;

    function setupVitalsObservers() {
      var vitals = {
        lcp: null,           // ms — largest-contentful-paint startTime (last candidate)
        cls: null,           // unitless cumulative layout shift
        inp: null,           // ms — worst event-timing duration (INP approximation)
        longTasks: [],       // [{start, duration}] capped at VITALS_LONGTASK_CAP
        longTasksCapped: false
      };
      window.__devtool_vitals = vitals;

      if (typeof PerformanceObserver !== 'function') return;

      try {
        new PerformanceObserver(function(list) {
          var entries = list.getEntries();
          if (entries.length) {
            vitals.lcp = Math.round(entries[entries.length - 1].startTime);
          }
        }).observe({ type: 'largest-contentful-paint', buffered: true });
      } catch (e) { /* unsupported — vitals.lcp stays null */ }

      try {
        var clsObserver = new PerformanceObserver(function(list) {
          var entries = list.getEntries();
          for (var i = 0; i < entries.length; i++) {
            if (!entries[i].hadRecentInput) {
              vitals.cls = (vitals.cls || 0) + entries[i].value;
            }
          }
        });
        clsObserver.observe({ type: 'layout-shift', buffered: true });
        vitals.cls = 0;
      } catch (e) { /* unsupported — vitals.cls stays null */ }

      try {
        new PerformanceObserver(function(list) {
          var entries = list.getEntries();
          for (var i = 0; i < entries.length; i++) {
            var d = Math.round(entries[i].duration);
            if (vitals.inp === null || d > vitals.inp) vitals.inp = d;
          }
        }).observe({ type: 'event', buffered: true, durationThreshold: 40 });
      } catch (e) { /* unsupported — vitals.inp stays null */ }

      try {
        new PerformanceObserver(function(list) {
          var entries = list.getEntries();
          for (var i = 0; i < entries.length; i++) {
            if (vitals.longTasks.length >= VITALS_LONGTASK_CAP) {
              vitals.longTasksCapped = true;
              break;
            }
            vitals.longTasks.push({
              start: Math.round(entries[i].startTime),
              duration: Math.round(entries[i].duration)
            });
          }
        }).observe({ type: 'longtask', buffered: true });
      } catch (e) { /* unsupported — vitals.longTasks stays empty */ }
    }

    // ============================================
    // Diagnostics Stream
    // Track proxy connection events for debugging
    // ============================================
    var MAX_DIAG_ENTRIES = 200;
    var diagnosticsBuffer = [];
    var diagnosticsListeners = [];

    function addDiagnostic(category, event, data) {
      var entry = {
        timestamp: Date.now(),
        category: category,  // 'websocket', 'send', 'receive', 'proxy'
        event: event,
        data: data || {}
      };

      diagnosticsBuffer.push(entry);
      if (diagnosticsBuffer.length > MAX_DIAG_ENTRIES) {
        diagnosticsBuffer.shift();
      }

      // Notify listeners
      for (var i = 0; i < diagnosticsListeners.length; i++) {
        try {
          diagnosticsListeners[i](entry);
        } catch (e) {
          // Don't let listener errors break diagnostics
        }
      }

      // Also log to console in debug mode
      if (window.__devtool_debug) {
        console.log('[DevTool Diag]', category + ':' + event, data);
      }
    }

    // reconnectDelay is the wait before reconnect attempt n (1-based):
    // 1s, 2s, 4s, ... capped at RECONNECT_MAX_MS.
    function reconnectDelay(attempt) {
      var exp = Math.min(Math.max(attempt, 1) - 1, 16);
      return Math.min(RECONNECT_BASE_MS * Math.pow(2, exp), RECONNECT_MAX_MS);
    }

    // scheduleReconnect arms exactly one retry timer. There is no attempt
    // limit; see RECONNECT_MAX_MS.
    function scheduleReconnect() {
      if (reconnectTimer) return;
      reconnectAttempts++;
      var delay = reconnectDelay(reconnectAttempts);
      addDiagnostic('websocket', 'reconnect_scheduled', { delay: delay, attempt: reconnectAttempts });
      // Say it once per outage. Retries continue for as long as the outage
      // does, and a line per attempt would bury the page's own console.
      if (reconnectAttempts === 1) {
        nativeConsoleLog('[DevTool] Metrics connection lost - retrying (backoff up to ' + (RECONNECT_MAX_MS / 1000) + 's)');
      }
      reconnectTimer = setTimeout(function() {
        reconnectTimer = null;
        connect();
      }, delay);
    }

    // rearmTransport reconnects immediately when the page has a reason to
    // believe the network is back (it came online, or its tab became visible
    // again after timers were throttled). No-op while a socket is open or
    // already connecting.
    function rearmTransport(reason) {
      try {
        if (!transportWanted || !hasWebSocket) return;
        if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
        addDiagnostic('websocket', 'rearm', { reason: reason });
        reconnectAttempts = 0;
        connect();
      } catch (e) {
        reportInternalError('rearm_failed', e);
      }
    }

    // WebSocket connection. Every handler is bound to the socket it was
    // installed on and ignores events from a socket that is no longer the
    // current one: closing an old socket delivers its close event later, and
    // acting on it would schedule a reconnect that tears down the healthy
    // replacement — once a second, forever.
    function connect() {
      if (!hasWebSocket) {
        addDiagnostic('websocket', 'unavailable', { reason: 'WebSocket not supported' });
        return;
      }

      try {
        if (reconnectTimer) {
          clearTimeout(reconnectTimer);
          reconnectTimer = null;
        }

        if (ws) {
          var old = ws;
          ws = null;
          try {
            // Detach before closing so the retired socket cannot call back.
            old.onopen = old.onmessage = old.onclose = old.onerror = null;
            old.close();
          } catch (e) {
            // Ignore close errors
          }
        }

        addDiagnostic('websocket', 'connecting', { url: WS_URL, attempt: reconnectAttempts + 1 });

        var sock = new WebSocket(WS_URL);
        ws = sock;

        sock.onopen = function() {
          if (sock !== ws) return;
          try {
            addDiagnostic('websocket', 'connected', { url: WS_URL, readyState: sock.readyState });
            nativeConsoleLog('[DevTool] Metrics connection established');
            reconnectAttempts = 0;
            sendPageLoad();
            notifyConnectionState();
          } catch (e) {
            reportInternalError('onopen_handler_failed', e);
          }
        };

        sock.onmessage = function(event) {
          if (sock !== ws) return;
          try {
            if (!event || !event.data) return;

            addDiagnostic('websocket', 'message_received', {
              size: event.data.length,
              preview: event.data.substring(0, 100)
            });

            var message = null;
            try {
              message = JSON.parse(event.data);
            } catch (e) {
              addDiagnostic('websocket', 'parse_error', { error: e.toString(), data: event.data.substring(0, 200) });
              reportInternalError('message_parse_failed', e);
              return;
            }

            if (message) {
              handleServerMessage(message);
            }
          } catch (e) {
            reportInternalError('onmessage_handler_failed', e);
          }
        };

        sock.onclose = function(event) {
          if (sock !== ws) return;
          try {
            addDiagnostic('websocket', 'closed', {
              code: event && event.code,
              reason: event && event.reason,
              wasClean: event && event.wasClean,
              reconnectAttempts: reconnectAttempts
            });
            scheduleReconnect();
            // After the retry timer is armed, so listeners see "connecting"
            // rather than a spurious "disconnected" flicker.
            notifyConnectionState();
          } catch (e) {
            reportInternalError('onclose_handler_failed', e);
          }
        };

        sock.onerror = function(err) {
          if (sock !== ws) return;
          try {
            addDiagnostic('websocket', 'error', {
              type: err && err.type,
              message: (err && err.message) || 'WebSocket error',
              readyState: sock.readyState
            });
            // Native ref and once per outage: routed through the overridden
            // console.error this would land in the PAGE's error buffers, once
            // per retry, for as long as the proxy is down.
            if (reconnectAttempts === 0) {
              nativeConsoleError('[DevTool] Metrics connection error:', err);
            }
            notifyConnectionState();
          } catch (e) {
            // Ignore - error handler itself failed
          }
        };

      } catch (e) {
        addDiagnostic('websocket', 'creation_failed', { error: e.toString() });
        reportInternalError('websocket_creation_failed', e);
        // A constructor that throws must not end the retry loop either.
        scheduleReconnect();
      }
    }

    // Enhanced send with diagnostics
    var originalSend = send;
    send = function(type, data) {
      var result = originalSend(type, data);

      addDiagnostic('send', result ? 'success' : 'failed', {
        type: type,
        connected: ws && ws.readyState === WebSocket.OPEN,
        readyState: ws ? ws.readyState : -1
      });

      return result;
    };

    // Initialize. Two concerns gate separately under the always-wrap model
    // (docs/responsive-canonical-target.md §5):
    //
    //   * Error/mutation INSTRUMENTATION is the canonical telemetry context —
    //     exactly one per page, in the content frame. The chrome shell and any
    //     passive foreign frames must NOT install error tracking (double
    //     reporting).
    //   * The control TRANSPORT (this WebSocket) is what drives the indicator
    //     status dot (core.isConnected) and the panel send button
    //     (core.send('panel_message', …)). Those UI surfaces live in the chrome
    //     shell (see injector.go: proxy UI runtime lives in the shell), so the
    //     shell needs its own WebSocket even though it reports no telemetry.
    //
    // Role is resolved by frames.js (loads first) into
    // the frame-context adapter; standalone pages resolve to content.
    // content path so non-wrapped pages keep full instrumentation.
    try {
      var __frameRole = frameContext.role;
      var __isContent = (!__frameRole || __frameRole === 'content');
      var __wantsTransport = __isContent || __frameRole === 'chrome';
      if (__isContent) {
        setupErrorTrackingWithBuffer();
        // Vitals are page telemetry — content frame only, same as error
        // instrumentation (the chrome shell reports no page metrics).
        try {
          setupVitalsObservers();
        } catch (e) {
          reportInternalError('vitals_setup_failed', e);
        }
      }
      if (__wantsTransport) {
        addDiagnostic('core', 'initializing', { version: window.__devtool_version || 'unknown', role: __frameRole || 'standalone' });
        transportWanted = true;
        connect();
        // Re-arm hooks: reconnect at once when the network returns or the tab
        // is shown again, instead of waiting out the current backoff delay.
        try {
          if (typeof window.addEventListener === 'function') {
            window.addEventListener('online', function() { rearmTransport('online'); });
          }
          if (typeof document.addEventListener === 'function') {
            document.addEventListener('visibilitychange', function() {
              if (document.visibilityState === 'visible') { rearmTransport('visible'); }
            });
          }
        } catch (e) {
          reportInternalError('rearm_hooks_failed', e);
        }
      } else {
        addDiagnostic('core', 'telemetry_suppressed', { role: __frameRole });
      }
    } catch (e) {
      addDiagnostic('core', 'init_failed', { error: e.toString() });
      reportInternalError('initialization_failed', e);
    }

    // Export for other modules - with existence checks
    try {
      // Drag shield: while an overlay element is being dragged in the outer
      // document, a pointer that crosses an <iframe> (e.g. the always-wrap
      // content frame) is captured by the iframe and the outer document stops
      // receiving mousemove — the drag "halts" even with the button held.
      // Disabling pointer-events on all iframes for the duration of the drag
      // keeps the events flowing to the outer document. Ref-counted so nested
      // or concurrent drags don't restore early.
      var dragShieldDepth = 0;
      var dragShieldSaved = [];
      function beginDragShield() {
        dragShieldDepth++;
        if (dragShieldDepth > 1) return;
        dragShieldSaved = [];
        try {
          var frames = document.getElementsByTagName('iframe');
          for (var i = 0; i < frames.length; i++) {
            dragShieldSaved.push([frames[i], frames[i].style.pointerEvents]);
            frames[i].style.pointerEvents = 'none';
          }
        } catch (e) { /* defensive */ }
      }
      function endDragShield() {
        if (dragShieldDepth === 0) return;
        dragShieldDepth--;
        if (dragShieldDepth > 0) return;
        try {
          for (var i = 0; i < dragShieldSaved.length; i++) {
            dragShieldSaved[i][0].style.pointerEvents = dragShieldSaved[i][1] || '';
          }
        } catch (e) { /* defensive */ }
        dragShieldSaved = [];
      }

      if (!window.__devtool_core) {
        window.__devtool_core = {
          beginDragShield: beginDragShield,
          endDragShield: endDragShield,
          send: send,
          sendBinary: sendBinary,
          onMessage: onMessage,
          onConnected: onConnected,
          ws: function() { return ws; },
          isConnected: function() {
            try {
              return ws && ws.readyState === WebSocket.OPEN;
            } catch (e) {
              return false;
            }
          },
          // 'connected' | 'connecting' | 'disconnected' — three-way state for
          // UI that must distinguish "retrying" from "given up".
          connectionState: connectionState,
          // Drop the current socket and connect again now.
          reconnect: function() {
            reconnectAttempts = 0;
            addDiagnostic('websocket', 'manual_reconnect', {});
            connect();
          },
          getSessionId: getOrCreateSessionId,
          reportError: reportInternalError
        };
      }
    } catch (e) {
      console.error('[DevTool] Failed to export core API:', e);
    }

    // Export the TRANSPORT diagnostics API (WebSocket connect/send/receive
    // history). It has its own global: window.__devtool_diagnostics belongs to
    // diagnostics.js (the CSS visual-debugging overlays), which is a different
    // API and used to overwrite this one wherever both modules loaded.
    try {
      if (!window.__devtool_transport_diag) {
        window.__devtool_transport_diag = {
          // Get all diagnostic events
          getHistory: function() {
            return diagnosticsBuffer.slice();
          },

          // Get recent events (last N)
          getRecent: function(count) {
            count = count || 20;
            return diagnosticsBuffer.slice(-count);
          },

          // Filter by category
          getByCategory: function(category) {
            return diagnosticsBuffer.filter(function(e) { return e.category === category; });
          },

          // Subscribe to new events
          subscribe: function(callback) {
            if (typeof callback === 'function') {
              diagnosticsListeners.push(callback);
              return function unsubscribe() {
                var idx = diagnosticsListeners.indexOf(callback);
                if (idx > -1) diagnosticsListeners.splice(idx, 1);
              };
            }
            return function() {};
          },

          // Get connection state
          getConnectionState: function() {
            var states = ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED'];
            return {
              connected: ws && ws.readyState === WebSocket.OPEN,
              readyState: ws ? ws.readyState : -1,
              readyStateLabel: ws ? states[ws.readyState] || 'UNKNOWN' : 'NO_SOCKET',
              reconnectAttempts: reconnectAttempts,
              // null: there is no attempt limit (see RECONNECT_MAX_MS).
              maxReconnectAttempts: null,
              reconnectPending: !!reconnectTimer,
              reconnectMaxDelayMs: RECONNECT_MAX_MS,
              wsUrl: WS_URL
            };
          },

          // Clear history
          clear: function() {
            diagnosticsBuffer = [];
          },

          // Get stats
          getStats: function() {
            var stats = { total: diagnosticsBuffer.length, byCategory: {}, byEvent: {} };
            diagnosticsBuffer.forEach(function(e) {
              stats.byCategory[e.category] = (stats.byCategory[e.category] || 0) + 1;
              var key = e.category + ':' + e.event;
              stats.byEvent[key] = (stats.byEvent[key] || 0) + 1;
            });
            return stats;
          },

          // Enable/disable debug logging
          setDebug: function(enabled) {
            window.__devtool_debug = !!enabled;
          },

          // Force reconnect
          reconnect: function() {
            reconnectAttempts = 0;
            addDiagnostic('websocket', 'manual_reconnect', {});
            connect();
          },

          // Log a custom diagnostic
          log: function(category, event, data) {
            addDiagnostic(category || 'custom', event || 'log', data);
          }
        };
      }
    } catch (e) {
      console.error('[DevTool] Failed to export transport diagnostics API:', e);
    }

    // Export error tracking API for diagnostics panel
    try {
      // Shell-side ingest sink for errors forwarded by content frames. Exported
      // unconditionally (every frame) — only the shell is ever called, by its
      // content child via the frame-context adapter's shell export. See
      // forwardErrorToShell / ingestForwardedError above.
      window.__devtool_errors_ingest = ingestForwardedError;
      if (!window.__devtool_errors) {
        window.__devtool_errors = {
          getJSErrors: function() {
            return jsErrorBuffer.slice();
          },
          getConsoleErrors: function() {
            return consoleErrorBuffer.slice();
          },
          getConsoleWarnings: function() {
            return consoleWarningBuffer.slice();
          },
          getAllErrors: function() {
            return {
              jsErrors: jsErrorBuffer.slice(),
              consoleErrors: consoleErrorBuffer.slice(),
              consoleWarnings: consoleWarningBuffer.slice()
            };
          },
          getDeduplicatedErrors: function() {
            return {
              jsErrors: getDeduplicatedErrors(jsErrorBuffer),
              consoleErrors: getDeduplicatedErrors(consoleErrorBuffer),
              consoleWarnings: getDeduplicatedErrors(consoleWarningBuffer)
            };
          },
          clear: function() {
            jsErrorBuffer = [];
            consoleErrorBuffer = [];
            consoleWarningBuffer = [];
          },
          getStats: function() {
            return {
              jsErrorCount: jsErrorBuffer.length,
              consoleErrorCount: consoleErrorBuffer.length,
              consoleWarningCount: consoleWarningBuffer.length,
              totalCount: jsErrorBuffer.length + consoleErrorBuffer.length + consoleWarningBuffer.length
            };
          },

          // Consolidated error stream - all errors from all sources
          // Sources: proxy, js, console, http, process
          getConsolidated: function() {
            return consolidatedErrorBuffer.slice();
          },

          getConsolidatedRecent: function(count) {
            count = count || 20;
            return consolidatedErrorBuffer.slice(-count);
          },

          getConsolidatedBySource: function(source) {
            return consolidatedErrorBuffer.filter(function(e) {
              return e.source === source;
            });
          },

          getConsolidatedByLevel: function(level) {
            return consolidatedErrorBuffer.filter(function(e) {
              return e.level === level;
            });
          },

          // Subscribe to new consolidated errors
          subscribeConsolidated: function(callback) {
            if (typeof callback === 'function') {
              consolidatedErrorListeners.push(callback);
              return function unsubscribe() {
                var idx = consolidatedErrorListeners.indexOf(callback);
                if (idx > -1) consolidatedErrorListeners.splice(idx, 1);
              };
            }
            return function() {};
          },

          clearConsolidated: function() {
            consolidatedErrorBuffer = [];
          },

          getConsolidatedStats: function() {
            var stats = {
              total: consolidatedErrorBuffer.length,
              bySource: {},
              byLevel: {}
            };
            consolidatedErrorBuffer.forEach(function(e) {
              stats.bySource[e.source] = (stats.bySource[e.source] || 0) + 1;
              stats.byLevel[e.level] = (stats.byLevel[e.level] || 0) + 1;
            });
            return stats;
          },

          // Add custom error to consolidated stream (for external integrations)
          addError: function(source, level, message, data) {
            addConsolidatedError({
              source: source || 'custom',
              level: level || 'error',
              event: 'custom_error',
              message: message || '',
              url: safeGetUrl(),
              data: data || {}
            });
          }
        };
      }
    } catch (e) {
      console.error('[DevTool] Failed to export errors API:', e);
    }

  } catch (e) {
    // Top-level failure - log and abort
    console.error('[DevTool] Core module initialization failed:', e);
  }
})();

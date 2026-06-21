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
    var MAX_RECONNECT_ATTEMPTS = 5;
    var reconnectTimer = null;

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

    // Error reporting to server
    function reportInternalError(context, error) {
      try {
        console.error('[DevTool][INTERNAL]', context, error);

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
            frame_id: window.__devtool_frame_id || ''
          }));
        }
      } catch (e) {
        // Last resort - just console
        console.error('[DevTool] Error reporting failed:', e);
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
        console.error('[DevTool] WebSocket send failed:', e);
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

    // WebSocket connection
    function connect() {
      if (!hasWebSocket) {
        console.warn('[DevTool] WebSocket not available - skipping connection');
        return;
      }

      try {
        // Clear any existing reconnect timer
        if (reconnectTimer) {
          clearTimeout(reconnectTimer);
          reconnectTimer = null;
        }

        // Close existing connection if any
        if (ws) {
          try {
            ws.close();
          } catch (e) {
            // Ignore close errors
          }
        }

        ws = new WebSocket(WS_URL);

        ws.onopen = function() {
          try {
            console.log('[DevTool] Metrics connection established');
            reconnectAttempts = 0;
            sendPageLoad();
            connectedCallbacks.forEach(function(cb) { try { cb(); } catch(e) {} });
          } catch (e) {
            reportInternalError('onopen_handler_failed', e);
          }
        };

        ws.onmessage = function(event) {
          try {
            if (!event || !event.data) return;

            var message = null;
            try {
              message = JSON.parse(event.data);
            } catch (e) {
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

        ws.onclose = function() {
          try {
            console.log('[DevTool] Metrics connection closed');

            if (reconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
              reconnectAttempts++;
              var delay = 1000 * reconnectAttempts;
              console.log('[DevTool] Reconnecting in ' + delay + 'ms (attempt ' + reconnectAttempts + ')');
              reconnectTimer = setTimeout(connect, delay);
            } else {
              console.warn('[DevTool] Max reconnection attempts reached');
            }
          } catch (e) {
            reportInternalError('onclose_handler_failed', e);
          }
        };

        ws.onerror = function(err) {
          try {
            console.error('[DevTool] Metrics connection error:', err);
          } catch (e) {
            // Ignore - error handler itself failed
          }
        };

      } catch (e) {
        reportInternalError('websocket_creation_failed', e);
      }
    }

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
          var myFrame = window.__devtool_frame_id || '';
          // '@chrome' is a role token addressing the outer shell regardless of its
          // id, so the agent can run a REPL in the outer frame ("outer" target)
          // without knowing the per-page shell id.
          var myRole = window.__devtool_frame_role || window.__devtool_role || '';
          var match = targetFrame === '' ||
            targetFrame === myFrame ||
            (targetFrame === '@chrome' && myRole === 'chrome');
          if (match) {
            executeJavaScript(message.id, message.code);
          }
        }

        // Handle proxy diagnostics from server
        if (message.type === 'proxy_diagnostic' && message.payload) {
          var diag = message.payload;
          // Add to diagnostics buffer so it appears in __devtool_diagnostics
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

    // Register a callback for WebSocket connection events
    var connectedCallbacks = [];
    function onConnected(cb) { connectedCallbacks.push(cb); }

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
              // Check for explicit pretty-print request
              var pretty = val && val.__pretty === true;
              if (pretty) {
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
          frame_id: window.__devtool_frame_id || ''
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
            var message = Array.prototype.slice.call(arguments).map(function(arg) {
              if (typeof arg === 'object') {
                try {
                  return JSON.stringify(arg);
                } catch (e) {
                  return String(arg);
                }
              }
              return String(arg);
            }).join(' ');

            var entry = {
              message: message,
              timestamp: Date.now(),
              source: 'console.error',
              lineno: 0,
              colno: 0,
              stack: ''
            };

            addToBuffer(consoleErrorBuffer, entry);

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
              stack: '',
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
            var message = Array.prototype.slice.call(arguments).map(function(arg) {
              if (typeof arg === 'object') {
                try {
                  return JSON.stringify(arg);
                } catch (e) {
                  return String(arg);
                }
              }
              return String(arg);
            }).join(' ');

            var entry = {
              message: message,
              timestamp: Date.now(),
              source: 'console.warn',
              lineno: 0,
              colno: 0,
              stack: ''
            };

            addToBuffer(consoleWarningBuffer, entry);

            // Add to consolidated error stream
            addConsolidatedError({
              source: 'console',
              level: 'warning',
              event: 'console_warning',
              message: message,
              url: safeGetUrl()
            });
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
      if (window.__devtool_frame_role === 'chrome') { return; }
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

    // Wrap WebSocket connection with diagnostics
    function connectWithDiagnostics() {
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
          try {
            ws.close();
          } catch (e) {
            // Ignore close errors
          }
        }

        addDiagnostic('websocket', 'connecting', { url: WS_URL, attempt: reconnectAttempts + 1 });

        ws = new WebSocket(WS_URL);

        ws.onopen = function() {
          try {
            addDiagnostic('websocket', 'connected', { url: WS_URL, readyState: ws.readyState });
            console.log('[DevTool] Metrics connection established');
            reconnectAttempts = 0;
            sendPageLoad();
          } catch (e) {
            reportInternalError('onopen_handler_failed', e);
          }
        };

        ws.onmessage = function(event) {
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

        ws.onclose = function(event) {
          try {
            addDiagnostic('websocket', 'closed', {
              code: event.code,
              reason: event.reason,
              wasClean: event.wasClean,
              reconnectAttempts: reconnectAttempts
            });
            console.log('[DevTool] Metrics connection closed');

            if (reconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
              reconnectAttempts++;
              var delay = 1000 * reconnectAttempts;
              addDiagnostic('websocket', 'reconnect_scheduled', { delay: delay, attempt: reconnectAttempts });
              console.log('[DevTool] Reconnecting in ' + delay + 'ms (attempt ' + reconnectAttempts + ')');
              reconnectTimer = setTimeout(connectWithDiagnostics, delay);
            } else {
              addDiagnostic('websocket', 'reconnect_exhausted', { maxAttempts: MAX_RECONNECT_ATTEMPTS });
              console.warn('[DevTool] Max reconnection attempts reached');
            }
          } catch (e) {
            reportInternalError('onclose_handler_failed', e);
          }
        };

        ws.onerror = function(err) {
          try {
            addDiagnostic('websocket', 'error', {
              type: err.type,
              message: err.message || 'WebSocket error',
              readyState: ws ? ws.readyState : -1
            });
            console.error('[DevTool] Metrics connection error:', err);
          } catch (e) {
            // Ignore - error handler itself failed
          }
        };

      } catch (e) {
        addDiagnostic('websocket', 'creation_failed', { error: e.toString() });
        reportInternalError('websocket_creation_failed', e);
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

    // Replace connect with diagnostics version
    connect = connectWithDiagnostics;

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
    // window.__devtool_frame_role; absent (legacy/standalone) we default to the
    // content path so non-wrapped pages keep full instrumentation.
    try {
      var __frameRole = window.__devtool_frame_role;
      var __isContent = (!__frameRole || __frameRole === 'content');
      var __wantsTransport = __isContent || __frameRole === 'chrome';
      if (__isContent) {
        setupErrorTrackingWithBuffer();
      }
      if (__wantsTransport) {
        addDiagnostic('core', 'initializing', { version: window.__devtool_version || 'unknown', role: __frameRole || 'standalone' });
        connect();
      } else {
        addDiagnostic('core', 'telemetry_suppressed', { role: __frameRole });
      }
    } catch (e) {
      addDiagnostic('core', 'init_failed', { error: e.toString() });
      reportInternalError('initialization_failed', e);
    }

    // Export for other modules - with existence checks
    try {
      if (!window.__devtool_core) {
        window.__devtool_core = {
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
          getSessionId: getOrCreateSessionId,
          reportError: reportInternalError
        };
      }
    } catch (e) {
      console.error('[DevTool] Failed to export core API:', e);
    }

    // Export diagnostics API
    try {
      if (!window.__devtool_diagnostics) {
        window.__devtool_diagnostics = {
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
              maxReconnectAttempts: MAX_RECONNECT_ATTEMPTS,
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
      console.error('[DevTool] Failed to export diagnostics API:', e);
    }

    // Export error tracking API for diagnostics panel
    try {
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

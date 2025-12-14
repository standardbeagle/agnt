// Core DevTool instrumentation module
// Handles WebSocket connection, messaging, error/performance tracking

(function() {
  'use strict';

  // Configuration
  // Use relative WebSocket URL to automatically match the current connection
  var protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  var WS_URL = protocol + '//' + window.location.host + '/__devtool_metrics';
  var ws = null;
  var reconnectAttempts = 0;
  var MAX_RECONNECT_ATTEMPTS = 5;

  // Session ID - unique per browser tab/window, persists across page navigations
  // Uses a combination of cookie (for proxy visibility) and sessionStorage (for tab isolation)
  var COOKIE_NAME = '__devtool_sid';
  var SESSION_STORAGE_KEY = '__devtool_session_id';
  var sessionId = null;

  function getCookie(name) {
    var cookies = document.cookie.split(';');
    for (var i = 0; i < cookies.length; i++) {
      var cookie = cookies[i].trim();
      if (cookie.indexOf(name + '=') === 0) {
        return cookie.substring(name.length + 1);
      }
    }
    return null;
  }

  function setCookie(name, value) {
    // Set cookie with path=/ so it's sent on all requests
    // No expiry = session cookie (cleared when browser closes)
    document.cookie = name + '=' + value + '; path=/; SameSite=Lax';
  }

  function getOrCreateSessionId() {
    if (sessionId) return sessionId;

    try {
      // First check sessionStorage for tab-specific ID
      sessionId = sessionStorage.getItem(SESSION_STORAGE_KEY);
      if (!sessionId) {
        // Generate a unique session ID: timestamp + random
        sessionId = 'sess-' + Date.now().toString(36) + '-' + Math.random().toString(36).substr(2, 9);
        sessionStorage.setItem(SESSION_STORAGE_KEY, sessionId);
      }
      // Always sync to cookie so proxy can see it on HTTP requests
      setCookie(COOKIE_NAME, sessionId);
    } catch (e) {
      // sessionStorage not available (private mode, etc)
      // Fall back to cookie-only (shared across tabs but still works)
      sessionId = getCookie(COOKIE_NAME);
      if (!sessionId) {
        sessionId = 'sess-' + Date.now().toString(36) + '-' + Math.random().toString(36).substr(2, 9);
        setCookie(COOKIE_NAME, sessionId);
      }
    }
    return sessionId;
  }

  // Initialize session ID immediately
  getOrCreateSessionId();

  // WebSocket connection
  function connect() {
    try {
      ws = new WebSocket(WS_URL);

      ws.onopen = function() {
        console.log('[DevTool] Metrics connection established');
        reconnectAttempts = 0;
        sendPageLoad();
      };

      ws.onmessage = function(event) {
        try {
          var message = JSON.parse(event.data);
          handleServerMessage(message);
        } catch (err) {
          console.error('[DevTool] Failed to parse server message:', err);
        }
      };

      ws.onclose = function() {
        console.log('[DevTool] Metrics connection closed');
        if (reconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
          reconnectAttempts++;
          setTimeout(connect, 1000 * reconnectAttempts);
        }
      };

      ws.onerror = function(err) {
        console.error('[DevTool] Metrics connection error:', err);
      };
    } catch (err) {
      console.error('[DevTool] Failed to create WebSocket:', err);
    }
  }

  // Message handlers for other modules
  var messageHandlers = [];

  // Handle messages from server
  function handleServerMessage(message) {
    if (message.type === 'execute') {
      executeJavaScript(message.id, message.code);
    }

    // Notify registered handlers
    for (var i = 0; i < messageHandlers.length; i++) {
      try {
        messageHandlers[i](message);
      } catch (err) {
        console.error('[DevTool] Message handler error:', err);
      }
    }
  }

  // Register a message handler
  function onMessage(handler) {
    if (typeof handler === 'function') {
      messageHandlers.push(handler);
    }
  }

  // Execute JavaScript sent from server
  function executeJavaScript(execId, code) {
    var startTime = performance.now();

    function sendResult(result, error) {
      var duration = performance.now() - startTime;
      send('execution', {
        exec_id: execId,
        result: result || '',
        error: error || '',
        duration: duration,
        timestamp: Date.now()
      });
    }

    function formatResult(val) {
      if (val === undefined) {
        return 'undefined';
      } else if (val === null) {
        return 'null';
      } else if (typeof val === 'function') {
        return val.toString();
      } else if (typeof val === 'object') {
        try {
          return JSON.stringify(val, null, 2);
        } catch (e) {
          return String(val);
        }
      } else {
        return String(val);
      }
    }

    try {
      var result = eval(code);

      // Check if result is a Promise
      if (result && typeof result.then === 'function') {
        // Handle Promise - wait for it to resolve
        result.then(function(resolved) {
          sendResult(formatResult(resolved), '');
        }).catch(function(err) {
          var error = err.toString();
          if (err.stack) {
            error += '\n' + err.stack;
          }
          sendResult('', error);
        });
      } else {
        // Synchronous result
        sendResult(formatResult(result), '');
      }
    } catch (err) {
      var error = err.toString();
      if (err.stack) {
        error += '\n' + err.stack;
      }
      sendResult('', error);
    }
  }

  /**
   * Send a message to the DevTool server via WebSocket.
   *
   * The message is JSON-encoded with the following envelope:
   * {type: string, data: object, url: string, session_id: string}
   *
   * Supported message types (server.go must handle these):
   *
   * @param {string} type - Message type. One of:
   *
   *   **Automatic/System Messages:**
   *   - "error" - JavaScript errors captured by window.onerror
   *     data: {message, source, lineno, colno, error, stack, timestamp}
   *   - "performance" - Page load performance metrics
   *     data: {navigation_start, dom_content_loaded, load_event_end, dom_interactive,
   *            dom_complete, first_paint?, first_contentful_paint?, resources?, timestamp}
   *   - "execution" - Response to server-initiated JavaScript execution
   *     data: {exec_id, result, error, duration, timestamp}
   *
   *   **User-Initiated Messages (via __devtool API):**
   *   - "custom_log" - Custom log messages from __devtool.log()
   *     data: {message, level, extra?, timestamp}
   *   - "screenshot" - Screenshot capture from __devtool.screenshot()
   *     data: {name, image, timestamp}
   *
   *   **Interaction/Mutation Tracking:**
   *   - "interactions" - User interaction events (clicks, keyboard, scroll)
   *     data: {events: [...], timestamp}
   *   - "mutations" - DOM mutation records
   *     data: {mutations: [...], timestamp}
   *
   *   **Indicator Panel Messages:**
   *   - "panel_message" - Text message from floating indicator panel
   *     data: {message, timestamp}
   *     -> Forwarded to overlay, typed into PTY
   *
   *   **Sketch Mode Messages:**
   *   - "sketch" - Sketch/wireframe saved from sketch mode
   *     data: {json, image, timestamp}
   *     -> Forwarded to overlay, typed into PTY
   *
   *   **Capture Messages (from indicator panel):**
   *   - "screenshot_capture" - Screenshot with optional selection area
   *     data: {name, selection?, image, timestamp}
   *   - "element_capture" - Selected element info
   *     data: {selector, info, timestamp}
   *   - "sketch_capture" - Sketch saved via indicator panel
   *     data: {json, image, timestamp}
   *
   *   **Design Iteration Messages:**
   *   - "design_state" - Element selected for design iteration
   *     data: {selector, xpath, original_html, context_html, url, metadata}
   *     -> Forwarded to overlay, typed into PTY
   *   - "design_request" - Request for design alternatives
   *     data: {prompt, selector, timestamp}
   *     -> Forwarded to overlay, typed into PTY
   *   - "design_chat" - Chat message during design iteration
   *     data: {message, selector, timestamp}
   *     -> Forwarded to overlay, typed into PTY
   *
   *   **Voice Messages:**
   *   - "voice_start" - Voice recording started
   *     data: {timestamp}
   *   - "voice_stop" - Voice recording stopped
   *     data: {timestamp}
   *
   * @param {object} data - Message payload (structure depends on type)
   */
  function send(type, data) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      try {
        ws.send(JSON.stringify({
          type: type,
          data: data,
          url: window.location.href,
          session_id: getOrCreateSessionId()
        }));
      } catch (err) {
        console.error('[DevTool] Failed to send metric:', err);
      }
    }
  }

  // Send binary data to server (for audio streaming)
  function sendBinary(data) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      try {
        ws.send(data);
      } catch (err) {
        console.error('[DevTool] Failed to send binary data:', err);
      }
    }
  }

  // Error tracking
  window.addEventListener('error', function(event) {
    send('error', {
      message: event.message,
      source: event.filename,
      lineno: event.lineno,
      colno: event.colno,
      error: event.error ? event.error.toString() : '',
      stack: event.error ? event.error.stack : '',
      timestamp: Date.now()
    });
  });

  // Promise rejection tracking
  window.addEventListener('unhandledrejection', function(event) {
    send('error', {
      message: 'Unhandled Promise Rejection: ' + event.reason,
      source: '',
      lineno: 0,
      colno: 0,
      error: event.reason ? event.reason.toString() : '',
      stack: event.reason && event.reason.stack ? event.reason.stack : '',
      timestamp: Date.now()
    });
  });

  // Performance tracking
  function sendPageLoad() {
    // Wait for load event
    if (document.readyState === 'complete') {
      capturePerformance();
    } else {
      window.addEventListener('load', capturePerformance);
    }
  }

  function capturePerformance() {
    // Use setTimeout to ensure all metrics are available
    setTimeout(function() {
      try {
        var perf = window.performance;
        if (!perf || !perf.timing) return;

        var timing = perf.timing;

        var metrics = {
          navigation_start: timing.navigationStart,
          dom_content_loaded: timing.domContentLoadedEventEnd - timing.navigationStart,
          load_event_end: timing.loadEventEnd - timing.navigationStart,
          dom_interactive: timing.domInteractive - timing.navigationStart,
          dom_complete: timing.domComplete - timing.navigationStart,
          timestamp: Date.now()
        };

        // Paint timing (if available)
        if (perf.getEntriesByType) {
          var paintEntries = perf.getEntriesByType('paint');
          paintEntries.forEach(function(entry) {
            if (entry.name === 'first-paint') {
              metrics.first_paint = Math.round(entry.startTime);
            } else if (entry.name === 'first-contentful-paint') {
              metrics.first_contentful_paint = Math.round(entry.startTime);
            }
          });

          // Resource timing (summary)
          var resources = perf.getEntriesByType('resource');
          if (resources && resources.length > 0) {
            metrics.resources = resources.slice(0, 50).map(function(r) {
              return {
                name: r.name,
                duration: Math.round(r.duration),
                size: r.transferSize || 0
              };
            });
          }
        }

        send('performance', metrics);
      } catch (err) {
        console.error('[DevTool] Failed to capture performance:', err);
      }
    }, 100);
  }

  // Initialize connection
  connect();

  // Export for other modules
  window.__devtool_core = {
    send: send,
    sendBinary: sendBinary,
    onMessage: onMessage,
    ws: function() { return ws; },
    isConnected: function() {
      return ws && ws.readyState === WebSocket.OPEN;
    },
    getSessionId: getOrCreateSessionId
  };
})();

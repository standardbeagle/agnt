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
  // Uses sessionStorage so each tab gets its own ID
  var SESSION_STORAGE_KEY = '__devtool_session_id';
  var sessionId = null;

  function getOrCreateSessionId() {
    if (sessionId) return sessionId;

    try {
      sessionId = sessionStorage.getItem(SESSION_STORAGE_KEY);
      if (!sessionId) {
        // Generate a unique session ID: timestamp + random
        sessionId = 'sess-' + Date.now().toString(36) + '-' + Math.random().toString(36).substr(2, 9);
        sessionStorage.setItem(SESSION_STORAGE_KEY, sessionId);
      }
    } catch (e) {
      // sessionStorage not available (private mode, etc)
      // Fall back to in-memory ID (won't persist across navigations)
      sessionId = 'sess-' + Date.now().toString(36) + '-' + Math.random().toString(36).substr(2, 9);
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

  // Handle messages from server
  function handleServerMessage(message) {
    if (message.type === 'execute') {
      executeJavaScript(message.id, message.code);
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

  // Send metric to server
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
    ws: function() { return ws; },
    isConnected: function() {
      return ws && ws.readyState === WebSocket.OPEN;
    },
    getSessionId: getOrCreateSessionId
  };
})();

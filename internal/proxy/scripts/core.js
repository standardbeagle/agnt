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
        var random = Math.random().toString(36).substr(2, 9);
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
            session_id: getOrCreateSessionId()
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
        // Handle execution requests
        if (message.type === 'execute' && message.code) {
          executeJavaScript(message.id, message.code);
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
          session_id: getOrCreateSessionId()
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

    // Initialize
    try {
      setupErrorTrackingWithBuffer();
      connect();
    } catch (e) {
      reportInternalError('initialization_failed', e);
    }

    // Export for other modules - with existence checks
    try {
      if (!window.__devtool_core) {
        window.__devtool_core = {
          send: send,
          sendBinary: sendBinary,
          onMessage: onMessage,
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

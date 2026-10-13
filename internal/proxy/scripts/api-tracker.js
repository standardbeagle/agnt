/**
 * API Call Tracking Module
 * Intercepts fetch and XMLHttpRequest to track API calls
 */

(function() {
  'use strict';

  var frameContext = window.__devtool_context;

  var MAX_ENTRIES = 100;
  var callBuffer = [];
  var originalFetch = window.fetch;
  var originalXHROpen = XMLHttpRequest.prototype.open;
  var originalXHRSend = XMLHttpRequest.prototype.send;

  /**
   * Add call to buffer (circular)
   */
  function addCall(call) {
    callBuffer.push(call);
    if (callBuffer.length > MAX_ENTRIES) {
      callBuffer.shift();
    }
    // The proxy renders a top-level navigation as a chrome shell wrapping the
    // real page in a content <iframe>. The indicator UI runs in the shell, but
    // fetch/XHR happen in the content frame — so the shell's own buffer is
    // empty. Forward each captured call up to the shell (same-origin direct
    // reach; the proxy serves both frames) so its Network tab + badge update.
    // Best-effort; never let a forwarding failure break capture.
    if (frameContext.isContent()) {
      try {
        var ingest = frameContext.shellExport('__devtool_api_ingest');
        if (typeof ingest === 'function') {
          ingest(call);
        }
      } catch (e) { /* cross-origin / shell gone */ }
    }
  }

  /**
   * Truncate tokens from URLs for privacy
   */
  function sanitizeUrl(url) {
    try {
      // Resolve against the document URL, not the origin: a relative "items"
      // requested from /app/list/ is /app/list/items, not /items.
      var urlObj = new URL(String(url), window.location.href);
      var params = urlObj.searchParams;

      // Truncate sensitive parameters
      var sensitiveParams = ['token', 'api_key', 'apikey', 'key', 'secret', 'password', 'auth'];
      sensitiveParams.forEach(function(param) {
        if (params.has(param)) {
          params.set(param, '...');
        }
      });

      return urlObj.toString();
    } catch (e) {
      return String(url);
    }
  }

  // The interceptors below observe; they must never change what the page's
  // own call does. Every piece of bookkeeping is wrapped so a surprise input
  // (a URL object, a Request, a non-string method, no argument at all) costs
  // at most a missing or partial record — the native call always runs with
  // the page's original arguments and its result is returned untouched.

  // fetchTarget reads the URL a fetch() input names: a string, a URL (or any
  // object whose string form is the URL), or a Request (.url).
  function fetchTarget(resource) {
    if (typeof resource === 'string') { return resource; }
    if (resource && typeof resource.url === 'string') { return resource.url; }
    return String(resource);
  }

  function recordCall(call) {
    try {
      if (!frameContext.isChrome()) { addCall(call); }
    } catch (e) { /* recording is best-effort */ }
  }

  function describeFetch(resource, options, startTime) {
    try {
      // Request objects carry their own method; explicit options still win.
      var method = (options && options.method) || (resource && resource.method) || 'GET';
      return {
        timestamp: startTime,
        url: sanitizeUrl(fetchTarget(resource)),
        method: String(method).toUpperCase(),
        status: null,
        duration: null,
        ok: null,
        error: null
      };
    } catch (e) {
      return null;
    }
  }

  /**
   * Intercept fetch API
   */
  if (typeof originalFetch === 'function') {
    window.fetch = function(resource, options) {
      var startTime = Date.now();
      var call = describeFetch(resource, options, startTime);

      // The native call decides how a bad input fails (it rejects; it does
      // not throw), so it runs outside every try block of ours.
      var result = originalFetch.apply(this, arguments);
      if (!call || !result || typeof result.then !== 'function') { return result; }

      // Observe on a side branch and return the ORIGINAL promise: the page
      // sees the native resolution/rejection, never one routed through us.
      result.then(function(response) {
        try {
          call.status = response.status;
          call.ok = response.ok;
          call.duration = Date.now() - startTime;
          call.statusText = response.statusText;
          // Capture error response body (first 1KB) for 4xx/5xx
          if (!response.ok && response.status >= 400) {
            response.clone().text().then(function(body) {
              call.responseBody = body.substring(0, 1024);
              // Try to extract error message from JSON
              try {
                var json = JSON.parse(body);
                call.errorMessage = json.error || json.message || json.detail || json.title || null;
              } catch (e) {}
            }).catch(function() {});
          }
        } catch (e) { /* partial record is still worth keeping */ }
        recordCall(call);
      }, function(error) {
        try {
          call.status = 0;
          call.ok = false;
          call.duration = Date.now() - startTime;
          call.error = (error && error.message) || 'Network error';
        } catch (e) { /* partial record is still worth keeping */ }
        recordCall(call);
      });

      return result;
    };
  }

  /**
   * Intercept XMLHttpRequest
   */
  XMLHttpRequest.prototype.open = function(method, url) {
    try {
      this.__devtool_api = {
        method: String(method).toUpperCase(),
        url: sanitizeUrl(url),
        startTime: null
      };
    } catch (e) {
      this.__devtool_api = null;
    }
    return originalXHROpen.apply(this, arguments);
  };

  XMLHttpRequest.prototype.send = function() {
    var xhr = this;

    try {
      if (xhr.__devtool_api) {
        var meta = xhr.__devtool_api;
        meta.startTime = Date.now();

        xhr.addEventListener('loadend', function() {
          try {
            var call = {
              timestamp: meta.startTime,
              url: meta.url,
              method: meta.method,
              status: xhr.status,
              statusText: xhr.statusText,
              duration: Date.now() - meta.startTime,
              ok: xhr.status >= 200 && xhr.status < 300,
              error: xhr.status === 0 ? 'Network error' : null
            };
            // Capture error response body (first 1KB) for 4xx/5xx
            if (xhr.status >= 400) {
              try {
                var body = xhr.responseText || '';
                call.responseBody = body.substring(0, 1024);
                try {
                  var json = JSON.parse(body);
                  call.errorMessage = json.error || json.message || json.detail || json.title || null;
                } catch (e) {}
              } catch (e) {}
            }
            recordCall(call);
          } catch (e) { /* recording is best-effort */ }
        });
      }
    } catch (e) { /* never let bookkeeping stop the request */ }

    return originalXHRSend.apply(this, arguments);
  };

  /**
   * Get all API calls
   */
  function getCalls() {
    return callBuffer.slice();
  }

  /**
   * Get failed calls (4xx/5xx status codes)
   */
  function getFailedCalls() {
    return callBuffer.filter(function(call) {
      return call.status >= 400 || call.status === 0;
    });
  }

  /**
   * Get slow calls (above threshold)
   */
  function getSlowCalls(threshold) {
    threshold = threshold || 2000; // Default 2 seconds
    return callBuffer.filter(function(call) {
      return call.duration && call.duration >= threshold;
    });
  }

  /**
   * Get repeated calls (same URL called multiple times)
   */
  function getRepeatedCalls(windowMs, minCount) {
    windowMs = windowMs || 30000; // Default 30 seconds
    minCount = minCount || 3; // Default 3 times

    var now = Date.now();
    var recentCalls = callBuffer.filter(function(call) {
      return (now - call.timestamp) <= windowMs;
    });

    // Group by method + URL
    var groups = {};
    recentCalls.forEach(function(call) {
      var key = call.method + ' ' + call.url;
      if (!groups[key]) {
        groups[key] = {
          url: call.url,
          method: call.method,
          count: 0,
          calls: [],
          totalDuration: 0
        };
      }
      groups[key].count++;
      groups[key].calls.push(call);
      groups[key].totalDuration += call.duration || 0;
    });

    // Filter groups with count >= minCount
    var repeated = [];
    for (var key in groups) {
      if (groups[key].count >= minCount) {
        groups[key].avgDuration = Math.round(groups[key].totalDuration / groups[key].count);
        repeated.push(groups[key]);
      }
    }

    // Sort by count (most repeated first)
    repeated.sort(function(a, b) {
      return b.count - a.count;
    });

    return repeated;
  }

  /**
   * Get statistics
   */
  function getStats() {
    var total = callBuffer.length;
    var failed = getFailedCalls().length;
    var slow = getSlowCalls(2000).length;
    var repeated = getRepeatedCalls(30000, 3);

    var totalDuration = 0;
    var successfulCalls = 0;
    callBuffer.forEach(function(call) {
      if (call.duration) {
        totalDuration += call.duration;
      }
      if (call.ok) {
        successfulCalls++;
      }
    });

    return {
      total: total,
      failed: failed,
      slow: slow,
      repeated: repeated.length,
      avgDuration: successfulCalls > 0 ? Math.round(totalDuration / successfulCalls) : 0,
      successRate: total > 0 ? Math.round((successfulCalls / total) * 100) : 100
    };
  }

  /**
   * Get calls by status code range
   */
  function getCallsByStatus(minStatus, maxStatus) {
    return callBuffer.filter(function(call) {
      return call.status >= minStatus && call.status <= maxStatus;
    });
  }

  /**
   * Get recent calls (last N seconds)
   */
  function getRecentCalls(seconds) {
    seconds = seconds || 60;
    var cutoff = Date.now() - (seconds * 1000);
    return callBuffer.filter(function(call) {
      return call.timestamp >= cutoff;
    });
  }

  /**
   * Clear all tracked calls
   */
  function clear() {
    callBuffer = [];
  }

  /**
   * Get deduplicated error summary
   */
  function getErrorSummary() {
    var failedCalls = getFailedCalls();
    var errors = {};

    failedCalls.forEach(function(call) {
      var key = call.method + ' ' + call.url + ' [' + call.status + ']';
      if (!errors[key]) {
        errors[key] = {
          method: call.method,
          url: call.url,
          status: call.status,
          error: call.error,
          count: 0,
          firstSeen: call.timestamp,
          lastSeen: call.timestamp,
          examples: []
        };
      }
      errors[key].count++;
      errors[key].lastSeen = call.timestamp;
      if (errors[key].examples.length < 3) {
        errors[key].examples.push({
          timestamp: call.timestamp,
          duration: call.duration
        });
      }
    });

    // Convert to array and sort by count
    var summary = [];
    for (var key in errors) {
      summary.push(errors[key]);
    }
    summary.sort(function(a, b) {
      return b.count - a.count;
    });

    return summary;
  }

  /**
   * Get sparkline data: request counts bucketed by second over a rolling window.
   * Returns { buckets: number[], window: number, maxBucket: number }
   */
  function getSparklineData(windowSec) {
    windowSec = windowSec || 60;
    var now = Date.now();
    var buckets = new Array(windowSec);
    var i;
    for (i = 0; i < windowSec; i++) {
      buckets[i] = 0;
    }
    var maxBucket = 0;
    for (i = 0; i < callBuffer.length; i++) {
      var call = callBuffer[i];
      var age = now - call.timestamp;
      if (age < 0 || age >= windowSec * 1000) continue;
      var idx = Math.floor(age / 1000);
      buckets[windowSec - 1 - idx]++;
    }
    for (i = 0; i < windowSec; i++) {
      if (buckets[i] > maxBucket) maxBucket = buckets[i];
    }
    return { buckets: buckets, window: windowSec, maxBucket: maxBucket };
  }

  // Shell-side ingest: content frames forward their captured calls here (see
  // addCall) because the indicator runs in the chrome shell — a separate window
  // from the content frame where fetch/XHR actually happen. Terminal sink: does
  // not re-forward (the shell has no parent shell), so no loop. Defined in every
  // frame for simplicity; only ever invoked on the shell by its content child.
  window.__devtool_api_ingest = function(call) {
    if (!call) { return; }
    callBuffer.push(call);
    if (callBuffer.length > MAX_ENTRIES) {
      callBuffer.shift();
    }
  };

  // Export API
  window.__devtool_api = {
    getCalls: getCalls,
    getFailedCalls: getFailedCalls,
    getSlowCalls: getSlowCalls,
    getRepeatedCalls: getRepeatedCalls,
    getStats: getStats,
    getCallsByStatus: getCallsByStatus,
    getRecentCalls: getRecentCalls,
    getErrorSummary: getErrorSummary,
    getSparklineData: getSparklineData,
    clear: clear
  };
})();

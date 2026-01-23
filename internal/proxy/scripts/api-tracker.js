/**
 * API Call Tracking Module
 * Intercepts fetch and XMLHttpRequest to track API calls
 */

(function() {
  'use strict';

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
  }

  /**
   * Truncate tokens from URLs for privacy
   */
  function sanitizeUrl(url) {
    try {
      var urlObj = new URL(url, window.location.origin);
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
      return url;
    }
  }

  /**
   * Intercept fetch API
   */
  window.fetch = function(resource, options) {
    var url = typeof resource === 'string' ? resource : resource.url;
    var method = (options && options.method) || 'GET';
    var startTime = Date.now();

    var call = {
      timestamp: startTime,
      url: sanitizeUrl(url),
      method: method.toUpperCase(),
      status: null,
      duration: null,
      ok: null,
      error: null
    };

    return originalFetch.apply(this, arguments)
      .then(function(response) {
        call.status = response.status;
        call.ok = response.ok;
        call.duration = Date.now() - startTime;
        addCall(call);
        return response;
      })
      .catch(function(error) {
        call.status = 0;
        call.ok = false;
        call.duration = Date.now() - startTime;
        call.error = error.message || 'Network error';
        addCall(call);
        throw error;
      });
  };

  /**
   * Intercept XMLHttpRequest
   */
  XMLHttpRequest.prototype.open = function(method, url) {
    this.__devtool_api = {
      method: method.toUpperCase(),
      url: sanitizeUrl(url),
      startTime: null
    };
    return originalXHROpen.apply(this, arguments);
  };

  XMLHttpRequest.prototype.send = function() {
    var xhr = this;

    if (!xhr.__devtool_api) {
      return originalXHRSend.apply(this, arguments);
    }

    xhr.__devtool_api.startTime = Date.now();

    var onLoadEnd = function() {
      var call = {
        timestamp: xhr.__devtool_api.startTime,
        url: xhr.__devtool_api.url,
        method: xhr.__devtool_api.method,
        status: xhr.status,
        duration: Date.now() - xhr.__devtool_api.startTime,
        ok: xhr.status >= 200 && xhr.status < 300,
        error: xhr.status === 0 ? 'Network error' : null
      };
      addCall(call);
    };

    xhr.addEventListener('loadend', onLoadEnd);

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
    clear: clear
  };
})();

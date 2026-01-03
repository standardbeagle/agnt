// Store API for DevTool
// Provides persistent key-value storage with multiple scopes

(function() {
  'use strict';

  var core = window.__devtool_core;

  // Pending store requests (request_id -> {resolve, reject, timeout})
  var pendingRequests = {};
  var requestIdCounter = 0;

  // Scope constants
  var SCOPE_GLOBAL = 'global';
  var SCOPE_FOLDER = 'folder';
  var SCOPE_PAGE = 'page';

  // Generate unique request ID
  function generateRequestId() {
    return 'store_' + Date.now().toString(36) + '_' + (++requestIdCounter);
  }

  // Helper: Get current URL (origin + pathname)
  function getCurrentUrl() {
    return window.location.origin + window.location.pathname;
  }

  // Helper: Extract folder path from URL
  // e.g., "https://example.com/app/users/123" -> "https://example.com/app/users"
  function getFolderKey(url) {
    if (!url) url = getCurrentUrl();

    try {
      var urlObj = new URL(url);
      var pathname = urlObj.pathname;

      // Remove trailing slash
      if (pathname.endsWith('/')) {
        pathname = pathname.slice(0, -1);
      }

      // Get folder path (remove last segment)
      var lastSlash = pathname.lastIndexOf('/');
      var folderPath = lastSlash > 0 ? pathname.slice(0, lastSlash) : '';

      return urlObj.origin + folderPath;
    } catch (e) {
      console.error('[DevTool] Invalid URL for folder key:', url, e);
      return url; // Fallback to full URL
    }
  }

  // Send store request and return promise
  function sendStoreRequest(action, params) {
    return new Promise(function(resolve, reject) {
      var requestId = generateRequestId();
      var timeoutMs = 10000; // 10 second timeout

      // Set up timeout
      var timeout = setTimeout(function() {
        delete pendingRequests[requestId];
        reject(new Error('Store request timed out'));
      }, timeoutMs);

      // Store pending request
      pendingRequests[requestId] = {
        resolve: resolve,
        reject: reject,
        timeout: timeout
      };

      // Send request via WebSocket
      core.send('store_request', {
        request_id: requestId,
        action: action,
        params: params || {}
      });
    });
  }

  // Handle store response from server
  function handleStoreResponse(message) {
    if (message.type !== 'store_response') return;

    var data = message.data || message;
    var requestId = data.request_id;
    var pending = pendingRequests[requestId];

    if (!pending) {
      console.warn('[DevTool] Received store response for unknown request:', requestId);
      return;
    }

    // Clear timeout and remove from pending
    clearTimeout(pending.timeout);
    delete pendingRequests[requestId];

    // Resolve or reject based on response
    if (data.error) {
      pending.reject(new Error(data.error));
    } else {
      pending.resolve(data.result);
    }
  }

  // Register message handler for responses
  if (core && core.onMessage) {
    core.onMessage(handleStoreResponse);
  }

  // Core store methods
  function get(key, options) {
    if (!key) {
      return Promise.reject(new Error('Key is required'));
    }
    return sendStoreRequest('get', {
      key: key,
      scope: (options && options.scope) || undefined,
      scope_key: (options && options.scopeKey) || undefined
    });
  }

  function set(key, value, options) {
    if (!key) {
      return Promise.reject(new Error('Key is required'));
    }
    return sendStoreRequest('set', {
      key: key,
      value: value,
      scope: (options && options.scope) || undefined,
      scope_key: (options && options.scopeKey) || undefined,
      metadata: (options && options.metadata) || undefined
    });
  }

  function deleteKey(key, options) {
    if (!key) {
      return Promise.reject(new Error('Key is required'));
    }
    return sendStoreRequest('delete', {
      key: key,
      scope: (options && options.scope) || undefined,
      scope_key: (options && options.scopeKey) || undefined
    });
  }

  function list(options) {
    return sendStoreRequest('list', {
      scope: (options && options.scope) || undefined,
      scope_key: (options && options.scopeKey) || undefined
    });
  }

  function getAll(options) {
    return sendStoreRequest('getAll', {
      scope: (options && options.scope) || undefined,
      scope_key: (options && options.scopeKey) || undefined
    });
  }

  function clear(options) {
    return sendStoreRequest('clear', {
      scope: (options && options.scope) || undefined,
      scope_key: (options && options.scopeKey) || undefined
    });
  }

  // Create scope-specific namespaces
  var globalScope = {
    get: function(key) {
      return get(key, { scope: SCOPE_GLOBAL });
    },
    set: function(key, value, metadata) {
      return set(key, value, { scope: SCOPE_GLOBAL, metadata: metadata });
    },
    delete: function(key) {
      return deleteKey(key, { scope: SCOPE_GLOBAL });
    },
    list: function() {
      return list({ scope: SCOPE_GLOBAL });
    },
    getAll: function() {
      return getAll({ scope: SCOPE_GLOBAL });
    },
    clear: function() {
      return clear({ scope: SCOPE_GLOBAL });
    }
  };

  var folderScope = {
    get: function(key, url) {
      return get(key, { scope: SCOPE_FOLDER, scopeKey: getFolderKey(url) });
    },
    set: function(key, value, url, metadata) {
      return set(key, value, { scope: SCOPE_FOLDER, scopeKey: getFolderKey(url), metadata: metadata });
    },
    delete: function(key, url) {
      return deleteKey(key, { scope: SCOPE_FOLDER, scopeKey: getFolderKey(url) });
    },
    list: function(url) {
      return list({ scope: SCOPE_FOLDER, scopeKey: getFolderKey(url) });
    },
    getAll: function(url) {
      return getAll({ scope: SCOPE_FOLDER, scopeKey: getFolderKey(url) });
    },
    clear: function(url) {
      return clear({ scope: SCOPE_FOLDER, scopeKey: getFolderKey(url) });
    }
  };

  var pageScope = {
    get: function(key, url) {
      return get(key, { scope: SCOPE_PAGE, scopeKey: url || getCurrentUrl() });
    },
    set: function(key, value, url, metadata) {
      return set(key, value, { scope: SCOPE_PAGE, scopeKey: url || getCurrentUrl(), metadata: metadata });
    },
    delete: function(key, url) {
      return deleteKey(key, { scope: SCOPE_PAGE, scopeKey: url || getCurrentUrl() });
    },
    list: function(url) {
      return list({ scope: SCOPE_PAGE, scopeKey: url || getCurrentUrl() });
    },
    getAll: function(url) {
      return getAll({ scope: SCOPE_PAGE, scopeKey: url || getCurrentUrl() });
    },
    clear: function(url) {
      return clear({ scope: SCOPE_PAGE, scopeKey: url || getCurrentUrl() });
    }
  };

  // Main store API
  var store = {
    // Scope constants
    SCOPE_GLOBAL: SCOPE_GLOBAL,
    SCOPE_FOLDER: SCOPE_FOLDER,
    SCOPE_PAGE: SCOPE_PAGE,

    // Core methods
    get: get,
    set: set,
    delete: deleteKey,
    list: list,
    getAll: getAll,
    clear: clear,

    // Convenience namespaces
    global: globalScope,
    folder: folderScope,
    page: pageScope,

    // Helper utilities (exposed for testing/debugging)
    getCurrentUrl: getCurrentUrl,
    getFolderKey: getFolderKey
  };

  // Export to global scope
  window.__devtool_store = store;

  console.log('[DevTool] Store API available at window.__devtool_store');
})();

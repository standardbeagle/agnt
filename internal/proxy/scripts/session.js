// Session Management API for DevTool
// Allows browser-based interaction with agnt run sessions.
//
// Scoped by the server to the project this proxy belongs to: a session code or
// task id from another project is answered "not found", and with no session
// running for the project every call rejects. Refused on an untrusted
// connection (for example one that arrived through an unauthenticated tunnel).

(function() {
  'use strict';

  var core = window.__devtool_core;

  // Pending session requests (request_id -> {resolve, reject, timeout})
  var pendingRequests = {};
  var requestIdCounter = 0;

  // Generate unique request ID
  function generateRequestId() {
    return 'session_' + Date.now().toString(36) + '_' + (++requestIdCounter);
  }

  // Send session request and return promise
  function sendSessionRequest(action, params) {
    return new Promise(function(resolve, reject) {
      var requestId = generateRequestId();
      var timeoutMs = 10000; // 10 second timeout

      // Set up timeout
      var timeout = setTimeout(function() {
        delete pendingRequests[requestId];
        reject(new Error('Session request timed out'));
      }, timeoutMs);

      // Store pending request
      pendingRequests[requestId] = {
        resolve: resolve,
        reject: reject,
        timeout: timeout
      };

      // Send request via WebSocket
      core.send('session_request', {
        request_id: requestId,
        action: action,
        params: params || {}
      });
    });
  }

  // Handle session response from server
  function handleSessionResponse(message) {
    if (message.type !== 'session_response') return;

    var data = message.data || message;
    var requestId = data.request_id;
    var pending = pendingRequests[requestId];

    if (!pending) {
      console.warn('[DevTool] Received session response for unknown request:', requestId);
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
    core.onMessage(handleSessionResponse);
  }

  // Session API
  var session = {
    /**
     * List the sessions running in this proxy's project. The server pins the
     * scope to that project; the page cannot widen it.
     * @param {boolean} [global=false] - Kept for callers that pass it. `true`
     *   is refused by the server (the promise rejects): there is no
     *   cross-project listing from the browser.
     * @returns {Promise<Object>} - Sessions list with count
     */
    list: function(global) {
      return sendSessionRequest('list', { global: !!global });
    },

    /**
     * Get details for a specific session
     * @param {string} code - Session code
     * @returns {Promise<Object>} - Session details
     */
    get: function(code) {
      if (!code) {
        return Promise.reject(new Error('Session code is required'));
      }
      return sendSessionRequest('get', { code: code });
    },

    /**
     * Send a message to a session immediately
     * @param {string} code - Session code
     * @param {string} message - Message to send
     * @returns {Promise<Object>} - Result with success status
     */
    send: function(code, message) {
      if (!code) {
        return Promise.reject(new Error('Session code is required'));
      }
      if (!message) {
        return Promise.reject(new Error('Message is required'));
      }
      return sendSessionRequest('send', { code: code, message: message });
    },

    /**
     * Schedule a message for future delivery
     * @param {string} code - Session code
     * @param {string} duration - Duration string (e.g., "5m", "1h30m")
     * @param {string} message - Message to schedule
     * @returns {Promise<Object>} - Result with task_id and deliver_at
     */
    schedule: function(code, duration, message) {
      if (!code) {
        return Promise.reject(new Error('Session code is required'));
      }
      if (!duration) {
        return Promise.reject(new Error('Duration is required (e.g., "5m", "1h30m")'));
      }
      if (!message) {
        return Promise.reject(new Error('Message is required'));
      }
      return sendSessionRequest('schedule', {
        code: code,
        duration: duration,
        message: message
      });
    },

    /**
     * List the scheduled tasks of this proxy's project.
     * @param {boolean} [global=false] - Kept for callers that pass it. `true`
     *   is refused by the server (the promise rejects).
     * @returns {Promise<Object>} - Tasks list with count
     */
    tasks: function(global) {
      return sendSessionRequest('tasks', { global: !!global });
    },

    /**
     * Cancel a scheduled task
     * @param {string} taskId - Task ID to cancel
     * @returns {Promise<Object>} - Result with success status
     */
    cancel: function(taskId) {
      if (!taskId) {
        return Promise.reject(new Error('Task ID is required'));
      }
      return sendSessionRequest('cancel', { task_id: taskId });
    }
  };

  // Export to global scope
  window.__devtool_session = session;

  console.log('[DevTool] Session API available at window.__devtool_session');
})();

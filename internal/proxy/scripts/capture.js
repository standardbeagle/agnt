// State capture primitives for DevTool
// Capture DOM, styles, storage, and network state

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  function captureDOM() {
    try {
      var html = utils.passwordSafeOuterHTML(document.documentElement).html;
      var hash = 0;
      for (var i = 0; i < html.length; i++) {
        var char = html.charCodeAt(i);
        hash = ((hash << 5) - hash) + char;
        hash = hash & hash;
      }

      return {
        snapshot: html,
        hash: hash.toString(16),
        timestamp: Date.now(),
        url: window.location.href,
        size: html.length
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function captureStyles(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);
      var inline = el.style.cssText;

      var computedObj = {};
      for (var i = 0; i < computed.length; i++) {
        var prop = computed[i];
        computedObj[prop] = computed.getPropertyValue(prop);
      }

      return {
        selector: utils.generateSelector(el),
        computed: computedObj,
        inline: inline,
        timestamp: Date.now()
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function captureState(keys) {
    try {
      var state = {
        timestamp: Date.now(),
        url: window.location.href
      };

      if (!keys || keys.indexOf('localStorage') !== -1) {
        state.localStorage = {};
        try {
          for (var i = 0; i < localStorage.length; i++) {
            var key = localStorage.key(i);
            state.localStorage[key] = localStorage.getItem(key);
          }
        } catch (e) {
          state.localStorage = { error: 'Access denied' };
        }
      }

      if (!keys || keys.indexOf('sessionStorage') !== -1) {
        state.sessionStorage = {};
        try {
          for (var j = 0; j < sessionStorage.length; j++) {
            var skey = sessionStorage.key(j);
            state.sessionStorage[skey] = sessionStorage.getItem(skey);
          }
        } catch (e) {
          state.sessionStorage = { error: 'Access denied' };
        }
      }

      if (!keys || keys.indexOf('cookies') !== -1) {
        state.cookies = document.cookie;
      }

      return state;
    } catch (e) {
      return { error: e.message };
    }
  }

  function captureNetwork() {
    try {
      var resources = [];

      if (window.performance && window.performance.getEntriesByType) {
        var entries = window.performance.getEntriesByType('resource');

        for (var i = 0; i < entries.length; i++) {
          var entry = entries[i];
          resources.push({
            name: entry.name,
            type: entry.initiatorType,
            duration: Math.round(entry.duration),
            size: entry.transferSize || 0,
            startTime: Math.round(entry.startTime)
          });
        }
      }

      return {
        resources: resources,
        count: resources.length,
        timestamp: Date.now()
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  // Export capture functions
  window.__devtool_capture = {
    captureDOM: captureDOM,
    captureStyles: captureStyles,
    captureState: captureState,
    captureNetwork: captureNetwork
  };
})();

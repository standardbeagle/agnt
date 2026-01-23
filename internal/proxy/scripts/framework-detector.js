/**
 * Framework Detection Module
 * Detects frontend frameworks and their versions
 */

(function() {
  'use strict';

  var frameworkCache = null;
  var detectionTimestamp = null;

  /**
   * Detect React framework
   */
  function detectReact() {
    // Check for React global
    if (window.React) {
      return {
        name: 'React',
        version: window.React.version || 'unknown',
        confidence: 'high'
      };
    }

    // Check for React DevTools
    if (window.__REACT_DEVTOOLS_GLOBAL_HOOK__) {
      return {
        name: 'React',
        version: 'unknown',
        confidence: 'medium'
      };
    }

    // Check for React DOM markers
    var reactRoot = document.querySelector('[data-reactroot], [data-reactid]');
    if (reactRoot) {
      return {
        name: 'React',
        version: 'unknown',
        confidence: 'medium'
      };
    }

    // Check for React fiber nodes
    var elements = document.querySelectorAll('*');
    for (var i = 0; i < Math.min(elements.length, 50); i++) {
      var keys = Object.keys(elements[i]);
      for (var j = 0; j < keys.length; j++) {
        if (keys[j].startsWith('__react')) {
          return {
            name: 'React',
            version: 'unknown',
            confidence: 'low'
          };
        }
      }
    }

    return null;
  }

  /**
   * Detect Vue framework
   */
  function detectVue() {
    // Check for Vue global
    if (window.Vue) {
      return {
        name: 'Vue',
        version: window.Vue.version || 'unknown',
        confidence: 'high'
      };
    }

    // Check for Vue DevTools
    if (window.__VUE_DEVTOOLS_GLOBAL_HOOK__) {
      return {
        name: 'Vue',
        version: 'unknown',
        confidence: 'medium'
      };
    }

    // Check for Vue DOM markers
    var vueElement = document.querySelector('[data-v-], [v-cloak]');
    if (vueElement) {
      return {
        name: 'Vue',
        version: 'unknown',
        confidence: 'medium'
      };
    }

    // Check for __vue__ property
    var elements = document.querySelectorAll('*');
    for (var i = 0; i < Math.min(elements.length, 50); i++) {
      if (elements[i].__vue__ || elements[i].__vue_app__) {
        return {
          name: 'Vue',
          version: 'unknown',
          confidence: 'low'
        };
      }
    }

    return null;
  }

  /**
   * Detect Angular framework
   */
  function detectAngular() {
    // Check for Angular global
    if (window.ng) {
      return {
        name: 'Angular',
        version: 'unknown',
        confidence: 'high'
      };
    }

    // Check for Angular DOM markers
    var ngApp = document.querySelector('[ng-app], [ng-version], [ng-controller]');
    if (ngApp) {
      var version = ngApp.getAttribute('ng-version');
      return {
        name: 'Angular',
        version: version || 'unknown',
        confidence: 'high'
      };
    }

    // Check for AngularJS markers
    var ngElement = document.querySelector('[ng-bind], [ng-model], [ng-repeat]');
    if (ngElement) {
      return {
        name: 'AngularJS',
        version: 'unknown',
        confidence: 'medium'
      };
    }

    // Check for Angular attributes
    var elements = document.querySelectorAll('*');
    for (var i = 0; i < Math.min(elements.length, 50); i++) {
      var attrs = elements[i].attributes;
      for (var j = 0; j < attrs.length; j++) {
        if (attrs[j].name.startsWith('_ng')) {
          return {
            name: 'Angular',
            version: 'unknown',
            confidence: 'low'
          };
        }
      }
    }

    return null;
  }

  /**
   * Detect Svelte framework
   */
  function detectSvelte() {
    // Check for Svelte global
    if (window.__SVELTE__) {
      return {
        name: 'Svelte',
        version: 'unknown',
        confidence: 'high'
      };
    }

    // Check for Svelte DOM markers
    var svelteElement = document.querySelector('[data-svelte-h]');
    if (svelteElement) {
      return {
        name: 'Svelte',
        version: 'unknown',
        confidence: 'medium'
      };
    }

    // Check for svelte-* classes
    var elements = document.querySelectorAll('[class*="svelte-"]');
    if (elements.length > 0) {
      return {
        name: 'Svelte',
        version: 'unknown',
        confidence: 'low'
      };
    }

    return null;
  }

  /**
   * Main framework detection function
   */
  function detect() {
    // Return cached result if detected within last 5 seconds
    if (frameworkCache && detectionTimestamp && (Date.now() - detectionTimestamp < 5000)) {
      return frameworkCache;
    }

    // Try each framework in order
    var result = detectReact() || detectVue() || detectAngular() || detectSvelte();

    if (result) {
      frameworkCache = result;
      detectionTimestamp = Date.now();
      return result;
    }

    // Default to vanilla JS
    var vanillaResult = {
      name: 'Vanilla JS',
      version: 'n/a',
      confidence: 'low'
    };

    frameworkCache = vanillaResult;
    detectionTimestamp = Date.now();
    return vanillaResult;
  }

  /**
   * Clear detection cache (useful for SPA navigation)
   */
  function clearCache() {
    frameworkCache = null;
    detectionTimestamp = null;
  }

  // Export API
  window.__devtool_framework = {
    detect: detect,
    clearCache: clearCache
  };
})();

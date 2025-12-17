// DevTool API assembly
// Combines all modules into the window.__devtool global API

(function() {
  'use strict';

  var core = window.__devtool_core;
  var utils = window.__devtool_utils;
  var overlay = window.__devtool_overlay;
  var inspection = window.__devtool_inspection;
  var tree = window.__devtool_tree;
  var visual = window.__devtool_visual;
  var layout = window.__devtool_layout;
  var interactive = window.__devtool_interactive;
  var capture = window.__devtool_capture;
  var accessibility = window.__devtool_accessibility;
  var audit = window.__devtool_audit;
  var interactions = window.__devtool_interactions;
  var mutations = window.__devtool_mutations;
  var voice = window.__devtool_voice;
  var indicator = window.__devtool_indicator;
  var sketch = window.__devtool_sketch;
  var design = window.__devtool_design;
  var diagnostics = window.__devtool_diagnostics;
  var session = window.__devtool_session;

  // Main DevTool API
  window.__devtool = {
    // ========================================================================
    // LOGGING API
    // ========================================================================

    log: function(message, level, data) {
      level = level || 'info';
      core.send('custom_log', {
        level: level,
        message: String(message),
        data: data || {},
        timestamp: Date.now()
      });
    },

    debug: function(message, data) {
      this.log(message, 'debug', data);
    },

    info: function(message, data) {
      this.log(message, 'info', data);
    },

    warn: function(message, data) {
      this.log(message, 'warn', data);
    },

    error: function(message, data) {
      this.log(message, 'error', data);
    },

    // ========================================================================
    // ELEMENT INSPECTION
    // ========================================================================

    getElementInfo: inspection.getElementInfo,
    getPosition: inspection.getPosition,
    getComputed: inspection.getComputed,
    getBox: inspection.getBox,
    getLayout: inspection.getLayout,
    getContainer: inspection.getContainer,
    getStacking: inspection.getStacking,
    getTransform: inspection.getTransform,
    getOverflow: inspection.getOverflow,

    // ========================================================================
    // TREE WALKING
    // ========================================================================

    walkChildren: tree.walkChildren,
    walkParents: tree.walkParents,
    findAncestor: tree.findAncestor,

    // ========================================================================
    // VISUAL STATE
    // ========================================================================

    isVisible: visual.isVisible,
    isInViewport: visual.isInViewport,
    checkOverlap: visual.checkOverlap,

    // ========================================================================
    // LAYOUT DIAGNOSTICS
    // ========================================================================

    findOverflows: layout.findOverflows,
    findStackingContexts: layout.findStackingContexts,
    findOffscreen: layout.findOffscreen,

    // ========================================================================
    // VISUAL OVERLAYS
    // ========================================================================

    highlight: overlay.highlight,
    removeHighlight: overlay.removeHighlight,
    clearAllOverlays: overlay.clearAllOverlays,

    // ========================================================================
    // INTERACTIVE
    // ========================================================================

    selectElement: interactive.selectElement,
    waitForElement: interactive.waitForElement,
    ask: interactive.ask,
    measureBetween: interactive.measureBetween,

    // ========================================================================
    // STATE CAPTURE
    // ========================================================================

    captureDOM: capture.captureDOM,
    captureStyles: capture.captureStyles,
    captureState: capture.captureState,
    captureNetwork: capture.captureNetwork,

    // ========================================================================
    // ACCESSIBILITY
    // ========================================================================

    getA11yInfo: accessibility.getA11yInfo,
    getContrast: accessibility.getContrast,
    getTabOrder: accessibility.getTabOrder,
    getScreenReaderText: accessibility.getScreenReaderText,
    auditAccessibility: accessibility.auditAccessibility,

    // ========================================================================
    // QUALITY AUDITS
    // ========================================================================

    auditDOMComplexity: audit.auditDOMComplexity,
    auditCSS: audit.auditCSS,
    auditSecurity: audit.auditSecurity,
    auditPageQuality: audit.auditPageQuality,

    // ========================================================================
    // INTERACTION TRACKING (NEW)
    // ========================================================================

    interactions: interactions,

    // ========================================================================
    // MUTATION TRACKING (NEW)
    // ========================================================================

    mutations: mutations,

    // ========================================================================
    // FLOATING INDICATOR
    // ========================================================================

    indicator: {
      show: indicator.show,
      hide: indicator.hide,
      toggle: indicator.toggle,
      togglePanel: indicator.togglePanel,
      destroy: indicator.destroy
    },

    // ========================================================================
    // SKETCH MODE
    // ========================================================================

    sketch: {
      open: sketch.init,
      close: sketch.close,
      toggle: sketch.toggle,
      save: sketch.saveAndSend,
      toJSON: sketch.toJSON,
      fromJSON: sketch.fromJSON,
      toDataURL: sketch.toDataURL,
      setTool: sketch.setTool,
      undo: sketch.undo,
      redo: sketch.redo,
      clear: sketch.clearAll
    },

    // ========================================================================
    // DESIGN ITERATION
    // ========================================================================

    design: {
      start: design.start,
      stop: design.stop,
      selectElement: design.selectElement,
      next: design.next,
      previous: design.previous,
      addAlternative: design.addAlternative,
      applyAlternative: design.applyAlternative,
      chat: design.chat,
      getState: design.getState
    },

    // ========================================================================
    // VOICE TRANSCRIPTION
    // ========================================================================

    voice: {
      init: voice.init,
      start: voice.start,
      stop: voice.stop,
      toggle: voice.toggle,
      setMode: voice.setMode,
      getStatus: voice.getStatus,
      configure: voice.configure,
      isSupported: voice.isSupported
    },

    // ========================================================================
    // SNAPSHOT (VISUAL REGRESSION TESTING)
    // ========================================================================

    snapshot: window.__devtool_snapshot || {
      captureCurrentPage: function() { return Promise.reject(new Error('Snapshot helpers not loaded')); },
      createBaseline: function() { return Promise.reject(new Error('Snapshot helpers not loaded')); },
      compareToBaseline: function() { return Promise.reject(new Error('Snapshot helpers not loaded')); },
      quickBaseline: function() { return Promise.reject(new Error('Snapshot helpers not loaded')); }
    },

    // ========================================================================
    // DIAGNOSTICS (CSS VISUAL DEBUGGING)
    // ========================================================================

    diagnostics: diagnostics || {
      // Structure & Layout
      outlineAll: function() { return { error: 'Diagnostics not loaded' }; },
      showSemanticElements: function() { return { error: 'Diagnostics not loaded' }; },
      showContainers: function() { return { error: 'Diagnostics not loaded' }; },
      showGrid: function() { return { error: 'Diagnostics not loaded' }; },
      showFlexbox: function() { return { error: 'Diagnostics not loaded' }; },
      showGaps: function() { return { error: 'Diagnostics not loaded' }; },
      // Typography
      showTypographyPanel: function() { return { error: 'Diagnostics not loaded' }; },
      highlightInconsistentText: function() { return { error: 'Diagnostics not loaded' }; },
      showTextBounds: function() { return { error: 'Diagnostics not loaded' }; },
      // Stacking & Layering
      showStacking: function() { return { error: 'Diagnostics not loaded' }; },
      opacity: function() { return { error: 'Diagnostics not loaded' }; },
      showPositioned: function() { return { error: 'Diagnostics not loaded' }; },
      // Interactive
      showInteractive: function() { return { error: 'Diagnostics not loaded' }; },
      showFocusOrder: function() { return { error: 'Diagnostics not loaded' }; },
      showClickTargets: function() { return { error: 'Diagnostics not loaded' }; },
      // Responsive
      showViewportInfo: function() { return { error: 'Diagnostics not loaded' }; },
      // Color & Spacing
      showColorPalette: function() { return { error: 'Diagnostics not loaded' }; },
      showSpacingScale: function() { return { error: 'Diagnostics not loaded' }; },
      // DOM Snapshot & Diff
      captureDOMSnapshot: function() { return { error: 'Diagnostics not loaded' }; },
      compareDOMSnapshots: function() { return { error: 'Diagnostics not loaded' }; },
      showDOMDiff: function() { return { error: 'Diagnostics not loaded' }; },
      highlightDOMChanges: function() { return { error: 'Diagnostics not loaded' }; },
      // Control
      clear: function() { return { error: 'Diagnostics not loaded' }; },
      clearAll: function() { return { error: 'Diagnostics not loaded' }; },
      list: function() { return { error: 'Diagnostics not loaded' }; }
    },

    // ========================================================================
    // SESSION MANAGEMENT
    // ========================================================================

    session: session || {
      list: function() { return Promise.reject(new Error('Session module not loaded')); },
      get: function() { return Promise.reject(new Error('Session module not loaded')); },
      send: function() { return Promise.reject(new Error('Session module not loaded')); },
      schedule: function() { return Promise.reject(new Error('Session module not loaded')); },
      tasks: function() { return Promise.reject(new Error('Session module not loaded')); },
      cancel: function() { return Promise.reject(new Error('Session module not loaded')); }
    },

    // ========================================================================
    // COMPOSITE CONVENIENCE FUNCTIONS
    // ========================================================================

    inspect: function(selector) {
      var el = utils.resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      return {
        info: inspection.getElementInfo(selector),
        position: inspection.getPosition(selector),
        box: inspection.getBox(selector),
        layout: inspection.getLayout(selector),
        stacking: inspection.getStacking(selector),
        container: inspection.getContainer(selector),
        visibility: visual.isVisible(selector),
        viewport: visual.isInViewport(selector)
      };
    },

    diagnoseLayout: function(selector) {
      var overflows = layout.findOverflows();
      var contexts = layout.findStackingContexts();
      var offscreen = layout.findOffscreen();

      var result = {
        overflows: overflows,
        stackingContexts: contexts,
        offscreen: offscreen
      };

      if (selector) {
        var el = utils.resolveElement(selector);
        result.element = {
          selector: utils.generateSelector(el),
          stacking: inspection.getStacking(selector)
        };
      }

      return result;
    },

    // ========================================================================
    // CONNECTION STATUS
    // ========================================================================

    isConnected: function() {
      return core.isConnected();
    },

    getStatus: function() {
      var ws = core.ws();
      if (!ws) return 'not_initialized';
      switch (ws.readyState) {
        case WebSocket.CONNECTING: return 'connecting';
        case WebSocket.OPEN: return 'connected';
        case WebSocket.CLOSING: return 'closing';
        case WebSocket.CLOSED: return 'closed';
        default: return 'unknown';
      }
    },

    // ========================================================================
    // SCREENSHOT
    // ========================================================================

    screenshot: function(name, selector) {
      return new Promise(function(resolve, reject) {
        if (typeof html2canvas === 'undefined') {
          reject(new Error('html2canvas not loaded'));
          return;
        }

        // Handle different parameter combinations
        if (typeof name === 'string' && !selector && (name.startsWith('.') || name.startsWith('#') || name.startsWith('['))) {
          selector = name;
          name = null;
        }

        name = name || 'screenshot_' + Date.now();

        var targetElement = document.body;
        if (selector) {
          try {
            targetElement = document.querySelector(selector);
            if (!targetElement) {
              reject(new Error('Element not found: ' + selector));
              return;
            }
          } catch (err) {
            reject(new Error('Invalid selector: ' + selector + ' - ' + err.message));
            return;
          }
        }

        html2canvas(targetElement, {
          allowTaint: true,
          useCORS: true,
          logging: false,
          scrollY: -window.scrollY,
          scrollX: -window.scrollX,
          windowWidth: targetElement === document.body ? document.documentElement.scrollWidth : undefined,
          windowHeight: targetElement === document.body ? document.documentElement.scrollHeight : undefined
        }).then(function(canvas) {
          var dataUrl = canvas.toDataURL('image/png');
          var width = canvas.width;
          var height = canvas.height;

          core.send('screenshot', {
            name: name,
            data: dataUrl,
            width: width,
            height: height,
            format: 'png',
            selector: selector || 'body',
            timestamp: Date.now()
          });

          resolve({
            name: name,
            width: width,
            height: height,
            selector: selector || 'body'
          });
        }).catch(function(err) {
          reject(err);
        });
      });
    },

    // ========================================================================
    // TOAST NOTIFICATIONS
    // ========================================================================

    toast: window.__devtool_toast || {
      show: function() { console.warn('Toast not initialized'); },
      success: function() { console.warn('Toast not initialized'); },
      error: function() { console.warn('Toast not initialized'); },
      warning: function() { console.warn('Toast not initialized'); },
      info: function() { console.warn('Toast not initialized'); },
      dismiss: function() {},
      dismissAll: function() {},
      configure: function() {}
    }
  };

  console.log('[DevTool] API available at window.__devtool');
  console.log('[DevTool] Usage:');
  console.log('  __devtool.log("message", "info", {key: "value"})');
  console.log('  __devtool.screenshot("my-screenshot")');
  console.log('  __devtool.interactions.getLastClickContext()');
  console.log('  __devtool.mutations.highlightRecent(5000)');
  console.log('  __devtool.indicator.toggle() - Toggle floating indicator');
  console.log('  __devtool.sketch.open() - Open sketch mode');
  console.log('  __devtool.design.start() - Start design iteration mode');
  console.log('  __devtool.toast.success("Done!", "Title") - Show toast');
  console.log('  __devtool.diagnostics.outlineAll() - Visual CSS debugging');
  console.log('  __devtool.session.list() - List active sessions');
  console.log('  __devtool.session.schedule("code", "5m", "message") - Schedule a message');
})();

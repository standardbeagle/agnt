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
  var store = window.__devtool_store;
  var content = window.__devtool_content;

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
    // All audit functions support options: {detailLevel: 'summary'|'compact'|'full', maxIssues: 20}
    // ========================================================================

    auditDOMComplexity: audit.auditDOMComplexity,
    auditCSS: audit.auditCSS,
    auditSecurity: audit.auditSecurity,
    auditPageQuality: audit.auditPageQuality,
    auditPerformance: audit.auditPerformance,

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
    // PERSISTENT STORE
    // ========================================================================

    store: store || {
      get: function() { return Promise.reject(new Error('Store module not loaded')); },
      set: function() { return Promise.reject(new Error('Store module not loaded')); },
      delete: function() { return Promise.reject(new Error('Store module not loaded')); },
      list: function() { return Promise.reject(new Error('Store module not loaded')); },
      getAll: function() { return Promise.reject(new Error('Store module not loaded')); },
      clear: function() { return Promise.reject(new Error('Store module not loaded')); },
      global: {},
      folder: {},
      page: {}
    },

    // ========================================================================
    // CONTENT EXTRACTION & INFORMATION ARCHITECTURE
    // ========================================================================

    content: content || {
      extractLinks: function() { return { error: 'Content module not loaded' }; },
      extractNavigation: function() { return { error: 'Content module not loaded' }; },
      extractContent: function() { return { error: 'Content module not loaded' }; },
      extractHeadings: function() { return { error: 'Content module not loaded' }; },
      buildSitemap: function() { return { error: 'Content module not loaded' }; },
      extractStructuredData: function() { return { error: 'Content module not loaded' }; }
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
      var overflowsResult = layout.findOverflows();
      var contextsResult = layout.findStackingContexts();
      var offscreenResult = layout.findOffscreen();

      // Extract the arrays from the result objects
      var result = {
        overflows: overflowsResult.overflows || [],
        stackingContexts: contextsResult.contexts || [],
        offscreen: offscreenResult.offscreen || []
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
    // TEXT FRAGILITY
    // ========================================================================

    /**
     * Check text elements for fragility issues.
     * Analyzes max word length, overflow risks, and layout shift potential.
     */
    checkTextFragility: function() {
      var textFragility = window.__devtool_text_fragility;
      if (textFragility && textFragility.checkTextFragility) {
        return textFragility.checkTextFragility();
      }
      return { error: 'Text fragility module not loaded' };
    },

    // ========================================================================
    // RESPONSIVE RISK
    // ========================================================================

    /**
     * Check elements for responsive design risks.
     * Analyzes fixed dimensions, touch targets, positioning, and viewport issues.
     */
    checkResponsiveRisk: function() {
      var responsiveRisk = window.__devtool_responsive_risk;
      if (responsiveRisk && responsiveRisk.checkResponsiveRisk) {
        return responsiveRisk.checkResponsiveRisk();
      }
      return { error: 'Responsive risk module not loaded' };
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

    /**
     * Take a screenshot of the page or a specific element.
     *
     * @param {string|object} nameOrOptions - Screenshot name or options object
     * @param {string} [selector] - CSS selector for element to capture
     *
     * Options object properties:
     * - name: Screenshot name (default: 'screenshot_<timestamp>')
     * - selector: CSS selector for element to capture
     * - region: Pixel region to capture {x, y, width, height}
     * - fullPage: Capture full page height (default: false for pages > 2000px)
     * - overview: For long pages, capture scaled overview + detail sections (default: true)
     * - maxHeight: Maximum height in pixels before triggering overview mode (default: 2000)
     * - overviewScale: Scale factor for overview image (default: 0.25)
     * - detailHeight: Height of detail sections at top/bottom (default: 600)
     * - quality: JPEG quality 0-1 (default: 0.92, only for JPEG)
     * - format: 'png' or 'jpeg' (default: 'png')
     *
     * For pages > maxHeight, returns:
     * - overview: Scaled full-page image (25% size by default)
     * - top: Full-resolution top section
     * - viewport: Full-resolution current viewport
     * - bottom: Full-resolution bottom section
     *
     * Examples:
     *   __devtool.screenshot()                           // Smart mode (overview for long pages)
     *   __devtool.screenshot('my-shot')                  // Named screenshot
     *   __devtool.screenshot('#header')                  // Capture specific element
     *   __devtool.screenshot({fullPage: true})           // Full page (may be large!)
     *   __devtool.screenshot({overview: false})          // Viewport only, no overview
     *   __devtool.screenshot({selector: '.card', name: 'card'})
     *   __devtool.screenshot({region: {x: 100, y: 200, width: 800, height: 600}})
     */
    screenshot: function(nameOrOptions, selector) {
      return new Promise(function(resolve, reject) {
        if (typeof html2canvas === 'undefined') {
          reject(new Error('html2canvas not loaded'));
          return;
        }

        // Parse options
        var options = {};
        var name = null;

        if (typeof nameOrOptions === 'object' && nameOrOptions !== null) {
          options = nameOrOptions;
          name = options.name;
          selector = options.selector || selector;
        } else if (typeof nameOrOptions === 'string') {
          // Handle legacy: selector passed as first argument
          if (nameOrOptions.startsWith('.') || nameOrOptions.startsWith('#') || nameOrOptions.startsWith('[')) {
            selector = nameOrOptions;
          } else {
            name = nameOrOptions;
          }
        }

        name = name || 'screenshot_' + Date.now();
        var fullPage = options.fullPage === true;
        var overview = options.overview !== false; // Default true
        var maxHeight = options.maxHeight || 2000;
        var overviewScale = options.overviewScale || 0.25;
        var detailHeight = options.detailHeight || 600;
        var format = options.format || 'png';
        var quality = options.quality || 0.92;
        var region = options.region || null;

        // Handle pixel region capture
        if (region) {
          if (typeof region.x !== 'number' || typeof region.y !== 'number' ||
              typeof region.width !== 'number' || typeof region.height !== 'number') {
            reject(new Error('Region must have x, y, width, and height as numbers'));
            return;
          }
          return captureRegion(region);
        }

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

        // Calculate dimensions
        var pageWidth = document.documentElement.scrollWidth;
        var pageHeight = document.documentElement.scrollHeight;
        var viewportHeight = window.innerHeight;
        var scrollY = window.scrollY;

        // For specific elements or small pages, capture directly
        if (targetElement !== document.body || fullPage || pageHeight <= maxHeight) {
          return captureSimple(targetElement, fullPage, pageHeight, pageWidth, viewportHeight);
        }

        // For long pages with overview mode, capture multiple sections
        if (overview) {
          return captureWithOverview();
        }

        // Viewport only (overview disabled)
        return captureViewportOnly();

        function captureSimple(element, isFullPage, ph, pw, vh) {
          var canvasOptions = {
            allowTaint: true,
            useCORS: true,
            logging: false,
            scrollY: -window.scrollY,
            scrollX: -window.scrollX
          };

          if (element === document.body) {
            canvasOptions.windowWidth = pw;
            canvasOptions.windowHeight = isFullPage ? ph : vh;
          }

          html2canvas(element, canvasOptions).then(function(canvas) {
            var mimeType = format === 'jpeg' ? 'image/jpeg' : 'image/png';
            var dataUrl = format === 'jpeg' ? canvas.toDataURL(mimeType, quality) : canvas.toDataURL(mimeType);

            var result = {
              name: name,
              width: canvas.width,
              height: canvas.height,
              selector: selector || 'body',
              fullPage: isFullPage,
              pageHeight: ph
            };

            core.send('screenshot', Object.assign({}, result, {
              data: dataUrl,
              format: format,
              timestamp: Date.now()
            }));

            resolve(result);
          }).catch(reject);
        }

        function captureViewportOnly() {
          var canvasOptions = {
            allowTaint: true,
            useCORS: true,
            logging: false,
            scrollY: -scrollY,
            scrollX: -window.scrollX,
            windowWidth: pageWidth,
            windowHeight: viewportHeight,
            height: viewportHeight,
            y: scrollY
          };

          html2canvas(document.body, canvasOptions).then(function(canvas) {
            var mimeType = format === 'jpeg' ? 'image/jpeg' : 'image/png';
            var dataUrl = format === 'jpeg' ? canvas.toDataURL(mimeType, quality) : canvas.toDataURL(mimeType);

            var result = {
              name: name,
              width: canvas.width,
              height: canvas.height,
              selector: 'body',
              mode: 'viewport',
              pageHeight: pageHeight,
              truncated: true,
              message: 'Captured viewport only (' + canvas.height + 'px of ' + pageHeight + 'px page).'
            };

            core.send('screenshot', Object.assign({}, result, {
              data: dataUrl,
              format: format,
              timestamp: Date.now()
            }));

            resolve(result);
          }).catch(reject);
        }

        function captureWithOverview() {
          console.log('[DevTool] Long page (' + pageHeight + 'px) - capturing overview + detail sections');

          // Capture full page at reduced scale for overview
          var overviewOptions = {
            allowTaint: true,
            useCORS: true,
            logging: false,
            scrollY: 0,
            scrollX: 0,
            windowWidth: pageWidth,
            windowHeight: pageHeight,
            scale: overviewScale
          };

          html2canvas(document.body, overviewOptions).then(function(overviewCanvas) {
            // Now capture detail sections
            var captures = {
              overview: overviewCanvas
            };

            // Capture top section
            return captureSection(0, detailHeight).then(function(topCanvas) {
              captures.top = topCanvas;

              // Capture current viewport
              return captureSection(scrollY, viewportHeight);
            }).then(function(viewportCanvas) {
              captures.viewport = viewportCanvas;

              // Capture bottom section
              var bottomY = Math.max(0, pageHeight - detailHeight);
              return captureSection(bottomY, detailHeight);
            }).then(function(bottomCanvas) {
              captures.bottom = bottomCanvas;
              return captures;
            });
          }).then(function(captures) {
            // Combine into result
            var mimeType = format === 'jpeg' ? 'image/jpeg' : 'image/png';

            var overviewData = format === 'jpeg'
              ? captures.overview.toDataURL(mimeType, quality)
              : captures.overview.toDataURL(mimeType);

            var topData = format === 'jpeg'
              ? captures.top.toDataURL(mimeType, quality)
              : captures.top.toDataURL(mimeType);

            var viewportData = format === 'jpeg'
              ? captures.viewport.toDataURL(mimeType, quality)
              : captures.viewport.toDataURL(mimeType);

            var bottomData = format === 'jpeg'
              ? captures.bottom.toDataURL(mimeType, quality)
              : captures.bottom.toDataURL(mimeType);

            var result = {
              name: name,
              mode: 'overview',
              pageWidth: pageWidth,
              pageHeight: pageHeight,
              overview: {
                width: captures.overview.width,
                height: captures.overview.height,
                scale: overviewScale
              },
              top: {
                width: captures.top.width,
                height: captures.top.height,
                y: 0
              },
              viewport: {
                width: captures.viewport.width,
                height: captures.viewport.height,
                y: scrollY
              },
              bottom: {
                width: captures.bottom.width,
                height: captures.bottom.height,
                y: pageHeight - detailHeight
              },
              message: 'Long page (' + pageHeight + 'px) - captured scaled overview (' + Math.round(overviewScale * 100) + '%) + detail sections (top, viewport, bottom)'
            };

            // Send overview as main screenshot
            core.send('screenshot', {
              name: name,
              data: overviewData,
              width: captures.overview.width,
              height: captures.overview.height,
              format: format,
              mode: 'overview',
              pageHeight: pageHeight,
              timestamp: Date.now()
            });

            // Send detail sections
            core.send('screenshot', {
              name: name + '_top',
              data: topData,
              width: captures.top.width,
              height: captures.top.height,
              format: format,
              mode: 'detail_top',
              y: 0,
              timestamp: Date.now()
            });

            core.send('screenshot', {
              name: name + '_viewport',
              data: viewportData,
              width: captures.viewport.width,
              height: captures.viewport.height,
              format: format,
              mode: 'detail_viewport',
              y: scrollY,
              timestamp: Date.now()
            });

            core.send('screenshot', {
              name: name + '_bottom',
              data: bottomData,
              width: captures.bottom.width,
              height: captures.bottom.height,
              format: format,
              mode: 'detail_bottom',
              y: pageHeight - detailHeight,
              timestamp: Date.now()
            });

            resolve(result);
          }).catch(reject);
        }

        function captureSection(y, height) {
          return new Promise(function(resolve, reject) {
            var canvasOptions = {
              allowTaint: true,
              useCORS: true,
              logging: false,
              scrollY: -y,
              scrollX: 0,
              windowWidth: pageWidth,
              windowHeight: height,
              y: y,
              height: height
            };

            html2canvas(document.body, canvasOptions).then(resolve).catch(reject);
          });
        }

        function captureRegion(reg) {
          return new Promise(function(resolve, reject) {
            console.log('[DevTool] Capturing region: x=' + reg.x + ', y=' + reg.y + ', width=' + reg.width + ', height=' + reg.height);

            var canvasOptions = {
              allowTaint: true,
              useCORS: true,
              logging: false,
              scrollY: -reg.y,
              scrollX: -reg.x,
              windowWidth: reg.width,
              windowHeight: reg.height,
              x: reg.x,
              y: reg.y,
              width: reg.width,
              height: reg.height
            };

            html2canvas(document.body, canvasOptions).then(function(canvas) {
              var mimeType = format === 'jpeg' ? 'image/jpeg' : 'image/png';
              var dataUrl = format === 'jpeg' ? canvas.toDataURL(mimeType, quality) : canvas.toDataURL(mimeType);

              var result = {
                name: name,
                width: canvas.width,
                height: canvas.height,
                mode: 'region',
                region: {
                  x: reg.x,
                  y: reg.y,
                  width: reg.width,
                  height: reg.height
                }
              };

              core.send('screenshot', Object.assign({}, result, {
                data: dataUrl,
                format: format,
                timestamp: Date.now()
              }));

              resolve(result);
            }).catch(reject);
          });
        }
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
  console.log('  __devtool.auditAccessibility({mode:"standard", detailLevel:"compact"})');
  console.log('  __devtool.auditPerformance({detailLevel:"summary"})');
  console.log('  __devtool.interactions.getLastClickContext()');
  console.log('  __devtool.indicator.toggle() - Toggle floating indicator');
  console.log('  __devtool.sketch.open() - Open sketch mode');
})();

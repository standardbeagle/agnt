// Layout diagnostic primitives for DevTool
// Find overflows, stacking contexts, and offscreen elements

(function() {
  'use strict';

  var utils = window.__devtool_utils;
  var inspection = window.__devtool_inspection;
  var visual = window.__devtool_visual;

  function findOverflows() {
    var elements = document.querySelectorAll('*');
    var results = [];

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      var overflow = inspection.getOverflow(el);

      if (overflow && overflow.hasOverflow) {
        results.push({
          element: el,
          selector: utils.generateSelector(el),
          type: overflow.x === 'hidden' || overflow.y === 'hidden' ? 'hidden' : 'scrollable',
          scrollWidth: overflow.scrollWidth,
          scrollHeight: overflow.scrollHeight,
          clientWidth: overflow.clientWidth,
          clientHeight: overflow.clientHeight
        });
      }
    }

    return { overflows: results, count: results.length };
  }

  function findStackingContexts() {
    var elements = document.querySelectorAll('*');
    var contexts = [];

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      var computed = window.getComputedStyle(el);

      var isContext = (
        (computed.position !== 'static' && computed.zIndex !== 'auto') ||
        parseFloat(computed.opacity) < 1 ||
        computed.transform !== 'none' ||
        computed.filter !== 'none' ||
        computed.perspective !== 'none'
      );

      if (isContext) {
        var reasons = [];
        if (computed.position !== 'static' && computed.zIndex !== 'auto') {
          reasons.push('positioned');
        }
        if (parseFloat(computed.opacity) < 1) {
          reasons.push('opacity');
        }
        if (computed.transform !== 'none') {
          reasons.push('transform');
        }
        if (computed.filter !== 'none') {
          reasons.push('filter');
        }

        contexts.push({
          element: el,
          selector: utils.generateSelector(el),
          zIndex: computed.zIndex,
          reason: reasons
        });
      }
    }

    return { contexts: contexts, count: contexts.length };
  }

  function findOffscreen() {
    var elements = document.querySelectorAll('*');
    var results = [];

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      var viewport = visual.isInViewport(el);

      // Skip if error or no valid response
      if (!viewport || viewport.error || !viewport.rect) {
        continue;
      }

      if (!viewport.intersecting) {
        var rect = viewport.rect;
        var direction = [];

        if (rect.bottom < 0) direction.push('above');
        if (rect.top > window.innerHeight) direction.push('below');
        if (rect.right < 0) direction.push('left');
        if (rect.left > window.innerWidth) direction.push('right');

        results.push({
          element: el,
          selector: utils.generateSelector(el),
          direction: direction,
          rect: rect
        });
      }
    }

    return { offscreen: results, count: results.length };
  }

  // Export layout functions
  window.__devtool_layout = {
    findOverflows: findOverflows,
    findStackingContexts: findStackingContexts,
    findOffscreen: findOffscreen
  };
})();

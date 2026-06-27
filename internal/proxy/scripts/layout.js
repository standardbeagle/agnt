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
      if (utils.isDevtoolElement && utils.isDevtoolElement(el)) continue;

      var computed = window.getComputedStyle(el);
      // Canonical, complete trigger set (will-change, isolation, contain,
      // mix-blend-mode, clip-path, mask, backdrop-filter, flex/grid children
      // — all of which the old check silently missed) shared with getStacking
      // via utils so the two never disagree. Each trigger is {property, value}
      // so the agent gets the removable cause, not a bare label.
      var triggers = utils.stackingContextTriggers(computed, utils.isFlexOrGridItem(el));

      if (triggers.length > 0) {
        contexts.push({
          selector: utils.generateSelector(el),
          zIndex: computed.zIndex,
          triggers: triggers,
          // Back-compat: flat reason[] of property names.
          reason: triggers.map(function(t) { return t.property; })
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

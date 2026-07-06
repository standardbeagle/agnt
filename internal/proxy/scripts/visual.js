// Visual state primitives for DevTool
// Check visibility, viewport, and overlap

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // DOMRect serializes to {} through JSON.stringify (its properties live on
  // the prototype), so every returned rect must be hand-copied to a plain
  // object (same pattern as inspection.js getPosition).
  function rectToPlain(rect) {
    return {
      x: rect.x !== undefined ? rect.x : rect.left,
      y: rect.y !== undefined ? rect.y : rect.top,
      width: rect.width,
      height: rect.height,
      top: rect.top,
      right: rect.right,
      bottom: rect.bottom,
      left: rect.left
    };
  }

  function isVisible(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);
      var rect = utils.getRect(el);

      if (!rect) {
        return { visible: false, reason: 'No bounding rect' };
      }

      if (computed.display === 'none') {
        return { visible: false, reason: 'display: none' };
      }

      if (computed.visibility === 'hidden') {
        return { visible: false, reason: 'visibility: hidden' };
      }

      if (parseFloat(computed.opacity) === 0) {
        return { visible: false, reason: 'opacity: 0' };
      }

      if (rect.width === 0 || rect.height === 0) {
        return { visible: false, reason: 'zero size' };
      }

      return { visible: true, area: rect.width * rect.height };
    } catch (e) {
      return { error: e.message };
    }
  }

  // isInViewport(selector, threshold?)
  // threshold is the minimum visible-area ratio (0..1) required for the
  // element to count as "in viewport". With no/zero threshold, any
  // intersection counts.
  function isInViewport(selector, threshold) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    threshold = typeof threshold === 'number' ? threshold : 0;

    try {
      var rect = utils.getRect(el);
      if (!rect) return { error: 'Failed to get bounding rect' };

      var viewportHeight = window.innerHeight || document.documentElement.clientHeight;
      var viewportWidth = window.innerWidth || document.documentElement.clientWidth;

      var intersecting = !(
        rect.bottom < 0 ||
        rect.top > viewportHeight ||
        rect.right < 0 ||
        rect.left > viewportWidth
      );

      var visibleWidth = Math.min(rect.right, viewportWidth) - Math.max(rect.left, 0);
      var visibleHeight = Math.min(rect.bottom, viewportHeight) - Math.max(rect.top, 0);
      var visibleArea = Math.max(0, visibleWidth) * Math.max(0, visibleHeight);
      var totalArea = rect.width * rect.height;
      var ratio = totalArea > 0 ? visibleArea / totalArea : 0;

      return {
        inViewport: threshold > 0 ? ratio >= threshold : intersecting,
        intersecting: intersecting,
        ratio: ratio,
        percentVisible: Math.round(ratio * 100),
        rect: rectToPlain(rect),
        fullyVisible: ratio === 1
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function checkOverlap(selector1, selector2) {
    var el1 = utils.resolveElement(selector1);
    var el2 = utils.resolveElement(selector2);

    if (!el1 || !el2) return { error: 'Element not found' };

    try {
      var rect1 = utils.getRect(el1);
      var rect2 = utils.getRect(el2);

      if (!rect1 || !rect2) return { error: 'Failed to get bounding rects' };

      var overlaps = !(
        rect1.right < rect2.left ||
        rect1.left > rect2.right ||
        rect1.bottom < rect2.top ||
        rect1.top > rect2.bottom
      );

      if (!overlaps) {
        return { overlaps: false };
      }

      var overlapLeft = Math.max(rect1.left, rect2.left);
      var overlapRight = Math.min(rect1.right, rect2.right);
      var overlapTop = Math.max(rect1.top, rect2.top);
      var overlapBottom = Math.min(rect1.bottom, rect2.bottom);

      var overlapArea = (overlapRight - overlapLeft) * (overlapBottom - overlapTop);

      return {
        overlaps: true,
        area: overlapArea,
        rect: {
          left: overlapLeft,
          right: overlapRight,
          top: overlapTop,
          bottom: overlapBottom
        }
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  // Export visual functions
  window.__devtool_visual = {
    isVisible: isVisible,
    isInViewport: isInViewport,
    checkOverlap: checkOverlap
  };
})();

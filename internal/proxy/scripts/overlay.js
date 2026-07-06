// Overlay management system for DevTool
// Handles visual overlays, highlights, and labels

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Overlay state
  var overlayState = {
    container: null,
    overlays: {},
    highlights: {},
    labels: {},
    nextId: 1
  };

  function initOverlayContainer() {
    if (overlayState.container) return overlayState.container;

    var container = document.createElement('div');
    container.id = '__devtool-overlays';
    container.style.cssText = [
      'position: fixed',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'pointer-events: none',
      // Shared layer scale (ui-tokens.js): element highlights sit on the
      // lowest devtool layer so they never cover panels or toasts.
      'z-index: ' + (window.__devtoolTokens ? window.__devtoolTokens.z.highlight : 2147483640),
      'overflow: hidden',
      // Scope layout/style invalidation to the overlay subtree. The container is
      // already viewport-sized via fixed top/left/right/bottom, and children use
      // position:absolute (not fixed), so contain: layout style cannot reparent
      // their containing block. paint is intentionally omitted because callers
      // may add highlights whose bounds extend via border/box-shadow.
      'contain: layout style'
    ].join(';');

    // Mount into the devtool shadow root when available (style isolation
    // from host page). Falls back to document.body via the helper when the
    // shadow root could not be created. We intentionally do NOT mount on
    // documentElement anymore because that would bypass shadow isolation.
    var mountRoot = typeof window.__devtoolGetMountRoot === 'function'
      ? window.__devtoolGetMountRoot()
      : document.documentElement;
    mountRoot.appendChild(container);
    overlayState.container = container;
    return container;
  }

  function removeOverlayContainer() {
    if (overlayState.container && overlayState.container.parentNode) {
      overlayState.container.parentNode.removeChild(overlayState.container);
      overlayState.container = null;
    }
  }

  function createOverlayElement(type, config) {
    var el = document.createElement('div');
    el.className = '__devtool-overlay-' + type;
    el.style.position = 'absolute';
    el.style.pointerEvents = 'none';
    return el;
  }

  function highlight(selector, config) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    config = config || {};
    var color = config.color || 'rgba(0, 123, 255, 0.3)';
    var duration = config.duration;
    var id = 'highlight-' + overlayState.nextId++;

    try {
      initOverlayContainer();
      var rect = utils.getRect(el);

      var highlightEl = createOverlayElement('highlight', config);
      highlightEl.id = id;
      highlightEl.style.cssText += [
        'top: ' + rect.top + 'px',
        'left: ' + rect.left + 'px',
        'width: ' + rect.width + 'px',
        'height: ' + rect.height + 'px',
        'background-color: ' + color,
        'border: 2px solid ' + (config.borderColor || '#007bff'),
        'box-sizing: border-box'
      ].join(';');

      overlayState.container.appendChild(highlightEl);
      overlayState.highlights[id] = highlightEl;

      if (duration) {
        setTimeout(function() {
          removeHighlight(id);
        }, duration);
      }

      return { highlightId: id };
    } catch (e) {
      return { error: e.message };
    }
  }

  function removeHighlight(highlightId) {
    var highlightEl = overlayState.highlights[highlightId];
    if (highlightEl && highlightEl.parentNode) {
      highlightEl.parentNode.removeChild(highlightEl);
      delete overlayState.highlights[highlightId];
    }
  }

  function clearAllOverlays() {
    overlayState.overlays = {};
    overlayState.highlights = {};
    overlayState.labels = {};

    if (overlayState.container) {
      removeOverlayContainer();
    }
  }

  // Export overlay functions
  window.__devtool_overlay = {
    state: overlayState,
    initContainer: initOverlayContainer,
    removeContainer: removeOverlayContainer,
    createOverlayElement: createOverlayElement,
    highlight: highlight,
    removeHighlight: removeHighlight,
    clearAllOverlays: clearAllOverlays
  };
})();

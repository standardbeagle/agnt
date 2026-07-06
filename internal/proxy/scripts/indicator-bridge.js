// Floating Indicator bridge — the content-side half of the indicator.
//
// Loads in EVERY frame (shared role set). It owns the global Ctrl/Cmd+Y hotkey
// registration for all frames: in a nested frame it forwards to the parent's
// indicator (the chrome shell owns the panel UI); in a top-level frame it
// invokes the local full indicator. The full indicator (indicator.js, present
// in the chrome and passive bundles only) detects this bridge via
// window.__devtool_indicator_bridge and skips its own hotkey registration, so
// exactly one keydown handler exists per frame. It also overwrites
// window.__devtool_indicator with the real implementation at its own eval.
//
// In the content bundle the full indicator module is absent, and the minimal
// forwarding surface installed below is what content-side callers reach:
// style-editor's addAttachment and exec'd __devtool.indicator.* calls forward
// to the parent (shell) indicator; in a frame with no reachable parent
// indicator they are safe no-ops.
(function() {
  'use strict';

  window.__devtool_indicator_bridge = true;

  function isNestedFrame() {
    try { return window.top !== window.self; } catch (e) { return true; }
  }

  function parentIndicator() {
    try {
      if (window.parent && window.parent !== window && window.parent.__devtool_indicator) {
        return window.parent.__devtool_indicator;
      }
    } catch (e) { /* cross-origin parent — nothing to forward to */ }
    return null;
  }

  function forward(method) {
    return function() {
      var p = parentIndicator();
      if (p && typeof p[method] === 'function') {
        try { return p[method].apply(p, arguments); } catch (e) { /* parent went away */ }
      }
      return undefined;
    };
  }

  // Global hotkey — registered at script-eval time on window + capture so it
  // wins the registration-order race against page inline scripts (see the
  // matching comment at the bottom of indicator.js). Ctrl+Y (or Cmd+Y).
  window.addEventListener('keydown', function(e) {
    if (e.key === 'y' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault();
      if (isNestedFrame()) {
        forward('togglePanel')();
      } else {
        var ind = window.__devtool_indicator;
        if (ind && ind !== bridgeSurface && typeof ind.togglePanel === 'function') {
          ind.togglePanel();
        }
      }
    }
  }, true);

  // Minimal indicator surface for content frames. Overwritten by the full
  // indicator (indicator.js) wherever that module is present.
  var bridgeSurface = {
    bridge: true,
    show: forward('show'),
    hide: forward('hide'),
    toggle: forward('toggle'),
    togglePanel: forward('togglePanel'),
    addAttachment: forward('addAttachment'),
    showMicroToast: forward('showMicroToast'),
    logHistoryEvent: forward('logHistoryEvent'),
    destroy: function() { /* content frames have no local indicator UI to destroy */ }
  };
  window.__devtool_indicator = bridgeSurface;
})();

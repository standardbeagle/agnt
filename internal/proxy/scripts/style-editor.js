// Style Editor Module
// Floating panel for inspecting and live-editing CSS variables, inline styles,
// and viewing React props for any selected DOM element.

(function() {
  'use strict';

  // Use getters to ensure modules are available at call time, not at parse time
  function getCore() { return window.__devtool_core; }
  function getUtils() { return window.__devtool_utils; }

  // State
  var state = {
    isOpen: false,
    selectedElement: null,
    selector: null,
    panel: null,
    changes: [],
    originalValues: {}
  };

  // Open the style editor for a given element or enter selection mode
  function open(element) {
    if (state.isOpen) return;
    state.isOpen = true;

    if (element) {
      state.selectedElement = element;
      state.selector = getUtils().generateSelector(element);
    }
  }

  // Close the style editor and clean up
  function close() {
    if (!state.isOpen) return;
    state.isOpen = false;
    state.selectedElement = null;
    state.selector = null;
    state.changes = [];
    state.originalValues = {};

    if (state.panel && state.panel.parentNode) {
      state.panel.parentNode.removeChild(state.panel);
    }
    state.panel = null;
  }

  // Toggle the style editor open/closed
  function toggle() {
    if (state.isOpen) {
      close();
    } else {
      open();
    }
  }

  // Get current editor state
  function getState() {
    return {
      isOpen: state.isOpen,
      hasSelection: !!state.selectedElement,
      selector: state.selector,
      changesCount: state.changes.length
    };
  }

  // Build and return a style-edit attachment from current changes
  function attachChanges() {
    if (!state.selectedElement || state.changes.length === 0) {
      return null;
    }

    return {
      type: 'style-edit',
      selector: state.selector,
      changes: state.changes.slice(),
      screenshots: { before: null, after: null }
    };
  }

  // Check whether the editor is currently open
  function isOpen() {
    return state.isOpen;
  }

  // Export public API
  window.__devtool_style_editor = {
    open: open,
    close: close,
    toggle: toggle,
    getState: getState,
    attachChanges: attachChanges,
    isOpen: isOpen
  };
})();

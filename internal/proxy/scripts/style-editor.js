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
    selecting: false,
    selectedElement: null,
    selector: null,
    xpath: null,
    beforeScreenshotId: null,
    panel: null,
    overlay: null,
    escapeHandler: null,
    changes: [],
    originalValues: {}
  };

  // Generate XPath for an element
  function generateXPath(element) {
    if (element.id) {
      return '//*[@id="' + element.id + '"]';
    }

    var path = '';
    var node = element;

    while (node && node.nodeType === Node.ELEMENT_NODE) {
      var index = 0;
      var sibling = node.previousSibling;

      while (sibling) {
        if (sibling.nodeType === Node.ELEMENT_NODE && sibling.nodeName === node.nodeName) {
          index++;
        }
        sibling = sibling.previousSibling;
      }

      var tagName = node.nodeName.toLowerCase();
      var pathIndex = index > 0 ? '[' + (index + 1) + ']' : '';
      path = '/' + tagName + pathIndex + path;

      node = node.parentNode;
    }

    return path;
  }

  // Capture a full-page "before" screenshot via the binary WebSocket pipeline.
  // Returns the screenshot context ID, or null if capture is unavailable.
  function captureBeforeScreenshot() {
    if (typeof html2canvas === 'undefined') {
      return null;
    }

    var core = getCore();
    if (!core || !core.ws) return null;

    var ws = core.ws();
    if (!ws || ws.readyState !== WebSocket.OPEN) return null;

    var id = 'ctx_' + Date.now().toString(36) + Math.random().toString(36).substr(2, 5);

    html2canvas(document.body, {
      allowTaint: true,
      useCORS: true,
      logging: false,
      scrollX: 0,
      scrollY: 0,
      windowWidth: document.documentElement.scrollWidth,
      windowHeight: document.documentElement.scrollHeight
    }).then(function(canvas) {
      canvas.toBlob(function(blob) {
        if (!blob) return;
        blob.arrayBuffer().then(function(buf) {
          var wsNow = core.ws();
          if (!wsNow || wsNow.readyState !== WebSocket.OPEN) return;
          var idBytes = new TextEncoder().encode(id);
          var frame = new Uint8Array(1 + idBytes.length + buf.byteLength);
          frame[0] = idBytes.length;
          frame.set(idBytes, 1);
          frame.set(new Uint8Array(buf), 1 + idBytes.length);
          wsNow.send(frame.buffer);
        });
      }, 'image/png');
    });

    return id;
  }

  // Start element selection mode.
  // Creates a hover-to-select overlay with crosshair cursor, highlight box,
  // and tooltip showing the CSS selector. On click, captures element reference,
  // selector, xpath, and triggers a "before" screenshot, then calls onSelect.
  function startSelection(onSelect) {
    if (state.selecting) return;
    state.selecting = true;

    var overlay = document.createElement('div');
    overlay.id = '__devtool-style-overlay';
    overlay.style.cssText = [
      'position: fixed',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'z-index: 2147483647',
      'cursor: crosshair',
      'background: rgba(99, 102, 241, 0.05)'
    ].join(';');

    var highlight = document.createElement('div');
    highlight.id = '__devtool-style-highlight';
    highlight.style.cssText = [
      'position: absolute',
      'border: 2px solid #6366f1',
      'background: rgba(99, 102, 241, 0.1)',
      'pointer-events: none',
      'border-radius: 4px',
      'display: none'
    ].join(';');
    overlay.appendChild(highlight);

    var tooltip = document.createElement('div');
    tooltip.id = '__devtool-style-tooltip';
    tooltip.style.cssText = [
      'position: absolute',
      'background: #1e293b',
      'color: white',
      'padding: 4px 8px',
      'border-radius: 6px',
      'font-size: 11px',
      'font-family: ui-monospace, monospace',
      'white-space: nowrap',
      'pointer-events: none',
      'display: none'
    ].join(';');
    overlay.appendChild(tooltip);

    var instructions = document.createElement('div');
    instructions.style.cssText = [
      'position: fixed',
      'bottom: 20px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: #1e293b',
      'color: white',
      'padding: 10px 20px',
      'border-radius: 9999px',
      'font-size: 13px',
      'font-weight: 500',
      'box-shadow: 0 10px 40px rgba(0,0,0,0.15)',
      'z-index: 2147483648'
    ].join(';');
    instructions.textContent = 'Click an element to inspect styles \u2022 ESC to cancel';
    overlay.appendChild(instructions);

    var hoveredElement = null;

    overlay.addEventListener('mousemove', function(e) {
      overlay.style.pointerEvents = 'none';
      var el = document.elementFromPoint(e.clientX, e.clientY);
      overlay.style.pointerEvents = 'auto';

      if (!el || (el.id && el.id.indexOf('__devtool') === 0)) {
        highlight.style.display = 'none';
        tooltip.style.display = 'none';
        hoveredElement = null;
        return;
      }

      hoveredElement = el;
      var rect = el.getBoundingClientRect();

      highlight.style.display = 'block';
      highlight.style.left = rect.left + 'px';
      highlight.style.top = rect.top + 'px';
      highlight.style.width = rect.width + 'px';
      highlight.style.height = rect.height + 'px';

      var selector = getUtils().generateSelector(el);
      tooltip.textContent = selector;
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(rect.left, window.innerWidth - 200) + 'px';
      tooltip.style.top = Math.max(rect.top - 28, 5) + 'px';
    });

    overlay.addEventListener('click', function(e) {
      e.preventDefault();
      e.stopPropagation();

      if (!hoveredElement) return;

      var element = hoveredElement;
      var selector = getUtils().generateSelector(element);
      var xpath = generateXPath(element);

      // Remove overlay before screenshot so it does not appear in the capture
      removeOverlay();

      var screenshotId = captureBeforeScreenshot();

      if (typeof onSelect === 'function') {
        onSelect(element, selector, xpath, screenshotId);
      }
    });

    function handleEscape(e) {
      if (e.key === 'Escape') {
        removeOverlay();
      }
    }

    function removeOverlay() {
      state.selecting = false;
      if (overlay.parentNode) {
        overlay.parentNode.removeChild(overlay);
      }
      document.removeEventListener('keydown', handleEscape);
      state.overlay = null;
      state.escapeHandler = null;
    }

    document.addEventListener('keydown', handleEscape);
    state.overlay = overlay;
    state.escapeHandler = handleEscape;
    document.body.appendChild(overlay);
  }

  // Open the style editor for a given element or enter selection mode
  function open(element) {
    if (state.isOpen) return;
    state.isOpen = true;

    if (element) {
      state.selectedElement = element;
      state.selector = getUtils().generateSelector(element);
      state.xpath = generateXPath(element);
      state.beforeScreenshotId = captureBeforeScreenshot();
    } else {
      startSelection(function(el, selector, xpath, screenshotId) {
        state.selectedElement = el;
        state.selector = selector;
        state.xpath = xpath;
        state.beforeScreenshotId = screenshotId;
      });
    }
  }

  // Close the style editor and clean up
  function close() {
    if (!state.isOpen) return;
    state.isOpen = false;
    state.selectedElement = null;
    state.selector = null;
    state.xpath = null;
    state.beforeScreenshotId = null;
    state.changes = [];
    state.originalValues = {};

    if (state.selecting) {
      state.selecting = false;
      if (state.overlay && state.overlay.parentNode) {
        state.overlay.parentNode.removeChild(state.overlay);
      }
      if (state.escapeHandler) {
        document.removeEventListener('keydown', state.escapeHandler);
      }
      state.overlay = null;
      state.escapeHandler = null;
    }

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
      selecting: state.selecting,
      hasSelection: !!state.selectedElement,
      selector: state.selector,
      xpath: state.xpath,
      beforeScreenshotId: state.beforeScreenshotId,
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
      screenshots: { before: state.beforeScreenshotId, after: null }
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
    isOpen: isOpen,
    startSelection: startSelection
  };
})();

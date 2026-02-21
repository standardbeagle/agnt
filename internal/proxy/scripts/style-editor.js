// Style Editor Module
// Floating panel for inspecting and live-editing CSS variables, inline styles,
// and viewing React props for any selected DOM element.

(function() {
  'use strict';

  // Use getters to ensure modules are available at call time, not at parse time
  function getCore() { return window.__devtool_core; }
  function getUtils() { return window.__devtool_utils; }

  // localStorage key for panel position persistence
  var STORAGE_KEY = '__devtool_style_editor_pos';

  // Design tokens matching indicator.js visual language
  var TOKENS = {
    colors: {
      primary: '#6366f1',
      primaryDark: '#4f46e5',
      surface: '#ffffff',
      surfaceAlt: '#f8fafc',
      border: '#e2e8f0',
      text: '#1e293b',
      textMuted: '#64748b',
      textInverse: '#ffffff',
      error: '#ef4444'
    },
    radius: {
      sm: '6px',
      md: '10px',
      lg: '14px'
    },
    shadow: {
      lg: '0 10px 40px rgba(0,0,0,0.15)'
    },
    spacing: {
      xs: '4px',
      sm: '8px',
      md: '12px',
      lg: '16px'
    }
  };

  // SVG icons (inline, matching indicator.js patterns)
  var ICONS = {
    pin: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 17v5"/><path d="M9 11V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v7"/><path d="M5 17h14"/><path d="M7 11l-2 6h14l-2-6"/></svg>',
    pinFilled: '<svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 17v5"/><path d="M9 11V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v7"/><path d="M5 17h14"/><path d="M7 11l-2 6h14l-2-6"/></svg>',
    reselect: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 0 0-9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/><path d="M3 12a9 9 0 0 0 9 9 9.75 9.75 0 0 0 6.74-2.74L21 16"/><path d="M16 16h5v5"/></svg>',
    close: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>',
    chevron: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg>'
  };

  // State
  var state = {
    isOpen: false,
    selecting: false,
    pinned: false,
    selectedElement: null,
    selector: null,
    xpath: null,
    beforeScreenshotId: null,
    panel: null,
    overlay: null,
    escapeHandler: null,
    outsideClickHandler: null,
    panelEscapeHandler: null,
    changes: [],
    originalValues: {},
    sections: [],
    panelPosition: null
  };

  // Load persisted panel position from localStorage
  function loadPosition() {
    try {
      var saved = localStorage.getItem(STORAGE_KEY);
      if (saved) {
        var pos = JSON.parse(saved);
        if (typeof pos.x === 'number' && typeof pos.y === 'number') {
          return pos;
        }
      }
    } catch (e) { /* ignore */ }
    return null;
  }

  // Save panel position to localStorage
  function savePosition(x, y) {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ x: x, y: y }));
    } catch (e) { /* ignore */ }
  }

  // Clamp position within viewport bounds
  function clampPosition(x, y) {
    var panelW = 360;
    var panelH = 400;
    x = Math.max(0, Math.min(x, window.innerWidth - panelW));
    y = Math.max(0, Math.min(y, window.innerHeight - panelH));
    return { x: x, y: y };
  }

  // Default position: right side of viewport, vertically centered
  function defaultPosition() {
    var x = window.innerWidth - 360 - 20;
    var y = Math.round((window.innerHeight - 400) / 2);
    return clampPosition(x, y);
  }

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
        if (state.isOpen && !state.panel) {
          state.isOpen = false;
        }
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

  // Create the floating panel DOM structure
  function createPanel(selector) {
    var panel = document.createElement('div');
    panel.id = '__devtool-style-panel';
    panel.style.cssText = [
      'position: fixed',
      'width: 360px',
      'max-height: 70vh',
      'display: flex',
      'flex-direction: column',
      'background: ' + TOKENS.colors.surface,
      'border-radius: ' + TOKENS.radius.lg,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483645',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      'font-size: 13px',
      'color: ' + TOKENS.colors.text,
      'overflow: hidden',
      'user-select: none'
    ].join(';');

    // Title bar
    var titleBar = document.createElement('div');
    titleBar.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 8px',
      'padding: 8px 12px',
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'cursor: grab',
      'flex-shrink: 0'
    ].join(';');

    // Selector display
    var selectorDisplay = document.createElement('div');
    selectorDisplay.style.cssText = [
      'flex: 1',
      'min-width: 0',
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 11px',
      'color: ' + TOKENS.colors.primary,
      'white-space: nowrap',
      'overflow: hidden',
      'text-overflow: ellipsis',
      'font-weight: 600'
    ].join(';');
    selectorDisplay.textContent = selector || '';
    selectorDisplay.title = selector || '';
    titleBar.appendChild(selectorDisplay);

    // Pin button
    var pinBtn = createTitleButton(ICONS.pin, 'Pin panel open', function() {
      state.pinned = !state.pinned;
      pinBtn.innerHTML = state.pinned ? ICONS.pinFilled : ICONS.pin;
      pinBtn.style.color = state.pinned ? TOKENS.colors.primary : TOKENS.colors.textMuted;
      pinBtn.title = state.pinned ? 'Unpin panel' : 'Pin panel open';
    });
    titleBar.appendChild(pinBtn);

    // Re-select button
    var reselectBtn = createTitleButton(ICONS.reselect, 'Re-select element', function() {
      startSelection(function(el, sel, xpath, screenshotId) {
        state.selectedElement = el;
        state.selector = sel;
        state.xpath = xpath;
        state.beforeScreenshotId = screenshotId;
        selectorDisplay.textContent = sel;
        selectorDisplay.title = sel;
      });
    });
    titleBar.appendChild(reselectBtn);

    // Close button
    var closeBtn = createTitleButton(ICONS.close, 'Close', function() {
      close();
    });
    titleBar.appendChild(closeBtn);

    panel.appendChild(titleBar);

    // Scrollable content area
    var content = document.createElement('div');
    content.id = '__devtool-style-content';
    content.style.cssText = [
      'flex: 1',
      'overflow-y: auto',
      'overflow-x: hidden',
      'min-height: 0'
    ].join(';');
    panel.appendChild(content);

    // Bottom bar
    var bottomBar = document.createElement('div');
    bottomBar.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 8px',
      'padding: 8px 12px',
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-top: 1px solid ' + TOKENS.colors.border,
      'flex-shrink: 0'
    ].join(';');

    // Attach Changes button
    var attachBtn = document.createElement('button');
    attachBtn.id = '__devtool-style-attach-btn';
    attachBtn.style.cssText = [
      'flex: 1',
      'padding: 6px 12px',
      'background: ' + TOKENS.colors.primary,
      'color: ' + TOKENS.colors.textInverse,
      'border: none',
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 12px',
      'font-weight: 600',
      'cursor: pointer',
      'transition: background 0.15s ease'
    ].join(';');
    attachBtn.textContent = 'Attach Changes (0)';
    attachBtn.onmouseenter = function() { attachBtn.style.background = TOKENS.colors.primaryDark; };
    attachBtn.onmouseleave = function() { attachBtn.style.background = TOKENS.colors.primary; };
    bottomBar.appendChild(attachBtn);

    // Reset All button
    var resetBtn = document.createElement('button');
    resetBtn.style.cssText = [
      'padding: 6px 12px',
      'background: transparent',
      'color: ' + TOKENS.colors.textMuted,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 12px',
      'font-weight: 500',
      'cursor: pointer',
      'transition: all 0.15s ease'
    ].join(';');
    resetBtn.textContent = 'Reset All';
    resetBtn.onmouseenter = function() {
      resetBtn.style.borderColor = TOKENS.colors.error;
      resetBtn.style.color = TOKENS.colors.error;
    };
    resetBtn.onmouseleave = function() {
      resetBtn.style.borderColor = TOKENS.colors.border;
      resetBtn.style.color = TOKENS.colors.textMuted;
    };
    bottomBar.appendChild(resetBtn);

    panel.appendChild(bottomBar);

    // Wire drag behavior on title bar
    setupDrag(titleBar, panel);

    return panel;
  }

  // Create a small title bar button
  function createTitleButton(icon, title, onClick) {
    var btn = document.createElement('button');
    btn.style.cssText = [
      'background: none',
      'border: none',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'padding: 4px',
      'border-radius: ' + TOKENS.radius.sm,
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'transition: background 0.15s ease, color 0.15s ease',
      'flex-shrink: 0'
    ].join(';');
    btn.innerHTML = icon;
    btn.title = title;
    btn.onclick = onClick;
    btn.onmouseenter = function() {
      btn.style.background = TOKENS.colors.border;
    };
    btn.onmouseleave = function() {
      btn.style.background = 'none';
    };
    return btn;
  }

  // Set up title bar drag behavior (matching indicator.js drag pattern)
  function setupDrag(titleBar, panel) {
    titleBar.addEventListener('mousedown', function(e) {
      if (e.button !== 0) return;
      e.preventDefault();

      var startX = e.clientX;
      var startY = e.clientY;
      var startLeft = panel.offsetLeft;
      var startTop = panel.offsetTop;
      var dragged = false;

      titleBar.style.cursor = 'grabbing';

      function onMove(e) {
        var dx = e.clientX - startX;
        var dy = e.clientY - startY;

        if (Math.abs(dx) > 3 || Math.abs(dy) > 3) dragged = true;

        if (dragged) {
          var pos = clampPosition(startLeft + dx, startTop + dy);
          panel.style.left = pos.x + 'px';
          panel.style.top = pos.y + 'px';
          state.panelPosition = pos;
        }
      }

      function onUp() {
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
        titleBar.style.cursor = 'grab';

        if (dragged && state.panelPosition) {
          savePosition(state.panelPosition.x, state.panelPosition.y);
        }
      }

      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
  }

  // Show the panel in the DOM, positioned for the given element and selector
  function showPanel(element, selector) {
    if (state.panel) {
      hidePanel();
    }

    var panel = createPanel(selector);

    // Determine position: use persisted, or default
    var pos = loadPosition();
    if (!pos) {
      pos = defaultPosition();
    } else {
      pos = clampPosition(pos.x, pos.y);
    }

    panel.style.left = pos.x + 'px';
    panel.style.top = pos.y + 'px';
    state.panelPosition = pos;

    document.body.appendChild(panel);
    state.panel = panel;

    // Set up outside-click-to-close (when not pinned)
    // Use setTimeout to avoid the current click event from triggering dismissal
    setTimeout(function() {
      state.outsideClickHandler = function(e) {
        if (state.pinned) return;
        if (!state.panel) return;
        if (state.selecting) return;
        if (state.panel.contains(e.target)) return;
        // Ignore clicks on devtool elements
        if (e.target.id && e.target.id.indexOf('__devtool') === 0) return;
        close();
      };
      document.addEventListener('mousedown', state.outsideClickHandler, true);
    }, 0);

    // Set up Escape to close panel
    state.panelEscapeHandler = function(e) {
      if (e.key === 'Escape' && !state.selecting) {
        close();
      }
    };
    document.addEventListener('keydown', state.panelEscapeHandler);
  }

  // Remove the panel from the DOM and clean up listeners
  function hidePanel() {
    if (state.panel && state.panel.parentNode) {
      state.panel.parentNode.removeChild(state.panel);
    }
    state.panel = null;
    state.sections = [];

    if (state.outsideClickHandler) {
      document.removeEventListener('mousedown', state.outsideClickHandler, true);
      state.outsideClickHandler = null;
    }

    if (state.panelEscapeHandler) {
      document.removeEventListener('keydown', state.panelEscapeHandler);
      state.panelEscapeHandler = null;
    }
  }

  // Create a collapsible section element with count and changed badge.
  // Returns the section container; content is appended to the returned
  // element's `.contentEl` property.
  function createSection(title, count, changedCount) {
    var section = document.createElement('div');
    section.style.cssText = [
      'border-bottom: 1px solid ' + TOKENS.colors.border
    ].join(';');

    var expanded = true;

    // Header
    var header = document.createElement('div');
    header.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 6px',
      'padding: 8px 12px',
      'cursor: pointer',
      'user-select: none',
      'transition: background 0.1s ease'
    ].join(';');
    header.onmouseenter = function() { header.style.background = TOKENS.colors.surfaceAlt; };
    header.onmouseleave = function() { header.style.background = 'transparent'; };

    // Chevron
    var chevron = document.createElement('span');
    chevron.style.cssText = [
      'display: flex',
      'align-items: center',
      'transition: transform 0.15s ease',
      'color: ' + TOKENS.colors.textMuted,
      'flex-shrink: 0'
    ].join(';');
    chevron.innerHTML = ICONS.chevron;
    header.appendChild(chevron);

    // Title
    var titleEl = document.createElement('span');
    titleEl.style.cssText = [
      'font-size: 11px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px',
      'flex: 1',
      'min-width: 0'
    ].join(';');
    titleEl.textContent = title;
    header.appendChild(titleEl);

    // Count badge
    var countBadge = document.createElement('span');
    countBadge.style.cssText = [
      'font-size: 10px',
      'color: ' + TOKENS.colors.textMuted,
      'flex-shrink: 0'
    ].join(';');
    countBadge.textContent = typeof count === 'number' ? '(' + count + ')' : '';
    header.appendChild(countBadge);

    // Changed badge
    var changedBadge = document.createElement('span');
    changedBadge.style.cssText = [
      'font-size: 10px',
      'color: ' + TOKENS.colors.primary,
      'font-weight: 600',
      'flex-shrink: 0',
      'display: ' + (changedCount > 0 ? 'inline' : 'none')
    ].join(';');
    changedBadge.textContent = changedCount > 0 ? '(' + changedCount + ' changed)' : '';
    header.appendChild(changedBadge);

    section.appendChild(header);

    // Content area
    var content = document.createElement('div');
    content.style.cssText = [
      'padding: 0 12px 8px 12px',
      'overflow: hidden'
    ].join(';');
    section.appendChild(content);

    // Toggle expand/collapse
    header.addEventListener('click', function() {
      expanded = !expanded;
      content.style.display = expanded ? 'block' : 'none';
      chevron.style.transform = expanded ? 'rotate(0deg)' : 'rotate(-90deg)';
    });

    // Expose content element and update methods for external use
    section.contentEl = content;
    section.updateCount = function(newCount) {
      countBadge.textContent = typeof newCount === 'number' ? '(' + newCount + ')' : '';
    };
    section.updateChanged = function(newChanged) {
      changedBadge.style.display = newChanged > 0 ? 'inline' : 'none';
      changedBadge.textContent = newChanged > 0 ? '(' + newChanged + ' changed)' : '';
    };

    state.sections.push(section);
    return section;
  }

  // Update the attach button label to reflect current change count
  function updateAttachButton() {
    var btn = document.getElementById('__devtool-style-attach-btn');
    if (btn) {
      btn.textContent = 'Attach Changes (' + state.changes.length + ')';
    }
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
      showPanel(element, state.selector);
    } else {
      startSelection(function(el, selector, xpath, screenshotId) {
        state.selectedElement = el;
        state.selector = selector;
        state.xpath = xpath;
        state.beforeScreenshotId = screenshotId;
        showPanel(el, selector);
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
    state.pinned = false;

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

    hidePanel();
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
      pinned: state.pinned,
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
    startSelection: startSelection,
    createSection: createSection,
    showPanel: showPanel,
    hidePanel: hidePanel
  };
})();

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

  // Cache for browser default computed styles per tag name.
  // Keys are uppercase tag names (e.g. 'DIV'), values are plain objects
  // mapping CSS property name to its default computed value string.
  var defaultStyleCache = {};

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
        // Refresh content sections
        var contentEl = document.getElementById('__devtool-style-content');
        if (contentEl) {
          contentEl.innerHTML = '';
          state.sections = [];
          var variables = discoverCSSVariables(el);
          var varSection = renderCSSVariablesSection(variables);
          if (varSection) {
            contentEl.appendChild(varSection);
          }
          var computedStyles = extractComputedStyles(el);
          var computedSections = renderComputedStylesSections(computedStyles);
          for (var csi = 0; csi < computedSections.length; csi++) {
            contentEl.appendChild(computedSections[csi]);
          }
        }
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
    resetBtn.addEventListener('click', function() {
      resetAllVariables();
      // Refresh all CSS variable section rows to show restored values
      for (var si = 0; si < state.sections.length; si++) {
        if (typeof state.sections[si].refreshRows === 'function') {
          state.sections[si].refreshRows();
        }
      }
    });
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

    // Populate content sections
    var content = document.getElementById('__devtool-style-content');
    if (content && element) {
      var variables = discoverCSSVariables(element);
      var varSection = renderCSSVariablesSection(variables);
      if (varSection) {
        content.appendChild(varSection);
      }

      var computedStyles = extractComputedStyles(element);
      var computedSections = renderComputedStylesSections(computedStyles);
      for (var csi = 0; csi < computedSections.length; csi++) {
        content.appendChild(computedSections[csi]);
      }
    }

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

  // Generate a short scope label for an element (used in CSS variable display)
  function scopeLabel(element) {
    if (!element || element === document.documentElement) return ':root';
    if (element === document.body) return 'body';
    return getUtils().generateSelector(element) || element.tagName.toLowerCase();
  }

  // Discover all CSS custom properties (--*) in scope for the given element.
  // Returns array of {name, value, scopeElement, scopeSelector} sorted by
  // closest scope first, alphabetical within each scope.
  function discoverCSSVariables(element) {
    if (!element) return [];

    // Map: variable name -> {name, value, scopeElement, scopeSelector, depth}
    // depth 0 = the element itself, 1 = parent, etc.  Closest scope wins.
    var found = {};

    // Build ancestor chain from element up to documentElement
    var ancestors = [];
    var node = element;
    while (node && node.nodeType === Node.ELEMENT_NODE) {
      ancestors.push(node);
      node = node.parentElement;
    }

    // 1. getComputedStyle — all resolved custom properties on the element
    var computed = getComputedStyle(element);
    for (var i = 0; i < computed.length; i++) {
      var prop = computed[i];
      if (prop.indexOf('--') === 0) {
        found[prop] = {
          name: prop,
          value: computed.getPropertyValue(prop).trim(),
          scopeElement: document.documentElement,
          scopeSelector: ':root',
          depth: ancestors.length - 1
        };
      }
    }

    // 2. Walk ancestors — check element.style for inline custom properties
    for (var ai = 0; ai < ancestors.length; ai++) {
      var anc = ancestors[ai];
      var style = anc.style;
      for (var si = 0; si < style.length; si++) {
        var sp = style[si];
        if (sp.indexOf('--') === 0) {
          var existing = found[sp];
          if (!existing || ai < existing.depth) {
            found[sp] = {
              name: sp,
              value: style.getPropertyValue(sp).trim(),
              scopeElement: anc,
              scopeSelector: scopeLabel(anc),
              depth: ai
            };
          }
        }
      }
    }

    // 3. Scan document.styleSheets — find rules matching element or ancestors
    for (var shi = 0; shi < document.styleSheets.length; shi++) {
      var sheet = document.styleSheets[shi];
      var rules;
      try {
        rules = sheet.cssRules || sheet.rules;
      } catch (e) {
        // Cross-origin stylesheet — skip silently
        continue;
      }
      if (!rules) continue;
      scanRules(rules, ancestors, found);
    }

    // Convert map to array, drop depth field
    var result = [];
    var names = Object.keys(found);
    for (var ri = 0; ri < names.length; ri++) {
      var entry = found[names[ri]];
      result.push({
        name: entry.name,
        value: entry.value,
        scopeElement: entry.scopeElement,
        scopeSelector: entry.scopeSelector,
        depth: entry.depth
      });
    }

    // Sort: closest scope first (lowest depth), alphabetical within same depth
    result.sort(function(a, b) {
      if (a.depth !== b.depth) return a.depth - b.depth;
      return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
    });

    // Strip internal depth field from output
    for (var di = 0; di < result.length; di++) {
      delete result[di].depth;
    }

    return result;
  }

  // Scan CSS rules (including nested @media/@supports) for custom properties
  // on elements in the ancestor chain.
  function scanRules(rules, ancestors, found) {
    for (var ri = 0; ri < rules.length; ri++) {
      var rule = rules[ri];

      // Handle nested rules (@media, @supports, @layer, etc.)
      if (rule.cssRules) {
        scanRules(rule.cssRules, ancestors, found);
        continue;
      }

      // Only process style rules with selectors
      if (rule.type !== CSSRule.STYLE_RULE) continue;

      var ruleStyle = rule.style;
      if (!ruleStyle) continue;

      // Check if this rule has any custom properties at all (fast bail)
      var hasCustom = false;
      for (var pi = 0; pi < ruleStyle.length; pi++) {
        if (ruleStyle[pi].indexOf('--') === 0) {
          hasCustom = true;
          break;
        }
      }
      if (!hasCustom) continue;

      // Find which ancestor (if any) matches this rule's selector
      var matchedAncestorIndex = -1;
      for (var ai = 0; ai < ancestors.length; ai++) {
        try {
          if (ancestors[ai].matches(rule.selectorText)) {
            matchedAncestorIndex = ai;
            break;
          }
        } catch (e) {
          // Invalid selector — skip
          break;
        }
      }
      if (matchedAncestorIndex < 0) continue;

      var matchedEl = ancestors[matchedAncestorIndex];

      // Extract custom properties from this rule
      for (var ci = 0; ci < ruleStyle.length; ci++) {
        var cp = ruleStyle[ci];
        if (cp.indexOf('--') !== 0) continue;

        var existing = found[cp];
        if (!existing || matchedAncestorIndex < existing.depth) {
          found[cp] = {
            name: cp,
            value: ruleStyle.getPropertyValue(cp).trim(),
            scopeElement: matchedEl,
            scopeSelector: scopeLabel(matchedEl),
            depth: matchedAncestorIndex
          };
        }
      }
    }
  }

  // Property-to-category mapping for computed style grouping.
  // Each key is a category name; the value is an array of property name
  // prefixes or exact names.  A property matches the first category whose
  // list contains an entry that the property starts with.
  var STYLE_CATEGORIES = {
    'Layout': [
      'display', 'position', 'top', 'right', 'bottom', 'left',
      'width', 'height', 'min-width', 'min-height', 'max-width', 'max-height',
      'flex-', 'grid-', 'order', 'float', 'clear', 'overflow',
      'z-index', 'box-sizing', 'vertical-align'
    ],
    'Spacing': [
      'margin-', 'padding-'
    ],
    'Typography': [
      'font-size', 'font-weight', 'font-family', 'font-style', 'font-variant',
      'line-height', 'color', 'text-align', 'text-decoration', 'text-transform',
      'letter-spacing', 'word-spacing', 'white-space', 'text-indent',
      'text-overflow', 'text-shadow'
    ],
    'Background': [
      'background-color', 'background-image', 'background-position',
      'background-repeat', 'background-size'
    ],
    'Border': [
      'border-width', 'border-style', 'border-color', 'border-radius',
      'border-top-', 'border-right-', 'border-bottom-', 'border-left-',
      'border-image', 'outline'
    ],
    'Effects': [
      'opacity', 'box-shadow', 'transform', 'transition', 'filter',
      'animation', 'cursor', 'pointer-events', 'visibility', 'mix-blend-mode'
    ]
  };

  // Order of categories for display purposes
  var CATEGORY_ORDER = ['Layout', 'Spacing', 'Typography', 'Background', 'Border', 'Effects'];

  // Determine which category a CSS property belongs to.
  // Returns the category name or null if it does not match any.
  function categorizeProperty(property) {
    for (var ci = 0; ci < CATEGORY_ORDER.length; ci++) {
      var cat = CATEGORY_ORDER[ci];
      var prefixes = STYLE_CATEGORIES[cat];
      for (var pi = 0; pi < prefixes.length; pi++) {
        var prefix = prefixes[pi];
        if (property === prefix || property.indexOf(prefix) === 0) {
          return cat;
        }
      }
    }
    return null;
  }

  // Get browser default computed styles for a given tag name.
  // Creates a hidden element of the same tag, reads its computed styles,
  // and caches the result keyed by uppercase tag name.
  function getDefaultStyles(tagName) {
    var key = tagName.toUpperCase();
    if (defaultStyleCache[key]) {
      return defaultStyleCache[key];
    }

    var temp = document.createElement(tagName);
    temp.style.cssText = 'position:absolute;visibility:hidden;pointer-events:none;width:auto;height:auto;';
    document.body.appendChild(temp);

    var computed = getComputedStyle(temp);
    var defaults = {};
    for (var i = 0; i < computed.length; i++) {
      var prop = computed[i];
      defaults[prop] = computed.getPropertyValue(prop);
    }

    document.body.removeChild(temp);
    defaultStyleCache[key] = defaults;
    return defaults;
  }

  // Extract non-default computed styles for the given element, grouped by
  // category.  Returns an array of {category, property, value, isDefault}
  // for every categorized property.  Properties whose computed value
  // matches the browser default for the element's tag have isDefault=true.
  function extractComputedStyles(element) {
    if (!element || element.nodeType !== Node.ELEMENT_NODE) return [];

    var computed = getComputedStyle(element);
    var defaults = getDefaultStyles(element.tagName);

    // Collect entries grouped by category
    var groups = {};
    for (var ci = 0; ci < CATEGORY_ORDER.length; ci++) {
      groups[CATEGORY_ORDER[ci]] = [];
    }

    for (var i = 0; i < computed.length; i++) {
      var prop = computed[i];
      // Skip custom properties (handled by discoverCSSVariables)
      if (prop.indexOf('--') === 0) continue;

      var cat = categorizeProperty(prop);
      if (!cat) continue;

      var value = computed.getPropertyValue(prop);
      var isDefault = defaults[prop] !== undefined && value === defaults[prop];

      groups[cat].push({
        category: cat,
        property: prop,
        value: value,
        isDefault: isDefault
      });
    }

    // Flatten in category order, properties alphabetical within each category
    var result = [];
    for (var gi = 0; gi < CATEGORY_ORDER.length; gi++) {
      var catName = CATEGORY_ORDER[gi];
      var entries = groups[catName];
      entries.sort(function(a, b) {
        return a.property < b.property ? -1 : a.property > b.property ? 1 : 0;
      });
      for (var ei = 0; ei < entries.length; ei++) {
        result.push(entries[ei]);
      }
    }

    return result;
  }

  // Key properties per category shown in collapsed summary lines.
  var SUMMARY_PROPERTIES = {
    'Layout': ['display', 'position', 'width', 'height'],
    'Spacing': ['margin-top', 'margin-right', 'padding-top', 'padding-right'],
    'Typography': ['font-size', 'font-weight', 'color', 'line-height'],
    'Background': ['background-color', 'background-image'],
    'Border': ['border-width', 'border-style', 'border-color', 'border-radius'],
    'Effects': ['opacity', 'box-shadow', 'transform']
  };

  // Build a short summary string for a category's entries (shown when collapsed).
  function buildCategorySummary(entries) {
    var cat = entries[0] ? entries[0].category : '';
    var keys = SUMMARY_PROPERTIES[cat] || [];
    var parts = [];
    for (var ki = 0; ki < keys.length && parts.length < 3; ki++) {
      for (var ei = 0; ei < entries.length; ei++) {
        if (entries[ei].property === keys[ki] && !entries[ei].isDefault) {
          var val = entries[ei].value;
          if (val.length > 24) val = val.substring(0, 21) + '...';
          parts.push(entries[ei].property + ': ' + val);
          break;
        }
      }
    }
    return parts.length > 0 ? parts.join(', ') : '';
  }

  // Scan document stylesheets for properties with !important on the selected element.
  // Returns a Set-like object (plain object with property names as keys) for fast lookup.
  function scanImportantProperties(element) {
    var result = {};
    if (!element) return result;

    for (var shi = 0; shi < document.styleSheets.length; shi++) {
      var sheet = document.styleSheets[shi];
      var rules;
      try {
        rules = sheet.cssRules || sheet.rules;
      } catch (e) {
        continue;
      }
      if (!rules) continue;
      scanRulesForImportant(rules, element, result);
    }
    return result;
  }

  // Recursively scan CSS rules for !important declarations matching the element.
  function scanRulesForImportant(rules, element, result) {
    for (var ri = 0; ri < rules.length; ri++) {
      var rule = rules[ri];
      if (rule.cssRules) {
        scanRulesForImportant(rule.cssRules, element, result);
        continue;
      }
      if (rule.type !== CSSRule.STYLE_RULE) continue;
      var ruleStyle = rule.style;
      if (!ruleStyle) continue;

      var matches = false;
      try {
        matches = element.matches(rule.selectorText);
      } catch (e) {
        continue;
      }
      if (!matches) continue;

      for (var pi = 0; pi < ruleStyle.length; pi++) {
        var prop = ruleStyle[pi];
        if (ruleStyle.getPropertyPriority(prop) === 'important') {
          result[prop] = true;
        }
      }
    }
  }

  // Apply an inline style edit on the selected element and track in state.changes.
  function applyInlineStyleEdit(property, newValue) {
    var element = state.selectedElement;
    if (!element) return;

    var key = property + '|inline';

    // Capture original on first edit
    if (!state.originalValues[key]) {
      state.originalValues[key] = {
        value: getComputedStyle(element).getPropertyValue(property).trim(),
        scopeElement: element
      };
    }

    element.style.setProperty(property, newValue);

    var found = false;
    for (var i = 0; i < state.changes.length; i++) {
      if (state.changes[i].property === property && state.changes[i].scope === 'inline') {
        state.changes[i].current = newValue;
        found = true;
        break;
      }
    }
    if (!found) {
      state.changes.push({
        property: property,
        scope: 'inline',
        original: state.originalValues[key].value,
        current: newValue
      });
    }

    updateAttachButton();
  }

  // Check whether an inline style property has been edited.
  function isInlineStyleEdited(property) {
    for (var i = 0; i < state.changes.length; i++) {
      if (state.changes[i].property === property && state.changes[i].scope === 'inline') {
        return true;
      }
    }
    return false;
  }

  // Count inline style edits for a given category.
  function countCategoryChanges(entries) {
    var count = 0;
    for (var i = 0; i < entries.length; i++) {
      if (isInlineStyleEdited(entries[i].property)) {
        count++;
      }
    }
    return count;
  }

  // Render computed styles as collapsible category sections in the panel.
  // Returns an array of section elements (one per non-empty category).
  function renderComputedStylesSections(styles) {
    if (!styles || styles.length === 0) return [];

    var importantProps = scanImportantProperties(state.selectedElement);

    // Group styles by category for section rendering
    var byCategory = {};
    for (var i = 0; i < styles.length; i++) {
      var s = styles[i];
      if (!byCategory[s.category]) {
        byCategory[s.category] = [];
      }
      byCategory[s.category].push(s);
    }

    var sections = [];
    for (var ci = 0; ci < CATEGORY_ORDER.length; ci++) {
      (function(cat) {
        var entries = byCategory[cat];
        if (!entries || entries.length === 0) return;

        var nonDefaultEntries = [];
        var defaultEntries = [];
        for (var si = 0; si < entries.length; si++) {
          if (entries[si].isDefault) {
            defaultEntries.push(entries[si]);
          } else {
            nonDefaultEntries.push(entries[si]);
          }
        }

        var changedCount = countCategoryChanges(entries);
        var section = createSection(cat, entries.length, changedCount);

        // Summary line (visible only when section is collapsed).
        // Inserted between header and content so it remains visible when content is hidden.
        var summaryText = buildCategorySummary(entries);
        var summaryEl = document.createElement('div');
        summaryEl.style.cssText = [
          'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
          'font-size: 11px',
          'color: ' + TOKENS.colors.textMuted,
          'padding: 2px 12px 6px 30px',
          'white-space: nowrap',
          'overflow: hidden',
          'text-overflow: ellipsis',
          'display: none'
        ].join(';');
        summaryEl.textContent = summaryText;
        summaryEl.title = summaryText;
        section.insertBefore(summaryEl, section.contentEl);

        // Track rows and controls for refresh
        var rowControls = [];
        var showingAll = false;

        // Build a property row with blue dot, name, control, and optional !important icon.
        function buildPropertyRow(entry) {
          var row = document.createElement('div');
          row.style.cssText = [
            'display: flex',
            'align-items: center',
            'gap: 6px',
            'padding: 3px 0',
            'font-size: 12px',
            'line-height: 1.4',
            entry.isDefault ? 'opacity: 0.45' : ''
          ].join(';');

          // Blue dot indicator (hidden by default)
          var dot = document.createElement('span');
          dot.style.cssText = [
            'width: 6px',
            'height: 6px',
            'border-radius: 50%',
            'background: ' + TOKENS.colors.primary,
            'flex-shrink: 0',
            'display: ' + (isInlineStyleEdited(entry.property) ? 'inline-block' : 'none')
          ].join(';');
          row.appendChild(dot);

          // Property name
          var nameEl = document.createElement('span');
          nameEl.style.cssText = [
            'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
            'color: ' + (entry.isDefault ? TOKENS.colors.textMuted : TOKENS.colors.text),
            'font-weight: 500',
            'white-space: nowrap',
            'flex-shrink: 0',
            'min-width: 110px'
          ].join(';');
          nameEl.textContent = entry.property;
          row.appendChild(nameEl);

          // Warning icon for !important properties
          if (importantProps[entry.property]) {
            var warnIcon = document.createElement('span');
            warnIcon.style.cssText = [
              'flex-shrink: 0',
              'font-size: 13px',
              'line-height: 1',
              'cursor: help'
            ].join(';');
            warnIcon.textContent = '\u26A0';
            warnIcon.title = entry.property + ' has !important in stylesheet';
            row.appendChild(warnIcon);
          }

          // Value control
          var controlContainer = document.createElement('div');
          controlContainer.style.cssText = [
            'flex: 1',
            'min-width: 0',
            'overflow: hidden'
          ].join(';');

          var control = renderControl(entry.property, entry.value, function(newValue) {
            applyInlineStyleEdit(entry.property, newValue);
            dot.style.display = 'inline-block';
            section.updateChanged(countCategoryChanges(entries));
          });
          controlContainer.appendChild(control);
          row.appendChild(controlContainer);

          rowControls.push({ row: row, dot: dot, property: entry.property });
          return row;
        }

        // Render non-default rows (always visible)
        var nonDefaultRowContainer = document.createElement('div');
        for (var ni = 0; ni < nonDefaultEntries.length; ni++) {
          nonDefaultRowContainer.appendChild(buildPropertyRow(nonDefaultEntries[ni]));
        }
        section.contentEl.appendChild(nonDefaultRowContainer);

        // Render default rows (hidden by default, shown with "Show all" toggle)
        var defaultRowContainer = document.createElement('div');
        defaultRowContainer.style.display = 'none';
        for (var di = 0; di < defaultEntries.length; di++) {
          defaultRowContainer.appendChild(buildPropertyRow(defaultEntries[di]));
        }
        section.contentEl.appendChild(defaultRowContainer);

        // "Show all" toggle (only if there are default entries)
        if (defaultEntries.length > 0) {
          var toggleBtn = document.createElement('button');
          toggleBtn.style.cssText = [
            'background: none',
            'border: none',
            'color: ' + TOKENS.colors.textMuted,
            'font-size: 11px',
            'cursor: pointer',
            'padding: 4px 0',
            'text-decoration: underline',
            'transition: color 0.15s ease'
          ].join(';');
          toggleBtn.textContent = 'Show all (' + defaultEntries.length + ' defaults)';
          toggleBtn.onmouseenter = function() { toggleBtn.style.color = TOKENS.colors.primary; };
          toggleBtn.onmouseleave = function() { toggleBtn.style.color = TOKENS.colors.textMuted; };
          toggleBtn.addEventListener('click', function() {
            showingAll = !showingAll;
            defaultRowContainer.style.display = showingAll ? 'block' : 'none';
            toggleBtn.textContent = showingAll
              ? 'Hide defaults'
              : 'Show all (' + defaultEntries.length + ' defaults)';
          });
          section.contentEl.appendChild(toggleBtn);
        }

        // Wire summary visibility to section collapse state
        var header = section.firstChild;
        if (header) {
          header.addEventListener('click', function() {
            // After createSection's click handler toggles display, check state
            var contentVisible = section.contentEl.style.display !== 'none';
            summaryEl.style.display = contentVisible ? 'none' : 'block';
          });
        }

        // Expose a refresh method for rows
        section.refreshRows = function() {
          for (var ri = 0; ri < rowControls.length; ri++) {
            var ctrl = rowControls[ri];
            ctrl.dot.style.display = isInlineStyleEdited(ctrl.property) ? 'inline-block' : 'none';
          }
          section.updateChanged(countCategoryChanges(entries));
        };

        sections.push(section);
      })(CATEGORY_ORDER[ci]);
    }

    return sections;
  }

  // ── Reusable Value Controls ──────────────────────────────────────────

  // Named CSS color keywords mapped to hex values.
  // Covers the CSS Color Level 4 named color set.
  var NAMED_COLORS = {
    aliceblue:'#f0f8ff',antiquewhite:'#faebd7',aqua:'#00ffff',aquamarine:'#7fffd4',
    azure:'#f0ffff',beige:'#f5f5dc',bisque:'#ffe4c4',black:'#000000',
    blanchedalmond:'#ffebcd',blue:'#0000ff',blueviolet:'#8a2be2',brown:'#a52a2a',
    burlywood:'#deb887',cadetblue:'#5f9ea0',chartreuse:'#7fff00',chocolate:'#d2691e',
    coral:'#ff7f50',cornflowerblue:'#6495ed',cornsilk:'#fff8dc',crimson:'#dc143c',
    cyan:'#00ffff',darkblue:'#00008b',darkcyan:'#008b8b',darkgoldenrod:'#b8860b',
    darkgray:'#a9a9a9',darkgreen:'#006400',darkgrey:'#a9a9a9',darkkhaki:'#bdb76b',
    darkmagenta:'#8b008b',darkolivegreen:'#556b2f',darkorange:'#ff8c00',darkorchid:'#9932cc',
    darkred:'#8b0000',darksalmon:'#e9967a',darkseagreen:'#8fbc8f',darkslateblue:'#483d8b',
    darkslategray:'#2f4f4f',darkslategrey:'#2f4f4f',darkturquoise:'#00ced1',darkviolet:'#9400d3',
    deeppink:'#ff1493',deepskyblue:'#00bfff',dimgray:'#696969',dimgrey:'#696969',
    dodgerblue:'#1e90ff',firebrick:'#b22222',floralwhite:'#fffaf0',forestgreen:'#228b22',
    fuchsia:'#ff00ff',gainsboro:'#dcdcdc',ghostwhite:'#f8f8ff',gold:'#ffd700',
    goldenrod:'#daa520',gray:'#808080',green:'#008000',greenyellow:'#adff2f',
    grey:'#808080',honeydew:'#f0fff0',hotpink:'#ff69b4',indianred:'#cd5c5c',
    indigo:'#4b0082',ivory:'#fffff0',khaki:'#f0e68c',lavender:'#e6e6fa',
    lavenderblush:'#fff0f5',lawngreen:'#7cfc00',lemonchiffon:'#fffacd',lightblue:'#add8e6',
    lightcoral:'#f08080',lightcyan:'#e0ffff',lightgoldenrodyellow:'#fafad2',lightgray:'#d3d3d3',
    lightgreen:'#90ee90',lightgrey:'#d3d3d3',lightpink:'#ffb6c1',lightsalmon:'#ffa07a',
    lightseagreen:'#20b2aa',lightskyblue:'#87cefa',lightslategray:'#778899',lightslategrey:'#778899',
    lightsteelblue:'#b0c4de',lightyellow:'#ffffe0',lime:'#00ff00',limegreen:'#32cd32',
    linen:'#faf0e6',magenta:'#ff00ff',maroon:'#800000',mediumaquamarine:'#66cdaa',
    mediumblue:'#0000cd',mediumorchid:'#ba55d3',mediumpurple:'#9370db',mediumseagreen:'#3cb371',
    mediumslateblue:'#7b68ee',mediumspringgreen:'#00fa9a',mediumturquoise:'#48d1cc',mediumvioletred:'#c71585',
    midnightblue:'#191970',mintcream:'#f5fffa',mistyrose:'#ffe4e1',moccasin:'#ffe4b5',
    navajowhite:'#ffdead',navy:'#000080',oldlace:'#fdf5e6',olive:'#808000',
    olivedrab:'#6b8e23',orange:'#ffa500',orangered:'#ff4500',orchid:'#da70d6',
    palegoldenrod:'#eee8aa',palegreen:'#98fb98',paleturquoise:'#afeeee',palevioletred:'#db7093',
    papayawhip:'#ffefd5',peachpuff:'#ffdab9',peru:'#cd853f',pink:'#ffc0cb',
    plum:'#dda0dd',powderblue:'#b0e0e6',purple:'#800080',rebeccapurple:'#663399',
    red:'#ff0000',rosybrown:'#bc8f8f',royalblue:'#4169e1',saddlebrown:'#8b4513',
    salmon:'#fa8072',sandybrown:'#f4a460',seagreen:'#2e8b57',seashell:'#fff5ee',
    sienna:'#a0522d',silver:'#c0c0c0',skyblue:'#87ceeb',slateblue:'#6a5acd',
    slategray:'#708090',slategrey:'#708090',snow:'#fffafa',springgreen:'#00ff7f',
    steelblue:'#4682b4',tan:'#d2b48c',teal:'#008080',thistle:'#d8bfd8',
    tomato:'#ff6347',turquoise:'#40e0d0',violet:'#ee82ee',wheat:'#f5deb3',
    white:'#ffffff',whitesmoke:'#f5f5f5',yellow:'#ffff00',yellowgreen:'#9acd32',
    transparent:'#00000000'
  };

  // Detect the color format of a CSS color string.
  // Returns one of: 'hex3', 'hex6', 'hex8', 'rgb', 'rgba', 'hsl', 'hsla', 'named', or null.
  function detectColorFormat(value) {
    if (!value || typeof value !== 'string') return null;
    var v = value.trim().toLowerCase();
    if (/^#[0-9a-f]{3}$/i.test(v)) return 'hex3';
    if (/^#[0-9a-f]{6}$/i.test(v)) return 'hex6';
    if (/^#[0-9a-f]{8}$/i.test(v)) return 'hex8';
    if (/^rgba\s*\(/.test(v)) return 'rgba';
    if (/^rgb\s*\(/.test(v)) return 'rgb';
    if (/^hsla\s*\(/.test(v)) return 'hsla';
    if (/^hsl\s*\(/.test(v)) return 'hsl';
    if (NAMED_COLORS[v] !== undefined) return 'named';
    return null;
  }

  // Clamp a number between min and max.
  function clampNum(n, min, max) {
    return n < min ? min : n > max ? max : n;
  }

  // Parse any supported CSS color value into {r, g, b, a} (0-255 for rgb, 0-1 for a).
  // Returns null if the value cannot be parsed.
  function parseColor(value) {
    if (!value || typeof value !== 'string') return null;
    var v = value.trim().toLowerCase();

    // Named colors
    if (NAMED_COLORS[v] !== undefined) {
      return parseColor(NAMED_COLORS[v]);
    }

    // #rgb
    if (/^#[0-9a-f]{3}$/i.test(v)) {
      return {
        r: parseInt(v[1] + v[1], 16),
        g: parseInt(v[2] + v[2], 16),
        b: parseInt(v[3] + v[3], 16),
        a: 1
      };
    }

    // #rrggbb
    if (/^#[0-9a-f]{6}$/i.test(v)) {
      return {
        r: parseInt(v.substr(1, 2), 16),
        g: parseInt(v.substr(3, 2), 16),
        b: parseInt(v.substr(5, 2), 16),
        a: 1
      };
    }

    // #rrggbbaa
    if (/^#[0-9a-f]{8}$/i.test(v)) {
      return {
        r: parseInt(v.substr(1, 2), 16),
        g: parseInt(v.substr(3, 2), 16),
        b: parseInt(v.substr(5, 2), 16),
        a: Math.round(parseInt(v.substr(7, 2), 16) / 255 * 100) / 100
      };
    }

    // rgb(r, g, b) or rgb(r g b)
    var rgbMatch = v.match(/^rgb\s*\(\s*(\d+)\s*[,\s]\s*(\d+)\s*[,\s]\s*(\d+)\s*\)/);
    if (rgbMatch) {
      return {
        r: clampNum(parseInt(rgbMatch[1], 10), 0, 255),
        g: clampNum(parseInt(rgbMatch[2], 10), 0, 255),
        b: clampNum(parseInt(rgbMatch[3], 10), 0, 255),
        a: 1
      };
    }

    // rgba(r, g, b, a) or rgba(r g b / a)
    var rgbaMatch = v.match(/^rgba\s*\(\s*(\d+)\s*[,\s]\s*(\d+)\s*[,\s]\s*(\d+)\s*[,/]\s*([\d.]+)\s*\)/);
    if (rgbaMatch) {
      return {
        r: clampNum(parseInt(rgbaMatch[1], 10), 0, 255),
        g: clampNum(parseInt(rgbaMatch[2], 10), 0, 255),
        b: clampNum(parseInt(rgbaMatch[3], 10), 0, 255),
        a: clampNum(parseFloat(rgbaMatch[4]), 0, 1)
      };
    }

    // hsl(h, s%, l%) or hsl(h s% l%)
    var hslMatch = v.match(/^hsl\s*\(\s*([\d.]+)\s*[,\s]\s*([\d.]+)%?\s*[,\s]\s*([\d.]+)%?\s*\)/);
    if (hslMatch) {
      var rgba = hslToRgb(parseFloat(hslMatch[1]), parseFloat(hslMatch[2]), parseFloat(hslMatch[3]));
      rgba.a = 1;
      return rgba;
    }

    // hsla(h, s%, l%, a) or hsla(h s% l% / a)
    var hslaMatch = v.match(/^hsla\s*\(\s*([\d.]+)\s*[,\s]\s*([\d.]+)%?\s*[,\s]\s*([\d.]+)%?\s*[,/]\s*([\d.]+)\s*\)/);
    if (hslaMatch) {
      var rgbaFromHsl = hslToRgb(parseFloat(hslaMatch[1]), parseFloat(hslaMatch[2]), parseFloat(hslaMatch[3]));
      rgbaFromHsl.a = clampNum(parseFloat(hslaMatch[4]), 0, 1);
      return rgbaFromHsl;
    }

    return null;
  }

  // Convert HSL values to {r, g, b} (0-255).
  // h: 0-360, s: 0-100, l: 0-100
  function hslToRgb(h, s, l) {
    h = ((h % 360) + 360) % 360;
    s = clampNum(s, 0, 100) / 100;
    l = clampNum(l, 0, 100) / 100;

    var c = (1 - Math.abs(2 * l - 1)) * s;
    var x = c * (1 - Math.abs((h / 60) % 2 - 1));
    var m = l - c / 2;
    var rp, gp, bp;

    if (h < 60)       { rp = c; gp = x; bp = 0; }
    else if (h < 120) { rp = x; gp = c; bp = 0; }
    else if (h < 180) { rp = 0; gp = c; bp = x; }
    else if (h < 240) { rp = 0; gp = x; bp = c; }
    else if (h < 300) { rp = x; gp = 0; bp = c; }
    else               { rp = c; gp = 0; bp = x; }

    return {
      r: Math.round((rp + m) * 255),
      g: Math.round((gp + m) * 255),
      b: Math.round((bp + m) * 255)
    };
  }

  // Convert {r, g, b} to {h, s, l} (h: 0-360, s: 0-100, l: 0-100).
  function rgbToHsl(r, g, b) {
    r /= 255; g /= 255; b /= 255;
    var max = Math.max(r, g, b);
    var min = Math.min(r, g, b);
    var d = max - min;
    var l = (max + min) / 2;
    var h = 0, s = 0;

    if (d > 0) {
      s = d / (1 - Math.abs(2 * l - 1));
      if (max === r) h = 60 * (((g - b) / d) % 6);
      else if (max === g) h = 60 * ((b - r) / d + 2);
      else h = 60 * ((r - g) / d + 4);
      if (h < 0) h += 360;
    }

    return {
      h: Math.round(h),
      s: Math.round(s * 100),
      l: Math.round(l * 100)
    };
  }

  // Convert an {r, g, b, a} object to a 6-digit hex string (#rrggbb).
  function rgbaToHex6(rgba) {
    var rr = ('0' + rgba.r.toString(16)).slice(-2);
    var gg = ('0' + rgba.g.toString(16)).slice(-2);
    var bb = ('0' + rgba.b.toString(16)).slice(-2);
    return '#' + rr + gg + bb;
  }

  // Format an {r, g, b, a} color back to the specified format string.
  function formatColor(rgba, format) {
    switch (format) {
      case 'hex3': {
        var hex = rgbaToHex6(rgba);
        // Collapse to 3-digit if possible (each pair is identical digits)
        if (hex[1] === hex[2] && hex[3] === hex[4] && hex[5] === hex[6]) {
          return '#' + hex[1] + hex[3] + hex[5];
        }
        return hex;
      }
      case 'hex6':
        return rgbaToHex6(rgba);
      case 'hex8': {
        var alphaHex = ('0' + Math.round(rgba.a * 255).toString(16)).slice(-2);
        return rgbaToHex6(rgba) + alphaHex;
      }
      case 'rgb':
        return 'rgb(' + rgba.r + ', ' + rgba.g + ', ' + rgba.b + ')';
      case 'rgba':
        return 'rgba(' + rgba.r + ', ' + rgba.g + ', ' + rgba.b + ', ' + rgba.a + ')';
      case 'hsl': {
        var hsl = rgbToHsl(rgba.r, rgba.g, rgba.b);
        return 'hsl(' + hsl.h + ', ' + hsl.s + '%, ' + hsl.l + '%)';
      }
      case 'hsla': {
        var hsla = rgbToHsl(rgba.r, rgba.g, rgba.b);
        return 'hsla(' + hsla.h + ', ' + hsla.s + '%, ' + hsla.l + '%, ' + rgba.a + ')';
      }
      case 'named': {
        // Reverse lookup: find named color matching this hex
        var hexVal = rgbaToHex6(rgba);
        var names = Object.keys(NAMED_COLORS);
        for (var ni = 0; ni < names.length; ni++) {
          if (NAMED_COLORS[names[ni]] === hexVal) return names[ni];
        }
        return hexVal;
      }
      default:
        return rgbaToHex6(rgba);
    }
  }

  // ColorPicker(value, onChange) — Reusable color picker control.
  // Returns a DOM element containing a native color swatch, hex text input,
  // and an opacity slider when the original value uses rgba/hsla format.
  // onChange receives the new color string in the original format.
  function ColorPicker(value, onChange) {
    var format = detectColorFormat(value) || 'hex6';
    var parsed = parseColor(value);
    if (!parsed) {
      parsed = { r: 0, g: 0, b: 0, a: 1 };
    }

    var hasAlpha = (format === 'rgba' || format === 'hsla' || format === 'hex8');
    var debounceTimer = null;

    function debouncedOnChange(newRgba) {
      clearTimeout(debounceTimer);
      debounceTimer = setTimeout(function() {
        if (typeof onChange === 'function') {
          onChange(formatColor(newRgba, format));
        }
      }, 16);
    }

    // Container
    var container = document.createElement('div');
    container.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 6px',
      'flex-wrap: wrap'
    ].join(';');

    // Native color input (swatch)
    var colorInput = document.createElement('input');
    colorInput.type = 'color';
    colorInput.value = rgbaToHex6(parsed);
    colorInput.style.cssText = [
      'width: 28px',
      'height: 24px',
      'padding: 0',
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'cursor: pointer',
      'background: none',
      'flex-shrink: 0'
    ].join(';');
    container.appendChild(colorInput);

    // Hex text input
    var hexInput = document.createElement('input');
    hexInput.type = 'text';
    hexInput.value = rgbaToHex6(parsed);
    hexInput.style.cssText = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: 2px 6px',
      'width: 80px',
      'outline: none',
      'box-sizing: border-box'
    ].join(';');
    hexInput.addEventListener('focus', function() {
      hexInput.style.borderColor = TOKENS.colors.primary;
    });
    hexInput.addEventListener('blur', function() {
      hexInput.style.borderColor = TOKENS.colors.border;
    });
    container.appendChild(hexInput);

    // Swatch preview (shows actual color with alpha)
    var swatch = document.createElement('div');
    swatch.style.cssText = [
      'width: 18px',
      'height: 18px',
      'border-radius: 3px',
      'border: 1px solid ' + TOKENS.colors.border,
      'flex-shrink: 0',
      'background: ' + formatColor(parsed, 'rgba')
    ].join(';');
    container.appendChild(swatch);

    // Opacity slider row (only for rgba/hsla/hex8)
    var opacitySlider = null;
    var opacityLabel = null;
    if (hasAlpha) {
      var opacityRow = document.createElement('div');
      opacityRow.style.cssText = [
        'display: flex',
        'align-items: center',
        'gap: 6px',
        'width: 100%'
      ].join(';');

      var opacityText = document.createElement('span');
      opacityText.style.cssText = [
        'font-size: 11px',
        'color: ' + TOKENS.colors.textMuted,
        'flex-shrink: 0'
      ].join(';');
      opacityText.textContent = 'Opacity';
      opacityRow.appendChild(opacityText);

      opacitySlider = document.createElement('input');
      opacitySlider.type = 'range';
      opacitySlider.min = '0';
      opacitySlider.max = '1';
      opacitySlider.step = '0.01';
      opacitySlider.value = String(parsed.a);
      opacitySlider.style.cssText = [
        'flex: 1',
        'min-width: 0',
        'height: 4px',
        'cursor: pointer'
      ].join(';');
      opacityRow.appendChild(opacitySlider);

      opacityLabel = document.createElement('span');
      opacityLabel.style.cssText = [
        'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
        'font-size: 11px',
        'color: ' + TOKENS.colors.text,
        'min-width: 32px',
        'text-align: right',
        'flex-shrink: 0'
      ].join(';');
      opacityLabel.textContent = String(parsed.a);
      opacityRow.appendChild(opacityLabel);

      container.appendChild(opacityRow);
    }

    // Sync all controls from a new rgba value
    function syncControls(rgba) {
      colorInput.value = rgbaToHex6(rgba);
      hexInput.value = rgbaToHex6(rgba);
      swatch.style.background = formatColor(rgba, 'rgba');
      if (opacitySlider) {
        opacitySlider.value = String(rgba.a);
        opacityLabel.textContent = String(rgba.a);
      }
    }

    // Color input change
    colorInput.addEventListener('input', function() {
      var newParsed = parseColor(colorInput.value);
      if (newParsed) {
        if (hasAlpha) {
          newParsed.a = parseFloat(opacitySlider.value);
        }
        parsed = newParsed;
        hexInput.value = rgbaToHex6(parsed);
        swatch.style.background = formatColor(parsed, 'rgba');
        debouncedOnChange(parsed);
      }
    });

    // Hex input commit on Enter or blur
    function commitHexInput() {
      var newParsed = parseColor(hexInput.value);
      if (newParsed) {
        if (hasAlpha) {
          newParsed.a = parseFloat(opacitySlider.value);
        }
        parsed = newParsed;
        syncControls(parsed);
        debouncedOnChange(parsed);
      } else {
        // Revert to last valid value
        hexInput.value = rgbaToHex6(parsed);
      }
    }

    hexInput.addEventListener('keydown', function(e) {
      if (e.key === 'Enter') {
        e.preventDefault();
        commitHexInput();
      }
    });
    hexInput.addEventListener('blur', commitHexInput);

    // Opacity slider change
    if (opacitySlider) {
      opacitySlider.addEventListener('input', function() {
        parsed.a = clampNum(parseFloat(opacitySlider.value), 0, 1);
        opacityLabel.textContent = String(parsed.a);
        swatch.style.background = formatColor(parsed, 'rgba');
        debouncedOnChange(parsed);
      });
    }

    return container;
  }

  // Per-property range heuristics for the numeric slider.
  // Maps property name patterns to {min, max, step} in px.
  // The first matching entry wins (checked via indexOf prefix match).
  var NUMERIC_RANGES = {
    'font-size':     { min: 8,   max: 72,  step: 1 },
    'border-radius': { min: 0,   max: 50,  step: 1 },
    'padding':       { min: 0,   max: 100, step: 1 },
    'margin':        { min: 0,   max: 100, step: 1 },
    'gap':           { min: 0,   max: 64,  step: 1 },
    'opacity':       { min: 0,   max: 1,   step: 0.01 },
    'line-height':   { min: 0.5, max: 3,   step: 0.1 },
    'border-width':  { min: 0,   max: 20,  step: 1 }
  };

  // Property name order for range lookup (longest prefix first to avoid
  // 'border-' matching before 'border-radius' or 'border-width').
  var NUMERIC_RANGE_KEYS = [
    'border-radius', 'border-width',
    'font-size', 'line-height',
    'padding', 'margin', 'gap', 'opacity'
  ];

  // Unit conversion factors relative to px.
  // rem and em are approximated at 16px base.
  var UNIT_PX_FACTORS = {
    'px': 1,
    'rem': 16,
    'em': 16,
    '%': 1
  };

  // Look up the range heuristics for a given CSS property name.
  // For custom properties (--*), infers a range of 0 to 4x the current value
  // (minimum range of 0-100 when the value is 0).
  // Returns {min, max, step}.
  function getRangeForProperty(property, currentValue) {
    // Check known properties
    for (var i = 0; i < NUMERIC_RANGE_KEYS.length; i++) {
      var key = NUMERIC_RANGE_KEYS[i];
      if (property === key || property.indexOf(key) === 0) {
        return NUMERIC_RANGES[key];
      }
    }

    // Custom properties: infer from current value
    if (property.indexOf('--') === 0) {
      var num = parseFloat(currentValue);
      if (isNaN(num) || num === 0) {
        return { min: 0, max: 100, step: 1 };
      }
      var absVal = Math.abs(num);
      return { min: 0, max: Math.round(absVal * 4), step: absVal >= 1 ? 1 : 0.01 };
    }

    // Fallback: infer from current value
    var fallback = parseFloat(currentValue);
    if (isNaN(fallback) || fallback === 0) {
      return { min: 0, max: 100, step: 1 };
    }
    var absFb = Math.abs(fallback);
    return { min: 0, max: Math.round(absFb * 4), step: absFb >= 1 ? 1 : 0.01 };
  }

  // Convert a numeric value from one unit to another.
  // Both units must be keys in UNIT_PX_FACTORS.
  // For '%' conversion, the value is kept as-is (no meaningful px equivalent).
  function convertUnit(value, fromUnit, toUnit) {
    if (fromUnit === toUnit) return value;
    // Convert to px first, then to target unit
    var pxValue = value * (UNIT_PX_FACTORS[fromUnit] || 1);
    var result = pxValue / (UNIT_PX_FACTORS[toUnit] || 1);
    // Round to avoid floating point noise
    return Math.round(result * 100) / 100;
  }

  // NumericSlider(property, value, unit, onChange) -- Reusable numeric slider control.
  // Returns a DOM element containing a synced range slider, number input,
  // and unit dropdown.  onChange receives the formatted value+unit string
  // (e.g. "16px", "1.5rem", "0.8").
  function NumericSlider(property, value, unit, onChange) {
    var numValue = parseFloat(value);
    if (isNaN(numValue)) numValue = 0;

    var currentUnit = unit || '';
    var range = getRangeForProperty(property, numValue);
    var debounceTimer = null;

    // For unitless properties (opacity, line-height), suppress the unit selector
    var isUnitless = (property === 'opacity' || property === 'line-height');
    var units = ['px', 'rem', 'em', '%'];

    function formatOutput(num, u) {
      // Round to step precision to avoid floating point noise
      var precision = range.step < 1 ? Math.ceil(-Math.log10(range.step)) : 0;
      var rounded = parseFloat(num.toFixed(precision));
      if (isUnitless || !u) return String(rounded);
      return rounded + u;
    }

    function debouncedOnChange() {
      clearTimeout(debounceTimer);
      debounceTimer = setTimeout(function() {
        if (typeof onChange === 'function') {
          onChange(formatOutput(numValue, currentUnit));
        }
      }, 16);
    }

    // Container
    var container = document.createElement('div');
    container.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 4px'
    ].join(';');

    // Range slider
    var slider = document.createElement('input');
    slider.type = 'range';
    slider.min = String(range.min);
    slider.max = String(range.max);
    slider.step = String(range.step);
    slider.value = String(numValue);
    slider.style.cssText = [
      'flex: 1',
      'min-width: 60px',
      'height: 4px',
      'cursor: pointer'
    ].join(';');
    container.appendChild(slider);

    // Number input
    var numberInput = document.createElement('input');
    numberInput.type = 'number';
    numberInput.min = String(range.min);
    numberInput.max = String(range.max);
    numberInput.step = String(range.step);
    numberInput.value = String(numValue);
    numberInput.style.cssText = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: 2px 4px',
      'width: 56px',
      'outline: none',
      'box-sizing: border-box',
      '-moz-appearance: textfield'
    ].join(';');
    numberInput.addEventListener('focus', function() {
      numberInput.style.borderColor = TOKENS.colors.primary;
    });
    numberInput.addEventListener('blur', function() {
      numberInput.style.borderColor = TOKENS.colors.border;
    });
    container.appendChild(numberInput);

    // Unit dropdown (hidden for unitless properties)
    var unitSelect = null;
    if (!isUnitless && currentUnit) {
      unitSelect = document.createElement('select');
      unitSelect.style.cssText = [
        'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
        'font-size: 11px',
        'color: ' + TOKENS.colors.text,
        'background: ' + TOKENS.colors.surface,
        'border: 1px solid ' + TOKENS.colors.border,
        'border-radius: ' + TOKENS.radius.sm,
        'padding: 2px 2px',
        'outline: none',
        'cursor: pointer'
      ].join(';');

      for (var ui = 0; ui < units.length; ui++) {
        var opt = document.createElement('option');
        opt.value = units[ui];
        opt.textContent = units[ui];
        if (units[ui] === currentUnit) opt.selected = true;
        unitSelect.appendChild(opt);
      }
      unitSelect.addEventListener('focus', function() {
        unitSelect.style.borderColor = TOKENS.colors.primary;
      });
      unitSelect.addEventListener('blur', function() {
        unitSelect.style.borderColor = TOKENS.colors.border;
      });
      container.appendChild(unitSelect);
    }

    // Sync controls from current numValue
    function syncControls() {
      slider.value = String(numValue);
      numberInput.value = String(numValue);
    }

    // Update range bounds (used when unit changes and ranges need recalculation)
    function updateRangeBounds() {
      slider.min = String(range.min);
      slider.max = String(range.max);
      slider.step = String(range.step);
      numberInput.min = String(range.min);
      numberInput.max = String(range.max);
      numberInput.step = String(range.step);
    }

    // Slider input
    slider.addEventListener('input', function() {
      numValue = parseFloat(slider.value);
      numberInput.value = String(numValue);
      debouncedOnChange();
    });

    // Number input
    numberInput.addEventListener('input', function() {
      var parsed = parseFloat(numberInput.value);
      if (!isNaN(parsed)) {
        numValue = parsed;
        slider.value = String(numValue);
        debouncedOnChange();
      }
    });

    // Unit selector change: convert the current value to the new unit
    if (unitSelect) {
      unitSelect.addEventListener('change', function() {
        var newUnit = unitSelect.value;
        numValue = convertUnit(numValue, currentUnit, newUnit);
        currentUnit = newUnit;

        // Recalculate range heuristics scaled to the new unit
        var baseRange = getRangeForProperty(property, numValue);
        range = baseRange;
        updateRangeBounds();
        syncControls();
        debouncedOnChange();
      });
    }

    return container;
  }

  // Side labels for the four sides of a box-model shorthand property.
  var SIDE_LABELS = ['top', 'right', 'bottom', 'left'];

  // Parse a CSS shorthand value (e.g. margin, padding) into four side values.
  // Supports 1-value, 2-value, 3-value, and 4-value shorthand forms.
  // Returns {values: [top, right, bottom, left], unit: string}.
  // Each value is a number; unit is extracted from the first token.
  function parseShorthand(value) {
    if (!value || typeof value !== 'string') {
      return { values: [0, 0, 0, 0], unit: 'px' };
    }

    var tokens = value.trim().split(/\s+/);
    var unit = 'px';
    var nums = [];

    for (var i = 0; i < tokens.length; i++) {
      var match = tokens[i].match(/^(-?[\d.]+)(px|rem|em|%)?$/);
      if (match) {
        nums.push(parseFloat(match[1]));
        if (i === 0 && match[2]) {
          unit = match[2];
        }
      } else {
        nums.push(0);
      }
    }

    if (nums.length === 0) return { values: [0, 0, 0, 0], unit: unit };
    if (nums.length === 1) return { values: [nums[0], nums[0], nums[0], nums[0]], unit: unit };
    if (nums.length === 2) return { values: [nums[0], nums[1], nums[0], nums[1]], unit: unit };
    if (nums.length === 3) return { values: [nums[0], nums[1], nums[2], nums[1]], unit: unit };
    return { values: [nums[0], nums[1], nums[2], nums[3]], unit: unit };
  }

  // Collapse four side values back into shortest CSS shorthand form.
  // Returns a formatted string like "10px", "10px 20px", or "10px 20px 30px 40px".
  function collapseShorthand(values, unit) {
    var t = values[0], r = values[1], b = values[2], l = values[3];
    var u = unit || 'px';

    // Round to avoid floating point noise
    t = Math.round(t * 100) / 100;
    r = Math.round(r * 100) / 100;
    b = Math.round(b * 100) / 100;
    l = Math.round(l * 100) / 100;

    if (t === r && r === b && b === l) {
      return t + u;
    }
    if (t === b && r === l) {
      return t + u + ' ' + r + u;
    }
    if (r === l) {
      return t + u + ' ' + r + u + ' ' + b + u;
    }
    return t + u + ' ' + r + u + ' ' + b + u + ' ' + l + u;
  }

  // MultiValueInput(property, value, onChange) -- Reusable multi-value input
  // for CSS shorthand properties like margin and padding.
  // Returns a DOM element containing four linked number inputs (top, right,
  // bottom, left), a "link all" toggle, a shared unit selector, and slider
  // popups on individual input focus.
  // onChange receives the collapsed shorthand string (e.g. "10px 20px").
  function MultiValueInput(property, value, onChange) {
    var parsed = parseShorthand(value);
    var sideValues = parsed.values.slice();
    var currentUnit = parsed.unit;
    var linked = (sideValues[0] === sideValues[1] &&
                  sideValues[1] === sideValues[2] &&
                  sideValues[2] === sideValues[3]);
    var range = getRangeForProperty(property, sideValues[0]);
    var units = ['px', 'rem', 'em', '%'];
    var debounceTimer = null;
    var activeSliderPopup = null;

    function emitChange() {
      clearTimeout(debounceTimer);
      debounceTimer = setTimeout(function() {
        if (typeof onChange === 'function') {
          onChange(collapseShorthand(sideValues, currentUnit));
        }
      }, 16);
    }

    // Container
    var container = document.createElement('div');
    container.style.cssText = [
      'display: flex',
      'flex-direction: column',
      'gap: 4px'
    ].join(';');

    // Top row: link toggle + unit selector
    var topRow = document.createElement('div');
    topRow.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 6px'
    ].join(';');

    // Link toggle button
    var linkBtn = document.createElement('button');
    linkBtn.style.cssText = [
      'background: none',
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'cursor: pointer',
      'padding: 2px 6px',
      'font-size: 11px',
      'color: ' + (linked ? TOKENS.colors.primary : TOKENS.colors.textMuted),
      'display: flex',
      'align-items: center',
      'gap: 3px',
      'transition: all 0.15s ease',
      'flex-shrink: 0'
    ].join(';');
    linkBtn.innerHTML = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>';
    linkBtn.title = linked ? 'Unlink sides' : 'Link all sides';
    if (linked) {
      linkBtn.style.borderColor = TOKENS.colors.primary;
    }
    topRow.appendChild(linkBtn);

    // Unit selector
    var unitSelect = document.createElement('select');
    unitSelect.style.cssText = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 11px',
      'color: ' + TOKENS.colors.text,
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: 2px 2px',
      'outline: none',
      'cursor: pointer'
    ].join(';');
    for (var ui = 0; ui < units.length; ui++) {
      var opt = document.createElement('option');
      opt.value = units[ui];
      opt.textContent = units[ui];
      if (units[ui] === currentUnit) opt.selected = true;
      unitSelect.appendChild(opt);
    }
    unitSelect.addEventListener('focus', function() {
      unitSelect.style.borderColor = TOKENS.colors.primary;
    });
    unitSelect.addEventListener('blur', function() {
      unitSelect.style.borderColor = TOKENS.colors.border;
    });
    topRow.appendChild(unitSelect);

    container.appendChild(topRow);

    // Grid of 4 side inputs
    var inputGrid = document.createElement('div');
    inputGrid.style.cssText = [
      'display: grid',
      'grid-template-columns: 1fr 1fr',
      'gap: 3px'
    ].join(';');

    var sideInputs = [];

    function createSideInput(index) {
      var wrapper = document.createElement('div');
      wrapper.style.cssText = [
        'display: flex',
        'align-items: center',
        'gap: 3px',
        'position: relative'
      ].join(';');

      var label = document.createElement('span');
      label.style.cssText = [
        'font-size: 10px',
        'color: ' + TOKENS.colors.textMuted,
        'width: 10px',
        'text-align: right',
        'flex-shrink: 0',
        'text-transform: uppercase'
      ].join(';');
      label.textContent = SIDE_LABELS[index][0];
      label.title = SIDE_LABELS[index];
      wrapper.appendChild(label);

      var input = document.createElement('input');
      input.type = 'number';
      input.step = String(range.step);
      input.value = String(sideValues[index]);
      input.style.cssText = [
        'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
        'font-size: 12px',
        'color: ' + TOKENS.colors.text,
        'background: ' + TOKENS.colors.surface,
        'border: 1px solid ' + TOKENS.colors.border,
        'border-radius: ' + TOKENS.radius.sm,
        'padding: 2px 4px',
        'width: 100%',
        'min-width: 0',
        'outline: none',
        'box-sizing: border-box',
        '-moz-appearance: textfield'
      ].join(';');
      input.addEventListener('focus', function() {
        input.style.borderColor = TOKENS.colors.primary;
        showSliderPopup(wrapper, index);
      });
      input.addEventListener('blur', function() {
        input.style.borderColor = TOKENS.colors.border;
        // Delay hiding to allow slider interaction
        setTimeout(function() {
          if (activeSliderPopup && activeSliderPopup.sideIndex === index) {
            hideSliderPopup();
          }
        }, 200);
      });

      input.addEventListener('input', function() {
        var val = parseFloat(input.value);
        if (isNaN(val)) return;
        if (linked) {
          for (var si = 0; si < 4; si++) {
            sideValues[si] = val;
            sideInputs[si].value = String(val);
          }
        } else {
          sideValues[index] = val;
        }
        if (activeSliderPopup && activeSliderPopup.sideIndex === index) {
          activeSliderPopup.slider.value = String(val);
        }
        emitChange();
      });

      wrapper.appendChild(input);
      sideInputs.push(input);
      return wrapper;
    }

    // Create slider popup shown on individual input focus
    function showSliderPopup(wrapper, sideIndex) {
      hideSliderPopup();

      var popup = document.createElement('div');
      popup.style.cssText = [
        'position: absolute',
        'left: 0',
        'right: 0',
        'top: 100%',
        'margin-top: 2px',
        'background: ' + TOKENS.colors.surface,
        'border: 1px solid ' + TOKENS.colors.border,
        'border-radius: ' + TOKENS.radius.sm,
        'padding: 4px 6px',
        'box-shadow: 0 2px 8px rgba(0,0,0,0.1)',
        'z-index: 1'
      ].join(';');

      var slider = document.createElement('input');
      slider.type = 'range';
      slider.min = String(range.min);
      slider.max = String(range.max);
      slider.step = String(range.step);
      slider.value = String(sideValues[sideIndex]);
      slider.style.cssText = [
        'width: 100%',
        'height: 4px',
        'cursor: pointer'
      ].join(';');

      slider.addEventListener('input', function() {
        var val = parseFloat(slider.value);
        if (linked) {
          for (var si = 0; si < 4; si++) {
            sideValues[si] = val;
            sideInputs[si].value = String(val);
          }
        } else {
          sideValues[sideIndex] = val;
          sideInputs[sideIndex].value = String(val);
        }
        emitChange();
      });

      // Prevent blur on slider interaction
      slider.addEventListener('mousedown', function(e) {
        e.preventDefault();
      });

      popup.appendChild(slider);
      wrapper.appendChild(popup);
      activeSliderPopup = { el: popup, slider: slider, sideIndex: sideIndex };
    }

    function hideSliderPopup() {
      if (activeSliderPopup && activeSliderPopup.el.parentNode) {
        activeSliderPopup.el.parentNode.removeChild(activeSliderPopup.el);
      }
      activeSliderPopup = null;
    }

    // Build inputs in order: top, right, bottom, left
    for (var si = 0; si < 4; si++) {
      inputGrid.appendChild(createSideInput(si));
    }
    container.appendChild(inputGrid);

    // Link toggle handler
    linkBtn.addEventListener('click', function() {
      linked = !linked;
      linkBtn.style.color = linked ? TOKENS.colors.primary : TOKENS.colors.textMuted;
      linkBtn.style.borderColor = linked ? TOKENS.colors.primary : TOKENS.colors.border;
      linkBtn.title = linked ? 'Unlink sides' : 'Link all sides';
      if (linked) {
        // Sync all to top value
        var syncVal = sideValues[0];
        for (var si = 0; si < 4; si++) {
          sideValues[si] = syncVal;
          sideInputs[si].value = String(syncVal);
        }
        emitChange();
      }
    });

    // Unit selector: convert all values to the new unit
    unitSelect.addEventListener('change', function() {
      var newUnit = unitSelect.value;
      for (var si = 0; si < 4; si++) {
        sideValues[si] = convertUnit(sideValues[si], currentUnit, newUnit);
        sideInputs[si].value = String(sideValues[si]);
      }
      currentUnit = newUnit;
      // Recalculate range for new unit
      range = getRangeForProperty(property, sideValues[0]);
      for (var ri = 0; ri < sideInputs.length; ri++) {
        sideInputs[ri].step = String(range.step);
      }
      emitChange();
    });

    return container;
  }

  // Known enum value lists for CSS properties with discrete options.
  var ENUM_VALUES = {
    'display': ['none', 'block', 'inline', 'inline-block', 'flex', 'inline-flex', 'grid', 'inline-grid'],
    'position': ['static', 'relative', 'absolute', 'fixed', 'sticky'],
    'text-align': ['left', 'center', 'right', 'justify'],
    'flex-direction': ['row', 'row-reverse', 'column', 'column-reverse'],
    'justify-content': ['flex-start', 'flex-end', 'center', 'space-between', 'space-around', 'space-evenly'],
    'align-items': ['flex-start', 'flex-end', 'center', 'stretch', 'baseline'],
    'overflow': ['visible', 'hidden', 'scroll', 'auto', 'clip'],
    'overflow-x': ['visible', 'hidden', 'scroll', 'auto', 'clip'],
    'overflow-y': ['visible', 'hidden', 'scroll', 'auto', 'clip'],
    'visibility': ['visible', 'hidden', 'collapse'],
    'cursor': ['pointer', 'default', 'text', 'move', 'not-allowed', 'grab', 'grabbing', 'zoom-in', 'zoom-out', 'crosshair', 'wait', 'help', 'progress']
  };

  // Dropdown control for enum CSS properties.
  // Renders a styled <select> that fires onChange immediately on selection.
  function DropdownControl(property, value, onChange) {
    var options = ENUM_VALUES[property] || [];

    var select = document.createElement('select');
    select.style.cssText = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: 2px 6px',
      'outline: none',
      'cursor: pointer',
      'width: 100%',
      'box-sizing: border-box'
    ].join(';');

    // Populate options; include the current value even if not in the enum list
    var valueInList = false;
    for (var i = 0; i < options.length; i++) {
      var opt = document.createElement('option');
      opt.value = options[i];
      opt.textContent = options[i];
      if (options[i] === value) {
        opt.selected = true;
        valueInList = true;
      }
      select.appendChild(opt);
    }
    if (!valueInList && value) {
      var customOpt = document.createElement('option');
      customOpt.value = value;
      customOpt.textContent = value;
      customOpt.selected = true;
      select.insertBefore(customOpt, select.firstChild);
    }

    select.addEventListener('focus', function() {
      select.style.borderColor = TOKENS.colors.primary;
    });
    select.addEventListener('blur', function() {
      select.style.borderColor = TOKENS.colors.border;
    });
    select.addEventListener('change', function() {
      if (typeof onChange === 'function') {
        onChange(select.value);
      }
    });

    return select;
  }

  // Plain text input for CSS values that don't match any specific control.
  // Commits on Enter or blur; Escape reverts to the last applied value.
  function TextInput(value, onChange) {
    var lastApplied = value;

    var input = document.createElement('input');
    input.type = 'text';
    input.value = value;
    input.style.cssText = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: 2px 6px',
      'outline: none',
      'width: 100%',
      'box-sizing: border-box',
      'transition: border-color 0.15s ease'
    ].join(';');

    function commit() {
      var v = input.value;
      if (v !== lastApplied) {
        lastApplied = v;
        if (typeof onChange === 'function') {
          onChange(v);
        }
      }
    }

    input.addEventListener('focus', function() {
      input.style.borderColor = TOKENS.colors.primary;
    });
    input.addEventListener('blur', function() {
      input.style.borderColor = TOKENS.colors.border;
      commit();
    });
    input.addEventListener('keydown', function(e) {
      if (e.key === 'Enter') {
        commit();
        input.blur();
      } else if (e.key === 'Escape') {
        input.value = lastApplied;
        input.blur();
      }
    });

    return input;
  }

  // Shorthand CSS properties that accept multi-value (top right bottom left) input.
  var SHORTHAND_PROPERTIES = [
    'margin', 'padding'
  ];

  // Properties that accept unitless 0-1 float values.
  var UNITLESS_FLOAT_PROPERTIES = [
    'opacity', 'line-height'
  ];

  // Regex for numeric values with optional CSS unit.
  var NUMERIC_VALUE_RE = /^-?\d+\.?\d*(px|rem|em|%|vw|vh)?$/;

  // Detect the appropriate control type for a CSS property and value.
  // Returns {type: 'color'|'numeric'|'multivalue'|'dropdown'|'text', config: {...}}
  // where config contains control-specific parameters.
  function detectControlType(property, value) {
    var v = (value || '').trim();

    // 1. Enum properties -> DropdownControl
    if (ENUM_VALUES[property]) {
      return { type: 'dropdown', config: { options: ENUM_VALUES[property] } };
    }

    // 2. Shorthand properties -> MultiValueInput
    for (var si = 0; si < SHORTHAND_PROPERTIES.length; si++) {
      if (property === SHORTHAND_PROPERTIES[si]) {
        return { type: 'multivalue', config: {} };
      }
    }

    // 3. Color values -> ColorPicker
    if (detectColorFormat(v)) {
      return { type: 'color', config: { format: detectColorFormat(v) } };
    }

    // 4. Numeric + unit values -> NumericSlider
    if (NUMERIC_VALUE_RE.test(v)) {
      var numMatch = v.match(/^(-?\d+\.?\d*)(px|rem|em|%|vw|vh)?$/);
      var numVal = parseFloat(numMatch[1]);
      var numUnit = numMatch[2] || '';
      var numRange = getRangeForProperty(property, numVal);
      return { type: 'numeric', config: { value: numVal, unit: numUnit, range: numRange } };
    }

    // 5. Unitless float properties (opacity, line-height) -> NumericSlider
    //    Checked after unit regex so "24px" still gets the unit path above.
    for (var fi = 0; fi < UNITLESS_FLOAT_PROPERTIES.length; fi++) {
      if (property === UNITLESS_FLOAT_PROPERTIES[fi]) {
        var floatRange = getRangeForProperty(property, v);
        return { type: 'numeric', config: { value: parseFloat(v) || 0, unit: '', range: floatRange } };
      }
    }

    // 6. Fallback -> TextInput
    return { type: 'text', config: {} };
  }

  // Create and return the correct DOM control element for a CSS property and value.
  // onChange receives the new value string when the user edits.
  function renderControl(property, value, onChange) {
    var detected = detectControlType(property, value);

    switch (detected.type) {
      case 'dropdown':
        return DropdownControl(property, value, onChange);
      case 'multivalue':
        return MultiValueInput(property, value, onChange);
      case 'color':
        return ColorPicker(value, onChange);
      case 'numeric':
        return NumericSlider(property, detected.config.value, detected.config.unit, onChange);
      case 'text':
        return TextInput(value, onChange);
      default:
        return TextInput(value, onChange);
    }
  }

  // Apply a CSS variable edit: sets the property on the scope element,
  // captures the original value on first edit, and updates the changes array.
  function applyVariableEdit(name, newValue, scopeElement, scopeSelector) {
    var key = name + '|' + scopeSelector;

    // Capture original on first edit
    if (!state.originalValues[key]) {
      state.originalValues[key] = {
        value: getComputedStyle(scopeElement).getPropertyValue(name).trim(),
        scopeElement: scopeElement
      };
    }

    // Apply to DOM
    scopeElement.style.setProperty(name, newValue);

    // Update changes array: replace existing entry or add new
    var found = false;
    for (var i = 0; i < state.changes.length; i++) {
      if (state.changes[i].property === name && state.changes[i].scope === scopeSelector) {
        state.changes[i].current = newValue;
        found = true;
        break;
      }
    }
    if (!found) {
      state.changes.push({
        property: name,
        scope: scopeSelector,
        original: state.originalValues[key].value,
        current: newValue
      });
    }

    updateAttachButton();
  }

  // Reset a single CSS variable edit: removes the inline override and
  // removes the entry from the changes array.
  function resetVariableEdit(name, scopeSelector) {
    var key = name + '|' + scopeSelector;
    var orig = state.originalValues[key];
    if (orig) {
      orig.scopeElement.style.removeProperty(name);
      delete state.originalValues[key];
    }

    for (var i = state.changes.length - 1; i >= 0; i--) {
      if (state.changes[i].property === name && state.changes[i].scope === scopeSelector) {
        state.changes.splice(i, 1);
        break;
      }
    }

    updateAttachButton();
  }

  // Reset all CSS variable edits: removes all inline overrides and
  // clears the changes array.
  function resetAllVariables() {
    var keys = Object.keys(state.originalValues);
    for (var i = 0; i < keys.length; i++) {
      var orig = state.originalValues[keys[i]];
      var name = keys[i].split('|')[0];
      orig.scopeElement.style.removeProperty(name);
    }
    state.originalValues = {};
    state.changes = [];
    updateAttachButton();
  }

  // Check whether a variable has been edited
  function isVariableEdited(name, scopeSelector) {
    for (var i = 0; i < state.changes.length; i++) {
      if (state.changes[i].property === name && state.changes[i].scope === scopeSelector) {
        return true;
      }
    }
    return false;
  }

  // Render the CSS Variables section in the panel content area.
  // Returns the section element (or null if no variables found).
  function renderCSSVariablesSection(variables) {
    if (!variables || variables.length === 0) return null;

    var section = createSection('CSS Variables', variables.length, 0);
    var rowControls = [];

    for (var i = 0; i < variables.length; i++) {
      (function(v) {
        var row = document.createElement('div');
        row.style.cssText = [
          'display: flex',
          'align-items: center',
          'gap: 6px',
          'padding: 3px 0',
          'font-size: 12px',
          'line-height: 1.4'
        ].join(';');

        // Blue dot indicator (hidden by default)
        var dot = document.createElement('span');
        dot.style.cssText = [
          'width: 6px',
          'height: 6px',
          'border-radius: 50%',
          'background: ' + TOKENS.colors.primary,
          'flex-shrink: 0',
          'display: none'
        ].join(';');
        row.appendChild(dot);

        // Variable name
        var nameEl = document.createElement('span');
        nameEl.style.cssText = [
          'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
          'color: ' + TOKENS.colors.primary,
          'font-weight: 500',
          'white-space: nowrap',
          'flex-shrink: 0'
        ].join(';');
        nameEl.textContent = v.name;
        nameEl.title = v.name + ' (scope: ' + v.scopeSelector + ')';
        row.appendChild(nameEl);

        // Value container (holds display span or input)
        var valueContainer = document.createElement('span');
        valueContainer.style.cssText = [
          'flex: 1',
          'min-width: 0',
          'display: flex',
          'align-items: center'
        ].join(';');

        // Value display (clickable to edit)
        var valueEl = document.createElement('span');
        valueEl.style.cssText = [
          'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
          'color: ' + TOKENS.colors.text,
          'cursor: text',
          'overflow: hidden',
          'text-overflow: ellipsis',
          'white-space: nowrap',
          'flex: 1',
          'min-width: 0',
          'padding: 1px 3px',
          'border-radius: 3px',
          'border: 1px solid transparent'
        ].join(';');
        valueEl.textContent = v.value;
        valueEl.title = 'Click to edit';
        valueContainer.appendChild(valueEl);

        row.appendChild(valueContainer);

        // Per-variable reset button (hidden by default)
        var resetBtn = document.createElement('button');
        resetBtn.style.cssText = [
          'background: none',
          'border: none',
          'color: ' + TOKENS.colors.textMuted,
          'cursor: pointer',
          'padding: 0 2px',
          'font-size: 14px',
          'line-height: 1',
          'flex-shrink: 0',
          'display: none',
          'opacity: 0.6'
        ].join(';');
        resetBtn.innerHTML = '&#x21ba;';
        resetBtn.title = 'Reset to original';
        resetBtn.onmouseenter = function() { resetBtn.style.opacity = '1'; };
        resetBtn.onmouseleave = function() { resetBtn.style.opacity = '0.6'; };
        row.appendChild(resetBtn);

        // Scope label
        var scopeEl = document.createElement('span');
        scopeEl.style.cssText = [
          'font-size: 10px',
          'color: ' + TOKENS.colors.textMuted,
          'white-space: nowrap',
          'flex-shrink: 0'
        ].join(';');
        scopeEl.textContent = v.scopeSelector;
        row.appendChild(scopeEl);

        // Update visual state of this row based on edit status
        function updateRowState() {
          var edited = isVariableEdited(v.name, v.scopeSelector);
          dot.style.display = edited ? 'inline-block' : 'none';
          resetBtn.style.display = edited ? 'inline-block' : 'none';
          // Update section changed badge
          var changedCount = 0;
          for (var ci = 0; ci < state.changes.length; ci++) {
            // Count all variable changes (scope starts with -- prefix check via property)
            if (state.changes[ci].property.indexOf('--') === 0) {
              changedCount++;
            }
          }
          section.updateChanged(changedCount);
        }

        // Enter edit mode: replace value display with an input
        function startEditing() {
          if (valueContainer.querySelector('input')) return;

          var input = document.createElement('input');
          input.type = 'text';
          input.value = getComputedStyle(v.scopeElement).getPropertyValue(v.name).trim();
          input.style.cssText = [
            'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
            'font-size: 12px',
            'color: ' + TOKENS.colors.text,
            'background: ' + TOKENS.colors.surface,
            'border: 1px solid ' + TOKENS.colors.primary,
            'border-radius: 3px',
            'padding: 1px 3px',
            'outline: none',
            'width: 100%',
            'box-sizing: border-box'
          ].join(';');

          valueEl.style.display = 'none';
          valueContainer.appendChild(input);
          input.focus();
          input.select();

          function commitEdit() {
            var newValue = input.value.trim();
            var liveValue = getComputedStyle(v.scopeElement).getPropertyValue(v.name).trim();
            valueContainer.removeChild(input);
            valueEl.style.display = '';

            if (newValue && newValue !== liveValue) {
              applyVariableEdit(v.name, newValue, v.scopeElement, v.scopeSelector);
              valueEl.textContent = newValue;
            } else {
              // No change or empty: show current live value
              valueEl.textContent = liveValue;
            }
            updateRowState();
          }

          input.addEventListener('keydown', function(e) {
            if (e.key === 'Enter') {
              e.preventDefault();
              commitEdit();
            } else if (e.key === 'Escape') {
              e.preventDefault();
              valueContainer.removeChild(input);
              valueEl.style.display = '';
            }
          });

          input.addEventListener('blur', function() {
            // Guard: input may already be removed by keydown handler
            if (input.parentNode) {
              commitEdit();
            }
          });
        }

        valueEl.addEventListener('click', startEditing);

        // Per-variable reset
        resetBtn.addEventListener('click', function(e) {
          e.stopPropagation();
          resetVariableEdit(v.name, v.scopeSelector);
          valueEl.textContent = getComputedStyle(v.scopeElement).getPropertyValue(v.name).trim();
          updateRowState();
        });

        // Store controls for Reset All refresh
        rowControls.push({ updateRowState: updateRowState, valueEl: valueEl, variable: v });

        section.contentEl.appendChild(row);
      })(variables[i]);
    }

    // Expose a refresh method for Reset All to update all rows
    section.refreshRows = function() {
      for (var ri = 0; ri < rowControls.length; ri++) {
        var ctrl = rowControls[ri];
        ctrl.valueEl.textContent = getComputedStyle(ctrl.variable.scopeElement).getPropertyValue(ctrl.variable.name).trim();
        ctrl.updateRowState();
      }
    };

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

  // Return a copy of the current changes array
  function getChanges() {
    return state.changes.slice();
  }

  // Export public API
  window.__devtool_style_editor = {
    open: open,
    close: close,
    toggle: toggle,
    getState: getState,
    getChanges: getChanges,
    attachChanges: attachChanges,
    isOpen: isOpen,
    startSelection: startSelection,
    createSection: createSection,
    showPanel: showPanel,
    hidePanel: hidePanel,
    discoverCSSVariables: discoverCSSVariables,
    extractComputedStyles: extractComputedStyles,
    ColorPicker: ColorPicker,
    NumericSlider: NumericSlider,
    MultiValueInput: MultiValueInput,
    DropdownControl: DropdownControl,
    TextInput: TextInput,
    detectControlType: detectControlType,
    renderControl: renderControl
  };
})();

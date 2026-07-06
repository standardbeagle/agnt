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

  // Shared z-index layer scale (see ui-tokens.js) with load-failure fallbacks.
  var SE_Z = window.__devtoolTokens ? window.__devtoolTokens.z : { panel: 2147483644, critical: 2147483647 };

  // Cache for browser default computed styles per tag name.
  // Keys are uppercase tag names (e.g. 'DIV'), values are plain objects
  // mapping CSS property name to its default computed value string.
  var defaultStyleCache = {};

  // Design tokens matching indicator.js visual language. Colors come from the
  // shared ui-tokens module (light/dark picked at load via
  // prefers-color-scheme); fallback literals only cover the defensive
  // "ui-tokens failed to load" case.
  var TOKENS = {
    colors: (window.__devtoolTokens && window.__devtoolTokens.theme()) || {
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
    panelPosition: null,
    boxModelContainer: null,
    prevFocus: null
  };

  // Notify the indicator (shell frame under always-wrap, else this window)
  // that inspect-mode open/close state changed, so its inspect button can
  // re-render without polling isOpen() every second.
  function notifyInspectState() {
    try {
      var fn = window.__devtool_indicator_syncInspect;
      if (!fn && window.parent && window.parent !== window) {
        fn = window.parent.__devtool_indicator_syncInspect;
      }
      if (typeof fn === 'function') fn();
    } catch (e) { /* cross-origin parent — nothing to notify */ }
  }

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

  // Capture a screenshot of the given element's bounding rect via the binary
  // WebSocket pipeline. Returns the context ID synchronously; the actual PNG
  // bytes are sent asynchronously. Falls back to full-page capture when no
  // element is provided. Returns null when capture prerequisites are missing.
  function captureElementScreenshot(element) {
    if (typeof html2canvas === 'undefined') {
      return null;
    }

    var core = getCore();
    if (!core || !core.ws) return null;

    var ws = core.ws();
    if (!ws || ws.readyState !== WebSocket.OPEN) return null;

    var id = 'ctx_' + Date.now().toString(36) + Math.random().toString(36).substr(2, 5);

    // Calculate capture region from element bounding rect (absolute coords)
    var opts = {
      allowTaint: true,
      useCORS: true,
      logging: false,
      scrollX: 0,
      scrollY: 0,
      windowWidth: document.documentElement.scrollWidth,
      windowHeight: document.documentElement.scrollHeight
    };

    if (element) {
      var rect = element.getBoundingClientRect();
      opts.x = rect.left + window.scrollX;
      opts.y = rect.top + window.scrollY;
      opts.width = rect.width;
      opts.height = rect.height;
    }

    html2canvas(document.body, opts).then(function(canvas) {
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
      'z-index: ' + SE_Z.critical,
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
      // critical, not 2147483648: that literal overflows int32 and browsers
      // clamp it anyway; the bar renders above overlay siblings by DOM order.
      'z-index: ' + SE_Z.critical
    ].join(';');
    instructions.textContent = 'Click an element to inspect styles \u2022 ESC to cancel';
    overlay.appendChild(instructions);

    var hoveredElement = null;
    // rAF-batch layout reads triggered by mousemove. The mousemove handler
    // only records the latest cursor coords; the real work (elementFromPoint
    // + getBoundingClientRect) runs once per animation frame. rafId is
    // cancelled by the teardown hook attached to the overlay.
    var rafId = 0;
    var pendingMove = null;

    function processPendingMove() {
      rafId = 0;
      var move = pendingMove;
      pendingMove = null;
      if (!move) return;

      overlay.style.pointerEvents = 'none';
      var el = document.elementFromPoint(move.x, move.y);
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
    }

    overlay.addEventListener('mousemove', function(e) {
      pendingMove = { x: e.clientX, y: e.clientY };
      if (rafId) return; // coalesce multiple mousemove events into one frame
      rafId = requestAnimationFrame(processPendingMove);
    });

    overlay.__devtoolCancelRaf = function() {
      if (rafId) {
        cancelAnimationFrame(rafId);
        rafId = 0;
      }
      pendingMove = null;
    };

    overlay.addEventListener('click', function(e) {
      e.preventDefault();
      e.stopPropagation();

      if (!hoveredElement) return;

      var element = hoveredElement;
      var selector = getUtils().generateSelector(element);
      var xpath = generateXPath(element);

      // Remove overlay before screenshot so it does not appear in the capture
      removeOverlay();

      var screenshotId = captureElementScreenshot(element);

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
      if (typeof overlay.__devtoolCancelRaf === 'function') {
        overlay.__devtoolCancelRaf();
      }
      if (overlay.parentNode) {
        overlay.parentNode.removeChild(overlay);
      }
      if (window.__devtoolOverlayStack) {
        window.__devtoolOverlayStack.pop('style-editor-select');
      }
      document.removeEventListener('keydown', handleEscape);
      state.overlay = null;
      state.escapeHandler = null;
    }

    // Escape via the shared overlay stack (top-most surface only); the
    // document listener is the fallback when ui-tokens failed to load.
    if (window.__devtoolOverlayStack) {
      window.__devtoolOverlayStack.push('style-editor-select', function() {
        removeOverlay();
        if (state.isOpen && !state.panel) {
          state.isOpen = false;
        }
      });
    } else {
      document.addEventListener('keydown', handleEscape);
      state.escapeHandler = handleEscape;
    }
    state.overlay = overlay;
    // Mount into the devtool shadow root when available; elementFromPoint
    // hit-testing still resolves page elements (pointer-events toggling).
    (window.__devtoolGetMountRoot ? window.__devtoolGetMountRoot() : document.body).appendChild(overlay);
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
      'z-index: ' + SE_Z.panel,
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
        // Refresh content sections (panel-scoped — see showPanel note)
        var contentEl = state.panel ? state.panel.querySelector('#__devtool-style-content') : null;
        if (contentEl) {
          contentEl.innerHTML = '';
          state.sections = [];
          // Box model diagram at the top
          state.boxModelContainer = renderBoxModel(el, scrollToBoxModelInput);
          contentEl.appendChild(state.boxModelContainer);

          var variables = discoverCSSVariables(el);
          var varUsage = buildVariableUsageMap(el, variables);
          var varSection = renderCSSVariablesSection(variables, varUsage);
          if (varSection) {
            contentEl.appendChild(varSection);
          }
          var computedStyles = extractComputedStyles(el);
          var computedSections = renderComputedStylesSections(computedStyles);
          for (var csi = 0; csi < computedSections.length; csi++) {
            contentEl.appendChild(computedSections[csi]);
          }
          var reactSection = renderReactComponentSection(el);
          if (reactSection) {
            contentEl.appendChild(reactSection);
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
    attachBtn.onclick = function() { attachStyleEdits(); };
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
    btn.setAttribute('aria-label', title);
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
        try { window.__devtool_core.endDragShield(); } catch (e) { /* optional */ }
        titleBar.style.cursor = 'grab';

        if (dragged && state.panelPosition) {
          savePosition(state.panelPosition.x, state.panelPosition.y);
        }
      }

      try { window.__devtool_core.beginDragShield(); } catch (e) { /* optional */ }
      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
  }

  // Scroll to and focus the first input for the given box model region.
  // region is one of 'margin', 'border', 'padding'.
  function scrollToBoxModelInput(region) {
    // Panel-scoped lookup — getElementById cannot see into the shadow-root mount.
    var content = state.panel ? state.panel.querySelector('#__devtool-style-content') : null;
    if (!content) return;

    // Map region to the first CSS property name to find the matching input
    var target;
    if (region === 'margin') target = 'margin-top';
    else if (region === 'padding') target = 'padding-top';
    else if (region === 'border') target = 'border-top-width';
    else return;

    // Search sections for the property row. Sections track their contentEl
    // which may be collapsed (display: none). Expand it before scrolling.
    for (var si = 0; si < state.sections.length; si++) {
      var sect = state.sections[si];
      var rows = sect.contentEl.querySelectorAll('div');
      for (var ri = 0; ri < rows.length; ri++) {
        // The property name is in the second span (first is the dot indicator)
        var spans = rows[ri].querySelectorAll('span');
        var nameSpan = spans.length > 1 ? spans[1] : spans[0];
        if (!nameSpan || nameSpan.textContent !== target) continue;

        // Expand the section if collapsed
        if (sect.contentEl.style.display === 'none') {
          var hdr = sect.firstChild;
          if (hdr) hdr.click();
        }

        rows[ri].scrollIntoView({ behavior: 'smooth', block: 'center' });
        var focusTarget = rows[ri].querySelector('input, select');
        if (focusTarget) {
          setTimeout(function() { focusTarget.focus(); }, 150);
        }
        return;
      }
    }
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

    // Dialog semantics + programmatic focus target for open/close focus
    // management (non-modal: the page stays interactive).
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-modal', 'false');
    panel.setAttribute('aria-label', 'Style editor');
    panel.tabIndex = -1;

    // Mount into the devtool shadow root when available (style isolation).
    (window.__devtoolGetMountRoot ? window.__devtoolGetMountRoot() : document.body).appendChild(panel);
    state.panel = panel;

    // Move focus into the panel; restore to the previously focused element
    // on hidePanel().
    state.prevFocus = document.activeElement;
    try { panel.focus({ preventScroll: true }); } catch (e) { try { panel.focus(); } catch (e2) {} }

    // Populate content sections (panel-scoped lookup — getElementById cannot
    // see into the shadow-root mount)
    var content = panel.querySelector('#__devtool-style-content');
    if (content && element) {
      // Box model diagram at the top
      state.boxModelContainer = renderBoxModel(element, scrollToBoxModelInput);
      content.appendChild(state.boxModelContainer);

      var variables = discoverCSSVariables(element);
      var varUsage = buildVariableUsageMap(element, variables);
      var varSection = renderCSSVariablesSection(variables, varUsage);
      if (varSection) {
        content.appendChild(varSection);
      }

      var computedStyles = extractComputedStyles(element);
      var computedSections = renderComputedStylesSections(computedStyles);
      for (var csi = 0; csi < computedSections.length; csi++) {
        content.appendChild(computedSections[csi]);
      }

      var reactSection = renderReactComponentSection(element);
      if (reactSection) {
        content.appendChild(reactSection);
      }
    }

    // Set up outside-click-to-close (when not pinned)
    // Use setTimeout to avoid the current click event from triggering dismissal
    setTimeout(function() {
      state.outsideClickHandler = function(e) {
        if (state.pinned) return;
        if (!state.panel) return;
        if (state.selecting) return;
        // Use composedPath() for constant-time "is event inside panel" check
        // instead of Node.contains() which walks the DOM tree on each call.
        var path = typeof e.composedPath === 'function' ? e.composedPath() : null;
        if (path && path.indexOf(state.panel) !== -1) return;
        if (!path && state.panel.contains(e.target)) return;
        // Ignore clicks on devtool elements
        if (e.target.id && e.target.id.indexOf('__devtool') === 0) return;
        close();
      };
      document.addEventListener('mousedown', state.outsideClickHandler, true);
    }, 0);

    // Set up Escape to close panel — via the shared overlay stack when
    // available (top-most surface only), else a document-level fallback.
    if (window.__devtoolOverlayStack) {
      window.__devtoolOverlayStack.push('style-editor-panel', function() {
        if (!state.selecting) close();
      });
    } else {
      state.panelEscapeHandler = function(e) {
        if (e.key === 'Escape' && !state.selecting) {
          close();
        }
      };
      document.addEventListener('keydown', state.panelEscapeHandler);
    }
  }

  // Remove the panel from the DOM and clean up listeners
  function hidePanel() {
    if (window.__devtoolOverlayStack) {
      window.__devtoolOverlayStack.pop('style-editor-panel');
    }
    if (state.panel && state.panel.parentNode) {
      state.panel.parentNode.removeChild(state.panel);
    }
    state.panel = null;
    state.sections = [];
    state.boxModelContainer = null;

    // Restore focus to wherever the user was before the panel opened.
    if (state.prevFocus && typeof state.prevFocus.focus === 'function' && state.prevFocus.isConnected) {
      try { state.prevFocus.focus({ preventScroll: true }); } catch (e) { try { state.prevFocus.focus(); } catch (e2) {} }
    }
    state.prevFocus = null;

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

  // Build a reverse map: varName -> [propertyName, ...] showing which CSS
  // properties on the element reference each variable via var(--name).
  // Scans CSS rules matching the element and inline styles for var() usage.
  function buildVariableUsageMap(element, variables) {
    if (!element || !variables || variables.length === 0) return {};

    var usage = {};
    for (var vi = 0; vi < variables.length; vi++) {
      usage[variables[vi].name] = [];
    }

    var varNames = Object.keys(usage);
    if (varNames.length === 0) return usage;

    // Check if a CSS value string references any known variable
    function scanValue(value, property) {
      if (!value || value.indexOf('var(') < 0) return;
      for (var ni = 0; ni < varNames.length; ni++) {
        var name = varNames[ni];
        // Match var(--name) or var(--name, fallback)
        if (value.indexOf('var(' + name + ')') >= 0 ||
            value.indexOf('var(' + name + ',') >= 0) {
          // Avoid duplicates
          var list = usage[name];
          var dup = false;
          for (var di = 0; di < list.length; di++) {
            if (list[di] === property) { dup = true; break; }
          }
          if (!dup) list.push(property);
        }
      }
    }

    // 1. Scan element's inline style
    var style = element.style;
    for (var si = 0; si < style.length; si++) {
      var prop = style[si];
      if (prop.indexOf('--') === 0) continue;
      scanValue(style.getPropertyValue(prop), prop);
    }

    // 2. Scan matching CSS rules from stylesheets
    function scanRulesForUsage(rules) {
      for (var ri = 0; ri < rules.length; ri++) {
        var rule = rules[ri];
        if (rule.cssRules) {
          scanRulesForUsage(rule.cssRules);
          continue;
        }
        if (rule.type !== CSSRule.STYLE_RULE) continue;
        var rs = rule.style;
        if (!rs) continue;

        // Check if this rule matches the element
        try {
          if (!element.matches(rule.selectorText)) continue;
        } catch (e) {
          continue;
        }

        for (var pi = 0; pi < rs.length; pi++) {
          var rp = rs[pi];
          if (rp.indexOf('--') === 0) continue;
          scanValue(rs.getPropertyValue(rp), rp);
        }
      }
    }

    for (var shi = 0; shi < document.styleSheets.length; shi++) {
      var sheet = document.styleSheets[shi];
      var cssRules;
      try {
        cssRules = sheet.cssRules || sheet.rules;
      } catch (e) {
        continue;
      }
      if (cssRules) scanRulesForUsage(cssRules);
    }

    return usage;
  }

  // Box model colors matching Chrome DevTools conventions.
  var BOX_MODEL_COLORS = {
    margin:  { bg: 'rgba(246, 178, 107, 0.6)', label: '#c47a2a' },
    border:  { bg: 'rgba(252, 220, 140, 0.6)', label: '#a38b2e' },
    padding: { bg: 'rgba(147, 196, 125, 0.6)', label: '#4a7a2e' },
    content: { bg: 'rgba(140, 181, 221, 0.7)', label: '#3a6a9a' }
  };

  // Box model property groups for reading computed values.
  var BOX_MODEL_SIDES = ['top', 'right', 'bottom', 'left'];

  // Render an interactive box model diagram for the given element.
  // Returns a container element with a .refresh() method for live updates.
  // onClickRegion(regionName) is called when a region is clicked.
  function renderBoxModel(element, onClickRegion) {
    var container = document.createElement('div');
    container.style.cssText = [
      'padding: ' + TOKENS.spacing.md,
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'background: ' + TOKENS.colors.surfaceAlt
    ].join(';');

    var diagram = document.createElement('div');
    diagram.style.cssText = [
      'position: relative',
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 10px',
      'line-height: 1',
      'user-select: none'
    ].join(';');

    // Read computed box model values from the element.
    function readValues() {
      var cs = getComputedStyle(element);
      var vals = { margin: [], border: [], padding: [] };
      for (var si = 0; si < BOX_MODEL_SIDES.length; si++) {
        var side = BOX_MODEL_SIDES[si];
        vals.margin.push(parseFloat(cs.getPropertyValue('margin-' + side)) || 0);
        vals.border.push(parseFloat(cs.getPropertyValue('border-' + side + '-width')) || 0);
        vals.padding.push(parseFloat(cs.getPropertyValue('padding-' + side)) || 0);
      }
      vals.width = Math.round(parseFloat(cs.getPropertyValue('width')) || 0);
      vals.height = Math.round(parseFloat(cs.getPropertyValue('height')) || 0);
      return vals;
    }

    // Create a single box layer (margin, border, or padding).
    // Returns {el, labels: [top, right, bottom, left]} for updating.
    function createLayer(name, color) {
      var box = document.createElement('div');
      box.style.cssText = [
        'position: relative',
        'background: ' + color.bg,
        'display: flex',
        'flex-direction: column',
        'align-items: center',
        'cursor: pointer'
      ].join(';');

      box.addEventListener('click', function(e) {
        // Only fire if this layer is the direct target (not a child layer)
        if (e.target === box || e.target === boxLabel || e.target.parentNode === box) {
          e.stopPropagation();
          if (onClickRegion) onClickRegion(name);
        }
      });

      // Layer label (top-left corner)
      var boxLabel = document.createElement('span');
      boxLabel.style.cssText = [
        'position: absolute',
        'top: 2px',
        'left: 4px',
        'font-size: 9px',
        'color: ' + color.label,
        'font-weight: 600',
        'pointer-events: none'
      ].join(';');
      boxLabel.textContent = name;
      box.appendChild(boxLabel);

      // Side value labels: top, right, bottom, left
      var labels = [];
      var positions = [
        { pos: 'top: 2px; left: 50%; transform: translateX(-50%)' },
        { pos: 'top: 50%; right: 4px; transform: translateY(-50%)' },
        { pos: 'bottom: 2px; left: 50%; transform: translateX(-50%)' },
        { pos: 'top: 50%; left: 4px; transform: translateY(-50%)' }
      ];

      for (var li = 0; li < 4; li++) {
        var lbl = document.createElement('span');
        lbl.style.cssText = [
          'position: absolute',
          positions[li].pos,
          'font-size: 10px',
          'color: ' + color.label,
          'font-weight: 500',
          'pointer-events: none',
          'white-space: nowrap'
        ].join(';');
        lbl.textContent = '0';
        box.appendChild(lbl);
        labels.push(lbl);
      }

      return { el: box, labels: labels };
    }

    // Create the content box (innermost)
    var contentBox = document.createElement('div');
    contentBox.style.cssText = [
      'background: ' + BOX_MODEL_COLORS.content.bg,
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'min-height: 32px',
      'font-size: 11px',
      'font-weight: 600',
      'color: ' + BOX_MODEL_COLORS.content.label,
      'white-space: nowrap'
    ].join(';');
    contentBox.addEventListener('click', function(e) { e.stopPropagation(); });
    var contentLabel = document.createElement('span');
    contentLabel.style.cssText = 'pointer-events: none';
    contentLabel.textContent = '0 \u00d7 0';
    contentBox.appendChild(contentLabel);

    // Build layers from outside in: margin > border > padding > content
    var marginLayer = createLayer('margin', BOX_MODEL_COLORS.margin);
    var borderLayer = createLayer('border', BOX_MODEL_COLORS.border);
    var paddingLayer = createLayer('padding', BOX_MODEL_COLORS.padding);

    // Nest: padding wraps content, border wraps padding, margin wraps border
    paddingLayer.el.appendChild(contentBox);
    borderLayer.el.appendChild(paddingLayer.el);
    marginLayer.el.appendChild(borderLayer.el);
    diagram.appendChild(marginLayer.el);
    container.appendChild(diagram);

    // Apply padding to each layer to make space for the value labels.
    function applyLayerPadding(layer, values) {
      var t = Math.max(16, values[0] > 0 ? 16 : 14);
      var r = Math.max(24, values[1] > 0 ? 24 : 20);
      var b = Math.max(14, values[2] > 0 ? 14 : 12);
      var l = Math.max(24, values[3] > 0 ? 24 : 20);
      layer.el.style.padding = t + 'px ' + r + 'px ' + b + 'px ' + l + 'px';
    }

    // Format a numeric value for display: strip trailing zeros, show '-' for 0
    function fmtVal(n) {
      var rounded = Math.round(n * 100) / 100;
      return rounded === 0 ? '-' : String(rounded);
    }

    // Update all labels from current computed styles.
    function refresh() {
      var v = readValues();

      for (var si = 0; si < 4; si++) {
        marginLayer.labels[si].textContent = fmtVal(v.margin[si]);
        borderLayer.labels[si].textContent = fmtVal(v.border[si]);
        paddingLayer.labels[si].textContent = fmtVal(v.padding[si]);
      }

      contentLabel.textContent = v.width + ' \u00d7 ' + v.height;

      applyLayerPadding(marginLayer, v.margin);
      applyLayerPadding(borderLayer, v.border);
      applyLayerPadding(paddingLayer, v.padding);
    }

    // Initial render
    refresh();

    container.refresh = refresh;
    return container;
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

  // ---------------------------------------------------------------------------
  // Shared design-state sync (panel side)
  //
  // Mirror of the palette's outbound dispatch path. When the user edits an
  // inline style here, we emit a 'devtool:design-state' CustomEvent so the
  // mini palette (palette.js) can update its scrubber/swatch in place. We
  // also subscribe so palette-originated edits flow back into our row
  // controls without re-rendering the whole panel.
  //
  // Loop guard: detail.source === 'panel' for our dispatches; the listener
  // ignores 'panel' events. applyingRemoteDesignState additionally
  // suppresses the outbound dispatch from applyInlineStyleEdit when the
  // edit itself originates from an inbound event — both guards are
  // needed because applyInlineStyleEdit also runs from internal panel
  // controls that we cannot easily tell apart from remote application.
  // ---------------------------------------------------------------------------
  // Wire-protocol literals ('devtool:design-state', 'panel') are inline at
  // dispatch/subscribe sites so grep across modules turns up both ends of
  // the contract. The mini palette uses 'palette' as its source tag.
  var applyingRemoteDesignState = false;
  var designStateListener = null;

  function dispatchDesignStateEdit(property, value) {
    if (applyingRemoteDesignState) return;
    try {
      document.dispatchEvent(new CustomEvent('devtool:design-state', {
        detail: {
          source: 'panel',
          kind: 'edit',
          selector: state.selector || null,
          change: { property: property, value: value }
        }
      }));
    } catch (e) {
      // Legacy IE without CustomEvent constructor — degrade silently.
    }
  }

  function dispatchDesignStateDeselect() {
    if (applyingRemoteDesignState) return;
    try {
      document.dispatchEvent(new CustomEvent('devtool:design-state', {
        detail: { source: 'panel', kind: 'deselect' }
      }));
    } catch (e) { /* ignore */ }
  }

  // Refresh the visible value of a property row after a remote edit.
  // Addresses the row's control container by dataset (set in buildPropertyRow)
  // so the lookup survives re-renders that change inline style strings.
  // If the property is not currently rendered (e.g. category collapsed),
  // the next open of that section will read the live value naturally.
  function refreshRowForProperty(property) {
    if (!state.panel || !state.selectedElement) return;
    // Property names are CSS-safe (lowercase letters + hyphens) so direct
    // attribute-selector interpolation is safe. We restrict to property
    // names matching /^[a-z-]+$/ to defend against accidental injection.
    if (!/^[a-z-]+$/.test(property)) return;
    var controlContainer = state.panel.querySelector(
      '[data-property-control="' + property + '"]'
    );
    if (!controlContainer) return;
    // Re-render with the new value pulled live from the element. The
    // control rebuild path mirrors Reset's behaviour so we know it
    // produces a control consistent with the rest of the panel.
    var liveValue = getComputedStyle(state.selectedElement).getPropertyValue(property);
    controlContainer.innerHTML = '';
    var freshControl = renderControl(property, liveValue, function(newValue) {
      applyInlineStyleEdit(property, newValue);
    });
    controlContainer.appendChild(freshControl);
  }

  function attachDesignStateListener() {
    if (designStateListener) return;
    designStateListener = function(e) {
      if (!e || !e.detail) return;
      // Loop guard: ignore our own emits. Literal 'panel' source duplicated
      // here so grep across modules surfaces both producer and consumer.
      if (e.detail.source === 'panel') return;
      if (!state.isOpen) return;

      if (e.detail.kind === 'deselect') {
        // Palette closed (Escape, click-outside, delete) — close the panel
        // in sync. applyingRemoteDesignState suppresses our deselect
        // re-broadcast so the round-trip terminates.
        applyingRemoteDesignState = true;
        try { close(); } finally { applyingRemoteDesignState = false; }
        return;
      }

      if (e.detail.kind === 'select' && e.detail.element) {
        // Palette mounted on a new element — re-target the panel without
        // closing/reopening so the user's panel position is preserved.
        // applyingRemoteDesignState suppresses our own re-broadcast.
        applyingRemoteDesignState = true;
        try {
          state.selectedElement = e.detail.element;
          state.selector = e.detail.selector || (getUtils() && getUtils().generateSelector
            ? getUtils().generateSelector(e.detail.element) : null);
          state.changes = [];
          state.originalValues = {};
          // Re-render panel content for the new element. hidePanel +
          // showPanel rebuilds sections from scratch, which is the
          // simplest correct behaviour and matches what open() does.
          if (state.panel) {
            hidePanel();
            showPanel(e.detail.element, state.selector);
          }
        } finally {
          applyingRemoteDesignState = false;
        }
        return;
      }

      if (e.detail.kind === 'edit' && e.detail.change && state.selectedElement) {
        // Mirror the palette's edit into the panel: write the inline
        // style + re-render the matching row's control. We bypass
        // applyInlineStyleEdit's outbound dispatch via the guard so the
        // event doesn't ricochet back to the palette.
        applyingRemoteDesignState = true;
        try {
          applyInlineStyleEdit(e.detail.change.property, e.detail.change.value);
          refreshRowForProperty(e.detail.change.property);
        } finally {
          applyingRemoteDesignState = false;
        }
      }
    };
    document.addEventListener('devtool:design-state', designStateListener);
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
    if (state.boxModelContainer && state.boxModelContainer.refresh) {
      state.boxModelContainer.refresh();
    }

    // Notify the mini palette so it can mirror this change. The
    // applyingRemoteDesignState guard skips the dispatch when this edit is
    // itself the result of a palette-originated event we're applying.
    // See the design-state listener below for the shared sync wiring.
    dispatchDesignStateEdit(property, newValue);
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

  // Reset a single inline style edit: removes the inline override and
  // removes the entry from the changes array.
  function resetInlineStyleEdit(property) {
    var element = state.selectedElement;
    if (!element) return;

    var key = property + '|inline';
    if (state.originalValues[key]) {
      delete state.originalValues[key];
    }

    element.style.removeProperty(property);

    for (var i = state.changes.length - 1; i >= 0; i--) {
      if (state.changes[i].property === property && state.changes[i].scope === 'inline') {
        state.changes.splice(i, 1);
        break;
      }
    }

    updateAttachButton();
    if (state.boxModelContainer && state.boxModelContainer.refresh) {
      state.boxModelContainer.refresh();
    }
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
          // Tag the row + control container with the property name so the
          // shared design-state listener can address them by attribute
          // selector (refreshRowForProperty) without relying on inline
          // style-string matching, which is brittle across re-renders.
          row.dataset.propertyRow = entry.property;
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

          // WYSIWYG scrubber: for numeric rows, turn the property-name label
          // into a drag/wheel handle so users can adjust the value without
          // typing. Detection mirrors detectControlType so we only attach
          // the affordance to rows that have a meaningful numeric value.
          // Commit flows through applyInlineStyleEdit (the same write path
          // the per-row controls use), so the design-state event dispatches
          // and the mini palette mirrors the change in real time.
          var scrubDetected = detectControlType(entry.property, entry.value);
          if (scrubDetected.type === 'numeric') {
            // Step heuristic: opacity / line-height want 0.01 per pixel of
            // drag for fine-grained control; integer-valued properties get
            // 1 per pixel which feels natural for px / % / rem ranges
            // typical of the panel.
            var scrubStep = (scrubDetected.config && scrubDetected.config.range && scrubDetected.config.range.step < 1) ? 0.01 : 1;
            attachScrubber(nameEl, {
              readValue: function() {
                // Read from the live element rather than the cached entry
                // value so the scrubber composes with mid-session edits
                // (palette changes, undo, the per-row reset button).
                if (!state.selectedElement) return entry.value;
                return getComputedStyle(state.selectedElement).getPropertyValue(entry.property).trim();
              },
              onScrub: function(newValue) {
                applyInlineStyleEdit(entry.property, newValue);
                dot.style.display = 'inline-block';
                resetBtn.style.display = 'inline-block';
                section.updateChanged(countCategoryChanges(entries));
                // Reflect the new value in the per-row control without a
                // full row rebuild — keeps the scrub feel snappy.
                refreshRowForProperty(entry.property);
              },
              onCommit: function(newValue) {
                applyInlineStyleEdit(entry.property, newValue);
                refreshRowForProperty(entry.property);
              },
              onRevert: function(originalValue) {
                applyInlineStyleEdit(entry.property, originalValue);
                refreshRowForProperty(entry.property);
              },
              step: scrubStep
            });
            // Tooltip hint so the affordance is discoverable on hover for
            // users who don't know the ew-resize cursor convention.
            nameEl.title = 'Drag or scroll to scrub ' + entry.property + ' (Shift = ×10, Esc = revert)';
          }

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
          controlContainer.dataset.propertyControl = entry.property;
          controlContainer.style.cssText = [
            'flex: 1',
            'min-width: 0',
            'overflow: hidden'
          ].join(';');

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
            'display: ' + (isInlineStyleEdited(entry.property) ? 'inline-block' : 'none'),
            'opacity: 0.6'
          ].join(';');
          resetBtn.innerHTML = '&#x21ba;';
          resetBtn.title = 'Reset to original';
          resetBtn.onmouseenter = function() { resetBtn.style.opacity = '1'; };
          resetBtn.onmouseleave = function() { resetBtn.style.opacity = '0.6'; };

          var control = renderControl(entry.property, entry.value, function(newValue) {
            applyInlineStyleEdit(entry.property, newValue);
            dot.style.display = 'inline-block';
            resetBtn.style.display = 'inline-block';
            section.updateChanged(countCategoryChanges(entries));
          });
          controlContainer.appendChild(control);
          row.appendChild(controlContainer);

          resetBtn.addEventListener('click', function(e) {
            e.stopPropagation();
            resetInlineStyleEdit(entry.property);
            dot.style.display = 'none';
            resetBtn.style.display = 'none';
            // Replace control with fresh one showing restored value
            var liveValue = getComputedStyle(state.selectedElement).getPropertyValue(entry.property);
            controlContainer.innerHTML = '';
            var freshControl = renderControl(entry.property, liveValue, function(newValue) {
              applyInlineStyleEdit(entry.property, newValue);
              dot.style.display = 'inline-block';
              resetBtn.style.display = 'inline-block';
              section.updateChanged(countCategoryChanges(entries));
            });
            controlContainer.appendChild(freshControl);
            section.updateChanged(countCategoryChanges(entries));
          });
          row.appendChild(resetBtn);

          rowControls.push({ row: row, dot: dot, resetBtn: resetBtn, controlContainer: controlContainer, property: entry.property });
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

        // Expose a refresh method for rows (used by Reset All)
        section.refreshRows = function() {
          for (var ri = 0; ri < rowControls.length; ri++) {
            var ctrl = rowControls[ri];
            var edited = isInlineStyleEdited(ctrl.property);
            ctrl.dot.style.display = edited ? 'inline-block' : 'none';
            ctrl.resetBtn.style.display = edited ? 'inline-block' : 'none';
            if (!edited && state.selectedElement) {
              // Re-render control with restored computed value
              var liveValue = getComputedStyle(state.selectedElement).getPropertyValue(ctrl.property);
              ctrl.controlContainer.innerHTML = '';
              var freshControl = renderControl(ctrl.property, liveValue, (function(prop, dot, resetBtn) {
                return function(newValue) {
                  applyInlineStyleEdit(prop, newValue);
                  dot.style.display = 'inline-block';
                  resetBtn.style.display = 'inline-block';
                  section.updateChanged(countCategoryChanges(entries));
                };
              })(ctrl.property, ctrl.dot, ctrl.resetBtn));
              ctrl.controlContainer.appendChild(freshControl);
            }
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

  // ---------------------------------------------------------------------------
  // attachScrubber(target, opts) -- WYSIWYG drag/wheel scrubber.
  //
  // Wires the standard "scrub by drag or wheel" UX onto a target element so
  // numeric CSS properties can be edited directly from the inspect panel
  // without typing into the number input. Modeled on the affordance used by
  // Chrome devtools' Styles pane, Photoshop, and Figma: hover shows
  // ew-resize cursor, horizontal drag adjusts value by px-of-drag * step,
  // wheel adjusts by ±step per tick (×10 with shift), Escape mid-scrub
  // reverts to the pre-scrub value.
  //
  // opts:
  //   readValue()           — return the current value string (e.g. '16px',
  //                           '1.5rem', '0.8'). Re-read on each scrub start
  //                           so external mutations stay in sync.
  //   onScrub(newValueStr)  — called continuously during drag/wheel to apply
  //                           the live preview. Should route through
  //                           applyInlineStyleEdit so design-state mirrors.
  //   onCommit(newValueStr) — called on pointerup / wheel idle / blur with
  //                           the final value. Often the same callback as
  //                           onScrub; kept distinct so callers can debounce
  //                           heavy commit work (state.changes ledger update,
  //                           updateAttachButton, etc.).
  //   onRevert(originalStr) — called on Escape; receives the value captured
  //                           at scrub start so the caller can restore both
  //                           DOM and panel state.
  //   step (optional)       — units of value mutated per pixel of drag /
  //                           per wheel tick. Default 1. Pass 0.01 for
  //                           opacity-style fine-grained controls.
  //
  // The scrubber preserves the trailing unit verbatim by parsing with
  // NUMERIC_VALUE_RE — dragging '1.5rem' yields '1.6rem', never '1.6px'.
  // Unitless values (raw numbers) round-trip without a unit suffix.
  //
  // Returns a teardown function that removes the listeners (used when the
  // panel rebuilds rows so we don't accumulate handlers on detached nodes).
  // ---------------------------------------------------------------------------
  function attachScrubber(target, opts) {
    if (!target || !opts) return function() {};
    var readValue = opts.readValue || function() { return '0'; };
    var onScrub = opts.onScrub || function() {};
    var onCommit = opts.onCommit || onScrub;
    var onRevert = opts.onRevert || function() {};
    var stepBase = typeof opts.step === 'number' && opts.step > 0 ? opts.step : 1;

    // Make the target obviously interactive: ew-resize is the
    // industry-standard scrub cursor (Chrome devtools, Figma, Photoshop).
    // We append rather than replace so existing cursor declarations on the
    // element are overridden but related styles are preserved.
    try {
      target.style.cursor = 'ew-resize';
      target.style.userSelect = 'none';
      // Pointer events default to allowing native drag-select on text; the
      // scrubber needs the pointer events but not the selection, so
      // suppress selection explicitly.
      target.style.webkitUserSelect = 'none';
    } catch (e) { /* style setter may throw in unusual hosts; ignore */ }

    // Parse a CSS numeric value into {num, unit}. Falls back to {num: 0,
    // unit: ''} for unparseable input. Reuses NUMERIC_VALUE_RE so the
    // unit set tracked here stays in lockstep with detectControlType.
    function parseNumeric(str) {
      var s = String(str || '').trim();
      if (!NUMERIC_VALUE_RE.test(s)) return { num: 0, unit: '' };
      var m = s.match(/^(-?\d+\.?\d*)(px|rem|em|%|vw|vh)?$/);
      if (!m) return { num: 0, unit: '' };
      return { num: parseFloat(m[1]), unit: m[2] || '' };
    }

    function formatNumeric(num, unit) {
      // Round to 2 decimals to avoid floating-point noise; matches the
      // precision used by NumericSlider's formatOutput.
      var rounded = Math.round(num * 100) / 100;
      return unit ? rounded + unit : String(rounded);
    }

    // Per-scrub session state. Captured on pointerdown / first wheel and
    // cleared on pointerup / Escape so concurrent scrubs can't interleave.
    var session = null;
    var keydownAttached = false;
    var wheelIdleTimer = null;

    function startSession() {
      var initial = parseNumeric(readValue());
      session = {
        startNum: initial.num,
        currentNum: initial.num,
        unit: initial.unit,
        originalStr: formatNumeric(initial.num, initial.unit)
      };
      attachKeydown();
    }

    function endSession(commit) {
      if (!session) return;
      var finalStr = formatNumeric(session.currentNum, session.unit);
      var s = session;
      session = null;
      detachKeydown();
      if (commit) {
        onCommit(finalStr);
      } else {
        onRevert(s.originalStr);
      }
    }

    // Pointer drag: track horizontal delta. requestPointerLock would be
    // even smoother (infinite drag distance) but adds permission UX
    // friction; the simple delta-from-start model matches the rest of
    // the panel's interaction style.
    var dragStartX = 0;
    var pointerCaptured = false;

    function onPointerDown(e) {
      // Left button only; ignore right-click context menu, middle-click
      // paste, and synthetic events without buttons (some test harnesses).
      if (e.button !== 0) return;
      // If a wheel-idle session is still pending, commit it before the
      // drag begins — the wheel ticks were intentional edits, so they
      // should be preserved. Then start a fresh drag session whose
      // baseline reads the post-wheel value.
      if (session) {
        if (wheelIdleTimer) {
          clearTimeout(wheelIdleTimer);
          wheelIdleTimer = null;
        }
        endSession(true);
      }
      startSession();
      dragStartX = e.clientX;
      pointerCaptured = false;
      try {
        // Pointer capture lets us keep receiving move events even if the
        // pointer leaves the target's bounding box mid-drag.
        if (target.setPointerCapture && typeof e.pointerId === 'number') {
          target.setPointerCapture(e.pointerId);
          pointerCaptured = true;
        }
      } catch (err) { /* setPointerCapture throws on stale pointers */ }
      // preventDefault suppresses native text-selection drag behaviour
      // that would otherwise hijack the drag gesture on the label.
      if (e.preventDefault) e.preventDefault();
    }

    function onPointerMove(e) {
      if (!session) return;
      var dx = e.clientX - dragStartX;
      // Shift accelerates by 10×, matching Chrome devtools and Figma.
      var step = stepBase * (e.shiftKey ? 10 : 1);
      session.currentNum = session.startNum + dx * step;
      onScrub(formatNumeric(session.currentNum, session.unit));
    }

    function onPointerUp(e) {
      if (!session) return;
      try {
        if (pointerCaptured && target.releasePointerCapture && typeof e.pointerId === 'number') {
          target.releasePointerCapture(e.pointerId);
        }
      } catch (err) { /* releasePointerCapture can throw if capture lost */ }
      pointerCaptured = false;
      endSession(true);
    }

    // Wheel: each tick is one step (or 10 with shift). passive: false is
    // mandatory because we need preventDefault() to suppress page scroll
    // — without it the page jumps every time the user scrubs over a
    // property and the scrubber feels broken. The wheelIdleTimer treats
    // a pause as commit so the design-state mirror gets a final value.
    function onWheel(e) {
      if (!session) startSession();
      // deltaY > 0 means scrolling down; convention matches Chrome
      // devtools where down decrements. Inverted on macOS natural
      // scroll? No — we use the raw direction; users with natural
      // scroll get the inverted feel they already expect from other
      // scrubbers (Figma, Photoshop) on macOS.
      var direction = e.deltaY > 0 ? -1 : 1;
      var step = stepBase * (e.shiftKey ? 10 : 1);
      session.currentNum = session.currentNum + direction * step;
      onScrub(formatNumeric(session.currentNum, session.unit));
      if (e.preventDefault) e.preventDefault();
      // Reset the idle timer on every tick; commit ~250ms after the
      // last tick so a continuous spin batches into one logical edit.
      if (wheelIdleTimer) clearTimeout(wheelIdleTimer);
      wheelIdleTimer = setTimeout(function() {
        wheelIdleTimer = null;
        endSession(true);
      }, 250);
    }

    // Escape revert: bound on document during an active session so the
    // user doesn't need to focus the scrub handle first. Detached on
    // session end to keep the global keydown surface lean.
    function onKeyDown(e) {
      if (!session) return;
      if (e.key === 'Escape') {
        if (wheelIdleTimer) {
          clearTimeout(wheelIdleTimer);
          wheelIdleTimer = null;
        }
        endSession(false);
        if (e.preventDefault) e.preventDefault();
      }
    }

    function attachKeydown() {
      if (keydownAttached) return;
      document.addEventListener('keydown', onKeyDown, true);
      keydownAttached = true;
    }

    function detachKeydown() {
      if (!keydownAttached) return;
      document.removeEventListener('keydown', onKeyDown, true);
      keydownAttached = false;
    }

    target.addEventListener('pointerdown', onPointerDown);
    target.addEventListener('pointermove', onPointerMove);
    target.addEventListener('pointerup', onPointerUp);
    target.addEventListener('pointercancel', onPointerUp);
    // passive: false required so onWheel can preventDefault page scroll.
    target.addEventListener('wheel', onWheel, { passive: false });

    return function teardown() {
      target.removeEventListener('pointerdown', onPointerDown);
      target.removeEventListener('pointermove', onPointerMove);
      target.removeEventListener('pointerup', onPointerUp);
      target.removeEventListener('pointercancel', onPointerUp);
      target.removeEventListener('wheel', onWheel);
      detachKeydown();
      if (wheelIdleTimer) {
        clearTimeout(wheelIdleTimer);
        wheelIdleTimer = null;
      }
      if (session) session = null;
    };
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
  function renderCSSVariablesSection(variables, usageMap) {
    if (!variables || variables.length === 0) return null;
    if (!usageMap) usageMap = {};

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

        // Usage hint: show which properties reference this variable
        var props = usageMap[v.name];
        if (props && props.length > 0) {
          var hint = document.createElement('div');
          hint.style.cssText = [
            'font-size: 10px',
            'color: ' + TOKENS.colors.textMuted,
            'padding: 0 0 2px 18px',
            'line-height: 1.3'
          ].join(';');
          hint.textContent = '\u2514 used in: ' + props.join(', ') +
            ' (' + props.length + (props.length === 1 ? ' usage' : ' usages') + ')';
          section.contentEl.appendChild(hint);
        }
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

  // Show a brief "Copied!" toast near the cursor position.
  function showCopiedToast(anchorEl) {
    var toast = document.createElement('div');
    toast.style.cssText = [
      'position: fixed',
      'z-index: ' + SE_Z.critical,
      'background: ' + TOKENS.colors.text,
      'color: ' + TOKENS.colors.textInverse,
      'font-size: 11px',
      'padding: 3px 8px',
      'border-radius: ' + TOKENS.radius.sm,
      'pointer-events: none',
      'opacity: 1',
      'transition: opacity 0.3s ease'
    ].join(';');
    toast.textContent = 'Copied!';
    (window.__devtoolGetMountRoot ? window.__devtoolGetMountRoot() : document.body).appendChild(toast);

    var rect = anchorEl.getBoundingClientRect();
    toast.style.left = rect.left + 'px';
    toast.style.top = (rect.top - 24) + 'px';

    setTimeout(function() { toast.style.opacity = '0'; }, 600);
    setTimeout(function() {
      if (toast.parentNode) toast.parentNode.removeChild(toast);
    }, 900);
  }

  // Determine the display type of a prop value.
  function propType(val) {
    if (val === null || val === undefined) return 'string';
    if (Array.isArray(val)) return 'array';
    var t = typeof val;
    if (t === 'object') return 'object';
    if (t === 'function') return 'function';
    if (t === 'boolean') return 'boolean';
    if (t === 'number') return 'number';
    return 'string';
  }

  // Format a prop value for display based on its type.
  function formatPropValue(val, type) {
    if (type === 'string') {
      var s = String(val == null ? '' : val);
      if (s.length > 50) s = s.slice(0, 50) + '\u2026';
      return '"' + s + '"';
    }
    if (type === 'boolean') return String(val);
    if (type === 'number') return String(val);
    if (type === 'function') {
      var name = val.displayName || val.name || '';
      return '\u0192 ' + name + '()';
    }
    if (type === 'array') return '[' + val.length + ' item' + (val.length !== 1 ? 's' : '') + ']';
    if (type === 'object') {
      var keys = Object.keys(val);
      return '{' + keys.length + ' key' + (keys.length !== 1 ? 's' : '') + '}';
    }
    return String(val);
  }

  // Create a type badge element for a prop row.
  function createTypeBadge(type) {
    var badge = document.createElement('span');
    badge.style.cssText = [
      'font-size: 9px',
      'color: ' + TOKENS.colors.textMuted,
      'background: ' + TOKENS.colors.surfaceAlt,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: 0 3px',
      'flex-shrink: 0',
      'line-height: 1.6',
      'text-transform: lowercase'
    ].join(';');
    badge.textContent = type;
    return badge;
  }

  // Render expanded JSON content for an object or array prop value.
  function renderExpandedValue(val) {
    var pre = document.createElement('pre');
    pre.style.cssText = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 11px',
      'color: ' + TOKENS.colors.text,
      'background: ' + TOKENS.colors.surfaceAlt,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: 6px 8px',
      'margin: 4px 0 2px 0',
      'overflow-x: auto',
      'max-height: 200px',
      'white-space: pre-wrap',
      'word-break: break-all'
    ].join(';');
    try {
      pre.textContent = JSON.stringify(val, null, 2);
    } catch (e) {
      pre.textContent = String(val);
    }
    return pre;
  }

  // Render a single prop row with name, type badge, value, expand/copy behavior.
  function renderPropRow(propName, propValue) {
    var type = propType(propValue);
    var isExpandable = type === 'object' || type === 'array';
    var isCopyable = !isExpandable && type !== 'function';
    var expanded = false;

    var wrapper = document.createElement('div');

    var row = document.createElement('div');
    row.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 6px',
      'padding: 3px 0',
      'font-size: 12px',
      'line-height: 1.4',
      'cursor: ' + (isExpandable || isCopyable ? 'pointer' : 'default')
    ].join(';');

    // Prop name
    var nameEl = document.createElement('span');
    nameEl.style.cssText = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'color: ' + TOKENS.colors.primary,
      'font-weight: 500',
      'white-space: nowrap',
      'flex-shrink: 0'
    ].join(';');
    nameEl.textContent = propName;
    row.appendChild(nameEl);

    // Type badge
    row.appendChild(createTypeBadge(type));

    // Value
    var valueEl = document.createElement('span');
    var valueStyles = [
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 11px',
      'flex: 1',
      'min-width: 0',
      'overflow: hidden',
      'text-overflow: ellipsis',
      'white-space: nowrap'
    ];

    if (type === 'boolean') {
      valueStyles.push('color: ' + TOKENS.colors.textMuted);
    } else if (type === 'function') {
      valueStyles.push('color: ' + TOKENS.colors.textMuted);
      valueStyles.push('font-style: italic');
    } else if (isExpandable) {
      valueStyles.push('color: ' + TOKENS.colors.textMuted);
    } else {
      valueStyles.push('color: ' + TOKENS.colors.text);
    }

    valueEl.style.cssText = valueStyles.join(';');
    valueEl.textContent = formatPropValue(propValue, type);
    row.appendChild(valueEl);

    // Expand indicator for objects/arrays
    if (isExpandable) {
      var arrow = document.createElement('span');
      arrow.style.cssText = [
        'display: flex',
        'align-items: center',
        'transition: transform 0.15s ease',
        'color: ' + TOKENS.colors.textMuted,
        'flex-shrink: 0',
        'transform: rotate(-90deg)'
      ].join(';');
      arrow.innerHTML = ICONS.chevron;
      row.appendChild(arrow);
    }

    wrapper.appendChild(row);

    // Expand container for objects/arrays
    var expandContainer = null;
    if (isExpandable) {
      expandContainer = document.createElement('div');
      expandContainer.style.display = 'none';
      wrapper.appendChild(expandContainer);
    }

    // Click handler
    row.addEventListener('click', function() {
      if (isExpandable && expandContainer) {
        expanded = !expanded;
        if (expanded) {
          expandContainer.innerHTML = '';
          expandContainer.appendChild(renderExpandedValue(propValue));
          expandContainer.style.display = 'block';
          arrow.style.transform = 'rotate(0deg)';
        } else {
          expandContainer.style.display = 'none';
          arrow.style.transform = 'rotate(-90deg)';
        }
      } else if (isCopyable) {
        var text = type === 'string' ? String(propValue == null ? '' : propValue) : String(propValue);
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text);
        }
        showCopiedToast(valueEl);
      }
    });

    // Hover feedback
    row.onmouseenter = function() { row.style.background = TOKENS.colors.surfaceAlt; };
    row.onmouseleave = function() { row.style.background = 'transparent'; };

    return wrapper;
  }

  // Capture surrounding DOM context (parent and siblings) for AI context.
  function captureContextHTML(element) {
    var parent = element.parentElement;
    if (!parent) return element.outerHTML.substring(0, 2000);
    var clone = parent.cloneNode(true);
    // Strip script/style content and deeply nested children to keep context compact
    var scripts = clone.querySelectorAll('script, style');
    for (var i = 0; i < scripts.length; i++) scripts[i].remove();
    var html = clone.outerHTML;
    if (html.length > 4000) html = html.substring(0, 4000) + '...';
    return html;
  }

  // Build a serializable props snapshot, converting non-JSON-safe values to descriptors.
  function serializeProps(props) {
    var result = {};
    var keys = Object.keys(props);
    for (var i = 0; i < keys.length; i++) {
      var key = keys[i];
      if (key === 'children') continue;
      var val = props[key];
      if (typeof val === 'function') {
        result[key] = '[Function]';
      } else if (val instanceof Element) {
        result[key] = '[Element: ' + val.tagName.toLowerCase() + ']';
      } else {
        try { JSON.stringify(val); result[key] = val; }
        catch (e) { result[key] = String(val); }
      }
    }
    return result;
  }

  // Send React component data to the AI agent via the indicator compose tab.
  function sendReactPropsToAgent(element, info) {
    var indicator = window.__devtool_indicator;
    if (!indicator || !indicator.addAttachment) return;

    var propsSnapshot = serializeProps(info.props);
    var contextHTML = captureContextHTML(element);

    var reactProps = {
      component: info.componentName,
      props: propsSnapshot,
      source: info.source || null,
      contextHTML: contextHTML
    };

    indicator.addAttachment('style-edit', {
      label: info.componentName + ' React props',
      summary: info.componentName + ' component props + source location',
      selector: state.selector || '',
      changes: state.changes.slice(),
      screenshots: { before: state.beforeScreenshotId || null, after: null },
      reactProps: reactProps
    });

    indicator.setMessage('Update the ' + info.componentName + ' component:');
    indicator.switchTab('compose');
    indicator.togglePanel(true);
  }

  // Render a collapsible section displaying React component info for the selected element.
  // Returns null if no React component is detected.
  function renderReactComponentSection(element) {
    var info = detectReactComponent(element);
    if (!info) return null;

    var propKeys = Object.keys(info.props);
    var sectionTitle = 'React: ' + info.componentName;
    var section = createSection(sectionTitle, propKeys.length, 0);

    // Source row (if available)
    if (info.source && info.source.fileName) {
      var srcRow = document.createElement('div');
      srcRow.style.cssText = [
        'padding: 2px 0 4px 0',
        'font-size: 11px',
        'line-height: 1.4'
      ].join(';');

      var srcValue = document.createElement('span');
      srcValue.style.cssText = [
        'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
        'color: ' + TOKENS.colors.textMuted,
        'font-size: 11px',
        'word-break: break-all'
      ].join(';');
      var srcText = info.source.fileName;
      if (info.source.lineNumber) srcText += ':' + info.source.lineNumber;
      srcValue.textContent = srcText;
      srcRow.appendChild(srcValue);

      section.contentEl.appendChild(srcRow);
    }

    // Props list
    for (var i = 0; i < propKeys.length; i++) {
      var key = propKeys[i];
      // Skip children prop (React internal) and key/ref
      if (key === 'children') continue;
      section.contentEl.appendChild(renderPropRow(key, info.props[key]));
    }

    // "Edit via AI" button
    var editBtn = document.createElement('button');
    editBtn.style.cssText = [
      'width: 100%',
      'margin-top: 6px',
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
    editBtn.textContent = 'Edit via AI';
    editBtn.onmouseenter = function() { editBtn.style.background = TOKENS.colors.primaryDark; };
    editBtn.onmouseleave = function() { editBtn.style.background = TOKENS.colors.primary; };
    editBtn.onclick = function() { sendReactPropsToAgent(element, info); };
    section.contentEl.appendChild(editBtn);

    return section;
  }

  // Update the attach button label to reflect current change count
  function updateAttachButton() {
    var btn = state.panel ? state.panel.querySelector('#__devtool-style-attach-btn') : null;
    if (btn) {
      btn.textContent = 'Attach Changes (' + state.changes.length + ')';
    }
  }

  // Open the style editor for a given element or enter selection mode
  function open(element) {
    if (state.isOpen) return;
    state.isOpen = true;
    notifyInspectState();

    if (element) {
      state.selectedElement = element;
      state.selector = getUtils().generateSelector(element);
      state.xpath = generateXPath(element);
      state.beforeScreenshotId = captureElementScreenshot(element);
      showPanel(element, state.selector);
      dispatchPaletteShow(element);
    } else {
      startSelection(function(el, selector, xpath, screenshotId) {
        state.selectedElement = el;
        state.selector = selector;
        state.xpath = xpath;
        state.beforeScreenshotId = screenshotId;
        showPanel(el, selector);
        dispatchPaletteShow(el);
      });
    }
  }

  // Inspect mode pairs the full panel with the inline quick-style palette
  // (palette.js — the p/bg/op/disp bar). They sync via 'devtool:design-state'
  // (see the design-state listener above). Design mode does NOT drive the
  // palette; only Inspect does, via this dispatch.
  function dispatchPaletteShow(element) {
    if (!element) return;
    try {
      document.dispatchEvent(new CustomEvent('devtool:palette-show', {
        detail: { element: element, source: 'style-editor' }
      }));
    } catch (e) {
      // CustomEvent constructor unsupported in some legacy IE — degrade silently.
    }
  }

  // Close the style editor and clean up
  function close() {
    if (!state.isOpen) return;
    var hadSelection = !!state.selectedElement;
    state.isOpen = false;
    notifyInspectState();
    state.selectedElement = null;
    state.selector = null;
    state.xpath = null;
    state.beforeScreenshotId = null;
    state.changes = [];
    state.originalValues = {};
    state.pinned = false;

    // Notify the mini palette so it can hide in sync. Only dispatch when
    // we actually had a selection — close() can also fire from a no-op
    // toggle path, and a stray deselect would race the palette listener.
    if (hadSelection) {
      dispatchDesignStateDeselect();
    }

    if (state.selecting) {
      state.selecting = false;
      if (state.overlay) {
        if (typeof state.overlay.__devtoolCancelRaf === 'function') {
          state.overlay.__devtoolCancelRaf();
        }
        if (state.overlay.parentNode) {
          state.overlay.parentNode.removeChild(state.overlay);
        }
      }
      if (state.escapeHandler) {
        document.removeEventListener('keydown', state.escapeHandler);
      }
      if (window.__devtoolOverlayStack) {
        window.__devtoolOverlayStack.pop('style-editor-select');
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

  // Capture an "after" screenshot and add a style-edit attachment to the indicator
  function attachStyleEdits() {
    if (!state.selectedElement || state.changes.length === 0) return;

    var indicator = window.__devtool_indicator;
    if (!indicator || !indicator.addAttachment) return;

    var afterScreenshotId = captureElementScreenshot(state.selectedElement);
    var changeCount = state.changes.length;
    var selectorLabel = state.selector || 'element';

    indicator.addAttachment('style-edit', {
      label: selectorLabel + ': ' + changeCount + ' change' + (changeCount !== 1 ? 's' : ''),
      summary: selectorLabel + ': ' + changeCount + ' style change' + (changeCount !== 1 ? 's' : ''),
      selector: state.selector,
      changes: state.changes.slice(),
      screenshots: { before: state.beforeScreenshotId, after: afterScreenshotId }
    });
  }

  // Check whether the editor is currently open
  function isOpen() {
    return state.isOpen;
  }

  // Return a copy of the current changes array
  function getChanges() {
    return state.changes.slice();
  }

  // Find the React fiber key on an element.
  // React attaches internal data using a property like
  // __reactFiber$xxxx (React 18+) or __reactInternalInstance$xxxx (React 16-17).
  // Returns the key string or null.
  function findFiberKey(el) {
    var keys = Object.keys(el);
    for (var i = 0; i < keys.length; i++) {
      if (keys[i].indexOf('__reactFiber$') === 0 ||
          keys[i].indexOf('__reactInternalInstance$') === 0) {
        return keys[i];
      }
    }
    return null;
  }

  // Unwrap React.memo / React.forwardRef wrappers to get the actual component type.
  // These wrappers nest the real component under .type or .render.
  function unwrapType(fiberType) {
    if (!fiberType) return fiberType;
    // React.memo wraps in { $$typeof: Symbol(react.memo), type: inner }
    if (fiberType.$$typeof && fiberType.type) return unwrapType(fiberType.type);
    // React.forwardRef wraps in { $$typeof: Symbol(react.forward_ref), render: fn }
    if (fiberType.$$typeof && fiberType.render) return fiberType.render;
    return fiberType;
  }

  // Extract a human-readable component name from a fiber node.
  function getComponentName(fiber) {
    if (!fiber || !fiber.type) return null;
    var t = unwrapType(fiber.type);
    if (typeof t === 'string') return null; // native DOM element like 'div'
    if (typeof t === 'function') return t.displayName || t.name || null;
    if (typeof t === 'object' && t !== null) return t.displayName || t.name || null;
    return null;
  }

  // Detect the React component associated with a DOM element.
  // Walks up the DOM tree to find the nearest fiber, then walks up the fiber
  // tree to find the nearest component boundary (non-host fiber).
  // Returns null if no React detected, or {componentName, props, source, hasState}.
  function detectReactComponent(element) {
    if (!element || !element.nodeType) return null;

    // Walk up DOM to find an element that has a fiber key
    var fiberKey = null;
    var el = element;
    while (el && el.nodeType === 1) {
      fiberKey = findFiberKey(el);
      if (fiberKey) break;
      el = el.parentElement;
    }
    if (!fiberKey) return null;

    // Get the fiber node from the DOM element
    var fiber = el[fiberKey];
    if (!fiber) return null;

    // Walk up the fiber tree to find the nearest component boundary.
    // Host fibers (tag 5 = HostComponent, tag 6 = HostText) represent DOM nodes;
    // we want the first ancestor whose type is a function or object (a React component).
    var current = fiber;
    while (current) {
      var name = getComponentName(current);
      if (name) {
        var source = null;
        if (current._debugSource) {
          source = {
            fileName: current._debugSource.fileName || null,
            lineNumber: current._debugSource.lineNumber || null
          };
        }
        return {
          componentName: name,
          props: current.memoizedProps || {},
          source: source,
          hasState: current.memoizedState !== null && current.memoizedState !== undefined
        };
      }
      current = current.return;
    }

    return null;
  }

  // Export public API
  window.__devtool_style_editor = {
    open: open,
    close: close,
    toggle: toggle,
    getState: getState,
    getChanges: getChanges,
    attachChanges: attachChanges,
    attachStyleEdits: attachStyleEdits,
    isOpen: isOpen,
    startSelection: startSelection,
    createSection: createSection,
    showPanel: showPanel,
    hidePanel: hidePanel,
    discoverCSSVariables: discoverCSSVariables,
    buildVariableUsageMap: buildVariableUsageMap,
    extractComputedStyles: extractComputedStyles,
    ColorPicker: ColorPicker,
    NumericSlider: NumericSlider,
    MultiValueInput: MultiValueInput,
    DropdownControl: DropdownControl,
    TextInput: TextInput,
    detectControlType: detectControlType,
    renderControl: renderControl,
    detectReactComponent: detectReactComponent,
    renderBoxModel: renderBoxModel,
    serializeProps: serializeProps,
    captureContextHTML: captureContextHTML,
    sendReactPropsToAgent: sendReactPropsToAgent
  };

  // Attach the cross-surface design-state listener at module load. Idempotent
  // by guard inside attachDesignStateListener — double-loads of the bundle
  // (e.g. hot-reload) won't double-subscribe.
  attachDesignStateListener();
})();

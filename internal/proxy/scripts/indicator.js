// Floating Indicator for DevTool
// Redesigned with visual hierarchy and Gestalt principles
// Attachments are logged first, then referenced in messages

(function() {
  'use strict';

  var core = window.__devtool_core;
  var utils = window.__devtool_utils;

  // Generate unique IDs for attachments
  function generateId() {
    return 'ctx_' + Date.now().toString(36) + Math.random().toString(36).substr(2, 5);
  }

  // State
  var state = {
    container: null,
    bug: null,
    panel: null,
    isExpanded: false,
    isDragging: false,
    dragOffset: { x: 0, y: 0 },
    position: { x: 20, y: 20 },
    isVisible: true,
    isActive: false, // AI tool activity state
    activityTimeout: null,
    requestNotification: false, // Request notification when task completes
    // Attachments are now logged items with references
    attachments: [] // { id, type, label, summary, timestamp }
  };

  // Design tokens - consistent visual language
  var TOKENS = {
    colors: {
      primary: '#6366f1',      // Indigo
      primaryDark: '#4f46e5',
      secondary: '#64748b',    // Slate
      success: '#22c55e',
      error: '#ef4444',
      active: '#f59e0b',       // Amber - for activity state
      surface: '#ffffff',
      surfaceAlt: '#f8fafc',
      border: '#e2e8f0',
      text: '#1e293b',
      textMuted: '#64748b',
      textInverse: '#ffffff'
    },
    radius: {
      sm: '6px',
      md: '10px',
      lg: '14px',
      full: '9999px'
    },
    shadow: {
      sm: '0 1px 2px rgba(0,0,0,0.05)',
      md: '0 4px 12px rgba(0,0,0,0.1)',
      lg: '0 10px 40px rgba(0,0,0,0.15)',
      glow: '0 0 20px rgba(99,102,241,0.3)'
    },
    spacing: {
      xs: '4px',
      sm: '8px',
      md: '12px',
      lg: '16px',
      xl: '20px'
    }
  };

  // Styles
  var STYLES = {
    // The floating bug - entry point
    bug: [
      'position: fixed',
      'width: 52px',
      'height: 52px',
      'border-radius: ' + TOKENS.radius.full,
      'background: ' + TOKENS.colors.primary,
      'box-shadow: ' + TOKENS.shadow.lg + ', ' + TOKENS.shadow.glow,
      'cursor: pointer',
      'z-index: 2147483646',
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'transition: transform 0.2s ease, box-shadow 0.2s ease',
      'user-select: none'
    ].join(';'),

    statusDot: [
      'position: absolute',
      'top: 0',
      'right: 0',
      'width: 14px',
      'height: 14px',
      'border-radius: ' + TOKENS.radius.full,
      'border: 2.5px solid ' + TOKENS.colors.surface,
      'transition: background-color 0.3s ease'
    ].join(';'),

    // Activity ring - pulses when AI is working
    activityRing: [
      'position: absolute',
      'top: -4px',
      'left: -4px',
      'right: -4px',
      'bottom: -4px',
      'border-radius: ' + TOKENS.radius.full,
      'border: 2px solid ' + TOKENS.colors.active,
      'opacity: 0',
      'pointer-events: none'
    ].join(';'),

    // Panel - the main interface
    panel: [
      'position: fixed',
      'width: 380px',
      'background: ' + TOKENS.colors.surface,
      'border-radius: ' + TOKENS.radius.lg,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483645',
      'overflow: hidden',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      'font-size: 14px',
      'color: ' + TOKENS.colors.text,
      'transition: opacity 0.2s ease, transform 0.2s ease'
    ].join(';'),

    // Header - minimal, functional
    header: [
      'display: flex',
      'align-items: center',
      'justify-content: space-between',
      'padding: ' + TOKENS.spacing.md + ' ' + TOKENS.spacing.lg,
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-bottom: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    headerTitle: [
      'font-weight: 600',
      'font-size: 13px',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px'
    ].join(';'),

    closeBtn: [
      'background: none',
      'border: none',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'padding: 4px',
      'border-radius: ' + TOKENS.radius.sm,
      'display: flex',
      'transition: background 0.15s ease'
    ].join(';'),

    // Compose area - the main content
    compose: [
      'padding: ' + TOKENS.spacing.lg
    ].join(';'),

    // Message card - groups message + attachments (Gestalt: Common Region)
    messageCard: [
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.md,
      'background: ' + TOKENS.colors.surface,
      'overflow: hidden',
      'transition: border-color 0.2s ease, box-shadow 0.2s ease'
    ].join(';'),

    messageCardFocused: [
      'border-color: ' + TOKENS.colors.primary,
      'box-shadow: 0 0 0 3px rgba(99,102,241,0.1)'
    ].join(';'),

    // Text input within card
    textarea: [
      'width: 100%',
      'min-height: 80px',
      'padding: ' + TOKENS.spacing.md,
      'border: none',
      'outline: none',
      'resize: none',
      'font-size: 14px',
      'font-family: inherit',
      'line-height: 1.5',
      'color: ' + TOKENS.colors.text,
      'background: transparent',
      'box-sizing: border-box'
    ].join(';'),

    // Attachment chips area (Gestalt: Proximity - grouped with message)
    attachmentArea: [
      'padding: 0 ' + TOKENS.spacing.md + ' ' + TOKENS.spacing.md,
      'display: flex',
      'flex-wrap: wrap',
      'gap: ' + TOKENS.spacing.sm
    ].join(';'),

    // Individual attachment chip
    chip: [
      'display: inline-flex',
      'align-items: center',
      'gap: 6px',
      'padding: 5px 10px',
      'background: ' + TOKENS.colors.surfaceAlt,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.full,
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'max-width: 200px',
      'overflow: hidden'
    ].join(';'),

    chipIcon: [
      'flex-shrink: 0',
      'width: 14px',
      'height: 14px'
    ].join(';'),

    chipLabel: [
      'white-space: nowrap',
      'overflow: hidden',
      'text-overflow: ellipsis'
    ].join(';'),

    chipRemove: [
      'flex-shrink: 0',
      'background: none',
      'border: none',
      'padding: 0',
      'cursor: pointer',
      'color: ' + TOKENS.colors.textMuted,
      'display: flex',
      'transition: color 0.15s ease'
    ].join(';'),

    // Toolbar - secondary actions (Gestalt: Similarity)
    toolbar: [
      'display: flex',
      'align-items: center',
      'gap: ' + TOKENS.spacing.sm,
      'padding: ' + TOKENS.spacing.sm + ' ' + TOKENS.spacing.md,
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-top: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    toolBtn: [
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'gap: 5px',
      'padding: 7px 12px',
      'background: transparent',
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 12px',
      'font-weight: 500',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'transition: all 0.15s ease'
    ].join(';'),

    // Notification toggle
    notifyToggle: [
      'display: flex',
      'align-items: center',
      'gap: 6px',
      'margin-left: auto',
      'margin-right: 8px',
      'padding: 4px 8px',
      'font-size: 12px',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'user-select: none'
    ].join(';'),

    // Primary send button - visual hierarchy (most prominent)
    sendBtn: [
      'display: flex',
      'align-items: center',
      'gap: 6px',
      'padding: 8px 16px',
      'background: ' + TOKENS.colors.primary,
      'border: none',
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 13px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textInverse,
      'cursor: pointer',
      'transition: background 0.15s ease, transform 0.1s ease'
    ].join(';'),

    // Selection overlays
    overlay: [
      'position: fixed',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'z-index: 2147483647',
      'cursor: crosshair'
    ].join(';'),

    overlayDimmed: [
      'background: rgba(0, 0, 0, 0.4)'
    ].join(';'),

    selectionBox: [
      'position: absolute',
      'border: 2px solid ' + TOKENS.colors.primary,
      'background: rgba(99, 102, 241, 0.15)',
      'border-radius: 4px',
      'pointer-events: none'
    ].join(';'),

    elementHighlight: [
      'position: absolute',
      'border: 2px solid ' + TOKENS.colors.primary,
      'background: rgba(99, 102, 241, 0.1)',
      'pointer-events: none',
      'border-radius: 4px',
      'z-index: 2147483647'
    ].join(';'),

    tooltip: [
      'position: absolute',
      'background: ' + TOKENS.colors.text,
      'color: ' + TOKENS.colors.textInverse,
      'padding: 4px 8px',
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 11px',
      'font-family: ui-monospace, monospace',
      'white-space: nowrap',
      'pointer-events: none'
    ].join(';'),

    // Instructions bar during selection
    instructionBar: [
      'position: fixed',
      'bottom: 20px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: ' + TOKENS.colors.text,
      'color: ' + TOKENS.colors.textInverse,
      'padding: 10px 20px',
      'border-radius: ' + TOKENS.radius.full,
      'font-size: 13px',
      'font-weight: 500',
      'z-index: 2147483647',
      'box-shadow: ' + TOKENS.shadow.lg
    ].join(';')
  };

  // Icons (compact SVGs)
  var ICONS = {
    logo: '<svg width="24" height="24" viewBox="0 0 24 24" fill="white"><path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg>',
    close: '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6L6 18M6 6l12 12"/></svg>',
    send: '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z"/></svg>',
    screenshot: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="M21 15l-5-5L5 21"/></svg>',
    element: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>',
    sketch: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 19l7-7 3 3-7 7-3-3z"/><path d="M18 13l-1.5-7.5L2 2l3.5 14.5L13 18l5-5z"/><path d="M2 2l7.586 7.586"/><circle cx="11" cy="11" r="2"/></svg>',
    x: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M18 6L6 18M6 6l12 12"/></svg>'
  };

  // Initialize
  function init() {
    if (state.container) return;
    loadPrefs();
    createUI();
    setupStatusPolling();
  }

  function createUI() {
    state.container = document.createElement('div');
    state.container.id = '__devtool-indicator';
    if (!state.isVisible) state.container.style.display = 'none';

    createBug();
    createPanel();

    document.documentElement.appendChild(state.container);
  }

  function createBug() {
    var bug = document.createElement('div');
    bug.style.cssText = STYLES.bug;
    bug.style.left = state.position.x + 'px';
    bug.style.bottom = state.position.y + 'px';
    bug.innerHTML = ICONS.logo;

    // Activity ring (pulses when AI is working)
    var ring = document.createElement('div');
    ring.id = '__devtool-activity-ring';
    ring.style.cssText = STYLES.activityRing;
    bug.appendChild(ring);

    // Inject CSS animation for pulse effect
    injectActivityAnimation();

    // Status indicator
    var dot = document.createElement('div');
    dot.id = '__devtool-status';
    dot.style.cssText = STYLES.statusDot;
    dot.style.backgroundColor = core.isConnected() ? TOKENS.colors.success : TOKENS.colors.error;
    bug.appendChild(dot);

    // Drag and click handling
    bug.addEventListener('mousedown', handleDragStart);
    bug.addEventListener('mouseenter', function() {
      if (!state.isDragging) {
        bug.style.transform = 'scale(1.08)';
      }
    });
    bug.addEventListener('mouseleave', function() {
      if (!state.isDragging) {
        bug.style.transform = 'scale(1)';
      }
    });

    state.bug = bug;
    state.container.appendChild(bug);
  }

  // Inject CSS keyframes for activity animation
  function injectActivityAnimation() {
    if (document.getElementById('__devtool-activity-style')) return;

    var style = document.createElement('style');
    style.id = '__devtool-activity-style';
    style.textContent = [
      '@keyframes __devtool-pulse {',
      '  0% { transform: scale(1); opacity: 0.8; }',
      '  50% { transform: scale(1.15); opacity: 0.4; }',
      '  100% { transform: scale(1.3); opacity: 0; }',
      '}',
      '.__devtool-active {',
      '  animation: __devtool-pulse 1.5s ease-out infinite;',
      '}'
    ].join('\\n');
    document.head.appendChild(style);
  }

  // Set activity state (called when AI tool becomes active/idle)
  function setActivityState(isActive) {
    state.isActive = isActive;
    var ring = document.getElementById('__devtool-activity-ring');
    if (!ring) return;

    if (isActive) {
      ring.classList.add('__devtool-active');
      ring.style.opacity = '1';
    } else {
      ring.classList.remove('__devtool-active');
      ring.style.opacity = '0';
    }
  }

  function createPanel() {
    var panel = document.createElement('div');
    panel.id = '__devtool-panel';
    panel.style.cssText = STYLES.panel;
    panel.style.display = 'none';
    panel.style.opacity = '0';
    panel.style.transform = 'translateY(8px)';

    // Header
    var header = document.createElement('div');
    header.style.cssText = STYLES.header;

    var title = document.createElement('span');
    title.style.cssText = STYLES.headerTitle;
    title.textContent = 'Send to Claude';
    header.appendChild(title);

    var closeBtn = document.createElement('button');
    closeBtn.style.cssText = STYLES.closeBtn;
    closeBtn.innerHTML = ICONS.close;
    closeBtn.onclick = function(e) { e.stopPropagation(); togglePanel(false); };
    closeBtn.onmouseenter = function() { closeBtn.style.background = TOKENS.colors.border; };
    closeBtn.onmouseleave = function() { closeBtn.style.background = 'none'; };
    header.appendChild(closeBtn);

    panel.appendChild(header);

    // Compose area
    var compose = document.createElement('div');
    compose.style.cssText = STYLES.compose;

    // Message card (groups message + attachments - Gestalt: Common Region)
    var card = document.createElement('div');
    card.id = '__devtool-card';
    card.style.cssText = STYLES.messageCard;

    var textarea = document.createElement('textarea');
    textarea.id = '__devtool-message';
    textarea.style.cssText = STYLES.textarea;
    textarea.placeholder = 'Describe what you need help with...';
    textarea.onfocus = function() {
      card.style.cssText = STYLES.messageCard + ';' + STYLES.messageCardFocused;
    };
    textarea.onblur = function() {
      card.style.cssText = STYLES.messageCard;
    };
    card.appendChild(textarea);

    // Attachment chips container
    var attachArea = document.createElement('div');
    attachArea.id = '__devtool-attachments';
    attachArea.style.cssText = STYLES.attachmentArea;
    attachArea.style.display = 'none';
    card.appendChild(attachArea);

    compose.appendChild(card);
    panel.appendChild(compose);

    // Toolbar with actions
    var toolbar = document.createElement('div');
    toolbar.style.cssText = STYLES.toolbar;

    // Tool buttons (Gestalt: Similarity - all secondary actions look alike)
    var screenshotBtn = createToolBtn('Screenshot', ICONS.screenshot, startScreenshotMode);
    var elementBtn = createToolBtn('Element', ICONS.element, startElementMode);
    var sketchBtn = createToolBtn('Sketch', ICONS.sketch, openSketch);

    toolbar.appendChild(screenshotBtn);
    toolbar.appendChild(elementBtn);
    toolbar.appendChild(sketchBtn);

    // Notification toggle
    var notifyToggle = document.createElement('label');
    notifyToggle.style.cssText = STYLES.notifyToggle;
    notifyToggle.title = 'Request notification when task completes';

    var notifyCheckbox = document.createElement('input');
    notifyCheckbox.type = 'checkbox';
    notifyCheckbox.id = '__devtool-notify';
    notifyCheckbox.checked = state.requestNotification;
    notifyCheckbox.onchange = function() {
      state.requestNotification = notifyCheckbox.checked;
    };
    notifyToggle.appendChild(notifyCheckbox);
    notifyToggle.appendChild(document.createTextNode(' Notify'));
    toolbar.appendChild(notifyToggle);

    // Send button (visual hierarchy - primary action)
    var sendBtn = document.createElement('button');
    sendBtn.style.cssText = STYLES.sendBtn;
    sendBtn.innerHTML = ICONS.send + ' Send';
    sendBtn.onclick = handleSend;
    sendBtn.onmouseenter = function() { sendBtn.style.background = TOKENS.colors.primaryDark; };
    sendBtn.onmouseleave = function() { sendBtn.style.background = TOKENS.colors.primary; };
    toolbar.appendChild(sendBtn);

    panel.appendChild(toolbar);

    state.panel = panel;
    state.container.appendChild(panel);
  }

  function createToolBtn(label, icon, onClick) {
    var btn = document.createElement('button');
    btn.style.cssText = STYLES.toolBtn;
    btn.innerHTML = icon + ' ' + label;
    btn.onclick = onClick;
    btn.onmouseenter = function() {
      btn.style.background = TOKENS.colors.surface;
      btn.style.borderColor = TOKENS.colors.primary;
      btn.style.color = TOKENS.colors.primary;
    };
    btn.onmouseleave = function() {
      btn.style.background = 'transparent';
      btn.style.borderColor = TOKENS.colors.border;
      btn.style.color = TOKENS.colors.textMuted;
    };
    return btn;
  }

  // Attachment chip creation
  function createChip(attachment) {
    var chip = document.createElement('div');
    chip.style.cssText = STYLES.chip;
    chip.dataset.id = attachment.id;

    var icon = document.createElement('span');
    icon.style.cssText = STYLES.chipIcon;
    var iconSvg = ICONS.element;
    if (attachment.type === 'screenshot') iconSvg = ICONS.screenshot;
    else if (attachment.type === 'sketch') iconSvg = ICONS.sketch;
    icon.innerHTML = iconSvg;
    chip.appendChild(icon);

    var label = document.createElement('span');
    label.style.cssText = STYLES.chipLabel;
    label.textContent = attachment.label;
    label.title = attachment.summary;
    chip.appendChild(label);

    var removeBtn = document.createElement('button');
    removeBtn.style.cssText = STYLES.chipRemove;
    removeBtn.innerHTML = ICONS.x;
    removeBtn.onclick = function(e) {
      e.stopPropagation();
      removeAttachment(attachment.id);
    };
    removeBtn.onmouseenter = function() { removeBtn.style.color = TOKENS.colors.error; };
    removeBtn.onmouseleave = function() { removeBtn.style.color = TOKENS.colors.textMuted; };
    chip.appendChild(removeBtn);

    return chip;
  }

  function addAttachment(type, data) {
    var attachment = {
      id: generateId(),
      type: type,
      label: data.label,
      summary: data.summary,
      data: data,
      timestamp: Date.now()
    };

    // Log to proxy first (this is the source of truth)
    core.send(type + '_capture', {
      id: attachment.id,
      timestamp: attachment.timestamp,
      data: data
    });

    // Add to local state
    state.attachments.push(attachment);

    // Update UI
    var container = document.getElementById('__devtool-attachments');
    if (container) {
      container.style.display = 'flex';
      container.appendChild(createChip(attachment));
    }

    return attachment.id;
  }

  function removeAttachment(id) {
    state.attachments = state.attachments.filter(function(a) { return a.id !== id; });

    var container = document.getElementById('__devtool-attachments');
    if (container) {
      var chip = container.querySelector('[data-id="' + id + '"]');
      if (chip) container.removeChild(chip);
      if (state.attachments.length === 0) container.style.display = 'none';
    }
  }

  function clearAttachments() {
    state.attachments = [];
    var container = document.getElementById('__devtool-attachments');
    if (container) {
      container.innerHTML = '';
      container.style.display = 'none';
    }
  }

  // Send message - assembles everything into a structured message
  function handleSend() {
    var textarea = document.getElementById('__devtool-message');
    var userMessage = textarea ? textarea.value.trim() : '';

    if (!userMessage && state.attachments.length === 0) return;

    // Build the structured message
    var parts = [];

    // User's message first
    if (userMessage) {
      parts.push(userMessage);
    }

    // Add context section if there are attachments
    if (state.attachments.length > 0) {
      parts.push('');
      parts.push('---');
      parts.push('**Context from page:** ' + window.location.href);
      parts.push('');

      state.attachments.forEach(function(att) {
        if (att.type === 'screenshot') {
          parts.push('- Screenshot `' + att.id + '`: ' + att.summary);
        } else if (att.type === 'element') {
          parts.push('- Element `' + att.id + '`: `' + att.data.selector + '` (' + att.data.tag + ')');
        } else if (att.type === 'sketch') {
          parts.push('- Sketch `' + att.id + '`: ' + att.summary);
        }
      });

      parts.push('');
      parts.push('*Use `proxylog` to fetch capture details. Use `proxy exec` to inspect or interact with the page.*');
    }

    var fullMessage = parts.join('\n');

    // Send via panel_message
    core.send('panel_message', {
      timestamp: Date.now(),
      payload: {
        message: fullMessage,
        references: state.attachments.map(function(a) {
          return { id: a.id, type: a.type };
        }),
        url: window.location.href,
        request_notification: state.requestNotification
      }
    });

    // Clear
    if (textarea) textarea.value = '';
    clearAttachments();
    togglePanel(false);
  }

  // Screenshot mode
  function startScreenshotMode() {
    togglePanel(false);

    var overlay = document.createElement('div');
    overlay.style.cssText = STYLES.overlay + ';' + STYLES.overlayDimmed;

    var box = document.createElement('div');
    box.style.cssText = STYLES.selectionBox;
    box.style.display = 'none';
    overlay.appendChild(box);

    var instructions = document.createElement('div');
    instructions.style.cssText = STYLES.instructionBar;
    instructions.textContent = 'Click and drag to select area \u2022 ESC to cancel';
    overlay.appendChild(instructions);

    var start = null;

    overlay.onmousedown = function(e) {
      start = { x: e.clientX, y: e.clientY };
      box.style.display = 'block';
      box.style.left = start.x + 'px';
      box.style.top = start.y + 'px';
      box.style.width = '0';
      box.style.height = '0';
    };

    overlay.onmousemove = function(e) {
      if (!start) return;
      var x = Math.min(start.x, e.clientX);
      var y = Math.min(start.y, e.clientY);
      var w = Math.abs(e.clientX - start.x);
      var h = Math.abs(e.clientY - start.y);
      box.style.left = x + 'px';
      box.style.top = y + 'px';
      box.style.width = w + 'px';
      box.style.height = h + 'px';
    };

    overlay.onmouseup = function(e) {
      if (!start) return;
      var x = Math.min(start.x, e.clientX);
      var y = Math.min(start.y, e.clientY);
      var w = Math.abs(e.clientX - start.x);
      var h = Math.abs(e.clientY - start.y);

      cleanup();

      if (w > 20 && h > 20) {
        // Add attachment with area info
        addAttachment('screenshot', {
          label: w + '\u00d7' + h + ' area',
          summary: 'Screenshot area at (' + x + ',' + y + ') size ' + w + 'x' + h,
          area: { x: x + window.scrollX, y: y + window.scrollY, width: w, height: h }
        });
        togglePanel(true);
      } else {
        togglePanel(true);
      }
    };

    function cleanup() {
      document.removeEventListener('keydown', onKey);
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
    }

    function onKey(e) {
      if (e.key === 'Escape') {
        cleanup();
        togglePanel(true);
      }
    }
    document.addEventListener('keydown', onKey);

    document.body.appendChild(overlay);
  }

  // Element selection mode
  function startElementMode() {
    togglePanel(false);

    var overlay = document.createElement('div');
    overlay.style.cssText = STYLES.overlay;

    var highlight = document.createElement('div');
    highlight.style.cssText = STYLES.elementHighlight;
    highlight.style.display = 'none';
    overlay.appendChild(highlight);

    var tooltip = document.createElement('div');
    tooltip.style.cssText = STYLES.tooltip;
    tooltip.style.display = 'none';
    overlay.appendChild(tooltip);

    var instructions = document.createElement('div');
    instructions.style.cssText = STYLES.instructionBar;
    instructions.textContent = 'Click an element to select \u2022 ESC to cancel';
    overlay.appendChild(instructions);

    var hovered = null;

    overlay.onmousemove = function(e) {
      overlay.style.pointerEvents = 'none';
      var el = document.elementFromPoint(e.clientX, e.clientY);
      overlay.style.pointerEvents = 'auto';

      if (!el || el === state.container || state.container.contains(el)) {
        highlight.style.display = 'none';
        tooltip.style.display = 'none';
        hovered = null;
        return;
      }

      hovered = el;
      var rect = el.getBoundingClientRect();

      highlight.style.display = 'block';
      highlight.style.left = rect.left + 'px';
      highlight.style.top = rect.top + 'px';
      highlight.style.width = rect.width + 'px';
      highlight.style.height = rect.height + 'px';

      var selector = utils.generateSelector(el);
      tooltip.textContent = selector;
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(rect.left, window.innerWidth - 200) + 'px';
      tooltip.style.top = Math.max(rect.top - 28, 5) + 'px';
    };

    overlay.onclick = function(e) {
      e.preventDefault();
      e.stopPropagation();
      cleanup();

      if (hovered) {
        var selector = utils.generateSelector(hovered);
        var tag = hovered.tagName.toLowerCase();
        var text = (hovered.textContent || '').trim().substring(0, 50);

        addAttachment('element', {
          label: selector.length > 30 ? tag + (hovered.id ? '#' + hovered.id : '') : selector,
          summary: selector + ' - "' + text + '"',
          selector: selector,
          tag: tag,
          id: hovered.id || null,
          classes: Array.from(hovered.classList),
          text: text,
          rect: hovered.getBoundingClientRect()
        });
      }

      togglePanel(true);
    };

    function cleanup() {
      document.removeEventListener('keydown', onKey);
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
    }

    function onKey(e) {
      if (e.key === 'Escape') {
        cleanup();
        togglePanel(true);
      }
    }
    document.addEventListener('keydown', onKey);

    document.body.appendChild(overlay);
  }

  // Sketch mode - opens sketch, on save adds as attachment
  function openSketch() {
    togglePanel(false);
    if (window.__devtool_sketch) {
      // Set callback for when sketch is saved
      window.__devtool_sketch.onSave = function(sketchData) {
        var id = generateId();

        // Log sketch to proxy first
        core.send('sketch_capture', {
          id: id,
          timestamp: Date.now(),
          data: sketchData
        });

        // Add as attachment chip
        var attachment = {
          id: id,
          type: 'sketch',
          label: sketchData.elementCount + ' elements',
          summary: 'Sketch with ' + sketchData.elementCount + ' elements',
          data: sketchData,
          timestamp: Date.now()
        };

        state.attachments.push(attachment);

        var container = document.getElementById('__devtool-attachments');
        if (container) {
          container.style.display = 'flex';
          container.appendChild(createChip(attachment));
        }

        togglePanel(true);
      };

      window.__devtool_sketch.toggle();
    }
  }

  // Panel toggle
  function togglePanel(show) {
    var shouldShow = show !== undefined ? show : !state.isExpanded;
    state.isExpanded = shouldShow;

    if (shouldShow) {
      updatePanelPosition();
      state.panel.style.display = 'block';
      requestAnimationFrame(function() {
        state.panel.style.opacity = '1';
        state.panel.style.transform = 'translateY(0)';
      });
    } else {
      state.panel.style.opacity = '0';
      state.panel.style.transform = 'translateY(8px)';
      setTimeout(function() { state.panel.style.display = 'none'; }, 200);
    }
  }

  function updatePanelPosition() {
    if (!state.panel || !state.bug) return;
    var rect = state.bug.getBoundingClientRect();
    var panelH = state.panel.offsetHeight || 300;

    var x = rect.left;
    var y = rect.top - panelH - 12;

    if (x + 380 > window.innerWidth) x = window.innerWidth - 390;
    if (x < 10) x = 10;
    if (y < 10) y = rect.bottom + 12;

    state.panel.style.left = x + 'px';
    state.panel.style.top = y + 'px';
  }

  // Drag handling
  function handleDragStart(e) {
    if (e.button !== 0) return;

    var startX = e.clientX;
    var startY = e.clientY;
    var startPos = { x: state.position.x, y: state.position.y };
    var dragged = false;

    function onMove(e) {
      var dx = e.clientX - startX;
      var dy = e.clientY - startY;

      if (Math.abs(dx) > 5 || Math.abs(dy) > 5) dragged = true;

      if (dragged) {
        state.isDragging = true;
        var x = startPos.x + dx;
        var y = startPos.y - dy;

        x = Math.max(0, Math.min(x, window.innerWidth - 52));
        y = Math.max(0, Math.min(y, window.innerHeight - 52));

        state.position = { x: x, y: y };
        state.bug.style.left = x + 'px';
        state.bug.style.bottom = y + 'px';
        updatePanelPosition();
      }
    }

    function onUp() {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);

      if (dragged) {
        savePrefs();
        setTimeout(function() { state.isDragging = false; }, 0);
      } else {
        togglePanel();
      }
    }

    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  }

  // Status polling and message handling
  function setupStatusPolling() {
    setInterval(function() {
      var dot = document.getElementById('__devtool-status');
      if (dot) {
        dot.style.backgroundColor = core.isConnected() ? TOKENS.colors.success : TOKENS.colors.error;
      }
    }, 1000);

    // Register message handler for activity state
    if (core && core.onMessage) {
      core.onMessage(handleMessage);
    }
  }

  // Handle incoming WebSocket messages
  function handleMessage(message) {
    if (message.type === 'activity') {
      var payload = message.payload || message;
      setActivityState(payload.active === true);
    }
  }

  // Preferences
  function savePrefs() {
    try {
      localStorage.setItem('__devtool_prefs', JSON.stringify({
        position: state.position,
        isVisible: state.isVisible
      }));
    } catch (e) {}
  }

  function loadPrefs() {
    try {
      var saved = localStorage.getItem('__devtool_prefs');
      if (saved) {
        var prefs = JSON.parse(saved);
        if (prefs.position) state.position = prefs.position;
        if (typeof prefs.isVisible === 'boolean') state.isVisible = prefs.isVisible;
      }
    } catch (e) {}
  }

  // Public API
  function show() {
    if (state.container) {
      state.container.style.display = 'block';
      state.isVisible = true;
      savePrefs();
    }
  }

  function hide() {
    if (state.container) {
      state.container.style.display = 'none';
      state.isVisible = false;
      savePrefs();
    }
  }

  function toggle() {
    state.isVisible ? hide() : show();
  }

  function destroy() {
    if (state.container && state.container.parentNode) {
      state.container.parentNode.removeChild(state.container);
    }
    state.container = null;
    state.bug = null;
    state.panel = null;
  }

  // Init on ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  // Export
  window.__devtool_indicator = {
    init: init,
    show: show,
    hide: hide,
    toggle: toggle,
    destroy: destroy,
    togglePanel: togglePanel,
    setActivityState: setActivityState,
    state: state
  };
})();

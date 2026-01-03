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
    outputPreview: null, // Floating output preview element
    isExpanded: false,
    isDragging: false,
    dragOffset: { x: 0, y: 0 },
    position: { x: 20, y: 20 },
    isVisible: true,
    isActive: false, // AI tool activity state
    activityTimeout: null,
    outputPreviewTimeout: null, // Auto-hide timeout for output preview
    requestNotification: true, // Always request notification when task completes
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

    // Output preview - floating next to the bug when AI is outputting
    outputPreview: [
      'position: fixed',
      'max-width: 400px',
      'min-width: 200px',
      'background: rgba(30, 41, 59, 0.95)',
      'color: #e2e8f0',
      'border-radius: ' + TOKENS.radius.md,
      'padding: 10px 14px',
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 12px',
      'line-height: 1.5',
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483645',
      'pointer-events: none',
      'opacity: 0',
      'transform: translateX(10px)',
      'transition: opacity 0.2s ease, transform 0.2s ease',
      'overflow: hidden',
      'white-space: pre-wrap',
      'word-break: break-word',
      'backdrop-filter: blur(8px)'
    ].join(';'),

    outputPreviewVisible: [
      'opacity: 1',
      'transform: translateX(0)'
    ].join(';'),

    // Panel - the main interface
    panel: [
      'position: fixed',
      'width: 380px',
      'background: ' + TOKENS.colors.surface,
      'border-radius: ' + TOKENS.radius.lg,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483645',
      'overflow: visible',
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
    // Flexbox with wrap for responsive fit
    toolbar: [
      'display: flex',
      'flex-wrap: wrap',
      'align-items: center',
      'gap: 6px',
      'padding: 10px ' + TOKENS.spacing.md,
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-top: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    // Container for action buttons (left side)
    toolbarActions: [
      'display: flex',
      'flex-wrap: wrap',
      'align-items: center',
      'gap: 6px',
      'flex: 1 1 auto',
      'min-width: 0'
    ].join(';'),

    toolBtn: [
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'gap: 4px',
      'padding: 6px 10px',
      'background: transparent',
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 12px',
      'font-weight: 500',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'transition: all 0.15s ease',
      'white-space: nowrap'
    ].join(';'),

    // Primary send button - visual hierarchy (most prominent)
    sendBtn: [
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'gap: 5px',
      'padding: 8px 14px',
      'background: ' + TOKENS.colors.primary,
      'border: none',
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 13px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textInverse,
      'cursor: pointer',
      'transition: background 0.15s ease, transform 0.1s ease',
      'white-space: nowrap',
      'margin-left: auto'
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
    ].join(';'),

    // Dropdown container
    dropdownContainer: [
      'position: relative',
      'display: inline-block'
    ].join(';'),

    // Dropdown button with chevron
    dropdownBtn: [
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'gap: 4px',
      'padding: 6px 10px',
      'background: transparent',
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 12px',
      'font-weight: 500',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'transition: all 0.15s ease',
      'white-space: nowrap'
    ].join(';'),

    // Dropdown menu
    dropdownMenu: [
      'position: absolute',
      'bottom: calc(100% + 4px)',
      'left: 0',
      'min-width: 180px',
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.md,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483648',
      'overflow: hidden',
      'opacity: 0',
      'transform: translateY(4px)',
      'pointer-events: none',
      'transition: opacity 0.15s ease, transform 0.15s ease'
    ].join(';'),

    dropdownMenuVisible: [
      'opacity: 1',
      'transform: translateY(0)',
      'pointer-events: auto'
    ].join(';'),

    // Dropdown menu item
    dropdownItem: [
      'display: flex',
      'align-items: center',
      'gap: 8px',
      'padding: 10px 12px',
      'font-size: 13px',
      'color: ' + TOKENS.colors.text,
      'cursor: pointer',
      'transition: background 0.1s ease',
      'border: none',
      'background: none',
      'width: 100%',
      'text-align: left'
    ].join(';'),

    dropdownItemHover: [
      'background: ' + TOKENS.colors.surfaceAlt
    ].join(';'),

    // Dropdown section header
    dropdownHeader: [
      'padding: 6px 12px',
      'font-size: 10px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px',
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'background: ' + TOKENS.colors.surfaceAlt
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
    design: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 20h9"/><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>',
    x: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M18 6L6 18M6 6l12 12"/></svg>',
    actions: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>',
    chevronDown: '<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="6 9 12 15 18 9"/></svg>',
    check: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg>',
    audit: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 11l3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/></svg>'
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
    createOutputPreview();

    document.documentElement.appendChild(state.container);
  }

  // Create floating output preview element
  function createOutputPreview() {
    var preview = document.createElement('div');
    preview.id = '__devtool-output-preview';
    preview.style.cssText = STYLES.outputPreview;
    state.outputPreview = preview;
    state.container.appendChild(preview);
  }

  // Show output preview with lines floating next to the bug
  function showOutputPreview(lines) {
    if (!state.outputPreview || !state.bug || !lines || lines.length === 0) return;

    // Format lines with subtle styling
    var html = lines.map(function(line) {
      // Limit each line to prevent overflow
      if (line.length > 80) {
        line = line.substring(0, 77) + '...';
      }
      return escapeHtml(line);
    }).join('\n');

    state.outputPreview.innerHTML = html;

    // Position next to the bug (to the right)
    var bugRect = state.bug.getBoundingClientRect();
    var previewWidth = Math.min(400, window.innerWidth - bugRect.right - 30);

    state.outputPreview.style.left = (bugRect.right + 12) + 'px';
    state.outputPreview.style.bottom = state.position.y + 'px';
    state.outputPreview.style.maxWidth = previewWidth + 'px';

    // If not enough space on right, position on left
    if (bugRect.right + 220 > window.innerWidth) {
      state.outputPreview.style.left = 'auto';
      state.outputPreview.style.right = (window.innerWidth - bugRect.left + 12) + 'px';
    }

    // Show with animation
    state.outputPreview.style.cssText = STYLES.outputPreview + ';' + STYLES.outputPreviewVisible;
    state.outputPreview.style.left = (bugRect.right + 12) + 'px';
    state.outputPreview.style.bottom = state.position.y + 'px';

    // Auto-hide after 3 seconds of no updates
    clearTimeout(state.outputPreviewTimeout);
    state.outputPreviewTimeout = setTimeout(function() {
      hideOutputPreview();
    }, 3000);
  }

  // Hide output preview
  function hideOutputPreview() {
    if (!state.outputPreview) return;
    state.outputPreview.style.cssText = STYLES.outputPreview;
    clearTimeout(state.outputPreviewTimeout);
  }

  // Escape HTML to prevent XSS
  function escapeHtml(text) {
    var div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
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
      // Ring pulse animation (expanding outward)
      '@keyframes __devtool-pulse {',
      '  0% { transform: scale(1); opacity: 0.8; }',
      '  50% { transform: scale(1.15); opacity: 0.4; }',
      '  100% { transform: scale(1.3); opacity: 0; }',
      '}',
      '.__devtool-active {',
      '  animation: __devtool-pulse 1.5s ease-out infinite;',
      '}',
      // Bug outline throb animation
      '@keyframes __devtool-throb {',
      '  0%, 100% {',
      '    box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 20px rgba(99,102,241,0.3), 0 0 0 0 rgba(245,158,11,0);',
      '  }',
      '  50% {',
      '    box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 20px rgba(99,102,241,0.3), 0 0 0 4px rgba(245,158,11,0.6);',
      '  }',
      '}',
      '.__devtool-bug-active {',
      '  animation: __devtool-throb 1.2s ease-in-out infinite !important;',
      '}'
    ].join('\\n');
    document.head.appendChild(style);
  }

  // Set activity state (called when AI tool becomes active/idle)
  function setActivityState(isActive) {
    state.isActive = isActive;
    var ring = document.getElementById('__devtool-activity-ring');
    var bug = state.bug;

    if (isActive) {
      // Throb the bug outline
      if (bug) {
        bug.classList.add('__devtool-bug-active');
      }
      // Also show expanding ring
      if (ring) {
        ring.classList.add('__devtool-active');
        ring.style.opacity = '1';
      }
    } else {
      // Stop throb
      if (bug) {
        bug.classList.remove('__devtool-bug-active');
      }
      if (ring) {
        ring.classList.remove('__devtool-active');
        ring.style.opacity = '0';
      }
      // Hide output preview when going idle
      hideOutputPreview();
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
    title.textContent = 'AI';
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
    textarea.placeholder = 'Describe what you need help with... (Ctrl+Enter to send)';
    textarea.onfocus = function() {
      card.style.cssText = STYLES.messageCard + ';' + STYLES.messageCardFocused;
    };
    textarea.onblur = function() {
      card.style.cssText = STYLES.messageCard;
    };
    // Auto-expand textarea based on content
    textarea.oninput = function() {
      var previousHeight = state.panel ? state.panel.offsetHeight : 0;
      textarea.style.height = 'auto';
      textarea.style.height = Math.min(Math.max(textarea.scrollHeight, 80), 200) + 'px';
      // Reposition panel to expand upward
      if (state.panel && state.isExpanded) {
        requestAnimationFrame(function() {
          var newHeight = state.panel.offsetHeight;
          var heightDiff = newHeight - previousHeight;
          if (heightDiff !== 0) {
            var currentTop = parseInt(state.panel.style.top) || 0;
            state.panel.style.top = (currentTop - heightDiff) + 'px';
          }
        });
      }
    };
    // Ctrl+Enter to send
    textarea.onkeydown = function(e) {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        handleSend();
      }
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

    // Action buttons container (wraps for responsive layout)
    var actionsContainer = document.createElement('div');
    actionsContainer.style.cssText = STYLES.toolbarActions;

    // Tool buttons (Gestalt: Similarity - all secondary actions look alike)
    var screenshotBtn = createToolBtn('Screenshot', ICONS.screenshot, startScreenshotMode);
    var elementBtn = createToolBtn('Element', ICONS.element, startElementMode);
    var sketchBtn = createToolBtn('Sketch', ICONS.sketch, openSketch);
    var designBtn = createToolBtn('Design', ICONS.design, startDesignMode);
    var auditDropdown = createActionsDropdown();

    actionsContainer.appendChild(screenshotBtn);
    actionsContainer.appendChild(elementBtn);
    actionsContainer.appendChild(sketchBtn);
    actionsContainer.appendChild(designBtn);
    actionsContainer.appendChild(auditDropdown);
    toolbar.appendChild(actionsContainer);

    // Send button (visual hierarchy - primary action, auto-pushed right via margin-left: auto)
    var sendBtn = document.createElement('button');
    sendBtn.style.cssText = STYLES.sendBtn;
    sendBtn.innerHTML = ICONS.send + ' Send';
    sendBtn.title = 'Send message (Ctrl+Enter)';
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

  // Audit actions configuration
  var AUDIT_ACTIONS = [
    // Quality Audits
    {
      id: 'fullAudit',
      label: 'Full Page Audit',
      description: 'Comprehensive quality audit with grade (A-F)',
      async: true,
      fn: function() {
        if (window.__devtool && window.__devtool.auditPageQuality) {
          return window.__devtool.auditPageQuality();
        }
        return Promise.resolve({ error: 'Page quality audit not available' });
      }
    },
    {
      id: 'accessibility',
      label: 'Accessibility',
      description: 'Check for a11y issues (WCAG)',
      fn: function() {
        if (window.__devtool_accessibility) {
          return window.__devtool_accessibility.auditAccessibility();
        }
        return { error: 'Accessibility module not loaded' };
      }
    },
    {
      id: 'security',
      label: 'Security',
      description: 'Mixed content, XSS risks, noopener',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditSecurity();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'seo',
      label: 'SEO / Meta',
      description: 'Meta tags, headings, structure',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditPageQuality();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    // Layout & Visual
    {
      id: 'layoutIssues',
      label: 'Layout Issues',
      description: 'Overflows, z-index, offscreen elements',
      fn: function() {
        if (window.__devtool && window.__devtool.diagnoseLayout) {
          return window.__devtool.diagnoseLayout();
        }
        return { error: 'Layout diagnostics not available' };
      }
    },
    {
      id: 'textFragility',
      label: 'Text Fragility',
      description: 'Truncation, overflow, font issues',
      fn: function() {
        if (window.__devtool && window.__devtool.checkTextFragility) {
          return window.__devtool.checkTextFragility();
        }
        return { error: 'Text fragility check not available' };
      }
    },
    {
      id: 'responsiveRisk',
      label: 'Responsive Risk',
      description: 'Elements that may break at different sizes',
      fn: function() {
        if (window.__devtool && window.__devtool.checkResponsiveRisk) {
          return window.__devtool.checkResponsiveRisk();
        }
        return { error: 'Responsive risk check not available' };
      }
    },
    // Debug Context
    {
      id: 'lastClick',
      label: 'Last Click Context',
      description: 'What the user just clicked + mouse trail',
      fn: function() {
        if (window.__devtool_interactions) {
          return window.__devtool_interactions.getLastClickContext();
        }
        return { error: 'Interaction tracking not available' };
      }
    },
    {
      id: 'recentMutations',
      label: 'Recent DOM Changes',
      description: 'What changed in the DOM recently',
      fn: function() {
        if (window.__devtool_mutations) {
          return {
            added: window.__devtool_mutations.getAdded(Date.now() - 30000),
            removed: window.__devtool_mutations.getRemoved(Date.now() - 30000),
            modified: window.__devtool_mutations.getModified(Date.now() - 30000)
          };
        }
        return { error: 'Mutation tracking not available' };
      }
    },
    // State Capture
    {
      id: 'captureState',
      label: 'Browser State',
      description: 'localStorage, sessionStorage, cookies',
      fn: function() {
        if (window.__devtool_capture) {
          return window.__devtool_capture.captureState(['localStorage', 'sessionStorage', 'cookies']);
        }
        return { error: 'State capture not available' };
      }
    },
    {
      id: 'networkSummary',
      label: 'Network/Resources',
      description: 'Resource timing and loading data',
      fn: function() {
        if (window.__devtool_capture) {
          return window.__devtool_capture.captureNetwork();
        }
        return { error: 'Network capture not available' };
      }
    },
    // Technical
    {
      id: 'domComplexity',
      label: 'DOM Complexity',
      description: 'Node count, depth, performance impact',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditDOMComplexity();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'css',
      label: 'CSS Quality',
      description: 'Inline styles, !important usage',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditCSS();
        }
        return { error: 'Audit module not loaded' };
      }
    }
  ];

  // Create the Actions dropdown
  function createActionsDropdown() {
    var container = document.createElement('div');
    container.style.cssText = STYLES.dropdownContainer;

    var btn = document.createElement('button');
    btn.style.cssText = STYLES.dropdownBtn;
    btn.innerHTML = ICONS.actions + ' Audit ' + ICONS.chevronDown;
    container.appendChild(btn);

    var menu = document.createElement('div');
    menu.style.cssText = STYLES.dropdownMenu + ';max-height:400px;overflow-y:auto';
    menu.id = '__devtool-audit-menu';

    // Group actions by category
    var sections = [
      { label: 'Quality Audits', ids: ['fullAudit', 'accessibility', 'security', 'seo'] },
      { label: 'Layout & Visual', ids: ['layoutIssues', 'textFragility', 'responsiveRisk'] },
      { label: 'Debug Context', ids: ['lastClick', 'recentMutations'] },
      { label: 'State & Network', ids: ['captureState', 'networkSummary'] },
      { label: 'Technical', ids: ['domComplexity', 'css'] }
    ];

    // Build menu with sections
    sections.forEach(function(section, sectionIndex) {
      // Add section header
      var header = document.createElement('div');
      header.style.cssText = STYLES.dropdownHeader;
      if (sectionIndex > 0) {
        header.style.borderTop = '1px solid ' + TOKENS.colors.border;
      }
      header.textContent = section.label;
      menu.appendChild(header);

      // Add items in this section
      section.ids.forEach(function(actionId) {
        var action = AUDIT_ACTIONS.find(function(a) { return a.id === actionId; });
        if (!action) return;

        var item = document.createElement('button');
        item.style.cssText = STYLES.dropdownItem;
        item.innerHTML = action.label;
        item.title = action.description;

        item.onmouseenter = function() {
          item.style.cssText = STYLES.dropdownItem + ';' + STYLES.dropdownItemHover;
        };
        item.onmouseleave = function() {
          item.style.cssText = STYLES.dropdownItem;
        };

        item.onclick = function(e) {
          e.stopPropagation();
          closeDropdown();
          runAuditAction(action);
        };

        menu.appendChild(item);
      });
    });

    container.appendChild(menu);

    // Toggle dropdown
    var isOpen = false;

    function openDropdown() {
      isOpen = true;
      menu.style.cssText = STYLES.dropdownMenu + ';' + STYLES.dropdownMenuVisible;
      btn.style.background = TOKENS.colors.surface;
      btn.style.borderColor = TOKENS.colors.primary;
      btn.style.color = TOKENS.colors.primary;
      document.addEventListener('click', handleOutsideClick);
    }

    function closeDropdown() {
      isOpen = false;
      menu.style.cssText = STYLES.dropdownMenu;
      btn.style.background = 'transparent';
      btn.style.borderColor = TOKENS.colors.border;
      btn.style.color = TOKENS.colors.textMuted;
      document.removeEventListener('click', handleOutsideClick);
    }

    function handleOutsideClick(e) {
      if (!container.contains(e.target)) {
        closeDropdown();
      }
    }

    btn.onclick = function(e) {
      e.stopPropagation();
      isOpen ? closeDropdown() : openDropdown();
    };

    btn.onmouseenter = function() {
      if (!isOpen) {
        btn.style.background = TOKENS.colors.surface;
        btn.style.borderColor = TOKENS.colors.primary;
        btn.style.color = TOKENS.colors.primary;
      }
    };
    btn.onmouseleave = function() {
      if (!isOpen) {
        btn.style.background = 'transparent';
        btn.style.borderColor = TOKENS.colors.border;
        btn.style.color = TOKENS.colors.textMuted;
      }
    };

    return container;
  }

  // Run an audit action and add result as attachment
  function runAuditAction(action) {
    function handleResult(result) {
      // Format summary based on result
      var summary = formatAuditSummary(action.id, result);

      // Add as attachment
      addAttachment('audit', {
        label: action.label,
        summary: summary,
        auditType: action.id,
        result: result
      });

      togglePanel(true);
    }

    try {
      var result = action.fn();

      // Handle async functions (like fullAudit)
      if (result && typeof result.then === 'function') {
        result.then(handleResult).catch(function(e) {
          handleResult({ error: e.message || 'Async audit failed' });
        });
      } else {
        handleResult(result);
      }
    } catch (e) {
      console.error('Audit failed:', e);
      handleResult({ error: e.message || 'Audit failed' });
    }
  }

  // Format a human-readable summary for audit results
  function formatAuditSummary(auditId, result) {
    if (!result) {
      return 'No data captured';
    }
    if (result.error) {
      return 'Error: ' + result.error;
    }

    switch (auditId) {
      // Quality Audits
      case 'fullAudit':
        return 'Grade: ' + (result.grade || '?') + ' (' + (result.overallScore || 0) + '/100) - ' +
               (result.criticalIssues ? result.criticalIssues.length : 0) + ' critical issues';

      case 'accessibility':
        return result.count + ' issue(s): ' + result.errors + ' errors, ' + result.warnings + ' warnings';

      case 'security':
        return result.count + ' issue(s): ' + result.errors + ' errors, ' + result.warnings + ' warnings';

      case 'seo':
        return result.count + ' issue(s) - Title: "' + (result.title || 'missing').substring(0, 30) + '"';

      // Layout & Visual
      case 'layoutIssues':
        var overflowCount = result.overflows ? result.overflows.length : 0;
        var stackingCount = result.stackingContexts ? result.stackingContexts.length : 0;
        var offscreenCount = result.offscreen ? result.offscreen.length : 0;
        return overflowCount + ' overflows, ' + stackingCount + ' z-index contexts, ' + offscreenCount + ' offscreen';

      case 'textFragility':
        if (result.summary) {
          return result.summary.total + ' issue(s): ' + result.summary.errors + ' errors, ' + result.summary.warnings + ' warnings';
        }
        return (result.issues ? result.issues.length : 0) + ' text issues found';

      case 'responsiveRisk':
        if (result.summary) {
          return result.summary.total + ' risk(s): ' + result.summary.errors + ' errors, ' + result.summary.warnings + ' warnings';
        }
        return (result.issues ? result.issues.length : 0) + ' responsive risks found';

      // Debug Context
      case 'lastClick':
        if (!result || !result.click) {
          return 'No recent click recorded';
        }
        var click = result.click;
        var target = click.target ? (click.target.selector || click.target.tag) : 'unknown';
        return 'Clicked: ' + target.substring(0, 40);

      case 'recentMutations':
        var addedCount = result.added ? result.added.length : 0;
        var removedCount = result.removed ? result.removed.length : 0;
        var modifiedCount = result.modified ? result.modified.length : 0;
        return addedCount + ' added, ' + removedCount + ' removed, ' + modifiedCount + ' modified (last 30s)';

      // State Capture
      case 'captureState':
        var localCount = result.localStorage ? Object.keys(result.localStorage).length : 0;
        var sessionCount = result.sessionStorage ? Object.keys(result.sessionStorage).length : 0;
        var cookieCount = result.cookies ? Object.keys(result.cookies).length : 0;
        return localCount + ' localStorage, ' + sessionCount + ' sessionStorage, ' + cookieCount + ' cookies';

      case 'networkSummary':
        var entries = result.entries || [];
        var totalSize = entries.reduce(function(sum, e) { return sum + (e.size || 0); }, 0);
        var totalSizeKB = Math.round(totalSize / 1024);
        return entries.length + ' resources, ' + totalSizeKB + 'KB total';

      // Technical
      case 'domComplexity':
        var rating = result.rating || 'unknown';
        return result.totalElements + ' nodes, depth ' + result.maxDepth + ' (' + rating + ')';

      case 'css':
        return result.issues.length + ' issue(s), ' + result.inlineStyleCount + ' inline styles';

      default:
        // Try to create a generic summary
        if (typeof result === 'object') {
          var keys = Object.keys(result).slice(0, 3);
          return keys.map(function(k) { return k + ': ' + JSON.stringify(result[k]).substring(0, 20); }).join(', ');
        }
        return String(result).substring(0, 100);
    }
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
    else if (attachment.type === 'audit') iconSvg = ICONS.audit;
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
        } else if (att.type === 'audit') {
          parts.push('- Audit `' + att.id + '` (' + att.data.auditType + '): ' + att.summary);
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

    // Detect responsive mode or touch device
    var isResponsive = window.innerWidth < 768; // Common tablet breakpoint
    var isTouchDevice = 'ontouchstart' in window || navigator.maxTouchPoints > 0;
    var isDragSelectUnavailable = isResponsive || isTouchDevice;

    // If drag select is not available, capture full screen immediately
    if (isDragSelectUnavailable) {
      var w = window.innerWidth;
      var h = window.innerHeight;
      addAttachment('screenshot', {
        label: 'Full screen (' + w + '\u00d7' + h + ')',
        summary: 'Full screen screenshot ' + w + 'x' + h,
        area: { x: 0, y: 0, width: w, height: h }
      });
      togglePanel(true);
      return;
    }

    // Desktop mode: show drag selection overlay
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

  // Design mode - start design iteration for an element
  function startDesignMode() {
    togglePanel(false);
    if (window.__devtool_design) {
      window.__devtool_design.start();
    } else {
      console.error('[Indicator] Design module not loaded');
      togglePanel(true);
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
    } else if (message.type === 'output_preview') {
      var payload = message.payload || message;
      if (payload.lines && Array.isArray(payload.lines)) {
        showOutputPreview(payload.lines);
      }
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

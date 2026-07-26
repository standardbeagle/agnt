// Floating Indicator — shared design tokens, styles, and icons.
// Split from indicator.js; shares symbols with the other indicator-*
// modules via the window.__devtool_indicator_internal namespace. Loads
// first of the indicator-* modules (see moduleOrder in embed.go).

(function() {
  'use strict';

  var I = window.__devtool_indicator_internal = window.__devtool_indicator_internal || {};

  // Design tokens - consistent visual language. Colors come from the shared
  // ui-tokens module (light/dark picked at load via prefers-color-scheme);
  // the literal block is the fallback for the defensive "ui-tokens failed to
  // load" case.
  var TOKENS = {
    colors: (window.__devtoolTokens && window.__devtoolTokens.theme()) || {
      primary: '#6366f1',      // Indigo
      primaryDark: '#4f46e5',
      secondary: '#64748b',    // Slate
      success: '#22c55e',
      error: '#ef4444',
      active: '#f59e0b',       // Amber - for activity state
      chaos: '#a855f7',        // Purple - for chaos mode
      chaosDeep: '#7c3aed',    // Violet - chaos accents
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

  // Shared z-index layer scale + reduced-motion preference (ui-tokens.js),
  // with load-failure fallbacks. Layer mapping: floating panels/previews sit
  // on `panel`, the bug + micro toast on `toast`, capture/selection overlays
  // and the audit mega menu on `critical` (the old 2147483648 literal
  // overflowed int32 and was clamped by browsers anyway).
  var IND_Z = window.__devtoolTokens ? window.__devtoolTokens.z : { panel: 2147483644, toast: 2147483646, critical: 2147483647 };
  var IND_MOTION = window.__devtoolTokens ? window.__devtoolTokens.motion() : { reduce: false, transition: function(spec) { return spec; } };

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
      'z-index: ' + IND_Z.toast,
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'transition: ' + IND_MOTION.transition('transform 0.2s ease, box-shadow 0.2s ease'),
      'user-select: none',
      // Size is fixed (52x52), so layout/style invalidation here cannot affect
      // the rest of the document. paint is intentionally omitted because the
      // glow box-shadow and activity ring (position:absolute with top:-4px...)
      // deliberately extend outside the 52x52 box.
      'contain: layout style'
    ].join(';'),

    statusDot: [
      'position: absolute',
      'top: 0',
      'right: 0',
      'width: 14px',
      'height: 14px',
      'border-radius: ' + TOKENS.radius.full,
      'border: 2.5px solid ' + TOKENS.colors.surface,
      // Connection state changes fill colour, fill opacity, and the inset/halo
      // shadow together (see CONN_STATES in indicator.js) — all three must
      // animate or the dot appears to change in two steps.
      'transition: ' + IND_MOTION.transition('background-color 0.3s ease, opacity 0.3s ease, box-shadow 0.3s ease')
    ].join(';'),

    // Activity ring - pulses when AI is working
    activityRing: [
      'position: absolute',
      'top: -4px',
      'left: -4px',
      'right: -4px',
      'bottom: -4px',
      'border-radius: ' + TOKENS.radius.full,
      // Arc spinner: transparent sides make the rotation visible (a uniform
      // circular border looks identical at every angle and reads as static).
      'border: 3px solid transparent',
      'border-top-color: ' + TOKENS.colors.active,
      'border-right-color: ' + TOKENS.colors.active,
      'opacity: 0',
      'pointer-events: none'
    ].join(';'),

    // Activity ripples - expanding rings emitted while AI is working
    activityRipple: [
      'position: absolute',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'border-radius: ' + TOKENS.radius.full,
      'border: 2px solid ' + TOKENS.colors.active,
      'opacity: 0',
      'pointer-events: none'
    ].join(';'),

    // Chaos ring - rotating dashed storm ring when chaos mode is enabled
    chaosRing: [
      'position: absolute',
      'top: -7px',
      'left: -7px',
      'right: -7px',
      'bottom: -7px',
      'border-radius: ' + TOKENS.radius.full,
      'border: 2px dashed ' + TOKENS.colors.chaos,
      'opacity: 0',
      'pointer-events: none',
      'transition: ' + IND_MOTION.transition('opacity 0.3s ease')
    ].join(';'),

    // Chaos badge - small storm marker pinned bottom-right of the bug
    chaosBadge: [
      'position: absolute',
      'bottom: -2px',
      'right: -2px',
      'width: 18px',
      'height: 18px',
      'border-radius: ' + TOKENS.radius.full,
      'background: ' + TOKENS.colors.chaos,
      'border: 2px solid ' + TOKENS.colors.surface,
      'display: none',
      'align-items: center',
      'justify-content: center',
      'font-size: 10px',
      'line-height: 1',
      'pointer-events: none',
      'box-shadow: 0 0 8px rgba(168,85,247,0.6)'
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
      'z-index: ' + IND_Z.panel,
      'pointer-events: none',
      'opacity: 0',
      'transform: translateX(10px)',
      'transition: ' + IND_MOTION.transition('opacity 0.2s ease, transform 0.2s ease'),
      'overflow: hidden',
      'white-space: pre-wrap',
      'word-break: break-word',
      'backdrop-filter: blur(8px)',
      // Already has overflow:hidden, so paint would be redundant with existing
      // clipping. contain: layout style scopes invalidation to the preview box.
      'contain: layout style'
    ].join(';'),

    outputPreviewVisible: [
      'opacity: 1',
      'transform: translateX(0)'
    ].join(';'),

    // One rendered line inside the output preview (icon + text)
    outputPreviewLine: [
      'display: flex',
      'align-items: flex-start',
      'gap: 6px',
      'min-width: 0'
    ].join(';'),

    outputPreviewIcon: [
      'flex: none',
      'width: 14px',
      'text-align: center',
      'font-size: 11px',
      'line-height: 18px'
    ].join(';'),

    outputPreviewText: [
      'flex: 1',
      'min-width: 0',
      'line-height: 18px',
      'white-space: pre-wrap',
      'word-break: break-word'
    ].join(';'),

    // Live throbber line pinned under the preview lines, updated in place
    outputPreviewThrobber: [
      'display: none',
      'margin-top: 4px',
      'padding-top: 4px',
      'border-top: 1px solid rgba(148, 163, 184, 0.25)',
      'font-style: italic',
      'line-height: 18px',
      'white-space: pre-wrap',
      'word-break: break-word'
    ].join(';'),

    // API activity sparkline - thin strip below the bug
    sparkline: [
      'position: fixed',
      'width: 52px',
      'height: 12px',
      'border-radius: 6px',
      'background: rgba(99,102,241,0.15)',
      'overflow: hidden',
      'pointer-events: none',
      'z-index: ' + IND_Z.panel,
      'transition: ' + IND_MOTION.transition('opacity 0.3s ease'),
      // Fixed 52x12 size with overflow:hidden; safe to use the strict `content`
      // shorthand (layout + style + paint) because the SVG bars stay inside.
      'contain: content'
    ].join(';'),

    // Panel - the main interface
    panel: [
      'position: fixed',
      'width: 480px',
      'background: ' + TOKENS.colors.surface,
      'border-radius: ' + TOKENS.radius.lg,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: ' + IND_Z.panel,
      'overflow: visible',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      'font-size: 14px',
      'color: ' + TOKENS.colors.text,
      'transition: ' + IND_MOTION.transition('opacity 0.2s ease, transform 0.2s ease'),
      // Scope layout/style invalidation to the panel. paint is omitted because
      // the audit dropdown megaMenu inside the panel uses position:absolute
      // with bottom:calc(100% + 4px) to open upward and its box-shadow extends
      // outside the panel's bounds.
      'contain: layout style'
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
      'transition: ' + IND_MOTION.transition('background 0.15s ease')
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
      'transition: ' + IND_MOTION.transition('border-color 0.2s ease, box-shadow 0.2s ease')
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
      'transition: ' + IND_MOTION.transition('color 0.15s ease')
    ].join(';'),

    // Attachment preview popup (shown on chip hover)
    attachmentPreview: [
      'position: fixed',
      'z-index: ' + IND_Z.critical,
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.lg,
      'box-shadow: 0 8px 24px rgba(0,0,0,0.4)',
      'padding: ' + TOKENS.spacing.sm,
      'max-width: 320px',
      'max-height: 240px',
      'overflow: hidden',
      'pointer-events: none',
      'opacity: 0',
      'transition: ' + IND_MOTION.transition('opacity 0.15s ease')
    ].join(';'),

    attachmentPreviewImage: [
      'width: 100%',
      'height: auto',
      'max-height: 200px',
      'object-fit: contain',
      'border-radius: ' + TOKENS.radius.sm
    ].join(';'),

    attachmentPreviewElement: [
      'font-family: monospace',
      'font-size: 11px',
      'color: ' + TOKENS.colors.text,
      'white-space: pre-wrap',
      'word-break: break-word'
    ].join(';'),

    // Element highlight overlay for element preview
    elementPreviewHighlight: [
      'position: fixed',
      'z-index: ' + IND_Z.panel,
      'background: rgba(99, 102, 241, 0.2)',
      'border: 2px solid ' + TOKENS.colors.primary,
      'border-radius: 2px',
      'pointer-events: none',
      'transition: ' + IND_MOTION.transition('all 0.15s ease')
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
      'transition: ' + IND_MOTION.transition('all 0.15s ease'),
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
      'transition: ' + IND_MOTION.transition('background 0.15s ease, transform 0.1s ease'),
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
      'z-index: ' + IND_Z.critical,
      'background: transparent',
      'pointer-events: auto',
      'user-select: none',
      '-webkit-user-select: none',
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
      'z-index: ' + IND_Z.critical
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
      'z-index: ' + IND_Z.critical,
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
      'transition: ' + IND_MOTION.transition('all 0.15s ease'),
      'white-space: nowrap'
    ].join(';'),

    // Mega menu (audit dropdown). Uses position: fixed so it escapes
    // ancestor overflow clipping (e.g. .tabContent's overflow-y: auto).
    //
    // Modern path: rendered via native HTML Popover API — top-layer
    //   rendering + built-in light-dismiss + Escape handling. Visibility
    //   and fade/translate transitions are handled by [popover]:popover-open
    //   CSS rules injected by injectAuditMenuStyles() — NOT by toggling
    //   this inline cssText. margin/inset are explicitly overridden because
    //   browsers default [popover] elements to margin:auto + inset:0 to
    //   center in viewport, which would fight our JS-computed top/left.
    //
    // Legacy path (pre-baseline browsers without popover support): the
    //   megaMenuVisible rule below is composed on top of this via inline
    //   cssText in the legacy openDropdown(), which still toggles the
    //   opacity/transform/pointer-events properties manually.
    //
    // Both paths set top/left dynamically via getBoundingClientRect.
    megaMenu: [
      'position: fixed',
      'margin: 0',
      'inset: unset',
      'width: 480px',
      'max-width: calc(100vw - 16px)',
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.md,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: ' + IND_Z.critical,
      'opacity: 0',
      'transform: translateY(4px)',
      'pointer-events: none',
      'transition: ' + IND_MOTION.transition('opacity 0.15s ease, transform 0.15s ease')
    ].join(';'),

    megaMenuVisible: [
      'opacity: 1',
      'transform: translateY(0)',
      'pointer-events: auto'
    ].join(';'),

    // Top row of mega menu: 4 columns for the first 4 sections
    megaMenuTopRow: [
      'display: grid',
      'grid-template-columns: 1fr 1fr 1fr 1fr',
      'gap: 0'
    ].join(';'),

    // Bottom row of mega menu: Technical section spanning full width
    megaMenuBottomRow: [
      'border-top: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    // Column within mega menu
    megaMenuColumn: [
      'padding: ' + TOKENS.spacing.sm + ' 0',
      'border-right: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    megaMenuColumnLast: [
      'border-right: none'
    ].join(';'),

    // Column header in mega menu
    megaMenuColumnHeader: [
      'padding: 4px ' + TOKENS.spacing.sm,
      'font-size: 10px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px',
      'margin-bottom: 2px'
    ].join(';'),

    // Menu item with label + description blurb
    megaMenuItem: [
      'display: block',
      'padding: 5px ' + TOKENS.spacing.sm,
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'cursor: pointer',
      'transition: ' + IND_MOTION.transition('background 0.1s ease'),
      'border: none',
      'background: none',
      'width: 100%',
      'text-align: left',
      'line-height: 1.3'
    ].join(';'),

    megaMenuItemHover: [
      'background: ' + TOKENS.colors.surfaceAlt
    ].join(';'),

    megaMenuItemLabel: [
      'font-weight: 600',
      'font-size: 12px',
      'color: ' + TOKENS.colors.text
    ].join(';'),

    megaMenuItemDesc: [
      'font-weight: 400',
      'font-size: 10px',
      'color: ' + TOKENS.colors.textMuted,
      'display: block',
      'margin-top: 1px'
    ].join(';'),

    // Technical section: 3-column grid spanning full width
    megaMenuTechnicalGrid: [
      'display: grid',
      'grid-template-columns: 1fr 1fr 1fr',
      'padding: ' + TOKENS.spacing.sm + ' 0'
    ].join(';'),

    megaMenuTechnicalHeader: [
      'padding: 4px ' + TOKENS.spacing.sm,
      'font-size: 10px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px',
      'border-top: none'
    ].join(';'),

    // Tab styles
    tabBar: [
      'display: flex',
      'align-items: center',
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'overflow-x: auto',
      'overflow-y: hidden',
      'padding: 0 ' + TOKENS.spacing.sm,
      'gap: ' + TOKENS.spacing.xs
    ].join(';'),

    tab: [
      'padding: 8px 12px',
      'font-size: 12px',
      'font-weight: 500',
      'border: none',
      'background: transparent',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'border-bottom: 2px solid transparent',
      'transition: ' + IND_MOTION.transition('color 0.15s ease, border-color 0.15s ease'),
      'white-space: nowrap',
      'position: relative',
      'display: flex',
      'align-items: center',
      'gap: 4px'
    ].join(';'),

    tabActive: [
      'color: ' + TOKENS.colors.primary,
      'border-bottom-color: ' + TOKENS.colors.primary
    ].join(';'),

    tabBadge: [
      'min-width: 16px',
      'height: 16px',
      'padding: 0 4px',
      'font-size: 10px',
      'font-weight: 600',
      'border-radius: ' + TOKENS.radius.full,
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'line-height: 1'
    ].join(';'),

    tabBadgeRed: [
      'background: ' + TOKENS.colors.error,
      'color: white'
    ].join(';'),

    tabBadgeYellow: [
      'background: ' + TOKENS.colors.active,
      'color: white'
    ].join(';'),

    tabBadgeGreen: [
      'background: ' + TOKENS.colors.success,
      'color: white'
    ].join(';'),

    tabBadgePurple: [
      'background: ' + TOKENS.colors.chaos,
      'color: white'
    ].join(';'),

    tabContent: [
      'padding: ' + TOKENS.spacing.lg,
      'max-height: 400px',
      'overflow-y: auto',
      'overflow-x: hidden',
      // Switching tabs rewrites tabContent.innerHTML; scope layout/style to
      // this subtree so a tab swap does not invalidate the rest of the panel.
      // paint is intentionally omitted to avoid any interaction with the audit
      // megaMenu dropdown inside the Compose tab, which uses position:absolute
      // with bottom:calc(100% + 4px) to open upward. The existing overflow-y:
      // auto already clips within-box content.
      'contain: layout style'
    ].join(';'),

    tabCloseBtn: [
      'margin-left: auto',
      'background: none',
      'border: none',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'padding: 4px',
      'display: flex',
      'flex-shrink: 0'
    ].join(';'),

    // Tab content specific styles
    healthCard: [
      'background: ' + TOKENS.colors.surfaceAlt,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: ' + TOKENS.spacing.md,
      'margin-bottom: ' + TOKENS.spacing.sm
    ].join(';'),

    healthLabel: [
      'font-size: 11px',
      'color: ' + TOKENS.colors.textMuted,
      'margin-bottom: 4px',
      'text-transform: uppercase',
      'letter-spacing: 0.5px'
    ].join(';'),

    healthValue: [
      'font-size: 20px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.text
    ].join(';'),

    errorItem: [
      'padding: ' + TOKENS.spacing.sm,
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'font-size: 12px',
      'cursor: pointer',
      'transition: ' + IND_MOTION.transition('background 0.15s ease')
    ].join(';'),

    errorMessage: [
      'color: ' + TOKENS.colors.text,
      'margin-bottom: 4px',
      'font-weight: 500'
    ].join(';'),

    errorMeta: [
      'color: ' + TOKENS.colors.textMuted,
      'font-size: 11px'
    ].join(';'),

    emptyState: [
      'text-align: center',
      'padding: ' + TOKENS.spacing.xl,
      'color: ' + TOKENS.colors.textMuted,
      'font-size: 13px'
    ].join(';'),

    // Micro toast - compact pill near the bug
    microToast: [
      'position: fixed',
      'background: rgba(15, 23, 42, 0.92)',
      'color: #e2e8f0',
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 11px',
      'font-weight: 500',
      'padding: 5px 12px',
      'border-radius: ' + TOKENS.radius.full,
      'white-space: nowrap',
      'backdrop-filter: blur(12px)',
      'pointer-events: none',
      'z-index: ' + IND_Z.toast,
      'opacity: 0',
      'transform: translateY(6px) scale(0.92)',
      'transition: ' + IND_MOTION.transition('opacity 0.25s cubic-bezier(0.16,1,0.3,1), transform 0.25s cubic-bezier(0.16,1,0.3,1)'),
      'box-shadow: 0 4px 20px rgba(0,0,0,0.25), inset 0 0.5px 0 rgba(255,255,255,0.06)',
      'letter-spacing: 0.3px',
      'max-width: 260px',
      'overflow: hidden',
      'text-overflow: ellipsis',
      // 2.5s-lived pill with transform+opacity enter/leave. Scope layout/style
      // and promote to a compositor layer for the duration of the animation.
      // paint omitted because the box-shadow renders outside the pill bounds.
      'contain: layout style',
      'will-change: transform, opacity'
    ].join(';'),

    microToastVisible: [
      'opacity: 1',
      'transform: translateY(0) scale(1)'
    ].join(';'),

    // History tab item
    historyItem: [
      'display: flex',
      'align-items: flex-start',
      'gap: 10px',
      'padding: 8px 0',
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'font-size: 12px',
      'line-height: 1.4',
      'transition: ' + IND_MOTION.transition('background 0.12s ease')
    ].join(';'),

    historyDot: [
      'flex-shrink: 0',
      'width: 8px',
      'height: 8px',
      'border-radius: ' + TOKENS.radius.full,
      'margin-top: 4px'
    ].join(';'),

    historyBody: [
      'flex: 1',
      'min-width: 0'
    ].join(';'),

    historyText: [
      'color: ' + TOKENS.colors.text,
      'white-space: nowrap',
      'overflow: hidden',
      'text-overflow: ellipsis'
    ].join(';'),

    historyMeta: [
      'font-size: 10px',
      'color: ' + TOKENS.colors.textMuted,
      'margin-top: 1px'
    ].join(';'),

    historyTime: [
      'flex-shrink: 0',
      'font-size: 10px',
      'color: ' + TOKENS.colors.textMuted,
      'margin-top: 3px',
      'white-space: nowrap'
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
    audit: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 11l3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/></svg>',
    styleEdit: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.9 0 1.7-.1 2.5-.3"/><path d="M12 2c2.2 0 4 4.5 4 10"/><path d="M12 2c-2.2 0-4 4.5-4 10s1.8 10 4 10"/><path d="M2 12h10"/><path d="M20 14l-4 4 1.5 1.5a2.12 2.12 0 0 0 3 0 2.12 2.12 0 0 0 0-3L20 14z"/></svg>',
    inspect: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M2 22l1-6.5 5.5 5.5L2 22z"/><path d="M8.5 15.5L18 6a2.83 2.83 0 1 0-4-4L4.5 11.5"/><circle cx="18" cy="4" r="1" fill="currentColor" stroke="none"/></svg>',
    responsive: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="2" y="4" width="14" height="11" rx="1"/><path d="M2 18h14"/><rect x="17" y="9" width="5" height="11" rx="1"/></svg>',
    refresh: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>'
  };

  // Shared with indicator-data.js / indicator-tabs.js / indicator.js.
  I.TOKENS = TOKENS;
  I.IND_Z = IND_Z;
  I.IND_MOTION = IND_MOTION;
  I.STYLES = STYLES;
  I.ICONS = ICONS;
})();

// Floating Indicator for DevTool
// Redesigned with visual hierarchy and Gestalt principles
// Attachments are logged first, then referenced in messages
// Uses VanJS for reactive state management
//
// This module is the chrome shell (bug, panel, tab bar, sparkline, hotkeys,
// capture modes, connection status). The reactive store/data refresh lives in
// indicator-data.js, the VanJS tab components in indicator-tabs.js, and the
// design tokens/styles/icons in indicator-styles.js. The four indicator-*
// modules share symbols via the window.__devtool_indicator_internal namespace.

(function() {
  'use strict';

	var frameContext = window.__devtool_context;

  var I = window.__devtool_indicator_internal;

  // Shared symbols from indicator-styles.js (loads earlier).
  var TOKENS = I.TOKENS;
  var IND_Z = I.IND_Z;
  var IND_MOTION = I.IND_MOTION;
  var STYLES = I.STYLES;
  var ICONS = I.ICONS;

  // Shared symbols from indicator-data.js (loads earlier).
  var van = I.van;
  var tags = I.tags;
  var core = I.core;
  var utils = I.utils;
  var mountRoot = I.mountRoot;
  var isShadowMount = I.isShadowMount;
  var getInMount = I.getInMount;
  var styleTarget = I.styleTarget;
  var state = I.state;
  var store = I.store;
  var chaosPending = I.chaosPending;
  var applyChaosSnapshot = I.applyChaosSnapshot;
  var refreshChaosData = I.refreshChaosData;
  var dvErrors = I.dvErrors;
  var dvApi = I.dvApi;
  var dvMutations = I.dvMutations;
  var mutationRateStats = I.mutationRateStats;
  var refreshOverviewData = I.refreshOverviewData;
  var refreshErrorsData = I.refreshErrorsData;
  var refreshNetworkData = I.refreshNetworkData;
  var refreshPerformanceData = I.refreshPerformanceData;
  var refreshInteractionsData = I.refreshInteractionsData;
  var actions = I.actions;
  var targetWindow = I.targetWindow;

  // Tab components from indicator-tabs.js (loads earlier).
  var AttachmentAreaComponent = I.AttachmentAreaComponent;
  var OverviewTabComponent = I.OverviewTabComponent;
  var ErrorsTabComponent = I.ErrorsTabComponent;
  var NetworkTabComponent = I.NetworkTabComponent;
  var PerformanceTabComponent = I.PerformanceTabComponent;
  var InteractionsTabComponent = I.InteractionsTabComponent;
  var HistoryTabComponent = I.HistoryTabComponent;
  var ChaosTabComponent = I.ChaosTabComponent;

  // Initialize
  function init() {
    if (state.container) return;
    loadPrefs();
    // Nested (iframe) instance — e.g. the live frame inside responsive mode,
    // which loads this same proxied page and therefore re-injects this script.
    // The page's own global key hooks live inside the frame and cannot reach
    // the parent, so the parent owns the chrome. Run bridge-only: render no UI
    // and skip status polling. The hotkey handler (registered at script-eval
    // time, see bottom) forwards our global hotkeys up to the parent's
    // indicator. The headless modules (__devtool_responsive/core/audit) stay
    // loaded for responsive-mode to read.
    if (isNestedFrame()) {
      return;
    }
    createUI();
    setupStatusPolling();
  }

  // True when this script runs inside a frame rather than the top window.
  // The frame-context adapter owns the same-origin/cross-origin distinction.
  function isNestedFrame() {
	return !frameContext.isTopLevel();
  }

  // Call a method on the top frame's indicator. Same-origin direct call;
  // swallow if the parent is gone or (defensively) cross-origin.
  function callParentIndicator(method) {
    try {
		var parentIndicator = frameContext.shellExport('__devtool_indicator');
      if (parentIndicator && typeof parentIndicator[method] === 'function') {
        parentIndicator[method]();
      }
    } catch (e) { /* parent unreachable — nothing to forward to */ }
  }

  // Single global-hotkey handler for both top and nested instances.
  // Nested → forward to the parent's indicator; top → act locally.
  function handleGlobalHotkey(e) {
    // Ctrl+Y (or Cmd+Y on Mac) - toggle indicator panel
    if (e.key === 'y' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
      e.preventDefault();
      if (isNestedFrame()) {
        callParentIndicator('togglePanel');
      } else {
        togglePanel();
      }
    }
  }

  function createUI() {
    state.container = document.createElement('div');
    state.container.id = '__devtool-indicator';
    if (!state.isVisible) state.container.style.display = 'none';

    createBug();
    createPanel();
    createOutputPreview();
    createMicroToast();

    // Isolate clicks on the interactive surfaces (panel + bug) from page
    // handlers (e.g., dropdown "click outside" detectors). Scoped to those
    // elements — NOT the whole container — so passive elements (micro toast,
    // sparkline, output preview) never swallow page events. Bubbling phase so
    // events still reach children first; click/mousedown/pointerdown only —
    // NOT mouseup/pointerup, which must reach document for drag release
    // detection in handleDragStart.
    [state.panel, state.bug].forEach(function(el) {
      if (!el) return;
      el.addEventListener('click', function(e) { e.stopPropagation(); });
      el.addEventListener('mousedown', function(e) { e.stopPropagation(); });
      el.addEventListener('pointerdown', function(e) { e.stopPropagation(); });
    });

    // Minimal ARIA surface: the panel is a non-modal dialog, the bug is its
    // toggle button, decorative strips are hidden from assistive tech.
    if (state.panel) {
      state.panel.setAttribute('role', 'dialog');
      state.panel.setAttribute('aria-modal', 'false');
      state.panel.setAttribute('aria-label', 'agnt developer panel');
      state.panel.tabIndex = -1;
    }
    if (state.bug) {
      state.bug.setAttribute('role', 'button');
      state.bug.setAttribute('aria-label', 'Toggle agnt developer panel');
      state.bug.setAttribute('aria-haspopup', 'dialog');
      state.bug.tabIndex = 0;
      state.bug.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          togglePanel();
        }
      });
    }
    if (state.microToast) state.microToast.setAttribute('aria-hidden', 'true');
    if (state.outputPreview) state.outputPreview.setAttribute('aria-hidden', 'true');

    // Mount the indicator container into the devtool shadow root (or
    // document.body in the fallback path). See mountRoot() helper at top.
    mountRoot().appendChild(state.container);
    createSparkline();
  }

  // Create floating output preview element
  function createOutputPreview() {
    var preview = document.createElement('div');
    preview.id = '__devtool-output-preview';
    preview.style.cssText = STYLES.outputPreview;

    var linesEl = document.createElement('div');
    linesEl.id = '__devtool-output-preview-lines';
    preview.appendChild(linesEl);

    var throbberEl = document.createElement('div');
    throbberEl.id = '__devtool-output-throbber';
    throbberEl.style.cssText = STYLES.outputPreviewThrobber;
    preview.appendChild(throbberEl);

    state.outputPreviewLines = linesEl;
    state.outputPreviewThrobber = throbberEl;
    state.outputPreview = preview;
    state.container.appendChild(preview);
  }

  // Classify a preview line by its leading glyph / content so tool calls,
  // results, errors, and successes each get an icon and color.
  function classifyPreviewLine(line) {
    // Tool call markers (Claude Code prints "⏺ Bash(...)")
    var m = line.match(/^[⏺●]\s*/);
    if (m) return { icon: '⚙', color: TOKENS.colors.primary, text: line.substring(m[0].length) };
    // Tool result continuation ("⎿  result", box corners)
    m = line.match(/^[⎿└╰↳]\s*/);
    if (m) return { icon: '↳', color: TOKENS.colors.textMuted, text: line.substring(m[0].length) };
    // Errors: explicit glyph or keyword
    m = line.match(/^[✗✘✖❌]\s*/);
    if (m || /\b(error|failed|failure|exception|fatal|panic)\b/i.test(line)) {
      return { icon: '✘', color: TOKENS.colors.error, text: m ? line.substring(m[0].length) : line };
    }
    // Warnings
    m = line.match(/^⚠️?\s*/);
    if (m || /\bwarn(ing)?\b/i.test(line)) {
      return { icon: '⚠', color: TOKENS.colors.active, text: m ? line.substring(m[0].length) : line };
    }
    // Success: explicit glyph or keyword
    m = line.match(/^[✓✔✅]\s*/);
    if (m || /\b(passed|succeeded|success|completed?|done)\b/i.test(line)) {
      return { icon: '✓', color: TOKENS.colors.success, text: m ? line.substring(m[0].length) : line };
    }
    return { icon: '', color: TOKENS.colors.textInverse, text: line };
  }

  function truncatePreviewLine(line) {
    return line.length > 96 ? line.substring(0, 93) + '...' : line;
  }

  // Show output preview with lines floating next to the bug. throbber is the
  // in-flight animated line (spinner text), rendered as a single pinned line
  // that updates in place instead of scrolling.
  function showOutputPreview(lines, throbber) {
    lines = lines || [];
    throbber = throbber || '';
    if (!state.outputPreview || !state.bug) return;
    // The interactive developer panel owns the foreground while it is open.
    // Output previews are passive agent-update text, so showing one above the
    // panel would obscure controls and make the panel harder to use.
    if (state.isExpanded) return;
    if (lines.length === 0 && !throbber) return;

    var html = lines.map(function(line) {
      var cls = classifyPreviewLine(truncatePreviewLine(line));
      return '<div style="' + STYLES.outputPreviewLine + '">' +
        '<span style="' + STYLES.outputPreviewIcon + ';color:' + cls.color + '">' + cls.icon + '</span>' +
        '<span style="' + STYLES.outputPreviewText + ';color:' + (cls.icon ? cls.color : '#e2e8f0') + '">' + escapeHtml(cls.text) + '</span>' +
        '</div>';
    }).join('');

    // Only touch the DOM when the committed lines actually changed, so
    // throbber-only updates never rebuild (or flicker) the line list.
    if (html !== state.outputPreviewHTML) {
      state.outputPreviewHTML = html;
      state.outputPreviewLines.innerHTML = html;
    }

    // Throbber: update text in place; pulse while present.
    if (throbber) {
      state.outputPreviewThrobber.textContent = truncatePreviewLine(throbber);
      state.outputPreviewThrobber.style.display = 'block';
      state.outputPreviewThrobber.style.color = TOKENS.colors.active;
      state.outputPreviewThrobber.classList.add('__devtool-throbber-live');
    } else {
      state.outputPreviewThrobber.textContent = '';
      state.outputPreviewThrobber.style.display = 'none';
      state.outputPreviewThrobber.classList.remove('__devtool-throbber-live');
    }

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

    // Auto-hide after 4 seconds of no updates (throbber updates reset this,
    // so the block stays up while the agent is mid-operation)
    clearTimeout(state.outputPreviewTimeout);
    state.outputPreviewTimeout = setTimeout(function() {
      hideOutputPreview();
    }, 4000);
  }

  // Hide output preview
  function hideOutputPreview() {
    if (!state.outputPreview) return;
    state.outputPreview.style.cssText = STYLES.outputPreview;
    state.outputPreviewHTML = null;
    clearTimeout(state.outputPreviewTimeout);
  }

  // Create micro toast element
  function createMicroToast() {
    var toast = document.createElement('div');
    toast.id = '__devtool-micro-toast';
    toast.style.cssText = STYLES.microToast;
    state.microToast = toast;
    state.container.appendChild(toast);
  }

  // Show a compact micro toast near the bug
  function showMicroToast(text, color) {
    if (!state.microToast || !state.bug) return;

    // Set content
    state.microToast.textContent = text;

    // Subtle left accent line
    var accentColor = color || TOKENS.colors.primary;
    state.microToast.style.cssText = STYLES.microToast;
    state.microToast.style.borderLeft = '2px solid ' + accentColor;

    // Position above the bug, centered
    var bugRect = state.bug.getBoundingClientRect();
    var toastW = state.microToast.offsetWidth || 120;
    var left = bugRect.left + (bugRect.width / 2) - (toastW / 2);
    // Keep on screen
    left = Math.max(8, Math.min(left, window.innerWidth - toastW - 8));

    state.microToast.style.left = left + 'px';
    state.microToast.style.bottom = (window.innerHeight - bugRect.top + 8) + 'px';

    // Animate in
    requestAnimationFrame(function() {
      state.microToast.style.cssText = STYLES.microToast + ';' + STYLES.microToastVisible;
      state.microToast.style.borderLeft = '2px solid ' + accentColor;
      state.microToast.style.left = left + 'px';
      state.microToast.style.bottom = (window.innerHeight - bugRect.top + 8) + 'px';
    });

    // Auto-hide after 2.5 seconds
    clearTimeout(state.microToastTimeout);
    state.microToastTimeout = setTimeout(function() {
      hideMicroToast();
    }, 2500);
  }

  // Hide the micro toast
  function hideMicroToast() {
    if (!state.microToast) return;
    state.microToast.style.cssText = STYLES.microToast;
    clearTimeout(state.microToastTimeout);
  }

  // Create the API activity sparkline element
  function createSparkline() {
    var el = document.createElement('div');
    el.id = '__devtool-sparkline';
    el.setAttribute('aria-hidden', 'true');
    el.style.cssText = STYLES.sparkline;
    el.innerHTML = '<svg width="52" height="12" viewBox="0 0 52 12" xmlns="http://www.w3.org/2000/svg"></svg>';
    state.sparkline = el;
    state.container.appendChild(el);
    positionSparkline();
    updateSparkline();
    // Refresh every 2 seconds
    state.sparklineInterval = setInterval(updateSparkline, 2000);
  }

  // Position sparkline below the bug
  function positionSparkline() {
    if (!state.sparkline || !state.bug) return;
    var bugRect = state.bug.getBoundingClientRect();
    state.sparkline.style.left = bugRect.left + 'px';
    state.sparkline.style.bottom = (window.innerHeight - bugRect.bottom - 18) + 'px';
  }

  // Update sparkline SVG from API tracker data
  function updateSparkline() {
    if (!state.sparkline) return;
    if (!state.isVisible) return; // container hidden — skip the SVG rebuild
    if (!dvApi() || !dvApi().getSparklineData) return;

    var data = dvApi().getSparklineData(60);
    var buckets = data.buckets;
    var maxBucket = data.maxBucket || 1;
    var svg = state.sparkline.querySelector('svg');
    if (!svg) return;

    // Downsample 60 buckets to 26 bars (fit in 52px with 2px per bar)
    var barCount = 26;
    var barWidth = 2;
    var maxH = 10; // leave 1px padding top+bottom
    var step = Math.max(1, Math.floor(buckets.length / barCount));
    var points = '';
    for (var i = 0; i < barCount; i++) {
      var sum = 0;
      var count = 0;
      for (var j = 0; j < step && (i * step + j) < buckets.length; j++) {
        sum += buckets[i * step + j];
        count++;
      }
      var avg = count > 0 ? sum / count : 0;
      var h = Math.round((avg / maxBucket) * maxH);
      if (h < 1 && avg > 0) h = 1; // minimum visible bar for activity
      var x = i * barWidth;
      var y = 12 - h;
      points += '<rect x="' + x + '" y="' + y + '" width="' + (barWidth - 0.5) + '" height="' + h + '" fill="rgba(99,102,241,0.7)" rx="0.5"/>';
    }

    svg.innerHTML = points;
    positionSparkline();

    // Fade out if no recent activity
    var recentTotal = 0;
    for (var k = buckets.length - 5; k < buckets.length; k++) {
      if (k >= 0) recentTotal += buckets[k];
    }
    state.sparkline.style.opacity = recentTotal > 0 ? '1' : '0.3';

    // Emit a one-shot ripple on the bug when fresh API traffic shows up
    // (only while the sustained activity animation isn't already running).
    var freshTotal = 0;
    for (var m = buckets.length - 2; m < buckets.length; m++) {
      if (m >= 0) freshTotal += buckets[m];
    }
    if (freshTotal > 0) fireTrafficPing();
  }

  // Log an event to the history store
  var MAX_HISTORY = 200;

  function makeHistoryEntry(type, text, detail) {
    return {
      id: Date.now() + '_' + Math.random().toString(36).substr(2, 4),
      type: type,       // 'tool', 'message', 'error', 'network', 'screenshot', 'system'
      text: text,
      detail: detail || '',
      timestamp: Date.now()
    };
  }

  function logHistoryEvent(type, text, detail) {
    var current = store.history.val.slice();
    current.unshift(makeHistoryEntry(type, text, detail));
    if (current.length > MAX_HISTORY) current = current.slice(0, MAX_HISTORY);
    store.history.val = current;
  }

  // Append several history entries in ONE reactive state write, so a burst of
  // events costs a single array copy + a single history-panel re-render instead
  // of one per event. entries are in arrival order (oldest first); the store is
  // newest-first, so reverse the batch onto the front to match
  // logHistoryEvent's semantics (last-arrived ends up at index 0).
  function logHistoryBatch(entries) {
    if (!entries || entries.length === 0) return;
    var current = entries.slice().reverse().concat(store.history.val);
    if (current.length > MAX_HISTORY) current = current.slice(0, MAX_HISTORY);
    store.history.val = current;
  }

  // Coalescing queue for tool_event frames. Pre/post-tool-use hooks fire on
  // EVERY tool call, so a parallel read fan-out or a tight tool loop can deliver
  // dozens of frames in a few milliseconds. Rendering each one directly would
  // thrash the DOM: showMicroToast reads layout (getBoundingClientRect,
  // offsetWidth) and rewrites cssText, and each logHistoryEvent triggers a
  // reactive history re-render. The queue collapses a burst into at most one
  // micro-toast + one batched history write per flush window.
  var toolEventQueue = {
    pending: [],        // history entries awaiting a batched flush
    toast: null,        // most recent frame to surface as a micro-toast
    scheduled: false,
    lastFlush: 0
  };
  var TOOL_EVENT_FLUSH_MS = 250;

  function enqueueToolEvent(name, action, detail) {
    var type = action === 'error' ? 'error' : 'tool';
    var text = action === 'error' ? ('Tool error: ' + name)
      : action === 'done' ? (name + ' done')
        : name;
    toolEventQueue.pending.push(makeHistoryEntry(type, text, detail));
    // An error in the window always wins the toast slot (rare + important);
    // otherwise the newest frame represents current activity.
    if (action === 'error' || !toolEventQueue.toast || toolEventQueue.toast.action !== 'error') {
      toolEventQueue.toast = { name: name, action: action };
    }
    scheduleToolEventFlush();
  }

  function scheduleToolEventFlush() {
    if (toolEventQueue.scheduled) return;
    toolEventQueue.scheduled = true;
    var since = Date.now() - toolEventQueue.lastFlush;
    var delay = since >= TOOL_EVENT_FLUSH_MS ? 0 : (TOOL_EVENT_FLUSH_MS - since);
    setTimeout(function() {
      // rAF-align the DOM writes so a flush that lands mid-frame does not force
      // an extra synchronous layout.
      requestAnimationFrame(flushToolEvents);
    }, delay);
  }

  function flushToolEvents() {
    toolEventQueue.scheduled = false;
    toolEventQueue.lastFlush = Date.now();

    var entries = toolEventQueue.pending;
    toolEventQueue.pending = [];
    var toast = toolEventQueue.toast;
    toolEventQueue.toast = null;

    logHistoryBatch(entries);

    if (!toast) return;
    if (toast.action === 'error') {
      showMicroToast('✘ ' + toast.name, TOKENS.colors.error);
    } else if (toast.action === 'done') {
      showMicroToast('✓ ' + toast.name, TOKENS.colors.success);
    } else {
      showMicroToast('⚙ ' + toast.name, TOKENS.colors.primary);
    }
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

    // Activity ripples — two staggered expanding rings, animated only while
    // the AI is active (classes toggled in setActivityState)
    for (var ri = 1; ri <= 2; ri++) {
      var ripple = document.createElement('div');
      ripple.id = '__devtool-ripple-' + ri;
      ripple.className = '__devtool-ripple';
      ripple.style.cssText = STYLES.activityRipple;
      bug.appendChild(ripple);
    }

    // Chaos storm ring + badge (visible only while chaos mode is enabled)
    var chaosRing = document.createElement('div');
    chaosRing.id = '__devtool-chaos-ring';
    chaosRing.style.cssText = STYLES.chaosRing;
    bug.appendChild(chaosRing);

    var chaosBadge = document.createElement('div');
    chaosBadge.id = '__devtool-chaos-badge';
    chaosBadge.style.cssText = STYLES.chaosBadge;
    chaosBadge.textContent = '⛈';
    chaosBadge.title = 'Chaos mode active — failures are being injected';
    bug.appendChild(chaosBadge);

    // Inject CSS animation for pulse effect
    injectActivityAnimation();

    // Status indicator
    var dot = document.createElement('div');
    dot.id = '__devtool-status';
    dot.style.cssText = STYLES.statusDot;
    dot.style.backgroundColor = core.isConnected() ? TOKENS.colors.success : TOKENS.colors.secondary;
    bug.appendChild(dot);

    // Entrance animation, then idle breathing
    bug.classList.add('__devtool-entrance');
    bug.addEventListener('animationend', function handler() {
      bug.classList.remove('__devtool-entrance');
      if (!state.isActive) {
        bug.classList.add('__devtool-breathe');
      }
      bug.removeEventListener('animationend', handler);
    });

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
    // Dedupe against the appropriate target: shadow root or document.head.
    // getInMount resolves to the correct scope automatically.
    if (getInMount('__devtool-activity-style')) return;

    var style = document.createElement('style');
    style.id = '__devtool-activity-style';
    style.textContent = [
      // Entrance animation - spring bounce with rotation
      '@keyframes __devtool-entrance {',
      '  0% { transform: scale(0) rotate(-180deg); opacity: 0; }',
      '  50% { transform: scale(1.12) rotate(8deg); opacity: 1; }',
      '  70% { transform: scale(0.96) rotate(-3deg); }',
      '  100% { transform: scale(1) rotate(0deg); }',
      '}',
      '.__devtool-entrance {',
      // will-change scoped to the class so it is only present during the
      // 0.6s entrance animation; the class is removed on animationend above.
      '  will-change: transform, opacity;',
      '  animation: __devtool-entrance 0.6s cubic-bezier(0.34, 1.56, 0.64, 1) both;',
      '}',
      // Idle breathing glow (box-shadow only; no transform/opacity -> no GPU promotion needed)
      '@keyframes __devtool-breathe {',
      '  0%, 100% { box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 20px rgba(99,102,241,0.2); }',
      '  50% { box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 28px rgba(99,102,241,0.4); }',
      '}',
      '.__devtool-breathe {',
      '  animation: __devtool-breathe 3s ease-in-out infinite;',
      '}',
      // Orbital ring - rotating glow
      // Spinning arc + throb. The arc (transparent border sides) makes the
      // rotation visible; the scale throb pulses the radius so the ring both
      // spins and breathes, clearly signalling "agent working".
      '@keyframes __devtool-orbit {',
      '  0% { transform: rotate(0deg) scale(1); }',
      '  50% { transform: rotate(180deg) scale(1.14); }',
      '  100% { transform: rotate(360deg) scale(1); }',
      '}',
      '.__devtool-active {',
      // Toggled via setActivityState(true/false) in indicator.js; will-change
      // is scoped to the class and removed when the class is removed, so GPU
      // memory is reclaimed when the activity ring is idle.
      '  will-change: transform;',
      '  animation: __devtool-orbit 1s linear infinite;',
      '}',
      // Bug active pulse - scale + shadow throb
      '@keyframes __devtool-active-pulse {',
      '  0%, 100% {',
      '    transform: scale(1);',
      '    box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 20px rgba(99,102,241,0.3), 0 0 0 0 rgba(245,158,11,0);',
      '  }',
      '  50% {',
      '    transform: scale(1.06);',
      '    box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 25px rgba(99,102,241,0.5), 0 0 12px rgba(245,158,11,0.35);',
      '  }',
      '}',
      '.__devtool-bug-active {',
      // Toggled via setActivityState(true/false); removed when idle so the
      // bug bug does not keep a compositor layer around when not pulsing.
      '  will-change: transform;',
      '  animation: __devtool-active-pulse 2s ease-in-out infinite !important;',
      '}',
      // Activity ripples - sonar-style expanding rings emitted while active
      '@keyframes __devtool-ripple-out {',
      '  0% { transform: scale(1); opacity: 0.55; }',
      '  100% { transform: scale(2.1); opacity: 0; }',
      '}',
      '.__devtool-ripple-active-1 {',
      '  will-change: transform, opacity;',
      '  animation: __devtool-ripple-out 1.8s cubic-bezier(0.25, 0.6, 0.4, 1) infinite;',
      '}',
      '.__devtool-ripple-active-2 {',
      '  will-change: transform, opacity;',
      '  animation: __devtool-ripple-out 1.8s cubic-bezier(0.25, 0.6, 0.4, 1) 0.9s infinite;',
      '}',
      // One-shot traffic ping - a quick ripple when proxied requests flow
      '@keyframes __devtool-traffic-ping {',
      '  0% { transform: scale(1); opacity: 0.5; border-color: ' + TOKENS.colors.primary + '; }',
      '  100% { transform: scale(1.9); opacity: 0; border-color: ' + TOKENS.colors.primary + '; }',
      '}',
      '.__devtool-traffic-ping {',
      '  animation: __devtool-traffic-ping 0.7s ease-out 1;',
      '}',
      // Chaos storm ring - counter-rotating dashed ring with electric flicker
      '@keyframes __devtool-chaos-spin {',
      '  0% { transform: rotate(0deg); }',
      '  100% { transform: rotate(-360deg); }',
      '}',
      '@keyframes __devtool-chaos-flicker {',
      '  0%, 100% { box-shadow: 0 0 12px rgba(168,85,247,0.45); border-color: ' + TOKENS.colors.chaos + '; }',
      '  30% { box-shadow: 0 0 22px rgba(124,58,237,0.7); border-color: ' + TOKENS.colors.chaosDeep + '; }',
      '  55% { box-shadow: 0 0 14px rgba(236,72,153,0.5); border-color: #ec4899; }',
      '  80% { box-shadow: 0 0 26px rgba(168,85,247,0.65); border-color: ' + TOKENS.colors.chaos + '; }',
      '}',
      '.__devtool-chaos-active {',
      '  will-change: transform;',
      '  animation: __devtool-chaos-spin 6s linear infinite, __devtool-chaos-flicker 2.2s ease-in-out infinite;',
      '}',
      // Chaos badge wobble - the storm marker sways like weather
      '@keyframes __devtool-chaos-wobble {',
      '  0%, 100% { transform: rotate(-8deg) scale(1); }',
      '  50% { transform: rotate(8deg) scale(1.12); }',
      '}',
      '.__devtool-chaos-badge-on {',
      '  animation: __devtool-chaos-wobble 1.6s ease-in-out infinite;',
      '}',
      // Disconnected - bug desaturates and dims, dot blinks urgently
      '.__devtool-disconnected {',
      '  filter: grayscale(0.75) brightness(0.85);',
      '  transition: filter 0.4s ease;',
      '}',
      '@keyframes __devtool-dot-blink {',
      '  0%, 100% { opacity: 1; transform: scale(1); }',
      '  50% { opacity: 0.25; transform: scale(0.82); }',
      '}',
      '.__devtool-dot-lost {',
      '  animation: __devtool-dot-blink 0.9s ease-in-out infinite;',
      '}',
      // Reconnected - one-shot green shockwave + dot pop
      '@keyframes __devtool-reconnect-flash {',
      '  0% { box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 0 0 rgba(34,197,94,0.65); }',
      '  100% { box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 0 22px rgba(34,197,94,0); }',
      '}',
      '.__devtool-reconnect-flash {',
      '  animation: __devtool-reconnect-flash 0.8s ease-out 1;',
      '}',
      '@keyframes __devtool-dot-pop {',
      '  0% { transform: scale(1); }',
      '  45% { transform: scale(1.65); }',
      '  100% { transform: scale(1); }',
      '}',
      '.__devtool-dot-pop {',
      '  animation: __devtool-dot-pop 0.5s cubic-bezier(0.34, 1.56, 0.64, 1) 1;',
      '}',
      // Output preview throbber - live spinner line, pulses while updating
      '@keyframes __devtool-throbber-pulse {',
      '  0%, 100% { opacity: 1; }',
      '  50% { opacity: 0.55; }',
      '}',
      '.__devtool-throbber-live {',
      '  animation: __devtool-throbber-pulse 1.2s ease-in-out infinite;',
      '}'
    ].join('\n');
    // Append to the shadow root (if active) so the keyframes/classes apply
    // to the indicator UI inside the shadow boundary. Falls back to
    // document.head when the UI mounts on document.body.
    styleTarget().appendChild(style);
  }

  // Set activity state (called when AI tool becomes active/idle)
  function setActivityState(isActive) {
    state.isActive = isActive;
    var ring = getInMount('__devtool-activity-ring');
    var bug = state.bug;
    var ripple1 = getInMount('__devtool-ripple-1');
    var ripple2 = getInMount('__devtool-ripple-2');

    if (isActive) {
      if (bug) {
        bug.classList.remove('__devtool-breathe');
        bug.classList.add('__devtool-bug-active');
      }
      if (ring) {
        ring.classList.add('__devtool-active');
        ring.style.opacity = '1';
      }
      if (ripple1) ripple1.classList.add('__devtool-ripple-active-1');
      if (ripple2) ripple2.classList.add('__devtool-ripple-active-2');
    } else {
      if (bug) {
        bug.classList.remove('__devtool-bug-active');
        bug.classList.add('__devtool-breathe');
      }
      if (ring) {
        ring.classList.remove('__devtool-active');
        ring.style.opacity = '0';
      }
      if (ripple1) ripple1.classList.remove('__devtool-ripple-active-1');
      if (ripple2) ripple2.classList.remove('__devtool-ripple-active-2');
      hideOutputPreview();
    }
  }

  // Toggle the chaos-mode visuals on the bug: counter-rotating dashed storm
  // ring with electric flicker plus a wobbling storm badge. Driven by
  // chaos_state pushes, so MCP/hub changes light it up too.
  function setChaosIndicator(enabled) {
    var ring = getInMount('__devtool-chaos-ring');
    var badge = getInMount('__devtool-chaos-badge');

    if (ring) {
      if (enabled) {
        ring.classList.add('__devtool-chaos-active');
        ring.style.opacity = '1';
      } else {
        ring.classList.remove('__devtool-chaos-active');
        ring.style.opacity = '0';
      }
    }
    if (badge) {
      badge.style.display = enabled ? 'flex' : 'none';
      if (enabled) badge.classList.add('__devtool-chaos-badge-on');
      else badge.classList.remove('__devtool-chaos-badge-on');
    }
  }

  // One-shot traffic ping: quick indigo ripple when proxied requests flow.
  // Reuses ripple-1 only when the sustained activity animation isn't running.
  var trafficPingArmed = true;
  function fireTrafficPing() {
    if (state.isActive || !trafficPingArmed) return;
    var ripple = getInMount('__devtool-ripple-1');
    if (!ripple) return;
    trafficPingArmed = false;
    ripple.classList.add('__devtool-traffic-ping');
    setTimeout(function() {
      ripple.classList.remove('__devtool-traffic-ping');
      trafficPingArmed = true;
    }, 750);
  }

  // Inject container query styles for responsive tabs
  function injectContainerQueryStyles() {
    if (getInMount('__devtool-container-style')) return;

    var style = document.createElement('style');
    style.id = '__devtool-container-style';
    style.textContent = [
      // Make panel a container for container queries
      '#__devtool-panel {',
      '  container-type: inline-size;',
      '  container-name: devtool-panel;',
      '}',
      // Tab bar uses CSS grid - horizontal row with auto-sizing columns
      '.__devtool-tab-bar {',
      '  display: grid !important;',
      '  grid-template-columns: repeat(8, 1fr) auto;',
      '  gap: 0;',
      '  align-items: center;',
      '}',
      // Tab buttons - centered text, no overflow
      '.__devtool-tab {',
      '  justify-content: center;',
      '  text-align: center;',
      '  min-width: 0;',
      '  white-space: nowrap !important;',
      '}',
      // Short label hidden by default (wide panel shows full labels)
      '.__devtool-tab-short { display: none; }',
      '.__devtool-tab-full { display: inline; }',
      // Container query: narrow panel (< 420px) - show short labels
      '@container devtool-panel (max-width: 420px) {',
      '  .__devtool-tab { padding: 6px 4px !important; font-size: 11px !important; }',
      '  .__devtool-tab-short { display: inline; }',
      '  .__devtool-tab-full { display: none; }',
      '}',
      // Container query: very narrow panel (< 300px) - minimal padding
      '@container devtool-panel (max-width: 300px) {',
      '  .__devtool-tab { padding: 4px 2px !important; font-size: 10px !important; }',
      '}',
      // Container query: wide panel (> 520px) - comfortable spacing
      '@container devtool-panel (min-width: 520px) {',
      '  .__devtool-tab { padding: 10px 14px !important; }',
      '}'
    ].join('\n');
    styleTarget().appendChild(style);
  }

  // Inject CSS rules for the audit mega menu popover's fade/translate
  // transition. The native Popover API ties visibility to the :popover-open
  // pseudo-class and the `display`/`overlay` properties — both of which are
  // discrete (not animatable) by default, so the menu would pop in/out
  // instantly without the transition rules below. `allow-discrete` opts into
  // animating the discrete transitions, and `@starting-style` provides the
  // "from" state for the initial open so the fade-in actually happens
  // instead of starting at the "to" state.
  //
  // The `[popover]#__devtool-audit-menu` selector works even when the menu
  // is inside a shadow root because both the `popover` attribute and the id
  // are on the same element; the selector does not cross the shadow
  // boundary, it only matches inside the styleTarget() tree.
  function injectAuditMenuStyles() {
    if (getInMount('__devtool-audit-menu-style')) return;
    // Feature gate: no-op for browsers without popover support so the
    // rules don't leak onto legacy-path elements that toggle visibility
    // via inline cssText.
    if (!(typeof HTMLElement !== 'undefined' && 'popover' in HTMLElement.prototype)) return;

    // Progressive enhancement: CSS Anchor Positioning API.
    // When supported (Chrome 125+, Edge 125+), the browser places the
    // popover directly relative to the audit button without any JS
    // layout reads — no rAF dance, no resize listener, no upper-left
    // corner flash on first open. The `position-try-fallbacks` value
    // makes the browser flip block/inline direction automatically if
    // there's not enough room in the preferred slot.
    //
    // Feature detection uses CSS.supports() which returns true only on
    // engines that have shipped the property, so legacy browsers receive
    // no anchor rules at all and keep the JS fallback path.
    var supportsAnchor =
      typeof CSS !== 'undefined' &&
      typeof CSS.supports === 'function' &&
      CSS.supports('anchor-name: --x') &&
      CSS.supports('position-anchor: --x');

    var rules = [
      '[popover]#__devtool-audit-menu {',
      '  opacity: 0;',
      '  transform: translateY(4px);',
      '  pointer-events: none;',
      '  transition: opacity 0.15s ease, transform 0.15s ease, overlay 0.15s allow-discrete, display 0.15s allow-discrete;',
      '}',
      '[popover]#__devtool-audit-menu:popover-open {',
      '  opacity: 1;',
      '  transform: translateY(0);',
      '  pointer-events: auto;',
      '}',
      '@starting-style {',
      '  [popover]#__devtool-audit-menu:popover-open {',
      '    opacity: 0;',
      '    transform: translateY(4px);',
      '  }',
      '}'
    ];

    if (supportsAnchor) {
      // Modern path: declare the anchor on the button, anchor the menu
      // to the button's bottom-left, and let the browser flip sides if
      // the preferred slot overflows the viewport. No JS positioning.
      //
      // The explicit `top`/`left` here override the empty-string auto
      // values that would otherwise leave the popover at the containing
      // block origin (viewport 0,0) — that was the upper-left-corner
      // bug on the pre-anchor rAF path.
      rules.push('#__devtool-audit-btn {');
      rules.push('  anchor-name: --devtool-audit-anchor;');
      rules.push('}');
      rules.push('[popover]#__devtool-audit-menu {');
      rules.push('  position-anchor: --devtool-audit-anchor;');
      rules.push('  top: anchor(bottom);');
      rules.push('  left: anchor(left);');
      rules.push('  position-try-fallbacks: flip-block, flip-inline;');
      rules.push('}');
    }

    var style = document.createElement('style');
    style.id = '__devtool-audit-menu-style';
    style.textContent = rules.join('\n');
    styleTarget().appendChild(style);
  }

  function createPanel() {
    // Inject container query styles
    injectContainerQueryStyles();

    var panel = document.createElement('div');
    panel.id = '__devtool-panel';
    panel.style.cssText = STYLES.panel + '; display: flex; flex-direction: column;';
    panel.style.display = 'none';
    panel.style.opacity = '0';
    panel.style.transform = 'translateY(8px)';

    // Tab bar (replaces header)
    var tabBar = createTabBar();
    panel.appendChild(tabBar);

    // Tab content area
    var tabContent = document.createElement('div');
    tabContent.id = '__devtool-tab-content';
    tabContent.style.cssText = STYLES.tabContent;
    panel.appendChild(tabContent);

    state.panel = panel;
    state.container.appendChild(panel);

    // Load active tab from localStorage
    try {
      var savedTab = localStorage.getItem('__devtool_active_tab');
      if (savedTab) {
        state.activeTab = savedTab;
      }
    } catch (e) {
      // Ignore localStorage errors
    }

    // Initial render
    switchTab(state.activeTab);
  }

  function createTabBar() {
    var tabBar = document.createElement('div');
    tabBar.className = '__devtool-tab-bar';
    tabBar.style.cssText = STYLES.tabBar;

    var tabs = [
      { id: 'compose', label: 'Message', short: 'Msg', title: 'Send message to agent' },
      { id: 'overview', label: 'Overview', short: 'Info', title: 'Page overview' },
      { id: 'errors', label: 'Errors', short: 'Err', title: 'JavaScript errors' },
      { id: 'network', label: 'Network', short: 'Net', title: 'Network requests' },
      { id: 'performance', label: 'Perf', short: 'Perf', title: 'Performance metrics' },
      { id: 'interactions', label: 'Interact', short: 'Intx', title: 'User interactions' },
      { id: 'history', label: 'History', short: 'Hist', title: 'Event history' },
      { id: 'chaos', label: 'Chaos', short: '⛈', title: 'Chaos engineering — failure injection' }
    ];

    tabs.forEach(function(tabInfo) {
      var tab = document.createElement('button');
      tab.id = '__devtool-tab-' + tabInfo.id;
      tab.className = '__devtool-tab';
      tab.style.cssText = STYLES.tab;
      // Add both full and short labels as spans for container query switching
      tab.innerHTML = '<span class="__devtool-tab-full">' + tabInfo.label + '</span>' +
                      '<span class="__devtool-tab-short">' + tabInfo.short + '</span>';
      if (tabInfo.title) tab.title = tabInfo.title;
      tab.onclick = function() { switchTab(tabInfo.id); };

      // Highlight active tab
      if (state.activeTab === tabInfo.id) {
        tab.style.cssText = STYLES.tab + ';' + STYLES.tabActive;
      }

      tabBar.appendChild(tab);
    });

    // Close button at the end
    var closeBtn = document.createElement('button');
    closeBtn.style.cssText = STYLES.tabCloseBtn;
    closeBtn.innerHTML = ICONS.close;
    closeBtn.setAttribute('aria-label', 'Close panel');
    closeBtn.title = 'Close panel';
    closeBtn.onclick = function(e) { e.stopPropagation(); togglePanel(false); };
    closeBtn.onmouseenter = function() { closeBtn.style.color = TOKENS.colors.text; };
    closeBtn.onmouseleave = function() { closeBtn.style.color = TOKENS.colors.textMuted; };
    tabBar.appendChild(closeBtn);

    return tabBar;
  }

  function switchTab(tabId) {
    state.activeTab = tabId;

    // Save to localStorage
    try {
      localStorage.setItem('__devtool_active_tab', tabId);
    } catch (e) {
      // Ignore localStorage errors
    }

    // Update tab bar highlighting
    var tabs = ['compose', 'overview', 'errors', 'network', 'performance', 'interactions', 'history', 'chaos'];
    tabs.forEach(function(id) {
      var tab = getInMount('__devtool-tab-' + id);
      if (tab) {
        if (id === tabId) {
          tab.style.cssText = STYLES.tab + ';' + STYLES.tabActive;
        } else {
          tab.style.cssText = STYLES.tab;
        }
      }
    });

    // Render tab content
    var content = getInMount('__devtool-tab-content');
    if (!content) return;

    content.innerHTML = '';

    // Refresh data for the tab before rendering
    switch (tabId) {
      case 'overview':
        refreshOverviewData();
        van.add(content, OverviewTabComponent());
        break;
      case 'errors':
        refreshErrorsData();
        van.add(content, ErrorsTabComponent());
        break;
      case 'network':
        refreshNetworkData();
        van.add(content, NetworkTabComponent());
        break;
      case 'performance':
        refreshPerformanceData();
        van.add(content, PerformanceTabComponent());
        break;
      case 'interactions':
        refreshInteractionsData();
        van.add(content, InteractionsTabComponent());
        break;
      case 'history':
        van.add(content, HistoryTabComponent());
        break;
      case 'chaos':
        refreshChaosData();
        van.add(content, ChaosTabComponent());
        break;
      case 'compose':
        renderComposeTab(content);
        break;
    }

    // Adjust panel width for history tab (needs more room for timestamps)
    if (state.panel) {
      state.panel.style.width = (tabId === 'history') ? '540px' : '480px';
    }

    // Start update interval for active tab
    updateTabBadges();
    startTabUpdates();
  }

  // Returns true when a non-empty text selection has at least one endpoint
  // inside the tab content area. The 1s tab refresh writes to reactive stores
  // that VanJS uses to swap whole DOM subtrees inside __devtool-tab-content;
  // a swap mid-selection wipes the user's selection before mouseup fires,
  // making copy/paste from the panel impossible. Skip the data refresh
  // (which is what triggers the swap) while a selection is active. Badge
  // updates outside the content area still run. Selection lookup goes
  // through document first; when the panel lives inside a shadow root and
  // the browser implements ShadowRoot.getSelection (Chromium), we also
  // consult that path so selections fully inside the shadow tree are seen.
  function selectionWithinTabContent() {
    var content = getInMount('__devtool-tab-content');
    if (!content) return false;
    var selections = [];
    try {
      var docSel = document.getSelection && document.getSelection();
      if (docSel) selections.push(docSel);
    } catch (_) { /* getSelection unavailable */ }
    if (isShadowMount()) {
      try {
        var root = mountRoot();
        if (root && typeof root.getSelection === 'function') {
          var shadowSel = root.getSelection();
          if (shadowSel) selections.push(shadowSel);
        }
      } catch (_) { /* ShadowRoot.getSelection unavailable in this engine */ }
    }
    for (var i = 0; i < selections.length; i++) {
      var sel = selections[i];
      if (!sel || sel.isCollapsed) continue;
      var anchor = sel.anchorNode;
      var focus = sel.focusNode;
      if (anchor && content.contains(anchor)) return true;
      if (focus && content.contains(focus)) return true;
    }
    return false;
  }

  function startTabUpdates() {
    // Clear existing interval
    if (state.tabUpdateInterval) {
      clearInterval(state.tabUpdateInterval);
    }

    // Only update if panel is expanded
    if (!state.isExpanded) return;

    // Update every second
    state.tabUpdateInterval = setInterval(function() {
      if (!state.isExpanded) {
        clearInterval(state.tabUpdateInterval);
        state.tabUpdateInterval = null;
        return;
      }

      updateTabBadges();
      // Skip the reactive-store refresh while the user is selecting text in
      // the tab content. Without this gate VanJS swaps DOM nodes under the
      // selection and the browser collapses the range before mouseup, so
      // copy fails. Badges above are out-of-content and safe to update.
      if (selectionWithinTabContent()) return;
      updateActiveTabContent();
    }, 1000);
  }

  function updateTabBadges() {
    // Update error tab badge
    var errorTab = getInMount('__devtool-tab-errors');
    if (errorTab && dvErrors()) {
      var stats = dvErrors().getStats();
      var totalErrors = stats.totalCount;
      updateTabBadge(errorTab, totalErrors, totalErrors > 0 ? 'red' : null);
    }

    // Update network tab badge
    var networkTab = getInMount('__devtool-tab-network');
    if (networkTab && dvApi()) {
      var failedCalls = dvApi().getFailedCalls().length;
      updateTabBadge(networkTab, failedCalls, failedCalls > 0 ? 'red' : null);
    }

    // Update history tab badge (show count of recent events)
    var historyTab = getInMount('__devtool-tab-history');
    if (historyTab) {
      var recentEvents = store.history.val.filter(function(e) {
        return (Date.now() - e.timestamp) < 30000; // Last 30 seconds
      }).length;
      updateTabBadge(historyTab, recentEvents > 0 ? recentEvents : null, recentEvents > 0 ? null : null);
    }

    // Update chaos tab badge (purple dot while chaos is enabled)
    var chaosTab = getInMount('__devtool-tab-chaos');
    if (chaosTab) {
      var chaosOn = store.chaos.val.enabled;
      updateTabBadge(chaosTab, chaosOn ? '●' : null, chaosOn ? 'purple' : null);
    }

    // Update performance tab badge
    var perfTab = getInMount('__devtool-tab-performance');
    if (perfTab && dvMutations()) {
      var rateStats = mutationRateStats([5000]);
      if (rateStats[5000]) {
        var rate = rateStats[5000].rate;
        var color = rate > 50 ? 'red' : (rate > 20 ? 'yellow' : 'green');
        updateTabBadge(perfTab, '●', color);
      }
    }
  }

  function updateTabBadge(tabElement, content, color) {
    // Remove existing badge
    var existing = tabElement.querySelector('[data-badge]');
    if (existing) {
      existing.remove();
    }

    if (!content) return;

    var badge = document.createElement('span');
    badge.setAttribute('data-badge', 'true');
    badge.style.cssText = STYLES.tabBadge;

    if (color === 'red') {
      badge.style.cssText += ';' + STYLES.tabBadgeRed;
    } else if (color === 'yellow') {
      badge.style.cssText += ';' + STYLES.tabBadgeYellow;
    } else if (color === 'green') {
      badge.style.cssText += ';' + STYLES.tabBadgeGreen;
    } else if (color === 'purple') {
      badge.style.cssText += ';' + STYLES.tabBadgePurple;
    }

    badge.textContent = content;
    tabElement.appendChild(badge);
  }

  function updateActiveTabContent() {
    // Only update non-compose tabs (compose is static)
    if (state.activeTab === 'compose') return;

    // Just refresh the store data - VanJS will automatically update the DOM
    switch (state.activeTab) {
      case 'overview':
        refreshOverviewData();
        break;
      case 'errors':
        refreshErrorsData();
        break;
      case 'network':
        refreshNetworkData();
        break;
      case 'performance':
        refreshPerformanceData();
        break;
      case 'interactions':
        refreshInteractionsData();
        break;
      case 'history':
        // Force re-render to update relative timestamps
        store.history.val = store.history.val.slice();
        break;
      case 'chaos':
        // Pull fresh stats while the tab is open; pushed chaos_state covers
        // config changes but stats counters only move with traffic.
        refreshChaosData();
        break;
      // other tabs have no data to refresh
    }
  }

  function renderComposeTab(container) {
    // Compose area (exact copy of original)
    var compose = document.createElement('div');
    compose.style.cssText = 'padding: 0;'; // Remove extra padding since tab content already has padding

    // Message card (groups message + attachments - Gestalt: Common Region)
    var card = document.createElement('div');
    card.id = '__devtool-card';
    card.style.cssText = STYLES.messageCard;

    var textarea = document.createElement('textarea');
    textarea.id = '__devtool-message';
    textarea.style.cssText = STYLES.textarea;
    textarea.placeholder = 'Describe what you need help with... (Ctrl+Enter to send)';
    textarea.value = store.message.val;
    textarea.onfocus = function() {
      card.style.cssText = STYLES.messageCard + ';' + STYLES.messageCardFocused;
    };
    textarea.onblur = function() {
      card.style.cssText = STYLES.messageCard;
    };
    // Off-screen measurement element mirrors the textarea so we can read
    // content height without triggering synchronous layout on the live
    // textarea. We defer both the measurement read AND the subsequent
    // style.height write into a single rAF callback, so no read follows a
    // write on the same element within the same frame.
    var measureEl = null;
    var textareaRafId = 0;
    var textareaLastHeight = 0;

    function getMeasureEl() {
      if (measureEl) return measureEl;
      measureEl = document.createElement('div');
      // Position off-screen but still laid out so offsetHeight is accurate.
      // visibility:hidden keeps it invisible without opting out of layout.
      measureEl.style.cssText = [
        'position: absolute',
        'left: -9999px',
        'top: 0',
        'visibility: hidden',
        'white-space: pre-wrap',
        'word-wrap: break-word',
        'box-sizing: border-box',
        'pointer-events: none'
      ].join(';');
      // Mirror font + sizing properties once on creation. textarea styles
      // are static (from STYLES.textarea) so we do not need to re-read.
      var cs = window.getComputedStyle(textarea);
      measureEl.style.width = cs.width;
      measureEl.style.fontFamily = cs.fontFamily;
      measureEl.style.fontSize = cs.fontSize;
      measureEl.style.fontWeight = cs.fontWeight;
      measureEl.style.lineHeight = cs.lineHeight;
      measureEl.style.padding = cs.padding;
      measureEl.style.border = cs.border;
      measureEl.style.letterSpacing = cs.letterSpacing;
      // Mount measurement element inside the shadow root when possible so
      // it inherits the same font resolution as the textarea it measures.
      // getComputedStyle values (fontFamily/fontSize) are absolute strings
      // so shadow vs body placement is measurement-equivalent, but keeping
      // all devtool DOM nodes inside the same root keeps cleanup simple.
      mountRoot().appendChild(measureEl);
      return measureEl;
    }

    function scheduleTextareaResize() {
      if (textareaRafId) return;
      textareaRafId = requestAnimationFrame(function() {
        textareaRafId = 0;
        var m = getMeasureEl();
        // Use a trailing space so a pure-newline last line still contributes
        // a measurable row (textContent collapses trailing whitespace runs).
        m.textContent = textarea.value + ' ';
        // Read measurement element's height (safe — no pending write on it)
        var measured = m.offsetHeight;
        var newHeight = Math.min(Math.max(measured, 80), 200);
        if (newHeight === textareaLastHeight) return;

        var previousHeight = state.panel ? state.panel.offsetHeight : 0;
        textarea.style.height = newHeight + 'px';
        textareaLastHeight = newHeight;

        // Reposition panel to expand upward on next frame to let the
        // textarea height change settle before we read panel.offsetHeight.
        if (state.panel && state.isExpanded) {
          requestAnimationFrame(function() {
            var panelHeight = state.panel ? state.panel.offsetHeight : 0;
            var heightDiff = panelHeight - previousHeight;
            if (heightDiff !== 0) {
              var currentTop = parseInt(state.panel.style.top) || 0;
              state.panel.style.top = (currentTop - heightDiff) + 'px';
            }
          });
        }
      });
    }

    // Auto-expand textarea based on content (rAF-batched, no layout thrash)
    textarea.oninput = function() {
      store.message.val = textarea.value;
      scheduleTextareaResize();
    };
    // Ctrl+Enter to send
    textarea.onkeydown = function(e) {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        handleSend();
      }
    };
    card.appendChild(textarea);

    // Attachment chips container - REACTIVE with VanJS
    // This automatically updates when store.attachments changes
    van.add(card, AttachmentAreaComponent());

    compose.appendChild(card);
    container.appendChild(compose);

    // Toolbar with actions
    var toolbar = document.createElement('div');
    toolbar.style.cssText = STYLES.toolbar;

    // Action buttons container (wraps for responsive layout)
    var actionsContainer = document.createElement('div');
    actionsContainer.style.cssText = STYLES.toolbarActions;

    // Tool buttons (Gestalt: Similarity - all secondary actions look alike)
    var refreshBtn = createToolBtn('Refresh', ICONS.refresh, refreshContent);
    refreshBtn.title = 'Reload the app (content frame) without dropping the agnt connection';
    var screenshotBtn = createToolBtn('Screenshot', ICONS.screenshot, startScreenshotMode);
    var elementBtn = createToolBtn('Element', ICONS.element, startElementMode);
    var sketchBtn = createToolBtn('Sketch', ICONS.sketch, openSketch);
    var designBtn = createToolBtn('Design', ICONS.design, startDesignMode);
    state.inspectBtn = createToolBtn('Inspect', ICONS.inspect, startInspectMode);
    state.inspectBtn.title = 'Live style editor \u2014 inspect and edit CSS';
    var responsiveBtn = createToolBtn('Responsive', ICONS.responsive, startResponsiveMode);
    responsiveBtn.title = 'Responsive mode \u2014 live iframe at controllable width';
    // Override hover handlers to preserve active state
    state.inspectBtn.onmouseleave = function() {
      var sw = targetWindow();
      var active = sw.__devtool_style_editor && sw.__devtool_style_editor.isOpen();
      if (!active) {
        state.inspectBtn.style.background = 'transparent';
        state.inspectBtn.style.borderColor = TOKENS.colors.border;
        state.inspectBtn.style.color = TOKENS.colors.textMuted;
      }
    };
    var auditDropdown = createActionsDropdown();

    actionsContainer.appendChild(refreshBtn);
    actionsContainer.appendChild(screenshotBtn);
    actionsContainer.appendChild(elementBtn);
    actionsContainer.appendChild(sketchBtn);
    actionsContainer.appendChild(designBtn);
    actionsContainer.appendChild(state.inspectBtn);
    actionsContainer.appendChild(responsiveBtn);
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

    container.appendChild(toolbar);
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

  // refreshContent reloads the app the developer is looking at (the content
  // frame) without tearing down the chrome shell that hosts the indicator,
  // panel, and control WebSocket — so the agnt connection survives the reload.
  // In the shell this delegates to __devtool_reload_content (frames.js); in an
  // unwrapped/standalone page there is no separate shell, so we reload self.
  function refreshContent() {
    try {
		if (frameContext.isChrome()) {
			frameContext.reloadContent();
        return;
      }
      var w = targetWindow();
      if (w && w.location) { w.location.reload(); }
    } catch (e) { /* best-effort — a failed refresh must not break the toolbar */ }
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
        var w = targetWindow();
        if (w.__devtool && w.__devtool.auditPageQuality) {
          return w.__devtool.auditPageQuality();
        }
        return Promise.resolve({ error: 'Page quality audit not available' });
      }
    },
    {
      id: 'accessibility',
      label: 'Accessibility',
      description: 'Check for a11y issues (WCAG)',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool_accessibility) {
          return w.__devtool_accessibility.auditAccessibility();
        }
        return { error: 'Accessibility module not loaded' };
      }
    },
    {
      id: 'security',
      label: 'Security',
      description: 'Mixed content, XSS risks, noopener',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool_audit) {
          return w.__devtool_audit.auditSecurity();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'seo',
      label: 'SEO / Meta',
      description: 'Meta tags, headings, structure',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool_audit) {
          return w.__devtool_audit.auditPageQuality();
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
        var w = targetWindow();
        if (w.__devtool && w.__devtool.diagnoseLayout) {
          return w.__devtool.diagnoseLayout();
        }
        return { error: 'Layout diagnostics not available' };
      }
    },
    {
      id: 'textFragility',
      label: 'Text Fragility',
      description: 'Truncation, overflow, font issues',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool && w.__devtool.checkTextFragility) {
          return w.__devtool.checkTextFragility();
        }
        return { error: 'Text fragility check not available' };
      }
    },
    {
      id: 'responsiveRisk',
      label: 'Responsive Risk',
      description: 'Elements that may break at different sizes',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool && w.__devtool.checkResponsiveRisk) {
          return w.__devtool.checkResponsiveRisk();
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
        var w = targetWindow();
        if (w.__devtool_interactions) {
          return w.__devtool_interactions.getLastClickContext();
        }
        return { error: 'Interaction tracking not available' };
      }
    },
    {
      id: 'recentMutations',
      label: 'Recent DOM Changes',
      description: 'What changed in the DOM recently',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool_mutations) {
          return {
            added: w.__devtool_mutations.getAdded(Date.now() - 30000),
            removed: w.__devtool_mutations.getRemoved(Date.now() - 30000),
            modified: w.__devtool_mutations.getModified(Date.now() - 30000)
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
        var w = targetWindow();
        if (w.__devtool_capture) {
          return w.__devtool_capture.captureState(['localStorage', 'sessionStorage', 'cookies']);
        }
        return { error: 'State capture not available' };
      }
    },
    {
      id: 'networkSummary',
      label: 'Network/Resources',
      description: 'Resource timing and loading data',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool_capture) {
          return w.__devtool_capture.captureNetwork();
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
        var w = targetWindow();
        if (w.__devtool_audit) {
          return w.__devtool_audit.auditDOMComplexity();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'css',
      label: 'CSS Quality',
      description: 'Inline styles, !important usage',
      fn: function() {
        var w = targetWindow();
        if (w.__devtool_audit) {
          return w.__devtool_audit.auditCSS();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'performance',
      label: 'Performance',
      description: 'Resource timing, render-blocking, layout thrash',
      async: true,
      fn: function() {
        var w = targetWindow();
        if (w.__devtool_audit) {
          return w.__devtool_audit.auditPerformance();
        }
        return { error: 'Audit module not loaded' };
      }
    }
  ];

  // Create the Actions dropdown
  function createActionsDropdown() {
    // Baseline (April 2026): Chrome 114+, Edge 114+, Firefox 125+, Safari 17+.
    // Older browsers fall through to the legacy absolute-positioning code
    // path further down. Keep the legacy block byte-for-byte identical to
    // its pre-migration implementation so we don't regress.
    var supportsPopover = typeof HTMLElement !== 'undefined' && 'popover' in HTMLElement.prototype;

    // Progressive enhancement: CSS Anchor Positioning API. When both
    // `anchor-name` and `position-anchor` are supported, the browser
    // places the popover relative to the button via the stylesheet
    // rules emitted by injectAuditMenuStyles(). The JS `repositionMenu`
    // dance (getBoundingClientRect + rAF + resize listener) is skipped
    // entirely on this path, which also fixes the upper-left-corner
    // race that bit pre-anchor implementations.
    var supportsAnchorPositioning =
      typeof CSS !== 'undefined' &&
      typeof CSS.supports === 'function' &&
      CSS.supports('anchor-name: --x') &&
      CSS.supports('position-anchor: --x');

    // Inject the popover transition + anchor positioning rules
    // (no-op for legacy browsers; anchor rules only emitted when the
    // feature is detected above inside injectAuditMenuStyles).
    injectAuditMenuStyles();

    var container = document.createElement('div');
    container.style.cssText = STYLES.dropdownContainer;

    var btn = document.createElement('button');
    // The id is required so the #__devtool-audit-btn { anchor-name: ... }
    // rule injected by injectAuditMenuStyles() can target this element.
    // It also makes the button easy to inspect in devtools when triaging
    // positioning regressions.
    btn.id = '__devtool-audit-btn';
    btn.style.cssText = STYLES.dropdownBtn;
    btn.innerHTML = ICONS.actions + ' Audit ' + ICONS.chevronDown;
    container.appendChild(btn);

    var menu = document.createElement('div');
    menu.style.cssText = STYLES.megaMenu;
    menu.id = '__devtool-audit-menu';

    // Modern path: visibility (opacity / transform / pointer-events) is owned
    // by the stylesheet rules from injectAuditMenuStyles() via :popover-open.
    // STYLES.megaMenu still includes the "hidden" defaults so the legacy path
    // can toggle them inline, but inline styles beat stylesheet rules — so
    // when the popover opens, the inline opacity:0 wins and the menu renders
    // invisible. Clear the visibility properties from inline on the modern
    // path so the stylesheet can take over.
    if (supportsPopover) {
      menu.style.opacity = '';
      menu.style.transform = '';
      menu.style.pointerEvents = '';
    }

    // Anchor-positioning path: the `inset: unset` inline expansion from
    // STYLES.megaMenu sets top/right/bottom/left to auto at inline
    // specificity, which would beat the stylesheet's
    // `top: anchor(bottom); left: anchor(left);` rules and leave the
    // menu at the top-layer containing-block origin (viewport 0,0).
    // Clear those four longhands so the stylesheet cascade can apply
    // the anchor() values unopposed.
    //
    // Note: we clear the individual longhands (not the `inset`
    // shorthand) because browsers expand shorthand assignments into
    // longhand inline declarations, and setting `inset = ''` only
    // removes a pending shorthand value — the already-expanded
    // longhands would persist. Clearing the longhands directly is the
    // only reliable way to drop them from the inline style map.
    if (supportsAnchorPositioning) {
      menu.style.top = '';
      menu.style.right = '';
      menu.style.bottom = '';
      menu.style.left = '';
    }

    // Group actions by category
    var sections = [
      { label: 'Quality Audits', ids: ['fullAudit', 'accessibility', 'security', 'seo'] },
      { label: 'Layout & Visual', ids: ['layoutIssues', 'textFragility', 'responsiveRisk'] },
      { label: 'Debug Context', ids: ['lastClick', 'recentMutations'] },
      { label: 'State & Network', ids: ['captureState', 'networkSummary'] },
      { label: 'Technical', ids: ['domComplexity', 'css', 'performance'] }
    ];

    // Helper: build a menu item with label + description blurb
    function buildMenuItem(action) {
      var item = document.createElement('button');
      item.style.cssText = STYLES.megaMenuItem;
      var labelSpan = document.createElement('span');
      labelSpan.style.cssText = STYLES.megaMenuItemLabel;
      labelSpan.textContent = action.label;
      var descSpan = document.createElement('span');
      descSpan.style.cssText = STYLES.megaMenuItemDesc;
      descSpan.textContent = action.description;
      item.appendChild(labelSpan);
      item.appendChild(descSpan);

      item.onmouseenter = function() {
        item.style.cssText = STYLES.megaMenuItem + ';' + STYLES.megaMenuItemHover;
      };
      item.onmouseleave = function() {
        item.style.cssText = STYLES.megaMenuItem;
      };

      item.onclick = function(e) {
        e.stopPropagation();
        dismissMenu();
        runAuditAction(action);
      };

      return item;
    }

    // Legacy close handle. Assigned inside the legacy branch below; the
    // `dismissMenu` helper reads this so the shared scope can invoke the
    // block-scoped closeDropdown defined inside the legacy path.
    var legacyClose = null;

    // Unified dismissal: hide the popover on the modern path, fall through
    // to the legacy close routine otherwise. Safe to call before
    // legacyClose is assigned because this wrapper is only invoked in
    // response to a click on an already-open menu item, by which time
    // one of the two paths has wired itself up.
    function dismissMenu() {
      if (supportsPopover) {
        try {
          if (menu.matches && menu.matches(':popover-open')) {
            menu.hidePopover();
          }
        } catch (_e) {
          // hidePopover() can throw InvalidStateError if the menu was
          // already dismissed by a light-dismiss race. Swallow it — the
          // menu is closed either way.
        }
      } else if (legacyClose) {
        legacyClose();
      }
    }

    // Top row: first 4 sections as columns
    var topSections = sections.slice(0, 4);
    var topRow = document.createElement('div');
    topRow.style.cssText = STYLES.megaMenuTopRow;

    topSections.forEach(function(section, colIndex) {
      var col = document.createElement('div');
      col.style.cssText = STYLES.megaMenuColumn;
      if (colIndex === topSections.length - 1) {
        col.style.cssText = STYLES.megaMenuColumn + ';' + STYLES.megaMenuColumnLast;
      }

      var header = document.createElement('div');
      header.style.cssText = STYLES.megaMenuColumnHeader;
      header.textContent = section.label;
      col.appendChild(header);

      section.ids.forEach(function(actionId) {
        var action = AUDIT_ACTIONS.find(function(a) { return a.id === actionId; });
        if (!action) return;
        col.appendChild(buildMenuItem(action));
      });

      topRow.appendChild(col);
    });

    menu.appendChild(topRow);

    // Bottom row: Technical section spanning full width
    var techSection = sections[4];
    var bottomRow = document.createElement('div');
    bottomRow.style.cssText = STYLES.megaMenuBottomRow;

    var techHeader = document.createElement('div');
    techHeader.style.cssText = STYLES.megaMenuTechnicalHeader;
    techHeader.textContent = techSection.label;
    bottomRow.appendChild(techHeader);

    var techGrid = document.createElement('div');
    techGrid.style.cssText = STYLES.megaMenuTechnicalGrid;

    techSection.ids.forEach(function(actionId) {
      var action = AUDIT_ACTIONS.find(function(a) { return a.id === actionId; });
      if (!action) return;
      techGrid.appendChild(buildMenuItem(action));
    });

    bottomRow.appendChild(techGrid);
    menu.appendChild(bottomRow);

    // -----------------------------------------------------------------
    // Path split: modern (native Popover API) vs legacy (JS-positioned
    // position:fixed). Both paths are shown the same DOM, differ only in
    // how the menu is mounted and how it's toggled + positioned.
    //
    // The hover handlers and the scheduleReposition/repositionMenu helpers
    // are shared between both paths because they only read/write local
    // closure state (`isOpen`, `rafPositionId`) which each path manages.
    // -----------------------------------------------------------------

    // Toggle state — read by shared helpers (repositionMenu guard,
    // btn.onmouseenter/leave) and written by both paths' open/close flows.
    var isOpen = false;
    // rAF handle for resize-coalesced reposition. Cancelled in closeDropdown
    // (legacy) and in the 'closed' branch of beforetoggle (modern) to avoid
    // a ghost reposition after the menu is closed.
    var rafPositionId = 0;

    // Reposition the menu relative to the audit button. Uses position: fixed
    // coordinates so ancestor overflow boxes (.tabContent) cannot clip us.
    // Opens upward by default; falls back to opening below when the upward
    // slot would clip the top of the viewport. Used by both paths.
    function repositionMenu() {
      var btnRect = btn.getBoundingClientRect();
      var menuW = menu.offsetWidth;
      var menuH = menu.offsetHeight;
      var vw = window.innerWidth;
      var vh = window.innerHeight;

      // Prefer opening above the button (matches previous bottom: 100% behavior).
      var top = btnRect.top - menuH - 4;
      if (top < 8) {
        // Not enough room above; open below the button instead.
        top = btnRect.bottom + 4;
        // Clamp bottom edge to viewport.
        if (top + menuH > vh - 8) {
          top = Math.max(8, vh - menuH - 8);
        }
      }

      var left = btnRect.left;
      if (left + menuW > vw - 8) {
        left = vw - menuW - 8;
      }
      if (left < 8) {
        left = 8;
      }

      menu.style.top = top + 'px';
      menu.style.left = left + 'px';
    }

    // Stable handler reference so removeEventListener matches addEventListener.
    // Declared once in the closure (NOT recreated per open) so open/close
    // flows always reference the same function identity. Shared by both paths.
    function scheduleReposition() {
      if (rafPositionId) {
        cancelAnimationFrame(rafPositionId);
      }
      rafPositionId = requestAnimationFrame(function() {
        rafPositionId = 0;
        if (isOpen) {
          repositionMenu();
        }
      });
    }

    // Hover affordance on the audit button. Shared by both paths.
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

    if (!supportsPopover) {
      // ==================== LEGACY PATH ====================
      // Byte-for-byte the pre-migration behavior: menu appended to the
      // dropdownContainer, manual outside-click/scroll close, manual open
      // and close helpers. Kept intact for pre-baseline browsers.

      container.appendChild(menu);

      function handleScrollClose() {
        closeDropdown();
      }

      // Iter 13 migration: prefer composedPath() over Node.contains() so
      // outside-click detection is O(1) relative to the event path length
      // instead of walking the entire subtree.
      function handleOutsideClick(e) {
        var path = typeof e.composedPath === 'function' ? e.composedPath() : null;
        if (path) {
          if (path.indexOf(container) === -1) closeDropdown();
          return;
        }
        if (!container.contains(e.target)) {
          closeDropdown();
        }
      }

      function openDropdown() {
        isOpen = true;
        // Apply visible styles FIRST so offsetWidth/offsetHeight are accurate
        // when repositionMenu() measures the menu.
        menu.style.cssText = STYLES.megaMenu + ';' + STYLES.megaMenuVisible;

        // Responsive: reduce columns on narrow viewports. Must happen BEFORE
        // repositionMenu() so width/height are measured at final column count.
        var vw = window.innerWidth;
        if (vw < 360) {
          topRow.style.gridTemplateColumns = '1fr 1fr';
          techGrid.style.gridTemplateColumns = '1fr 1fr';
        } else if (vw < 480) {
          topRow.style.gridTemplateColumns = '1fr 1fr';
        } else {
          topRow.style.gridTemplateColumns = '1fr 1fr 1fr 1fr';
          techGrid.style.gridTemplateColumns = '1fr 1fr 1fr';
        }

        // Initial positioning: sync (not rAF) so the menu never flashes at 0,0.
        repositionMenu();

        btn.style.background = TOKENS.colors.surface;
        btn.style.borderColor = TOKENS.colors.primary;
        btn.style.color = TOKENS.colors.primary;

        // Lifecycle listeners. scroll uses capture:true so it catches scrolls
        // on any ancestor (including .tabContent) since scroll events do not
        // bubble.
        window.addEventListener('resize', scheduleReposition);
        document.addEventListener('scroll', handleScrollClose, { capture: true, passive: true });
        document.addEventListener('click', handleOutsideClick);
      }

      function closeDropdown() {
        isOpen = false;
        menu.style.cssText = STYLES.megaMenu;
        // Clear the dynamic positions so a stale top/left from this open does
        // not flicker into view on the next open before repositionMenu() runs.
        menu.style.top = '';
        menu.style.left = '';

        // Cancel any pending rAF reposition to avoid a ghost update after close.
        if (rafPositionId) {
          cancelAnimationFrame(rafPositionId);
          rafPositionId = 0;
        }

        btn.style.background = 'transparent';
        btn.style.borderColor = TOKENS.colors.border;
        btn.style.color = TOKENS.colors.textMuted;

        // Remove with the same function references and option flags used in open.
        window.removeEventListener('resize', scheduleReposition);
        document.removeEventListener('scroll', handleScrollClose, { capture: true, passive: true });
        document.removeEventListener('click', handleOutsideClick);
      }

      btn.onclick = function(e) {
        e.stopPropagation();
        isOpen ? closeDropdown() : openDropdown();
      };

      // Expose to shared-scope dismissMenu so item clicks can close us.
      legacyClose = closeDropdown;

      return container;
    }

    // ==================== MODERN PATH (Popover API) ====================
    // The native HTML Popover API renders the menu in the top layer so no
    // ancestor overflow/contain/transform stacking context can clip it. The
    // browser also handles light-dismiss (outside click) and Escape for us,
    // replacing the handleOutsideClick and keydown wiring from the legacy
    // path.
    //
    // Mount location: same mount root as the rest of the indicator (shadow
    // root if iter 15's shadow-root.js is active, else document.body). The
    // popover top layer is per-document, so visually the menu still floats
    // above every page element regardless of which tree it's attached to.
    // Mounting inside the same tree as btn is required so the native
    // `popovertarget` attribute can resolve menu.id via the containing
    // tree's getElementById — popovertarget does NOT cross shadow
    // boundaries, so if btn is inside the shadow root, menu must be too.
    menu.popover = 'auto';
    btn.setAttribute('popovertarget', menu.id);
    // Important: menu is NOT appended to `container` on the modern path.
    // It's a sibling of the rest of the indicator UI inside the mount root.
    // The audit button stays inside `container`; the popover is wired back
    // to it only by the popovertarget attribute.
    mountRoot().appendChild(menu);

    var resizeListener = null;
    var scrollListener = null;

    menu.addEventListener('beforetoggle', function(e) {
      if (e.newState === 'open') {
        isOpen = true;

        // Responsive column layout must happen BEFORE measuring so the
        // menu has its final dimensions when repositionMenu reads them.
        var vw = window.innerWidth;
        if (vw < 360) {
          topRow.style.gridTemplateColumns = '1fr 1fr';
          techGrid.style.gridTemplateColumns = '1fr 1fr';
        } else if (vw < 480) {
          topRow.style.gridTemplateColumns = '1fr 1fr';
        } else {
          topRow.style.gridTemplateColumns = '1fr 1fr 1fr 1fr';
          techGrid.style.gridTemplateColumns = '1fr 1fr 1fr';
        }

        if (!supportsAnchorPositioning) {
          // Legacy rAF path for engines with the Popover API but without
          // CSS Anchor Positioning (Firefox stable as of writing, older
          // Safari). Compute position AFTER the browser has laid out the
          // popover in the top layer.
          //
          // DOUBLE rAF is required: a single rAF callback runs after
          // style/layout for the current frame but BEFORE the popover's
          // top-layer promotion + display change has been committed in
          // some engines (the HTML spec dispatches beforetoggle before
          // the element is added to the top layer). In that race,
          // offsetWidth/offsetHeight still return 0 and repositionMenu
          // would place the menu at nonsensical coordinates, leaving
          // it at the viewport upper-left.
          //
          // The second rAF nested inside the first guarantees we run
          // AFTER the first post-beforetoggle paint, at which point
          // the popover is laid out in the top layer and the measure
          // call returns real pixel dimensions.
          requestAnimationFrame(function() {
            requestAnimationFrame(function() {
              if (isOpen) repositionMenu();
            });
          });

          // Reposition on window resize. Save the function reference in
          // a closure-scoped var so the matching removeEventListener call
          // in the 'closed' branch can find it.
          resizeListener = scheduleReposition;
          window.addEventListener('resize', resizeListener);
        }
        // Modern anchor-positioning path: the browser handles placement
        // and resize automatically via the stylesheet rules in
        // injectAuditMenuStyles(). No rAF, no resize listener.

        // Active button styling.
        btn.style.background = TOKENS.colors.surface;
        btn.style.borderColor = TOKENS.colors.primary;
        btn.style.color = TOKENS.colors.primary;

        // Close on any ancestor scroll (matches legacy path behavior,
        // and satisfies the "scroll closes the menu" acceptance
        // criterion on both the anchor-positioning and rAF paths).
        scrollListener = function() {
          try {
            if (menu.matches && menu.matches(':popover-open')) {
              menu.hidePopover();
            }
          } catch (_e) {
            // Swallow InvalidStateError from a race with light-dismiss.
          }
        };
        document.addEventListener('scroll', scrollListener, { capture: true, passive: true });
      } else if (e.newState === 'closed') {
        isOpen = false;

        // Reset button styling.
        btn.style.background = 'transparent';
        btn.style.borderColor = TOKENS.colors.border;
        btn.style.color = TOKENS.colors.textMuted;

        // Clear computed coordinates so stale top/left from this open does
        // not flicker into view on the next open before repositionMenu runs.
        menu.style.top = '';
        menu.style.left = '';

        // Cancel any pending rAF reposition to avoid a ghost update after close.
        if (rafPositionId) {
          cancelAnimationFrame(rafPositionId);
          rafPositionId = 0;
        }

        // Unregister lifecycle listeners. Must use the same references
        // and option flags we passed to addEventListener above.
        if (resizeListener) {
          window.removeEventListener('resize', resizeListener);
          resizeListener = null;
        }
        if (scrollListener) {
          document.removeEventListener('scroll', scrollListener, { capture: true, passive: true });
          scrollListener = null;
        }
      }
    });

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
  // Updated to use new action-oriented audit schema with summary, score, grade
  function formatAuditSummary(auditId, result) {
    if (!result) {
      return 'No data captured';
    }
    if (result.error) {
      return 'Error: ' + result.error;
    }

    // New schema: if result has summary field, use it directly
    if (result.summary && typeof result.summary === 'string') {
      var prefix = '';
      if (result.grade) {
        prefix = '[' + result.grade + '] ';
      } else if (result.score !== undefined) {
        prefix = '[' + result.score + '/100] ';
      }
      return prefix + result.summary;
    }

    // Legacy support for older audit formats
    switch (auditId) {
      // Quality Audits
      case 'fullAudit':
        return 'Grade: ' + (result.grade || '?') + ' (' + (result.overallScore || 0) + '/100) - ' +
               (result.criticalIssues ? result.criticalIssues.length : 0) + ' critical issues';

      case 'accessibility':
        if (result.stats) {
          // AI-optimized format uses critical/serious/moderate/minor
          if (result.stats.totalIssues !== undefined) {
            var criticalCount = (result.stats.critical || 0) + (result.stats.serious || 0);
            var otherCount = (result.stats.moderate || 0) + (result.stats.minor || 0);
            if (result.stats.totalIssues === 0) {
              return '[' + (result.grade || 'A') + '] No accessibility issues found across ' + (result.stats.rulesChecked || result.stats.passed || 0) + ' checks.';
            }
            return '[' + (result.grade || '?') + '] ' + criticalCount + ' critical accessibility error' + (criticalCount !== 1 ? 's' : '') + ' found' + (otherCount > 0 ? ': ' + otherCount + ' ' + Object.keys(result.raw?.issuesByType || {}).slice(0, 3).join(', ') : '') + '.';
          }
          // Legacy format
          return '[' + (result.grade || '?') + '] ' + result.stats.errors + ' errors, ' + result.stats.warnings + ' warnings';
        }
        return result.count + ' issue(s): ' + result.errors + ' errors, ' + result.warnings + ' warnings';

      case 'security':
        if (result.stats) {
          // AI-optimized format
          if (result.stats.totalIssues !== undefined) {
            var criticalCount = (result.stats.critical || 0);
            var errorCount = (result.stats.errors || 0);
            if (result.stats.totalIssues === 0) {
              return '[' + (result.grade || 'A') + '] No security issues found.';
            }
            return '[' + (result.grade || '?') + '] ' + criticalCount + ' critical, ' + errorCount + ' errors';
          }
          // Legacy format
          return '[' + (result.grade || '?') + '] ' + result.stats.errors + ' errors, ' + result.stats.warnings + ' warnings';
        }
        return result.count + ' issue(s): ' + result.errors + ' errors, ' + result.warnings + ' warnings';

      case 'seo':
        if (result.meta && result.meta.title) {
          return '[' + (result.grade || '?') + '] Title: "' + result.meta.title.value.substring(0, 30) + '"';
        }
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
        if (result.metrics) {
          return '[' + (result.grade || '?') + '] ' + result.metrics.totalElements + ' elements, depth ' + result.metrics.maxDepth;
        }
        var rating = result.rating || 'unknown';
        return result.totalElements + ' nodes, depth ' + result.maxDepth + ' (' + rating + ')';

      case 'css':
        if (result.metrics) {
          return '[' + (result.grade || '?') + '] ' + result.metrics.inlineStyleCount + ' inline styles, ' + result.stats.fixable + ' issues';
        }
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

  // Format audit result as actionable markdown for AI agent
  // Prioritizes issues by severity and provides fix instructions
  function formatAuditForAgent(auditType, label, result) {
    if (!result) {
      return '**' + label + '**: No data';
    }
    if (result.error) {
      return '**' + label + '**: Error - ' + result.error;
    }

    var lines = [];
    var grade = result.grade || '?';
    var score = result.score !== undefined ? result.score + '/100' : '';

    lines.push('**' + label + '** [' + grade + (score ? ', ' + score : '') + ']');

    // Handle accessibility audit (AI-optimized format)
    if (auditType === 'accessibility' && result.raw && result.raw.issuesByType) {
      var issues = result.raw.issuesByType;
      var issueKeys = Object.keys(issues);

      if (issueKeys.length === 0) {
        lines.push('No issues found.');
        return lines.join('\n');
      }

      // Sort by impact: critical > serious > moderate > minor
      var impactOrder = { critical: 0, serious: 1, moderate: 2, minor: 3 };
      issueKeys.sort(function(a, b) {
        return (impactOrder[issues[a].impact] || 4) - (impactOrder[issues[b].impact] || 4);
      });

      lines.push('');
      lines.push('**Fix these issues:**');

      // Limit to top 5 most important
      issueKeys.slice(0, 5).forEach(function(key, idx) {
        var issue = issues[key];
        var severity = issue.impact === 'critical' || issue.impact === 'serious' ? 'ERROR' : 'WARN';
        lines.push((idx + 1) + '. **' + issue.ruleId + '** [' + severity + '] - ' + issue.count + ' instance(s)');
        lines.push('   ' + issue.message);
        if (issue.fix) {
          lines.push('   Fix: ' + issue.fix);
        }
        if (issue.examples && issue.examples.length > 0) {
          lines.push('   Target: `' + issue.examples[0].selector + '`');
        }
      });

      if (issueKeys.length > 5) {
        lines.push('');
        lines.push('_+ ' + (issueKeys.length - 5) + ' more issues_');
      }

      return lines.join('\n');
    }

    // Handle security audit
    if (auditType === 'security' && result.raw && result.raw.issuesByType) {
      var issues = result.raw.issuesByType;
      var issueKeys = Object.keys(issues);

      if (issueKeys.length === 0) {
        lines.push('No security issues found.');
        return lines.join('\n');
      }

      lines.push('');
      lines.push('**Security issues:**');

      issueKeys.slice(0, 5).forEach(function(key, idx) {
        var issueList = issues[key];
        var count = issueList.length;
        var first = issueList[0] || {};
        lines.push((idx + 1) + '. **' + key + '** - ' + count + ' instance(s)');
        if (first.message) lines.push('   ' + first.message);
        if (first.selector) lines.push('   Target: `' + first.selector + '`');
      });

      return lines.join('\n');
    }

    // Handle layout issues audit
    if (auditType === 'layoutIssues') {
      var overflows = result.overflows || [];
      var stacking = result.stackingContexts || [];
      var offscreen = result.offscreen || [];
      var totalIssues = overflows.length + stacking.length + offscreen.length;

      if (totalIssues === 0) {
        lines.push('No layout issues found.');
        return lines.join('\n');
      }

      lines.push('');

      if (overflows.length > 0) {
        lines.push('**Overflow Issues** (' + overflows.length + '):');
        overflows.slice(0, 3).forEach(function(item, idx) {
          lines.push((idx + 1) + '. `' + item.selector + '` - ' + item.type + ' overflow');
          lines.push('   Content: ' + item.scrollWidth + 'x' + item.scrollHeight + ', Container: ' + item.clientWidth + 'x' + item.clientHeight);
          lines.push('   Fix: Add overflow handling or resize container');
        });
        if (overflows.length > 3) {
          lines.push('   _+ ' + (overflows.length - 3) + ' more_');
        }
        lines.push('');
      }

      if (stacking.length > 0) {
        lines.push('**Stacking Contexts** (' + stacking.length + '):');
        stacking.slice(0, 3).forEach(function(item, idx) {
          var reasons = item.reason ? item.reason.join(', ') : 'unknown';
          lines.push((idx + 1) + '. `' + item.selector + '` - z-index: ' + item.zIndex);
          lines.push('   Reason: ' + reasons);
        });
        if (stacking.length > 3) {
          lines.push('   _+ ' + (stacking.length - 3) + ' more_');
        }
        lines.push('');
      }

      if (offscreen.length > 0) {
        lines.push('**Offscreen Elements** (' + offscreen.length + '):');
        offscreen.slice(0, 3).forEach(function(item, idx) {
          var dir = item.direction ? item.direction.join(', ') : 'unknown';
          lines.push((idx + 1) + '. `' + item.selector + '` - positioned ' + dir);
          lines.push('   Fix: Check positioning or remove hidden element');
        });
        if (offscreen.length > 3) {
          lines.push('   _+ ' + (offscreen.length - 3) + ' more_');
        }
      }

      return lines.join('\n');
    }

    // Handle other audits with fixable array
    if (result.fixable && result.fixable.length > 0) {
      lines.push('');
      lines.push('**Issues to fix:**');

      result.fixable.slice(0, 5).forEach(function(issue, idx) {
        lines.push((idx + 1) + '. **' + issue.type + '** [' + (issue.severity || 'info').toUpperCase() + ']');
        if (issue.message) lines.push('   ' + issue.message);
        if (issue.fix) lines.push('   Fix: ' + issue.fix);
        if (issue.selector) lines.push('   Target: `' + issue.selector + '`');
      });

      if (result.fixable.length > 5) {
        lines.push('');
        lines.push('_+ ' + (result.fixable.length - 5) + ' more issues_');
      }

      return lines.join('\n');
    }

    // Fallback: use summary if available
    if (result.summary) {
      lines.push(result.summary);
    } else {
      lines.push('Audit complete. Score: ' + (score || 'N/A'));
    }

    return lines.join('\n');
  }

  // Attachment preview state
  var previewState = {
    popup: null,
    highlight: null
  };

  // Show attachment preview on hover
  function showAttachmentPreview(attachment, chipRect) {
    hideAttachmentPreview();

    var popup = document.createElement('div');
    popup.id = '__devtool-attachment-preview';
    popup.style.cssText = STYLES.attachmentPreview;

    if (attachment.type === 'screenshot') {
      // Shrunk-down thumbnail captured at screenshot time, if available.
      var thumb = attachment.data && attachment.data.thumbnail;
      if (thumb) {
        var img = document.createElement('img');
        img.src = thumb;
        img.style.cssText = STYLES.attachmentPreviewImage;
        popup.appendChild(img);
      }
      var info = document.createElement('div');
      info.style.cssText = STYLES.attachmentPreviewElement;
      var area = attachment.data && attachment.data.area;
      var dims = area ? (area.width + '\u00d7' + area.height) : '';
      if (attachment.filePath) {
        info.textContent = (dims ? dims + '\n' : '') + attachment.filePath;
      } else {
        info.textContent = (dims || 'Screenshot') + (thumb ? '' : '\nSaving\u2026');
      }
      popup.appendChild(info);
    } else if (attachment.type === 'element' && attachment.data) {
      // Element preview - show selector and highlight the element
      var info = document.createElement('div');
      info.style.cssText = STYLES.attachmentPreviewElement;
      info.innerHTML = '<strong>' + (attachment.data.tag || 'element') + '</strong>\n' +
        (attachment.data.selector || '') + '\n\n' +
        (attachment.data.text ? '"' + attachment.data.text.substring(0, 100) + '"' : '');
      popup.appendChild(info);

      // Highlight the element on the page
      if (attachment.data.selector) {
        try {
          var el = document.querySelector(attachment.data.selector);
          if (el) {
            var rect = el.getBoundingClientRect();
            var highlight = document.createElement('div');
            highlight.id = '__devtool-preview-highlight';
            highlight.style.cssText = STYLES.elementPreviewHighlight;
            highlight.style.left = rect.left + 'px';
            highlight.style.top = rect.top + 'px';
            highlight.style.width = rect.width + 'px';
            highlight.style.height = rect.height + 'px';
            // position:fixed; mounted into shadow root for style isolation.
            mountRoot().appendChild(highlight);
            previewState.highlight = highlight;
          }
        } catch (e) {
          // Invalid selector, ignore
        }
      }
    } else if (attachment.type === 'sketch' && attachment.data) {
      // Sketch preview - show thumbnail or info
      var sketchInfo = document.createElement('div');
      sketchInfo.style.cssText = STYLES.attachmentPreviewElement;
      var elements = attachment.data.elements || [];
      sketchInfo.innerHTML = '<strong>Sketch</strong>\n' +
        elements.length + ' element' + (elements.length !== 1 ? 's' : '') + '\n' +
        (attachment.summary || '');
      popup.appendChild(sketchInfo);
    } else if (attachment.type === 'style-edit' && attachment.data) {
      // Style-edit preview - show diff of original -> current values
      var styleInfo = document.createElement('div');
      styleInfo.style.cssText = STYLES.attachmentPreviewElement;
      var changes = attachment.data.changes || [];
      var lines = ['<strong>' + (attachment.data.selector || 'style-edit') + '</strong>'];
      for (var ci = 0; ci < changes.length; ci++) {
        var c = changes[ci];
        lines.push(c.property + ': ' + c.original + ' \u2192 ' + c.current);
      }
      styleInfo.innerHTML = lines.join('\n');
      popup.appendChild(styleInfo);
    } else if (attachment.type === 'audit' && attachment.data) {
      // Audit preview - show summary
      var auditInfo = document.createElement('div');
      auditInfo.style.cssText = STYLES.attachmentPreviewElement;
      auditInfo.innerHTML = '<strong>' + (attachment.data.auditType || 'Audit') + '</strong>\n' +
        (attachment.summary || '');
      popup.appendChild(auditInfo);
    } else {
      // Default: show summary
      var defaultInfo = document.createElement('div');
      defaultInfo.style.cssText = STYLES.attachmentPreviewElement;
      defaultInfo.textContent = attachment.summary || attachment.label;
      popup.appendChild(defaultInfo);
    }

    // Position the popup above the chip
    mountRoot().appendChild(popup);
    previewState.popup = popup;

    // Calculate position - show above chip, centered
    var popupRect = popup.getBoundingClientRect();
    var left = chipRect.left + (chipRect.width / 2) - (popupRect.width / 2);
    var top = chipRect.top - popupRect.height - 8;

    // Keep within viewport
    left = Math.max(8, Math.min(left, window.innerWidth - popupRect.width - 8));
    if (top < 8) {
      top = chipRect.bottom + 8; // Show below if not enough space above
    }

    popup.style.left = left + 'px';
    popup.style.top = top + 'px';

    // Fade in
    requestAnimationFrame(function() {
      popup.style.opacity = '1';
    });
  }

  // Hide attachment preview
  function hideAttachmentPreview() {
    if (previewState.popup) {
      previewState.popup.parentNode.removeChild(previewState.popup);
      previewState.popup = null;
    }
    if (previewState.highlight) {
      previewState.highlight.parentNode.removeChild(previewState.highlight);
      previewState.highlight = null;
    }
  }

  // Attachment chip creation
  function createChip(attachment) {
    var chip = document.createElement('div');
    chip.style.cssText = STYLES.chip;
    chip.dataset.id = attachment.id;
    chip.style.cursor = 'pointer';

    var icon = document.createElement('span');
    icon.style.cssText = STYLES.chipIcon;
    var iconSvg = ICONS.element;
    if (attachment.type === 'screenshot') iconSvg = ICONS.screenshot;
    else if (attachment.type === 'sketch') iconSvg = ICONS.sketch;
    else if (attachment.type === 'audit') iconSvg = ICONS.audit;
    else if (attachment.type === 'style-edit') iconSvg = ICONS.styleEdit;
    icon.innerHTML = iconSvg;
    chip.appendChild(icon);

    var label = document.createElement('span');
    label.style.cssText = STYLES.chipLabel;
    label.textContent = attachment.label;
    chip.appendChild(label);

    var removeBtn = document.createElement('button');
    removeBtn.style.cssText = STYLES.chipRemove;
    removeBtn.innerHTML = ICONS.x;
    removeBtn.setAttribute('aria-label', 'Remove ' + attachment.label);
    removeBtn.title = 'Remove ' + attachment.label;
    removeBtn.onclick = function(e) {
      e.stopPropagation();
      removeAttachment(attachment.id);
      hideAttachmentPreview();
    };
    removeBtn.onmouseenter = function() { removeBtn.style.color = TOKENS.colors.error; };
    removeBtn.onmouseleave = function() { removeBtn.style.color = TOKENS.colors.textMuted; };
    chip.appendChild(removeBtn);

    // Hover preview handlers
    chip.onmouseenter = function() {
      var rect = chip.getBoundingClientRect();
      showAttachmentPreview(attachment, rect);
    };
    chip.onmouseleave = function() {
      hideAttachmentPreview();
    };

    return chip;
  }

  // addAttachment - now uses reactive store (VanJS)
  // DOM updates automatically via AttachmentAreaComponent
  function addAttachment(type, data) {
    return actions.addAttachment(type, data);

    return attachment.id;
  }

  // removeAttachment - now uses reactive store (VanJS)
  // DOM updates automatically via AttachmentAreaComponent
  function removeAttachment(id) {
    hideAttachmentPreview();
    actions.removeAttachment(id);
  }

  // clearAttachments - now uses reactive store (VanJS)
  function clearAttachments() {
    hideAttachmentPreview();
    actions.clearAttachments();
  }

  // Send message - assembles everything into a structured message
  function handleSend() {
    var textarea = getInMount('__devtool-message');
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
          var desc = '- Screenshot `' + att.id + '`';
          if (att.filePath) {
            desc += ' (`' + att.filePath + '`)';
          }
          desc += ': ' + att.summary;
          parts.push(desc);
        } else if (att.type === 'element') {
          parts.push('- Element `' + att.id + '`: `' + att.data.selector + '` (' + att.data.tag + ')');
        } else if (att.type === 'sketch') {
          parts.push('- Sketch `' + att.id + '`: ' + att.summary);
        } else if (att.type === 'style-edit') {
          var seChanges = att.data && att.data.changes || [];
          var seSelector = att.data && att.data.selector || '';
          var seHeader = '- Style edit `' + seSelector + '`: ' + seChanges.length + ' CSS change' + (seChanges.length !== 1 ? 's' : '');
          if (att.data && att.data.reactProps && att.data.reactProps.component) {
            seHeader += ', React ' + att.data.reactProps.component + ' props attached';
          }
          parts.push(seHeader);
          for (var sci = 0; sci < seChanges.length; sci++) {
            var sc = seChanges[sci];
            parts.push('  ' + sc.property + ': ' + sc.original + ' \u2192 ' + sc.current + ' (' + sc.scope + ')');
          }
          if (att.data && att.data.screenshots) {
            var seBefore = att.data.screenshots.before;
            var seAfter = att.data.screenshots.after;
            if (seBefore || seAfter) {
              var ssIds = [];
              if (seBefore) ssIds.push('`' + seBefore + '`');
              if (seAfter) ssIds.push('`' + seAfter + '`');
              parts.push('  Before/after screenshots: ' + ssIds.join(', '));
            }
          }
        } else if (att.type === 'audit') {
          // Format audit result as actionable markdown summary
          parts.push('');
          parts.push(formatAuditForAgent(att.data.auditType, att.data.label, att.data.result));
        }
      });

      parts.push('');
      parts.push('*Use `proxy exec` to inspect or interact with the page.*');
    }

    var fullMessage = parts.join('\n');

    // Send via panel_message
    core.send('panel_message', {
      timestamp: Date.now(),
      payload: {
        message: fullMessage,
        attachments: state.attachments.map(function(a) {
          // For screenshots, omit binary area data — already sent via screenshot_capture event.
          // For other types (audit, element, sketch), include data payload used by the overlay.
          var area = a.data && a.data.area;
          var att = {
            id: a.id,
            type: a.type,
            selector: a.data && a.data.selector,
            tag: a.data && a.data.tag,
            text: a.data && a.data.text,
            summary: a.summary,
            filePath: a.filePath || null
          };
          if (a.type === 'screenshot') {
            att.area = area ? { x: area.x, y: area.y, width: area.width, height: area.height } : null;
          } else {
            att.area = area || null;
            att.data = a.data;
          }
          return att;
        }),
        url: window.location.href,
        request_notification: state.requestNotification
      }
    });

    // Clear
    if (textarea) textarea.value = '';
    store.message.val = '';
    clearAttachments();
    togglePanel(false);
  }

  // Capture screenshot area using html2canvas.
  // Calls back with an ArrayBuffer of raw PNG bytes (no base64 encoding).
  //
  // (x, y) are SHELL viewport coords. Under the always-wrap model the indicator
  // runs in the chrome shell, but the real page lives in the content iframe —
  // html2canvas(document.body) on the shell renders the (blank) chrome, not the
  // page. So we render the CONTENT frame's document instead. The content frame
  // is fullscreen (inset:0), so shell viewport coords map 1:1 onto it; we add
  // the content frame's scroll offset to convert viewport coords to the
  // document-space x/y html2canvas expects. Cross-origin / unwrapped pages fall
  // back to self via targetWindow().
  function captureArea(x, y, w, h, callback) {
    var pageWin = targetWindow();
    var pageDoc = (pageWin && pageWin.document) ? pageWin.document : document;
    var h2c = (pageWin && pageWin.html2canvas) || (typeof html2canvas !== 'undefined' ? html2canvas : undefined);
    if (typeof h2c === 'undefined') {
      // html2canvas is loaded on demand. The capture must run in the CONTENT
      // frame (where the real page lives), so ensure it there via that frame's
      // own loader, then re-enter. Fall back to this frame's loader if the
      // content frame has none reachable.
      var ensure = null;
      try {
        if (pageWin && typeof pageWin.__devtool_ensureHtml2canvas === 'function') {
          ensure = pageWin.__devtool_ensureHtml2canvas;
        }
      } catch (e) { /* cross-origin content frame — fall through */ }
      if (!ensure && typeof window.__devtool_ensureHtml2canvas === 'function') {
        ensure = window.__devtool_ensureHtml2canvas;
      }
      if (ensure) {
        ensure().then(function() {
          captureArea(x, y, w, h, callback);
        }, function() {
          console.error('[DevTool] html2canvas not loaded for screenshot capture');
          callback(null);
        });
      } else {
        console.error('[DevTool] html2canvas not loaded for screenshot capture');
        callback(null);
      }
      return;
    }

    var sx = (pageWin && pageWin.scrollX) || 0;
    var sy = (pageWin && pageWin.scrollY) || 0;

    h2c(pageDoc.body, {
      allowTaint: true,
      useCORS: true,
      logging: false,
      x: x + sx,
      y: y + sy,
      width: w,
      height: h,
      scrollX: 0,
      scrollY: 0,
      windowWidth: pageDoc.documentElement.scrollWidth,
      windowHeight: pageDoc.documentElement.scrollHeight
    }).then(function(canvas) {
      // Downscaled dataURL kept in-memory for the chip hover-preview thumbnail
      // (the raw PNG buffer is streamed out and dropped, so it can't be reused).
      var thumb = null;
      try { thumb = makeThumbnail(canvas, 300); } catch (e) { thumb = null; }
      canvas.toBlob(function(blob) {
        if (!blob) { callback(null, thumb); return; }
        blob.arrayBuffer().then(function(buf) {
          callback(buf, thumb);
        }).catch(function() { callback(null, thumb); });
      }, 'image/png');
    }).catch(function(err) {
      console.error('[DevTool] Screenshot capture failed:', err);
      callback(null);
    });
  }

  // Downscale a canvas to a max width and return a PNG dataURL for the hover
  // thumbnail. Height scales proportionally; small captures are not upscaled.
  function makeThumbnail(canvas, maxW) {
    var scale = (canvas.width > maxW) ? (maxW / canvas.width) : 1;
    var tw = Math.max(1, Math.round(canvas.width * scale));
    var th = Math.max(1, Math.round(canvas.height * scale));
    var tc = document.createElement('canvas');
    tc.width = tw;
    tc.height = th;
    tc.getContext('2d').drawImage(canvas, 0, 0, tw, th);
    return tc.toDataURL('image/png');
  }

  // Send raw PNG bytes as a binary WebSocket frame.
  // Frame format: [1 byte: idLen][idLen bytes: id][PNG bytes]
  function sendCaptureBinary(id, arrayBuffer) {
    var ws = core.ws && core.ws();
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    var idBytes = new TextEncoder().encode(id);
    var frame = new Uint8Array(1 + idBytes.length + arrayBuffer.byteLength);
    frame[0] = idBytes.length;
    frame.set(idBytes, 1);
    frame.set(new Uint8Array(arrayBuffer), 1 + idBytes.length);
    ws.send(frame.buffer);
  }

  // Screenshot mode
  function startScreenshotMode() {
    // Hide panel immediately without animation so overlay is the topmost interactive layer
    state.panel.style.display = 'none';
    state.isExpanded = false;
    if (state.tabUpdateInterval) {
      clearInterval(state.tabUpdateInterval);
      state.tabUpdateInterval = null;
    }

    // Detect responsive mode or touch device
    var isResponsive = window.innerWidth < 768; // Common tablet breakpoint
    var isTouchDevice = 'ontouchstart' in window || navigator.maxTouchPoints > 0;
    var isDragSelectUnavailable = isResponsive || isTouchDevice;

    // Hide indicator during capture to avoid including it in screenshots
    function hideIndicatorForCapture() {
      if (state.container) state.container.style.display = 'none';
    }
    function restoreIndicator() {
      if (state.container && state.isVisible) state.container.style.display = 'block';
    }

    // If drag select is not available, capture full screen immediately
    if (isDragSelectUnavailable) {
      var w = window.innerWidth;
      var h = window.innerHeight;
      hideIndicatorForCapture();
      captureArea(0, 0, w, h, function(imageBuffer) {
        restoreIndicator();
        addAttachment('screenshot', {
          label: 'Full screen (' + w + '\u00d7' + h + ')',
          summary: 'Full screen screenshot ' + w + 'x' + h,
          area: { x: 0, y: 0, width: w, height: h },
          imageBuffer: imageBuffer
        });
        togglePanel(true);
      });
      return;
    }

    // Desktop mode: show drag selection overlay
    var overlay = document.createElement('div');
    overlay.style.cssText = STYLES.overlay;

    var box = document.createElement('div');
    box.style.cssText = STYLES.selectionBox;
    box.style.display = 'none';
    overlay.appendChild(box);

    var instructions = document.createElement('div');
    instructions.style.cssText = STYLES.instructionBar;
    instructions.textContent = 'Click and drag to select area \u2022 ESC to cancel';
    overlay.appendChild(instructions);

    var start = null;

    // Block all events from reaching the page underneath (capture phase)
    // Uses stopPropagation (not stopImmediatePropagation) so sibling
    // handlers for mousedown/mousemove/mouseup on this overlay still fire.
    //
    // preventDefault is intentionally NOT called for pointer* events: per the
    // Pointer Events spec, preventDefault() on pointerdown suppresses the
    // compatibility mousedown/mousemove/mouseup events that the drag handlers
    // below depend on. We still preventDefault on mouse events / click /
    // contextmenu to suppress text selection and the right-click menu.
    function block(e) {
      e.stopPropagation();
      if (!e.type.startsWith('pointer')) {
        e.preventDefault();
      }
    }

    var blockTypes = ['mousedown', 'mouseup', 'mousemove', 'click', 'pointerdown', 'pointerup', 'pointermove', 'contextmenu'];
    blockTypes.forEach(function(type) {
      overlay.addEventListener(type, block, true);
    });

    overlay.addEventListener('mousedown', function(e) {
      start = { x: e.clientX, y: e.clientY };
      box.style.display = 'block';
      box.style.left = start.x + 'px';
      box.style.top = start.y + 'px';
      box.style.width = '0';
      box.style.height = '0';
    }, true);

    overlay.addEventListener('mousemove', function(e) {
      if (!start) return;
      var x = Math.min(start.x, e.clientX);
      var y = Math.min(start.y, e.clientY);
      var w = Math.abs(e.clientX - start.x);
      var h = Math.abs(e.clientY - start.y);
      box.style.left = x + 'px';
      box.style.top = y + 'px';
      box.style.width = w + 'px';
      box.style.height = h + 'px';
    }, true);

    overlay.addEventListener('mouseup', function(e) {
      if (!start) return;
      var x = Math.min(start.x, e.clientX);
      var y = Math.min(start.y, e.clientY);
      var w = Math.abs(e.clientX - start.x);
      var h = Math.abs(e.clientY - start.y);

      cleanup();

      if (w > 20 && h > 20) {
        // Hide indicator before capture to avoid including it in screenshot
        hideIndicatorForCapture();
        captureArea(x, y, w, h, function(imageBuffer, thumbnail) {
          restoreIndicator();
          addAttachment('screenshot', {
            label: w + '\u00d7' + h + ' area',
            summary: 'Screenshot area at (' + x + ',' + y + ') size ' + w + 'x' + h,
            area: { x: x, y: y, width: w, height: h },
            imageBuffer: imageBuffer,
            thumbnail: thumbnail
          });
          togglePanel(true);
        });
      } else {
        togglePanel(true);
      }
    }, true);

    // Prevent clicks on the instruction bar from starting a selection
    instructions.addEventListener('mousedown', function(e) { e.stopPropagation(); }, true);

    function cleanup() {
      if (window.__devtoolOverlayStack) window.__devtoolOverlayStack.pop('indicator-screenshot');
      document.removeEventListener('keydown', onKey);
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
    }

    function onKey(e) {
      if (e.key === 'Escape') {
        cleanup();
        togglePanel(true);
      }
    }
    // Escape via the shared overlay stack (top-most surface only); document
    // listener is the fallback when ui-tokens failed to load.
    if (window.__devtoolOverlayStack) {
      window.__devtoolOverlayStack.push('indicator-screenshot', function() {
        cleanup();
        togglePanel(true);
      });
    } else {
      document.addEventListener('keydown', onKey);
    }

    // Mount into shadow root (or body in fallback). position:fixed + full
    // viewport overlay works identically inside open shadow roots.
    mountRoot().appendChild(overlay);
  }

  // Element selection mode
  function startElementMode() {
    togglePanel(false);

    // The picker overlay lives in the shell (it must float above everything,
    // including the content iframe). But hit-testing must resolve REAL page
    // elements, not the iframe box. Under the always-wrap model the content
    // frame is fullscreen (inset:0), so shell viewport coords map 1:1 onto the
    // content frame — we read elementFromPoint / getComputedStyle from the
    // content window while drawing the highlight in the shell at the same
    // coords. pageWin falls back to self for the unwrapped/legacy page.
    var pageWin = targetWindow();
    var pageDoc = pageWin.document;
    // Realm-correct utils: generateSelector does `el instanceof HTMLElement`,
    // and a content-frame element is NOT an instance of the SHELL realm's
    // HTMLElement — so the shell copy would return ''. Use the content frame's
    // own utils (falls back to the shell copy for the unwrapped page).
    var pageUtils = pageWin.__devtool_utils || utils;

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
    // rAF-batch layout reads triggered by element-select mousemove. Each
    // mousemove only caches the latest pointer coordinates; the actual
    // elementFromPoint + getBoundingClientRect work runs once per animation
    // frame via processPendingMove. rafId is cancelled in cleanup().
    var rafId = 0;
    var pendingMove = null;

    function processPendingMove() {
      rafId = 0;
      var move = pendingMove;
      pendingMove = null;
      if (!move) return;

      overlay.style.pointerEvents = 'none';
      var el = pageDoc.elementFromPoint(move.x, move.y);
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

      var selector = pageUtils.generateSelector(el);
      tooltip.textContent = selector;
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(rect.left, window.innerWidth - 200) + 'px';
      tooltip.style.top = Math.max(rect.top - 28, 5) + 'px';
    }

    overlay.onmousemove = function(e) {
      pendingMove = { x: e.clientX, y: e.clientY };
      if (rafId) return; // coalesce events within one frame
      rafId = requestAnimationFrame(processPendingMove);
    };

    overlay.onclick = function(e) {
      e.preventDefault();
      e.stopPropagation();
      cleanup();

      if (hovered) {
        var selector = pageUtils.generateSelector(hovered);
        var tag = hovered.tagName.toLowerCase();
        var text = (hovered.textContent || '').trim().substring(0, 100);
        var computed = pageWin.getComputedStyle(hovered);

        var meta = {
          label: selector.length > 30 ? tag + (hovered.id ? '#' + hovered.id : '') : selector,
          summary: selector + ' - "' + text.substring(0, 50) + '"',
          selector: selector,
          tag: tag,
          id: hovered.id || null,
          classes: Array.from(hovered.classList),
          text: text,
          rect: hovered.getBoundingClientRect()
        };

        // Semantic attributes
        var attrs = ['role', 'aria-label', 'name', 'type', 'href', 'data-testid', 'data-test-id', 'placeholder', 'title', 'alt'];
        attrs.forEach(function(a) { var v = hovered.getAttribute(a); if (v) meta[a.replace(/-/g, '_')] = v; });

        // Framework detection
        var reactKey = Object.keys(hovered).find(function(k) { return k.startsWith('__reactFiber$') || k.startsWith('__reactInternalInstance$'); });
        if (reactKey) {
          var fiber = hovered[reactKey];
          if (fiber && fiber.type) {
            meta.framework = 'react';
            meta.component = typeof fiber.type === 'function' ? (fiber.type.displayName || fiber.type.name || 'Anonymous') : String(fiber.type);
          }
        }
        if (hovered.__vue__) { meta.framework = 'vue'; meta.component = hovered.__vue__.$options.name || 'Anonymous'; }
        if (hovered.__svelte_meta) { meta.framework = 'svelte'; }

        // Layout-relevant computed styles
        if (computed.display !== 'block' && computed.display !== 'inline') meta.display = computed.display;
        if (computed.position !== 'static') meta.position = computed.position;
        if (computed.overflow !== 'visible') meta.overflow = computed.overflow;

        // Event listeners (Chrome DevTools API only)
        if (typeof getEventListeners === 'function') {
          try { var ls = getEventListeners(hovered); var lt = Object.keys(ls); if (lt.length) meta.listeners = lt; } catch (e) {}
        }

        addAttachment('element', meta);
      }

      togglePanel(true);
    };

    function cleanup() {
      if (window.__devtoolOverlayStack) window.__devtoolOverlayStack.pop('indicator-element');
      document.removeEventListener('keydown', onKey);
      if (rafId) {
        cancelAnimationFrame(rafId);
        rafId = 0;
      }
      pendingMove = null;
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
    }

    function onKey(e) {
      if (e.key === 'Escape') {
        cleanup();
        togglePanel(true);
      }
    }
    // Escape via the shared overlay stack (top-most surface only); document
    // listener is the fallback when ui-tokens failed to load.
    if (window.__devtoolOverlayStack) {
      window.__devtoolOverlayStack.push('indicator-element', function() {
        cleanup();
        togglePanel(true);
      });
    } else {
      document.addEventListener('keydown', onKey);
    }

    // Mount overlay into shadow root. Hit-testing via document.elementFromPoint
    // still sees elements in the host page because open shadow roots are
    // hit-testable from document level, and the overlay toggles its own
    // pointer-events: none while reading elementFromPoint (see above).
    mountRoot().appendChild(overlay);
  }

  // Sketch mode - opens sketch, on save adds as attachment
  function openSketch() {
    togglePanel(false);
    // Run sketch in the content frame so its overlay draws over the real page,
    // not over the shell (where it would float above an opaque iframe). The
    // onSave closure stays a shell function — it captures addAttachment/
    // togglePanel here and runs them in the shell when the content frame fires
    // it (same-origin cross-frame call).
    var w = targetWindow();
    if (w.__devtool_sketch) {
      // Set callback for when sketch is saved
      w.__devtool_sketch.onSave = function(sketchData) {
        // Use reactive addAttachment - DOM updates automatically
        addAttachment('sketch', {
          label: sketchData.elementCount + ' elements',
          summary: 'Sketch with ' + sketchData.elementCount + ' elements',
          elements: sketchData.elements,
          elementCount: sketchData.elementCount
        });

        togglePanel(true);
      };

      w.__devtool_sketch.toggle();
    }
  }

  // Design mode - start design iteration for an element
  function startDesignMode() {
    togglePanel(false);
    var w = targetWindow();
    if (w.__devtool_design) {
      w.__devtool_design.start();
    } else {
      console.error('[Indicator] Design module not loaded');
      togglePanel(true);
    }
  }

  // Inspect mode - open style editor for hover-to-select. Runs in the content
  // frame so elementFromPoint resolves real page elements instead of the
  // content iframe (the always-wrap shell symptom).
  function startInspectMode() {
    var w = targetWindow();
    if (w.__devtool_style_editor) {
      w.__devtool_style_editor.open();
    } else {
      console.error('[Indicator] Style editor module not loaded');
    }
  }

  // Responsive mode - open the live-iframe responsive workbench
  function startResponsiveMode() {
    togglePanel(false);
    if (window.__devtool_responsive && window.__devtool_responsive.toggle) {
      window.__devtool_responsive.toggle();
    } else {
      console.error('[Indicator] Responsive mode module not loaded');
      togglePanel(true);
    }
  }

  // Panel toggle
  function togglePanel(show) {
    // Guard against a hotkey arriving before createUI() built the panel
    // (the handler is registered at script-eval time, ahead of DOM ready).
    if (!state.panel) return;
    var shouldShow = show !== undefined ? show : !state.isExpanded;
    state.isExpanded = shouldShow;

    if (shouldShow) {
      // A preview already visible before the panel opened must not linger
      // above it during the panel's enter transition.
      hideOutputPreview();
      // Register on the shared Escape stack so Esc closes the panel when it
      // is the top-most devtool surface.
      if (window.__devtoolOverlayStack) {
        window.__devtoolOverlayStack.push('indicator-panel', function() {
          togglePanel(false);
        });
      }
      // Focus management: remember where the user was, move focus into the
      // panel; restored on close below.
      state.prevFocus = document.activeElement;
      updatePanelPosition();
      state.panel.style.display = 'flex'; // Changed to flex for column layout
      // Promote the panel to a compositor layer just for the duration of the
      // 0.2s opacity+transform transition. Cleared below after the transition
      // completes so we don't pin GPU memory for an idle panel.
      state.panel.style.willChange = 'transform, opacity';
      // Re-render active tab now that panel is visible
      switchTab(state.activeTab);
      requestAnimationFrame(function() {
        state.panel.style.opacity = '1';
        state.panel.style.transform = 'translateY(0)';
      });
      // Release will-change after the enter transition settles (0.2s +
      // small safety margin). Stored on state so rapid toggles replace it.
      clearTimeout(state.panelWillChangeTimeout);
      state.panelWillChangeTimeout = setTimeout(function() {
        if (state.panel && state.isExpanded) {
          state.panel.style.willChange = 'auto';
        }
      }, 300);
      try { state.panel.focus({ preventScroll: true }); } catch (e) { try { state.panel.focus(); } catch (e2) {} }
    } else {
      if (window.__devtoolOverlayStack) {
        window.__devtoolOverlayStack.pop('indicator-panel');
      }
      // Restore focus to wherever the user was before the panel opened.
      if (state.prevFocus && typeof state.prevFocus.focus === 'function' && state.prevFocus.isConnected) {
        try { state.prevFocus.focus({ preventScroll: true }); } catch (e) { /* focus restore is best-effort */ }
      }
      state.prevFocus = null;
      state.panel.style.willChange = 'transform, opacity';
      state.panel.style.opacity = '0';
      state.panel.style.transform = 'translateY(8px)';
      clearTimeout(state.panelWillChangeTimeout);
      setTimeout(function() {
        state.panel.style.display = 'none';
        if (!state.isExpanded) {
          state.panel.style.willChange = 'auto';
        }
      }, 200);
      // Stop tab updates
      if (state.tabUpdateInterval) {
        clearInterval(state.tabUpdateInterval);
        state.tabUpdateInterval = null;
      }
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

    // rAF-batch drag updates. Each mousemove updates pendingDelta only; the
    // rAF callback applies the style write once per frame and then runs the
    // dependent layout reads (updatePanelPosition, positionSparkline).
    // Coalescing multiple mousemove events into one frame avoids synchronous
    // layout thrash from reading bounding rects after each style write.
    var rafId = 0;
    var pendingDelta = null;

    function applyPendingDrag() {
      rafId = 0;
      var d = pendingDelta;
      pendingDelta = null;
      if (!d) return;

      var x = startPos.x + d.dx;
      var y = startPos.y - d.dy;

      x = Math.max(0, Math.min(x, window.innerWidth - 52));
      y = Math.max(0, Math.min(y, window.innerHeight - 52));

      state.position = { x: x, y: y };
      state.bug.style.left = x + 'px';
      state.bug.style.bottom = y + 'px';
      updatePanelPosition();
      positionSparkline();
    }

    function onMove(e) {
      var dx = e.clientX - startX;
      var dy = e.clientY - startY;

      if (Math.abs(dx) > 5 || Math.abs(dy) > 5) dragged = true;

      if (dragged) {
        state.isDragging = true;
        pendingDelta = { dx: dx, dy: dy };
        if (rafId) return; // coalesce into pending frame
        rafId = requestAnimationFrame(applyPendingDrag);
      }
    }

    function onUp() {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);
      try { core.endDragShield(); } catch (e) { /* optional */ }
      if (rafId) {
        cancelAnimationFrame(rafId);
        rafId = 0;
      }
      // Flush any pending delta so the final drag position is applied
      // before we persist preferences.
      if (pendingDelta) {
        applyPendingDrag();
      }

      if (dragged) {
        savePrefs();
        setTimeout(function() { state.isDragging = false; }, 0);
      } else {
        togglePanel();
      }
    }

    try { core.beginDragShield(); } catch (e) { /* optional */ }
    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  }

  // Status polling and message handling
  function setupStatusPolling() {
    var updateDot = function() {
      var dot = getInMount('__devtool-status');
      var bug = state.bug;
      var connected = core.isConnected();

      if (dot) {
        dot.style.backgroundColor = connected ? TOKENS.colors.success : TOKENS.colors.error;
      }

      // Transition handling: desaturate the bug + blink the dot while the
      // metrics socket is down; fire a one-shot green shockwave + dot pop
      // when it comes back.
      if (connected !== lastConnected) {
        if (connected) {
          if (bug) {
            bug.classList.remove('__devtool-disconnected');
            bug.classList.add('__devtool-reconnect-flash');
            setTimeout(function() { bug.classList.remove('__devtool-reconnect-flash'); }, 850);
          }
          if (dot) {
            dot.classList.remove('__devtool-dot-lost');
            dot.classList.add('__devtool-dot-pop');
            setTimeout(function() { dot.classList.remove('__devtool-dot-pop'); }, 550);
          }
          // Only toast on a true reconnect, not the initial connect
          if (lastConnected === false) {
            showMicroToast('● Reconnected', TOKENS.colors.success);
            logHistoryEvent('system', 'Proxy reconnected', '');
          }
        } else {
          if (bug) bug.classList.add('__devtool-disconnected');
          if (dot) dot.classList.add('__devtool-dot-lost');
          if (lastConnected === true) {
            showMicroToast('● Connection lost', TOKENS.colors.error);
            logHistoryEvent('error', 'Proxy connection lost', '');
          }
        }
        lastConnected = connected;
      }
    };
    var lastConnected = null; // null = unknown until first update
    // Connection dot is purely event-driven via core.onConnected — the old
    // 200ms setInterval(updateDot) poll was redundant and leaked.
    core.onConnected(updateDot);
    core.onConnected(function() {
      // Sync chaos state on (re)connect so the bug indicator is correct
      // even if the panel was never opened.
      refreshChaosData();
    });
    // Inspect button active state is event-driven: style-editor.js calls
    // __devtool_indicator_syncInspect (directly, or via the frame adapter from
    // the content frame) on every open()/close() — replaces the old 1s
    // cross-frame isOpen() poll.
    window.__devtool_indicator_syncInspect = updateInspectBtnState;
    updateInspectBtnState();

    // Register message handler for activity state
    if (core && core.onMessage) {
      core.onMessage(handleMessage);
    }
  }

  // Re-render the inspect toolbar button from the style editor's current
  // open state. Exposed as window.__devtool_indicator_syncInspect.
  function updateInspectBtnState() {
    if (!state.inspectBtn) return;
    var sw = targetWindow();
    var active = false;
    try {
      active = !!(sw.__devtool_style_editor && sw.__devtool_style_editor.isOpen());
    } catch (e) { /* cross-origin content frame — treat as closed */ }
    if (active) {
      state.inspectBtn.style.background = TOKENS.colors.surface;
      state.inspectBtn.style.borderColor = TOKENS.colors.primary;
      state.inspectBtn.style.color = TOKENS.colors.primary;
    } else {
      state.inspectBtn.style.background = 'transparent';
      state.inspectBtn.style.borderColor = TOKENS.colors.border;
      state.inspectBtn.style.color = TOKENS.colors.textMuted;
    }
  }

  // Handle incoming WebSocket messages
  function handleMessage(message) {
    if (message.type === 'activity') {
      var payload = message.payload || message;
      var active = payload.active === true;
      setActivityState(active);
      if (active) {
        showMicroToast('\u26a1 Working...', TOKENS.colors.active);
        logHistoryEvent('system', 'Agent active', '');
      } else {
        showMicroToast('\u2713 Idle', TOKENS.colors.success);
        logHistoryEvent('system', 'Agent idle', '');
      }
    } else if (message.type === 'output_preview') {
      var payload = message.payload || message;
      var previewLines = Array.isArray(payload.lines) ? payload.lines : [];
      showOutputPreview(previewLines, typeof payload.throbber === 'string' ? payload.throbber : '');
    } else if (message.type === 'tool_event') {
      // Tool call state from the AI agent (hook dispatcher fan-out):
      // call = tool starting, done = finished OK, error = finished with error.
      // Routed through the coalescing queue so a burst of tool calls cannot
      // storm the DOM (see enqueueToolEvent).
      var payload = message.payload || message;
      enqueueToolEvent(payload.name || 'unknown', payload.action || 'call', payload.detail || '');
    } else if (message.type === 'execute') {
      showMicroToast('\u25b6 exec', TOKENS.colors.secondary);
      logHistoryEvent('tool', 'Proxy exec', (message.code || '').substring(0, 80));
    } else if (message.type === 'proxy_diagnostic') {
      var payload = message.payload || message;
      var level = payload.level || 'info';
      var diagMsg = payload.message || 'diagnostic';
      var color = level === 'error' ? TOKENS.colors.error : TOKENS.colors.active;
      showMicroToast((level === 'error' ? '\u2718 ' : '\u26a0 ') + diagMsg.substring(0, 30), color);
      logHistoryEvent(level === 'error' ? 'error' : 'system', diagMsg, '');
    } else if (message.type === 'chaos_response') {
      var cb = chaosPending[message.request_id];
      if (cb) {
        delete chaosPending[message.request_id];
        cb(message.result || null, message.error || null);
      }
    } else if (message.type === 'chaos_state') {
      // Pushed by the proxy on any chaos mutation (panel, MCP, or hub)
      applyChaosSnapshot(message.payload || message);
    } else if (message.type === 'capture_ack' && message.id && message.file_path) {
      // Server confirmed screenshot saved
      store.attachments.val = store.attachments.val.map(function(a) {
        if (a.id !== message.id) return a;
        return Object.assign({}, a, { filePath: message.file_path });
      });
      state.attachments = store.attachments.val;
      showMicroToast('\ud83d\udcf7 Screenshot saved', TOKENS.colors.success);
      logHistoryEvent('screenshot', 'Screenshot captured', message.file_path);
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
    if (state.sparklineInterval) {
      clearInterval(state.sparklineInterval);
      state.sparklineInterval = null;
    }
    if (state.tabUpdateInterval) {
      clearInterval(state.tabUpdateInterval);
      state.tabUpdateInterval = null;
    }
    if (state.container && state.container.parentNode) {
      state.container.parentNode.removeChild(state.container);
    }
    state.container = null;
    state.bug = null;
    state.panel = null;
    state.sparkline = null;
  }

  // Chrome-shell functions invoked from indicator-data.js / indicator-tabs.js
  // at runtime (after all indicator-* modules have evaluated).
  I.setChaosIndicator = setChaosIndicator;
  I.showMicroToast = showMicroToast;
  I.logHistoryEvent = logHistoryEvent;
  I.sendCaptureBinary = sendCaptureBinary;
  I.switchTab = switchTab;
  I.togglePanel = togglePanel;
  I.hideAttachmentPreview = hideAttachmentPreview;
  I.showAttachmentPreview = showAttachmentPreview;

  // Register the global hotkey handler immediately at script-eval time, on
  // window + capture. Injection lands before </head>, so this runs and
  // registers BEFORE any page inline script in <body>. A hostile page hook
  // (window-capture + stopImmediatePropagation, the original failure mode)
  // registers later, so ours fires first and cannot be suppressed. Deferring
  // this to init()/DOMContentLoaded would lose the registration-order race.
  //
  // When indicator-bridge.js (shared role set) is present it already
  // registered the equivalent handler at its own eval — that handler
  // dispatches to window.__devtool_indicator.togglePanel(), i.e. to this
  // module once the export below runs — so registering again here would
  // double-toggle on every Ctrl/Cmd+Y.
  if (!window.__devtool_indicator_bridge) {
    window.addEventListener('keydown', handleGlobalHotkey, true);
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
    switchTab: switchTab,
    setMessage: actions.setMessage,
    setActivityState: setActivityState,
    addAttachment: addAttachment,
    showMicroToast: showMicroToast,
    logHistoryEvent: logHistoryEvent,
    state: state
  };
})();

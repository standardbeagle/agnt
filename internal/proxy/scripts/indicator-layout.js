// Floating Indicator — panel layout.
//
// The developer panel is either a drawer docked to the left (the default) or
// right edge, or a detached floating window. This module owns everything about
// where the panel sits:
//
//   - geometry for the current mode (applyLayout)
//   - the header drag: pulling a docked drawer detaches it into a floating
//     window; dragging a floating window moves it; dropping it within
//     SNAP_PX of a side edge docks it there again
//   - the resize handle (drawer width, or floating width + height)
//   - the content-frame inset: in the chrome shell a docked, open drawer
//     pushes the app's iframe aside instead of covering it
//   - layout prefs (own localStorage key, separate from the bug prefs)
//
// Loads after indicator-data (needs state) and before indicator, which calls
// I.panelLayout at runtime. See moduleOrder in embed.go.

(function() {
  'use strict';

  var I = window.__devtool_indicator_internal;
  var state = I.state;
  var core = I.core;
  var frameContext = window.__devtool_context;

  var PREFS_KEY = '__devtool_panel_layout';
  var DOCK_DEFAULT_WIDTH = 420;
  var DOCK_MIN_WIDTH = 320;
  var DOCK_MAX_WIDTH = 760;
  var FLOAT_MIN_WIDTH = 320;
  var FLOAT_MIN_HEIGHT = 240;
  var EDGE_MARGIN = 8;
  // A docked drawer pushes the app only while this much app width remains;
  // on a narrower viewport it overlays the app instead.
  var MIN_APP_WIDTH = 320;
  // Header drag distance that turns a docked drawer into a floating window.
  var DETACH_PX = 8;
  // Pointer distance from a side edge at which a dragged window re-docks.
  var SNAP_PX = 32;
  var MODES = ['left', 'right', 'float'];
  var MODE_LABELS = { left: 'Dock left', right: 'Dock right', float: 'Float (detach)' };
  var MODE_ICONS = {
    left: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/></svg>',
    right: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M15 4v16"/></svg>',
    float: '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="7" width="13" height="13" rx="2"/><path d="M9 3h10a2 2 0 0 1 2 2v10"/></svg>'
  };

  var layout = {
    mode: 'left',
    dockWidth: DOCK_DEFAULT_WIDTH,
    float: null // { x, y, w, h } once the panel has been detached
  };

  var modeButtons = {};
  var snapHint = null;
  var resizeHandle = null;
  var windowResizeRaf = 0;

  function isDocked() {
    return layout.mode !== 'float';
  }

  function isFiniteNumber(n) {
    return typeof n === 'number' && isFinite(n);
  }

  function loadLayout() {
    var saved = null;
    try { saved = JSON.parse(localStorage.getItem(PREFS_KEY) || 'null'); } catch (e) { saved = null; }
    if (!saved) return;
    if (MODES.indexOf(saved.mode) >= 0) layout.mode = saved.mode;
    if (isFiniteNumber(saved.dockWidth)) layout.dockWidth = saved.dockWidth;
    var f = saved.float;
    if (f && isFiniteNumber(f.x) && isFiniteNumber(f.y) && isFiniteNumber(f.w) && isFiniteNumber(f.h)) {
      layout.float = { x: f.x, y: f.y, w: f.w, h: f.h };
    }
  }

  function saveLayout() {
    try { localStorage.setItem(PREFS_KEY, JSON.stringify(layout)); } catch (e) { /* storage blocked */ }
  }

  function clamp(n, lo, hi) {
    return Math.max(lo, Math.min(n, hi));
  }

  // A drawer leaves MIN_APP_WIDTH of app beside it where the viewport allows,
  // and on a viewport narrower than the minimum it simply fills the width.
  function clampDockWidth(w) {
    var vw = window.innerWidth;
    var lo = Math.min(DOCK_MIN_WIDTH, vw);
    var hi = Math.max(lo, Math.min(DOCK_MAX_WIDTH, vw - MIN_APP_WIDTH));
    return Math.round(clamp(w, lo, hi));
  }

  // A floating window stays fully inside the viewport, EDGE_MARGIN from each edge.
  function clampFloat(r) {
    var vw = window.innerWidth;
    var vh = window.innerHeight;
    var maxW = Math.max(160, vw - 2 * EDGE_MARGIN);
    var maxH = Math.max(160, vh - 2 * EDGE_MARGIN);
    var w = Math.round(clamp(r.w, Math.min(FLOAT_MIN_WIDTH, maxW), maxW));
    var h = Math.round(clamp(r.h, Math.min(FLOAT_MIN_HEIGHT, maxH), maxH));
    return {
      x: Math.round(clamp(r.x, EDGE_MARGIN, vw - w - EDGE_MARGIN)),
      y: Math.round(clamp(r.y, EDGE_MARGIN, vh - h - EDGE_MARGIN)),
      w: w,
      h: h
    };
  }

  function defaultFloat() {
    return { x: 24, y: 64, w: layout.dockWidth, h: Math.min(640, window.innerHeight - 128) };
  }

  // applyLayout writes the panel geometry for the current mode, then the
  // content-frame inset and bug visibility that follow from it. Idempotent;
  // called on open/close, mode change, drag, resize, and window resize.
  function applyLayout() {
    var panel = state.panel;
    if (!panel) return;
    var s = panel.style;
    panel.setAttribute('data-dock', layout.mode);
    if (isDocked()) {
      layout.dockWidth = clampDockWidth(layout.dockWidth);
      s.top = '0';
      s.bottom = '0';
      s.height = 'auto';
      s.width = layout.dockWidth + 'px';
      s.left = layout.mode === 'left' ? '0' : 'auto';
      s.right = layout.mode === 'right' ? '0' : 'auto';
    } else {
      layout.float = clampFloat(layout.float || defaultFloat());
      s.top = layout.float.y + 'px';
      s.left = layout.float.x + 'px';
      s.right = 'auto';
      s.bottom = 'auto';
      s.width = layout.float.w + 'px';
      s.height = layout.float.h + 'px';
    }
    MODES.forEach(function(m) {
      if (modeButtons[m]) modeButtons[m].setAttribute('aria-pressed', String(m === layout.mode));
    });
    applyContentInset();
    // The open drawer is the indicator while it covers the bug; hiding the
    // bug keeps it from sitting on top of the drawer's controls.
    var covered = bugUnderDrawer();
    if (state.bug) state.bug.style.visibility = covered ? 'hidden' : '';
    if (state.sparkline) state.sparkline.style.visibility = covered ? 'hidden' : '';
  }

  // BUG_SIZE matches STYLES.bug (52px square); state.position.x is its left.
  var BUG_SIZE = 52;
  function bugUnderDrawer() {
    if (!drawerOpen()) return false;
    var x = state.position.x;
    if (layout.mode === 'left') return x < layout.dockWidth;
    return x + BUG_SIZE > window.innerWidth - layout.dockWidth;
  }

  function drawerOpen() {
    return isDocked() && state.isExpanded && state.isVisible && !!state.panel;
  }

  // In the chrome shell a docked open drawer narrows the app's iframe so the
  // app reflows beside the drawer rather than under it (frames.js owns the
  // frame). Outside the shell, and on a viewport too narrow to leave
  // MIN_APP_WIDTH of app, the drawer overlays the page instead.
  function applyContentInset() {
    if (!frameContext || !frameContext.isChrome()) return;
    var push = drawerOpen() && window.innerWidth - layout.dockWidth >= MIN_APP_WIDTH;
    frameContext.insetContent(push ? layout.mode : null, layout.dockWidth);
  }

  // Transform for the panel's hidden state, so it slides in from its edge.
  function closedTransform() {
    if (layout.mode === 'left') return 'translateX(-16px)';
    if (layout.mode === 'right') return 'translateX(16px)';
    return 'translateY(8px)';
  }

  function setMode(mode) {
    if (MODES.indexOf(mode) < 0) return;
    if (mode === 'float' && !layout.float) layout.float = defaultFloat();
    layout.mode = mode;
    applyLayout();
    saveLayout();
  }

  // pointerDrag runs one drag gesture: onFrame receives the latest pointer
  // position once per animation frame, onEnd once on release. The drag shield
  // keeps the content iframe from swallowing pointer moves mid-drag.
  function pointerDrag(e, onFrame, onEnd) {
    e.preventDefault();
    var last = { x: e.clientX, y: e.clientY };
    var rafId = 0;
    function flush() {
      rafId = 0;
      onFrame(last.x, last.y);
    }
    function onMove(ev) {
      last = { x: ev.clientX, y: ev.clientY };
      if (!rafId) rafId = requestAnimationFrame(flush);
    }
    function onUp() {
      window.removeEventListener('pointermove', onMove, true);
      window.removeEventListener('pointerup', onUp, true);
      window.removeEventListener('pointercancel', onUp, true);
      try { core.endDragShield(); } catch (err) { /* shield is optional */ }
      if (rafId) {
        cancelAnimationFrame(rafId);
        flush();
      }
      onEnd();
    }
    try { core.beginDragShield(); } catch (err) { /* shield is optional */ }
    window.addEventListener('pointermove', onMove, true);
    window.addEventListener('pointerup', onUp, true);
    window.addEventListener('pointercancel', onUp, true);
  }

  function snapSideAt(x) {
    if (x <= SNAP_PX) return 'left';
    if (x >= window.innerWidth - SNAP_PX) return 'right';
    return null;
  }

  function showSnapHint(side) {
    if (!side) {
      if (snapHint) snapHint.style.display = 'none';
      return;
    }
    if (!snapHint) {
      snapHint = document.createElement('div');
      snapHint.className = '__devtool-snap-hint';
      snapHint.setAttribute('aria-hidden', 'true');
      // Same layer as the panel, earlier in the DOM: drawn beneath it.
      state.container.insertBefore(snapHint, state.panel);
    }
    snapHint.style.width = layout.dockWidth + 'px';
    snapHint.style.left = side === 'left' ? '0' : 'auto';
    snapHint.style.right = side === 'right' ? '0' : 'auto';
    snapHint.style.display = 'block';
  }

  function startHeaderDrag(e) {
    if (e.button !== 0) return;
    if (e.target.closest && e.target.closest('button, a, input, select, textarea')) return;
    var startX = e.clientX;
    var startY = e.clientY;
    var rect = state.panel.getBoundingClientRect();
    var grabX = startX - rect.left;
    var grabY = startY - rect.top;
    var moved = false;
    var snapSide = null;

    pointerDrag(e, function(x, y) {
      if (!moved) {
        if (Math.abs(x - startX) < DETACH_PX && Math.abs(y - startY) < DETACH_PX) return;
        moved = true;
        if (isDocked()) {
          // Detach: the window keeps the drawer's width and stays under the
          // pointer at the same horizontal grab offset.
          var h = (layout.float && layout.float.h) || defaultFloat().h;
          layout.float = { x: 0, y: 0, w: layout.dockWidth, h: h };
          grabX = Math.min(grabX, layout.dockWidth - 48);
          layout.mode = 'float';
        }
      }
      layout.float.x = x - grabX;
      layout.float.y = y - grabY;
      snapSide = snapSideAt(x);
      showSnapHint(snapSide);
      applyLayout();
    }, function() {
      showSnapHint(null);
      if (!moved) return;
      if (snapSide) {
        setMode(snapSide);
      } else {
        saveLayout();
      }
    });
  }

  function startResize(e) {
    if (e.button !== 0) return;
    e.stopPropagation();
    var startX = e.clientX;
    var startY = e.clientY;
    var startW = isDocked() ? layout.dockWidth : layout.float.w;
    var startH = isDocked() ? 0 : layout.float.h;
    pointerDrag(e, function(x, y) {
      if (layout.mode === 'left') layout.dockWidth = startW + (x - startX);
      else if (layout.mode === 'right') layout.dockWidth = startW - (x - startX);
      else {
        layout.float.w = startW + (x - startX);
        layout.float.h = startH + (y - startY);
      }
      applyLayout();
    }, saveLayout);
  }

  // createModeSwitch returns the dock-left / dock-right / float button group
  // for the panel header.
  function createModeSwitch() {
    var group = document.createElement('div');
    group.className = '__devtool-mode-switch';
    group.setAttribute('role', 'group');
    group.setAttribute('aria-label', 'Panel position');
    MODES.forEach(function(m) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = '__devtool-icon-btn';
      btn.innerHTML = MODE_ICONS[m];
      btn.title = MODE_LABELS[m];
      btn.setAttribute('aria-label', MODE_LABELS[m]);
      btn.setAttribute('aria-pressed', String(m === layout.mode));
      btn.onclick = function(ev) { ev.stopPropagation(); setMode(m); };
      modeButtons[m] = btn;
      group.appendChild(btn);
    });
    return group;
  }

  function onWindowResize() {
    if (windowResizeRaf) return;
    windowResizeRaf = requestAnimationFrame(function() {
      windowResizeRaf = 0;
      if (state.panel && state.isExpanded) applyLayout();
    });
  }

  // attachPanelLayout wires the header drag, the resize handle, and the
  // viewport-resize clamp onto a freshly built panel.
  function attachPanelLayout(panel, header) {
    header.addEventListener('pointerdown', startHeaderDrag);
    resizeHandle = document.createElement('div');
    resizeHandle.className = '__devtool-panel-resize';
    resizeHandle.title = 'Drag to resize';
    resizeHandle.setAttribute('aria-hidden', 'true');
    resizeHandle.addEventListener('pointerdown', startResize);
    panel.appendChild(resizeHandle);
    window.addEventListener('resize', onWindowResize);
    applyLayout();
  }

  // detachPanelLayout undoes attachPanelLayout's global effects: the window
  // listener and the content-frame inset (destroy must not leave the app
  // squeezed beside a drawer that no longer exists).
  function detachPanelLayout() {
    window.removeEventListener('resize', onWindowResize);
    if (windowResizeRaf) cancelAnimationFrame(windowResizeRaf);
    windowResizeRaf = 0;
    if (frameContext && frameContext.isChrome()) frameContext.insetContent(null);
    modeButtons = {};
    snapHint = null;
    resizeHandle = null;
  }

  I.panelLayout = {
    load: loadLayout,
    attach: attachPanelLayout,
    detach: detachPanelLayout,
    apply: applyLayout,
    setMode: setMode,
    closedTransform: closedTransform,
    createModeSwitch: createModeSwitch,
    // Read-only view for the public export and tests.
    current: function() {
      return {
        mode: layout.mode,
        dockWidth: layout.dockWidth,
        float: layout.float ? { x: layout.float.x, y: layout.float.y, w: layout.float.w, h: layout.float.h } : null
      };
    }
  };
})();

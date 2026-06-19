// Responsive mode for DevTool — interactive responsive workbench.
//
// A 4th indicator mode (beside sketch/design). Opens a panel hosting a live
// iframe of the current page at a controllable width. A1 scaffolded the panel +
// iframe + open/close/toggle. A2 adds the width controls (slider, numeric input,
// preset chips, edge drag handle) all routed through a single applyWidth() so
// human-driven and agent-driven (setWidth) changes share one source of truth.
// Layout-shift detection (A3), shift overlays (A4), channel handoff (A5), and
// auto-sweep (A6) layer on top in later slices.
//
// It AUGMENTS the existing window.__devtool_responsive object created by
// responsive.js (the headless audit module) rather than replacing it, so the
// audit API (.audit, .detect*, DEFAULT_VIEWPORTS) stays intact. Load order in
// embed.go places this module after responsive.js for that reason.

(function() {
  'use strict';

  var MIN_WIDTH = 320;
  var MAX_WIDTH = 1920;
  var DEFAULT_WIDTH = 768;
  var SHIFT_DEBOUNCE = 250; // ms to settle after a width change before re-detecting
  var PANEL_ID = '__devtool_responsive_panel';
  var FRAME_ID = '__devtool_responsive_frame';

  // responsiveContentSrc builds the URL for the device-preview iframe: the
  // current page with the content-frame marker appended, so the proxy serves it
  // UNWRAPPED (not as another chrome shell) and frames.js registers it as a
  // distinct content frame. A unique id per open keeps it addressable.
  function responsiveContentSrc() {
    var param = (window.__devtool && window.__devtool.FRAME_PARAM) || '__devtool_frame';
    var rid = 'resp-' + Date.now();
    try {
      var u = new URL(window.location.href);
      u.searchParams.set(param, rid);
      return u.toString();
    } catch (e) {
      var base = window.location.href.split('#')[0];
      return base + (base.indexOf('?') >= 0 ? '&' : '?') + param + '=' + rid;
    }
  }

  var PRESETS = [
    { name: 'Mobile', width: 375 },
    { name: 'Tablet', width: 768 },
    { name: 'Desktop', width: 1440 }
  ];

  var state = {
    open: false,
    width: DEFAULT_WIDTH,
    panel: null,
    iframe: null,
    widthLabel: null,
    slider: null,
    numInput: null,
    frameWrap: null,
    overlayLayer: null,
    resultsPane: null,
    sweepBtn: null,
    sweeping: false,
    dragging: false,
    shifts: [],          // layout-shift findings at the current width (A3)
    prevShiftIds: {},     // finding ids seen at the previous width (for isNew marking)
    shiftTimer: null
  };

  function getCore() { return window.__devtool_core; }

  // sendRequest hands off the current width + layout-shift findings to the agent
  // via the proxy WS pipeline (flows into the traffic log + channel/overlay path,
  // mirroring design_request). Guarded so a missing core never throws.
  function sendRequest() {
    var core = getCore();
    if (!core || !core.send) {
      console.error('[Responsive] Core not available');
      return;
    }
    var st = getState();
    core.send('responsive_request', {
      width: st.width,
      shifts: st.shifts,
      selectors: st.selectors
    });
  }

  // emitState publishes the lightweight panel state (width + shift count). Sent
  // when the panel opens and after each captureShifts settle, mirroring
  // design_state. Guarded so a missing core never throws.
  function emitState() {
    var core = getCore();
    if (!core || !core.send) { return; }
    core.send('responsive_state', {
      width: state.width,
      shiftCount: state.shifts.length
    });
  }

  // Mount into the shared shadow root when available so host-page CSS cannot
  // bleed into the panel; fall back to document.body otherwise.
  function getMountRoot() {
    if (typeof window.__devtoolGetMountRoot === 'function') {
      try {
        var root = window.__devtoolGetMountRoot();
        if (root) { return root; }
      } catch (e) { /* fall through */ }
    }
    return document.body;
  }

  function clampWidth(w) {
    w = parseInt(w, 10);
    if (isNaN(w)) { w = DEFAULT_WIDTH; }
    if (w < MIN_WIDTH) { w = MIN_WIDTH; }
    if (w > MAX_WIDTH) { w = MAX_WIDTH; }
    return w;
  }

  function makeChip(label, width) {
    var chip = document.createElement('button');
    chip.textContent = label;
    chip.title = label + ' (' + width + 'px)';
    chip.style.cssText = [
      'background: #2c313a',
      'border: 1px solid #3a3f47',
      'color: #cfd3da',
      'border-radius: 4px',
      'padding: 3px 8px',
      'font-size: 11px',
      'cursor: pointer'
    ].join('; ');
    chip.onclick = function() { applyWidth(width); };
    return chip;
  }

  function buildControls() {
    var bar = document.createElement('div');
    bar.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 8px',
      'padding: 8px 14px',
      'background: #20242b',
      'border-top: 1px solid #2c313a',
      'flex: 0 0 auto'
    ].join('; ');

    var slider = document.createElement('input');
    slider.type = 'range';
    slider.min = String(MIN_WIDTH);
    slider.max = String(MAX_WIDTH);
    slider.value = String(state.width);
    slider.style.cssText = 'flex: 1 1 auto; cursor: pointer;';
    // 'input' fires continuously during drag — live resize.
    slider.addEventListener('input', function() { applyWidth(slider.value); });

    var numInput = document.createElement('input');
    numInput.type = 'number';
    numInput.min = String(MIN_WIDTH);
    numInput.max = String(MAX_WIDTH);
    numInput.value = String(state.width);
    numInput.style.cssText = [
      'width: 64px',
      'background: #15181d',
      'border: 1px solid #3a3f47',
      'color: #e6e8ec',
      'border-radius: 4px',
      'padding: 3px 6px',
      'font-size: 12px'
    ].join('; ');
    numInput.addEventListener('change', function() { applyWidth(numInput.value); });

    state.slider = slider;
    state.numInput = numInput;

    bar.appendChild(slider);
    bar.appendChild(numInput);
    for (var i = 0; i < PRESETS.length; i++) {
      bar.appendChild(makeChip(PRESETS[i].name, PRESETS[i].width));
    }

    var sweepBtn = document.createElement('button');
    sweepBtn.textContent = 'Auto-sweep';
    sweepBtn.title = 'Run the headless multi-viewport audit and list all findings';
    sweepBtn.style.cssText = [
      'background: #2f6f4f',
      'border: 1px solid #3c8a63',
      'color: #eafff3',
      'border-radius: 4px',
      'padding: 3px 10px',
      'font-size: 11px',
      'cursor: pointer'
    ].join('; ');
    sweepBtn.onclick = runAutoSweep;
    state.sweepBtn = sweepBtn;
    bar.appendChild(sweepBtn);

    return bar;
  }

  // Drag handle on the iframe's right edge. The frame is left-aligned in the
  // host so the new width is simply pointerX - frameLeft.
  function buildDragHandle() {
    var handle = document.createElement('div');
    handle.title = 'Drag to resize width';
    handle.style.cssText = [
      'width: 8px',
      'flex: 0 0 auto',
      'cursor: ew-resize',
      'background: #3a3f47',
      'border-radius: 2px',
      'align-self: stretch'
    ].join('; ');

    handle.addEventListener('mousedown', function(e) {
      e.preventDefault();
      state.dragging = true;
      document.addEventListener('mousemove', onDragMove, true);
      document.addEventListener('mouseup', onDragEnd, true);
    });
    return handle;
  }

  function onDragMove(e) {
    if (!state.dragging || !state.iframe) { return; }
    // Frame is centered, so the handle drag is center-anchored: each edge moves
    // symmetrically. Width = twice the pointer's distance from the frame center.
    var rect = state.iframe.getBoundingClientRect();
    var centerX = rect.left + rect.width / 2;
    applyWidth((e.clientX - centerX) * 2);
  }

  function onDragEnd() {
    state.dragging = false;
    document.removeEventListener('mousemove', onDragMove, true);
    document.removeEventListener('mouseup', onDragEnd, true);
  }

  function buildPanel() {
    var panel = document.createElement('div');
    panel.id = PANEL_ID;
    // Full-viewport opaque shell: covers the real page entirely so the framed
    // viewport reads as the site shrinking in place, not a panel layered over a
    // still-visible full-width copy.
    panel.style.cssText = [
      'position: fixed',
      'top: 0',
      'right: 0',
      'bottom: 0',
      'left: 0',
      'width: 100%',
      'background: #1b1d22',
      'z-index: 2147483600',
      'display: flex',
      'flex-direction: column',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif'
    ].join('; ');

    // Header: title + live width readout + close.
    var header = document.createElement('div');
    header.style.cssText = [
      'display: flex',
      'align-items: center',
      'gap: 12px',
      'padding: 10px 14px',
      'background: #24272e',
      'color: #e6e8ec',
      'font-size: 13px',
      'flex: 0 0 auto'
    ].join('; ');

    var title = document.createElement('span');
    title.textContent = 'Responsive';
    title.style.cssText = 'font-weight: 600;';

    var widthLabel = document.createElement('span');
    widthLabel.textContent = state.width + 'px';
    widthLabel.style.cssText = 'margin-left: auto; color: #9aa0aa; font-variant-numeric: tabular-nums;';

    var sendBtn = document.createElement('button');
    sendBtn.textContent = 'Send to agent';
    sendBtn.title = 'Hand off current width + layout shifts to the agent';
    sendBtn.style.cssText = [
      'background: #2c6cf0',
      'border: 1px solid #2c6cf0',
      'color: #ffffff',
      'border-radius: 4px',
      'padding: 4px 10px',
      'font-size: 12px',
      'cursor: pointer'
    ].join('; ');
    sendBtn.onclick = function() { sendRequest(); };

    var closeBtn = document.createElement('button');
    closeBtn.textContent = '✕';
    closeBtn.title = 'Close responsive mode (Esc)';
    closeBtn.style.cssText = [
      'background: transparent',
      'border: none',
      'color: #9aa0aa',
      'cursor: pointer',
      'font-size: 15px',
      'line-height: 1',
      'padding: 2px 4px'
    ].join('; ');
    closeBtn.onclick = function() { close(); };

    header.appendChild(title);
    header.appendChild(widthLabel);
    header.appendChild(sendBtn);
    header.appendChild(closeBtn);

    var controls = buildControls();

    // Frame host left-aligns the iframe + drag handle so the controllable
    // width reads visually and the edge drag maps directly to pointer x.
    var host = document.createElement('div');
    host.style.cssText = [
      'flex: 1 1 auto',
      'overflow: auto',
      'background: #0f1115',
      'display: flex',
      'justify-content: center',
      'align-items: stretch',
      'padding: 16px',
      'gap: 0'
    ].join('; ');

    // frameWrap holds the iframe + the shift-flag overlay layer at the same
    // box, so element rects from inside the iframe map directly to overlay
    // coordinates.
    var frameWrap = document.createElement('div');
    frameWrap.style.cssText = [
      'position: relative',
      'width: ' + state.width + 'px',
      'height: 100%',
      'flex: 0 0 auto'
    ].join('; ');

    var iframe = document.createElement('iframe');
    iframe.id = FRAME_ID;
    // Always-wrap model (docs/responsive-canonical-target.md §4/§7): the proxy
    // wraps a top-level navigation in a chrome shell, so loading
    // window.location.href verbatim would load ANOTHER shell (shell-in-shell).
    // Carry the content-frame marker so the proxy serves the page UNWRAPPED
    // here; the preview then registers as its own content frame (frames.js) and
    // becomes the active interaction target while responsive mode is open.
    iframe.src = responsiveContentSrc();
    iframe.style.cssText = [
      'width: 100%',
      'height: 100%',
      'border: 1px solid #3a3f47',
      'background: #ffffff',
      'display: block'
    ].join('; ');
    // Detect shifts once the framed page settles, and again after any in-frame
    // navigation reloads it.
    iframe.addEventListener('load', scheduleCapture);

    var overlayLayer = document.createElement('div');
    overlayLayer.style.cssText = [
      'position: absolute',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'pointer-events: none',
      'overflow: hidden',
      'z-index: 1'
    ].join('; ');

    frameWrap.appendChild(iframe);
    frameWrap.appendChild(overlayLayer);

    var handle = buildDragHandle();

    host.appendChild(frameWrap);
    host.appendChild(handle);

    // Auto-sweep results drawer: hidden until a sweep runs. Sweep findings span
    // multiple viewports, so they list here rather than overlay the single live
    // frame.
    var resultsPane = document.createElement('div');
    resultsPane.style.cssText = [
      'display: none',
      'flex: 0 0 auto',
      'max-height: 38%',
      'overflow: auto',
      'background: #15181d',
      'border-top: 1px solid #2c313a',
      'padding: 8px 14px',
      'color: #cfd3da',
      'font: 11px/1.5 -apple-system, sans-serif'
    ].join('; ');

    panel.appendChild(header);
    panel.appendChild(controls);
    panel.appendChild(host);
    panel.appendChild(resultsPane);

    state.widthLabel = widthLabel;
    state.iframe = iframe;
    state.frameWrap = frameWrap;
    state.overlayLayer = overlayLayer;
    state.resultsPane = resultsPane;

    // Escape closes while the panel is focused/hovered.
    panel.addEventListener('keydown', function(e) {
      if (e.key === 'Escape') { close(); }
    });

    return panel;
  }

  // captureShifts runs the layout/overflow detectors (reused from responsive.js,
  // which exposes them on window.__devtool_responsive) against the panel iframe's
  // contentWindow at the current width. Findings present at the current width but
  // not at the previous one are flagged isNew, so dialing the slider surfaces
  // exactly the widths where a break appears. Returns the finding list; results
  // are also stored on state for getState() and the A4 overlay.
  function captureShifts() {
    state.shiftTimer = null;
    var ns = window.__devtool_responsive;
    if (!state.iframe || !ns || typeof ns.detectLayoutIssues !== 'function') {
      return state.shifts;
    }
    var win;
    try {
      win = state.iframe.contentWindow;
      // Touch the document to surface a cross-origin access throw early.
      if (!win || !win.document) { return state.shifts; }
    } catch (e) {
      return state.shifts; // iframe not same-origin / not ready
    }

    var findings = [];
    try {
      findings = findings.concat(ns.detectLayoutIssues(win, state.width) || []);
      if (typeof ns.detectOverflowIssues === 'function') {
        findings = findings.concat(ns.detectOverflowIssues(win, state.width) || []);
      }
    } catch (e) {
      return state.shifts; // detector threw (e.g. mid-navigation); keep last good
    }

    var nextIds = {};
    for (var i = 0; i < findings.length; i++) {
      var f = findings[i];
      f.isNew = !state.prevShiftIds[f.id];
      f.width = state.width;
      nextIds[f.id] = true;
    }
    // Rank: critical first, then new-at-this-width, then by selector for stability.
    findings.sort(function(a, b) {
      var sevRank = { critical: 0, warning: 1, info: 2 };
      var sa = sevRank[a.severity] != null ? sevRank[a.severity] : 3;
      var sb = sevRank[b.severity] != null ? sevRank[b.severity] : 3;
      if (sa !== sb) { return sa - sb; }
      if (a.isNew !== b.isNew) { return a.isNew ? -1 : 1; }
      return (a.selector || '').localeCompare(b.selector || '');
    });

    state.prevShiftIds = nextIds;
    state.shifts = findings;
    renderOverlays();
    emitState();
    return findings;
  }

  var SEVERITY_COLOR = {
    critical: '#ff5c5c',
    warning: '#ffb454',
    info: '#5ca8ff'
  };

  // renderOverlays draws a labeled box over each flagged element. Element rects
  // come from inside the iframe; the overlay layer shares the iframe's box, so
  // viewport-relative rects map straight across. Resolution failures (selector
  // no longer matches at this width) are skipped silently — the finding still
  // shows in getState().
  function renderOverlays() {
    if (!state.overlayLayer) { return; }
    state.overlayLayer.innerHTML = '';
    var doc;
    try {
      doc = state.iframe && state.iframe.contentDocument;
    } catch (e) { return; }
    if (!doc) { return; }

    for (var i = 0; i < state.shifts.length; i++) {
      var f = state.shifts[i];
      if (!f.selector) { continue; }
      var el;
      try {
        el = doc.querySelector(f.selector);
      } catch (e) { continue; }
      if (!el) { continue; }
      var r = el.getBoundingClientRect();
      if (r.width <= 0 && r.height <= 0) { continue; }
      var color = SEVERITY_COLOR[f.severity] || SEVERITY_COLOR.info;

      var box = document.createElement('div');
      box.style.cssText = [
        'position: absolute',
        'left: ' + Math.max(0, r.left) + 'px',
        'top: ' + Math.max(0, r.top) + 'px',
        'width: ' + Math.max(0, r.width) + 'px',
        'height: ' + Math.max(0, r.height) + 'px',
        'border: 2px solid ' + color,
        'background: ' + color + '22',
        'box-sizing: border-box'
      ].join('; ');

      var tag = document.createElement('div');
      tag.textContent = f.type + (f.isNew ? ' *' : '');
      tag.title = f.message || '';
      tag.style.cssText = [
        'position: absolute',
        'top: -16px',
        'left: 0',
        'background: ' + color,
        'color: #15181d',
        'font: 10px/14px -apple-system, sans-serif',
        'padding: 0 4px',
        'border-radius: 2px',
        'white-space: nowrap'
      ].join('; ');
      box.appendChild(tag);
      state.overlayLayer.appendChild(box);
    }
  }

  // runAutoSweep runs the existing headless multi-viewport audit (responsive.js
  // ns.audit) and lists every finding in the results drawer. No new detection
  // logic — it bridges the automated sweep into the manual panel.
  function runAutoSweep() {
    var ns = window.__devtool_responsive;
    if (state.sweeping || !ns || typeof ns.audit !== 'function') {
      if (!ns || typeof ns.audit !== 'function') {
        renderSweepError('Responsive audit module not loaded');
      }
      return;
    }
    state.sweeping = true;
    if (state.sweepBtn) {
      state.sweepBtn.disabled = true;
      state.sweepBtn.textContent = 'Sweeping…';
    }
    Promise.resolve(ns.audit({ raw: true })).then(function(result) {
      renderSweepResults(result);
    }).catch(function(err) {
      renderSweepError((err && err.message) || 'Sweep failed');
    }).then(function() {
      state.sweeping = false;
      if (state.sweepBtn) {
        state.sweepBtn.disabled = false;
        state.sweepBtn.textContent = 'Auto-sweep';
      }
    });
  }

  function renderSweepError(msg) {
    if (!state.resultsPane) { return; }
    state.resultsPane.textContent = '⚠ ' + msg;
    state.resultsPane.style.display = 'block';
  }

  var SWEEP_SEVERITY_COLOR = {
    critical: '#ff5c5c',
    warning: '#ffb454',
    info: '#5ca8ff'
  };

  function renderSweepResults(result) {
    if (!state.resultsPane) { return; }
    state.resultsPane.innerHTML = '';
    state.resultsPane.style.display = 'block';

    var viewports = (result && result.viewports) || {};
    var names = Object.keys(viewports);
    if (!names.length) {
      state.resultsPane.textContent = 'No viewport results.';
      return;
    }

    for (var i = 0; i < names.length; i++) {
      var vp = viewports[names[i]];
      var issues = (vp && vp.issues) || [];
      var head = document.createElement('div');
      head.textContent = names[i] + ' (' + (vp.width || '?') + 'px) — ' + issues.length + ' issue(s)';
      head.style.cssText = 'font-weight: 600; color: #e6e8ec; margin: 6px 0 2px;';
      state.resultsPane.appendChild(head);

      for (var j = 0; j < issues.length; j++) {
        var it = issues[j];
        var row = document.createElement('div');
        var color = SWEEP_SEVERITY_COLOR[it.severity] || SWEEP_SEVERITY_COLOR.info;
        row.style.cssText = 'padding: 1px 0 1px 10px;';
        row.innerHTML = '<span style="color:' + color + '">●</span> [' +
          (it.type || '?') + '] ' +
          escapeHtml(it.selector || '') + ' — ' + escapeHtml(it.message || '');
        state.resultsPane.appendChild(row);
      }
    }
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;');
  }

  // Debounce re-detection so dragging the slider doesn't run a full DOM sweep on
  // every input event — only after the width settles.
  function scheduleCapture() {
    if (!state.open) { return; }
    if (state.shiftTimer) { clearTimeout(state.shiftTimer); }
    state.shiftTimer = setTimeout(captureShifts, SHIFT_DEBOUNCE);
  }

  // applyWidth is the single source of truth: every control (slider, numeric
  // input, preset chip, edge drag) and the agent's setWidth() funnel through
  // here, so all surfaces stay in sync regardless of who drove the change.
  function applyWidth(w) {
    state.width = clampWidth(w);
    if (state.frameWrap) { state.frameWrap.style.width = state.width + 'px'; }
    if (state.widthLabel) { state.widthLabel.textContent = state.width + 'px'; }
    if (state.slider && state.slider.value !== String(state.width)) {
      state.slider.value = String(state.width);
    }
    if (state.numInput && state.numInput.value !== String(state.width)) {
      state.numInput.value = String(state.width);
    }
    scheduleCapture();
    return state.width;
  }

  function open() {
    if (state.open) { return getState(); }
    state.panel = buildPanel();
    getMountRoot().appendChild(state.panel);
    state.open = true;
    emitState();
    return getState();
  }

  function close() {
    onDragEnd();
    if (state.shiftTimer) { clearTimeout(state.shiftTimer); state.shiftTimer = null; }
    if (state.panel && state.panel.parentNode) {
      state.panel.parentNode.removeChild(state.panel);
    }
    state.panel = null;
    state.iframe = null;
    state.widthLabel = null;
    state.slider = null;
    state.numInput = null;
    state.frameWrap = null;
    state.overlayLayer = null;
    state.resultsPane = null;
    state.sweepBtn = null;
    state.sweeping = false;
    state.shifts = [];
    state.prevShiftIds = {};
    state.open = false;
    return getState();
  }

  function toggle() {
    return state.open ? close() : open();
  }

  // setWidth is the bidirectional-control entry point: both the human controls
  // (A2) and the agent drive width through here so the panel stays in sync.
  function setWidth(w) {
    applyWidth(w);
    return getState();
  }

  // getState is the agent's read surface: current width plus the layout-shift
  // findings detected at that width and a flat list of their selectors (for the
  // A4 overlay / agent handoff).
  function getState() {
    var selectors = [];
    for (var i = 0; i < state.shifts.length; i++) {
      selectors.push({ id: state.shifts[i].id, selector: state.shifts[i].selector });
    }
    return {
      open: state.open,
      width: state.width,
      shifts: state.shifts,
      selectors: selectors
    };
  }

  // Augment the existing responsive global without clobbering the audit API.
  var ns = window.__devtool_responsive = window.__devtool_responsive || {};
  ns.open = open;
  ns.close = close;
  ns.toggle = toggle;
  ns.setWidth = setWidth;
  ns.getState = getState;
  ns.captureShifts = captureShifts;
})();

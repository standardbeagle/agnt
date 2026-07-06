// Transform Handles Module
//
// Direct-manipulation geometry handles for the element currently selected in
// design mode. Renders 8 resize handles (4 corner, 4 edge) plus a move grip
// around the live DOM node, translates pointer drags into geometry deltas,
// snaps them to design tokens / a spacing grid / sibling edges, previews the
// result non-destructively through the override store, and on release emits a
// `design_edit` code-change request to the agent (selector + computed delta).
//
// Building blocks reused by later passes (spacing handles, sibling reorder,
// inline editing): this handle renderer, the override store, and the
// design_edit channel.
(function() {
  'use strict';

  // Getters so dependencies resolve at call time, not parse time.
  function getCore() { return window.__devtool_core; }
  function getUtils() { return window.__devtool_utils; }
  function getStore() { return window.__devtool_override_store; }

  var SNAP_THRESHOLD = 4;   // px; nearest candidate within this wins
  var MIN_SIZE = 8;         // px; floor for resized width/height
  var HANDLE_SIZE = 10;     // px; visual handle square
  // One below tokens.z.panel so geometry handles never cover the palette or
  // style-editor panels (which sit at z.panel).
  var Z_INDEX = window.__devtoolTokens ? window.__devtoolTokens.z.panel - 1 : 2147483643;

  // Handle anchor table. ax/ay are fractional anchor points on the element
  // rect (0 = left/top, 0.5 = center, 1 = right/bottom). ex/ey name which edge
  // a drag moves ('e'/'w' horizontally, 'n'/'s' vertically); absent = fixed.
  var HANDLES = [
    { key: 'nw', ax: 0,   ay: 0,   ex: 'w', ey: 'n', cursor: 'nwse-resize' },
    { key: 'n',  ax: 0.5, ay: 0,            ey: 'n', cursor: 'ns-resize' },
    { key: 'ne', ax: 1,   ay: 0,   ex: 'e', ey: 'n', cursor: 'nesw-resize' },
    { key: 'e',  ax: 1,   ay: 0.5, ex: 'e',          cursor: 'ew-resize' },
    { key: 'se', ax: 1,   ay: 1,   ex: 'e', ey: 's', cursor: 'nwse-resize' },
    { key: 's',  ax: 0.5, ay: 1,            ey: 's', cursor: 'ns-resize' },
    { key: 'sw', ax: 0,   ay: 1,   ex: 'w', ey: 's', cursor: 'nesw-resize' },
    { key: 'w',  ax: 0,   ay: 0.5, ex: 'w',          cursor: 'ew-resize' }
  ];

  var state = {
    element: null,
    selector: null,
    xpath: null,
    container: null,   // overlay mount holding the handles (shadow DOM)
    handles: {},       // key -> handle DOM element
    moveGrip: null,
    rafPending: false,
    dragging: null     // active drag descriptor or null
  };

  // Active pointer capture {el, pointerId}, released on drag teardown so the
  // handle does not retain the pointer after the drag ends.
  var capture = null;

  // ── Snap engine (pure core) ────────────────────────────────────────────────

  // snapValue returns the nearest candidate to raw within threshold, else raw.
  // Pure — the unit of standalone testing.
  function snapValue(raw, candidates, threshold) {
    if (typeof threshold !== 'number') threshold = SNAP_THRESHOLD;
    var best = raw;
    var bestDist = threshold;
    for (var i = 0; i < candidates.length; i++) {
      var c = candidates[i];
      if (typeof c !== 'number' || !isFinite(c)) continue;
      var d = Math.abs(raw - c);
      if (d <= bestDist) {
        bestDist = d;
        best = c;
      }
    }
    return best;
  }

  // gridCandidates returns the nearest 4px and 8px multiples around raw. Pure.
  function gridCandidates(raw) {
    return [Math.round(raw / 4) * 4, Math.round(raw / 8) * 8];
  }

  // collectTokens scans :root / html rules for numeric --custom-property values
  // in px or rem and returns them as px numbers. Cross-origin sheets throw on
  // cssRules access — skipped, not fatal.
  function collectTokens() {
    var rootPx = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
    var out = [];
    var sheets = document.styleSheets;
    for (var s = 0; s < sheets.length; s++) {
      var rules;
      try {
        rules = sheets[s].cssRules;
      } catch (e) {
        continue; // cross-origin stylesheet — cannot read
      }
      if (!rules) continue;
      for (var r = 0; r < rules.length; r++) {
        var rule = rules[r];
        if (!rule.style || !rule.selectorText) continue;
        if (rule.selectorText !== ':root' && rule.selectorText !== 'html') continue;
        for (var p = 0; p < rule.style.length; p++) {
          var name = rule.style[p];
          if (name.indexOf('--') !== 0) continue;
          var val = rule.style.getPropertyValue(name).trim();
          var px = toPx(val, rootPx);
          if (px != null) out.push(px);
        }
      }
    }
    return out;
  }

  function toPx(val, rootPx) {
    var m = /^(-?[\d.]+)(px|rem)$/.exec(val);
    if (!m) return null;
    var n = parseFloat(m[1]);
    if (!isFinite(n)) return null;
    return m[2] === 'rem' ? n * rootPx : n;
  }

  // ── Selection wiring ───────────────────────────────────────────────────────

  function onPaletteShow(e) {
    var el = e && e.detail && e.detail.element;
    if (!el) return;
    select(el);
  }

  function select(element) {
    // Detached-element guard — abort, no phantom handles.
    if (!element || !element.isConnected) {
      console.warn('[DevTool] transform: selected element is detached; handles not rendered');
      hide();
      return;
    }
    state.element = element;
    state.selector = safeSelector(element);
    state.xpath = generateXPath(element);
    ensureContainer();
    showHandles();
    scheduleReposition();
  }

  function safeSelector(element) {
    try {
      var utils = getUtils();
      if (utils && utils.generateSelector) return utils.generateSelector(element);
    } catch (e) { /* fall through */ }
    return element.tagName ? element.tagName.toLowerCase() : '';
  }

  // ── Overlay rendering ──────────────────────────────────────────────────────

  function getMountRoot() {
    return typeof window.__devtoolGetMountRoot === 'function'
      ? window.__devtoolGetMountRoot()
      : document.documentElement;
  }

  function ensureContainer() {
    if (state.container && state.container.isConnected) return;
    var container = document.createElement('div');
    container.className = '__devtool-transform-handles';
    container.style.cssText = [
      'position: fixed',
      'top: 0',
      'left: 0',
      'width: 0',
      'height: 0',
      'pointer-events: none',
      'z-index: ' + Z_INDEX
    ].join(';');

    HANDLES.forEach(function(def) {
      var h = makeHandle(def);
      state.handles[def.key] = h;
      container.appendChild(h);
    });

    state.moveGrip = makeMoveGrip();
    container.appendChild(state.moveGrip);

    getMountRoot().appendChild(container);
    state.container = container;
  }

  function makeHandle(def) {
    var h = document.createElement('div');
    h.dataset.transformHandle = def.key;
    h.style.cssText = [
      'position: fixed',
      'width: ' + HANDLE_SIZE + 'px',
      'height: ' + HANDLE_SIZE + 'px',
      'box-sizing: border-box',
      'background: #ffffff',
      'border: 1.5px solid #2563eb',
      'border-radius: 2px',
      'pointer-events: auto',
      'cursor: ' + def.cursor,
      'display: none'
    ].join(';');
    h.addEventListener('pointerdown', function(ev) { startDrag(ev, def, h); });
    return h;
  }

  function makeMoveGrip() {
    var g = document.createElement('div');
    g.dataset.transformHandle = 'move';
    g.title = 'Drag to move';
    g.style.cssText = [
      'position: fixed',
      'width: 18px',
      'height: 18px',
      'box-sizing: border-box',
      'background: #2563eb',
      'border: 2px solid #ffffff',
      'border-radius: 50%',
      'pointer-events: auto',
      'cursor: move',
      'display: none'
    ].join(';');
    g.addEventListener('pointerdown', function(ev) {
      startDrag(ev, { key: 'move', move: true }, g);
    });
    return g;
  }

  function showHandles() {
    eachHandle(function(h) { h.style.display = 'block'; });
    if (state.moveGrip) state.moveGrip.style.display = 'block';
  }

  function hide() {
    state.element = null;
    state.selector = null;
    state.xpath = null;
    eachHandle(function(h) { h.style.display = 'none'; });
    if (state.moveGrip) state.moveGrip.style.display = 'none';
  }

  function eachHandle(fn) {
    for (var k in state.handles) {
      if (Object.prototype.hasOwnProperty.call(state.handles, k)) fn(state.handles[k]);
    }
  }

  // reposition places every handle on the element's current viewport rect.
  function reposition() {
    if (!state.element) return;
    if (!state.element.isConnected) {
      console.warn('[DevTool] transform: element detached; hiding handles');
      hide();
      return;
    }
    var rect = state.element.getBoundingClientRect();
    HANDLES.forEach(function(def) {
      var h = state.handles[def.key];
      if (!h) return;
      var cx = rect.left + rect.width * def.ax;
      var cy = rect.top + rect.height * def.ay;
      h.style.left = (cx - HANDLE_SIZE / 2) + 'px';
      h.style.top = (cy - HANDLE_SIZE / 2) + 'px';
    });
    if (state.moveGrip) {
      state.moveGrip.style.left = (rect.left + rect.width / 2 - 9) + 'px';
      state.moveGrip.style.top = (rect.top + rect.height / 2 - 9) + 'px';
    }
  }

  function scheduleReposition() {
    if (state.rafPending) return;
    state.rafPending = true;
    requestAnimationFrame(function() {
      state.rafPending = false;
      reposition();
    });
  }

  // ── Drag ───────────────────────────────────────────────────────────────────

  function startDrag(ev, def, handleEl) {
    if (!state.element) return;
    if (!state.element.isConnected) {
      console.warn('[DevTool] transform: element detached at drag start; aborting');
      hide();
      return;
    }
    ev.preventDefault();
    ev.stopPropagation();

    var rect = state.element.getBoundingClientRect();
    var cs = getComputedStyle(state.element);
    state.dragging = {
      def: def,
      startX: ev.clientX,
      startY: ev.clientY,
      rect: { left: rect.left, top: rect.top, width: rect.width, height: rect.height },
      before: { width: cs.width, height: cs.height },
      dimCandidates: collectTokens(),
      guides: collectGuides(state.element),
      props: null
    };

    try { handleEl.setPointerCapture(ev.pointerId); } catch (e) { /* non-fatal */ }
    capture = { el: handleEl, pointerId: ev.pointerId };
    document.addEventListener('pointermove', onDragMove, true);
    document.addEventListener('pointerup', onDragEnd, true);
  }

  function onDragMove(ev) {
    var d = state.dragging;
    if (!d) return;
    var props = computeProps(d, ev.clientX - d.startX, ev.clientY - d.startY);
    d.props = props;
    var store = getStore();
    try {
      var oid = store.ensureOID(state.element);
      store.upsert(oid, props);
    } catch (e) {
      console.error('[DevTool] transform: override upsert failed', e);
      cancelDrag();
      return;
    }
    scheduleReposition();
  }

  function onDragEnd(ev) {
    var d = state.dragging;
    teardownDragListeners();
    if (!d) return;
    state.dragging = null;

    if (!d.props) return; // no movement → nothing to commit
    if (!state.element || !state.element.isConnected) {
      console.warn('[DevTool] transform: element detached before commit; no edit emitted');
      return;
    }
    emitEdit(d);
  }

  function cancelDrag() {
    teardownDragListeners();
    state.dragging = null;
  }

  function teardownDragListeners() {
    document.removeEventListener('pointermove', onDragMove, true);
    document.removeEventListener('pointerup', onDragEnd, true);
    if (capture) {
      try { capture.el.releasePointerCapture(capture.pointerId); } catch (e) { /* pointer may be lost */ }
      capture = null;
    }
  }

  // computeProps turns a raw drag delta into snapped override properties.
  // Resize snaps width/height to token+grid dimension candidates and keeps the
  // opposite edge anchored; move snaps the top-left to sibling/parent edge
  // guides plus the grid. Output is also the design_edit delta payload.
  function computeProps(d, dx, dy) {
    var r = d.rect;
    var props = {};

    if (d.def.move) {
      var left = snapValue(r.left + dx, d.guides.x.concat(gridCandidates(r.left + dx)));
      var top = snapValue(r.top + dy, d.guides.y.concat(gridCandidates(r.top + dy)));
      var tx = left - r.left;
      var ty = top - r.top;
      if (tx !== 0 || ty !== 0) {
        props.transform = 'translate(' + Math.round(tx) + 'px, ' + Math.round(ty) + 'px)';
      }
      return props;
    }

    var w = r.width;
    var h = r.height;
    var tX = 0;
    var tY = 0;

    if (d.def.ex === 'e') {
      w = Math.max(MIN_SIZE, snapDim(r.width + dx, d));
    } else if (d.def.ex === 'w') {
      w = Math.max(MIN_SIZE, snapDim(r.width - dx, d));
      tX = r.width - w; // anchor the right edge
    }

    if (d.def.ey === 's') {
      h = Math.max(MIN_SIZE, snapDim(r.height + dy, d));
    } else if (d.def.ey === 'n') {
      h = Math.max(MIN_SIZE, snapDim(r.height - dy, d));
      tY = r.height - h; // anchor the bottom edge
    }

    if (d.def.ex) props.width = Math.round(w) + 'px';
    if (d.def.ey) props.height = Math.round(h) + 'px';
    if (tX !== 0 || tY !== 0) {
      props.transform = 'translate(' + Math.round(tX) + 'px, ' + Math.round(tY) + 'px)';
    }
    return props;
  }

  function snapDim(raw, d) {
    return snapValue(raw, d.dimCandidates.concat(gridCandidates(raw)));
  }

  // collectGuides gathers sibling + parent box edges as snap targets, split
  // into vertical guides (x: left/right coords) and horizontal (y: top/bottom).
  function collectGuides(element) {
    var x = [];
    var y = [];
    var parent = element.parentElement;
    if (parent) {
      var pr = parent.getBoundingClientRect();
      x.push(pr.left, pr.right);
      y.push(pr.top, pr.bottom);
      var sibs = parent.children;
      for (var i = 0; i < sibs.length; i++) {
        if (sibs[i] === element) continue;
        var sr = sibs[i].getBoundingClientRect();
        x.push(sr.left, sr.right);
        y.push(sr.top, sr.bottom);
      }
    }
    return { x: x, y: y };
  }

  // ── Emit ───────────────────────────────────────────────────────────────────

  function emitEdit(d) {
    var core = getCore();
    if (!core || !core.send) {
      console.warn('[DevTool] transform: core unavailable; design_edit not sent');
      return;
    }
    var store = getStore();
    var oid = state.element.getAttribute(store.OID_ATTR);
    var deltas = store.read(oid) || d.props;
    var cs = getComputedStyle(state.element);

    core.send('design_edit', {
      selector: state.selector,
      xpath: state.xpath,
      oid: oid,
      deltas: deltas,
      computedBefore: d.before,
      computedAfter: { width: cs.width, height: cs.height },
      metadata: buildMetadata(state.element)
    });
  }

  function buildMetadata(element) {
    return {
      tag: element.tagName.toLowerCase(),
      id: element.id || null,
      classes: Array.prototype.slice.call(element.classList),
      attributes: captureAttributes(element),
      text: (element.textContent || '').trim().substring(0, 100),
      rect: { width: element.offsetWidth, height: element.offsetHeight }
    };
  }

  function captureAttributes(element) {
    var attrs = {};
    var list = element.attributes;
    for (var i = 0; i < list.length; i++) {
      attrs[list[i].name] = list[i].value;
    }
    return attrs;
  }

  // generateXPath mirrors design.js so payloads from either path are
  // interchangeable for the agent.
  function generateXPath(element) {
    if (element.id) return '//*[@id="' + element.id + '"]';
    var path = '';
    var node = element;
    while (node && node.nodeType === 1) {
      var index = 0;
      var sibling = node.previousSibling;
      while (sibling) {
        if (sibling.nodeType === 1 && sibling.nodeName === node.nodeName) index++;
        sibling = sibling.previousSibling;
      }
      var tag = node.nodeName.toLowerCase();
      var idx = index > 0 ? '[' + (index + 1) + ']' : '';
      path = '/' + tag + idx + path;
      node = node.parentNode;
    }
    return path;
  }

  // ── Global listeners + API ──────────────────────────────────────────────────

  document.addEventListener('devtool:palette-show', onPaletteShow);
  window.addEventListener('scroll', scheduleReposition, true);
  window.addEventListener('resize', scheduleReposition);

  window.__devtool_transform = {
    select: select,
    hide: hide,
    // Pure snap helpers exposed for standalone unit testing.
    snapValue: snapValue,
    gridCandidates: gridCandidates
  };
})();

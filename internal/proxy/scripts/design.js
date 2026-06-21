// Design Iteration Module
// Enables iterative design exploration by selecting elements and generating alternatives

(function() {
  'use strict';

  // Use getters to ensure modules are available at call time, not at parse time
  function getCore() { return window.__devtool_core; }
  function getUtils() { return window.__devtool_utils; }

  // notify surfaces a deterministic browser toast so the developer always sees
  // design-mode activity even when the heavy lifting (DESIGN.md + alternatives)
  // happens asynchronously on the agent side. Guarded — degrades silently if the
  // toast module is not loaded.
  function notify(message, kind) {
    try {
      var t = window.__devtool_toast;
      if (!t) return;
      (t[kind || 'info'] || t.info)(message, 'Design mode');
    } catch (e) { /* toast unavailable — non-fatal */ }
  }

  // State
  var state = {
    isActive: false,
    selectedElement: null, // DOM element
    selector: null,        // CSS selector
    xpath: null,           // XPath for robustness
    originalHTML: '',
    currentIndex: 0,
    alternatives: [],      // Array of HTML strings
    contextHTML: '',       // Parent context for LLM
    metadata: null,        // Element metadata
    chatHistory: [],       // Chat messages about this element
    overlay: null,         // Selection overlay
    controls: null         // Navigation controls UI
  };

  // Start design mode - enable element selection
  function start() {
    if (state.isActive) return;
    state.isActive = true;
    notify('Active — click an element to redesign');
    showSelectionOverlay();
  }

  // Stop design mode
  function stop() {
    if (!state.isActive) return;
    state.isActive = false;
    hideSelectionOverlay();
    hideControls();
    clearSelection();
  }

  // Show overlay for element selection
  function showSelectionOverlay() {
    var overlay = document.createElement('div');
    overlay.id = '__devtool-design-overlay';
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
    highlight.id = '__devtool-design-highlight';
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
    tooltip.id = '__devtool-design-tooltip';
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
    instructions.textContent = 'Click an element to start design iteration • ESC to cancel';
    overlay.appendChild(instructions);

    var hoveredElement = null;
    // rAF-batch layout reads triggered by mousemove. We capture the latest
    // pointer coords into pendingMove and defer the elementFromPoint +
    // getBoundingClientRect work to the next animation frame. Multiple
    // mousemove events within the same frame coalesce to a single layout
    // read. rafId is cancelled on overlay teardown (see hideSelectionOverlay).
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

      // Ignore devtool elements
      if (!el || (el.id && el.id.startsWith('__devtool'))) {
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
      if (rafId) return; // already scheduled; coalesce into pending frame
      rafId = requestAnimationFrame(processPendingMove);
    });

    // Expose teardown for hideSelectionOverlay so we cancel any pending frame
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

      if (hoveredElement) {
        selectElement(hoveredElement);
      }
    });

    function handleEscape(e) {
      if (e.key === 'Escape') {
        stop();
        document.removeEventListener('keydown', handleEscape);
      }
    }
    document.addEventListener('keydown', handleEscape);

    state.overlay = overlay;
    document.body.appendChild(overlay);
  }

  // Hide selection overlay
  function hideSelectionOverlay() {
    if (state.overlay) {
      if (typeof state.overlay.__devtoolCancelRaf === 'function') {
        state.overlay.__devtoolCancelRaf();
      }
      if (state.overlay.parentNode) {
        state.overlay.parentNode.removeChild(state.overlay);
      }
      state.overlay = null;
    }
  }

  // Select an element for design iteration
  function selectElement(element) {
    if (!element) return;

    // Hide selection overlay
    hideSelectionOverlay();

    // Store element reference
    state.selectedElement = element;
    state.selector = getUtils().generateSelector(element);
    state.xpath = generateXPath(element);
    state.originalHTML = element.innerHTML;
    state.currentIndex = 0;
    state.alternatives = [element.innerHTML]; // Start with original

    // Capture context (parent with siblings)
    state.contextHTML = captureContext(element);

    // Capture metadata
    state.metadata = {
      tag: element.tagName.toLowerCase(),
      id: element.id || null,
      classes: Array.from(element.classList),
      attributes: captureAttributes(element),
      text: element.textContent.trim().substring(0, 100),
      rect: {
        width: element.offsetWidth,
        height: element.offsetHeight
      }
    };

    // Reset chat history and per-alternative metadata (index 0 = original)
    state.chatHistory = [];
    state.altMeta = [{ label: 'original', note: 'Original' }];

    // Extract the proxied app's design tokens so generated alternatives stay
    // on-scheme. Best-effort: never throws, returns null when nothing usable.
    state.scheme = extractScheme(element);

    // OID-primary locator: stamp a stable data-devtool-oid so preview/apply
    // resolve the target by oid, not a brittle nth-of-type selector that React
    // re-renders invalidate.
    try {
      var os = window.__devtool_override_store;
      state.oid = (os && os.ensureOID) ? os.ensureOID(element) : null;
    } catch (e) { state.oid = null; }
    if (!state.previewMode) state.previewMode = 'side';

    // Open the non-invasive preview panel and re-hydrate any alternatives that
    // survived an HMR recompile.
    ensurePanel();
    restorePersisted();
    renderPreview();
    notify('Scheme captured — preparing DESIGN.md & on-scheme alternatives…');

    // Send initial state to agent
    sendDesignState();

    // Dispatch palette-show event so the floating context palette (palette.js)
    // can attach to this element. Decoupled from palette internals — any
    // module that wants to drive the palette can dispatch the same event.
    try {
      document.dispatchEvent(new CustomEvent('devtool:palette-show', {
        detail: { element: element, source: 'design' }
      }));
    } catch (e) {
      // CustomEvent constructor unsupported in some legacy IE — degrade silently.
    }
  }

  // Generate XPath for element
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

  // Capture parent context with siblings
  function captureContext(element) {
    var parent = element.parentElement;
    if (!parent) return '';

    // Clone parent and truncate sibling content for brevity
    var clone = parent.cloneNode(true);
    var children = clone.children;

    for (var i = 0; i < children.length; i++) {
      var child = children[i];
      var originalChild = parent.children[i];

      if (originalChild === element) {
        // Mark the target element
        child.setAttribute('data-design-target', 'true');
      } else {
        // Truncate siblings to just their tag
        var summary = '<' + child.tagName.toLowerCase();
        if (child.className) summary += ' class="' + child.className + '"';
        if (child.id) summary += ' id="' + child.id + '"';
        summary += '>...</' + child.tagName.toLowerCase() + '>';

        child.outerHTML = summary;
      }
    }

    return clone.outerHTML;
  }

  // Capture relevant attributes
  function captureAttributes(element) {
    var attrs = {};
    var relevantAttrs = ['href', 'src', 'type', 'placeholder', 'alt', 'title', 'role', 'aria-label'];

    relevantAttrs.forEach(function(name) {
      if (element.hasAttribute(name)) {
        attrs[name] = element.getAttribute(name);
      }
    });

    return attrs;
  }

  // Show navigation controls
  // ── Non-invasive preview panel ──────────────────────────────────────────────
  // Previews render inside a Shadow-DOM panel, NEVER into the framework-owned
  // target subtree. Writing innerHTML into a React/HMR-managed node gets wiped by
  // reconciliation and leaves a stale selector; the shadow root is untouchable by
  // the app. Two modes: side-by-side (original | alternative) and overlay-on-top
  // (alternative floated over the target's rect).

  function mkBtn(label, onClick) {
    var b = document.createElement('button');
    b.className = 'btn';
    b.textContent = label;
    b.addEventListener('click', onClick);
    return b;
  }

  // resolveTarget: OID-primary. nth-of-type selectors break the instant React
  // re-renders, so the stable data-devtool-oid is the authoritative locator.
  function resolveTarget() {
    var os = window.__devtool_override_store;
    if (state.oid && os && os.OID_ATTR) {
      var byOid = document.querySelector('[' + os.OID_ATTR + '="' + state.oid + '"]');
      if (byOid) return byOid;
    }
    if (state.selector) {
      try { var bySel = document.querySelector(state.selector); if (bySel) return bySel; } catch (e) {}
    }
    return (state.selectedElement && state.selectedElement.isConnected) ? state.selectedElement : null;
  }

  function badgeFor(index) {
    var n = (index + 1) + ' / ' + state.alternatives.length;
    var meta = state.altMeta && state.altMeta[index];
    if (!meta || !meta.label) return n;
    var b = meta.label === 'draft' ? 'Fast draft'
      : meta.label === 'original' ? 'Original' : 'On-scheme';
    if (meta.note && meta.label !== 'original') b += ' · ' + meta.note;
    return n + '  [' + b + ']';
  }

  // renderFragment renders arbitrary alternative HTML into an isolated node. Safe
  // because it lives in the shadow root, not the app's tree.
  function renderFragment(html) {
    var d = document.createElement('div');
    d.className = 'render';
    d.innerHTML = html || '';
    return d;
  }

  function ensurePanel() {
    if (state.panel && state.panel.host && state.panel.host.isConnected) return state.panel;

    var host = document.createElement('div');
    host.id = '__devtool-design-panel';
    host.style.cssText = 'all:initial';
    var root = host.attachShadow ? host.attachShadow({ mode: 'open' }) : host;
    document.body.appendChild(host);

    var style = document.createElement('style');
    style.textContent = [
      ':host{all:initial}',
      '.dock{position:fixed;right:16px;bottom:16px;width:min(720px,46vw);max-height:78vh;background:#fff;border:1px solid #e2e8f0;border-radius:12px;box-shadow:0 10px 40px rgba(0,0,0,.18);display:flex;flex-direction:column;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}',
      '.bar{display:flex;align-items:center;gap:8px;padding:10px 12px;border-bottom:1px solid #eef2f7}',
      '.idx{font-size:12px;color:#64748b;min-width:140px}',
      '.spacer{flex:1}',
      '.btn{padding:6px 10px;background:#6366f1;color:#fff;border:none;border-radius:6px;font-size:12px;font-weight:600;cursor:pointer}',
      '.btn:hover{background:#4f46e5}',
      '.btn.ghost{background:#f1f5f9;color:#334155}',
      '.btn.ghost.on{background:#6366f1;color:#fff}',
      '.in{flex:1;min-width:120px;border:1px solid #e2e8f0;border-radius:8px;padding:7px 10px;font-size:13px;color:#1e293b;outline:none}',
      '.body{display:flex;overflow:auto}',
      '.col{flex:1;min-width:0;padding:12px;overflow:auto}',
      '.col+.col{border-left:1px solid #eef2f7}',
      '.cap{font-size:11px;text-transform:uppercase;letter-spacing:.04em;color:#94a3b8;margin:0 0 8px}',
      '.render{font-size:14px;color:#0f172a}'
    ].join('');
    root.appendChild(style);

    var dock = document.createElement('div'); dock.className = 'dock';
    var bar = document.createElement('div'); bar.className = 'bar';
    var idx = document.createElement('div'); idx.className = 'idx';
    var sideBtn = mkBtn('Side-by-side', function () { setMode('side'); }); sideBtn.className = 'btn ghost';
    var overBtn = mkBtn('Overlay', function () { setMode('overlay'); }); overBtn.className = 'btn ghost';
    var spacer = document.createElement('div'); spacer.className = 'spacer';
    var chatIn = document.createElement('input'); chatIn.className = 'in'; chatIn.type = 'text';
    chatIn.placeholder = 'Describe changes…';
    chatIn.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && chatIn.value.trim()) { chat(chatIn.value.trim()); chatIn.value = ''; }
    });
    bar.appendChild(mkBtn('◀', function () { previous(); }));
    bar.appendChild(idx);
    bar.appendChild(mkBtn('▶', function () { next(); }));
    bar.appendChild(sideBtn);
    bar.appendChild(overBtn);
    bar.appendChild(spacer);
    bar.appendChild(chatIn);
    var closeB = mkBtn('✕', function () { stop(); }); closeB.className = 'btn ghost';
    bar.appendChild(closeB);

    var body = document.createElement('div'); body.className = 'body';
    dock.appendChild(bar); dock.appendChild(body); root.appendChild(dock);

    // Overlay-on-top layer: its own shadow host so it can float anywhere over
    // the page, isolated from app CSS.
    var olHost = document.createElement('div'); olHost.id = '__devtool-design-overlay-preview';
    olHost.style.cssText = 'all:initial';
    var olRoot = olHost.attachShadow ? olHost.attachShadow({ mode: 'open' }) : olHost;
    var olStyle = document.createElement('style');
    olStyle.textContent = ':host{all:initial}.ol{position:fixed;z-index:2147483645;overflow:auto;background:#fff;border:2px solid #6366f1;border-radius:8px;box-shadow:0 8px 30px rgba(0,0,0,.25)}.render{padding:8px;font-family:-apple-system,sans-serif}';
    olRoot.appendChild(olStyle);
    var olBox = document.createElement('div'); olBox.className = 'ol'; olBox.style.display = 'none';
    olRoot.appendChild(olBox);
    document.body.appendChild(olHost);

    state.panel = { host: host, root: root, idx: idx, body: body, sideBtn: sideBtn, overBtn: overBtn, olHost: olHost, olBox: olBox };

    // Overlay mode must follow the target as the page scrolls/reflows.
    state.reposition = function () { if (state.previewMode === 'overlay') positionOverlay(); };
    window.addEventListener('scroll', state.reposition, true);
    window.addEventListener('resize', state.reposition, true);
    return state.panel;
  }

  function setMode(mode) {
    state.previewMode = mode;
    persistMode();
    renderPreview();
  }

  // renderPreview repaints the panel from state — never mutates the target.
  function renderPreview() {
    var p = state.panel;
    if (!p) return;
    var i = state.currentIndex;
    p.idx.textContent = badgeFor(i);
    p.sideBtn.className = 'btn ghost' + (state.previewMode === 'side' ? ' on' : '');
    p.overBtn.className = 'btn ghost' + (state.previewMode === 'overlay' ? ' on' : '');
    var altHTML = state.alternatives[i] != null ? state.alternatives[i] : '';

    if (state.previewMode === 'overlay') {
      p.body.innerHTML = '';
      var oc = document.createElement('div'); oc.className = 'col';
      var ocap = document.createElement('p'); ocap.className = 'cap';
      ocap.textContent = 'Overlay on target — switch to Side-by-side to compare';
      oc.appendChild(ocap); p.body.appendChild(oc);
      p.olBox.innerHTML = '';
      p.olBox.appendChild(renderFragment(altHTML));
      p.olBox.style.display = 'block';
      positionOverlay();
    } else {
      p.olBox.style.display = 'none';
      p.body.innerHTML = '';
      var origCol = document.createElement('div'); origCol.className = 'col';
      var oCap = document.createElement('p'); oCap.className = 'cap'; oCap.textContent = 'Original';
      origCol.appendChild(oCap); origCol.appendChild(renderFragment(state.originalHTML));
      var newCol = document.createElement('div'); newCol.className = 'col';
      var nCap = document.createElement('p'); nCap.className = 'cap'; nCap.textContent = badgeFor(i);
      newCol.appendChild(nCap); newCol.appendChild(renderFragment(altHTML));
      p.body.appendChild(origCol); p.body.appendChild(newCol);
    }
  }

  function positionOverlay() {
    var p = state.panel;
    if (!p || !p.olBox) return;
    var t = resolveTarget();
    if (!t) { p.olBox.style.display = 'none'; return; }
    var r = t.getBoundingClientRect();
    p.olBox.style.top = Math.max(8, r.top) + 'px';
    p.olBox.style.left = Math.max(8, r.left) + 'px';
    p.olBox.style.minWidth = Math.max(80, r.width) + 'px';
    p.olBox.style.maxWidth = Math.min(window.innerWidth - 16, Math.max(120, r.width)) + 'px';
    p.olBox.style.maxHeight = Math.min(window.innerHeight - 16, Math.max(80, r.height * 2)) + 'px';
  }

  // sessionStorage persistence: HMR/Turbopack recompiles were thrashing the
  // in-memory alternatives list (count bounced 4→3→1→4). Keyed by oid+path so a
  // recompile re-hydrates instead of losing work.
  function persistKey() { return '__devtool_design:' + (state.oid || state.selector || '') + ':' + location.pathname; }
  function persistAlternatives() {
    try {
      sessionStorage.setItem(persistKey(), JSON.stringify({
        alternatives: state.alternatives, altMeta: state.altMeta,
        originalHTML: state.originalHTML, currentIndex: state.currentIndex
      }));
    } catch (e) { /* storage disabled — in-memory still works */ }
  }
  function persistMode() { try { sessionStorage.setItem('__devtool_design:mode', state.previewMode); } catch (e) {} }
  function restorePersisted() {
    try {
      var mode = sessionStorage.getItem('__devtool_design:mode');
      if (mode) state.previewMode = mode;
      var raw = sessionStorage.getItem(persistKey());
      if (!raw) return;
      var saved = JSON.parse(raw);
      if (saved && Array.isArray(saved.alternatives) && saved.alternatives.length > state.alternatives.length) {
        state.alternatives = saved.alternatives;
        state.altMeta = saved.altMeta || state.altMeta;
        if (typeof saved.currentIndex === 'number') state.currentIndex = saved.currentIndex;
      }
    } catch (e) { /* corrupt entry — ignore, start fresh */ }
  }

  // hideControls tears down the panel + its global listeners.
  function hideControls() {
    if (state.reposition) {
      window.removeEventListener('scroll', state.reposition, true);
      window.removeEventListener('resize', state.reposition, true);
      state.reposition = null;
    }
    if (state.panel) {
      if (state.panel.host && state.panel.host.parentNode) state.panel.host.parentNode.removeChild(state.panel.host);
      if (state.panel.olHost && state.panel.olHost.parentNode) state.panel.olHost.parentNode.removeChild(state.panel.olHost);
      state.panel = null;
    }
  }

  // Navigate to next alternative
  function next() {
    if (state.currentIndex < state.alternatives.length - 1) {
      applyAlternative(state.currentIndex + 1);
    } else {
      // No more alternatives - request new ones from agent
      requestAlternatives();
    }
  }

  // Navigate to previous alternative
  function previous() {
    if (state.currentIndex > 0) {
      applyAlternative(state.currentIndex - 1);
    }
  }

  // Select an alternative for preview. NON-INVASIVE: never writes into the
  // framework-owned target subtree (React reconciliation / HMR would wipe it and
  // the stored selector would go stale, throwing "innerHTML … not an object").
  // The preview renders in the isolated shadow-root panel instead.
  function applyAlternative(index) {
    if (index < 0 || index >= state.alternatives.length) return;
    state.currentIndex = index;
    persistAlternatives();
    renderPreview();
  }

  // Add one alternative. Storing NEVER depends on a live target node — mid-HMR
  // the node may be briefly absent, and a store that touches the DOM would throw
  // and lose the alternative. We store + persist first, then best-effort render.
  // meta = {label:'draft'|'variation', note:'<direction>'}.
  function addAlternative(html, meta) {
    if (typeof html !== 'string') return { error: 'html must be a string' };
    state.alternatives.push(html);
    if (!state.altMeta) state.altMeta = [];
    state.altMeta[state.alternatives.length - 1] =
      (meta && typeof meta === 'object') ? meta : { label: 'variation', note: '' };
    state.currentIndex = state.alternatives.length - 1;
    persistAlternatives();
    if (state.panel) renderPreview();
    return { success: true, index: state.currentIndex, total: state.alternatives.length };
  }

  // Batch add — push the whole concurrent variation set in one round-trip so a
  // recompile can't race individual adds (the cause of mid-session add failures).
  // items = [{html, label?, note?}, ...].
  function addAlternatives(items) {
    if (!Array.isArray(items)) return { error: 'items must be an array' };
    var added = 0;
    for (var i = 0; i < items.length; i++) {
      var it = items[i] || {};
      if (typeof it.html !== 'string') continue;
      state.alternatives.push(it.html);
      if (!state.altMeta) state.altMeta = [];
      state.altMeta[state.alternatives.length - 1] = { label: it.label || 'variation', note: it.note || '' };
      added++;
    }
    if (added > 0) {
      state.currentIndex = state.alternatives.length - 1;
      persistAlternatives();
      if (state.panel) renderPreview();
    }
    return { success: true, added: added, total: state.alternatives.length };
  }

  // Request new alternatives from agent
  function requestAlternatives() {
    var core = getCore();
    if (!core || !core.send) {
      console.error('[Design] Core not available');
      return;
    }

    console.log('[Design] Requesting alternatives for:', state.selector);
    core.send('design_request', {
      timestamp: Date.now(),
      selector: state.selector,
      xpath: state.xpath,
      currentHTML: state.alternatives[state.currentIndex],
      originalHTML: state.originalHTML,
      contextHTML: state.contextHTML,
      metadata: state.metadata,
      alternativesCount: state.alternatives.length,
      chatHistory: state.chatHistory,
      scheme: state.scheme || undefined
    });
  }

  // Extract a compact design-token scheme from the live DOM so generated
  // alternatives match the proxied app's look. Bounded element sample, fully
  // guarded — any failure yields null rather than blocking design mode.
  function extractScheme(element) {
    try {
      var rank = function (counts, limit) {
        return Object.keys(counts)
          .sort(function (a, b) { return counts[b] - counts[a]; })
          .slice(0, limit);
      };
      var bump = function (counts, val) {
        if (!val) return;
        val = ('' + val).trim();
        if (!val || val === 'none' || val === 'normal' || val === 'auto') return;
        // Skip fully transparent / zero noise.
        if (val === 'rgba(0, 0, 0, 0)' || val === 'transparent' || val === '0px') return;
        counts[val] = (counts[val] || 0) + 1;
      };

      var palette = {}, families = {}, sizes = {}, weights = {},
          spacing = {}, radius = {}, shadows = {};

      // Sample: selected element + ancestors + a bounded slice of the document.
      var nodes = [];
      var n = element;
      while (n && n.nodeType === 1 && nodes.length < 20) { nodes.push(n); n = n.parentElement; }
      var all = document.querySelectorAll('*');
      var cap = Math.min(all.length, 400);
      var stride = Math.max(1, Math.floor(all.length / cap));
      for (var i = 0; i < all.length && nodes.length < 420; i += stride) { nodes.push(all[i]); }

      for (var j = 0; j < nodes.length; j++) {
        var cs;
        try { cs = window.getComputedStyle(nodes[j]); } catch (e) { continue; }
        if (!cs) continue;
        bump(palette, cs.color);
        bump(palette, cs.backgroundColor);
        bump(palette, cs.borderTopColor);
        bump(families, cs.fontFamily);
        bump(sizes, cs.fontSize);
        bump(weights, cs.fontWeight);
        bump(spacing, cs.paddingTop);
        bump(spacing, cs.marginTop);
        bump(spacing, cs.gap);
        bump(radius, cs.borderTopLeftRadius);
        bump(shadows, cs.boxShadow);
      }

      var scheme = {
        palette: rank(palette, 8),
        fontFamilies: rank(families, 4),
        fontSizes: rank(sizes, 8),
        fontWeights: rank(weights, 5),
        spacing: rank(spacing, 8),
        radius: rank(radius, 6),
        shadows: rank(shadows, 4),
        cssVars: extractCSSVars()
      };

      // Drop empties; return null if nothing usable so the agent prompt omits
      // the scheme block entirely.
      var hasAny = false;
      Object.keys(scheme).forEach(function (k) {
        var v = scheme[k];
        if (Array.isArray(v) && v.length === 0) { delete scheme[k]; }
        else if (k === 'cssVars' && (!v || Object.keys(v).length === 0)) { delete scheme[k]; }
        else { hasAny = true; }
      });
      return hasAny ? scheme : null;
    } catch (e) {
      return null;
    }
  }

  // Read declared --* custom properties from :root rules. Cross-origin sheets
  // throw on cssRules access — skipped silently. Capped to avoid bloat.
  function extractCSSVars() {
    var vars = {};
    try {
      for (var s = 0; s < document.styleSheets.length; s++) {
        var rules;
        try { rules = document.styleSheets[s].cssRules; } catch (e) { continue; }
        if (!rules) continue;
        for (var r = 0; r < rules.length; r++) {
          var rule = rules[r];
          if (!rule.selectorText || rule.selectorText.indexOf(':root') === -1) continue;
          var style = rule.style;
          if (!style) continue;
          for (var p = 0; p < style.length; p++) {
            var prop = style[p];
            if (prop && prop.indexOf('--') === 0 && Object.keys(vars).length < 30) {
              vars[prop] = style.getPropertyValue(prop).trim();
            }
          }
        }
      }
    } catch (e) { /* best-effort */ }
    return vars;
  }

  // Send design state to agent
  function sendDesignState() {
    var core = getCore();
    if (!core || !core.send) {
      console.error('[Design] Core not available');
      return;
    }

    console.log('[Design] Sending design state for:', state.selector);
    core.send('design_state', {
      timestamp: Date.now(),
      selector: state.selector,
      xpath: state.xpath,
      originalHTML: state.originalHTML,
      contextHTML: state.contextHTML,
      metadata: state.metadata,
      scheme: state.scheme || undefined,
      url: window.location.href
    });
  }

  // Chat with LLM about current element
  function chat(message) {
    if (!state.selectedElement) {
      console.error('[Design] No element selected');
      return;
    }

    var core = getCore();
    if (!core || !core.send) {
      console.error('[Design] Core not available');
      return;
    }

    state.chatHistory.push({
      timestamp: Date.now(),
      message: message,
      role: 'user'
    });

    core.send('design_chat', {
      timestamp: Date.now(),
      message: message,
      selector: state.selector,
      xpath: state.xpath,
      currentHTML: state.alternatives[state.currentIndex],
      originalHTML: state.originalHTML,
      contextHTML: state.contextHTML,
      metadata: state.metadata,
      chatHistory: state.chatHistory,
      url: window.location.href
    });

    // Request alternatives based on chat
    requestAlternatives();
  }

  // Get current state
  function getState() {
    return {
      isActive: state.isActive,
      hasSelection: !!state.selectedElement,
      selector: state.selector,
      currentIndex: state.currentIndex,
      alternativesCount: state.alternatives.length,
      metadata: state.metadata,
      chatHistory: state.chatHistory
    };
  }

  // Clear selection
  function clearSelection() {
    state.selectedElement = null;
    state.selector = null;
    state.xpath = null;
    state.originalHTML = '';
    state.currentIndex = 0;
    state.alternatives = [];
    state.contextHTML = '';
    state.metadata = null;
    state.chatHistory = [];
    state.altMeta = [];
    state.scheme = null;
    state.oid = null;
  }

  // Export public API
  window.__devtool_design = {
    start: start,
    stop: stop,
    selectElement: selectElement,
    next: next,
    previous: previous,
    addAlternative: addAlternative,
    addAlternatives: addAlternatives,
    applyAlternative: applyAlternative,
    setMode: setMode,
    chat: chat,
    getState: getState
  };
})();

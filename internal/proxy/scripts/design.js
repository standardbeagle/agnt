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
    slot: null,            // Parent container geometry the alternative must fit
    exemplars: null,       // Same-signature components elsewhere on the page
    pageColors: null,      // Every distinct colour the page renders (clash check)
    swap: null,            // In-place swap: {target, clone, observer}
    overlay: null,         // Selection overlay
    controls: null         // Navigation controls UI
  };

  // Declarative publish variants are kept separate from HTML alternatives.
  // The variant engine owns validation and DOM mutation; design mode only
  // provides a small import/export bridge for authoring hand-off.
  var importedVariantSet = null;
  var importedVariantEngine = null;

  function cloneJSON(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function importVariantSet(variantSet, routeBinding, opts) {
    var api = window.__variantEngine;
    if (!api || typeof api.create !== 'function') {
      throw new Error('[Design] Variant engine not available');
    }
    var snapshot = cloneJSON(variantSet);
    var next = api.create(cloneJSON(snapshot), routeBinding || { when: 'any' }, opts || {});
    if (importedVariantEngine) importedVariantEngine.destroy();
    importedVariantSet = snapshot;
    importedVariantEngine = next;
    return { success: true, variantIds: next.variantIds() };
  }

  function exportVariantSet() {
    return importedVariantSet ? cloneJSON(importedVariantSet) : null;
  }

  function applyVariant(variantId) {
    if (!importedVariantEngine) throw new Error('[Design] No VariantSet imported');
    importedVariantEngine.apply(variantId);
  }

  function revertVariant() {
    if (importedVariantEngine) importedVariantEngine.revert();
  }

  // Shared theme (light/dark) from ui-tokens.js; fallback literals only for
  // the defensive "ui-tokens failed to load" case.
  var designTheme = window.__devtoolTokens ? window.__devtoolTokens.theme() : {
    primary: '#6366f1',
    primaryDark: '#4f46e5',
    surface: '#ffffff',
    surfaceAlt: '#f8fafc',
    border: '#e2e8f0',
    text: '#1e293b',
    textMuted: '#64748b',
    textInverse: '#ffffff'
  };
  var designZ = window.__devtoolTokens ? window.__devtoolTokens.z : { overlay: 2147483642, panel: 2147483644, critical: 2147483647 };

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
      'z-index: ' + designZ.critical,
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
      // critical, not 2147483648: that literal overflows int32 and browsers
      // clamp it anyway; the bar renders above overlay siblings by DOM order.
      'z-index: ' + designZ.critical
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

    // Escape dismissal via the shared overlay stack (ui-tokens.js) so only
    // the top-most devtool surface closes per keypress; local listener is
    // the fallback when ui-tokens failed to load.
    if (window.__devtoolOverlayStack) {
      window.__devtoolOverlayStack.push('design-select', stop);
    } else {
      var handleEscape = function(e) {
        if (e.key === 'Escape') {
          stop();
          document.removeEventListener('keydown', handleEscape);
        }
      };
      document.addEventListener('keydown', handleEscape);
      overlay.__devtoolEscapeFallback = handleEscape;
    }

    state.overlay = overlay;
    // Mount into the devtool shadow root when available (style isolation);
    // document.elementFromPoint hit-testing still resolves page elements
    // because the overlay toggles its own pointer-events during reads.
    var mount = window.__devtoolGetMountRoot ? window.__devtoolGetMountRoot() : document.body;
    mount.appendChild(overlay);
  }

  // Hide selection overlay
  function hideSelectionOverlay() {
    if (window.__devtoolOverlayStack) {
      window.__devtoolOverlayStack.pop('design-select');
    }
    if (state.overlay) {
      if (typeof state.overlay.__devtoolCancelRaf === 'function') {
        state.overlay.__devtoolCancelRaf();
      }
      if (state.overlay.__devtoolEscapeFallback) {
        document.removeEventListener('keydown', state.overlay.__devtoolEscapeFallback);
        state.overlay.__devtoolEscapeFallback = null;
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
    removeSwap();

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
    var extracted = extractScheme(element);
    state.scheme = extracted.scheme;
    state.pageColors = extracted.colors;

    // Slot geometry + same-signature exemplars elsewhere on the page: what the
    // alternative must fit, and the house grammar it should vary within.
    state.slot = captureSlot(element);
    state.exemplars = captureExemplars(element);

    // OID-primary locator: stamp a stable data-devtool-oid so preview/apply
    // resolve the target by oid, not a brittle nth-of-type selector that React
    // re-renders invalidate.
    try {
      var os = window.__devtool_override_store;
      state.oid = (os && os.ensureOID) ? os.ensureOID(element) : null;
    } catch (e) { state.oid = null; }
    if (!state.previewMode) state.previewMode = 'inplace';

    // Instant options: restyles built only from the page's own tokens, so the
    // user sees on-scheme alternatives swapped in before the agent replies.
    var instant = buildInstantVariants(element, state.scheme);
    for (var v = 0; v < instant.length; v++) {
      state.alternatives.push(instant[v].html);
      state.altMeta.push({ label: 'instant', note: instant[v].note, style: instant[v].style });
    }
    if (instant.length) state.currentIndex = 1;

    // Open the panel and re-hydrate any alternatives that survived an HMR
    // recompile.
    ensurePanel();
    restorePersisted();
    renderPreview();
    notify(instant.length
      ? instant.length + ' instant on-scheme options — ◀ ▶ to compare; the agent is designing more'
      : 'Scheme captured — the agent is designing on-scheme alternatives');

    // The agent message goes out immediately: it is the slow leg, so nothing
    // (screenshots, rasterising) may sit in front of it.
    sendDesignState(instant);

    // Design mode is chat-only iteration: the AI preview panel drives the
    // redesign. The inline quick-style palette (p/bg/op/disp bar) belongs to
    // Inspect mode, not Design — so we deliberately do not fire the palette
    // selection event here. Inspect mode reaches the palette via
    // style-editor.open() instead.
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

  // captureContext summarises the parent: its opening tag, the target in full
  // (marked data-design-target), and up to CONTEXT_SIBLING_CAP siblings as
  // tag/class/id stubs. Built as a string: deep-cloning the parent and
  // rewriting each sibling cost seconds on long lists.
  var CONTEXT_SIBLING_CAP = 30;
  function captureContext(element) {
    var parent = element.parentElement;
    if (!parent) return '';
    var tag = parent.tagName.toLowerCase();
    var shell = parent.cloneNode(false).outerHTML;
    var parts = [shell.slice(0, shell.length - ('</' + tag + '>').length)];
    var kids = parent.children, stubs = 0, skipped = 0;
    for (var i = 0; i < kids.length; i++) {
      var child = kids[i];
      if (child === element) {
        var marked = element.cloneNode(true);
        marked.setAttribute('data-design-target', 'true');
        parts.push(marked.outerHTML);
        continue;
      }
      if (stubs >= CONTEXT_SIBLING_CAP) { skipped++; continue; }
      stubs++;
      var ctag = child.tagName.toLowerCase();
      var stub = '<' + ctag;
      if (typeof child.className === 'string' && child.className) stub += ' class="' + child.className + '"';
      if (child.id) stub += ' id="' + child.id + '"';
      parts.push(stub + '>...</' + ctag + '>');
    }
    if (skipped) parts.push('<!-- ' + skipped + ' more siblings -->');
    parts.push('</' + tag + '>');
    return parts.join('');
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
  // Three modes. In place (default) swaps the alternative into the page beside
  // the hidden target (see swapInPlace). Side-by-side and overlay render it in a
  // frame carrying the page CSS. None of them writes into the framework-owned
  // target subtree: React/HMR reconciliation would wipe it.

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
      : meta.label === 'original' ? 'Original'
      : meta.label === 'instant' ? 'Instant' : 'On-scheme';
    if (meta.note && meta.label !== 'original') b += ' · ' + meta.note;
    return n + '  [' + b + ']';
  }

  // collectPageCSS clones the page's stylesheet <link>s and inline <style>s into
  // a string suitable for an <iframe srcdoc> <head>, so the redesign preview
  // renders with the real site CSS instead of bare unstyled HTML. <base> makes
  // relative url()/href resolve against the page origin.
  function collectPageCSS() {
    var parts = ['<base href="' + location.origin + location.pathname + '">'];
    try {
      var links = document.querySelectorAll('link[rel~="stylesheet"][href]');
      for (var i = 0; i < links.length; i++) {
        parts.push('<link rel="stylesheet" href="' + links[i].href + '">');
      }
      var styles = document.querySelectorAll('style');
      for (var j = 0; j < styles.length; j++) {
        // Skip our own injected panels/overlays.
        if (styles[j].closest && styles[j].closest('#__devtool-design-panel')) continue;
        parts.push('<style>' + styles[j].textContent + '</style>');
      }
    } catch (e) { /* best-effort */ }
    return parts.join('\n');
  }

  // renderInFrame renders alternative HTML inside an <iframe srcdoc> that carries
  // the page's stylesheets — true isolation from React/HMR AND the site's look.
  // The frame background/color follow the page body, so dark-themed apps don't
  // render their previews as dark text on a forced white sheet.
  function renderInFrame(html, heightPx) {
    var bg = '#fff', fg = '';
    try {
      var bodyCS = window.getComputedStyle(document.body);
      if (bodyCS.backgroundColor && bodyCS.backgroundColor !== 'rgba(0, 0, 0, 0)' && bodyCS.backgroundColor !== 'transparent') {
        bg = bodyCS.backgroundColor;
      }
      fg = bodyCS.color || '';
    } catch (e) { /* defaults */ }
    var f = document.createElement('iframe');
    f.className = 'frender';
    f.setAttribute('sandbox', 'allow-same-origin');
    f.style.cssText = 'width:100%;border:0;display:block;background:' + bg + ';' +
      (heightPx ? 'height:' + heightPx + 'px;' : 'height:100%;');
    f.srcdoc = '<!doctype html><html><head><meta charset="utf-8">' +
      collectPageCSS() +
      '<style>html,body{margin:0;padding:0;background:' + bg + (fg ? ';color:' + fg : '') + '}</style>' +
      '</head><body>' + (html || '') + '</body></html>';
    return f;
  }

  // captureSegment rasterises the live selected element (with its real CSS) to a
  // PNG data URL via html2canvas, so the agent message can carry a screenshot of
  // exactly what the user is redesigning. Fully guarded; yields null on failure.
  function captureSegment(cb) {
    var t = visibleTarget();
    if (!t) { cb(null); return; }
    // html2canvas is loaded on demand; ensure it before rasterising.
    if (typeof window.html2canvas !== 'function') {
      if (typeof window.__devtool_ensureHtml2canvas === 'function') {
        window.__devtool_ensureHtml2canvas().then(function () { captureSegment(cb); }, function () { cb(null); });
      } else {
        cb(null);
      }
      return;
    }
    try {
      var r = t.getBoundingClientRect();
      window.html2canvas(t, {
        allowTaint: true, useCORS: true, logging: false,
        backgroundColor: null,
        width: Math.ceil(r.width), height: Math.ceil(r.height),
        scale: Math.min(2, window.devicePixelRatio || 1)
      }).then(function (canvas) {
        try { cb(canvas.toDataURL('image/png')); } catch (e) { cb(null); }
      }).catch(function () { cb(null); });
    } catch (e) { cb(null); }
  }

  // captureSlot records the geometry the alternative must fit: the parent
  // container's box and its layout mode (grid tracks / flex direction / gap).
  // Without it, alternatives are generated for a vacuum.
  function captureSlot(element) {
    try {
      var parent = element && element.parentElement;
      if (!parent) return null;
      var cs = window.getComputedStyle(parent);
      var slot = {
        parentRect: { width: parent.offsetWidth, height: parent.offsetHeight },
        display: cs.display,
        gap: cs.gap !== 'normal' ? cs.gap : undefined
      };
      if (cs.display.indexOf('grid') !== -1) {
        slot.gridColumns = cs.gridTemplateColumns;
      } else if (cs.display.indexOf('flex') !== -1) {
        slot.flexDirection = cs.flexDirection;
        slot.flexWrap = cs.flexWrap;
      }
      return slot;
    } catch (e) { return null; }
  }

  // captureExemplars finds same-signature components elsewhere on the page —
  // same tag, at least one shared class — and returns up to two truncated
  // outerHTML snippets. Real sibling components anchor variation in the
  // site's own grammar better than tokens alone.
  function captureExemplars(element) {
    try {
      if (!element || !element.tagName) return null;
      var tag = element.tagName.toLowerCase();
      var classes = Array.from(element.classList || []);
      var out = [];
      var all = document.querySelectorAll(tag);
      for (var i = 0; i < all.length && out.length < 2; i++) {
        var el = all[i];
        if (el === element || element.contains(el) || el.contains(element)) continue;
        var shared = classes.some(function (c) { return el.classList.contains(c); });
        if (!shared) continue;
        var html = el.outerHTML;
        if (html && html.length > 20) out.push(html.slice(0, 800));
      }
      return out.length ? out : null;
    } catch (e) { return null; }
  }

  // buildConstraints makes the generation contract explicit: which axes are
  // preserved from the site's scheme, which axes the alternatives should vary
  // on (UX over core UI), and the user's own words as the highest-precedence
  // steer. null when nothing is selected.
  function buildConstraints() {
    if (!state.selectedElement) return null;
    var preserve = [];
    if (state.scheme) {
      ['palette', 'fontFamilies', 'fontSizes', 'spacing', 'radius', 'shadows'].forEach(function (k) {
        if (state.scheme[k]) preserve.push(k);
      });
    }
    var steer = null;
    for (var i = state.chatHistory.length - 1; i >= 0; i--) {
      if (state.chatHistory[i].role === 'user') { steer = state.chatHistory[i].message; break; }
    }
    return {
      preserve: preserve,
      vary: ['layout', 'hierarchy', 'density', 'affordance'],
      steer: steer
    };
  }

  function ensurePanel() {
    if (state.panel && state.panel.host && state.panel.host.isConnected) return state.panel;

    var host = document.createElement('div');
    host.id = '__devtool-design-panel';
    host.style.cssText = 'all:initial';
    var root = host.attachShadow ? host.attachShadow({ mode: 'open' }) : host;
    // Mount into the devtool shadow root when available; the host carries its
    // own nested shadow root for the dock styles either way.
    var hostMount = window.__devtoolGetMountRoot ? window.__devtoolGetMountRoot() : document.body;
    hostMount.appendChild(host);

    var style = document.createElement('style');
    style.textContent = [
      ':host{all:initial}',
      '.dock{position:fixed;right:16px;bottom:16px;width:min(720px,46vw);max-height:78vh;background:' + designTheme.surface + ';border:1px solid ' + designTheme.border + ';border-radius:12px;box-shadow:0 10px 40px rgba(0,0,0,.18);display:flex;flex-direction:column;overflow:hidden;container-type:inline-size;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}',
      '.bar{display:flex;flex-wrap:wrap;align-items:center;gap:8px;padding:10px 12px;border-bottom:1px solid ' + designTheme.border + '}',
      '.idx{font-size:12px;color:' + designTheme.textMuted + ';flex:0 1 auto;min-width:48px;max-width:150px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}',
      '.spacer{flex:1}',
      // Container-responsive: when the dock is narrow the chat input drops to
      // its own full-width row so the nav buttons + close (✕) always stay
      // visible instead of overflowing and being clipped by dock overflow.
      '@container (max-width:520px){.in{flex:1 1 100%;order:10}.spacer{display:none}}',
      '.btn{padding:6px 10px;background:' + designTheme.primary + ';color:' + designTheme.textInverse + ';border:none;border-radius:6px;font-size:12px;font-weight:600;cursor:pointer}',
      '.btn:hover{background:' + designTheme.primaryDark + '}',
      '.btn.ghost{background:' + designTheme.surfaceAlt + ';color:' + designTheme.text + '}',
      '.btn.ghost.on{background:' + designTheme.primary + ';color:' + designTheme.textInverse + '}',
      '.in{flex:1;min-width:120px;border:1px solid ' + designTheme.border + ';border-radius:8px;padding:7px 10px;font-size:13px;color:' + designTheme.text + ';background:' + designTheme.surface + ';outline:none}',
      '.body{display:flex;overflow:auto}',
      '.col{flex:1;min-width:0;padding:12px;overflow:auto}',
      '.col+.col{border-left:1px solid ' + designTheme.border + '}',
      '.cap{font-size:11px;text-transform:uppercase;letter-spacing:.04em;color:#94a3b8;margin:0 0 8px}',
      '.render{font-size:14px;color:' + designTheme.text + '}',
      '.frender{width:100%;border:0;display:block;background:' + designTheme.surface + ';border-radius:0 0 12px 12px}'
    ].join('');
    root.appendChild(style);

    var dock = document.createElement('div'); dock.className = 'dock';
    dock.setAttribute('role', 'dialog');
    dock.setAttribute('aria-modal', 'false');
    dock.setAttribute('aria-label', 'Design iteration panel');
    var bar = document.createElement('div'); bar.className = 'bar';
    var idx = document.createElement('div'); idx.className = 'idx';
    var inBtn = mkBtn('In place', function () { setMode('inplace'); }); inBtn.className = 'btn ghost';
    var sideBtn = mkBtn('Side-by-side', function () { setMode('side'); }); sideBtn.className = 'btn ghost';
    var overBtn = mkBtn('Overlay', function () { setMode('overlay'); }); overBtn.className = 'btn ghost';
    var spacer = document.createElement('div'); spacer.className = 'spacer';
    var chatIn = document.createElement('input'); chatIn.className = 'in'; chatIn.type = 'text';
    chatIn.placeholder = 'Describe changes…';
    chatIn.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && chatIn.value.trim()) { chat(chatIn.value.trim()); chatIn.value = ''; }
    });
    var prevB = mkBtn('◀', function () { previous(); });
    prevB.setAttribute('aria-label', 'Previous alternative');
    bar.appendChild(prevB);
    bar.appendChild(idx);
    var nextB = mkBtn('▶', function () { next(); });
    nextB.setAttribute('aria-label', 'Next alternative');
    bar.appendChild(nextB);
    bar.appendChild(inBtn);
    bar.appendChild(sideBtn);
    bar.appendChild(overBtn);
    bar.appendChild(spacer);
    bar.appendChild(chatIn);
    var closeB = mkBtn('✕', function () { stop(); }); closeB.className = 'btn ghost';
    closeB.setAttribute('aria-label', 'Close design panel');
    bar.appendChild(closeB);

    var body = document.createElement('div'); body.className = 'body';
    dock.appendChild(bar); dock.appendChild(body); root.appendChild(dock);

    // Overlay-on-top layer: its own shadow host so it can float anywhere over
    // the page, isolated from app CSS.
    var olHost = document.createElement('div'); olHost.id = '__devtool-design-overlay-preview';
    olHost.style.cssText = 'all:initial';
    var olRoot = olHost.attachShadow ? olHost.attachShadow({ mode: 'open' }) : olHost;
    var olStyle = document.createElement('style');
    olStyle.textContent = ':host{all:initial}.ol{position:fixed;z-index:' + designZ.panel + ';overflow:auto;background:' + designTheme.surface + ';border:2px solid ' + designTheme.primary + ';border-radius:8px;box-shadow:0 8px 30px rgba(0,0,0,.25)}.render{padding:8px;font-family:-apple-system,sans-serif}';
    olRoot.appendChild(olStyle);
    var olBox = document.createElement('div'); olBox.className = 'ol'; olBox.style.display = 'none';
    olRoot.appendChild(olBox);
    hostMount.appendChild(olHost);

    state.panel = { host: host, root: root, dock: dock, idx: idx, body: body, inBtn: inBtn, sideBtn: sideBtn, overBtn: overBtn, olHost: olHost, olBox: olBox };

    // Overlay mode follows the target; side mode docks beside the target. Both
    // must track the target as the page scrolls/reflows.
    state.reposition = function () {
      if (state.previewMode === 'overlay') positionOverlay();
      else positionSide();
    };
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
    p.idx.title = badgeFor(i);
    p.inBtn.className = 'btn ghost' + (state.previewMode === 'inplace' ? ' on' : '');
    p.sideBtn.className = 'btn ghost' + (state.previewMode === 'side' ? ' on' : '');
    p.overBtn.className = 'btn ghost' + (state.previewMode === 'overlay' ? ' on' : '');
    var altHTML = state.alternatives[i] != null ? state.alternatives[i] : '';
    var altStyle = state.altMeta && state.altMeta[i] && state.altMeta[i].style;
    var frameHTML = altStyle ? '<div style="' + altStyle.replace(/"/g, '&quot;') + '">' + altHTML + '</div>' : altHTML;

    if (state.previewMode === 'inplace') {
      // The alternative replaces the element in the page itself, so it is
      // judged against the real neighbours, not in a detached frame.
      p.olBox.style.display = 'none';
      p.body.innerHTML = '';
      p.body.style.padding = ''; p.body.style.overflow = 'hidden';
      swapInPlace(i);
      positionSide();
      return;
    }
    removeSwap();

    if (state.previewMode === 'overlay') {
      // Restore the default docked corner — side mode moves the dock inline.
      if (p.dock) {
        p.dock.style.left = ''; p.dock.style.top = '';
        p.dock.style.right = '16px'; p.dock.style.bottom = '16px';
        p.dock.style.width = 'min(720px,46vw)';
      }
      p.body.style.padding = ''; p.body.style.overflow = 'auto';
      p.body.innerHTML = '';
      var oc = document.createElement('div'); oc.className = 'col';
      var ocap = document.createElement('p'); ocap.className = 'cap';
      ocap.textContent = 'Overlay on target — switch to Side-by-side to compare';
      oc.appendChild(ocap); p.body.appendChild(oc);
      // Render the redesign in an iframe carrying the page stylesheets so the
      // overlay matches the site's look (was a bare unstyled fragment). Sized to
      // the target so the superimposed preview lines up with what it covers.
      var ot = resolveTarget();
      var oh = ot ? Math.max(80, Math.round(ot.getBoundingClientRect().height)) : 0;
      p.olBox.innerHTML = '';
      p.olBox.appendChild(renderInFrame(frameHTML, oh || null));
      p.olBox.style.display = 'block';
      positionOverlay();
    } else {
      // Side-by-side = redesign panel docked next to the REAL on-page element.
      // The original keeps its real site CSS in place; we never copy it into the
      // panel. The redesign renders in an iframe carrying the page stylesheets so
      // it matches the site's look, and the panel is sized to the segment.
      p.olBox.style.display = 'none';
      p.body.innerHTML = '';
      p.body.style.padding = '0';
      p.body.style.overflow = 'hidden';
      var t = resolveTarget();
      var h = t ? Math.max(120, Math.round(t.getBoundingClientRect().height)) : 0;
      p.body.appendChild(renderInFrame(frameHTML, h || null));
      positionSide();
    }
  }

  // positionSide docks the redesign panel beside the live target element, sized
  // to the target's dimensions. Prefers the right gutter; falls back below the
  // element, then to a fixed bottom-right corner when no target resolves.
  function positionSide() {
    var p = state.panel;
    if (!p || !p.dock) return;
    var dock = p.dock;
    var t = visibleTarget();
    if (!t) {
      dock.style.left = ''; dock.style.top = '';
      dock.style.right = '16px'; dock.style.bottom = '16px';
      dock.style.width = 'min(720px,46vw)';
      return;
    }
    var r = t.getBoundingClientRect();
    var vw = window.innerWidth, vh = window.innerHeight, gap = 12;
    var w = Math.min(Math.max(280, r.width), vw - 16);
    dock.style.right = ''; dock.style.bottom = '';
    if (vw - r.right >= w + gap) {
      dock.style.left = (r.right + gap) + 'px';
      dock.style.top = Math.max(8, Math.min(r.top, vh - 140)) + 'px';
    } else if (r.left >= w + gap) {
      dock.style.left = (r.left - gap - w) + 'px';
      dock.style.top = Math.max(8, Math.min(r.top, vh - 140)) + 'px';
    } else {
      dock.style.left = Math.max(8, Math.min(r.left, vw - w - 8)) + 'px';
      dock.style.top = (r.bottom + gap) + 'px';
    }
    dock.style.width = w + 'px';
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
  var MODE_KEY = '__devtool_design:preview-mode';
  function persistMode() { try { sessionStorage.setItem(MODE_KEY, state.previewMode); } catch (e) {} }
  function restorePersisted() {
    try {
      var mode = sessionStorage.getItem(MODE_KEY);
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
    removeSwap();
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

  // Select an alternative for preview. Never writes into the framework-owned
  // target subtree (React reconciliation / HMR would wipe it and the stored
  // selector would go stale); renderPreview swaps or frames it instead.
  function applyAlternative(index) {
    if (typeof index !== 'number' || isNaN(index) || index < 0 || index >= state.alternatives.length) return;
    state.currentIndex = index;
    persistAlternatives();
    renderPreview();
  }

  // Add one alternative. Storing NEVER depends on a live target node — mid-HMR
  // the node may be briefly absent, and a store that touches the DOM would throw
  // and lose the alternative. We store + persist first, then best-effort render.
  // meta = {label:'variation', note:'<direction>', style?:'<wrapper css>'}.
  // Off-scheme colours or fonts are refused with the clashes listed, so the
  // agent can correct and resend.
  function addAlternative(html, meta) {
    if (typeof html !== 'string') return { error: 'html must be a string' };
    var clashes = findSchemeClashes(html, meta && meta.style);
    if (clashes.length) return rejectClash(clashes);
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
    var added = 0, rejected = [];
    for (var i = 0; i < items.length; i++) {
      var it = items[i] || {};
      if (typeof it.html !== 'string') { rejected.push({ index: i, error: 'html must be a string' }); continue; }
      var clashes = findSchemeClashes(it.html, it.style);
      if (clashes.length) { rejected.push({ index: i, clashes: clashes }); continue; }
      state.alternatives.push(it.html);
      if (!state.altMeta) state.altMeta = [];
      state.altMeta[state.alternatives.length - 1] = { label: it.label || 'variation', note: it.note || '', style: it.style || '' };
      added++;
    }
    if (rejected.length) {
      notify(rejected.length + ' agent option(s) rejected — colours or fonts off the page scheme', 'warning');
    }
    if (added > 0) {
      state.currentIndex = state.alternatives.length - 1;
      persistAlternatives();
      if (state.panel) renderPreview();
    }
    var res = { success: added > 0, added: added, total: state.alternatives.length };
    if (rejected.length) { res.rejected = rejected; res.hint = CLASH_HINT; }
    return res;
  }

  // Request new alternatives from agent. Captures a screenshot of the live
  // segment first (async, best-effort) and includes it so the agent sees what it
  // is redesigning. A passed-in shot is reused to avoid a double capture when
  // chat() already grabbed one.
  function requestAlternatives(shot) {
    var core = getCore();
    if (!core || !core.send) {
      console.error('[Design] Core not available');
      return;
    }
    var emit = function (screenshot) {
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
        scheme: state.scheme || undefined,
        slot: state.slot || undefined,
        exemplars: state.exemplars || undefined,
        constraints: buildConstraints() || undefined,
        screenshot: screenshot || undefined
      });
    };
    if (shot !== undefined) { emit(shot); return; }
    captureSegment(emit);
  }

  // Extract a compact design-token scheme from the live DOM so generated
  // alternatives match the proxied app's look, plus every distinct colour seen
  // (the clash check's allow-list). Bounded element sample, fully guarded — any
  // failure yields {scheme: null, colors: []} rather than blocking design mode.
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
      // The element's own subtree: its colours are on the page by definition.
      var own = element.querySelectorAll('*');
      for (var o = 0; o < own.length && o < 80; o++) nodes.push(own[o]);
      var all = document.querySelectorAll('*');
      var cap = Math.min(all.length, 400);
      var stride = Math.max(1, Math.floor(all.length / cap));
      for (var i = 0; i < all.length && nodes.length < 500; i += stride) { nodes.push(all[i]); }

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

      var colors = Object.keys(palette).map(parseColor);
      Object.keys(scheme.cssVars).forEach(function (k) { colors.push(parseColor(scheme.cssVars[k])); });
      colors = colors.filter(Boolean);

      // Drop empties; return null if nothing usable so the agent prompt omits
      // the scheme block entirely.
      var hasAny = false;
      Object.keys(scheme).forEach(function (k) {
        var v = scheme[k];
        if (Array.isArray(v) && v.length === 0) { delete scheme[k]; }
        else if (k === 'cssVars' && (!v || Object.keys(v).length === 0)) { delete scheme[k]; }
        else { hasAny = true; }
      });
      return { scheme: hasAny ? scheme : null, colors: colors };
    } catch (e) {
      return { scheme: null, colors: [] };
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


  // ── Colour maths ────────────────────────────────────────────────────────────
  // parseColor reads hex, rgb()/rgba() and hsl()/hsla() (comma or space syntax)
  // into {r,g,b,a}. Anything else — names, var(), color-mix() — yields null.
  function parseColor(str) {
    var s = String(str || '').trim().toLowerCase(), m;
    if ((m = /^#([0-9a-f]{3,8})$/.exec(s))) {
      var h = m[1];
      if (h.length === 3 || h.length === 4) h = h.replace(/./g, '$&$&');
      if (h.length !== 6 && h.length !== 8) return null;
      return {
        r: parseInt(h.slice(0, 2), 16), g: parseInt(h.slice(2, 4), 16), b: parseInt(h.slice(4, 6), 16),
        a: h.length === 8 ? parseInt(h.slice(6, 8), 16) / 255 : 1
      };
    }
    if (!(m = /^(rgba?|hsla?)\(([^()]*)\)$/.exec(s))) return null;
    var parts = m[2].split(/[\s,\/]+/).filter(Boolean);
    if (parts.length < 3) return null;
    var frac = function (v, scale) { return v.slice(-1) === '%' ? parseFloat(v) / 100 * scale : parseFloat(v); };
    var a = parts.length > 3 ? frac(parts[3], 1) : 1;
    var c;
    if (m[1].charAt(0) === 'r') {
      c = { r: frac(parts[0], 255), g: frac(parts[1], 255), b: frac(parts[2], 255), a: a };
    } else {
      c = hslToRgb(parseFloat(parts[0]), parseFloat(parts[1]) / 100, parseFloat(parts[2]) / 100, a);
    }
    return (isNaN(c.r) || isNaN(c.g) || isNaN(c.b) || isNaN(c.a)) ? null : c;
  }

  function hslToRgb(h, s, l, a) {
    h = ((h % 360) + 360) % 360 / 360;
    var q = l < 0.5 ? l * (1 + s) : l + s - l * s, p = 2 * l - q;
    var ch = function (t) {
      if (t < 0) t += 1; if (t > 1) t -= 1;
      if (t < 1 / 6) return p + (q - p) * 6 * t;
      if (t < 1 / 2) return q;
      if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
      return p;
    };
    return { r: ch(h + 1 / 3) * 255, g: ch(h) * 255, b: ch(h - 1 / 3) * 255, a: a };
  }

  function colorDistance(x, y) {
    var dr = x.r - y.r, dg = x.g - y.g, db = x.b - y.b;
    return Math.sqrt(dr * dr + dg * dg + db * db);
  }

  function luminance(c) {
    var lin = function (v) { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
    return 0.2126 * lin(c.r) + 0.7152 * lin(c.g) + 0.0722 * lin(c.b);
  }

  function contrastRatio(x, y) {
    var a = luminance(x), b = luminance(y);
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  }

  function saturation(c) {
    var max = Math.max(c.r, c.g, c.b), min = Math.min(c.r, c.g, c.b);
    return max === 0 ? 0 : (max - min) / max;
  }

  // ── Scheme clash check ──────────────────────────────────────────────────────
  // An alternative may only use colours the page already renders (within
  // COLOR_TOLERANCE, so rounding and alpha variants pass) and font families the
  // page already loads. Literals are read from style attributes, <style> blocks
  // and SVG paint attributes; var(--…) and keywords always pass.
  var COLOR_TOLERANCE = 24;
  var GENERIC_FONTS = ['inherit', 'initial', 'unset', 'serif', 'sans-serif', 'monospace', 'system-ui',
    'ui-sans-serif', 'ui-serif', 'ui-monospace', 'cursive', 'fantasy', '-apple-system'];
  var CLASH_HINT = 'Use only the page palette, its CSS variables (var(--…)) and its font families; ' +
    'prefer the classes the page already uses.';

  function styleContexts(text) {
    var out = [], m;
    var attr = /\b(?:style|fill|stroke|color|bgcolor|stop-color)\s*=\s*(?:"([^"]*)"|'([^']*)')/gi;
    while ((m = attr.exec(text))) out.push(m[1] != null ? m[1] : m[2]);
    var block = /<style[^>]*>([\s\S]*?)<\/style>/gi;
    while ((m = block.exec(text))) out.push(m[1]);
    return out;
  }

  // findSchemeClashes returns a human-readable entry per off-scheme literal in
  // the alternative's markup (plus its wrapper style). Empty when the page gave
  // us nothing to judge against.
  function findSchemeClashes(markup, wrapperStyle) {
    var colors = state.pageColors || [];
    if (!colors.length) return [];
    var css = styleContexts(markup);
    if (wrapperStyle) css.push(wrapperStyle);
    css = css.join(';');
    var clashes = [], seen = {}, m;
    var lit = /#[0-9a-f]{3,8}\b|(?:rgba?|hsla?)\([^()]*\)/gi;
    while ((m = lit.exec(css))) {
      var c = parseColor(m[0]);
      if (!c || c.a === 0 || seen[m[0]]) continue;
      seen[m[0]] = true;
      var near = colors.some(function (p) { return colorDistance(p, c) <= COLOR_TOLERANCE; });
      if (!near) clashes.push('colour ' + m[0]);
    }
    var fams = ((state.scheme && state.scheme.fontFamilies) || []).join(',').toLowerCase();
    if (fams) {
      var ff = /font-family\s*:\s*([^;}"]+)/gi;
      while ((m = ff.exec(css))) {
        var first = m[1].split(',')[0].trim().replace(/^['"]|['"]$/g, '').toLowerCase();
        if (!first || first.indexOf('var(') === 0 || GENERIC_FONTS.indexOf(first) !== -1) continue;
        if (fams.indexOf(first) === -1 && !seen['f:' + first]) {
          seen['f:' + first] = true;
          clashes.push('font ' + first);
        }
      }
    }
    return clashes;
  }

  function rejectClash(clashes) {
    notify('Agent option rejected — off the page scheme: ' + clashes.slice(0, 3).join(', '), 'warning');
    return { error: 'off-scheme', clashes: clashes, hint: CLASH_HINT };
  }

  // ── Instant variants ────────────────────────────────────────────────────────
  // buildInstantVariants restyles the element's own content with values taken
  // only from the page's scheme, so they cannot clash and need no agent round
  // trip. Each variant is skipped when the page lacks the token it needs.
  // Returns [{html, style, note}].
  function buildInstantVariants(element, scheme) {
    if (!scheme) return [];
    var out = [];
    try {
      var cs = window.getComputedStyle(element);
      var inner = element.innerHTML;
      var px = function (list) {
        var seen = {}, vals = [];
        (list || []).forEach(function (v) {
          var n = parseFloat(v);
          if (/px$/.test(v) && n > 0 && !seen[n]) { seen[n] = true; vals.push(n); }
        });
        return vals.sort(function (a, b) { return a - b; });
      };
      var steps = px(scheme.spacing);
      var pad = parseFloat(cs.paddingTop) || 0;
      var smaller = steps.filter(function (v) { return v < pad; }).pop();
      var larger = steps.filter(function (v) { return v > pad; })[0];
      if (smaller != null) {
        out.push({ html: inner, style: 'padding:' + smaller + 'px', note: 'Compact — ' + smaller + 'px padding from the page spacing scale' });
      }
      if (larger != null) {
        out.push({ html: inner, style: 'padding:' + larger + 'px', note: 'Spacious — ' + larger + 'px padding from the page spacing scale' });
      }

      var radius = (scheme.radius || []).filter(function (r) { return r !== cs.borderTopLeftRadius; })[0];
      var palette = (scheme.palette || []).map(function (v) { return { css: v, c: parseColor(v) }; })
        .filter(function (p) { return p.c && p.c.a >= 0.9; });
      var accent = palette.filter(function (p) { return saturation(p.c) >= 0.3; })[0];
      var bg = parseColor(cs.backgroundColor);
      if (accent && !(bg && colorDistance(bg, accent.c) <= COLOR_TOLERANCE)) {
        var ink = palette.slice().sort(function (a, b) {
          return contrastRatio(b.c, accent.c) - contrastRatio(a.c, accent.c);
        })[0];
        if (ink && contrastRatio(ink.c, accent.c) >= 4.5) {
          out.push({
            html: inner,
            style: 'background-color:' + accent.css + ';color:' + ink.css + (radius ? ';border-radius:' + radius : ''),
            note: 'Emphasis — page accent ' + accent.css + ' with its highest-contrast page colour'
          });
        }
      }

      var shadow = (scheme.shadows || []).filter(function (v) { return v !== cs.boxShadow; })[0];
      if (shadow) {
        out.push({
          html: inner,
          style: 'box-shadow:' + shadow + (radius ? ';border-radius:' + radius : ''),
          note: 'Elevated — the page’s own shadow' + (radius ? ' and ' + radius + ' radius' : '')
        });
      }
    } catch (e) { /* computed style unavailable — no instant options */ }
    return out;
  }

  // ── In-place swap ───────────────────────────────────────────────────────────
  // The alternative goes into the page where the element is: a shallow clone of
  // the target (same tag, classes and attributes, so the page's own selectors
  // style it) is inserted beside it and the target is hidden by attribute.
  // The framework-owned node is never rewritten, so React/HMR reconciliation
  // cannot wipe it; if a re-render drops the clone, the observer puts it back.
  var HIDDEN_ATTR = 'data-devtool-design-hidden';
  var SWAP_ATTR = 'data-devtool-design-swap';

  function ensureHideRule() {
    if (document.getElementById('__devtool-design-hide')) return;
    var st = document.createElement('style');
    st.id = '__devtool-design-hide';
    st.textContent = '[' + HIDDEN_ATTR + ']{display:none!important}';
    (document.head || document.documentElement).appendChild(st);
  }

  function swapInPlace(index) {
    removeSwap();
    if (index === 0) return; // the original is the page as it is
    var target = resolveTarget();
    if (!target || !target.parentNode) return;
    ensureHideRule();
    var clone = target.cloneNode(false);
    clone.removeAttribute('id');
    var os = window.__devtool_override_store;
    if (os && os.OID_ATTR) clone.removeAttribute(os.OID_ATTR);
    clone.setAttribute(SWAP_ATTR, String(index));
    var meta = state.altMeta && state.altMeta[index];
    if (meta && meta.style) clone.setAttribute('style', (clone.getAttribute('style') || '') + ';' + meta.style);
    clone.innerHTML = state.alternatives[index] || '';
    var parent = target.parentNode;
    parent.insertBefore(clone, target.nextSibling);
    target.setAttribute(HIDDEN_ATTR, '');
    var observer = new MutationObserver(function () {
      if (clone.isConnected && target.isConnected) return;
      if (state.swap && state.swap.clone === clone && state.previewMode === 'inplace') {
        swapInPlace(state.currentIndex);
      }
    });
    observer.observe(parent, { childList: true });
    state.swap = { target: target, clone: clone, observer: observer };
  }

  function removeSwap() {
    var sw = state.swap;
    if (!sw) return;
    state.swap = null;
    sw.observer.disconnect();
    if (sw.clone.parentNode) sw.clone.parentNode.removeChild(sw.clone);
    sw.target.removeAttribute(HIDDEN_ATTR);
  }

  // visibleTarget is what the user currently sees in the element's slot: the
  // swapped-in alternative while one is shown, else the element itself.
  function visibleTarget() {
    return (state.swap && state.swap.clone.isConnected) ? state.swap.clone : resolveTarget();
  }

  // Send design state to agent
  function sendDesignState(instant) {
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
      slot: state.slot || undefined,
      exemplars: state.exemplars || undefined,
      instantVariants: (instant || []).map(function (v) { return v.note; }),
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

    // One screenshot for both the chat note and the follow-on request.
    captureSegment(function (shot) {
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
        slot: state.slot || undefined,
        constraints: buildConstraints() || undefined,
        url: window.location.href,
        screenshot: shot || undefined
      });

      // Request alternatives based on chat (reuse the same screenshot).
      requestAlternatives(shot);
    });
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
    state.slot = null;
    state.exemplars = null;
    state.pageColors = null;
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
    getState: getState,
    importVariantSet: importVariantSet,
    exportVariantSet: exportVariantSet,
    applyVariant: applyVariant,
    revertVariant: revertVariant
  };
})();

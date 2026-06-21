// Mini Floating Context Palette
// Compact, context-aware floating palette that appears adjacent to the
// selected element. Shows controls relevant to the element type (text /
// image / button / block) and mutates live styles via window.__devtool
// primitives. Additive to the inspect panel — never replaces it.
//
// Selection feed: listens for a 'devtool:palette-show' CustomEvent on
// document. design.js dispatches that event from selectElement() so the
// palette is decoupled from the design module's internals. The palette
// can also be invoked directly via window.__devtool_palette.show(el).
//
// Sizing: max ~240px wide, single row of icons + scrubbers. Positioned
// 8px to the right of the element's bbox; falls back to below when there
// is no horizontal room. Auto-repositions on scroll/resize. Draggable.
// Dismiss with Escape or click outside; re-shows on next selection.

(function() {
  'use strict';

  // PALETTE_WIDTH must match the inline width set in show() — used by the
  // positioning math (rect.right + GAP + PALETTE_WIDTH > viewport check)
  // and by the drag clamp. Keep in sync with paletteEl.style.width below.
  var PALETTE_WIDTH = 240;
  var PALETTE_HEIGHT_GUESS = 44; // single row; refined after mount via offsetHeight
  var GAP = 8;

  // State
  var state = {
    paletteEl: null,
    selectedElement: null,
    elementType: null,
    dragging: false,
    dragOffsetX: 0,
    dragOffsetY: 0,
    userMoved: false,        // true once the user has dragged; suppresses auto-reposition
    userPos: null,           // {x,y} after drag
    repositionPending: false,
    escapeHandler: null,
    outsideClickHandler: null,
    scrollResizeHandler: null,
    selectionListener: null
  };

  // ---------------------------------------------------------------------------
  // Element classification
  //
  // classifyElement returns one of: 'text' | 'image' | 'button' | 'block'.
  // Order matters — image and button are checked before the text/heading
  // tag set so that <button>Hello</button> classifies as 'button' (not
  // 'text'), and <img alt="..."> classifies as 'image'.
  // ---------------------------------------------------------------------------
  function classifyElement(el) {
    if (!el || !el.tagName) return 'block';
    var tag = el.tagName.toLowerCase();

    // image: <img>, <picture>, <svg>
    if (tag === 'img' || tag === 'picture' || tag === 'svg') {
      return 'image';
    }

    // button/input: form controls
    if (tag === 'button' || tag === 'input' || tag === 'select' ||
        tag === 'textarea' || (el.getAttribute && el.getAttribute('role') === 'button')) {
      return 'button';
    }

    // text/heading: leaf-ish elements with text content
    var textTags = {
      h1: 1, h2: 1, h3: 1, h4: 1, h5: 1, h6: 1,
      p: 1, span: 1, a: 1, label: 1, li: 1,
      strong: 1, em: 1, small: 1, blockquote: 1,
      figcaption: 1, caption: 1, dt: 1, dd: 1
    };
    if (textTags[tag]) {
      return 'text';
    }

    // block: <div>, <section>, etc.
    return 'block';
  }

  // controlsForType returns the bank of control descriptors for a given
  // element type. Each control descriptor is rendered by buildControl().
  // The 'any' suffix bank (opacity / display / quick-delete) is appended
  // to every type — task spec calls these out as universal.
  //
  // Returning a fresh array per call lets tests assert on the exact shape
  // without mutating shared state.
  function controlsForType(type) {
    var base;
    switch (type) {
      case 'text':
        base = [
          { id: 'font-size', label: 'fs', kind: 'scrubber', prop: 'fontSize', unit: 'px', min: 8, max: 96, step: 1 },
          { id: 'font-weight-bold', label: 'B', kind: 'toggle', prop: 'fontWeight', onValue: 'bold', offValue: 'normal' },
          { id: 'font-style-italic', label: 'I', kind: 'toggle', prop: 'fontStyle', onValue: 'italic', offValue: 'normal' },
          { id: 'color', label: 'fg', kind: 'swatch', prop: 'color' },
          { id: 'line-height', label: 'lh', kind: 'scrubber', prop: 'lineHeight', unit: '', min: 0.8, max: 3, step: 0.1 }
        ];
        break;
      case 'image':
        base = [
          { id: 'width', label: 'w', kind: 'scrubber', prop: 'width', unit: 'px', min: 0, max: 2000, step: 1 },
          { id: 'height', label: 'h', kind: 'scrubber', prop: 'height', unit: 'px', min: 0, max: 2000, step: 1 },
          { id: 'object-fit', label: 'fit', kind: 'select', prop: 'objectFit', options: ['fill', 'contain', 'cover', 'none', 'scale-down'] }
        ];
        break;
      case 'button':
        base = [
          { id: 'padding', label: 'p', kind: 'scrubber', prop: 'padding', unit: 'px', min: 0, max: 64, step: 1 },
          { id: 'border-radius', label: 'r', kind: 'scrubber', prop: 'borderRadius', unit: 'px', min: 0, max: 32, step: 1 },
          { id: 'background-color', label: 'bg', kind: 'swatch', prop: 'backgroundColor' },
          { id: 'color', label: 'fg', kind: 'swatch', prop: 'color' }
        ];
        break;
      case 'block':
      default:
        base = [
          { id: 'width', label: 'w', kind: 'scrubber', prop: 'width', unit: 'px', min: 0, max: 2000, step: 1 },
          { id: 'height', label: 'h', kind: 'scrubber', prop: 'height', unit: 'px', min: 0, max: 2000, step: 1 },
          { id: 'padding', label: 'p', kind: 'scrubber', prop: 'padding', unit: 'px', min: 0, max: 64, step: 1 },
          { id: 'background-color', label: 'bg', kind: 'swatch', prop: 'backgroundColor' }
        ];
        break;
    }

    // Universal controls — appended to every type
    base.push({ id: 'opacity', label: 'op', kind: 'scrubber', prop: 'opacity', unit: '', min: 0, max: 1, step: 0.05 });
    base.push({ id: 'display', label: 'disp', kind: 'select', prop: 'display', options: ['block', 'inline', 'inline-block', 'flex', 'grid', 'none'] });
    base.push({ id: 'delete', label: 'x', kind: 'action', action: 'delete' });

    return base;
  }

  // ---------------------------------------------------------------------------
  // Positioning
  //
  // computePosition returns {x,y} for the palette top-left given the
  // selected element's bounding rect. Default placement: 8px to the right
  // of the rect. Falls back to below when right-of would overflow the
  // viewport. Always clamps to viewport bounds so the palette never
  // disappears off-screen on narrow viewports.
  // ---------------------------------------------------------------------------
  function computePosition(rect, viewportW, viewportH, paletteH) {
    var w = PALETTE_WIDTH;
    var h = paletteH || PALETTE_HEIGHT_GUESS;

    // Try right side: 8px gap from element right edge.
    var x = rect.right + GAP;
    var y = rect.top;

    if (x + w > viewportW) {
      // No horizontal room — fall back to below the element.
      x = rect.left;
      y = rect.bottom + GAP;

      // If still no room below either, place above. This is the rare
      // viewport-bottom + viewport-right corner case.
      if (y + h > viewportH) {
        y = Math.max(0, rect.top - h - GAP);
      }
    }

    // Final clamp to viewport bounds.
    x = Math.max(0, Math.min(x, viewportW - w));
    y = Math.max(0, Math.min(y, viewportH - h));

    return { x: x, y: y };
  }

  // ---------------------------------------------------------------------------
  // Shared design-state sync
  //
  // The palette and the full inspect panel (style-editor.js) share a single
  // logical selection + style-edit stream. Both surfaces dispatch the
  // 'devtool:design-state' CustomEvent on document on every mutation, and
  // both subscribe so changes in one surface mirror to the other within the
  // same render tick. Loop guard: each side tags detail.source ('palette'
  // or 'panel') and ignores events it emitted itself.
  //
  // Event detail shape:
  //   { source: 'palette'|'panel',
  //     kind:   'edit'|'deselect',
  //     selector?: string,                // best-effort element identifier
  //     change?: { property, value } }    // edit payload
  //
  // The palette also owns the cross-surface undo stack: Ctrl+Z (Cmd+Z on
  // macOS) pops the most recent edit and dispatches a revert as a normal
  // edit event so the panel reflects the rollback too.
  // ---------------------------------------------------------------------------
  // Wire-protocol literals (event name 'devtool:design-state' and source
  // tag 'palette') are kept inline at dispatch/subscribe sites for
  // grep-discoverability. Changing either requires a coordinated edit to
  // style-editor.js, which uses 'panel' as its source tag.

  // undoStack entries: { property, prevValue, element }. Cleared on hide()
  // because undoing an edit on a no-longer-selected element would silently
  // mutate page state the user can no longer see.
  var undoStack = [];

  // applyingRemote suppresses outbound dispatches while we apply an event
  // received from the other surface. Without this guard, applyStyle's
  // dispatch would echo back to the panel even though the panel originated
  // the change. The guard works alongside the source-tag filter — both are
  // needed because React-style controlled inputs can fire input events on
  // programmatic value changes.
  var applyingRemote = false;

  function dispatchDesignState(detail) {
    try {
      document.dispatchEvent(new CustomEvent('devtool:design-state', { detail: detail }));
    } catch (e) {
      // CustomEvent constructor unsupported in some legacy IE — degrade silently.
    }
  }

  // CSS property name conversion. The palette uses camelCase identifiers
  // (descriptor.prop = 'fontSize') because element.style[prop] accepts both
  // forms. The full inspect panel iterates getComputedStyle() which yields
  // kebab-case ('font-size'). The shared design-state event speaks
  // kebab-case (CSS standard) so both surfaces can match by string equality.
  function camelToKebab(name) {
    return name.replace(/[A-Z]/g, function(ch) { return '-' + ch.toLowerCase(); });
  }
  function kebabToCamel(name) {
    return name.replace(/-([a-z])/g, function(_, ch) { return ch.toUpperCase(); });
  }

  // currentSelector returns a best-effort identifier for the selected
  // element. We use generateSelector when utils is loaded; otherwise we
  // fall back to tag name. The selector is informational — both surfaces
  // already hold a direct element reference for matching.
  function currentSelector() {
    if (!state.selectedElement) return null;
    var utils = window.__devtool_utils;
    if (utils && typeof utils.generateSelector === 'function') {
      try {
        return utils.generateSelector(state.selectedElement);
      } catch (e) { /* fall through */ }
    }
    return state.selectedElement.tagName ? state.selectedElement.tagName.toLowerCase() : null;
  }

  // ---------------------------------------------------------------------------
  // Mutation primitives
  //
  // applyStyle writes one CSS property on the selected element. Centralising
  // the write keeps the wiring testable (single seam for verification) and
  // is the load-bearing seam for the cross-surface sync (every user-driven
  // change goes through here, so the dispatch is guaranteed to fire).
  // ---------------------------------------------------------------------------
  function applyStyle(prop, value) {
    if (!state.selectedElement) return;
    var prevValue = state.selectedElement.style[prop] || '';
    try {
      state.selectedElement.style[prop] = value;
    } catch (e) {
      // Some properties throw on invalid values — swallow so the palette
      // never breaks the page. console.error is enough; this is dev tooling.
      console.error('[Palette] applyStyle failed', prop, value, e);
      return;
    }
    if (!applyingRemote) {
      // Capture undo state before broadcast so a Ctrl+Z handler running
      // synchronously after dispatch sees the new top-of-stack entry.
      // Undo entries store the camelCase prop because the restore path
      // writes via element.style[prop] (same path as applyStyle).
      undoStack.push({ property: prop, prevValue: prevValue, element: state.selectedElement });
      dispatchDesignState({
        source: 'palette',
        kind: 'edit',
        selector: currentSelector(),
        change: { property: camelToKebab(prop), value: value }
      });
    }
  }

  function applyAction(action) {
    if (!state.selectedElement) return;
    if (action === 'delete') {
      var parent = state.selectedElement.parentNode;
      if (parent) {
        parent.removeChild(state.selectedElement);
      }
      // After delete the element no longer exists — hide the palette.
      hide();
    }
  }

  // ---------------------------------------------------------------------------
  // Control rendering
  //
  // buildControl returns a DOM node for a single control descriptor. Each
  // control is responsible for reading the current style value, presenting
  // an editor, and calling applyStyle on change.
  // ---------------------------------------------------------------------------
  function buildControl(descriptor, element) {
    var wrap = document.createElement('div');
    wrap.className = '__devtool-palette-ctl';
    wrap.dataset.paletteCtl = descriptor.id;
    wrap.style.cssText = [
      'display: inline-flex',
      'align-items: center',
      'gap: 2px',
      'padding: 2px 4px',
      'border-radius: 4px',
      'font-size: 11px',
      'font-family: ui-monospace, monospace',
      'color: #1e293b',
      'cursor: default',
      'user-select: none'
    ].join(';');

    var label = document.createElement('span');
    label.textContent = descriptor.label;
    label.style.cssText = 'color:#64748b;font-size:10px';
    wrap.appendChild(label);

    var input;
    var current = element && element.style ? element.style[descriptor.prop] : '';

    switch (descriptor.kind) {
      case 'scrubber':
        input = document.createElement('input');
        input.type = 'number';
        input.min = String(descriptor.min);
        input.max = String(descriptor.max);
        input.step = String(descriptor.step);
        // Strip unit when prepopulating from inline style ('16px' -> '16').
        var stripped = parseFloat(current);
        input.value = isNaN(stripped) ? '' : String(stripped);
        input.style.cssText = 'width:42px;border:1px solid #e2e8f0;border-radius:3px;padding:1px 3px;font-size:11px;font-family:inherit';
        input.addEventListener('input', function() {
          var v = input.value;
          if (v === '') {
            applyStyle(descriptor.prop, '');
            return;
          }
          applyStyle(descriptor.prop, descriptor.unit ? (v + descriptor.unit) : v);
        });
        wrap.appendChild(input);
        break;

      case 'toggle':
        input = document.createElement('button');
        input.type = 'button';
        input.textContent = descriptor.label;
        // Replace the label span with the button text so the toggle reads
        // as a single character (B / I) instead of label + button.
        wrap.removeChild(label);
        var isOn = current === descriptor.onValue;
        input.style.cssText = [
          'border:1px solid #e2e8f0',
          'border-radius:3px',
          'padding:1px 6px',
          'font-size:11px',
          'font-weight:600',
          'font-family:inherit',
          'cursor:pointer',
          'background:' + (isOn ? '#6366f1' : '#ffffff'),
          'color:' + (isOn ? '#ffffff' : '#1e293b')
        ].join(';');
        input.addEventListener('click', function() {
          var nowOn = state.selectedElement.style[descriptor.prop] === descriptor.onValue;
          var next = nowOn ? descriptor.offValue : descriptor.onValue;
          applyStyle(descriptor.prop, next);
          input.style.background = next === descriptor.onValue ? '#6366f1' : '#ffffff';
          input.style.color = next === descriptor.onValue ? '#ffffff' : '#1e293b';
        });
        wrap.appendChild(input);
        break;

      case 'swatch':
        input = document.createElement('input');
        input.type = 'color';
        // Color inputs require a hex value; computed style may be 'rgb(...)'.
        // Read inline first; if missing, leave default. Don't try to parse
        // computed colors — that's fragile and not required by the spec.
        if (current && /^#[0-9a-fA-F]{3,8}$/.test(current)) {
          input.value = current;
        }
        input.style.cssText = 'width:24px;height:18px;border:1px solid #e2e8f0;border-radius:3px;padding:0;cursor:pointer;background:transparent';
        input.addEventListener('input', function() {
          applyStyle(descriptor.prop, input.value);
        });
        wrap.appendChild(input);
        break;

      case 'select':
        input = document.createElement('select');
        input.style.cssText = 'border:1px solid #e2e8f0;border-radius:3px;padding:1px 2px;font-size:11px;font-family:inherit;background:#ffffff;color:#1e293b';
        descriptor.options.forEach(function(opt) {
          var option = document.createElement('option');
          option.value = opt;
          option.textContent = opt;
          if (opt === current) option.selected = true;
          input.appendChild(option);
        });
        input.addEventListener('change', function() {
          applyStyle(descriptor.prop, input.value);
        });
        wrap.appendChild(input);
        break;

      case 'action':
        input = document.createElement('button');
        input.type = 'button';
        input.textContent = descriptor.label;
        wrap.removeChild(label);
        input.style.cssText = 'border:1px solid #ef4444;color:#ef4444;background:#ffffff;border-radius:3px;padding:1px 6px;font-size:11px;font-weight:600;font-family:inherit;cursor:pointer';
        input.addEventListener('click', function() {
          applyAction(descriptor.action);
        });
        wrap.appendChild(input);
        break;
    }

    return wrap;
  }

  // ---------------------------------------------------------------------------
  // Public API: show / hide
  // ---------------------------------------------------------------------------
  function show(element) {
    if (!element || element.nodeType !== 1) return;

    // If already showing for a different element, tear down first so we
    // get a fresh control bank for the new element type.
    hide();

    state.selectedElement = element;
    state.elementType = classifyElement(element);
    state.userMoved = false;
    state.userPos = null;

    var palette = document.createElement('div');
    palette.id = '__devtool-palette';
    palette.dataset.devtoolPalette = 'true';
    palette.dataset.elementType = state.elementType;
    palette.style.cssText = [
      'position: fixed',
      'top: 0px',
      'left: 0px',
      'width: ' + PALETTE_WIDTH + 'px',
      'max-width: ' + PALETTE_WIDTH + 'px',
      'background: #ffffff',
      'border: 1px solid #e2e8f0',
      'border-radius: 8px',
      'padding: 4px 6px',
      'box-shadow: 0 4px 16px rgba(0,0,0,0.12)',
      'z-index: 2147483645',
      'display: flex',
      'flex-direction: row',
      'align-items: center',
      'gap: 4px',
      'overflow-x: auto',
      'overflow-y: hidden',
      'scrollbar-width: none',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif'
    ].join(';');

    // Drag handle — small grip on the left, doubles as element-type badge.
    var handle = document.createElement('div');
    handle.className = '__devtool-palette-handle';
    handle.textContent = '⋮⋮';
    handle.title = 'Drag to move • ' + state.elementType;
    handle.style.cssText = [
      'cursor: grab',
      'color: #94a3b8',
      'font-size: 10px',
      'padding: 0 2px',
      'user-select: none',
      'flex-shrink: 0'
    ].join(';');
    palette.appendChild(handle);

    // Build context-aware controls.
    var controls = controlsForType(state.elementType);
    controls.forEach(function(desc) {
      palette.appendChild(buildControl(desc, element));
    });

    document.body.appendChild(palette);
    state.paletteEl = palette;

    // Position. We measure offsetHeight after mount so the height-guess
    // doesn't matter for the rare top-of-viewport case.
    repositionToElement();

    // Wire dismiss handlers (Escape, click-outside) and live-reposition
    // (scroll, resize).
    attachHandlers(handle);

    // Notify the panel that selection has changed so it can re-target if
    // already open. Suppressed when applyingRemote is set (i.e. show() was
    // invoked from within an inbound design-state listener — currently no
    // such path exists, but the guard makes future composition safe).
    if (!applyingRemote) {
      dispatchDesignState({
        source: 'palette',
        kind: 'select',
        selector: currentSelector(),
        element: element
      });
    }

    return palette;
  }

  function hide() {
    var hadSelection = !!state.selectedElement;
    if (state.paletteEl && state.paletteEl.parentNode) {
      state.paletteEl.parentNode.removeChild(state.paletteEl);
    }
    state.paletteEl = null;
    state.selectedElement = null;
    state.elementType = null;
    // Drop the undo stack: undoing into an unselected element would mutate
    // page state with no visible surface, which breaks the spec invariant
    // that undo is observable across both surfaces.
    undoStack = [];
    detachHandlers();
    // Notify the panel only if we actually had a selection — hide() is
    // also called at the top of show() to tear down before re-mount, and
    // a no-op deselect would race with the subsequent edit event.
    if (hadSelection && !applyingRemote) {
      dispatchDesignState({ source: 'palette', kind: 'deselect' });
    }
  }

  // repositionToElement reads the selected element's bbox and writes the
  // palette's top/left. No-op when the user has dragged the palette.
  function repositionToElement() {
    if (!state.paletteEl || !state.selectedElement) return;
    if (state.userMoved && state.userPos) {
      state.paletteEl.style.left = state.userPos.x + 'px';
      state.paletteEl.style.top = state.userPos.y + 'px';
      return;
    }
    var rect = state.selectedElement.getBoundingClientRect();
    var pos = computePosition(
      rect,
      window.innerWidth,
      window.innerHeight,
      state.paletteEl.offsetHeight || PALETTE_HEIGHT_GUESS
    );
    state.paletteEl.style.left = pos.x + 'px';
    state.paletteEl.style.top = pos.y + 'px';
  }

  // attachHandlers wires Escape, outside-click, scroll/resize, and drag.
  // Handlers are stored on state so detachHandlers() can remove them
  // symmetrically without leaking listeners across show/hide cycles.
  function attachHandlers(handle) {
    state.escapeHandler = function(e) {
      if (e.key === 'Escape') hide();
    };
    document.addEventListener('keydown', state.escapeHandler);

    state.outsideClickHandler = function(e) {
      if (!state.paletteEl) return;
      // Click on palette itself: keep open.
      if (state.paletteEl.contains(e.target)) return;
      // Click on the selected element: also keep open. Re-selection of
      // a different element is handled by design.js dispatching another
      // palette-show event, which calls show() and tears the palette down.
      if (state.selectedElement && state.selectedElement.contains(e.target)) return;
      hide();
    };
    document.addEventListener('mousedown', state.outsideClickHandler);

    // Scroll/resize: rAF-coalesce so we don't thrash layout.
    state.scrollResizeHandler = function() {
      if (state.repositionPending) return;
      state.repositionPending = true;
      requestAnimationFrame(function() {
        state.repositionPending = false;
        repositionToElement();
      });
    };
    window.addEventListener('scroll', state.scrollResizeHandler, true);
    window.addEventListener('resize', state.scrollResizeHandler);

    // Drag from the handle.
    handle.addEventListener('mousedown', function(e) {
      e.preventDefault();
      state.dragging = true;
      var rect = state.paletteEl.getBoundingClientRect();
      state.dragOffsetX = e.clientX - rect.left;
      state.dragOffsetY = e.clientY - rect.top;
      handle.style.cursor = 'grabbing';

      // Cache size once at drag start; reading offsetWidth/offsetHeight on every
      // mousemove forces a synchronous layout after each style write (thrash).
      var w = state.paletteEl.offsetWidth || PALETTE_WIDTH;
      var h = state.paletteEl.offsetHeight || PALETTE_HEIGHT_GUESS;
      // Coalesce moves into one style write per frame to avoid jitter.
      var rafId = 0;
      var pendingPos = null;

      function applyPendingPos() {
        rafId = 0;
        if (!pendingPos) return;
        state.paletteEl.style.left = pendingPos.x + 'px';
        state.paletteEl.style.top = pendingPos.y + 'px';
        state.userMoved = true;
        state.userPos = pendingPos;
      }

      function onMove(ev) {
        if (!state.dragging) return;
        var x = ev.clientX - state.dragOffsetX;
        var y = ev.clientY - state.dragOffsetY;
        x = Math.max(0, Math.min(x, window.innerWidth - w));
        y = Math.max(0, Math.min(y, window.innerHeight - h));
        pendingPos = { x: x, y: y };
        if (rafId) return;
        rafId = requestAnimationFrame(applyPendingPos);
      }

      function onUp() {
        state.dragging = false;
        handle.style.cursor = 'grab';
        if (rafId) { cancelAnimationFrame(rafId); rafId = 0; }
        if (pendingPos) applyPendingPos();
        document.removeEventListener('mousemove', onMove);
        document.removeEventListener('mouseup', onUp);
      }

      document.addEventListener('mousemove', onMove);
      document.addEventListener('mouseup', onUp);
    });
  }

  function detachHandlers() {
    if (state.escapeHandler) {
      document.removeEventListener('keydown', state.escapeHandler);
      state.escapeHandler = null;
    }
    if (state.outsideClickHandler) {
      document.removeEventListener('mousedown', state.outsideClickHandler);
      state.outsideClickHandler = null;
    }
    if (state.scrollResizeHandler) {
      window.removeEventListener('scroll', state.scrollResizeHandler, true);
      window.removeEventListener('resize', state.scrollResizeHandler);
      state.scrollResizeHandler = null;
    }
  }

  // ---------------------------------------------------------------------------
  // Selection feed
  //
  // The palette listens for a 'devtool:palette-show' CustomEvent on the
  // document. design.js dispatches that event from selectElement() with
  // detail = { element }. This keeps the palette decoupled from design's
  // internal state — anything that wants to drive the palette can dispatch
  // the same event.
  // ---------------------------------------------------------------------------
  function attachSelectionListener() {
    if (state.selectionListener) return;
    state.selectionListener = function(e) {
      if (!e || !e.detail || !e.detail.element) return;
      show(e.detail.element);
    };
    document.addEventListener('devtool:palette-show', state.selectionListener);
  }

  // ---------------------------------------------------------------------------
  // Inbound design-state sync
  //
  // When the panel emits a style edit, we mirror it into the live palette
  // controls so the user sees the value update without re-fetching from the
  // DOM. The detail.source filter prevents echoes from our own dispatches.
  // applyingRemote suppresses the outbound dispatch we'd otherwise emit
  // from applyStyle, so two surfaces synced one way don't ricochet back.
  // ---------------------------------------------------------------------------
  function syncControlValue(property, value) {
    if (!state.paletteEl) return;
    // Find the descriptor whose `prop` matches the incoming property; that
    // is the only control that needs its rendered value patched.
    var ctls = state.paletteEl.querySelectorAll('[data-palette-ctl]');
    for (var i = 0; i < ctls.length; i++) {
      var input = ctls[i].querySelector('input, select, button');
      if (!input) continue;
      // Reverse-lookup: descriptor.prop is on the wrap via dataset.paletteCtl
      // → controlsForType id. We can't introspect descriptors back from DOM
      // directly, so instead we iterate the descriptor list and match by id.
      var descriptors = controlsForType(state.elementType);
      var descriptor = null;
      for (var di = 0; di < descriptors.length; di++) {
        if (descriptors[di].id === ctls[i].dataset.paletteCtl) {
          descriptor = descriptors[di];
          break;
        }
      }
      if (!descriptor || descriptor.prop !== property) continue;

      // Patch the input value without firing change events that would
      // re-dispatch and create a feedback loop.
      if (input.tagName === 'INPUT' && input.type === 'number') {
        var stripped = parseFloat(value);
        input.value = isNaN(stripped) ? '' : String(stripped);
      } else if (input.tagName === 'INPUT' && input.type === 'color') {
        if (value && /^#[0-9a-fA-F]{3,8}$/.test(value)) input.value = value;
      } else if (input.tagName === 'SELECT') {
        input.value = value;
      } else if (input.tagName === 'BUTTON' && descriptor.kind === 'toggle') {
        var on = value === descriptor.onValue;
        input.style.background = on ? '#6366f1' : '#ffffff';
        input.style.color = on ? '#ffffff' : '#1e293b';
      }
    }
  }

  var designStateListener = null;
  function attachDesignStateListener() {
    if (designStateListener) return;
    designStateListener = function(e) {
      if (!e || !e.detail) return;
      // Loop guard: ignore events we emitted ourselves. Literal source name
      // is duplicated here (vs. the SOURCE constant) so grep across modules
      // turns up both ends of the contract.
      if (e.detail.source === 'palette') return;

      if (e.detail.kind === 'deselect') {
        // The panel closed — tear down the palette in sync. applyingRemote
        // suppresses our own deselect dispatch so the round-trip stops.
        if (state.selectedElement) {
          applyingRemote = true;
          try { hide(); } finally { applyingRemote = false; }
        }
        return;
      }

      if (e.detail.kind === 'edit' && e.detail.change) {
        // Mirror the panel's edit into the palette without re-dispatching.
        // applyStyle's applyingRemote guard skips the outbound dispatch
        // and skips the undo-stack push (the panel owns its own change
        // tracking — undo is palette-side and intercepts before dispatch).
        // The wire format is kebab-case; convert to camelCase for the
        // descriptor.prop comparison and the element.style assignment
        // (both forms work, but camelCase matches descriptor.prop literally).
        var prop = kebabToCamel(e.detail.change.property);
        applyingRemote = true;
        try {
          if (state.selectedElement) {
            try {
              state.selectedElement.style[prop] = e.detail.change.value;
            } catch (err) {
              console.error('[Palette] remote applyStyle failed', e.detail.change, err);
            }
          }
          syncControlValue(prop, e.detail.change.value);
        } finally {
          applyingRemote = false;
        }
      }
    };
    document.addEventListener('devtool:design-state', designStateListener);
  }

  // ---------------------------------------------------------------------------
  // Cross-surface undo (Ctrl+Z / Cmd+Z)
  //
  // The palette owns the undo stack because applyStyle is the canonical
  // mutation seam — every user edit (palette or panel) ultimately emits a
  // design-state event the palette captures. On Ctrl+Z we pop the most
  // recent {property, prevValue, element} entry, restore it on the element,
  // and dispatch a normal edit event so the panel reflects the rollback.
  //
  // Scope: the listener attaches on document and only acts when an element
  // is selected (state.selectedElement set). When no element is selected
  // we let the browser/page handle Ctrl+Z normally.
  // ---------------------------------------------------------------------------
  var undoListener = null;
  function attachUndoListener() {
    if (undoListener) return;
    undoListener = function(e) {
      if (!(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) return;
      // Accept both 'z' and 'Z' (Caps Lock); Shift+Z is excluded by the
      // shiftKey guard above so the common redo binding (Ctrl+Shift+Z)
      // doesn't fire a single undo.
      if (!(e.key === 'z' || e.key === 'Z')) return;
      if (!state.selectedElement) return;
      if (undoStack.length === 0) return;

      var entry = undoStack.pop();
      // Don't restore against a stale element that's no longer the
      // selection (e.g. selection changed without hide()). Bail silently
      // rather than mutate something the user can't see.
      if (entry.element !== state.selectedElement) return;

      e.preventDefault();
      // Restore via direct write + outbound dispatch. We bypass applyStyle
      // because applyStyle would push *another* entry onto the undo stack,
      // making subsequent undos no-ops.
      try {
        state.selectedElement.style[entry.property] = entry.prevValue;
      } catch (err) {
        console.error('[Palette] undo restore failed', entry, err);
      }
      // Mirror the rollback into our own controls + tell the panel. The
      // panel speaks kebab-case on the wire; entry.property is camelCase
      // (we stored it from applyStyle's prop param, which is descriptor.prop).
      syncControlValue(entry.property, entry.prevValue);
      dispatchDesignState({
        source: 'palette',
        kind: 'edit',
        selector: currentSelector(),
        change: { property: camelToKebab(entry.property), value: entry.prevValue }
      });
    };
    document.addEventListener('keydown', undoListener);
  }

  // ---------------------------------------------------------------------------
  // Initialise
  // ---------------------------------------------------------------------------
  attachSelectionListener();
  attachDesignStateListener();
  attachUndoListener();

  // Public API
  window.__devtool_palette = {
    show: show,
    hide: hide,
    classifyElement: classifyElement,
    controlsForType: controlsForType,
    computePosition: computePosition,
    getState: function() {
      return {
        active: !!state.paletteEl,
        elementType: state.elementType,
        userMoved: state.userMoved,
        userPos: state.userPos,
        undoDepth: undoStack.length
      };
    }
  };
})();

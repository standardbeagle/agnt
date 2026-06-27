// Layout diagnostic primitives for DevTool
// Find overflows, stacking contexts, and offscreen elements

(function() {
  'use strict';

  var utils = window.__devtool_utils;
  var inspection = window.__devtool_inspection;
  var visual = window.__devtool_visual;

  function findOverflows() {
    var elements = document.querySelectorAll('*');
    var results = [];

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      var overflow = inspection.getOverflow(el);

      if (overflow && overflow.hasOverflow) {
        results.push({
          selector: utils.generateSelector(el),
          type: overflow.x === 'hidden' || overflow.y === 'hidden' ? 'hidden' : 'scrollable',
          scrollWidth: overflow.scrollWidth,
          scrollHeight: overflow.scrollHeight,
          clientWidth: overflow.clientWidth,
          clientHeight: overflow.clientHeight
        });
      }
    }

    return { overflows: results, count: results.length };
  }

  function findStackingContexts() {
    var elements = document.querySelectorAll('*');
    var contexts = [];

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      if (utils.isDevtoolElement && utils.isDevtoolElement(el)) continue;

      var computed = window.getComputedStyle(el);
      // Canonical, complete trigger set (will-change, isolation, contain,
      // mix-blend-mode, clip-path, mask, backdrop-filter, flex/grid children
      // — all of which the old check silently missed) shared with getStacking
      // via utils so the two never disagree. Each trigger is {property, value}
      // so the agent gets the removable cause, not a bare label.
      var triggers = utils.stackingContextTriggers(computed, utils.isFlexOrGridItem(el));

      if (triggers.length > 0) {
        contexts.push({
          selector: utils.generateSelector(el),
          zIndex: computed.zIndex,
          triggers: triggers,
          // Back-compat: flat reason[] of property names.
          reason: triggers.map(function(t) { return t.property; })
        });
      }
    }

    return { contexts: contexts, count: contexts.length };
  }

  function findOffscreen() {
    var elements = document.querySelectorAll('*');
    var results = [];

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      var viewport = visual.isInViewport(el);

      // Skip if error or no valid response
      if (!viewport || viewport.error || !viewport.rect) {
        continue;
      }

      if (!viewport.intersecting) {
        var rect = viewport.rect;
        var direction = [];

        if (rect.bottom < 0) direction.push('above');
        if (rect.top > window.innerHeight) direction.push('below');
        if (rect.right < 0) direction.push('left');
        if (rect.left > window.innerWidth) direction.push('right');

        results.push({
          selector: utils.generateSelector(el),
          direction: direction,
          rect: rect
        });
      }
    }

    return { offscreen: results, count: results.length };
  }

  // ==========================================================================
  // diagnose(): cause→symptom layout diagnostics
  //
  // A single bounded synchronous pass (~30-80ms on a typical page) that finds
  // the high-diagnostic-distance CSS/layout bugs an agent is otherwise blind to,
  // and — unlike findStackingContexts which only ENUMERATES — names the
  // OFFENDING ANCESTOR and the correct fix (plus the common wrong fix to avoid).
  //
  // Four checks:
  //   containing-block-trap  position:fixed/absolute trapped by an ancestor's
  //                          transform/filter/will-change/contain → not relative
  //                          to the viewport/positioned-parent as the author expects.
  //   ineffective-zindex     z-index on a position:static, non-flex/grid element
  //                          (silently ignored).
  //   click-interception     a visible interactive element whose center hits a
  //                          different element (a transparent overlay eats clicks).
  //   clipped-descendant     content cut off by an ancestor overflow:hidden/clip.
  // ==========================================================================

  var DIAG_MAX_ELEMENTS = 4000; // budget guard; reported when exceeded
  var DIAG_MAX_PER_CHECK = 15;  // cap findings per check (no silent truncation)
  var INTERACTIVE_SEL = 'a,button,input,select,textarea,label,[role="button"],[onclick],[tabindex]';

  // The CSS properties that make an ancestor the containing block for a
  // position:fixed descendant (plain positioning does NOT — only these do).
  function fixedContainingBlockCause(c) {
    if (c.transform && c.transform !== 'none') return 'transform';
    if (c.perspective && c.perspective !== 'none') return 'perspective';
    if (c.filter && c.filter !== 'none') return 'filter';
    if (c.willChange && /transform|perspective|filter/.test(c.willChange)) return 'will-change';
    if (c.contain && /paint|layout|strict|content/.test(c.contain)) return 'contain';
    return null;
  }

  function isClip(v) { return v === 'hidden' || v === 'clip'; }

  function rectHasArea(r) { return r.width > 0 && r.height > 0; }

  function diagnose() {
    var all = document.querySelectorAll('*');
    var scanned = Math.min(all.length, DIAG_MAX_ELEMENTS);
    var capped = all.length > DIAG_MAX_ELEMENTS;

    var findings = [];
    var perCheck = { 'containing-block-trap': 0, 'ineffective-zindex': 0, 'click-interception': 0, 'clipped-descendant': 0 };

    function add(check, severity, el, causeEl, causeProperty, detail, fix, avoid) {
      if (perCheck[check] >= DIAG_MAX_PER_CHECK) return;
      perCheck[check]++;
      findings.push({
        check: check,
        severity: severity,
        selector: utils.generateSelector(el),
        cause: causeEl ? utils.generateSelector(causeEl) : '',
        cause_property: causeProperty || '',
        detail: detail || '',
        fix: fix || '',
        avoid: avoid || ''
      });
    }

    for (var i = 0; i < scanned; i++) {
      var el = all[i];
      if (el === document.documentElement || el === document.body) continue;
      var c = window.getComputedStyle(el);
      var pos = c.position;

      // --- containing-block-trap (fixed/absolute) -------------------------
      if (pos === 'fixed' || pos === 'absolute') {
        var anc = el.parentElement;
        while (anc && anc !== document.documentElement) {
          var ac = window.getComputedStyle(anc);
          var cause = fixedContainingBlockCause(ac);
          if (cause) {
            if (pos === 'fixed') {
              add('containing-block-trap', 'high', el, anc, cause,
                'position:fixed is resolved against this ancestor (its ' + cause + ' makes it the containing block), not the viewport, so the element is positioned/clipped relative to the ancestor instead.',
                'Move the fixed element out of the transformed ancestor (e.g. portal it to <body>), or remove the ancestor ' + cause + '.',
                'Do not tweak top/left/right offsets to compensate — the reference frame is wrong, not the numbers.');
            } else {
              add('containing-block-trap', 'medium', el, anc, cause,
                'position:absolute resolves against this ancestor because its ' + cause + ' establishes a containing block, which may not be the positioned parent you intended.',
                'Add position:relative to the element you actually want as the reference, or remove the ancestor ' + cause + '.',
                'Do not adjust offsets blindly — verify which ancestor is the containing block first.');
            }
            break;
          }
          // a positioned ancestor is the normal containing block for absolute — stop.
          if (pos === 'absolute' && ac.position !== 'static') break;
          anc = anc.parentElement;
        }
      }

      // --- ineffective-zindex --------------------------------------------
      if (c.zIndex !== 'auto' && pos === 'static') {
        var parent = el.parentElement;
        var pc = parent ? window.getComputedStyle(parent) : null;
        var inFlexGrid = pc && /(^|\s)(flex|grid|inline-flex|inline-grid)($|\s)/.test(pc.display);
        if (!inFlexGrid) {
          add('ineffective-zindex', 'high', el, null, 'position:static',
            'z-index ' + c.zIndex + ' is ignored: z-index only applies to positioned elements or flex/grid items, and this element is position:static with a non-flex/grid parent.',
            'Set position:relative on the element (or make the parent display:flex/grid) so the z-index takes effect.',
            'Do not raise the z-index value — it is being discarded entirely, not losing a comparison.');
        }
      }

      // --- clipped-descendant (vs nearest clipping ancestor) -------------
      if (rectHasArea(el.getBoundingClientRect())) {
        var clipAnc = el.parentElement, clipStyle = null, clipFound = null;
        while (clipAnc && clipAnc !== document.documentElement) {
          var cc = window.getComputedStyle(clipAnc);
          if (isClip(cc.overflow) || isClip(cc.overflowX) || isClip(cc.overflowY)) {
            clipStyle = cc; clipFound = clipAnc; break;
          }
          clipAnc = clipAnc.parentElement;
        }
        if (clipFound) {
          var er = el.getBoundingClientRect();
          var cr = clipFound.getBoundingClientRect();
          var over = [];
          if (isClip(clipStyle.overflowX) || isClip(clipStyle.overflow)) {
            if (er.right > cr.right + 1) over.push('right');
            if (er.left < cr.left - 1) over.push('left');
          }
          if (isClip(clipStyle.overflowY) || isClip(clipStyle.overflow)) {
            if (er.bottom > cr.bottom + 1) over.push('bottom');
            if (er.top < cr.top - 1) over.push('top');
          }
          // Only report the boundary element: skip if the parent already
          // overflows the same clip box (avoids one finding per descendant).
          if (over.length && el.parentElement && el.parentElement !== clipFound) {
            var ppr = el.parentElement.getBoundingClientRect();
            var parentOver = (ppr.right > cr.right + 1) || (ppr.left < cr.left - 1) ||
              (ppr.bottom > cr.bottom + 1) || (ppr.top < cr.top - 1);
            if (parentOver) over = [];
          }
          if (over.length) {
            add('clipped-descendant', 'medium', el, clipFound, 'overflow',
              'This element extends past its ancestor on the ' + over.join('/') + ' edge and is clipped by the ancestor\'s overflow:' + (clipStyle.overflow !== 'visible' ? clipStyle.overflow : clipStyle.overflowX + '/' + clipStyle.overflowY) + '.',
              'If the content should be visible (dropdown/tooltip/menu), set overflow:visible on the ancestor or render the content in a portal outside it.',
              'Do not just enlarge the child — the ancestor is doing the clipping.');
          }
        }
      }
    }

    // --- click-interception (small interactive-element loop) --------------
    var interactive = document.querySelectorAll(INTERACTIVE_SEL);
    for (var j = 0; j < interactive.length; j++) {
      if (perCheck['click-interception'] >= DIAG_MAX_PER_CHECK) break;
      var iel = interactive[j];
      var ic = window.getComputedStyle(iel);
      if (ic.display === 'none' || ic.visibility === 'hidden' || parseFloat(ic.opacity) === 0) continue;
      if (ic.pointerEvents === 'none') continue; // intentionally click-through
      var ir = iel.getBoundingClientRect();
      if (!rectHasArea(ir)) continue;
      var cx = ir.left + ir.width / 2;
      var cy = ir.top + ir.height / 2;
      if (cx < 0 || cy < 0 || cx > window.innerWidth || cy > window.innerHeight) continue;
      var hit = document.elementFromPoint(cx, cy);
      if (!hit) continue;
      // OK when the point lands on the control itself, its own descendant, or a
      // wrapper that contains it.
      if (hit === iel || iel.contains(hit) || hit.contains(iel)) continue;
      var hc = window.getComputedStyle(hit);
      add('click-interception', 'high', iel, hit, 'overlay',
        'The center of this interactive element resolves to a different element (z-index ' + hc.zIndex + ', position ' + hc.position + '), which overlays it and will intercept clicks/taps.',
        'Find the overlay: remove it, lower its z-index/move it out of the way, or set pointer-events:none on it if it is meant to be click-through.',
        'Do not raise the button\'s z-index blindly — confirm the overlay is the one on top first.');
    }

    return {
      findings: findings,
      count: findings.length,
      scanned: scanned,
      capped: capped,
      by_check: perCheck
    };
  }

  // Export layout functions
  window.__devtool_layout = {
    findOverflows: findOverflows,
    findStackingContexts: findStackingContexts,
    findOffscreen: findOffscreen,
    diagnose: diagnose
  };
})();

// Read-only walkthrough VIEWER for the public walkthrough-publish plane (P4).
//
// This is the PLAYER half of the walkthrough surface: it renders a published
// walkthrough script (steps with narration + an optional target selector + an
// advance rule) as a floating card, highlights the current target, and lets a
// visitor step Next/Prev or auto-advance. It is deliberately a strict subset of
// walkthrough.js: it has NO authoring UI, NO agent-control transport, NO WS/exec
// channel, and NO `proxy exec` reach into other frames. It never evaluates
// author-supplied strings as code or markup — narration is written with
// textContent, targets are resolved with querySelector against a validated
// simple-selector grammar, and the highlight is a plain positioned box. It runs
// unchanged under `script-src 'self'` (P1 INV-12): no code-eval sinks, no dynamic
// function compilation, no inline/dynamic code, no screenshot-capture library,
// and none of the dev-control namespace. (This header is deliberately free of the
// literal tokens the public forbidden-symbol scan bans.)
//
// Public surface (what the public injector / publish plane calls):
//   window.__walkthroughViewer.create(script, opts) -> viewer
// viewer:
//   start()            render the panel at step 0
//   next() / prev()    step manually
//   goTo(index)        jump to a step
//   activeIndex()      current step index, or -1 before start
//   stepCount()        number of steps
//   missingTarget()    true if the current step's target matched nothing
//   destroy()          remove the panel + highlight + timers
//
// It is INERT until create() is called, so it is safe to ship in every bundle
// (the public allowlist selects it; the dev bundles never invoke it).

(function () {
  'use strict';

  try {
    var VIEWER_ID = '__wt_viewer_panel';
    var HILITE_ID = '__wt_viewer_highlight';
    // Simple-selector grammar the viewer will resolve. Anchored, no combinators
    // beyond descendant/child, no attribute/script sinks. A target that does not
    // match this grammar is treated as "no target" rather than passed to
    // querySelector, so an author cannot smuggle an exotic selector.
    var SAFE_SELECTOR_RE = /^[#.]?[A-Za-z_][A-Za-z0-9_-]*(?:[ >][#.]?[A-Za-z_][A-Za-z0-9_-]*)*$/;
    var MAX_STEPS = 64;
    var MAX_TEXT = 2048;

    function isSafeSelector(sel) {
      return typeof sel === 'string' && sel.length > 0 && sel.length <= 256 &&
        SAFE_SELECTOR_RE.test(sel);
    }

    function clampText(s) {
      if (typeof s !== 'string') return '';
      return s.length > MAX_TEXT ? s.slice(0, MAX_TEXT) : s;
    }

    function normalizeScript(script) {
      var steps = (script && Array.isArray(script.steps)) ? script.steps : [];
      var out = [];
      for (var i = 0; i < steps.length && out.length < MAX_STEPS; i++) {
        var s = steps[i] || {};
        out.push({
          narration: clampText(s.narration || s.text || ''),
          target: isSafeSelector(s.target) ? s.target : '',
          advance: (s.advance === 'auto' || s.advance === 'click') ? s.advance : 'manual',
          holdMs: (typeof s.holdMs === 'number' && s.holdMs > 0) ? Math.min(s.holdMs, 60000) : 4000
        });
      }
      return out;
    }

    function create(script, opts) {
      opts = opts || {};
      var steps = normalizeScript(script);
      var index = -1;
      var panel = null;
      var narrationEl = null;
      var counterEl = null;
      var highlight = null;
      var timer = null;
      var lastTargetMissing = false;

      function clearTimer() {
        if (timer) { clearTimeout(timer); timer = null; }
      }

      function removeHighlight() {
        if (highlight && highlight.parentNode) {
          highlight.parentNode.removeChild(highlight);
        }
        highlight = null;
      }

      function positionHighlight(el) {
        removeHighlight();
        if (!el || typeof el.getBoundingClientRect !== 'function') {
          lastTargetMissing = true;
          return;
        }
        lastTargetMissing = false;
        var r = el.getBoundingClientRect();
        highlight = document.createElement('div');
        highlight.id = HILITE_ID;
        var st = highlight.style;
        st.position = 'fixed';
        st.left = r.left + 'px';
        st.top = r.top + 'px';
        st.width = r.width + 'px';
        st.height = r.height + 'px';
        st.border = '2px solid #4c8bf5';
        st.borderRadius = '4px';
        st.boxShadow = '0 0 0 9999px rgba(0,0,0,0.35)';
        st.pointerEvents = 'none';
        st.zIndex = '2147483600';
        (document.body || document.documentElement).appendChild(highlight);
      }

      function resolveTarget(sel) {
        if (!sel) return null;
        try { return document.querySelector(sel); } catch (e) { return null; }
      }

      function render() {
        if (index < 0 || index >= steps.length) return;
        var step = steps[index];
        if (!panel) buildPanel();
        narrationEl.textContent = step.narration;
        counterEl.textContent = (index + 1) + ' / ' + steps.length;

        var el = resolveTarget(step.target);
        if (step.target) {
          positionHighlight(el);
        } else {
          removeHighlight();
          lastTargetMissing = false;
        }

        clearTimer();
        if (step.advance === 'auto') {
          timer = setTimeout(function () { next(); }, step.holdMs);
        } else if (step.advance === 'click' && el) {
          el.addEventListener('click', onTargetClick, { once: true });
        }
      }

      function onTargetClick() { next(); }

      function buildPanel() {
        panel = document.createElement('div');
        panel.id = VIEWER_ID;
        var ps = panel.style;
        ps.position = 'fixed';
        ps.right = '16px';
        ps.bottom = '16px';
        ps.maxWidth = '320px';
        ps.padding = '14px 16px';
        ps.background = '#1e1e24';
        ps.color = '#f5f5f7';
        ps.font = '14px/1.45 system-ui, sans-serif';
        ps.borderRadius = '10px';
        ps.boxShadow = '0 8px 28px rgba(0,0,0,0.4)';
        ps.zIndex = '2147483601';

        counterEl = document.createElement('div');
        counterEl.style.opacity = '0.6';
        counterEl.style.fontSize = '12px';
        counterEl.style.marginBottom = '6px';
        panel.appendChild(counterEl);

        narrationEl = document.createElement('div');
        narrationEl.style.marginBottom = '12px';
        panel.appendChild(narrationEl);

        var nav = document.createElement('div');
        nav.style.display = 'flex';
        nav.style.gap = '8px';
        nav.appendChild(makeButton('Prev', prev));
        nav.appendChild(makeButton('Next', next));
        nav.appendChild(makeButton('Close', destroy));
        panel.appendChild(nav);

        (document.body || document.documentElement).appendChild(panel);
      }

      function makeButton(label, handler) {
        var b = document.createElement('button');
        b.type = 'button';
        b.textContent = label;
        var bs = b.style;
        bs.flex = '1';
        bs.padding = '6px 8px';
        bs.border = '0';
        bs.borderRadius = '6px';
        bs.background = '#33333c';
        bs.color = '#f5f5f7';
        bs.cursor = 'pointer';
        bs.font = 'inherit';
        b.addEventListener('click', handler);
        return b;
      }

      function start() {
        if (steps.length === 0) return viewer;
        index = 0;
        render();
        return viewer;
      }

      function goTo(i) {
        if (typeof i !== 'number' || i < 0 || i >= steps.length) return viewer;
        index = i;
        render();
        return viewer;
      }

      function next() {
        if (index < steps.length - 1) { index++; render(); }
        return viewer;
      }

      function prev() {
        if (index > 0) { index--; render(); }
        return viewer;
      }

      function destroy() {
        clearTimer();
        removeHighlight();
        if (panel && panel.parentNode) panel.parentNode.removeChild(panel);
        panel = null;
      }

      var viewer = {
        start: start,
        next: next,
        prev: prev,
        goTo: goTo,
        activeIndex: function () { return index; },
        stepCount: function () { return steps.length; },
        missingTarget: function () { return lastTargetMissing; },
        destroy: destroy
      };
      return viewer;
    }

    window.__walkthroughViewer = {
      create: create,
      isSafeSelector: isSafeSelector,
      version: 'p4'
    };
  } catch (e) {
    try { console.error('[WalkthroughViewer] init failed:', e); } catch (e2) {}
  }
})();

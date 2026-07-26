// Public walkthrough PLAYER for the walkthrough-publish plane (P5).
//
// This is the player half of the public walkthrough surface. It runs ENTIRELY
// client-side on the public plane: NO agent, NO WebSocket, NO exec channel, NO
// dev-control namespace, NO cross-frame reach. It renders a published
// walkthrough (the P2 PublishedWalkthrough shape) as an accessible floating
// card, binds + applies the bound variant via the P3 variant engine BEFORE the
// first step, highlights each step's target, and advances the same three ways
// the live walkthrough does — auto / click-target / wait — but driven purely
// in-page (timers + DOM listeners) with zero transport. A step may also carry
// gesture: hover|click|scroll|drag — the player renders the matching animated
// affordance over the highlight (Web Animations API, static under reduced
// motion) and the narration reads through character-by-character; both are
// removed/reset the instant the step advances. The affordance carries a text
// label naming the action: the step's gesture_label when present (clamped,
// textContent only), else a generic verb phrase.
//
// Shared normalization (NOT a fork): the step model this player consumes is the
// SAME one P2 defines (internal/publish/schema.go Advance/Step) and the live
// walkthrough.js normalizes to — advance.type in {auto, click-target, wait},
// empty type defaults to 'auto', an auto with ms<=0 gets the 5000ms default
// (internal/publish/normalize.go defaultAutoMS), wait.when in
// {url-contains, element-present, element-visible}. The ONLY deliberate
// divergence from walkthrough.js's normalizeScript is error POLICY: the public
// player degrades an invalid step to a manual (user-driven) step instead of
// throwing, because the public plane must never crash a visitor's page.
//
// Security (P1 spec §4/§6, INV-6/INV-12): it never evaluates author-supplied
// strings as code or markup. Narration/title are written with textContent only,
// targets are resolved with querySelector ONLY after passing the P2/P3
// restricted selector grammar (a target that fails the grammar or resolves to
// nothing is treated as "no target" — never handed to querySelector, never
// followed as a link, never navigated to, never executed), and the highlight is
// a plain positioned pointer-events:none box. It runs unchanged under
// `script-src 'self'`: no code-eval sinks, no dynamic function compilation, no
// screenshot-capture library, none of the dev-control namespace. (This header is
// deliberately free of the literal tokens the public forbidden-symbol scan bans.)
//
// Public surface (what the public injector / publish plane calls):
//   window.__walkthroughViewer.create(walkthrough, opts) -> player
// opts: { variantEngine?, variantId?, routeBinding?, root?, reducedMotion? }
// player:
//   start()            bind+apply the variant, then render at step 0
//   next() / prev()    step manually
//   goTo(index)        jump to a step
//   activeIndex()      current step index, or -1 before start
//   stepCount()        number of steps
//   activeVariant()    variant id applied by the bound engine, or null
//   missingTarget()    true if the current step's target matched nothing
//   destroy()          revert variant + remove card/highlight + all timers/listeners
//   trackedTimerCount()    live timers/intervals (0 after destroy) — teardown gate
//   trackedListenerCount() live DOM listeners (0 after destroy) — teardown gate
//
// It is INERT until create() is called, so it is safe to ship in every bundle
// (the public allowlist selects it; the dev bundles never invoke it). The IIFE
// keeps a leading space after `function` so wrapModule leaves it isolated.

(function () {
  'use strict';

  try {
    var CARD_ID = '__wt_player_card';
    var HILITE_ID = '__wt_player_highlight';
    var LIVE_ID = '__wt_player_live';
    // Default auto dwell — mirrors internal/publish/normalize.go defaultAutoMS
    // and walkthrough.js's auto fallback (both 5000), replacing the P4 stub's
    // forked default so the player consumes the shared P2 normalization.
    var DEFAULT_AUTO_MS = 5000;
    var MAX_AUTO_MS = 600000;
    var POLL_MS = 300; // mirrors walkthrough.js wait-condition poll cadence
    var MAX_STEPS = 64;
    var MAX_TEXT = 2048;
    // Cap on an author-supplied gesture_label; mirrors walkthrough.js
    // MAX_GESTURE_LABEL and publish.MaxGestureLabelLength. The label pill is a
    // single nowrap line, so longer text runs off the viewport.
    var MAX_GESTURE_LABEL = 64;

    // Local restricted selector grammar. It is a SUBSET-equivalent fallback for
    // the P2/P3 grammar: when window.__variantEngine.validateSelector is present
    // we defer to it (single source of truth); otherwise we use this anchored
    // regex. Either way a target that fails is treated as "no target" and is
    // NEVER passed to querySelector — an author cannot smuggle an exotic selector,
    // a javascript:/href sink, or a code string through the target field.
    var SAFE_SELECTOR_RE = /^[#.]?[A-Za-z_][A-Za-z0-9_-]*(?:[ >][#.]?[A-Za-z_][A-Za-z0-9_-]*)*$/;

    function isSafeSelector(sel) {
      if (typeof sel !== 'string' || sel.length === 0 || sel.length > 256) { return false; }
      try {
        if (window.__variantEngine && typeof window.__variantEngine.validateSelector === 'function') {
          return !!window.__variantEngine.validateSelector(sel).ok;
        }
      } catch (e) { /* fall through to local grammar */ }
      return SAFE_SELECTOR_RE.test(sel);
    }

    function clampText(s) {
      if (typeof s !== 'string') { return ''; }
      return s.length > MAX_TEXT ? s.slice(0, MAX_TEXT) : s;
    }

    // normalizeStep mirrors walkthrough.js normalizeScript + internal/publish
    // NormalizeStep, degrading (never throwing) an invalid step to 'manual'.
    function normalizeStep(s) {
      s = s || {};
      var target = isSafeSelector(s.target) ? s.target : '';
      var adv = s.advance || {};
      var type = adv.type;
      if (type === undefined || type === null || type === '') {
        type = 'auto'; // P2 NormalizeStep: empty advance type defaults to auto
      }
      if (type !== 'auto' && type !== 'click-target' && type !== 'wait') {
        type = 'manual'; // unknown type: user-driven, never crash
      }
      if (type === 'click-target' && !target) {
        type = 'manual'; // click-target with no valid target degrades to manual
      }
      var when = adv.when;
      if (type === 'wait' &&
          when !== 'url-contains' && when !== 'element-present' && when !== 'element-visible') {
        type = 'manual'; // wait with an invalid condition degrades to manual
      }
      var ms = (typeof adv.ms === 'number' && adv.ms > 0) ? Math.min(adv.ms, MAX_AUTO_MS) : DEFAULT_AUTO_MS;
      // Gesture affordance: closed vocabulary, degrades to none. It is pure
      // decoration rendered near the highlight — never an action source.
      var g = s.gesture;
      var gesture = (g === 'hover' || g === 'click' || g === 'scroll' || g === 'drag') ? g : '';
      // gesture_label names the concrete action for this step. Degrade (clamp /
      // drop), never throw: the public plane must not crash a visitor's page.
      // Written with textContent only — never parsed as markup.
      var gLabel = (typeof s.gesture_label === 'string') ? s.gesture_label.slice(0, MAX_GESTURE_LABEL) : '';
      return {
        title: clampText(s.title || ''),
        body: clampText(s.body || s.narration || s.text || ''),
        target: target,
        gesture: target ? gesture : '',
        gestureLabel: (target && gesture) ? gLabel : '',
        advance: { type: type, ms: ms, when: when || '', value: clampText(adv.value || '') }
      };
    }

    function normalizeWalkthrough(wt) {
      var raw = (wt && Array.isArray(wt.steps)) ? wt.steps : [];
      var out = [];
      for (var i = 0; i < raw.length && out.length < MAX_STEPS; i++) {
        out.push(normalizeStep(raw[i]));
      }
      return out;
    }

    function prefersReducedMotion() {
      try {
        return !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
      } catch (e) { return false; }
    }

    function create(walkthrough, opts) {
      opts = opts || {};
      var steps = normalizeWalkthrough(walkthrough);
      var reducedMotion = (typeof opts.reducedMotion === 'boolean') ? opts.reducedMotion : prefersReducedMotion();

      var index = -1;
      var card = null;
      var titleEl = null;
      var bodyEl = null;
      var counterEl = null;
      var liveEl = null;
      var highlight = null;
      var gestureBox = null;
      var gestureAnims = [];
      var renderedGesture = '';
      var lastFocused = null;
      var lastTargetMissing = false;
      var destroyed = false;

      // ---- Teardown bookkeeping: every timer + listener is tracked so destroy
      // and each step transition remove them ALL — no orphaned timers/listeners.
      var timers = [];    // setTimeout ids
      var intervals = []; // setInterval ids
      var listeners = []; // { target, type, handler, opts }
      var variantEngine = null; // the P3 engine we created (reverted on destroy)

      function setTimer(fn, ms) { var id = setTimeout(fn, ms); timers.push(id); return id; }
      function setPoll(fn, ms) { var id = setInterval(fn, ms); intervals.push(id); return id; }
      function addListener(target, type, handler, listenerOpts) {
        target.addEventListener(type, handler, listenerOpts);
        listeners.push({ target: target, type: type, handler: handler, opts: listenerOpts });
      }
      function clearTimers() {
        for (var i = 0; i < timers.length; i++) { clearTimeout(timers[i]); }
        for (var j = 0; j < intervals.length; j++) { clearInterval(intervals[j]); }
        timers = [];
        intervals = [];
      }
      function removeListeners(pred) {
        var kept = [];
        for (var i = 0; i < listeners.length; i++) {
          var l = listeners[i];
          if (pred && !pred(l)) { kept.push(l); continue; }
          try { l.target.removeEventListener(l.type, l.handler, l.opts); } catch (e) { /* ignore */ }
        }
        listeners = kept;
      }

      // ---- Highlight (plain positioned box, pointer-events:none) --------------
      function removeGesture() {
        for (var i = 0; i < gestureAnims.length; i++) {
          try { gestureAnims[i].cancel(); } catch (e) { /* ignore */ }
        }
        gestureAnims = [];
        renderedGesture = '';
        if (gestureBox && gestureBox.parentNode) { gestureBox.parentNode.removeChild(gestureBox); }
        gestureBox = null;
      }
      function removeHighlight() {
        removeGesture();
        if (highlight && highlight.parentNode) { highlight.parentNode.removeChild(highlight); }
        highlight = null;
      }
      function positionHighlight(el) {
        if (!el || typeof el.getBoundingClientRect !== 'function') {
          removeHighlight();
          lastTargetMissing = true;
          return;
        }
        lastTargetMissing = false;
        var r = el.getBoundingClientRect();
        if (!highlight) {
          highlight = document.createElement('div');
          highlight.id = HILITE_ID;
          highlight.setAttribute('aria-hidden', 'true');
          var st = highlight.style;
          st.position = 'fixed';
          st.border = '2px solid #4c8bf5';
          st.borderRadius = '4px';
          st.boxShadow = '0 0 0 9999px rgba(0,0,0,0.35)';
          st.pointerEvents = 'none';
          st.zIndex = '2147483600';
          if (!reducedMotion) { st.transition = 'left 120ms, top 120ms, width 120ms, height 120ms'; }
          (document.body || document.documentElement).appendChild(highlight);
        }
        highlight.style.left = r.left + 'px';
        highlight.style.top = r.top + 'px';
        highlight.style.width = r.width + 'px';
        highlight.style.height = r.height + 'px';
        positionGesture(r);
      }

      // ---- Animated gesture affordance ---------------------------------------
      // Renders the step's requested interaction (hover/click/scroll/drag) as
      // a pointer-events:none animated affordance over the highlight. Motion
      // uses the Web Animations API (no style element, no code-eval sink, runs
      // under script-src 'self'); under prefers-reduced-motion the same shapes
      // render statically. The affordance is removed with the highlight, so it
      // auto-dismisses the instant the advance condition fires.
      var GESTURE_ID = '__wt_player_gesture';
      function makeGesturePart(styleFn) {
        var e = document.createElement('div');
        e.setAttribute('aria-hidden', 'true');
        e.style.position = 'absolute';
        styleFn(e.style);
        return e;
      }
      function animatePart(el, keyframes, opts) {
        if (reducedMotion || typeof el.animate !== 'function') { return; }
        try {
          gestureAnims.push(el.animate(keyframes, opts));
        } catch (e) { /* animation unsupported: static affordance */ }
      }
      function positionGesture(r) {
        var step = (index >= 0 && index < steps.length) ? steps[index] : null;
        var gesture = step ? step.gesture : '';
        var gestureLabel = step ? step.gestureLabel : '';
        if (!gesture) { removeGesture(); return; }
        // Cache key includes the label: consecutive steps can share a gesture
        // while naming different actions, and a stale box would keep the
        // previous step's label.
        var key = gesture + '\n' + gestureLabel;
        if (!gestureBox || renderedGesture !== key) {
          removeGesture();
          gestureBox = document.createElement('div');
          gestureBox.id = GESTURE_ID;
          gestureBox.setAttribute('aria-hidden', 'true');
          var gs = gestureBox.style;
          gs.position = 'fixed';
          gs.pointerEvents = 'none';
          gs.zIndex = '2147483601';
          buildGestureParts(gesture, gestureLabel);
          (document.body || document.documentElement).appendChild(gestureBox);
          renderedGesture = key;
        }
        gestureBox.style.left = r.left + 'px';
        gestureBox.style.top = r.top + 'px';
        gestureBox.style.width = r.width + 'px';
        gestureBox.style.height = r.height + 'px';
      }
      function buildGestureParts(gesture, gestureLabel) {
        // Label under the affordance so the shape reads as an instruction, not
        // a clickable control. The step's own gesture_label wins; these generic
        // verb phrases are the fallback (mirrors walkthrough.js GESTURE_LABELS).
        var labels = {
          hover: 'Hover here',
          click: 'Click here',
          scroll: 'Scroll this area',
          drag: 'Drag to move'
        };
        var lbl = makeGesturePart(function (s) {
          s.left = '50%'; s.top = 'calc(50% + 32px)'; s.transform = 'translateX(-50%)';
          s.padding = '2px 10px'; s.borderRadius = '10px';
          s.background = 'rgba(20,20,26,.85)'; s.border = '1px solid #4c8bf5';
          s.color = '#e6edf3'; s.font = '600 11px/1.6 system-ui,sans-serif';
          s.letterSpacing = '.04em'; s.whiteSpace = 'nowrap';
          s.boxShadow = '0 0 10px rgba(76,139,245,.5)';
        });
        lbl.textContent = gestureLabel || labels[gesture] || gesture;
        gestureBox.appendChild(lbl);
        if (gesture === 'hover') {
          var dot = makeGesturePart(function (s) {
            s.left = '50%'; s.top = '50%'; s.width = '28px'; s.height = '28px';
            s.borderRadius = '50%'; s.background = 'rgba(76,139,245,.45)';
            s.border = '2px solid #4c8bf5'; s.boxShadow = '0 0 12px rgba(76,139,245,.8)';
            s.transform = 'translate(-50%,-50%)';
          });
          gestureBox.appendChild(dot);
          animatePart(dot, [
            { transform: 'translate(-50%,-50%) scale(1)', opacity: 0.55 },
            { transform: 'translate(-50%,-64%) scale(1.18)', opacity: 0.95 }
          ], { duration: 1600, iterations: Infinity, direction: 'alternate', easing: 'ease-in-out' });
          return;
        }
        if (gesture === 'click') {
          for (var i = 0; i < 2; i++) {
            var ring = makeGesturePart(function (s) {
              s.left = '50%'; s.top = '50%'; s.width = '36px'; s.height = '36px';
              s.borderRadius = '50%'; s.border = '3px solid #4c8bf5';
              s.boxShadow = '0 0 10px rgba(76,139,245,.6)';
              s.transform = 'translate(-50%,-50%) scale(.3)';
            });
            gestureBox.appendChild(ring);
            animatePart(ring, [
              { transform: 'translate(-50%,-50%) scale(.3)', opacity: 0.9 },
              { transform: 'translate(-50%,-50%) scale(1.7)', opacity: 0 }
            ], { duration: 1500, iterations: Infinity, iterationStart: i * 0.5, easing: 'ease-out' });
          }
          return;
        }
        if (gesture === 'scroll') {
          var pill = makeGesturePart(function (s) {
            s.left = '50%'; s.top = '50%'; s.width = '24px'; s.height = '38px';
            s.borderRadius = '13px'; s.border = '2px solid #4c8bf5';
            s.background = 'rgba(20,20,26,.55)'; s.boxShadow = '0 0 10px rgba(76,139,245,.6)';
            s.transform = 'translate(-50%,-50%)';
          });
          var wheel = makeGesturePart(function (s) {
            s.left = '50%'; s.top = '7px'; s.width = '4px'; s.height = '8px';
            s.marginLeft = '-2px'; s.borderRadius = '2px'; s.background = '#4c8bf5';
          });
          pill.appendChild(wheel);
          gestureBox.appendChild(pill);
          animatePart(wheel, [
            { transform: 'translateY(0)', opacity: 1 },
            { transform: 'translateY(14px)', opacity: 0.5 }
          ], { duration: 1400, iterations: Infinity, direction: 'alternate', easing: 'ease-in-out' });
          return;
        }
        if (gesture === 'drag') {
          var track = makeGesturePart(function (s) {
            s.left = '12%'; s.right = '12%'; s.top = '50%'; s.height = '2px';
            s.marginTop = '-1px'; s.background = 'rgba(76,139,245,.45)'; s.borderRadius = '1px';
          });
          var knob = makeGesturePart(function (s) {
            s.left = '12%'; s.top = '50%'; s.width = '20px'; s.height = '20px';
            s.marginTop = '-10px'; s.borderRadius = '50%'; s.background = '#4c8bf5';
            s.border = '2px solid #fff'; s.boxShadow = '0 0 12px rgba(76,139,245,.8)';
          });
          gestureBox.appendChild(track);
          gestureBox.appendChild(knob);
          animatePart(knob, [
            { left: '12%', opacity: 0 },
            { left: '12%', opacity: 1, offset: 0.12 },
            { left: '88%', opacity: 1, offset: 0.88 },
            { left: '88%', opacity: 0 }
          ], { duration: 1800, iterations: Infinity, easing: 'ease-in-out' });
        }
      }
      function resolveTarget(sel) {
        if (!sel) { return null; }
        try { return document.querySelector(sel); } catch (e) { return null; }
      }

      // ---- Re-anchor on SPA route change / DOM remount ------------------------
      // A single observer + scroll/resize listeners keep the current step's
      // highlight glued to its (possibly re-created) target. If the target
      // vanished we surface it (missingTarget) but never crash; if it comes back
      // we re-anchor. This mirrors the variant engine's remount survival.
      var observer = (typeof MutationObserver !== 'undefined')
        ? new MutationObserver(function () { reanchor(); })
        : null;
      var reanchorScheduled = false;
      function reanchor() {
        if (destroyed || index < 0 || index >= steps.length) { return; }
        if (reanchorScheduled) { return; }
        reanchorScheduled = true;
        var run = function () {
          reanchorScheduled = false;
          if (destroyed || index < 0) { return; }
          var step = steps[index];
          if (!step.target) { return; }
          positionHighlight(resolveTarget(step.target));
        };
        if (typeof requestAnimationFrame === 'function') { requestAnimationFrame(run); }
        else { setTimer(run, 0); }
      }

      // ---- Step arming (advancement without any transport) --------------------
      function disarm() {
        clearTimers();
        // Drop per-step target listeners; keep the always-on keydown + observer.
        removeListeners(function (l) { return l.__step === true; });
      }

      function conditionHolds(adv) {
        try {
          if (adv.when === 'url-contains') {
            var href = String(window.location.href || '');
            return adv.value ? href.indexOf(String(adv.value)) !== -1 : false;
          }
          if (adv.when === 'element-present') {
            return isSafeSelector(adv.value) ? !!resolveTarget(adv.value) : false;
          }
          if (adv.when === 'element-visible') {
            if (!isSafeSelector(adv.value)) { return false; }
            var el = resolveTarget(adv.value);
            if (!el) { return false; }
            var r = el.getBoundingClientRect();
            return r.width > 0 && r.height > 0;
          }
        } catch (e) { /* ignore */ }
        return false;
      }

      function armStep() {
        var step = steps[index];
        var adv = step.advance;
        if (adv.type === 'click-target') {
          var el = resolveTarget(step.target);
          if (el) {
            var handler = function () { next(); };
            handler.__step = true;
            // Passive observation only: no preventDefault, no navigation, no
            // synthetic activation, no href read — the visitor's real click
            // drives the underlying app; we only advance the narration.
            var rec = { target: el, type: 'click', handler: handler, opts: true, __step: true };
            el.addEventListener('click', handler, true);
            listeners.push(rec);
          }
          // Missing target: no listener armed; step stays user-driven (never wedge).
          return;
        }
        if (adv.type === 'wait') {
          setPoll(function () { if (conditionHolds(adv)) { next(); } }, POLL_MS);
          return;
        }
        if (adv.type === 'auto') {
          setTimer(function () { next(); }, adv.ms);
        }
        // 'manual': nothing armed; user advances.
      }

      // ---- Read-through reveal ------------------------------------------------
      // The step narration reads through character-by-character (typewriter
      // sweep) so the visitor's eye tracks the text as it lands. The interval
      // is tracked like every other timer: disarm() (every step transition)
      // and destroy() both kill it, and it self-removes on completion so
      // trackedTimerCount stays exact. Reduced motion: full text instantly.
      function revealBody(text) {
        if (reducedMotion || !text || text.length < 2) {
          bodyEl.textContent = text || '';
          return;
        }
        bodyEl.textContent = '';
        var i = 0;
        var id = setInterval(function () {
          if (destroyed) { clearInterval(id); return; }
          i += 2;
          if (i >= text.length) {
            bodyEl.textContent = text;
            clearInterval(id);
            var k = intervals.indexOf(id);
            if (k !== -1) { intervals.splice(k, 1); }
            return;
          }
          bodyEl.textContent = text.slice(0, i);
        }, 18);
        intervals.push(id);
      }

      // ---- Render -------------------------------------------------------------
      function render() {
        if (index < 0 || index >= steps.length) { return; }
        var step = steps[index];
        if (!card) { buildCard(); }
        titleEl.textContent = step.title;
        counterEl.textContent = (index + 1) + ' / ' + steps.length;

        if (step.target) {
          positionHighlight(resolveTarget(step.target));
        } else {
          removeHighlight();
          lastTargetMissing = false;
        }

        // Announce the step change to assistive tech (full text immediately —
        // the visual read-through reveal below is decoration only).
        liveEl.textContent = 'Step ' + (index + 1) + ' of ' + steps.length +
          (step.title ? ': ' + step.title : '');

        disarm();
        revealBody(step.body);
        armStep();
        focusCard();
      }

      // ---- Accessible card ----------------------------------------------------
      function buildCard() {
        card = document.createElement('div');
        card.id = CARD_ID;
        card.setAttribute('role', 'dialog');
        card.setAttribute('aria-modal', 'true');
        card.setAttribute('aria-label', 'Walkthrough');
        card.setAttribute('tabindex', '-1');
        var cs = card.style;
        cs.position = 'fixed';
        cs.right = '16px';
        cs.bottom = '16px';
        cs.maxWidth = '320px';
        cs.padding = '14px 16px';
        cs.background = '#1e1e24';
        cs.color = '#f5f5f7';
        cs.font = '14px/1.45 system-ui, sans-serif';
        cs.borderRadius = '10px';
        cs.boxShadow = '0 8px 28px rgba(0,0,0,0.4)';
        cs.zIndex = '2147483601';

        counterEl = document.createElement('div');
        counterEl.setAttribute('aria-label', 'Step position');
        counterEl.style.opacity = '0.6';
        counterEl.style.fontSize = '12px';
        counterEl.style.marginBottom = '6px';
        card.appendChild(counterEl);

        titleEl = document.createElement('h2');
        titleEl.style.margin = '0 0 6px';
        titleEl.style.fontSize = '15px';
        card.appendChild(titleEl);

        bodyEl = document.createElement('div');
        bodyEl.style.marginBottom = '12px';
        card.appendChild(bodyEl);

        // Polite live region: announces step transitions to screen readers.
        liveEl = document.createElement('div');
        liveEl.id = LIVE_ID;
        liveEl.setAttribute('role', 'status');
        liveEl.setAttribute('aria-live', 'polite');
        liveEl.setAttribute('aria-atomic', 'true');
        // Visually hidden but readable by assistive tech.
        var ls = liveEl.style;
        ls.position = 'absolute';
        ls.width = '1px';
        ls.height = '1px';
        ls.margin = '-1px';
        ls.padding = '0';
        ls.overflow = 'hidden';
        ls.clip = 'rect(0 0 0 0)';
        ls.whiteSpace = 'nowrap';
        ls.border = '0';
        card.appendChild(liveEl);

        var nav = document.createElement('div');
        nav.style.display = 'flex';
        nav.style.gap = '8px';
        nav.appendChild(makeButton('Prev', 'Previous step', prev));
        nav.appendChild(makeButton('Next', 'Next step', next));
        nav.appendChild(makeButton('Close', 'Close walkthrough', destroy));
        card.appendChild(nav);

        // Always-on keyboard map on the card (focus is trapped within it):
        //   Enter / ArrowRight / ArrowDown → next
        //   ArrowLeft / ArrowUp            → prev
        //   Escape                          → close
        //   Tab / Shift+Tab                 → cycle focus within the card (trap)
        var keydown = function (e) { onKeyDown(e); };
        keydown.__step = false;
        card.addEventListener('keydown', keydown);
        listeners.push({ target: card, type: 'keydown', handler: keydown, opts: undefined });

        (document.body || document.documentElement).appendChild(card);

        // Re-anchor the highlight as the page scrolls/resizes/mutates.
        var onView = function () { reanchor(); };
        addListener(window, 'scroll', onView, true);
        addListener(window, 'resize', onView, true);
        if (observer) {
          var root = document.documentElement || document.body;
          if (root) { observer.observe(root, { childList: true, subtree: true, attributes: true }); }
        }
      }

      function makeButton(label, ariaLabel, handler) {
        var b = document.createElement('button');
        b.type = 'button';
        b.textContent = label;
        b.setAttribute('aria-label', ariaLabel);
        var bs = b.style;
        bs.flex = '1';
        bs.padding = '6px 8px';
        bs.border = '0';
        bs.borderRadius = '6px';
        bs.background = '#33333c';
        bs.color = '#f5f5f7';
        bs.cursor = 'pointer';
        bs.font = 'inherit';
        var click = function () { handler(); };
        b.addEventListener('click', click);
        listeners.push({ target: b, type: 'click', handler: click, opts: undefined });
        return b;
      }

      function focusableInCard() {
        if (!card) { return []; }
        var nodes = card.querySelectorAll('button, [tabindex]');
        var out = [];
        for (var i = 0; i < nodes.length; i++) {
          if (nodes[i].getAttribute('tabindex') !== '-1' || nodes[i] === card) { /* include */ }
          out.push(nodes[i]);
        }
        return out;
      }

      function focusCard() {
        // Move focus to the card on show (so the dialog is where keyboard lands).
        if (card && typeof card.focus === 'function') {
          try { card.focus(); } catch (e) { /* ignore */ }
        }
      }

      function onKeyDown(e) {
        var key = e.key;
        if (key === 'Escape') {
          e.preventDefault();
          destroy();
          return;
        }
        if (key === 'Enter' || key === 'ArrowRight' || key === 'ArrowDown') {
          // Enter on a button lets the button's own handler run; only advance
          // when focus is on the card itself, not on Prev/Close.
          var t = e.target;
          if (key === 'Enter' && t && t.tagName === 'BUTTON') { return; }
          e.preventDefault();
          next();
          return;
        }
        if (key === 'ArrowLeft' || key === 'ArrowUp') {
          e.preventDefault();
          prev();
          return;
        }
        if (key === 'Tab') {
          // Focus trap: keep Tab / Shift+Tab cycling within the card.
          var f = card.querySelectorAll('button');
          if (!f.length) { e.preventDefault(); return; }
          var first = f[0];
          var last = f[f.length - 1];
          var active = document.activeElement;
          if (e.shiftKey) {
            if (active === first || active === card) { e.preventDefault(); last.focus(); }
          } else {
            if (active === last) { e.preventDefault(); first.focus(); }
          }
        }
      }

      // ---- Variant binding (applied BEFORE step 0) ----------------------------
      function applyVariant() {
        var vs = walkthrough && walkthrough.variantSet;
        var engineApi = opts.variantEngine ||
          (typeof window !== 'undefined' ? window.__variantEngine : null);
        if (!vs || !engineApi || typeof engineApi.create !== 'function') { return; }
        var binding = opts.routeBinding || vs.routeBinding || { when: 'any' };
        try {
          variantEngine = engineApi.create(vs, binding, { root: opts.root });
          var vid = opts.variantId ||
            (vs.variants && vs.variants.length ? vs.variants[0].id : null);
          if (vid) { variantEngine.apply(vid); }
        } catch (e) {
          try { console.warn('[WalkthroughPlayer] variant bind failed:', e); } catch (e2) {}
          variantEngine = null;
        }
      }

      // ---- Public player methods ----------------------------------------------
      function start() {
        if (destroyed || steps.length === 0) { return player; }
        // Record where focus was so we can restore it on close.
        try { lastFocused = document.activeElement; } catch (e) { lastFocused = null; }
        applyVariant();   // variant applied BEFORE the first step renders
        index = 0;
        render();
        return player;
      }
      function goTo(i) {
        if (destroyed || typeof i !== 'number' || i < 0 || i >= steps.length) { return player; }
        index = i;
        render();
        return player;
      }
      function next() {
        if (destroyed) { return player; }
        if (index < steps.length - 1) { index++; render(); }
        return player;
      }
      function prev() {
        if (destroyed) { return player; }
        if (index > 0) { index--; render(); }
        return player;
      }
      function destroy() {
        if (destroyed) { return; }
        destroyed = true;
        disarm();
        clearTimers();
        removeListeners(null); // remove EVERY tracked listener
        if (observer) { try { observer.disconnect(); } catch (e) {} }
        removeHighlight();
        if (card && card.parentNode) { card.parentNode.removeChild(card); }
        card = null;
        if (variantEngine) { try { variantEngine.destroy(); } catch (e) {} variantEngine = null; }
        // Restore focus to where it was before the player opened.
        if (lastFocused && typeof lastFocused.focus === 'function') {
          try { lastFocused.focus(); } catch (e) { /* ignore */ }
        }
      }

      var player = {
        start: start,
        next: next,
        prev: prev,
        goTo: goTo,
        activeIndex: function () { return index; },
        stepCount: function () { return steps.length; },
        activeVariant: function () { return variantEngine ? variantEngine.activeVariant() : null; },
        missingTarget: function () { return lastTargetMissing; },
        reducedMotion: function () { return reducedMotion; },
        destroy: destroy,
        trackedTimerCount: function () { return timers.length + intervals.length; },
        trackedListenerCount: function () { return listeners.length; }
      };
      return player;
    }

    window.__walkthroughViewer = {
      create: create,
      isSafeSelector: isSafeSelector,
      normalizeStep: normalizeStep,
      version: 'p5'
    };
  } catch (e) {
    try { console.error('[WalkthroughPlayer] init failed:', e); } catch (e2) {}
  }
})();

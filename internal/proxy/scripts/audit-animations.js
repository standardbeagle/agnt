// Animation / compositor-load audit — finds the class of performance bug that
// JS profilers are structurally blind to: work that runs entirely on the
// compositor thread. An infinite CSS animation never touches the DOM (no
// mutations), never runs script (no longtasks), and reads as "visible: true"
// to any point-in-time visual check — yet it forces the compositor to commit
// a frame at the display refresh rate forever, so the page never goes idle.
// On high-refresh HiDPI displays this pegs the browser's GPU process from a
// visually static page.
//
// DATA SOURCE: document.getAnimations() — the declarative animation registry.
// It reports every running CSS animation/transition/Web Animation regardless
// of whether anything observable changes, which is exactly why it works where
// MutationObserver and visual-state snapshots cannot.
//
// DETECTORS:
//   1. INFINITE-ANIMATION      — infinite-iteration running animation on a
//      visible element: the frame pump. The trigger.
//   2. LAYOUT-PROPERTY-ANIMATION — animation/transition touching a
//      layout-inducing property (width/top/margin/...): forces main-thread
//      layout every frame, strictly worse than a compositor-only pump.
//   3. VIEWPORT-OVERLAY-AMPLIFIER — fixed full-viewport textured overlay
//      (noise/grain layers): multiplies the cost of every commit.
//   4. BACKDROP-FILTER-AMPLIFIER  — backdrop-filter / large-area filter:
//      per-commit blur pass over everything behind it.
//   5. WILL-CHANGE-OVERUSE     — layer-promotion hints scattered widely:
//      compositing memory + management cost on every commit.
//
// Amplifiers are only escalated when a frame pump exists: a noise overlay
// over a genuinely idle page costs one paint, not one per commit.
//
// OPTIONS: {sampleMs: N} switches the audit to async and resolves with a
// requestAnimationFrame frame-count sample proving (or refuting) that the
// page idles. rAF evidence is one signal, not an oracle — a purely
// compositor-driven animation can commit without firing page rAF, and the
// GPU-process CPU number itself lives outside any page API.

(function() {
  'use strict';

  // === THRESHOLDS (named constants) ===
  var OVERLAY_VIEWPORT_RATIO = 0.9;   // fixed element covering ≥ this fraction of the viewport → overlay
  var FILTER_AREA_RATIO = 0.25;       // filter/backdrop-filter over ≥ this fraction of viewport → large-area
  var WILL_CHANGE_MAX = 6;            // distinct will-change elements above this → overuse
  var MAX_ELEMENT_SCAN = 4000;        // bound the amplifier style sweep on huge DOMs
  var IDLE_FPS_CEILING = 5;           // sampled fps at/below this → page idles (no pump finding)

  // Properties whose animation forces main-thread layout each frame. Substring
  // match on the animated property name (covers margin-left, border-width...).
  var LAYOUT_PROPS = [
    'width', 'height', 'top', 'left', 'right', 'bottom',
    'margin', 'padding', 'border', 'flex-basis', 'font-size', 'inset'
  ];

  function computeFindingID(type, selector, message) {
    return window.__devtool_audit_utils.computeFindingID(type, selector, message);
  }
  function registerFinding(id, selector) {
    window.__devtool_audit_utils.registerFinding(id, selector);
  }
  function calculateGrade(score) {
    return window.__devtool_audit_utils.calculateGrade(score);
  }
  function getSelector(el) {
    return window.__devtool_audit_utils.getSelector(el);
  }

  // Point-in-time visibility — same checks as visual.js, inlined so the audit
  // depends only on audit-utils. NOTE: this is used to SCOPE findings (an
  // animation on a display:none element costs nothing), not to detect the bug —
  // a pulsing element always reads visible; that blindness is why this audit
  // exists.
  function isRenderedVisible(el) {
    if (!el || el.nodeType !== 1 || !el.isConnected) return false;
    var s = window.getComputedStyle(el);
    if (s.display === 'none' || s.visibility === 'hidden') return false;
    if (parseFloat(s.opacity) === 0) return false;
    var r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return false;
    return true;
  }

  function isInfinite(anim) {
    try {
      var t = anim.effect && anim.effect.getTiming && anim.effect.getTiming();
      return !!t && t.iterations === Infinity;
    } catch (e) {
      return false;
    }
  }

  // Property names an effect animates. KeyframeEffect.getKeyframes() lists the
  // animated properties as extra keys on each keyframe.
  function animatedProperties(anim) {
    var props = {};
    try {
      var frames = anim.effect && anim.effect.getKeyframes && anim.effect.getKeyframes();
      if (!frames) return [];
      for (var i = 0; i < frames.length; i++) {
        for (var k in frames[i]) {
          if (k === 'offset' || k === 'computedOffset' || k === 'easing' || k === 'composite') continue;
          props[k] = true;
        }
      }
    } catch (e) { /* CSSTransition on some engines throws for getKeyframes */ }
    // A CSSTransition names its property directly even when keyframes are opaque.
    if (anim.transitionProperty) props[anim.transitionProperty] = true;
    return Object.keys(props);
  }

  function touchesLayoutProperty(props) {
    for (var i = 0; i < props.length; i++) {
      var p = String(props[i]).toLowerCase();
      for (var j = 0; j < LAYOUT_PROPS.length; j++) {
        if (p.indexOf(LAYOUT_PROPS[j]) === 0) return p;
      }
    }
    return null;
  }

  // === 1+2. Animation registry scan ===
  function scanAnimations(addFinding) {
    var anims = document.getAnimations();
    var pumpCount = 0;

    for (var i = 0; i < anims.length; i++) {
      var a = anims[i];
      if (a.playState !== 'running') continue;
      var el = a.effect && a.effect.target;
      if (!el || !isRenderedVisible(el)) continue;

      var sel = getSelector(el);
      var name = a.animationName || (a.transitionProperty ? 'transition:' + a.transitionProperty : a.constructor.name);
      var props = animatedProperties(a);
      var layoutProp = touchesLayoutProperty(props);

      if (layoutProp) {
        var lmsg = 'Animation "' + name + '" animates layout property "' + layoutProp +
          '" — forces main-thread style+layout every frame. Animate transform/opacity instead.';
        addFinding({
          id: computeFindingID('layout-property-animation', sel, lmsg),
          type: 'layout-property-animation',
          severity: 'error',
          selector: sel,
          message: lmsg,
          animation: name,
          properties: props
        });
      }

      if (isInfinite(a)) {
        pumpCount++;
        var imsg = 'Infinite animation "' + name + '" on a visible element keeps the compositor ' +
          'committing at display refresh rate — the page can never go idle. ' +
          'Gate it on state (run only while active), honor prefers-reduced-motion, or make it finite.';
        addFinding({
          id: computeFindingID('infinite-animation', sel, imsg),
          type: 'infinite-animation',
          severity: 'warning',
          selector: sel,
          message: imsg,
          animation: name,
          properties: props
        });
      }
    }
    return pumpCount;
  }

  // === 3+4+5. Amplifier sweep ===
  function scanAmplifiers(addFinding, pumpActive) {
    var vw = window.innerWidth, vh = window.innerHeight;
    var viewportArea = Math.max(1, vw * vh);
    var all = document.querySelectorAll('*');
    var n = Math.min(all.length, MAX_ELEMENT_SCAN);
    var truncated = all.length > MAX_ELEMENT_SCAN;
    var willChangeCount = 0;
    var willChangeSample = null;
    // When a frame pump is live, every commit repaints through the amplifier,
    // so the amplifier is an active multiplier, not a latent one.
    var ampSeverity = pumpActive ? 'error' : 'info';

    for (var i = 0; i < n; i++) {
      var el = all[i];
      var s = window.getComputedStyle(el);

      if (s.willChange && s.willChange !== 'auto') {
        willChangeCount++;
        if (!willChangeSample) willChangeSample = getSelector(el);
      }

      var hasBackdrop = s.backdropFilter && s.backdropFilter !== 'none';
      var hasFilter = s.filter && s.filter !== 'none';
      if (!hasBackdrop && !hasFilter &&
          !(s.position === 'fixed' || s.position === 'sticky')) continue;

      var r = el.getBoundingClientRect();
      var area = Math.max(0, r.width) * Math.max(0, r.height);

      if (hasBackdrop || hasFilter) {
        if (area / viewportArea >= FILTER_AREA_RATIO) {
          var prop = hasBackdrop ? 'backdrop-filter: ' + s.backdropFilter : 'filter: ' + s.filter;
          var fsel = getSelector(el);
          var fmsg = 'Large-area ' + prop + ' — a per-commit filter pass over everything behind it' +
            (pumpActive ? ', multiplied by an active infinite animation.' : '.') +
            ' Keep filters only where content actually changes behind them.';
          addFinding({
            id: computeFindingID('backdrop-filter-amplifier', fsel, fmsg),
            type: 'backdrop-filter-amplifier',
            severity: ampSeverity,
            selector: fsel,
            message: fmsg
          });
        }
      }

      if ((s.position === 'fixed' || s.position === 'sticky') &&
          r.width >= vw * OVERLAY_VIEWPORT_RATIO && r.height >= vh * OVERLAY_VIEWPORT_RATIO &&
          (s.backgroundImage !== 'none' || (parseFloat(s.opacity) > 0 && parseFloat(s.opacity) < 1))) {
        var osel = getSelector(el);
        var omsg = 'Full-viewport overlay (noise/grain layer?) — repainted on every compositor commit' +
          (pumpActive ? ', multiplied by an active infinite animation.' : '.') +
          ' Bake the texture into the background asset so it costs one paint, not one per frame.';
        addFinding({
          id: computeFindingID('viewport-overlay-amplifier', osel, omsg),
          type: 'viewport-overlay-amplifier',
          severity: ampSeverity,
          selector: osel,
          message: omsg
        });
      }
    }

    if (willChangeCount > WILL_CHANGE_MAX) {
      var wmsg = willChangeCount + ' elements carry will-change — each is a standing ' +
        'layer-promotion hint costing compositing memory on every commit. Apply will-change ' +
        'just-in-time and remove it when the interaction ends.';
      addFinding({
        id: computeFindingID('will-change-overuse', willChangeSample || '', wmsg),
        type: 'will-change-overuse',
        severity: 'warning',
        selector: willChangeSample || '',
        message: wmsg,
        count: willChangeCount
      });
    }

    return truncated;
  }

  // === Optional rAF idle sample ===
  // Bounded probe: in a backgrounded/occluded tab the browser throttles or
  // stops rAF entirely, so an unguarded rAF loop would never resolve — an
  // indefinite-blocking probe is a liveness lie. A setTimeout backstop
  // resolves with whatever was counted and flags the starvation honestly.
  function sampleFrames(sampleMs) {
    return new Promise(function(resolve) {
      var frames = 0;
      var start = performance.now();
      var done = false;
      function finish(starved) {
        if (done) return;
        done = true;
        var elapsed = Math.max(1, performance.now() - start);
        var result = {
          frames: frames,
          sampleMs: Math.round(elapsed),
          effectiveFps: Math.round(frames / (elapsed / 1000))
        };
        if (starved) result.rafStarved = true;
        resolve(result);
      }
      function tick(t) {
        if (done) return;
        frames++;
        if (t - start < sampleMs) { requestAnimationFrame(tick); return; }
        finish(false);
      }
      requestAnimationFrame(tick);
      setTimeout(function() { finish(true); }, sampleMs + 1000);
    });
  }

  function buildReport(findings, findingSelectors, meta, raw) {
    var score = 100;
    var errorCount = 0, warningCount = 0, infoCount = 0;
    for (var i = 0; i < findings.length; i++) {
      var sv = findings[i].severity;
      if (sv === 'error' || sv === 'critical') { errorCount++; score -= 15; }
      else if (sv === 'warning') { warningCount++; score -= 8; }
      else { infoCount++; score -= 2; }
    }
    if (score < 0) score = 0;
    var grade = calculateGrade(score);

    var summaryParts = [];
    if (meta.pumpCount > 0) {
      summaryParts.push(meta.pumpCount + ' infinite animation' + (meta.pumpCount === 1 ? '' : 's') +
        ' keep the compositor committing every frame');
    } else {
      summaryParts.push('No infinite animations pinning the compositor');
    }
    if (meta.frameSample) {
      if (meta.frameSample.rafStarved) {
        summaryParts.push('frame sample inconclusive — rAF throttled (backgrounded/occluded tab?)');
      } else {
        summaryParts.push('sampled ' + meta.frameSample.effectiveFps + ' fps over ' +
          meta.frameSample.sampleMs + 'ms' +
          (meta.frameSample.effectiveFps <= IDLE_FPS_CEILING ? ' (page idles)' : ' (page never idles)'));
      }
    }
    var actionable = errorCount + warningCount;
    if (actionable > 0) summaryParts.push(actionable + ' issue' + (actionable === 1 ? '' : 's') + ' to address');
    if (meta.truncated) summaryParts.push('style sweep truncated at ' + MAX_ELEMENT_SCAN + ' elements');
    var summary = summaryParts.join('. ');

    if (raw) {
      return {
        audit: 'animations',
        score: score,
        grade: grade,
        summary: summary,
        checkedAt: new Date().toISOString(),
        frameSample: meta.frameSample || null,
        findings: findings,
        findingSelectors: findingSelectors
      };
    }

    var byType = {};
    for (var gi = 0; gi < findings.length; gi++) {
      var gf = findings[gi];
      if (!byType[gf.type]) byType[gf.type] = [];
      if (byType[gf.type].length < 5) {
        byType[gf.type].push({
          id: gf.id,
          severity: gf.severity,
          selector: gf.selector,
          message: gf.message
        });
      }
    }
    return {
      audit: 'animations',
      score: score,
      grade: grade,
      summary: summary,
      checkedAt: new Date().toISOString(),
      frameSample: meta.frameSample || null,
      stats: {
        animationsRunning: meta.animationsRunning,
        errors: errorCount,
        warnings: warningCount,
        info: infoCount,
        totalIssues: findings.length
      },
      findingsByType: byType
    };
  }

  function auditAnimations(options) {
    options = options || {};
    var raw = options.raw === true;

    // Honest not-applicable path: without the registry the audit cannot see
    // the bug class at all, and reporting "A" would be a lie about coverage.
    if (typeof document.getAnimations !== 'function') {
      return {
        audit: 'animations',
        notApplicable: true,
        score: null,
        grade: null,
        summary: 'document.getAnimations() unavailable in this browser — compositor load not measurable.',
        checkedAt: new Date().toISOString(),
        stats: { animationsRunning: 0, errors: 0, warnings: 0, info: 0, totalIssues: 0 },
        findingsByType: {}
      };
    }

    var findings = [];
    var findingSelectors = {};
    function addFinding(f) {
      findingSelectors[f.id] = f.selector || '';
      registerFinding(f.id, f.selector || '');
      findings.push(f);
    }

    var pumpCount = scanAnimations(addFinding);
    var truncated = scanAmplifiers(addFinding, pumpCount > 0);
    var meta = {
      pumpCount: pumpCount,
      animationsRunning: document.getAnimations().length,
      truncated: truncated,
      frameSample: null
    };

    if (typeof options.sampleMs === 'number' && options.sampleMs > 0) {
      var capped = Math.min(options.sampleMs, 10000);
      return sampleFrames(capped).then(function(sample) {
        meta.frameSample = sample;
        return buildReport(findings, findingSelectors, meta, raw);
      });
    }

    return buildReport(findings, findingSelectors, meta, raw);
  }

  window.__devtool_audit_animations = {
    auditAnimations: auditAnimations
  };

  // Register into the shared audit namespace alongside the other audit modules
  // (mirrors audit-api / audit-loading).
  if (!window.__devtool) { window.__devtool = {}; }
  if (!window.__devtool.audit) { window.__devtool.audit = {}; }
  window.__devtool.audit.auditAnimations = auditAnimations;
})();

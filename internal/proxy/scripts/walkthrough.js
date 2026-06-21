// Walkthrough mode for DevTool
//
// Lets a coding agent run a LIVE DEMO of work just completed. The agent loads a
// JSON script (steps with narration + a target element + an advance rule); this
// module pops a floating, scrolling list of step cards that narrates the current
// state, highlights the matching app element, and advances in three ways:
//   * auto         — show for N ms then advance (watch mode)
//   * click-target — advance when the user clicks the highlighted element
//   * wait         — advance when an app-state condition holds
// Panel Next/Prev/restart are always available so the user can step manually.
//
// The walkthrough UI lives in the OUTER frame (the always-wrap chrome shell, or
// an unwrapped top-level page). The chrome shell persists across content-frame
// navigation, so a multi-step demo survives page changes for free. Targets are
// resolved in the active content frame (same-origin direct reach via the shell's
// frame registry). See docs/responsive-canonical-target.md §5 and
// docs/superpowers/specs/2026-06-20-walkthrough-design.md.
//
// Transport: the agent drives this via `proxy exec`, which lands in the active
// content frame. On a non-host frame this module installs a thin forwarding
// proxy to the parent host (window.parent.__devtool_walkthrough_host), so an
// exec'd `window.__devtool_walkthrough.start(...)` reaches the host impl.

(function() {
  'use strict';

  try {
    var role = window.__devtool_frame_role || '';
    var isTop;
    try { isTop = window.top === window.self; } catch (e) { isTop = false; }
    // Host = where the outer proxy UI lives: the chrome shell, or an unwrapped
    // top-level page. A wrapped content frame and any passive embed are not.
    var isHost = (role === 'chrome') || (role !== 'passive' && isTop && role !== 'chrome' && role === 'content') || (!role && isTop);

    if (!isHost) {
      installForwardingProxy();
      return;
    }

    installHost();
  } catch (e) {
    try { console.error('[DevTool] Walkthrough init failed:', e); } catch (e2) {}
  }

  // ---- Non-host: forward calls to the parent host (same-origin) -------------
  function installForwardingProxy() {
    function host() {
      try {
        if (window.parent && window.parent !== window && window.parent.__devtool_walkthrough_host) {
          return window.parent.__devtool_walkthrough_host;
        }
      } catch (e) { /* cross-origin parent */ }
      return null;
    }
    function fwd(method) {
      return function() {
        var h = host();
        if (!h || typeof h[method] !== 'function') {
          return { error: 'walkthrough host frame unavailable' };
        }
        try { return h[method].apply(h, arguments); }
        catch (e) { return { error: String(e) }; }
      };
    }
    window.__devtool_walkthrough = {
      load: fwd('load'),
      start: fwd('start'),
      stop: fwd('stop'),
      next: fwd('next'),
      prev: fwd('prev'),
      play: fwd('play'),
      pause: fwd('pause'),
      status: fwd('status'),
      list: fwd('list')
    };
  }

  // ---- Host: full implementation -------------------------------------------
  function installHost() {
    var STYLE_ID = '__wt_styles';
    var PANEL_ID = '__wt_panel';
    var LAUNCHER_ID = '__wt_launcher';
    var HILITE_ID = '__devtool_wt_highlight';
    var POLL_MS = 300;

    // Registered scripts (for user replay) + the active run.
    var scripts = {};            // id -> normalized script
    var run = null;              // { script, index, mode('auto'|'manual'), playing, timer, poll, armed }

    function core() { return window.__devtool_core || null; }

    function emit(event, data) {
      try {
        var c = core();
        if (c && typeof c.send === 'function') {
          var payload = { event: event, timestamp: Date.now() };
          for (var k in data) { if (data.hasOwnProperty(k)) { payload[k] = data[k]; } }
          c.send('walkthrough_event', payload);
        }
      } catch (e) { /* transport not ready */ }
    }

    function mountRoot() {
      return typeof window.__devtoolGetMountRoot === 'function'
        ? window.__devtoolGetMountRoot()
        : document.body;
    }
    function styleTarget() {
      var r = mountRoot();
      // Shadow root: append style to it; document.body fallback: use head.
      return (r && r.host) ? r : document.head;
    }

    // ---- Content-frame access (same-origin) --------------------------------
    function contentWin() {
      // Unwrapped page: we ARE the content. Wrapped: reach the active frame.
      if (role !== 'chrome') { return window; }
      try {
        var fr = window.__devtool_frames;
        if (fr && typeof fr.active === 'function') {
          var a = fr.active();
          if (a && a.win) { return a.win; }
        }
      } catch (e) { /* registry not ready */ }
      // Fallback: first content iframe in the shell document.
      try {
        var ifr = document.querySelector('iframe');
        if (ifr && ifr.contentWindow) { return ifr.contentWindow; }
      } catch (e) {}
      return null;
    }
    function contentDoc() {
      var w = contentWin();
      try { return w ? w.document : null; } catch (e) { return null; }
    }
    function queryTarget(selector) {
      if (!selector) { return null; }
      var d = contentDoc();
      if (!d) { return null; }
      try { return d.querySelector(selector); } catch (e) { return null; }
    }

    // ---- Highlight in the content frame ------------------------------------
    function clearHighlight() {
      var d = contentDoc();
      if (!d) { return; }
      try {
        var ex = d.getElementById(HILITE_ID);
        if (ex && ex.parentNode) { ex.parentNode.removeChild(ex); }
      } catch (e) {}
    }
    function highlight(selector) {
      clearHighlight();
      var el = queryTarget(selector);
      var d = contentDoc();
      var w = contentWin();
      if (!el || !d || !w) { return false; }
      try {
        el.scrollIntoView({ behavior: 'smooth', block: 'center', inline: 'center' });
      } catch (e) { try { el.scrollIntoView(); } catch (e2) {} }
      try {
        ensureContentStyle(d);
        var rect = el.getBoundingClientRect();
        var box = d.createElement('div');
        box.id = HILITE_ID;
        box.className = '__devtool-wt-hilite';
        var top = rect.top + (w.scrollY || w.pageYOffset || 0);
        var left = rect.left + (w.scrollX || w.pageXOffset || 0);
        box.style.top = top + 'px';
        box.style.left = left + 'px';
        box.style.width = rect.width + 'px';
        box.style.height = rect.height + 'px';
        (d.body || d.documentElement).appendChild(box);
        return true;
      } catch (e) { return false; }
    }
    function ensureContentStyle(d) {
      if (d.getElementById('__devtool_wt_content_style')) { return; }
      try {
        var s = d.createElement('style');
        s.id = '__devtool_wt_content_style';
        s.textContent =
          '.__devtool-wt-hilite{position:absolute;z-index:2147483600;pointer-events:none;' +
          'border:3px solid #6ea8fe;border-radius:6px;box-shadow:0 0 0 3px rgba(110,168,254,.35),0 0 18px rgba(110,168,254,.65);' +
          'animation:__devtoolWtPulse 1.4s ease-in-out infinite;transition:top .2s,left .2s,width .2s,height .2s;}' +
          '@keyframes __devtoolWtPulse{0%,100%{box-shadow:0 0 0 3px rgba(110,168,254,.35),0 0 18px rgba(110,168,254,.55);}' +
          '50%{box-shadow:0 0 0 6px rgba(110,168,254,.18),0 0 26px rgba(110,168,254,.85);}}';
        (d.head || d.documentElement).appendChild(s);
      } catch (e) {}
    }

    // ---- Advance wiring per step -------------------------------------------
    function disarm() {
      if (!run) { return; }
      if (run.timer) { clearTimeout(run.timer); run.timer = null; }
      if (run.poll) { clearInterval(run.poll); run.poll = null; }
      if (run.armed) {
        try {
          if (run.armed.el && run.armed.handler) {
            run.armed.el.removeEventListener('click', run.armed.handler, true);
          }
        } catch (e) {}
        run.armed = null;
      }
    }

    function armStep() {
      if (!run) { return; }
      var step = run.script.steps[run.index];
      if (!step) { return; }
      var adv = step.advance || { type: 'auto' };

      if (adv.type === 'click-target') {
        var el = queryTarget(step.target);
        if (el) {
          var handler = function() {
            // Real click still happens (no preventDefault) so the demo follows
            // real app behavior; we just advance the narration.
            advance('click');
          };
          el.addEventListener('click', handler, true);
          run.armed = { el: el, handler: handler };
        } else {
          // Target missing: fall back to manual so the demo never wedges.
          emit('warning', { stepIndex: run.index, message: 'click-target selector not found: ' + step.target });
        }
        return;
      }

      if (adv.type === 'wait') {
        run.poll = setInterval(function() {
          if (!run || !run.playing) { return; }
          if (conditionHolds(adv)) { advance('wait'); }
        }, POLL_MS);
        return;
      }

      // default: auto timer
      var ms = (adv.type === 'auto' && typeof adv.ms === 'number' && adv.ms > 0) ? adv.ms : 5000;
      if (run.playing) {
        run.timer = setTimeout(function() { advance('timer'); }, ms);
      }
    }

    function conditionHolds(adv) {
      try {
        if (adv.when === 'url-contains') {
          var w = contentWin();
          var href = w ? String(w.location.href) : '';
          return adv.value ? href.indexOf(String(adv.value)) !== -1 : false;
        }
        if (adv.when === 'element-present') {
          return !!queryTarget(adv.value);
        }
        if (adv.when === 'element-visible') {
          var el = queryTarget(adv.value);
          if (!el) { return false; }
          var r = el.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        }
      } catch (e) {}
      return false;
    }

    // ---- Step navigation ----------------------------------------------------
    function gotoStep(index, how) {
      if (!run) { return; }
      disarm();
      var total = run.script.steps.length;
      if (index < 0) { index = 0; }
      if (index >= total) {
        finish();
        return;
      }
      run.index = index;
      var step = run.script.steps[index];
      highlight(step.target);
      renderPanel();
      emit('step', {
        scriptId: run.script.id,
        stepIndex: index,
        total: total,
        title: step.title || '',
        advance: (step.advance && step.advance.type) || 'auto',
        how: how || 'goto',
        mode: run.mode
      });
      armStep();
    }

    function advance(how) { if (run) { gotoStep(run.index + 1, how); } }

    function finish() {
      if (!run) { return; }
      var sid = run.script.id;
      disarm();
      clearHighlight();
      emit('finish', { scriptId: sid });
      run.playing = false;
      run.finished = true;
      renderPanel();
    }

    // ---- Public host API ----------------------------------------------------
    function normalizeScript(script) {
      if (!script || typeof script !== 'object') { throw new Error('script must be an object'); }
      if (typeof script === 'string') { script = JSON.parse(script); }
      if (!script.id) { throw new Error('script.id is required'); }
      if (!Array.isArray(script.steps) || !script.steps.length) { throw new Error('script.steps must be a non-empty array'); }
      script.steps.forEach(function(s, i) {
        var adv = s.advance || (s.advance = { type: 'auto' });
        if (['auto', 'click-target', 'wait'].indexOf(adv.type) === -1) {
          throw new Error('step ' + i + ': unknown advance.type ' + adv.type);
        }
        if (adv.type === 'click-target' && !s.target) {
          throw new Error('step ' + i + ': click-target requires a target selector');
        }
        if (adv.type === 'wait' && ['url-contains', 'element-present', 'element-visible'].indexOf(adv.when) === -1) {
          throw new Error('step ' + i + ': wait requires when in url-contains|element-present|element-visible');
        }
      });
      return script;
    }

    function parseArg(s) { return (typeof s === 'string') ? JSON.parse(s) : s; }

    var api = {
      load: function(script) {
        try {
          var norm = normalizeScript(parseArg(script));
          scripts[norm.id] = norm;
          renderLauncher();
          return { ok: true, id: norm.id, steps: norm.steps.length };
        } catch (e) { return { error: String(e.message || e) }; }
      },
      start: function(idOrScript, opts) {
        try {
          opts = parseArg(opts) || {};
          var script;
          if (typeof idOrScript === 'string' && scripts[idOrScript]) {
            script = scripts[idOrScript];
          } else {
            script = normalizeScript(parseArg(idOrScript));
            scripts[script.id] = script;
          }
          disarm();
          run = {
            script: script,
            index: -1,
            mode: (opts.mode === 'manual') ? 'manual' : 'auto',
            playing: (opts.mode !== 'manual'),
            timer: null, poll: null, armed: null, finished: false
          };
          renderLauncher();
          ensurePanel();
          emit('start', { scriptId: script.id, title: script.title || '', total: script.steps.length, mode: run.mode });
          gotoStep(0, 'start');
          return api.status();
        } catch (e) { return { error: String(e.message || e) }; }
      },
      stop: function() {
        if (run) { emit('stop', { scriptId: run.script.id, stepIndex: run.index }); }
        disarm();
        clearHighlight();
        run = null;
        removePanel();
        renderLauncher();
        return { ok: true };
      },
      next: function() { if (run) { gotoStep(run.index + 1, 'manual'); } return api.status(); },
      prev: function() { if (run) { gotoStep(run.index - 1, 'manual'); } return api.status(); },
      play: function() {
        if (run) { run.playing = true; renderPanel(); armStep(); emit('play', { scriptId: run.script.id, stepIndex: run.index }); }
        return api.status();
      },
      pause: function() {
        if (run) { run.playing = false; disarm(); highlight(run.script.steps[run.index] && run.script.steps[run.index].target); renderPanel(); emit('pause', { scriptId: run.script.id, stepIndex: run.index }); }
        return api.status();
      },
      status: function() {
        if (!run) {
          return { running: false, registered: Object.keys(scripts) };
        }
        var step = run.script.steps[run.index] || {};
        return {
          running: !run.finished,
          finished: !!run.finished,
          scriptId: run.script.id,
          title: run.script.title || '',
          stepIndex: run.index,
          total: run.script.steps.length,
          mode: run.mode,
          playing: !!run.playing,
          stepTitle: step.title || '',
          advance: (step.advance && step.advance.type) || 'auto'
        };
      },
      list: function() {
        return Object.keys(scripts).map(function(id) {
          return { id: id, title: scripts[id].title || '', steps: scripts[id].steps.length };
        });
      }
    };

    window.__devtool_walkthrough_host = api;
    window.__devtool_walkthrough = api;

    // ---- Panel UI (host frame, shadow-root mounted) ------------------------
    function ensureStyles() {
      var root = styleTarget();
      var ex = (root.querySelector && root.querySelector('#' + STYLE_ID)) || document.getElementById(STYLE_ID);
      if (ex) { return; }
      var s = document.createElement('style');
      s.id = STYLE_ID;
      s.textContent =
        '#' + PANEL_ID + '{position:fixed;right:16px;bottom:16px;width:340px;max-height:60vh;z-index:2147483600;' +
        'display:flex;flex-direction:column;background:#11151c;color:#e6edf3;border:1px solid #2b3340;border-radius:12px;' +
        'box-shadow:0 12px 40px rgba(0,0,0,.5);font:13px/1.5 -apple-system,BlinkMacSystemFont,Segoe UI,Roboto,sans-serif;overflow:hidden;}' +
        '#' + PANEL_ID + ' .wt-head{display:flex;align-items:center;gap:8px;padding:10px 12px;background:#161b22;border-bottom:1px solid #2b3340;}' +
        '#' + PANEL_ID + ' .wt-title{flex:1;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;}' +
        '#' + PANEL_ID + ' .wt-btn{cursor:pointer;border:0;background:#222b36;color:#e6edf3;border-radius:6px;padding:4px 8px;font-size:13px;}' +
        '#' + PANEL_ID + ' .wt-btn:hover{background:#2d3947;}' +
        '#' + PANEL_ID + ' .wt-list{overflow-y:auto;padding:8px;display:flex;flex-direction:column;gap:6px;}' +
        '#' + PANEL_ID + ' .wt-card{padding:8px 10px;border:1px solid #2b3340;border-radius:8px;background:#0d1117;opacity:.55;transition:opacity .2s,border-color .2s;}' +
        '#' + PANEL_ID + ' .wt-card.active{opacity:1;border-color:#6ea8fe;background:#10203a;}' +
        '#' + PANEL_ID + ' .wt-card.done{opacity:.4;}' +
        '#' + PANEL_ID + ' .wt-card h4{margin:0 0 3px;font-size:13px;font-weight:600;}' +
        '#' + PANEL_ID + ' .wt-card p{margin:0;font-size:12px;color:#9fb0c3;white-space:pre-wrap;}' +
        '#' + PANEL_ID + ' .wt-foot{padding:6px 10px;font-size:11px;color:#7d8da0;border-top:1px solid #2b3340;}' +
        '#' + LAUNCHER_ID + '{position:fixed;right:16px;bottom:16px;z-index:2147483600;cursor:pointer;border:0;' +
        'background:#1f6feb;color:#fff;border-radius:999px;padding:8px 14px;font:600 13px -apple-system,Segoe UI,Roboto,sans-serif;' +
        'box-shadow:0 8px 24px rgba(0,0,0,.4);}' +
        '#' + LAUNCHER_ID + ':hover{background:#388bfd;}';
      (root.appendChild ? root : document.head).appendChild(s);
    }

    function ensurePanel() { ensureStyles(); renderPanel(); }

    function removePanel() {
      var p = mountRoot().querySelector ? mountRoot().querySelector('#' + PANEL_ID) : document.getElementById(PANEL_ID);
      if (p && p.parentNode) { p.parentNode.removeChild(p); }
    }

    function renderPanel() {
      if (!run) { return; }
      ensureStyles();
      var root = mountRoot();
      var panel = (root.querySelector && root.querySelector('#' + PANEL_ID)) || document.getElementById(PANEL_ID);
      if (!panel) {
        panel = document.createElement('div');
        panel.id = PANEL_ID;
        (root.appendChild ? root : document.body).appendChild(panel);
      }
      var s = run.script;
      var playLabel = run.playing ? '⏸' : '▶';
      var cards = s.steps.map(function(step, i) {
        var cls = 'wt-card' + (i === run.index ? ' active' : (i < run.index ? ' done' : ''));
        return '<div class="' + cls + '" data-i="' + i + '">' +
          '<h4>' + (i + 1) + '. ' + esc(step.title || '') + '</h4>' +
          (step.body ? '<p>' + esc(step.body) + '</p>' : '') + '</div>';
      }).join('');
      var foot = run.finished ? 'Done' :
        ('Step ' + (run.index + 1) + '/' + s.steps.length + ' · ' + run.mode +
         (run.playing ? ' · playing' : ' · paused'));
      panel.innerHTML =
        '<div class="wt-head">' +
          '<div class="wt-title">' + esc(s.title || 'Walkthrough') + '</div>' +
          '<button class="wt-btn" data-act="prev" title="Previous">‹</button>' +
          '<button class="wt-btn" data-act="play" title="Play/Pause">' + playLabel + '</button>' +
          '<button class="wt-btn" data-act="next" title="Next">›</button>' +
          '<button class="wt-btn" data-act="stop" title="Close">✕</button>' +
        '</div>' +
        '<div class="wt-list">' + cards + '</div>' +
        '<div class="wt-foot">' + esc(foot) + '</div>';

      // Wire controls + card clicks.
      var btns = panel.querySelectorAll('.wt-btn');
      for (var b = 0; b < btns.length; b++) {
        btns[b].onclick = function() {
          var act = this.getAttribute('data-act');
          if (act === 'next') { api.next(); }
          else if (act === 'prev') { api.prev(); }
          else if (act === 'stop') { api.stop(); }
          else if (act === 'play') { run && run.playing ? api.pause() : api.play(); }
        };
      }
      var cardsEls = panel.querySelectorAll('.wt-card');
      for (var c = 0; c < cardsEls.length; c++) {
        cardsEls[c].onclick = function() {
          var i = parseInt(this.getAttribute('data-i'), 10);
          if (!isNaN(i)) { gotoStep(i, 'manual'); }
        };
      }
      // Auto-scroll active card into view.
      var active = panel.querySelector('.wt-card.active');
      if (active && active.scrollIntoView) {
        try { active.scrollIntoView({ block: 'nearest', behavior: 'smooth' }); } catch (e) {}
      }
    }

    function renderLauncher() {
      var root = mountRoot();
      var existing = (root.querySelector && root.querySelector('#' + LAUNCHER_ID)) || document.getElementById(LAUNCHER_ID);
      var ids = Object.keys(scripts);
      // Show the launcher only when something is registered and nothing running.
      if (run || !ids.length) {
        if (existing && existing.parentNode) { existing.parentNode.removeChild(existing); }
        return;
      }
      ensureStyles();
      if (!existing) {
        existing = document.createElement('button');
        existing.id = LAUNCHER_ID;
        (root.appendChild ? root : document.body).appendChild(existing);
      }
      var first = scripts[ids[0]];
      existing.textContent = '▶ ' + (first.title || 'Replay walkthrough');
      existing.onclick = function() { api.start(ids[0]); };
    }

    function esc(s) {
      return String(s).replace(/[&<>"]/g, function(ch) {
        return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[ch];
      });
    }
  }
})();

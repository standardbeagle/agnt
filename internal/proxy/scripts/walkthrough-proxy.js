// Walkthrough forwarding proxy — the content-side half of walkthrough.js.
//
// Loads in EVERY frame (shared role set). In a non-host frame (wrapped content
// frame, passive embed) it installs a window.__devtool_walkthrough surface that
// forwards every call to the parent host frame's __devtool_walkthrough_host —
// installed by walkthrough.js, which ships only in the chrome and passive
// bundles. In a host frame it does nothing; walkthrough.js installs the real
// implementation there. The host-detection predicate below must stay in
// lockstep with the one in walkthrough.js.
(function() {
  'use strict';
  try {
    var role = window.__devtool_frame_role || '';
    var isTop;
    try { isTop = window.top === window.self; } catch (e) { isTop = false; }
    // Host = where the outer proxy UI lives: the chrome shell, or an unwrapped
    // top-level page. A wrapped content frame and any passive embed are not.
    var isHost = (role === 'chrome') || (role !== 'passive' && isTop && role !== 'chrome' && role === 'content') || (!role && isTop);

    if (isHost) {
      return; // walkthrough.js installs the host implementation here
    }

    installForwardingProxy();
  } catch (e) {
    try { console.error('[DevTool] Walkthrough proxy init failed:', e); } catch (e2) {}
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
})();

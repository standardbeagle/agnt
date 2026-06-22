(function() {
  'use strict';

  // Frame role + registry primitive for the proxy "always-wrap" model.
  //
  // The proxy renders a top-level navigation as an outer chrome shell whose body
  // is a single content <iframe>; the real page loads inside that frame. This
  // module computes which role the current frame plays and, in the shell,
  // maintains the registry of live content frames + the active-target pointer,
  // and syncs the browser URL bar to the active content frame's navigations.
  //
  // Role *gating* of the telemetry/UI runtime (core / interaction / indicator /
  // error+fetch+xhr+mutation hooks) lands in Slice 3 — this slice only
  // establishes the primitive. See docs/responsive-canonical-target.md §5.

  var FRAME_PARAM = '__devtool_frame';

  // resolveRole returns 'chrome' | 'content' | 'passive' for this frame.
  //  - chrome  : the always-wrap outer shell (declares window.__devtool_role).
  //  - content : a wrapped content frame (URL carries the marker) OR an
  //              unwrapped top-level page (fallback path — it IS the page).
  //  - passive : any other nested frame we do not own (foreign embed).
  function resolveRole() {
    if (window.__devtool_role === 'chrome') { return 'chrome'; }
    if (window.__devtool_role === 'content') { return 'content'; }
    if (hasFrameMarker()) { return 'content'; }
    // No marker: the app may have stripped it (redirect / router URL-normalize)
    // on an in-frame navigation. We still own this frame iff we ARE the shell's
    // content <iframe> — a same-origin direct signal that survives marker loss:
    // window.frameElement is the embedding element (null/throws cross-origin),
    // and our shell stamps it with id=__devtool_content_frame.
    if (isShellContentFrame()) { return 'content'; }
    // Otherwise: a top frame with no marker is an unwrapped page (canonical
    // content); a nested unmarked frame we did not stamp is a foreign embed.
    var isTop;
    try { isTop = window.top === window.self; } catch (e) { isTop = false; }
    return isTop ? 'content' : 'passive';
  }

  // isShellContentFrame reports whether this window is the chrome shell's own
  // content <iframe> (id=__devtool_content_frame). Same-origin only: frameElement
  // is null cross-origin and a foreign embed has a different/absent id. Durable
  // across in-frame navigations that drop the URL marker.
  function isShellContentFrame() {
    try {
      var fe = window.frameElement; // throws or null when cross-origin
      return !!fe && fe.id === '__devtool_content_frame';
    } catch (e) { return false; }
  }

  function hasFrameMarker() {
    try { return new URLSearchParams(window.location.search).has(FRAME_PARAM); }
    catch (e) { return window.location.search.indexOf(FRAME_PARAM + '=') !== -1; }
  }

  function frameId() {
    if (window.__devtool_frame_id) { return window.__devtool_frame_id; }
    try {
      var v = new URLSearchParams(window.location.search).get(FRAME_PARAM);
      if (v) { window.__devtool_frame_id = v; return v; }
    } catch (e) { /* unparsable search — fall through */ }
    return '';
  }

  var role = resolveRole();

  // Stable role global. Other modules (core, interaction, ...) gate their
  // telemetry/UI runtime on this. It lives on its own global — NOT on
  // window.__devtool — because api.js / responsive.js reassign window.__devtool
  // wholesale and would otherwise clobber the role accessor. frames.js loads
  // first in the bundle, so this is set before any runtime init reads it.
  window.__devtool_frame_role = role;

  // Expose the primitive on the shared namespace too (best-effort convenience).
  // Created defensively; window.__devtool may be reassigned later by api.js.
  window.__devtool = window.__devtool || {};
  window.__devtool.role = function() { return window.__devtool_frame_role; };
  window.__devtool.frameId = frameId;
  window.__devtool.FRAME_PARAM = FRAME_PARAM;

  if (role === 'chrome') {
    setupShell();
  } else if (role === 'content') {
    registerWithShell();
  }
  // passive: do nothing — foreign embeds stay quiet.

  // ---- Shell side: registry + active pointer + URL-bar nav-sync ----
  function setupShell() {
    var registry = {};      // frameId -> { frameId, url, win }
    var activeId = null;

    window.__devtool_frames = {
      register: function(info) {
        if (!info || !info.frameId) { return null; }
        registry[info.frameId] = {
          frameId: info.frameId,
          url: info.url || '',
          win: info.win || null
        };
        if (activeId === null) { activeId = info.frameId; }
        return info.frameId;
      },
      deregister: function(id) {
        delete registry[id];
        if (activeId === id) {
          var keys = Object.keys(registry);
          activeId = keys.length ? keys[0] : null;
        }
      },
      setActive: function(id) { if (registry[id]) { activeId = id; } },
      active: function() { return activeId ? registry[activeId] : null; },
      activeId: function() { return activeId; },
      get: function(id) { return registry[id] || null; },
      list: function() {
        return Object.keys(registry).map(function(k) { return registry[k]; });
      }
    };

    // Reflect the active content frame's URL in the shell's address bar via the
    // History API, so back/forward + bookmarks track the content, not the shell.
    // Same-origin direct reach (the proxy serves both frames). Never let a URL
    // sync failure break the page.
    window.__devtool_sync_url = function(contentUrl) {
      if (!contentUrl) { return; }
      try {
        var u = new URL(contentUrl, window.location.href);
        u.searchParams.delete(FRAME_PARAM); // show the real page URL
        var shown = u.pathname + (u.search || '') + (u.hash || '');
        window.history.replaceState(window.history.state, '', shown);
      } catch (e) { /* swallow — URL sync is best-effort */ }
    };

    // Resize the live content frame from the shell (observed from outside): the
    // agent sets a viewport size and the page reflows in place — no reload, no
    // second iframe. w<=0 resets to the shell's full-bleed layout. Returns the
    // applied size so the caller can confirm and then run audits against the
    // (now resized) content frame.
    window.__devtool_resize_content = function(w, h) {
      var f = document.getElementById('__devtool_content_frame');
      if (!f) { return { ok: false, error: 'no content frame' }; }
      if (!w || w <= 0) {
        f.style.cssText = ''; // CSS rule (#__devtool_content_frame) re-applies full-bleed
        return { ok: true, reset: true, width: window.innerWidth, height: window.innerHeight };
      }
      f.style.position = 'fixed';
      f.style.inset = 'auto';
      f.style.top = '0';
      f.style.left = '50%';
      f.style.transform = 'translateX(-50%)';
      f.style.width = w + 'px';
      f.style.height = (h && h > 0) ? (h + 'px') : '100%';
      f.style.border = '1px solid #3a3f47';
      f.style.background = '#ffffff';
      return { ok: true, width: w, height: (h && h > 0) ? h : window.innerHeight };
    };
  }

  // ---- Content side: register with the shell (same-origin direct reach) ----
  function registerWithShell() {
    var id = frameId();
    var shell;
    try { shell = window.parent; } catch (e) { return; } // cross-origin: stay quiet
    if (!shell || shell === window || !shell.__devtool_frames) { return; }

    try { shell.__devtool_frames.register({ frameId: id, url: window.location.href, win: window }); }
    catch (e) { return; }

    syncUp();
    window.addEventListener('popstate', syncUp);
    window.addEventListener('hashchange', syncUp);
    wrapHistory('pushState');
    wrapHistory('replaceState');

    // Navigation API (Chromium): fires for pushState/replaceState/navigate/
    // traverse at the browser level, regardless of who owns history.pushState.
    // This catches (a) routers that use navigation.navigate() instead of the
    // History API and (b) routers that re-patch history.pushState AFTER us,
    // which would otherwise drop our wrapper's syncUp call. Guarded — absent in
    // Firefox/Safari, where the history wrap + popstate path remains the cover.
    if (window.navigation && typeof window.navigation.addEventListener === 'function') {
      try { window.navigation.addEventListener('currententrychange', syncUp); }
      catch (e) { /* unsupported event — history wrap still covers it */ }
    }

    // Re-patch defense for browsers without the Navigation API: a router that
    // wraps history.pushState after frames.js runs would clobber our wrapper.
    // Re-assert the wrap a few times after load so our syncUp survives. Cheap,
    // idempotent (wrapHistory no-ops when already ours), and bounded.
    ensureHistoryWrapped();
    window.addEventListener('load', ensureHistoryWrapped);
    [0, 300, 1000, 3000].forEach(function(ms) { setTimeout(ensureHistoryWrapped, ms); });

    window.addEventListener('pagehide', function() {
      try { shell.__devtool_frames.deregister(id); } catch (e) { /* shell gone */ }
    });

    // Tell the proxy this content frame is the active interaction target so MCP
    // exec / audits default to it. Report on focus + pointer, and once on load
    // (retrying until core's WS is open — frames.js runs before core connects).
    // See docs/responsive-canonical-target.md §5.3.
    window.addEventListener('focus', reportActive, true);
    window.addEventListener('pointerdown', reportActive, true);
    var tries = 0;
    (function initialReport() {
      if (reportActive() || tries++ > 50) { return; }
      setTimeout(initialReport, 100);
    })();
  }

  // reportActive sends a frame_active signal over core's WS. Returns true once
  // the send was actually dispatched (WS open), false while still connecting.
  function reportActive() {
    try {
      var core = window.__devtool_core;
      if (core && typeof core.send === 'function') {
        return core.send('frame_active', { frame_id: frameId() }) === true;
      }
    } catch (e) { /* core not ready / WS closed */ }
    return false;
  }

  function syncUp() {
    try {
      var shell = window.parent;
      if (shell && shell !== window && typeof shell.__devtool_sync_url === 'function') {
        shell.__devtool_sync_url(window.location.href);
      }
    } catch (e) { /* cross-origin / shell gone */ }
  }

  // Re-assert our history wrap. Idempotent: wrapHistory no-ops when the current
  // method is already ours. If a router replaced our wrapper with the native
  // method, this re-wraps it; if the router wrapped OUR wrapper (ours still in
  // the chain), syncUp still fires and wrapHistory leaves it alone.
  function ensureHistoryWrapped() {
    wrapHistory('pushState');
    wrapHistory('replaceState');
  }

  // Patch history.pushState/replaceState so SPA route changes inside the content
  // frame also sync the shell URL bar (popstate does not fire for pushState).
  function wrapHistory(method) {
    try {
      var orig = window.history[method];
      if (typeof orig !== 'function' || orig.__devtool_wrapped) { return; }
      var wrapped = function() {
        var r = orig.apply(this, arguments);
        syncUp();
        return r;
      };
      wrapped.__devtool_wrapped = true;
      window.history[method] = wrapped;
    } catch (e) { /* history not writable */ }
  }
})();

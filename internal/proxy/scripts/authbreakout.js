(function() {
  'use strict';

  // OAuth breakout for the always-wrap frame model.
  //
  // Identity providers (login.microsoftonline.com, figma.com/oauth, ...)
  // refuse to render inside an iframe, so an auth redirect that happens in
  // the content frame dead-ends. When window.__devtool_auth_config is set
  // (from the project's .agnt.kdl auth-breakout block, injected by the Go
  // side), a matching navigation is carried out in a top-level window and
  // the callback URL is replayed into the content iframe — same tab, so
  // sessionStorage state (e.g. MSAL's request nonce) survives.
  //
  // Three cooperating parts, all in this shared-role module:
  //  - chrome shell: window.__devtool_auth.{breakout,complete}
  //  - content frame: navigation interception → parent breakout
  //  - popup return: relay the callback URL to the opener shell and close
  //
  // Server-side 3xx redirects to the IdP are intercepted by the proxy itself
  // (rewrite.go interceptAuthRedirect) and served as a stub page that calls
  // parent.__devtool_auth.breakout — that path needs no config here.

  var cfg = window.__devtool_auth_config || null;
  var role = window.__devtool_frame_role;
  var POPUP_NAME = '__devtool_auth';
  var POPUP_KEY = '__devtool_auth_popup';
  var FRAME_PARAM = '__devtool_frame';

  // Marks the current window as an auth popup. Two independent signals,
  // because neither is universally durable across the proxy→IdP→proxy round
  // trip the popup makes:
  //   window.name — survives in Chrome (verified by the e2e round-trip test),
  //     but engines with anti-tracking name-clearing (Safari/ITP) drop it on a
  //     cross-origin navigation.
  //   sessionStorage — window.open clones the opener's sessionStorage into the
  //     new browsing context for the opener's origin, so the marker is there
  //     when the popup returns to that origin. Blocked when storage is denied.
  // Either one identifies the window; both are cleared once the relay fires.
  function markAuthPopup(w) {
    try { w.sessionStorage.setItem(POPUP_KEY, '1'); } catch (e) { /* storage blocked */ }
  }
  function clearAuthPopupMark() {
    try { window.sessionStorage.removeItem(POPUP_KEY); } catch (e) { /* storage blocked */ }
  }
  function hasAuthPopupMark() {
    try { return window.sessionStorage.getItem(POPUP_KEY) === '1'; } catch (e) { return false; }
  }

  // ---- Popup return relay ----
  // Runs in the popup's top-level document after the IdP redirected back to
  // the app origin (which the proxy wrapped again — this code executes in
  // that shell, or in the raw page when unwrapped). The opener check is what
  // keeps the shell that *opened* the popup from matching its own marker: a
  // shell has no opener.
  function isAuthPopupReturn() {
    try {
      // The shell document carries an inline copy of this relay
      // (injector.go authPopupRelayJS) that runs at document parse time,
      // before this async bundle loads. If it already fired, the marker is
      // cleared and the callback already relayed — never relay twice.
      if (window.__devtool_auth_relayed) { return false; }
      if (window.top !== window.self || !window.opener || window.opener === window) { return false; }
      if (!window.opener.__devtool_auth) { return false; }
      return hasAuthPopupMark() || window.name === POPUP_NAME;
    } catch (e) { return false; } // opener cross-origin/gone
  }
  if (isAuthPopupReturn()) {
    // Clear first: if complete() throws we still must not leave a live marker
    // behind, or this window would relay again on its next same-origin load.
    clearAuthPopupMark();
    try {
      window.opener.__devtool_auth.complete(window.location.href);
      document.title = 'Authentication complete';
      window.close();
    } catch (e) { /* opener rejected — leave the page to render normally */ }
    return;
  }

  function matches(url) {
    if (!cfg || !cfg.patterns || !cfg.patterns.length) { return false; }
    var u = String(url).toLowerCase();
    for (var i = 0; i < cfg.patterns.length; i++) {
      // Wildcard-substring semantics, mirroring Go's AuthBreakout.MatchesURL:
      // escape regex metachars, '*' becomes '.*', unanchored match.
      var rx = new RegExp(String(cfg.patterns[i]).toLowerCase()
        .replace(/[.*+?^${}()|[\]\\]/g, function(c) { return c === '*' ? '.*' : '\\' + c; }));
      if (rx.test(u)) { return true; }
    }
    return false;
  }

  // ---- Chrome shell side ----
  if (role === 'chrome') {
    window.__devtool_auth = {
      config: cfg,

      // Carry an auth navigation out of the content iframe. popup mode opens
      // a named window (reused across retries); a blocked popup falls back to
      // navigating the whole shell — the return redirect re-enters the proxy
      // and gets wrapped again, so the flow still completes.
      breakout: function(url) {
        var mode = (cfg && cfg.mode) || 'popup';
        if (mode === 'popup') {
          try {
            // The popup inherits a copy of this window's sessionStorage at
            // open time, so the marker must be written before window.open and
            // removed after — the shell itself must not stay marked.
            markAuthPopup(window);
            var w = window.open(url, POPUP_NAME, 'popup,width=600,height=760');
            clearAuthPopupMark();
            if (w) { try { w.focus(); } catch (e) { /* focus best-effort */ } return { ok: true, mode: 'popup' }; }
          } catch (e) { clearAuthPopupMark(); /* window.open threw — fall through to top */ }
        }
        window.location.href = url;
        return { ok: true, mode: 'top' };
      },

      // Replay the IdP callback URL into the content iframe. The frame
      // marker is re-attached (query, never hash — the token often lives in
      // the fragment and must reach the app intact) so the proxy serves the
      // callback in content role.
      complete: function(callbackUrl) {
        var f = document.getElementById('__devtool_content_frame');
        if (!f) { window.location.href = callbackUrl; return { ok: true, mode: 'top' }; }
        try {
          var u = new URL(callbackUrl, window.location.href);
          if (!u.searchParams.has(FRAME_PARAM)) {
            var reg = window.__devtool_frames;
            var id = reg && reg.activeId && reg.activeId();
            if (id) { u.searchParams.set(FRAME_PARAM, id); }
          }
          f.contentWindow.location.replace(u.pathname + u.search + u.hash);
        } catch (e) {
          try { f.src = callbackUrl; } catch (e2) { return { ok: false, error: String(e2) }; }
        }
        try {
          if (typeof window.__devtool_sync_url === 'function') { window.__devtool_sync_url(callbackUrl); }
        } catch (e3) { /* URL sync best-effort */ }
        return { ok: true, mode: 'iframe' };
      }
    };
  }

  // ---- Content frame side: intercept auth navigations ----
  if (role === 'content' && cfg) {
    function breakout(url) {
      try {
        if (window.parent && window.parent !== window && window.parent.__devtool_auth) {
          window.parent.__devtool_auth.breakout(url);
          return;
        }
      } catch (e) { /* shell unreachable */ }
      // Unwrapped fallback / shell gone: top-level nav is inherently fine.
      try { window.top.location.href = url; } catch (e2) { window.location.href = url; }
    }

    function shouldIntercept(url) {
      try {
        var u = new URL(url, window.location.href);
        // Same-origin URLs go through the proxy and never need breakout.
        return u.origin !== window.location.origin && matches(u.href);
      } catch (e) { return false; }
    }

    // Navigation API (Chromium): catches location.assign/href, meta refresh,
    // form GETs — every in-frame navigation the page initiates.
    if (window.navigation && typeof window.navigation.addEventListener === 'function') {
      try {
        window.navigation.addEventListener('navigate', function(e) {
          try {
            if (!e.destination || e.cancelable === false) { return; }
            if (!shouldIntercept(e.destination.url)) { return; }
            e.preventDefault();
            breakout(e.destination.url);
          } catch (err) { /* never break page navigation */ }
        });
      } catch (e) { /* unsupported — anchor fallback below still covers links */ }
    }

    // Anchor fallback for browsers without the Navigation API. Capture phase
    // so the app's own handlers can't swallow the click first.
    document.addEventListener('click', function(e) {
      try {
        var t = e.target;
        var a = t && t.closest ? t.closest('a[href]') : null;
        if (!a || !shouldIntercept(a.href)) { return; }
        e.preventDefault();
        e.stopPropagation();
        breakout(a.href);
      } catch (err) { /* never break page clicks */ }
    }, true);

    // Manual escape hatch for app code / debugging.
    window.__devtool_auth_breakout = breakout;
  }
})();

// Public walkthrough BOOTSTRAP for the walkthrough-publish plane.
//
// The artifact shell served by /s/{token} (internal/proxy/public_routes.go
// serveArtifact) is deliberately empty: one <script src> for the RolePublic
// bundle and nothing else — no inlined token, no inlined walkthrough, no inline
// script (the CSP forbids one). Without this module the bundle's three members
// (variant-engine, walkthrough-viewer, feedback-client) all sit inert and a
// visitor sees a blank page. This is the ~one screen of glue that turns them on:
//
//   read token from window.location  →  fetch /s/{token}/walkthrough.json
//     →  window.__walkthroughViewer.create(wt).start()
//
// It ships in every role bundle like its three siblings, so it MUST be inert
// everywhere but the public plane. Two independent gates, both required:
//   1. window.__agnt_public_version — set by buildCombinedScript ONLY for
//      RolePublic (the dev bundles set their own dev-namespaced marker).
//   2. location.pathname matching the /s/{token} share route.
// A dev-proxied page fails gate 1; any other public path fails gate 2.
//
// Security (P1 spec §4/§6): the token is read from the URL the visitor already
// has — never stored, never logged, never put in a query string, never sent
// anywhere but its own same-origin path. The fetch is same-origin (`connect-src
// 'self'`), credentials-omitted, and its response is handed to the viewer, which
// re-validates every step through its own restricted grammar. Nothing here
// evaluates a response value as code or markup: the failure notice is built with
// textContent, and the walkthrough JSON is only ever passed to create().
//
// Failure is surfaced, never silent (Silent Failure Prohibition): a revoked or
// unknown token, a network failure, or a malformed artifact renders a plain
// text notice instead of leaving the visitor on a blank page.

(function () {
  'use strict';

  try {
    // Base64url token, sized to the 256-bit CSPRNG token create() mints (43
    // chars) with headroom. A path that does not match is not a share route.
    var SHARE_PATH_RE = /^\/s\/([A-Za-z0-9_-]{16,128})\/?$/;
    var NOTICE_ID = '__wt_public_notice';

    function isPublicPlane() {
      return typeof window.__agnt_public_version === 'string';
    }

    // shareToken returns the token from a /s/{token} pathname, or '' when the
    // current path is not a share route.
    function shareToken(pathname) {
      var m = SHARE_PATH_RE.exec(String(pathname || ''));
      return m ? m[1] : '';
    }

    // notice renders an inert, text-only message. Used for every failure mode so
    // the visitor is told what happened rather than staring at an empty page.
    function notice(text) {
      try {
        var host = document.body || document.documentElement;
        if (!host) { return; }
        var prev = document.getElementById(NOTICE_ID);
        if (prev && prev.parentNode) { prev.parentNode.removeChild(prev); }
        var el = document.createElement('div');
        el.id = NOTICE_ID;
        el.setAttribute('role', 'status');
        var s = el.style;
        s.position = 'fixed';
        s.left = '50%';
        s.top = '50%';
        s.transform = 'translate(-50%,-50%)';
        s.maxWidth = '32em';
        s.padding = '16px 20px';
        s.background = '#1e1e24';
        s.color = '#f5f5f7';
        s.font = '14px/1.5 system-ui, sans-serif';
        s.borderRadius = '10px';
        s.textAlign = 'center';
        el.textContent = text;
        host.appendChild(el);
      } catch (e) { /* nothing renderable: the console warning below stands */ }
    }

    function warn(msg, err) {
      try { console.warn('[WalkthroughPublic] ' + msg, err || ''); } catch (e) {}
    }

    // boot fetches the published artifact for token and starts the player.
    // Returns the player, or null when the plane/route/payload is not bootable —
    // the caller (and the tests) can distinguish "did not boot" from "booted".
    function boot(token) {
      if (!token) { return null; }
      var viewer = window.__walkthroughViewer;
      if (!viewer || typeof viewer.create !== 'function') {
        warn('walkthrough viewer missing from the public bundle');
        notice('This walkthrough could not be loaded.');
        return null;
      }
      // Same-origin, token-in-path (never a query string: query strings leak via
      // Referer and proxy logs). No credentials — the public plane is anonymous
      // and sets no cookies.
      fetch('/s/' + token + '/walkthrough.json', {
        credentials: 'omit',
        headers: { 'Accept': 'application/json' }
      }).then(function (res) {
        if (res.status === 404 || res.status === 410) {
          // Revoked, rotated, or never existed — the public plane returns 404
          // for all three, and so must the visitor-facing message: saying which
          // one would turn this page into an existence oracle.
          notice('This walkthrough link is no longer available.');
          return null;
        }
        if (!res.ok) {
          warn('walkthrough fetch failed with status ' + res.status);
          notice('This walkthrough could not be loaded.');
          return null;
        }
        return res.json();
      }).then(function (wt) {
        if (!wt) { return; }
        if (!wt.steps || !wt.steps.length) {
          notice('This walkthrough has no steps.');
          return;
        }
        // The viewer re-validates and normalizes every step itself (degrading a
        // bad step to manual rather than throwing), and applies wt.variantSet
        // through window.__variantEngine before rendering step 0.
        booted = viewer.create(wt, {}).start();
      })['catch'](function (err) {
        warn('walkthrough boot failed', err);
        notice('This walkthrough could not be loaded.');
      });
      return null;
    }

    var booted = null;

    function autoBoot() {
      var token = shareToken(window.location && window.location.pathname);
      if (!token) { return; }
      boot(token);
    }

    // Gate 1 is checked BEFORE any listener is registered, so a dev-proxied page
    // carries neither the listener nor the boot — the module is fully inert
    // outside the public plane, not merely early-returning inside a callback.
    if (isPublicPlane()) {
      if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', autoBoot);
      } else {
        autoBoot();
      }
    }

    // Test/debug surface. Deliberately public-namespaced (__agnt_*), never the
    // dev-control namespace — the public bundle must stay provably free of it
    // (role_public_test.go scans the assembled bytes for those tokens, comments
    // included, so this header avoids spelling them out).
    window.__agntPublicBoot = {
      shareToken: shareToken,
      isPublicPlane: isPublicPlane,
      boot: boot,
      player: function () { return booted; },
      version: 'p12'
    };
  } catch (e) {
    try { console.error('[WalkthroughPublic] init failed:', e); } catch (e2) {}
  }
})();

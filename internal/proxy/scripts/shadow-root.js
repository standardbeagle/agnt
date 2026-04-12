// Shadow DOM root host for DevTool UI
//
// Creates a single <div id="__devtool-root"> attached to document.documentElement
// with an open shadow root, and exposes window.__devtoolGetMountRoot() which
// consumers (indicator.js, overlay.js) call to get their mount point.
//
// Design decisions:
//
//  * mode: 'open' (not 'closed'). All devtool scripts run in the same realm —
//    we are isolating styles, not protecting against adversarial host code.
//    'closed' would break Chrome DevTools inspection of the devtool UI itself
//    when debugging the devtool overlay. Style isolation is identical in
//    both modes.
//
//  * Host attaches to document.documentElement, NOT document.body. The
//    indicator's children use position:fixed, and fixed elements inside a
//    shadow host are escaped from the host's transform/filter/perspective
//    containing block. document.documentElement is the root and never has a
//    transform ancestor, so position:fixed children behave as viewport-relative
//    as expected.
//
//  * Fallback on failure: if attachShadow throws (legacy browsers, CSP with
//    no 'unsafe-inline' + no nonce for shadow stylesheets, etc), the helper
//    returns document.body and consumers mount there as before. This keeps
//    older environments working.
//
//  * The host element itself has `all: initial` to wipe any host-page CSS that
//    might target div#__devtool-root, plus position:static and 0x0 box so it
//    takes no layout space. All actual UI lives inside the shadow root as
//    position:fixed descendants.
//
//  * Idempotent: if window.__devtoolGetMountRoot already exists we early-out.
//    This guards against double-injection (e.g. if the proxy injects the
//    combined script twice due to reloads).
(function() {
  'use strict';

  if (window.__devtoolGetMountRoot) return;

  var mountRoot = null;

  try {
    var host = document.createElement('div');
    host.id = '__devtool-root';
    // `all: initial` resets any host-page CSS targeting this id.
    // position:static + 0x0 keeps the host out of layout flow — the actual
    // UI is position:fixed inside the shadow root.
    host.style.cssText = 'all: initial; position: static; width: 0; height: 0; pointer-events: none;';

    var root = document.documentElement || document.body;
    if (!root) {
      // Document not ready yet. Fall through to fallback.
      throw new Error('document root not available');
    }
    root.appendChild(host);

    if (typeof host.attachShadow !== 'function') {
      throw new Error('attachShadow not supported');
    }

    mountRoot = host.attachShadow({ mode: 'open' });
  } catch (err) {
    // Shadow DOM unavailable — fall back to document.body so callers still
    // function. The fallback branch matches legacy behavior exactly.
    if (typeof console !== 'undefined' && console.warn) {
      console.warn('[devtool] shadow root unavailable, falling back to document.body:', err);
    }
    mountRoot = null;
  }

  // Public accessor. Returns ShadowRoot on success, document.body on failure.
  // Consumers should NOT cache the return value permanently — always call
  // the helper on mount so the fallback path gets a live document.body
  // reference even if the DOM was not ready at shadow-root.js load time.
  window.__devtoolGetMountRoot = function() {
    return mountRoot || document.body;
  };

  // Returns true if the active mount root is a ShadowRoot. Consumers use
  // this to decide whether getElementById should go through document or
  // through the shadow root, and whether to inject styles into the shadow
  // root vs document.head.
  window.__devtoolIsShadowMount = function() {
    return mountRoot !== null && mountRoot !== document.body;
  };
})();

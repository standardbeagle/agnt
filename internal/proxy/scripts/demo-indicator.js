// Always-on demo indicator for the walkthrough-publish public plane (spec §9c,
// INV-14). It is a MANDATORY member of the RolePublic allowlist, not an opt-in
// module: every public artifact response — self-contained shell AND proxied
// upstream document — renders this badge, which says the page is an agnt demo
// rather than the live site itself.
//
// THE WORDING IS PATH-NEUTRAL ON PURPOSE. The badge ships on both public
// artifact paths, and on the self-contained path nothing is proxied at all —
// there is no upstream. Wording that asserted proxying was therefore false on
// exactly one of the two paths, which is the one thing an honesty control may not
// be. Path-AWARE wording was rejected instead of path-neutral wording because it
// would need a per-path signal in the document, and both paths deliberately serve
// the SAME content-addressed bundle bytes (public_routes_test.go asserts one hash,
// one pin, so there is no per-path flavour that could omit the badge). Reading
// such a signal would also make the disclosure depend on input, and INV-14 says
// it depends on none. So the text names agnt, names this a demo, and disclaims
// being the live site — all three true on both paths — and asserts nothing about
// proxying.
//
// WHY it is unconditional: §9c's honest mitigation for publisher-side deception.
// A proxied lookalike is indistinguishable from the real site to a viewer unless
// something on the page says otherwise, so a disclosure control that can be
// switched off protects exactly the viewers who need it least. There is
// therefore NO config key, no op, no query parameter, and no close affordance —
// the module reads no input at all beyond the public-plane marker that decides
// whether it is running on the public plane in the first place.
//
// STYLING IS CSSOM-ONLY, and that is a CSP constraint, not a preference. The
// public plane pins script-src to the bundle hash and confines everything else
// to 'self' (INV-11/INV-12), and the PROXIED path deliberately passes an EMPTY
// nonce (public_routes.go serveProxiedArtifact), so that response authorises no
// inline style at all. This module therefore uses a constructed CSSStyleSheet
// adopted into its shadow root: CSSOM-inserted rules are not subject to
// style-src, so nothing needs a nonce and not one directive has to widen. An
// inline style ELEMENT or a style ATTRIBUTE would be refused on the proxied path
// — never introduce either here (this header spells neither literal out, because
// demo_indicator_test.go scans these bytes for them). script-src needs nothing
// either: the
// bundle's sha256 (CSP source) and its SRI integrity are both derived from the
// bundle bytes, so adding this module re-pins the bundle automatically.
//
// TAMPER POSTURE (this slice): mounted in a CLOSED shadow root at the top
// z-layer, and the :host geometry is declared !important so an important
// declaration from the page cannot win over it. The adversarial "a variant's raw
// CSS/HTML cannot hide it" e2e is explicitly a separate slice (spec §11 → P10);
// this module ships the disclosure, it does not claim to have proven the
// tamper-resistance end to end.
//
// It cannot use ui-tokens for its z-scale/palette: ui-tokens is a dev module
// hanging its surface off the dev-control namespace, which the public bundle is
// proven free of (role_public_test.go). The two constants it needs are inlined.
//
// Public surface (deliberately public-namespaced, never the dev namespace):
//   window.__agntDemoIndicator.mount()      idempotent mount, returns the host
//   window.__agntDemoIndicator.text         the disclosure text
//   window.__agntDemoIndicator.hostId       the host element id

(function () {
  'use strict';

  try {
    var HOST_ID = 'agnt-demo-indicator';
    // Top of the stacking scale. Inlined rather than read from the dev token
    // scale, which is not part of the public bundle (see header).
    var Z_TOP = '2147483647';
    // The disclosure itself: names agnt, names this a demo, and disclaims the
    // live site — every clause true on BOTH public artifact paths (see header).
    // Constant — no input of any kind can change or blank it.
    var BRAND = 'agnt';
    var TEXT = 'Demo walkthrough — not the live site.';

    var CSS = [
      // all:initial first so a reset cannot clobber the geometry that follows.
      // !important on every :host declaration: an important declaration in the
      // shadow tree beats an important one from the outer page, so the page
      // cannot re-position or collapse the host by force.
      ':host{all:initial!important;position:fixed!important;left:12px!important;bottom:12px!important;' +
        'right:auto!important;top:auto!important;z-index:' + Z_TOP + '!important;display:block!important;' +
        'visibility:visible!important;opacity:1!important;pointer-events:none!important;' +
        'contain:layout style!important}',
      '.badge{display:flex;align-items:center;gap:8px;box-sizing:border-box;max-width:min(92vw,420px);' +
        'padding:8px 12px;border-radius:9999px;background:#111114;color:#f5f5f7;' +
        'font:12px/1.4 system-ui,-apple-system,"Segoe UI",sans-serif;text-align:left;' +
        'box-shadow:0 2px 10px rgba(0,0,0,0.35)}',
      '.brand{flex:none;font-weight:700;letter-spacing:0.02em}',
      '.text{font-weight:400}'
    ].join('\n');

    // adoptStyles installs CSS through the CSSOM only. Returns false when the
    // platform has no constructable stylesheets — the badge still renders with
    // its text (the disclosure survives), and the failure is reported rather
    // than papered over with an inline style the CSP would refuse anyway.
    function adoptStyles(root) {
      if (typeof CSSStyleSheet !== 'function' || !('adoptedStyleSheets' in root)) {
        return false;
      }
      var sheet = new CSSStyleSheet();
      if (typeof sheet.replaceSync === 'function') {
        sheet.replaceSync(CSS);
      } else if (typeof sheet.insertRule === 'function') {
        // Older constructable-stylesheet shape: insert rule by rule.
        var rules = CSS.split('\n');
        for (var i = 0; i < rules.length; i++) {
          if (rules[i]) { sheet.insertRule(rules[i], sheet.cssRules.length); }
        }
      } else {
        return false;
      }
      root.adoptedStyleSheets = [sheet];
      return true;
    }

    // mount is idempotent: a second call returns the existing host rather than
    // stacking badges.
    function mount() {
      var parent = document.body || document.documentElement;
      if (!parent) { return null; }
      var existing = document.getElementById(HOST_ID);
      if (existing) { return existing; }

      var host = document.createElement('div');
      host.id = HOST_ID;
      // CLOSED, so page script holds no handle on the shadow tree (§9c).
      var root = host.attachShadow({ mode: 'closed' });
      if (!adoptStyles(root)) {
        try {
          console.warn('[AgntDemoIndicator] constructable stylesheets unavailable; badge renders unstyled');
        } catch (e) {}
      }

      var badge = document.createElement('div');
      badge.className = 'badge';
      // A note, not a status: it never updates, and it must not be announced as
      // a live region on every step change.
      badge.setAttribute('role', 'note');
      var brand = document.createElement('span');
      brand.className = 'brand';
      brand.textContent = BRAND;
      var text = document.createElement('span');
      text.className = 'text';
      text.textContent = TEXT;
      badge.appendChild(brand);
      badge.appendChild(text);
      root.appendChild(badge);

      parent.appendChild(host);
      return host;
    }

    function mountWhenReady() {
      if (document.body) { mount(); return; }
      if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', mount);
        return;
      }
      mount();
    }

    // The ONLY gate: the RolePublic version marker, set by buildCombinedScript
    // for the public bundle alone. It is deliberately NOT the share-route gate the
    // boot module uses — the disclosure must not depend on a route shape, on the
    // walkthrough fetch succeeding, or on any other public member. Every public
    // artifact response carries this bundle, so every one renders the badge.
    if (typeof window.__agnt_public_version === 'string') {
      mountWhenReady();
    }

    window.__agntDemoIndicator = {
      mount: mount,
      text: TEXT,
      brand: BRAND,
      hostId: HOST_ID,
      version: 's7'
    };
  } catch (e) {
    try { console.error('[AgntDemoIndicator] init failed:', e); } catch (e2) {}
  }
})();

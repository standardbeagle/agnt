// Deterministic variant overlay engine for the public walkthrough-publish plane.
//
// Given a validated VariantSet (the P2 shape:
// internal/publish/schema.go — {version,id,stepId,variants:[{id,label,ops}]})
// bound to a route, this module applies the CLOSED, DECLARATIVE op vocabulary
// (spec §6) plus the raw-content ops (§6a) to the proxied page and can revert it
// EXACTLY. It is the client-side half of the defense-in-depth contract: the
// server (internal/publish) validates on publish; this engine RE-VALIDATES
// client-side so a tampered snapshot cannot smuggle in an op shape the
// vocabulary does not define.
//
// INV-6 RETIRED 2026-07-27 (§6a): the vocabulary now carries publisher-authored
// CSS, HTML, and script. That author is the publisher holding the dev session —
// a trusted actor — so the old "no author string reaches a markup/code sink"
// rule guarded the wrong boundary. Containment moved to CSP: authored script
// runs only because its sha256 is pinned in the served revision's script-src
// (INV-11/INV-12), so upstream- and viewer-injected script still cannot execute.
// What this engine therefore still refuses is DYNAMIC CODE COMPILATION
// (the dynamic-code sinks) — the served CSP carries no 'unsafe-eval', and a compile
// path would be an attempt to run a body no pinned hash covers.
//
// The op/selector/style/url contract below MIRRORS the P2 Go validators
// byte-for-byte (op.go, selector.go, style.go, url.go, limits.go). Keep the two
// in lockstep: a change to a Go allowlist/limit must be reflected here.
//
// Public surface (what the P4 injector / cycler call):
//   window.__variantEngine.create(variantSet, routeBinding, opts) -> engine
//   window.__variantEngine.validateOp(op)      -> { ok, reason }
//   window.__variantEngine.validateSelector(s) -> { ok, reason }
// engine:
//   apply(variantId)      set desired variant + render if the route matches
//   revert()              clear desired + remove overlay
//   activeVariant()       currently-rendered variant id, or null
//   desiredVariant()      variant that should render when the route matches
//   missingTargets()      selectors from the last apply that matched nothing
//   refusedOps()          [{op, reason}] ops rejected by client-side validation
//   routeMatches()        does the current location satisfy the route binding
//   stats()               { applyCount, writeCount, revertCount }
//   destroy()             revert + disconnect observer + unhook history

(function () {
  'use strict';

  try {
    // ---- Limits (mirror internal/publish/limits.go) ----------------------
    var MAX_SELECTOR_LENGTH = 256;
    var MAX_SELECTOR_DEPTH = 6;
    var MAX_STYLE_PATCH_BYTES = 4096;
    var MAX_TEXT_BYTES = 2048;
    var MAX_URL_LENGTH = 2048;
    var MAX_ATTR_VALUE_LENGTH = MAX_TEXT_BYTES;
    var MAX_VARIANTS_PER_SET = 12;
    // Raw-content caps (§5, mirrored from internal/publish/schema.go). These are
    // parse-cost/abuse bounds, not injection controls — §6a is explicit that the
    // payloads are NOT scanned for code-shape.
    var MAX_RAW_HTML_BYTES = 8192;
    var MAX_RAW_SCRIPT_BYTES = 16384;

    var LOG = '[VariantEngine]';

    // The variant root: the single container the variant-root ops (addStyle,
    // addScript) append to. Created lazily on first use, removed wholesale on
    // revert, so a variant's stylesheet and script leave no residue behind.
    var VARIANT_ROOT_ID = '__variant-engine-root';

    // ---- Restricted selector grammar (mirror selector.go §5a) ------------
    // ident := [A-Za-z_][A-Za-z0-9_-]*  — anchored full-string variant.
    var FULL_IDENT_RE = /^[A-Za-z_][A-Za-z0-9_-]*$/;
    var IDENT_HEAD_RE = /^[A-Za-z_][A-Za-z0-9_-]*/;
    // attr := '[' ident (('='|'^='|'$='|'*=') quoted)? ']' — quoted, no <>/quote.
    var ATTR_RE = /^\[[A-Za-z_][A-Za-z0-9_-]*(?:(?:=|\^=|\$=|\*=)(?:"[^"<>]*"|'[^'<>]*'))?\]/;
    // pseudo := ':' ('first-child'|'last-child'|'nth-child(' INT ')')
    var PSEUDO_RE = /^:(?:first-child|last-child|nth-child\(-?\d+\))/;

    // utf8Len returns the UTF-8 byte length of a JS string (mirrors Go len()).
    function utf8Len(s) {
      // TextEncoder is available in every target browser; count code units as a
      // safe fallback if it is somehow absent.
      if (typeof TextEncoder !== 'undefined') {
        try { return new TextEncoder().encode(s).length; } catch (e) { /* fall through */ }
      }
      return unescape(encodeURIComponent(s)).length;
    }

    // validateCompound checks one compound selector against the §5a production.
    function validateCompound(s) {
      var i = 0;
      if (s.charAt(i) === '*') {
        i++;
      } else {
        var head = IDENT_HEAD_RE.exec(s);
        if (head) { i += head[0].length; }
      }
      while (i < s.length) {
        var c = s.charAt(i);
        if (c === '.' || c === '#') {
          var m = IDENT_HEAD_RE.exec(s.slice(i + 1));
          if (!m) { return 'bad class/id'; }
          i += 1 + m[0].length;
        } else if (c === '[') {
          var a = ATTR_RE.exec(s.slice(i));
          if (!a) { return 'bad attribute selector'; }
          i += a[0].length;
        } else if (c === ':') {
          var p = PSEUDO_RE.exec(s.slice(i));
          if (!p) { return 'forbidden or malformed pseudo'; }
          i += p[0].length;
        } else {
          return 'unexpected character ' + JSON.stringify(c);
        }
      }
      if (i === 0) { return 'empty compound'; }
      return null;
    }

    // splitCompounds tokenizes on the descendant (whitespace) and child ('>')
    // combinators only, bracket/quote-aware so whitespace/'>' inside [attr="…"]
    // are not combinators. Mirrors selector.go splitCompounds.
    function splitCompounds(sel) {
      var compounds = [];
      var cur = '';
      var depth = 0;      // inside [ ... ]
      var quote = '';     // inside a quoted attr value
      var sawWS = false;
      var sawChild = false;

      function start(ch) {
        if ((sawWS || sawChild) && cur.length > 0) {
          compounds.push(cur);
          cur = '';
        }
        sawWS = false;
        sawChild = false;
        cur += ch;
      }

      for (var i = 0; i < sel.length; i++) {
        var c = sel.charAt(i);
        if (quote) {
          cur += c;
          if (c === quote) { quote = ''; }
          continue;
        }
        if (depth > 0) {
          cur += c;
          if (c === '"' || c === "'") { quote = c; }
          else if (c === ']') { depth--; }
          continue;
        }
        if (c === ' ' || c === '\t' || c === '\n' || c === '\r' || c === '\f') {
          if (cur.length > 0) { sawWS = true; }
        } else if (c === '>') {
          if (sawChild) { return { err: 'double child combinator' }; }
          if (cur.length === 0) { return { err: 'child combinator without left operand' }; }
          sawChild = true;
        } else if (c === '[') {
          start('[');
          depth++;
        } else {
          start(c);
        }
      }
      if (quote || depth > 0) { return { err: 'unterminated attribute' }; }
      if (sawChild) { return { err: 'child combinator without right operand' }; }
      if (cur.length > 0) { compounds.push(cur); }
      if (compounds.length === 0) { return { err: 'empty selector' }; }
      return { compounds: compounds };
    }

    // validateSelector reports {ok, reason} — nil-reason iff sel is in the
    // restricted grammar and within the length/depth limits (§5/§5a).
    function validateSelector(sel) {
      if (typeof sel !== 'string' || sel === '') { return { ok: false, reason: 'selector: empty' }; }
      if (sel.length > MAX_SELECTOR_LENGTH) {
        return { ok: false, reason: 'selector: length ' + sel.length + ' exceeds max ' + MAX_SELECTOR_LENGTH };
      }
      var split = splitCompounds(sel);
      if (split.err) { return { ok: false, reason: 'selector: ' + split.err }; }
      if (split.compounds.length > MAX_SELECTOR_DEPTH) {
        return { ok: false, reason: 'selector: depth ' + split.compounds.length + ' exceeds max ' + MAX_SELECTOR_DEPTH };
      }
      for (var i = 0; i < split.compounds.length; i++) {
        var err = validateCompound(split.compounds[i]);
        if (err) { return { ok: false, reason: 'selector: ' + err }; }
      }
      return { ok: true, reason: null };
    }

    // ---- Attr name allowlist (mirror op.go allowedAttrName) --------------
    // EXCLUDES on*/href/src/srcdoc/formaction; allows class/id/title/alt,
    // aria-*, data-*. href/src cannot be set via setAttribute — image URLs go
    // through setImageSrc (https-only).
    function allowedAttrName(name) {
      var lower = String(name).toLowerCase().trim();
      if (lower.indexOf('on') === 0) { return false; }
      switch (lower) {
        case 'href': case 'src': case 'srcdoc': case 'formaction':
          return false;
        case 'class': case 'id': case 'title': case 'alt':
          return true;
      }
      return lower.indexOf('aria-') === 0 || lower.indexOf('data-') === 0;
    }

    function isClassIdent(s) { return typeof s === 'string' && FULL_IDENT_RE.test(s); }

    function noControlChars(s) {
      for (var i = 0; i < s.length; i++) {
        var code = s.charCodeAt(i);
        if (code < 0x20 || code === 0x7f) { return false; }
      }
      return true;
    }

    // ---- URL allowlist (mirror url.go): https-only, capped, no ctrl ------
    function validateURL(raw) {
      if (typeof raw !== 'string' || raw === '') { return 'url: empty'; }
      if (raw.length > MAX_URL_LENGTH) {
        return 'url: length ' + raw.length + ' exceeds max ' + MAX_URL_LENGTH;
      }
      if (!noControlChars(raw)) { return 'url: contains control character'; }
      var u;
      try { u = new URL(raw); } catch (e) { return 'url: unparseable'; }
      if (u.protocol.toLowerCase() !== 'https:') {
        return 'url: scheme ' + JSON.stringify(u.protocol.replace(/:$/, '')) + ' not allowed (https only)';
      }
      if (!u.host) { return 'url: missing host'; }
      return null;
    }

    // ---- Style allowlist (mirror style.go §5b/§6) ------------------------
    var CSS_PROP_NAME_RE = /^-?[a-z][a-z-]*$/;
    var FORBIDDEN_CSS_TOKENS = [
      'url(', 'expression(', 'behavior', '-moz-binding', '@import', 'javascript:',
      'image-set(', '-webkit-image-set(', 'cross-fade(', '-webkit-cross-fade('
    ];
    var CSS_STRIP_RE = /\/\*[^*]*\*+(?:[^/*][^*]*\*+)*\/|\s+/g;
    var ALLOWED_CSS_EXACT = {
      'color': true, 'background-color': true, 'background': true,
      'line-height': true, 'letter-spacing': true, 'opacity': true,
      'display': true, 'visibility': true, 'width': true, 'height': true,
      'box-shadow': true, 'border-radius': true, 'transform': true, 'transition': true
    };
    var ALLOWED_CSS_PREFIX = ['border', 'outline', 'padding', 'margin', 'font', 'text-', 'min-', 'max-'];

    function allowedCSSProperty(prop) {
      if (ALLOWED_CSS_EXACT[prop]) { return true; }
      for (var i = 0; i < ALLOWED_CSS_PREFIX.length; i++) {
        if (prop.indexOf(ALLOWED_CSS_PREFIX[i]) === 0) { return true; }
      }
      return false;
    }

    function hasForbiddenCSSToken(s) {
      var lower = String(s).replace(CSS_STRIP_RE, '').toLowerCase();
      for (var i = 0; i < FORBIDDEN_CSS_TOKENS.length; i++) {
        if (lower.indexOf(FORBIDDEN_CSS_TOKENS[i]) !== -1) { return true; }
      }
      return false;
    }

    function validateStyleProps(props) {
      if (!props || typeof props !== 'object') { return 'applyStyle: no properties'; }
      var keys = Object.keys(props);
      if (keys.length === 0) { return 'applyStyle: no properties'; }
      var total = 0;
      for (var i = 0; i < keys.length; i++) {
        var prop = keys[i];
        var val = props[prop];
        if (typeof val !== 'string') { return 'applyStyle: property ' + JSON.stringify(prop) + ' value is not a string'; }
        total += prop.length + val.length;
        var name = prop.toLowerCase().trim();
        if (!CSS_PROP_NAME_RE.test(name)) { return 'applyStyle: illegal property name ' + JSON.stringify(prop); }
        if (!allowedCSSProperty(name)) { return 'applyStyle: property ' + JSON.stringify(prop) + ' not on allowlist'; }
        if (val.indexOf('\\') !== -1) { return 'applyStyle: property ' + JSON.stringify(prop) + ' value has illegal backslash escape'; }
        if (hasForbiddenCSSToken(val)) { return 'applyStyle: property ' + JSON.stringify(prop) + ' has forbidden token in value'; }
        if (/[;{}<>]/.test(val)) { return 'applyStyle: property ' + JSON.stringify(prop) + ' value has illegal character'; }
      }
      if (total > MAX_STYLE_PATCH_BYTES) {
        return 'applyStyle: patch size ' + total + ' exceeds max ' + MAX_STYLE_PATCH_BYTES;
      }
      return null;
    }

    // ---- Op validation (mirror op.go Op.Validate) ------------------------
    // Returns {ok, reason}. The vocabulary is CLOSED — an unknown op is a
    // rejection, never a passthrough — but since INV-6's retirement it includes
    // the raw-content ops (§6a), whose payloads are bounded by size and
    // contained by CSP rather than scanned for code-shape.
    function isRootOp(op) { return op && (op.op === 'addStyle' || op.op === 'addScript'); }

    function validateOp(op) {
      if (!op || typeof op !== 'object') { return { ok: false, reason: 'op: not an object' }; }
      if (isRootOp(op)) {
        // Variant-root ops append to the variant root rather than mutating a
        // matched element, so a selector is meaningless — and accepting one
        // would imply a targeting semantic the renderer does not have.
        if (op.selector) { return { ok: false, reason: op.op + ": unexpected field 'selector'" }; }
      } else {
        var sel = validateSelector(op.selector);
        if (!sel.ok) { return { ok: false, reason: sel.reason }; }
      }

      function forbidExcept(allowed) {
        var ok = {};
        for (var i = 0; i < allowed.length; i++) { ok[allowed[i]] = true; }
        if (op.value && !ok.value) { return "unexpected field 'value'"; }
        if (op.name && !ok.name) { return "unexpected field 'name'"; }
        if (op.from && !ok.from) { return "unexpected field 'from'"; }
        if (op.to && !ok.to) { return "unexpected field 'to'"; }
        if (op.url && !ok.url) { return "unexpected field 'url'"; }
        if (op.html && !ok.html) { return "unexpected field 'html'"; }
        if (op.css && !ok.css) { return "unexpected field 'css'"; }
        if (op.code && !ok.code) { return "unexpected field 'code'"; }
        if (op.src && !ok.src) { return "unexpected field 'src'"; }
        if (op.props && Object.keys(op.props).length > 0 && !ok.props) { return "unexpected field 'props'"; }
        return null;
      }

      var stray;
      switch (op.op) {
        case 'setText':
          stray = forbidExcept(['value']);
          if (stray) { return { ok: false, reason: 'setText: ' + stray }; }
          if (utf8Len(op.value || '') > MAX_TEXT_BYTES) { return { ok: false, reason: 'setText: value exceeds max ' + MAX_TEXT_BYTES }; }
          if (!noControlChars(String(op.value || '').replace(/\n/g, '').replace(/\t/g, ''))) {
            return { ok: false, reason: 'setText: value has illegal control character' };
          }
          return { ok: true, reason: null };
        case 'setAttribute':
          stray = forbidExcept(['name', 'value']);
          if (stray) { return { ok: false, reason: 'setAttribute: ' + stray }; }
          if (!allowedAttrName(op.name)) { return { ok: false, reason: 'setAttribute: attribute ' + JSON.stringify(op.name) + ' not on allowlist' }; }
          if (String(op.value || '').length > MAX_ATTR_VALUE_LENGTH) { return { ok: false, reason: 'setAttribute: value exceeds max ' + MAX_ATTR_VALUE_LENGTH }; }
          if (!noControlChars(String(op.value || ''))) { return { ok: false, reason: 'setAttribute: value has illegal control character' }; }
          return { ok: true, reason: null };
        case 'replaceClass':
          stray = forbidExcept(['from', 'to']);
          if (stray) { return { ok: false, reason: 'replaceClass: ' + stray }; }
          if (!isClassIdent(op.from) || !isClassIdent(op.to)) { return { ok: false, reason: 'replaceClass: from/to must be single class idents' }; }
          return { ok: true, reason: null };
        case 'addClass':
        case 'removeClass':
          stray = forbidExcept(['value']);
          if (stray) { return { ok: false, reason: op.op + ': ' + stray }; }
          if (!isClassIdent(op.value)) { return { ok: false, reason: op.op + ': value must be a single class ident' }; }
          return { ok: true, reason: null };
        case 'applyStyle':
          stray = forbidExcept(['props']);
          if (stray) { return { ok: false, reason: 'applyStyle: ' + stray }; }
          var serr = validateStyleProps(op.props);
          if (serr) { return { ok: false, reason: serr }; }
          return { ok: true, reason: null };
        case 'setImageSrc':
          stray = forbidExcept(['url']);
          if (stray) { return { ok: false, reason: 'setImageSrc: ' + stray }; }
          var uerr = validateURL(op.url);
          if (uerr) { return { ok: false, reason: uerr }; }
          return { ok: true, reason: null };
        case 'setHTML':
          stray = forbidExcept(['html']);
          if (stray) { return { ok: false, reason: 'setHTML: ' + stray }; }
          if (typeof op.html !== 'string' || op.html === '') { return { ok: false, reason: 'setHTML: html is required' }; }
          if (utf8Len(op.html) > MAX_RAW_HTML_BYTES) {
            return { ok: false, reason: 'setHTML: html exceeds max ' + MAX_RAW_HTML_BYTES };
          }
          return { ok: true, reason: null };
        case 'addStyle':
          stray = forbidExcept(['css']);
          if (stray) { return { ok: false, reason: 'addStyle: ' + stray }; }
          if (typeof op.css !== 'string' || op.css === '') { return { ok: false, reason: 'addStyle: css is required' }; }
          if (utf8Len(op.css) > MAX_STYLE_PATCH_BYTES) {
            return { ok: false, reason: 'addStyle: css exceeds max ' + MAX_STYLE_PATCH_BYTES };
          }
          return { ok: true, reason: null };
        case 'addScript':
          stray = forbidExcept(['code', 'src']);
          if (stray) { return { ok: false, reason: 'addScript: ' + stray }; }
          // src is a PUBLISH-TIME fetch input (§6a): the daemon fetches it once
          // under §4a and inlines the bytes so the hash can be pinned. An op
          // that still carries src by the time it reaches a browser means that
          // resolution did not happen — fail closed rather than invent a
          // runtime fetch, which would emit the <script src> §6a forbids and
          // load a body no pinned hash covers.
          if (op.src) { return { ok: false, reason: 'addScript: src must be inlined at publish time; the renderer emits no <script src>' }; }
          if (typeof op.code !== 'string' || op.code === '') { return { ok: false, reason: 'addScript: code is required' }; }
          if (utf8Len(op.code) > MAX_RAW_SCRIPT_BYTES) {
            return { ok: false, reason: 'addScript: code exceeds max ' + MAX_RAW_SCRIPT_BYTES };
          }
          return { ok: true, reason: null };
        default:
          return { ok: false, reason: 'unknown op type ' + JSON.stringify(op.op) };
      }
    }

    // opSignature is a stable key for a single op (mirror op.go signature()).
    function opSignature(op) {
      return [
        op.op, op.selector, op.name || '', op.value || '',
        op.from || '', op.to || '', op.url || '',
        op.html || '', op.css || '', op.code || '',
        op.props ? JSON.stringify(sortedProps(op.props)) : ''
      ].join('|');
    }
    function sortedProps(props) {
      var out = {};
      Object.keys(props).sort().forEach(function (k) { out[k] = props[k]; });
      return out;
    }

    // ---- Op apply / undo -------------------------------------------------
    // captureUndo snapshots the exact prior state so revert restores it byte-
    // for-byte, then applyOp performs the mutation. Raw-content ops (§6a) write
    // authored markup/CSS/script verbatim; the variant-root ops need no prior-
    // state snapshot because applyOp returns the node it created and the caller
    // builds a remove-the-node undo from it.
    function captureUndo(el, op) {
      switch (op.op) {
        case 'setHTML':
        case 'setText': {
          // Snapshot the live child nodes (detached, not cloned) so restore
          // rebuilds the subtree EXACTLY — including element children a plain
          // textContent snapshot would lose.
          var prevNodes = Array.prototype.slice.call(el.childNodes);
          return function () {
            while (el.firstChild) { el.removeChild(el.firstChild); }
            for (var i = 0; i < prevNodes.length; i++) { el.appendChild(prevNodes[i]); }
          };
        }
        case 'setAttribute': {
          var hadAttr = el.hasAttribute(op.name);
          var prevAttr = el.getAttribute(op.name);
          return function () {
            if (hadAttr) { el.setAttribute(op.name, prevAttr); } else { el.removeAttribute(op.name); }
          };
        }
        case 'setImageSrc': {
          var hadSrc = el.hasAttribute('src');
          var prevSrc = el.getAttribute('src');
          return function () {
            if (hadSrc) { el.setAttribute('src', prevSrc); } else { el.removeAttribute('src'); }
          };
        }
        case 'addClass': {
          var hadAdd = el.classList.contains(op.value);
          return function () { if (!hadAdd) { el.classList.remove(op.value); } };
        }
        case 'removeClass': {
          var hadRem = el.classList.contains(op.value);
          return function () { if (hadRem) { el.classList.add(op.value); } };
        }
        case 'replaceClass': {
          var hadFrom = el.classList.contains(op.from);
          var hadTo = el.classList.contains(op.to);
          return function () {
            el.classList.toggle(op.from, hadFrom);
            el.classList.toggle(op.to, hadTo);
          };
        }
        case 'applyStyle': {
          // Snapshot the WHOLE style attribute so revert is byte-exact: removing
          // individual properties would leave a spurious empty style="" attribute
          // on an element that had no inline style to begin with.
          var hadStyle = el.hasAttribute('style');
          var prevStyle = el.getAttribute('style');
          return function () {
            if (hadStyle) {
              el.setAttribute('style', prevStyle);
            } else {
              // Once el.style has been CSSOM-touched, a bare removeAttribute
              // leaves a spurious empty style="" (observed in headless Chrome).
              // Resetting the attribute node first, THEN removing it, drops the
              // attribute cleanly so revert is byte-exact.
              el.setAttribute('style', '');
              el.removeAttribute('style');
            }
          };
        }
        default:
          return function () {};
      }
    }

    // applyOp mutates el for op. For the variant-root ops it RETURNS the node it
    // appended, which the caller turns into the undo; every other op returns
    // undefined and is undone from captureUndo's snapshot.
    function applyOp(el, op) {
      switch (op.op) {
        case 'setText': el.textContent = op.value; break;
        case 'setHTML':
          // Authored markup, written verbatim (§6a). Inline on* handlers and
          // javascript: URLs inside the fragment are inert under the served
          // CSP (script-src carries no 'unsafe-inline'), which is why the
          // renderer neither strips nor rejects them.
          el.innerHTML = op.html;
          break;
        case 'addStyle': {
          var styleEl = document.createElement('style');
          styleEl.textContent = op.css;
          el.appendChild(styleEl);
          return styleEl;
        }
        case 'addScript': {
          // The authored script is emitted as a SCRIPT ELEMENT whose body is the
          // authored text — never handed to a dynamic-code sink, which the
          // served CSP forbids outright ('unsafe-eval' absent, INV-12). Whether
          // this body executes is decided entirely by CSP: it runs iff its
          // sha256 is in the revision's pinned script-src hash set. Until that
          // pinning lands the browser refuses it, which is the correct
          // fail-closed state — a body no hash covers must not run.
          var scriptEl = document.createElement('script');
          scriptEl.textContent = op.code;
          el.appendChild(scriptEl);
          return scriptEl;
        }
        case 'setAttribute': el.setAttribute(op.name, op.value); break;
        case 'setImageSrc': el.setAttribute('src', op.url); break;
        case 'addClass': el.classList.add(op.value); break;
        case 'removeClass': el.classList.remove(op.value); break;
        case 'replaceClass': el.classList.replace(op.from, op.to); break;
        case 'applyStyle': {
          var keys = Object.keys(op.props);
          for (var i = 0; i < keys.length; i++) {
            el.style.setProperty(keys[i].toLowerCase().trim(), op.props[keys[i]]);
          }
          break;
        }
      }
    }

    // ---- Route binding ---------------------------------------------------
    // routeBinding := { when: 'any'|'path-equals'|'path-prefix'|'path-contains', value }
    function routeMatches(binding) {
      if (!binding || !binding.when || binding.when === 'any') { return true; }
      var path = '';
      try { path = window.location.pathname || ''; } catch (e) { path = ''; }
      var v = binding.value || '';
      switch (binding.when) {
        case 'path-equals': return path === v;
        case 'path-prefix': return path.indexOf(v) === 0;
        case 'path-contains': return path.indexOf(v) !== -1;
        default: return false;
      }
    }

    // ---- Global history hook (install once, shared across engines) -------
    // pushState/replaceState are monkey-patched to emit a synthetic event so
    // engines re-evaluate route binding on SPA navigation, alongside popstate.
    var LOCATION_EVENT = '__variantengine_locationchange';
    function ensureHistoryHook() {
      if (window.__variantEngine_histHooked) { return; }
      window.__variantEngine_histHooked = true;
      ['pushState', 'replaceState'].forEach(function (name) {
        var orig = history[name];
        if (typeof orig !== 'function') { return; }
        history[name] = function () {
          var ret = orig.apply(this, arguments);
          try { window.dispatchEvent(new Event(LOCATION_EVENT)); } catch (e) { /* ignore */ }
          return ret;
        };
      });
    }

    // ---- Missing-target surfacing (spec §4 silent-failure prohibition) ---
    var MISSING_MARKER_ID = '__variant-engine-missing';
    function surfaceMissing(selectors) {
      if (!selectors || selectors.length === 0) { clearMissingMarker(); return; }
      // Structured console warning — never silent.
      try { console.warn(LOG + ' variant targets not found: ' + selectors.join(', ')); } catch (e) { /* ignore */ }
      // Visible marker element, built with textContent only (no markup).
      try {
        if (!document.body) { return; }
        var marker = document.getElementById(MISSING_MARKER_ID);
        if (!marker) {
          marker = document.createElement('div');
          marker.id = MISSING_MARKER_ID;
          marker.setAttribute('role', 'alert');
          marker.style.setProperty('position', 'fixed');
          marker.style.setProperty('bottom', '0');
          marker.style.setProperty('left', '0');
          marker.style.setProperty('z-index', '2147483647');
          marker.style.setProperty('background', '#b00020');
          marker.style.setProperty('color', '#fff');
          marker.style.setProperty('font', '12px monospace');
          marker.style.setProperty('padding', '4px 8px');
          document.body.appendChild(marker);
        }
        marker.textContent = LOG + ' missing targets: ' + selectors.join(', ');
      } catch (e) { /* marker is best-effort; the console.warn already fired */ }
    }
    function clearMissingMarker() {
      try {
        var marker = document.getElementById(MISSING_MARKER_ID);
        if (marker && marker.parentNode) { marker.parentNode.removeChild(marker); }
      } catch (e) { /* ignore */ }
    }

    // ---- Engine ----------------------------------------------------------
    function createEngine(variantSet, routeBinding, opts) {
      if (!variantSet || typeof variantSet !== 'object') {
        throw new Error(LOG + ' variantSet must be an object');
      }
      if (variantSet.version && variantSet.version !== 'v1') {
        throw new Error(LOG + ' unsupported schema version ' + JSON.stringify(variantSet.version));
      }
      var rawVariants = variantSet.variants || [];
      if (rawVariants.length > MAX_VARIANTS_PER_SET) {
        throw new Error(LOG + ' variant set exceeds max ' + MAX_VARIANTS_PER_SET + ' variants');
      }
      opts = opts || {};
      var scopeRoot = opts.root && typeof opts.root.querySelectorAll === 'function' ? opts.root : document;

      // Pre-validate every op once at bind time (defense in depth). Invalid ops
      // are dropped from the render set and recorded in refused[] + logged —
      // never silently skipped.
      var refused = [];
      var variants = {};
      var order = [];
      rawVariants.forEach(function (v) {
        if (!v || typeof v.id !== 'string') { return; }
        var ops = [];
        (v.ops || []).forEach(function (op) {
          var res = validateOp(op);
          if (!res.ok) {
            refused.push({ op: op, reason: res.reason });
            try { console.warn(LOG + ' refused op (' + res.reason + '): ' + JSON.stringify(op)); } catch (e) { /* ignore */ }
            return;
          }
          op._sig = opSignature(op);
          ops.push(op);
        });
        variants[v.id] = { id: v.id, label: v.label || '', ops: ops };
        order.push(v.id);
      });

      var desiredId = null; // variant that should render when the route matches
      var appliedId = null; // variant currently rendered to the DOM
      var undos = [];       // {el, undo} applied-op records, unwound on revert
      var marks = new WeakMap(); // el -> Set(opSig) already applied (idempotency)
      var lastMissing = [];
      var rootEl = null;    // the variant root, created on first variant-root op
      var applying = false; // true while WE mutate — makes the observer ignore self-mutations
      var scheduled = false;
      var destroyed = false;
      var stats = { applyCount: 0, writeCount: 0, revertCount: 0 };

      var observer = (typeof MutationObserver !== 'undefined')
        ? new MutationObserver(function () {
            if (applying || destroyed) { return; } // ignore our own mutations (loop guard)
            scheduleSync();
          })
        : null;

      function beginMutate() { applying = true; }
      function endMutate() {
        // Clear the flag on a microtask so the async-delivered self-mutation
        // batch (queued while applying) is still seen as ours and ignored.
        var clear = function () { applying = false; };
        if (typeof queueMicrotask === 'function') { queueMicrotask(clear); }
        else { Promise.resolve().then(clear); }
      }

      function scheduleSync() {
        if (scheduled || destroyed) { return; }
        scheduled = true;
        var run = function () { scheduled = false; sync(); };
        if (typeof requestAnimationFrame === 'function') { requestAnimationFrame(run); }
        else { setTimeout(run, 0); }
      }

      // variantRoot returns the container the variant-root ops append to,
      // creating it on first use. It is inert layout-wise (a hidden div): a
      // style element inside it still applies document-wide, and a script element
      // still runs — subject to CSP — while keeping every authored node under
      // one parent that revert can drop in a single removal.
      function variantRoot() {
        if (rootEl && rootEl.parentNode) { return rootEl; }
        var host = document.body || document.documentElement;
        rootEl = document.createElement('div');
        rootEl.id = VARIANT_ROOT_ID;
        rootEl.setAttribute('hidden', '');
        rootEl.style.setProperty('display', 'none');
        host.appendChild(rootEl);
        return rootEl;
      }

      function applyOps(id) {
        var v = variants[id];
        if (!v) { return; }
        beginMutate();
        try {
          var missing = [];
          for (var i = 0; i < v.ops.length; i++) {
            var op = v.ops[i];
            var els;
            if (isRootOp(op)) {
              // Variant-root ops target the engine's own container, so they can
              // never "miss" — there is no selector and nothing to report.
              els = [variantRoot()];
            } else {
              try { els = scopeRoot.querySelectorAll(op.selector); } catch (e) { els = []; }
              if (!els || els.length === 0) { missing.push(op.selector); continue; }
            }
            for (var j = 0; j < els.length; j++) {
              var el = els[j];
              var set = marks.get(el);
              if (!set) { set = {}; marks.set(el, set); }
              if (set[op._sig]) { continue; } // already applied to this node — idempotent
              var undo = captureUndo(el, op);
              var created = applyOp(el, op);
              if (created) {
                undo = (function (node) {
                  return function () { if (node.parentNode) { node.parentNode.removeChild(node); } };
                })(created);
              }
              undos.push({ el: el, undo: undo });
              set[op._sig] = true;
              stats.writeCount++;
            }
          }
          appliedId = id;
          stats.applyCount++;
          lastMissing = missing;
          surfaceMissing(missing);
        } finally {
          endMutate();
        }
      }

      function removeOverlay() {
        if (appliedId === null && undos.length === 0 && !rootEl) { clearMissingMarker(); return; }
        beginMutate();
        try {
          for (var i = undos.length - 1; i >= 0; i--) {
            try { undos[i].undo(); } catch (e) { /* one node's restore failed; continue */ }
          }
          undos = [];
          marks = new WeakMap();
          // Drop the variant root itself, not just its children: leaving an
          // empty container behind would be residue a revert is supposed to
          // erase, and the next apply must start from a fresh root.
          if (rootEl && rootEl.parentNode) { rootEl.parentNode.removeChild(rootEl); }
          rootEl = null;
          appliedId = null;
          lastMissing = [];
          stats.revertCount++;
          clearMissingMarker();
        } finally {
          endMutate();
        }
      }

      // sync reconciles DOM to (desiredId, routeMatch). Idempotent: re-entrant
      // calls that find the DOM already in the desired state write nothing.
      function sync() {
        if (destroyed) { return; }
        if (desiredId !== null && routeMatches(routeBinding) && variants[desiredId]) {
          if (appliedId !== null && appliedId !== desiredId) { removeOverlay(); }
          applyOps(desiredId);
        } else {
          removeOverlay();
        }
      }

      function onLocationChange() { sync(); }

      // Wire observer + history/nav listeners.
      if (observer) {
        var target = document.documentElement || document.body;
        if (target) {
          observer.observe(target, { childList: true, subtree: true, attributes: true, characterData: true });
        }
      }
      ensureHistoryHook();
      window.addEventListener('popstate', onLocationChange);
      window.addEventListener(LOCATION_EVENT, onLocationChange);

      return {
        apply: function (variantId) {
          if (destroyed) { return; }
          if (!variants[variantId]) { throw new Error(LOG + ' unknown variant ' + JSON.stringify(variantId)); }
          desiredId = variantId;
          sync();
        },
        revert: function () {
          if (destroyed) { return; }
          desiredId = null;
          sync();
        },
        activeVariant: function () { return appliedId; },
        desiredVariant: function () { return desiredId; },
        variantIds: function () { return order.slice(); },
        missingTargets: function () { return lastMissing.slice(); },
        refusedOps: function () { return refused.slice(); },
        routeMatches: function () { return routeMatches(routeBinding); },
        stats: function () { return { applyCount: stats.applyCount, writeCount: stats.writeCount, revertCount: stats.revertCount }; },
        destroy: function () {
          if (destroyed) { return; }
          removeOverlay();
          destroyed = true;
          if (observer) { observer.disconnect(); }
          window.removeEventListener('popstate', onLocationChange);
          window.removeEventListener(LOCATION_EVENT, onLocationChange);
        }
      };
    }

    // ---- Public surface --------------------------------------------------
    window.__variantEngine = {
      create: createEngine,
      validateOp: validateOp,
      validateSelector: validateSelector
    };
  } catch (e) {
    try { console.error('[VariantEngine] module initialization failed:', e); } catch (e2) { /* ignore */ }
  }
})();

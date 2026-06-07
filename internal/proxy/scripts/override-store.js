// Override Stylesheet Delta Store
//
// Accumulates design-mode geometry edits as rules in a single injected
// <style id="__devtool_overrides"> element. The rule set *is* the delta store:
// clean read-off for the round-trip payload, trivial undo, no inline-style
// collisions, and a stable target attribute for elements that lack a good
// selector.
//
// NOTE ON MOUNT TARGET: the override <style> is injected into the MAIN document
// head, NOT the devtool shadow root. Styles inside a shadow tree are
// encapsulated and never cascade onto host-page elements, so a shadow-mounted
// override rule could not affect the selected page node. The shadow root is the
// right home for the overlay UI (the handles); the override rules must live in
// the light DOM to reach the elements they target. (Deviation from the design
// spec, which said "in the shadow root" — that would not function.)
(function() {
  'use strict';

  var STYLE_ID = '__devtool_overrides';
  var OID_ATTR = 'data-devtool-oid';

  var store = {
    styleEl: null,
    rules: {},   // oid -> { cssProperty: value }
    nextId: 1
  };

  // getStyleEl returns the singleton <style>, creating + mounting it on first
  // use. Throws when there is no document head/documentElement to mount into —
  // a failed mount must surface, never silently swallow (project rule).
  function getStyleEl() {
    if (store.styleEl && store.styleEl.isConnected) return store.styleEl;
    var existing = document.getElementById(STYLE_ID);
    if (existing) {
      store.styleEl = existing;
      return existing;
    }
    var el = document.createElement('style');
    el.id = STYLE_ID;
    var mount = document.head || document.documentElement;
    if (!mount) {
      throw new Error('override-store: no document head/documentElement to mount the override stylesheet');
    }
    mount.appendChild(el);
    store.styleEl = el;
    return el;
  }

  // ensureOID assigns a stable unique data-devtool-oid to an element and
  // returns it. Reuses an existing oid so repeated edits to the same element
  // share one rule.
  function ensureOID(element) {
    if (!element || element.nodeType !== 1) {
      throw new Error('override-store: ensureOID requires an element');
    }
    var oid = element.getAttribute(OID_ATTR);
    if (!oid) {
      oid = 'oid-' + (store.nextId++);
      element.setAttribute(OID_ATTR, oid);
    }
    return oid;
  }

  // flush rebuilds the entire <style> text from the rule map. Rules use
  // !important + the attribute selector to win specificity battles against
  // existing page CSS and inline styles without mutating them.
  function flush() {
    var el = getStyleEl();
    var css = '';
    for (var oid in store.rules) {
      if (!Object.prototype.hasOwnProperty.call(store.rules, oid)) continue;
      var props = store.rules[oid];
      var body = '';
      for (var prop in props) {
        if (!Object.prototype.hasOwnProperty.call(props, prop)) continue;
        body += prop + ': ' + props[prop] + ' !important; ';
      }
      if (body) {
        css += '[' + OID_ATTR + '="' + oid + '"] { ' + body + '}\n';
      }
    }
    el.textContent = css;
  }

  // upsert merges props into the element's rule and re-flushes. Returns the
  // merged rule. Throws on bad input or mount failure so the caller can
  // suppress the design_edit emit — a failed override must not yield a phantom
  // delta.
  function upsert(oid, props) {
    if (!oid) throw new Error('override-store: upsert requires an oid');
    if (!props || typeof props !== 'object') {
      throw new Error('override-store: upsert requires a props object');
    }
    var merged = store.rules[oid] || {};
    for (var k in props) {
      if (Object.prototype.hasOwnProperty.call(props, k)) merged[k] = props[k];
    }
    store.rules[oid] = merged;
    flush();
    return copy(merged);
  }

  // read returns a copy of the element's accumulated rule (the canonical
  // computed delta), or null when none exists.
  function read(oid) {
    return store.rules[oid] ? copy(store.rules[oid]) : null;
  }

  // pop removes and returns an element's rule (undo).
  function pop(oid) {
    var prev = store.rules[oid] ? copy(store.rules[oid]) : null;
    delete store.rules[oid];
    flush();
    return prev;
  }

  // clear drops every rule and empties the stylesheet.
  function clear() {
    store.rules = {};
    if (store.styleEl) store.styleEl.textContent = '';
  }

  function copy(src) {
    var out = {};
    for (var k in src) {
      if (Object.prototype.hasOwnProperty.call(src, k)) out[k] = src[k];
    }
    return out;
  }

  window.__devtool_override_store = {
    ensureOID: ensureOID,
    upsert: upsert,
    read: read,
    pop: pop,
    clear: clear,
    OID_ATTR: OID_ATTR
  };
})();

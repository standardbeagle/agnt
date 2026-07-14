// Feedback client for the public walkthrough-publish plane (P4).
//
// THIN STUB. The real feedback widget (styled panel, server round-trip,
// moderation) lands in a later walkthrough-publish task; what P4 delivers is the
// public bundle ROLE + the allowlist gate, and this module is the third
// allowlisted member so the role has its intended shape. It exposes a minimal,
// read-only-safe surface that buffers visitor feedback and hands it to an
// author-supplied callback (or, if configured, POSTs it same-origin via fetch).
// It is inert until init() is called.
//
// Like every public-plane module it carries NO dev-control surface: none of the
// dev-control namespace, no WS/exec channel, no code-eval sinks, no dynamic
// function compilation, no screenshot-capture library. It runs unchanged under
// `script-src 'self'` (P1 INV-12). (This header is deliberately free of the
// literal tokens the public forbidden-symbol scan bans.)
//
// Public surface:
//   window.__feedbackClient.init(opts) -> client
//     opts.onSubmit(entry)  optional callback invoked per submission
//     opts.endpoint         optional same-origin path to POST entries to
//   client.submit(entry)    buffer + dispatch a feedback entry
//   client.entries()        the buffered entries (read-only copy)
//   client.reset()          clear the buffer

(function () {
  'use strict';

  try {
    var MAX_ENTRIES = 100;
    var MAX_MESSAGE = 4096;

    function clampMessage(s) {
      if (typeof s !== 'string') return '';
      return s.length > MAX_MESSAGE ? s.slice(0, MAX_MESSAGE) : s;
    }

    // sameOriginPath accepts ONLY a same-origin absolute path so this client can
    // never be pointed at a cross-origin sink (connect-src 'self', INV-7). The
    // prior guard (charAt(0)==='/') accepted a PROTOCOL-RELATIVE "//evil.com/collect",
    // which the browser resolves cross-origin — a data-exfil hole. Tighten it:
    //   - must start with exactly one '/',
    //   - reject "//host" (protocol-relative) and "/\\host" (backslash-smuggled,
    //     which browsers normalize to "//host"),
    //   - reject any scheme marker (a same-origin path never carries "://" or a
    //     "javascript:" / "data:" scheme).
    function sameOriginPath(s) {
      if (typeof s !== 'string' || s.length === 0) return false;
      if (s.charAt(0) !== '/') return false;
      var c1 = s.charAt(1);
      if (c1 === '/' || c1 === '\\') return false;
      if (s.indexOf('://') !== -1) return false;
      if (s.indexOf(':') !== -1 && s.indexOf(':') < s.indexOf('/', 1)) return false;
      return true;
    }

    function init(opts) {
      opts = opts || {};
      var onSubmit = typeof opts.onSubmit === 'function' ? opts.onSubmit : null;
      // endpoint is accepted only as a same-origin absolute path (see sameOriginPath).
      var endpoint = sameOriginPath(opts.endpoint) ? opts.endpoint : null;
      var buffer = [];

      function submit(entry) {
        entry = entry || {};
        var record = {
          message: clampMessage(entry.message || ''),
          rating: (typeof entry.rating === 'number') ? entry.rating : null,
          stepId: (typeof entry.stepId === 'string') ? entry.stepId : '',
          at: Date.now()
        };
        if (buffer.length < MAX_ENTRIES) buffer.push(record);
        if (onSubmit) {
          try { onSubmit(record); } catch (e) { /* callback owns its errors */ }
        }
        if (endpoint && typeof fetch === 'function') {
          try {
            fetch(endpoint, {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify(record),
              credentials: 'same-origin'
            }).catch(function () { /* stub: delivery is best-effort */ });
          } catch (e) { /* fetch unavailable */ }
        }
        return record;
      }

      function entries() { return buffer.slice(); }
      function reset() { buffer.length = 0; }

      return { submit: submit, entries: entries, reset: reset };
    }

    window.__feedbackClient = {
      init: init,
      version: 'p4-stub'
    };
  } catch (e) {
    try { console.error('[FeedbackClient] init failed:', e); } catch (e2) {}
  }
})();

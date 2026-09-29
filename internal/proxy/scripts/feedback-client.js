// Feedback client + widget for the public walkthrough-publish plane (P4).
//
// Buffers visitor feedback, hands it to an author-supplied callback and/or
// POSTs it same-origin via fetch, and (via mount()) renders the minimal
// visible UI a visitor uses to send it: a 1-5 rating, an optional comment,
// and a Send button that shows whether the submission actually landed. The
// server contract (POST /s/{token}/feedback) is documented in
// docs/public-walkthroughs.md §8 and internal/publish/feedback_store.go.
//
// Like every public-plane module it carries NO dev-control surface: none of the
// dev-control namespace, no WS/exec channel, no code-eval sinks, no dynamic
// function compilation, no screenshot-capture library. It runs unchanged under
// `script-src 'self'` (P1 INV-12). (This header is deliberately free of the
// literal tokens the public forbidden-symbol scan bans.)
//
// Public surface:
//   window.__feedbackClient.init(opts) -> client
//     opts.onSubmit(entry)   optional callback invoked per submission
//     opts.endpoint          optional same-origin path to POST entries to
//   client.submit(entry, done)  buffer + dispatch a feedback entry; done(ok,
//                                status) is called once delivery settles (ok is
//                                true when there is no endpoint to fail against)
//   client.entries()         the buffered entries (read-only copy)
//   client.reset()           clear the buffer
//   client.mount(mountOpts)  render the widget into mountOpts.container (default
//                            document.body) and wire it to client.submit; returns
//                            { root, destroy } or null when there is no document

(function () {
  'use strict';

  try {
    var MAX_ENTRIES = 100;
    var MAX_MESSAGE = 4096;

    function clampMessage(s) {
      if (typeof s !== 'string') return '';
      return s.length > MAX_MESSAGE ? s.slice(0, MAX_MESSAGE) : s;
    }

    // isValidRating mirrors the server's own bound (feedbackPayload.Rating,
    // internal/publish/feedback_store.go): an integer in [0, 5]. Checked here so
    // an invalid rating never leaves the client, not because the server would
    // fail to reject it.
    function isValidRating(n) {
      return typeof n === 'number' && isFinite(n) && Math.floor(n) === n && n >= 0 && n <= 5;
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

      function submit(entry, done) {
        entry = entry || {};
        var record = {
          message: clampMessage(entry.message || ''),
          rating: isValidRating(entry.rating) ? entry.rating : null,
          stepId: (typeof entry.stepId === 'string') ? entry.stepId : '',
          at: Date.now()
        };
        if (buffer.length < MAX_ENTRIES) buffer.push(record);
        if (onSubmit) {
          try { onSubmit(record); } catch (e) { /* callback owns its errors */ }
        }

        var reported = false;
        function report(ok, status) {
          if (reported) return;
          reported = true;
          if (typeof done === 'function') {
            try { done(ok, status); } catch (e) { /* caller owns its errors */ }
          }
        }

        if (endpoint && typeof fetch === 'function') {
          try {
            fetch(endpoint, {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify(record),
              credentials: 'same-origin'
            }).then(function (res) {
              report(!!(res && res.ok), res ? res.status : 0);
            }).catch(function () {
              report(false, 0);
            });
          } catch (e) {
            report(false, 0);
          }
        } else {
          // No network sink configured: the record only ever lived in the local
          // buffer, so from the caller's point of view it was delivered.
          report(true, 0);
        }
        return record;
      }

      function entries() { return buffer.slice(); }
      function reset() { buffer.length = 0; }

      var client = { submit: submit, entries: entries, reset: reset };
      client.mount = function (mountOpts) { return mountWidget(client, mountOpts); };
      return client;
    }

    // mountWidget renders the minimal visible feedback UI: a 1-5 rating, an
    // optional comment, a Send button, and a status line that ALWAYS reflects
    // what actually happened (never silently drops a failed submission — Silent
    // Failure Prohibition). Styled by setting element.style properties directly,
    // the same technique public-boot.js's notice() already uses on this plane;
    // no <style> element and no style="" attribute string are written.
    function mountWidget(client, mountOpts) {
      mountOpts = mountOpts || {};
      var doc = mountOpts.document || (typeof document !== 'undefined' ? document : null);
      if (!doc) return null;
      var container = mountOpts.container || doc.body;
      if (!container) return null;

      var root = doc.createElement('div');
      root.setAttribute('role', 'form');
      root.setAttribute('aria-label', 'Feedback');
      styleEl(root, {
        position: 'fixed', right: '16px', bottom: '16px', width: '220px',
        padding: '12px 14px', background: '#1e1e24', color: '#f5f5f7',
        font: '13px/1.4 system-ui, sans-serif', borderRadius: '10px',
        boxShadow: '0 4px 16px rgba(0,0,0,0.35)', zIndex: '2147483000'
      });

      var title = doc.createElement('div');
      title.textContent = 'Feedback';
      styleEl(title, { fontWeight: '600', marginBottom: '6px' });
      root.appendChild(title);

      var ratingRow = doc.createElement('div');
      styleEl(ratingRow, { display: 'flex', gap: '4px', marginBottom: '6px' });
      var selectedRating = null;
      var ratingButtons = [];

      function paintRating() {
        for (var i = 0; i < ratingButtons.length; i++) {
          var isSelected = ratingButtons[i].value === selectedRating;
          ratingButtons[i].el.style.background = isSelected ? '#4c8dff' : '#2a2a31';
          ratingButtons[i].el.setAttribute('aria-pressed', isSelected ? 'true' : 'false');
        }
      }

      function makeRatingButton(value) {
        var btn = doc.createElement('button');
        btn.type = 'button';
        btn.textContent = String(value);
        btn.setAttribute('aria-pressed', 'false');
        styleEl(btn, {
          flex: '1', padding: '4px 0', border: '1px solid #3a3a42',
          borderRadius: '6px', background: '#2a2a31', color: '#f5f5f7', cursor: 'pointer'
        });
        btn.addEventListener('click', function () {
          selectedRating = (selectedRating === value) ? null : value;
          paintRating();
        });
        ratingButtons.push({ value: value, el: btn });
        ratingRow.appendChild(btn);
      }
      for (var r = 1; r <= 5; r++) { makeRatingButton(r); }
      root.appendChild(ratingRow);

      var textarea = doc.createElement('textarea');
      textarea.setAttribute('placeholder', 'Optional comment');
      textarea.maxLength = MAX_MESSAGE;
      styleEl(textarea, {
        width: '100%', boxSizing: 'border-box', minHeight: '48px', resize: 'vertical',
        marginBottom: '6px', background: '#141418', color: '#f5f5f7',
        border: '1px solid #3a3a42', borderRadius: '6px', padding: '6px'
      });
      root.appendChild(textarea);

      var status = doc.createElement('div');
      status.setAttribute('role', 'status');
      styleEl(status, { minHeight: '16px', marginBottom: '4px' });
      root.appendChild(status);
      function setStatus(text) { status.textContent = text; }

      var sendBtn = doc.createElement('button');
      sendBtn.type = 'button';
      sendBtn.textContent = 'Send';
      styleEl(sendBtn, {
        width: '100%', padding: '6px 0', border: 'none', borderRadius: '6px',
        background: '#4c8dff', color: '#0b0b0f', fontWeight: '600', cursor: 'pointer'
      });
      sendBtn.addEventListener('click', function () {
        var message = textarea.value || '';
        if (!message && selectedRating === null) {
          setStatus('Add a rating or a comment first.');
          return;
        }
        if (selectedRating !== null && !isValidRating(selectedRating)) {
          setStatus('Rating must be between 0 and 5.');
          return;
        }
        sendBtn.disabled = true;
        setStatus('Sending…');
        client.submit({ message: message, rating: selectedRating }, function (ok) {
          sendBtn.disabled = false;
          if (ok) {
            setStatus('Thanks for the feedback!');
            textarea.value = '';
            selectedRating = null;
            paintRating();
          } else {
            setStatus('Could not send feedback. Try again.');
          }
        });
      });
      root.appendChild(sendBtn);

      container.appendChild(root);

      return {
        root: root,
        destroy: function () {
          if (root.parentNode) root.parentNode.removeChild(root);
        }
      };
    }

    function styleEl(el, props) {
      for (var k in props) {
        if (Object.prototype.hasOwnProperty.call(props, k)) { el.style[k] = props[k]; }
      }
    }

    window.__feedbackClient = {
      init: init,
      version: 'p5'
    };
  } catch (e) {
    try { console.error('[FeedbackClient] init failed:', e); } catch (e2) {}
  }
})();

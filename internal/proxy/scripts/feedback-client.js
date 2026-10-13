// Feedback client + visitor widget for the public walkthrough-publish plane.
//
// Two layers, both inert until called:
//   init(opts)   the transport: one same-origin POST per submission to
//                /s/{token}/feedback, with the outcome REPORTED to the caller.
//   mount(opts)  the visitor-facing widget: a "Feedback" button that opens a
//                small labelled form and shows sending / success / failure.
// public-boot.js calls mount() once a share has booted; nothing here starts on
// its own, so the module is safe in every role bundle.
//
// The wire contract is the server's (internal/proxy/public_routes.go
// serveFeedback, internal/publish/feedback_store.go), mirrored rather than
// invented:
//   POST, Content-Type application/json, body {message, rating?, stepId?, at?}
//   and NO other key (the decoder rejects unknown fields). rating is an integer
//   0..5, stepId at most 128 bytes, the whole body at most 4096 bytes by
//   default. 202 with an empty body is success; every refusal is text/plain:
//   404 revoked/unknown link, 413 too large, 415 wrong type, 422 rejected,
//   429 rate limited, 503 feedback unavailable.
// The operator may LOWER the body cap (feedback max-body-bytes), which this
// client cannot see, so the local 4096 check only saves a round trip — a 413
// is still handled as a failure like any other.
//
// Failure is surfaced, never silent (Silent Failure Prohibition): a non-2xx
// response and a network error both resolve to {ok: false, ...} and the widget
// tells the visitor. Nothing is truncated to fit — an over-long message is
// refused locally with a reason, exactly as the server would refuse it.
//
// Like every public-plane module it carries NO dev-control surface: none of the
// dev-control namespace, no WS/exec channel, no code-eval sinks, no dynamic
// function compilation, no screenshot-capture library. It runs unchanged under
// the public CSP (P1 INV-12): the request is same-origin (connect-src 'self'),
// and the widget is styled through element.style (CSSOM, which style-src does
// not govern) and built with textContent only — the server's refusal text is
// rendered as text, never as markup. (This header is deliberately free of the
// literal tokens the public forbidden-symbol scan bans.)
//
// Public surface:
//   window.__feedbackClient.init(opts) -> client
//     opts.endpoint         same-origin absolute path to POST entries to
//     opts.onSubmit(record) optional callback invoked per submission
//   client.submit(entry) -> Promise<result>, which NEVER rejects
//     result.ok             true only for a 2xx response
//     result.status         HTTP status, or 0 when no response was received
//     result.error          '' | 'http' | 'network' | 'too-large' | 'unavailable'
//     result.message        server refusal text / error text ('' on success)
//     result.record         the record that was (or would have been) sent
//   window.__feedbackClient.mount(opts) -> widget | null   (idempotent)
//     opts.endpoint         as above
//     opts.stepId()         optional; returns the current step id
//   widget.root / open() / close() / destroy()

(function () {
  'use strict';

  try {
    // Mirrors of the server's defaults (publish.MaxIDLength and
    // config.FeedbackConfig.MaxBodyBytes). Both are byte counts, not characters.
    var MAX_BODY_BYTES = 4096;
    var MAX_STEP_ID_BYTES = 128;
    // Server refusal text is short and constant; this only bounds a surprise.
    var MAX_SERVER_TEXT = 200;
    // Keeps ordinary prose well inside the byte cap. The byte check in submit()
    // is the authoritative one — multi-byte text can exceed it sooner.
    var MAX_MESSAGE_CHARS = 2000;

    var ROOT_ID = '__agnt_feedback';
    var PANEL_ID = '__agnt_feedback_panel';
    var MESSAGE_ID = '__agnt_feedback_message';
    var RATING_ID = '__agnt_feedback_rating';
    var STATUS_ID = '__agnt_feedback_status';

    // sameOriginPath accepts ONLY a same-origin absolute path so this client can
    // never be pointed at a cross-origin sink (connect-src 'self', INV-7). The
    // prior guard (charAt(0)==='/') accepted a PROTOCOL-RELATIVE "//evil.com/collect",
    // which the browser resolves cross-origin — a data-exfil hole. Tighten it:
    //   - must start with exactly one '/',
    //   - reject "//host" (protocol-relative) and "/\\host" (backslash-smuggled,
    //     which browsers normalize to "//host"),
    //   - reject any scheme marker (a same-origin path never carries "://" or a
    //     "javascript:" / "data:" scheme).
    //
    // Those spelling checks are kept as a cheap first pass, but the decision is
    // made on the RESOLVED URL: the string is parsed the way the request would
    // parse it and must land on this page's own origin. A spelling list can
    // never be complete — the URL parser discards tab, CR and LF anywhere in
    // its input, so "/<TAB>/evil.example/c" passes every character test above
    // and still resolves to https://evil.example/c. Whitespace and control
    // characters have no place in a path this client is handed, so they are
    // refused outright rather than normalized.
    function sameOriginPath(s) {
      if (typeof s !== 'string' || s.length === 0) return false;
      if (s.charAt(0) !== '/') return false;
      for (var i = 0; i < s.length; i++) {
        var code = s.charCodeAt(i);
        if (code <= 0x20 || code === 0x7f || code === 0x5c /* backslash */) return false;
      }
      var c1 = s.charAt(1);
      if (c1 === '/') return false;
      if (s.indexOf('://') !== -1) return false;
      if (s.indexOf(':') !== -1 && s.indexOf(':') < s.indexOf('/', 1)) return false;
      try {
        var here = window.location;
        var resolved = new URL(s, here.href);
        // An opaque origin serializes as "null" on both sides; equal strings
        // there prove nothing.
        if (!resolved.origin || resolved.origin === 'null') return false;
        return resolved.origin === here.origin;
      } catch (e) {
        return false; // unparseable, or no usable base: fail closed
      }
    }

    // utf8Length is the encoded size the server will count. The caps are in
    // BYTES, so String.length (UTF-16 units) under-counts any non-ASCII text.
    function utf8Length(s) {
      var n = 0;
      for (var i = 0; i < s.length; i++) {
        var c = s.charCodeAt(i);
        if (c < 0x80) { n += 1; }
        else if (c < 0x800) { n += 2; }
        else if (c >= 0xD800 && c <= 0xDBFF && i + 1 < s.length) { n += 4; i++; }
        else { n += 3; }
      }
      return n;
    }

    function clip(s) {
      s = String(s == null ? '' : s).replace(/^\s+|\s+$/g, '');
      return s.length > MAX_SERVER_TEXT ? s.slice(0, MAX_SERVER_TEXT) : s;
    }

    // The server decodes rating into an integer and accepts 0..5; a fraction
    // fails the decode (422), so anything else is left out rather than sent.
    function validRating(r) {
      return typeof r === 'number' && r >= 0 && r <= 5 && Math.floor(r) === r;
    }

    function failure(status, error, message, record) {
      return { ok: false, status: status, error: error, message: clip(message), record: record };
    }

    // readText reads a refusal body without letting a broken body turn a known
    // HTTP failure into an unknown one.
    function readText(res) {
      try {
        return res.text().then(clip, function () { return ''; });
      } catch (e) {
        return Promise.resolve('');
      }
    }

    function init(opts) {
      opts = opts || {};
      var onSubmit = typeof opts.onSubmit === 'function' ? opts.onSubmit : null;
      // endpoint is accepted only as a same-origin absolute path (see sameOriginPath).
      var endpoint = sameOriginPath(opts.endpoint) ? opts.endpoint : null;

      // submit sends one entry and resolves with what happened. It never rejects,
      // so a caller that ignores the result cannot raise an unhandled rejection,
      // and a caller that reads it cannot mistake a failure for a success.
      function submit(entry) {
        entry = entry || {};
        // Only the keys the server's schema knows. Optional ones are omitted
        // rather than sent empty.
        var record = {
          message: (typeof entry.message === 'string') ? entry.message : '',
          at: Date.now()
        };
        if (validRating(entry.rating)) { record.rating = entry.rating; }
        if (typeof entry.stepId === 'string' && entry.stepId !== '' &&
            utf8Length(entry.stepId) <= MAX_STEP_ID_BYTES) {
          record.stepId = entry.stepId;
        }
        if (onSubmit) {
          try { onSubmit(record); } catch (e) { /* callback owns its errors */ }
        }
        if (!endpoint || typeof fetch !== 'function') {
          return Promise.resolve(failure(0, 'unavailable',
            endpoint ? 'fetch is not available' : 'no feedback endpoint configured', record));
        }
        var body = JSON.stringify(record);
        if (utf8Length(body) > MAX_BODY_BYTES) {
          return Promise.resolve(failure(0, 'too-large', 'feedback too large', record));
        }
        var req;
        try {
          // No credentials: the public plane is anonymous and sets no cookies.
          req = fetch(endpoint, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: body,
            credentials: 'omit'
          });
        } catch (e) {
          return Promise.resolve(failure(0, 'network', e && e.message, record));
        }
        return req.then(function (res) {
          if (res.ok) {
            return { ok: true, status: res.status, error: '', message: '', record: record };
          }
          // fetch resolves for EVERY HTTP status; a 4xx/5xx is a failed
          // submission, and the server's text says which kind.
          return readText(res).then(function (text) {
            return failure(res.status, 'http', text || res.statusText, record);
          });
        }, function (err) {
          return failure(0, 'network', err && err.message, record);
        });
      }

      return { submit: submit };
    }

    // failureText turns a result into what the visitor reads. Statuses the
    // server documents get a plain sentence; anything else shows the status and
    // the server's own text, so an unexpected refusal is still diagnosable.
    function failureText(res) {
      if (res.error === 'too-large' || res.status === 413) {
        return 'That message is too long. Please shorten it and try again.';
      }
      if (res.error === 'network') {
        return 'Feedback could not be sent. Check your connection and try again.';
      }
      if (res.error === 'unavailable' || res.status === 503) {
        return 'Feedback is unavailable right now.';
      }
      if (res.status === 404 || res.status === 410) {
        // Same wording as the boot notice: revoked and unknown stay indistinct.
        return 'This walkthrough link is no longer available.';
      }
      if (res.status === 429) {
        return 'Too many submissions. Please wait a minute and try again.';
      }
      return 'Feedback could not be sent (' + res.status + (res.message ? ': ' + res.message : '') + ').';
    }

    function styleButton(b) {
      var bs = b.style;
      bs.padding = '6px 12px';
      bs.border = '0';
      bs.borderRadius = '6px';
      bs.background = '#33333c';
      bs.color = '#f5f5f7';
      bs.cursor = 'pointer';
      bs.font = 'inherit';
    }

    function styleField(f) {
      var fs = f.style;
      fs.display = 'block';
      fs.boxSizing = 'border-box';
      fs.width = '100%';
      fs.margin = '4px 0 10px';
      fs.padding = '6px 8px';
      fs.border = '1px solid #55555f';
      fs.borderRadius = '6px';
      fs.background = '#111114';
      fs.color = '#f5f5f7';
      fs.font = 'inherit';
    }

    function makeLabel(text, forId) {
      var l = document.createElement('label');
      l.setAttribute('for', forId);
      l.textContent = text;
      return l;
    }

    var mounted = null;

    // mount renders the widget and returns its handle, or null when there is
    // nowhere to render or no usable endpoint. Idempotent: a second call returns
    // the widget already on the page rather than stacking a second one.
    function mount(opts) {
      opts = opts || {};
      if (mounted && mounted.root.parentNode) { return mounted; }
      var host = document.body || document.documentElement;
      if (!host || !sameOriginPath(opts.endpoint)) { return null; }
      var client = init({ endpoint: opts.endpoint });
      var stepId = typeof opts.stepId === 'function' ? opts.stepId : null;
      var sending = false;

      // Top-right: the walkthrough card owns bottom-right and the demo
      // indicator bottom-left. One layer above the card so the open form is
      // never underneath it.
      var root = document.createElement('div');
      root.id = ROOT_ID;
      var rs = root.style;
      rs.position = 'fixed';
      rs.top = '16px';
      rs.right = '16px';
      rs.maxWidth = '300px';
      rs.color = '#f5f5f7';
      rs.font = '14px/1.45 system-ui, sans-serif';
      rs.textAlign = 'right';
      rs.zIndex = '2147483602';

      var toggle = document.createElement('button');
      toggle.type = 'button';
      toggle.textContent = 'Feedback';
      toggle.setAttribute('aria-expanded', 'false');
      toggle.setAttribute('aria-controls', PANEL_ID);
      styleButton(toggle);
      toggle.style.background = '#1e1e24';
      toggle.style.boxShadow = '0 2px 10px rgba(0,0,0,0.35)';
      root.appendChild(toggle);

      var panel = document.createElement('form');
      panel.id = PANEL_ID;
      panel.setAttribute('aria-label', 'Send feedback');
      // Our own messages, not the browser's validation bubble, so every state
      // is announced through the one live region below.
      panel.noValidate = true;
      panel.hidden = true;
      var ps = panel.style;
      ps.display = 'none';
      ps.marginTop = '8px';
      ps.padding = '14px 16px';
      ps.background = '#1e1e24';
      ps.borderRadius = '10px';
      ps.boxShadow = '0 8px 28px rgba(0,0,0,0.4)';
      ps.textAlign = 'left';

      var message = document.createElement('textarea');
      message.id = MESSAGE_ID;
      message.rows = 4;
      message.maxLength = MAX_MESSAGE_CHARS;
      message.setAttribute('aria-describedby', STATUS_ID);
      styleField(message);
      message.style.resize = 'vertical';
      panel.appendChild(makeLabel('Your feedback', MESSAGE_ID));
      panel.appendChild(message);

      var rating = document.createElement('select');
      rating.id = RATING_ID;
      styleField(rating);
      var labels = ['No rating', '1 (poor)', '2', '3', '4', '5 (great)'];
      for (var i = 0; i < labels.length; i++) {
        var o = document.createElement('option');
        o.value = i === 0 ? '' : String(i);
        o.textContent = labels[i];
        rating.appendChild(o);
      }
      panel.appendChild(makeLabel('Rating (optional)', RATING_ID));
      panel.appendChild(rating);

      // Polite live region: sending, success, and failure are all announced
      // here and mirrored on data-state for anything that needs the state.
      var status = document.createElement('div');
      status.id = STATUS_ID;
      status.setAttribute('role', 'status');
      status.setAttribute('aria-live', 'polite');
      status.style.minHeight = '1.45em';
      status.style.marginBottom = '10px';
      panel.appendChild(status);

      var actions = document.createElement('div');
      actions.style.display = 'flex';
      actions.style.gap = '8px';
      var send = document.createElement('button');
      send.type = 'submit';
      send.textContent = 'Send';
      styleButton(send);
      send.style.flex = '1';
      var cancel = document.createElement('button');
      cancel.type = 'button';
      cancel.textContent = 'Close';
      cancel.setAttribute('aria-label', 'Close feedback');
      styleButton(cancel);
      cancel.style.flex = '1';
      actions.appendChild(send);
      actions.appendChild(cancel);
      panel.appendChild(actions);
      root.appendChild(panel);

      function setState(state, text) {
        root.setAttribute('data-state', state);
        status.textContent = text;
        status.style.color = state === 'error' ? '#ff9b9b' : (state === 'success' ? '#9be7a8' : 'inherit');
      }

      function open() {
        panel.hidden = false;
        panel.style.display = 'block';
        toggle.setAttribute('aria-expanded', 'true');
        try { message.focus(); } catch (e) { /* ignore */ }
      }

      function close() {
        panel.hidden = true;
        panel.style.display = 'none';
        toggle.setAttribute('aria-expanded', 'false');
        try { toggle.focus(); } catch (e) { /* ignore */ }
      }

      function done(res) {
        sending = false;
        send.removeAttribute('aria-disabled');
        if (res && res.ok) {
          message.value = '';
          rating.value = '';
          setState('success', 'Thanks — your feedback was sent.');
          return;
        }
        // The message is kept so the visitor can retry without retyping.
        setState('error', failureText(res || failure(0, 'network', '', null)));
      }

      function onSend(e) {
        // The form must never navigate: there is no action to post to, and
        // form-action 'self' would reload the share.
        if (e && e.preventDefault) { e.preventDefault(); }
        if (sending) { return; }
        var text = message.value.replace(/^\s+|\s+$/g, '');
        if (!text) {
          setState('error', 'Please write a message first.');
          try { message.focus(); } catch (e2) { /* ignore */ }
          return;
        }
        var entry = { message: text };
        if (rating.value) { entry.rating = Number(rating.value); }
        if (stepId) {
          try { entry.stepId = stepId(); } catch (e3) { /* a step id is optional */ }
        }
        // aria-disabled, not disabled: a disabled button drops keyboard focus
        // mid-submit; the sending flag is what blocks a double send.
        sending = true;
        send.setAttribute('aria-disabled', 'true');
        setState('sending', 'Sending…');
        client.submit(entry).then(done, function (err) {
          done(failure(0, 'network', err && err.message, null));
        });
      }

      panel.addEventListener('submit', onSend);
      toggle.addEventListener('click', function () {
        if (panel.hidden) { open(); } else { close(); }
      });
      cancel.addEventListener('click', close);
      // Escape closes the form and returns focus to the button that opened it.
      root.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && !panel.hidden) {
          e.preventDefault();
          close();
        }
      });

      setState('idle', '');
      host.appendChild(root);

      mounted = {
        root: root,
        open: open,
        close: close,
        destroy: function () {
          if (root.parentNode) { root.parentNode.removeChild(root); }
          mounted = null;
        }
      };
      return mounted;
    }

    window.__feedbackClient = {
      init: init,
      mount: mount,
      version: 'p13'
    };
  } catch (e) {
    try { console.error('[FeedbackClient] init failed:', e); } catch (e2) {}
  }
})();

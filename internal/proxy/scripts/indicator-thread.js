// Floating Indicator — conversation thread.
//
// The Message tab shows the back and forth between the person at the browser
// and the agent: what they sent (with the proxy's delivery status), what the
// agent replied (channel_reply / proxy toast frames), and whether the agent is
// working right now. Without it a sent message vanished and a reply was a
// toast that faded in seconds.
//
// Delivery status comes from the proxy's 'panel_ack' frame, matched by the
// client_id the panel puts on each panel_message:
//   delivered — the agent's terminal session (agnt run) accepted it
//   logged    — no terminal session is attached; the message is in the proxy
//               log, where a channel-mode or watching agent reads it
//   failed    — refused, or the session did not accept it
// No ack within ACK_TIMEOUT_MS marks the message 'unconfirmed'.
//
// The thread is kept in sessionStorage (per tab, survives reloads), capped at
// MAX_ENTRIES. Loads after indicator-data and before indicator, which calls
// I.thread at runtime. See moduleOrder in embed.go.

(function() {
  'use strict';

  var I = window.__devtool_indicator_internal;
  var TOKENS = I.TOKENS;

  var STORAGE_KEY = '__devtool_thread';
  var MAX_ENTRIES = 50;
  var ACK_TIMEOUT_MS = 10000;

  var STATUS_TEXT = {
    sending: 'Sending…',
    delivered: 'Delivered to agent',
    logged: 'No agent session attached — saved to the proxy log',
    failed: 'Not delivered',
    unconfirmed: 'No confirmation from the proxy'
  };

  var entries = loadEntries();
  var agentActive = false;
  var listEl = null;
  var ackTimers = {};

  function loadEntries() {
    try {
      var saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY));
      if (!Array.isArray(saved)) return [];
      // A message still 'sending' when the page unloaded never got its ack.
      return saved.filter(isEntry).map(function(e) {
        return e.status === 'sending' ? Object.assign({}, e, { status: 'unconfirmed' }) : e;
      });
    } catch (e) {
      return [];
    }
  }

  function isEntry(e) {
    return e && typeof e.id === 'string' && (e.role === 'user' || e.role === 'agent') && typeof e.text === 'string';
  }

  function saveEntries() {
    try {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(entries));
    } catch (e) {}
  }

  function newClientId() {
    return 'm-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 8);
  }

  function push(entry) {
    entries.push(entry);
    if (entries.length > MAX_ENTRIES) entries.splice(0, entries.length - MAX_ENTRIES);
    saveEntries();
    render();
  }

  // addUserMessage records an outgoing message and returns the client_id the
  // caller puts on the panel_message so the proxy's ack can find it.
  function addUserMessage(text, attachmentCount) {
    var id = newClientId();
    push({ id: id, role: 'user', text: text, attachments: attachmentCount || 0, status: 'sending', ts: Date.now() });
    ackTimers[id] = setTimeout(function() {
      delete ackTimers[id];
      setStatus(id, 'unconfirmed', '');
    }, ACK_TIMEOUT_MS);
    return id;
  }

  function setStatus(id, status, detail) {
    for (var i = 0; i < entries.length; i++) {
      if (entries[i].id !== id) continue;
      // An ack that lands after the timeout still wins; a timeout never
      // overwrites an ack.
      if (status === 'unconfirmed' && entries[i].status !== 'sending') return;
      entries[i] = Object.assign({}, entries[i], { status: status, detail: detail || '' });
      saveEntries();
      render();
      return;
    }
  }

  function handleAck(ack) {
    if (!ack || typeof ack.client_id !== 'string') return;
    var status = STATUS_TEXT[ack.status] && ack.status !== 'sending' ? ack.status : 'failed';
    if (ackTimers[ack.client_id]) {
      clearTimeout(ackTimers[ack.client_id]);
      delete ackTimers[ack.client_id];
    }
    setStatus(ack.client_id, status, typeof ack.error === 'string' ? ack.error : '');
  }

  // addAgentMessage records a reply from the agent (a toast frame).
  function addAgentMessage(payload) {
    var text = typeof payload.message === 'string' ? payload.message : '';
    if (!text) return;
    var entry = { id: newClientId(), role: 'agent', text: text, severity: payload.type || 'info', ts: Date.now() };
    if (payload.title) entry.title = String(payload.title);
    // The secret itself is entered in the toast's masked field, never here.
    if (payload.input === 'secret' && payload.name) entry.secretName = String(payload.name);
    push(entry);
  }

  function setAgentActive(active) {
    agentActive = active === true;
    render();
  }

  function clear() {
    entries = [];
    saveEntries();
    render();
  }

  // mount renders the thread into container; the compose tab calls it on
  // every render, so the previous list is simply replaced.
  function mount(container) {
    listEl = document.createElement('div');
    listEl.className = '__devtool-thread';
    listEl.setAttribute('role', 'log');
    listEl.setAttribute('aria-live', 'polite');
    listEl.setAttribute('aria-label', 'Conversation with the agent');
    container.appendChild(listEl);
    render();
  }

  function render() {
    if (!listEl) return;
    listEl.textContent = '';
    listEl.hidden = entries.length === 0 && !agentActive;
    entries.forEach(function(e) { listEl.appendChild(renderEntry(e)); });
    if (agentActive) {
      var working = document.createElement('div');
      working.className = '__devtool-thread-working';
      working.textContent = 'Agent is working…';
      listEl.appendChild(working);
    }
    if (entries.length) {
      var clearBtn = document.createElement('button');
      clearBtn.type = 'button';
      clearBtn.className = '__devtool-thread-clear';
      clearBtn.textContent = 'Clear conversation';
      clearBtn.onclick = clear;
      listEl.appendChild(clearBtn);
    }
    listEl.scrollTop = listEl.scrollHeight;
  }

  function renderEntry(e) {
    var row = document.createElement('div');
    row.className = '__devtool-thread-msg';
    row.setAttribute('data-role', e.role);
    if (e.role === 'agent') row.setAttribute('data-severity', e.severity || 'info');

    var head = document.createElement('div');
    head.className = '__devtool-thread-head';
    head.textContent = (e.role === 'user' ? 'You' : 'Agent' + (e.title ? ' · ' + e.title : '')) + ' · ' + clock(e.ts);
    row.appendChild(head);

    var body = document.createElement('div');
    body.className = '__devtool-thread-body';
    body.textContent = e.text;
    row.appendChild(body);

    var meta = [];
    if (e.attachments) meta.push('+' + e.attachments + ' attachment' + (e.attachments === 1 ? '' : 's'));
    if (e.secretName) meta.push('Asked for secret ' + e.secretName + ' — enter it in the notification');
    if (e.role === 'user' && e.status) {
      meta.push(STATUS_TEXT[e.status] + (e.detail ? ': ' + e.detail : ''));
      row.setAttribute('data-status', e.status);
    }
    if (meta.length) {
      var foot = document.createElement('div');
      foot.className = '__devtool-thread-meta';
      foot.textContent = meta.join(' · ');
      row.appendChild(foot);
    }
    return row;
  }

  function clock(ts) {
    var d = new Date(ts);
    return ('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2);
  }

  // Stylesheet rules, added by injectPanelStyles (stateful styles must live in
  // the stylesheet, not inline).
  function cssRules() {
    var c = TOKENS.colors;
    return [
      '.__devtool-thread { display: flex; flex-direction: column; gap: 8px; max-height: 45vh; overflow-y: auto; margin-bottom: 12px; padding-right: 2px; }',
      '.__devtool-thread[hidden] { display: none; }',
      '.__devtool-thread-msg { max-width: 88%; padding: 8px 10px; border-radius: ' + TOKENS.radius.md + '; font-size: 13px; line-height: 1.45; }',
      '.__devtool-thread-msg[data-role="user"] { align-self: flex-end; background: color-mix(in srgb, ' + c.primary + ' 14%, ' + c.surface + '); color: ' + c.text + '; }',
      '.__devtool-thread-msg[data-role="agent"] { align-self: flex-start; background: ' + c.surfaceAlt + '; color: ' + c.text + '; border-left: 3px solid ' + c.primary + '; }',
      '.__devtool-thread-msg[data-severity="success"] { border-left-color: ' + c.success + '; }',
      '.__devtool-thread-msg[data-severity="warning"] { border-left-color: ' + c.active + '; }',
      '.__devtool-thread-msg[data-severity="error"] { border-left-color: ' + c.error + '; }',
      '.__devtool-thread-head { font-size: 11px; font-weight: 600; color: ' + c.textMuted + '; margin-bottom: 2px; }',
      '.__devtool-thread-body { white-space: pre-wrap; overflow-wrap: anywhere; }',
      '.__devtool-thread-meta { font-size: 11px; color: ' + c.textMuted + '; margin-top: 4px; }',
      '.__devtool-thread-msg[data-status="failed"] .__devtool-thread-meta, .__devtool-thread-msg[data-status="unconfirmed"] .__devtool-thread-meta { color: ' + c.error + '; }',
      '.__devtool-thread-msg[data-status="logged"] .__devtool-thread-meta { color: ' + c.active + '; }',
      '.__devtool-thread-working { align-self: flex-start; font-size: 12px; color: ' + c.textMuted + '; font-style: italic; }',
      '.__devtool-thread-clear { align-self: center; background: none; border: none; color: ' + c.textMuted + '; font-size: 11px; cursor: pointer; text-decoration: underline; }'
    ];
  }

  I.thread = {
    mount: mount,
    addUserMessage: addUserMessage,
    addAgentMessage: addAgentMessage,
    handleAck: handleAck,
    setAgentActive: setAgentActive,
    clear: clear,
    cssRules: cssRules,
    // Read-only copy for the public export and tests.
    entries: function() { return entries.map(function(e) { return Object.assign({}, e); }); }
  };
})();

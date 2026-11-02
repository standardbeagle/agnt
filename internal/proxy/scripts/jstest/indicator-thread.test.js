// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// indicator-thread.js: the Message tab keeps the conversation with the agent.
// A sent message stays in view with the proxy's delivery status (panel_ack,
// matched by client_id), agent replies (toast frames) land in the thread, the
// panel stays open after Send, and the thread survives a reload of the tab.
// Driven through the real core.js transport, so frames reach the indicator
// the way the server's would.

import { afterEach, describe, expect, it } from 'vitest';
import { loadIndicator } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const ACK_TIMEOUT_MS = 10000;

async function boot(opts = {}) {
  // Only the ack timeout is captured; every other timer runs for real.
  const ackTimers = [];
  rt = await loadIndicator({
    role: 'chrome',
    before: (win) => {
      const real = win.setTimeout.bind(win);
      win.setTimeout = (fn, ms, ...rest) => {
        if (ms === ACK_TIMEOUT_MS) { const t = { fn, cleared: false }; ackTimers.push(t); return t; }
        return real(fn, ms, ...rest);
      };
      const realClear = win.clearTimeout.bind(win);
      win.clearTimeout = (t) => { if (t && typeof t === 'object' && 'cleared' in t) t.cleared = true; else realClear(t); };
      if (opts.before) opts.before(win);
    },
  });
  const { win, ind, root } = rt;
  const sock = rt.sockets[0];
  sock.open();
  ind.togglePanel(true);

  const textarea = () => root.querySelector('#__devtool-message');
  const send = (text) => {
    textarea().value = text;
    textarea().dispatchEvent(new win.Event('input'));
    textarea().dispatchEvent(new win.KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true, cancelable: true }));
  };
  const sent = () => sock.sent.filter((m) => m.type === 'panel_message');
  const serverSends = (msg) => sock.onmessage({ data: JSON.stringify(msg) });
  const rows = () => [...root.querySelectorAll('.__devtool-thread-msg')];
  const expireAckTimers = () => {
    for (const t of ackTimers.filter((x) => !x.cleared)) { t.cleared = true; t.fn(); }
  };
  return { win, ind, root, sock, textarea, send, sent, serverSends, rows, expireAckTimers };
}

describe('conversation thread', { timeout: 30_000 }, () => {
  it('keeps a sent message in view with its delivery status, and the panel open', async () => {
    const t = await boot();
    expect(t.root.querySelector('.__devtool-thread').hidden).toBe(true);

    t.send('the save button does nothing');
    expect(t.sent()).toHaveLength(1);
    const clientId = t.sent()[0].data.payload.client_id;
    expect(clientId).toMatch(/^m-/);
    expect(t.textarea().value).toBe('');
    expect(t.ind.state.isExpanded).toBe(true);

    expect(t.rows()).toHaveLength(1);
    expect(t.rows()[0].getAttribute('data-role')).toBe('user');
    expect(t.rows()[0].textContent).toContain('the save button does nothing');
    expect(t.rows()[0].getAttribute('data-status')).toBe('sending');

    t.serverSends({ type: 'panel_ack', client_id: clientId, status: 'delivered' });
    expect(t.rows()[0].getAttribute('data-status')).toBe('delivered');
    expect(t.rows()[0].textContent).toContain('Delivered to agent');
    // The ack cleared the timeout: expiring it changes nothing.
    t.expireAckTimers();
    expect(t.rows()[0].getAttribute('data-status')).toBe('delivered');
  });

  it('shows why a message did not reach the agent', async () => {
    const t = await boot();
    t.send('one');
    t.send('two');
    t.send('three');
    const [a, b] = t.sent().map((m) => m.data.payload.client_id);
    t.serverSends({ type: 'panel_ack', client_id: a, status: 'logged' });
    t.serverSends({ type: 'panel_ack', client_id: b, status: 'failed', error: 'the agent session did not accept it' });
    t.expireAckTimers();
    const status = t.rows().map((r) => r.getAttribute('data-status'));
    expect(status).toEqual(['logged', 'failed', 'unconfirmed']);
    expect(t.rows()[0].textContent).toContain('No agent session attached');
    expect(t.rows()[1].textContent).toContain('the agent session did not accept it');
    expect(t.rows()[2].textContent).toContain('No confirmation from the proxy');
    // An unknown status from the server is a failure, never "sending".
    t.send('four');
    t.serverSends({ type: 'panel_ack', client_id: t.sent()[3].data.payload.client_id, status: 'sending' });
    expect(t.rows()[3].getAttribute('data-status')).toBe('failed');
  });

  it('a send while disconnected is marked failed and the draft is kept', async () => {
    const t = await boot();
    t.sock.serverClose();
    t.send('are you there?');
    expect(t.sent()).toHaveLength(0);
    expect(t.textarea().value).toBe('are you there?');
    expect(t.rows()[0].getAttribute('data-status')).toBe('failed');
    expect(t.rows()[0].textContent).toContain('not connected');
  });

  it('agent replies land in the thread, the working line follows activity', async () => {
    const t = await boot();
    t.send('which layout?');
    t.serverSends({ type: 'activity', payload: { active: true } });
    expect(t.root.querySelector('.__devtool-thread-working')).toBeTruthy();

    t.serverSends({ type: 'toast', payload: { type: 'warning', title: 'Choose layout', message: 'Grid or list?' } });
    t.serverSends({ type: 'toast', payload: { type: 'info', message: 'Paste your key', input: 'secret', name: 'FIGMA_KEY' } });
    t.serverSends({ type: 'toast', payload: { type: 'info', message: '' } });
    t.serverSends({ type: 'activity', payload: { active: false } });

    const rows = t.rows();
    expect(rows.map((r) => r.getAttribute('data-role'))).toEqual(['user', 'agent', 'agent']);
    expect(rows[1].getAttribute('data-severity')).toBe('warning');
    expect(rows[1].textContent).toContain('Choose layout');
    expect(rows[1].textContent).toContain('Grid or list?');
    expect(rows[2].textContent).toContain('Asked for secret FIGMA_KEY');
    expect(t.root.querySelector('.__devtool-thread-working')).toBeNull();
    expect(t.ind.conversation().map((e) => e.role)).toEqual(['user', 'agent', 'agent']);
  });

  it('the thread survives a reload; a message unacked at unload reads unconfirmed', async () => {
    const t = await boot();
    t.send('first');
    t.serverSends({ type: 'panel_ack', client_id: t.sent()[0].data.payload.client_id, status: 'delivered' });
    t.send('second');
    const saved = t.win.sessionStorage.getItem('__devtool_thread');
    rt.close();
    rt = null;

    const t2 = await boot({ before: (win) => win.sessionStorage.setItem('__devtool_thread', saved) });
    expect(t2.rows().map((r) => r.getAttribute('data-status'))).toEqual(['delivered', 'unconfirmed']);

    t2.root.querySelector('.__devtool-thread-clear').click();
    expect(t2.rows()).toHaveLength(0);
    expect(JSON.parse(t2.win.sessionStorage.getItem('__devtool_thread'))).toEqual([]);
  });

  it('keeps the newest 50 entries and ignores malformed saved ones', async () => {
    const seeded = [{ id: 'x', role: 'robot', text: 'no' }, { role: 'user', text: 'no id' }];
    for (let i = 0; i < 60; i++) seeded.push({ id: 'a' + i, role: 'agent', text: 'reply ' + i, ts: 0 });
    const t = await boot({ before: (win) => win.sessionStorage.setItem('__devtool_thread', JSON.stringify(seeded)) });
    expect(t.rows()).toHaveLength(60);
    t.send('newest');
    const conv = t.ind.conversation();
    expect(conv).toHaveLength(50);
    expect(conv[0].text).toBe('reply 11');
    expect(conv[49].text).toBe('newest');
  });
});

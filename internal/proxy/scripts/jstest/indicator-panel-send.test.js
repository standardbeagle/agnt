// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// indicator.js panel send: a message the socket did not take stays put.
//
// core.send returns false while the socket is not open (reconnecting). The
// panel must then keep the text, the attachments and itself open, and say the
// message was not sent — not clear everything as if it went out.

import { afterEach, describe, expect, it } from 'vitest';
import { loadIndicator } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

function compose(ind, root) {
  ind.togglePanel(true);
  ind.addAttachment('element', { selector: '#x', tag: 'div', text: 'hi' });
  const textarea = root.querySelector('#__devtool-message');
  textarea.value = 'please fix the header';
  const send = [...root.querySelectorAll('button')].find((b) => b.title === 'Send message (Ctrl+Enter)');
  return { textarea, send };
}

// Each case evaluates the whole indicator bundle in jsdom (~1s alone); under
// the full parallel tier that can pass vitest's 5s default, so the ceiling is
// a generous liveness bound, not a latency budget.
describe('panel send', { timeout: 30_000 }, () => {
  it('keeps the message, attachments and panel when the socket is not open', async () => {
    rt = await loadIndicator();
    const { ind, root } = rt;
    expect(rt.sockets[0].readyState, 'socket still CONNECTING').toBe(0);
    const { textarea, send } = compose(ind, root);
    send.click();

    expect(textarea.value).toBe('please fix the header');
    expect(ind.state.attachments).toHaveLength(1);
    expect(ind.state.isExpanded).toBe(true);
    expect(ind.state.microToast.textContent).toMatch(/not sent/i);
  });

  it('clears and closes after a send the socket accepted', async () => {
    rt = await loadIndicator();
    const { ind, root } = rt;
    rt.sockets[0].open();
    const { textarea, send } = compose(ind, root);
    send.click();

    expect(rt.sockets[0].sent.filter((m) => m.type === 'panel_message')).toHaveLength(1);
    expect(textarea.value).toBe('');
    expect(ind.state.attachments).toHaveLength(0);
    expect(ind.state.isExpanded).toBe(false);
  });
});

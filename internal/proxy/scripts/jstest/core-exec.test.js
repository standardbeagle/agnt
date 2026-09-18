import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { describe, expect, it } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedFrames = readFileSync(join(scriptsDir, 'frames.js'), 'utf8');
const shippedCore = readFileSync(join(scriptsDir, 'core.js'), 'utf8');

function runtime(role, frameId) {
  const dom = new JSDOM('<!doctype html><body></body>', {
    runScripts: 'outside-only',
    url: `http://proxy.test/page?__devtool_frame=${frameId}`,
  });
  const sent = [];
  const sockets = [];

  class FakeWebSocket {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;

    constructor() {
      this.readyState = FakeWebSocket.OPEN;
      sockets.push(this);
    }

    send(payload) {
      sent.push(JSON.parse(payload));
    }

    close() {
      this.readyState = FakeWebSocket.CLOSED;
    }
  }

  dom.window.WebSocket = FakeWebSocket;
  dom.window.__devtool_role = role;
  dom.window.__devtool_frame_id = frameId;
  dom.window.eval(shippedFrames);
  dom.window.eval(shippedCore);

  const socket = sockets.at(-1);
  if (!socket) throw new Error(`${role} runtime did not open its control socket`);

  return {
    window: dom.window,
    execute(message) {
      socket.onmessage({ data: JSON.stringify({ type: 'execute', ...message }) });
    },
    execution(id) {
      return sent.find((message) =>
        message.type === 'execution' && message.data.exec_id === id);
    },
    close() {
      dom.window.close();
    },
  };
}

async function settle() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('shipped proxy exec runtime', () => {
  it('settles synchronous values, Promises, throws, and rejections', async () => {
    const content = runtime('content', 'content-a');
    try {
      content.execute({ id: 'sync', frame_id: 'content-a', code: '({answer: 42})' });
      content.execute({ id: 'promise', frame_id: 'content-a', code: 'Promise.resolve("done")' });
      content.execute({ id: 'throw', frame_id: 'content-a', code: '(() => { throw new Error("boom") })()' });
      content.execute({ id: 'reject', frame_id: 'content-a', code: 'Promise.reject(new Error("nope"))' });
      await settle();

      expect(content.execution('sync').data).toMatchObject({ result: '{"answer":42}', error: '' });
      expect(content.execution('promise').data).toMatchObject({ result: 'done', error: '' });
      expect(content.execution('throw').data.error).toContain('Error: boom');
      expect(content.execution('reject').data.error).toContain('Error: nope');
    } finally {
      content.close();
    }
  });

  it('routes inner, outer, and explicit-frame execution under always-wrap', () => {
    const contentA = runtime('content', 'content-a');
    const contentB = runtime('content', 'content-b');
    const chrome = runtime('chrome', 'chrome-a');
    try {
      const runtimes = [contentA, contentB, chrome];
      for (const target of [
        { id: 'inner', frame_id: 'content-a' },
        { id: 'explicit', frame_id: 'content-b' },
        { id: 'outer', frame_id: '@chrome' },
        { id: 'pre-active', frame_id: '' },
      ]) {
        for (const candidate of runtimes) {
          candidate.execute({ ...target, code: 'window.__devtool_frame_role' });
        }
      }

      expect(contentA.execution('inner').data.result).toBe('content');
      expect(contentB.execution('inner')).toBeUndefined();
      expect(chrome.execution('inner')).toBeUndefined();

      expect(contentB.execution('explicit').data.result).toBe('content');
      expect(contentA.execution('explicit')).toBeUndefined();
      expect(chrome.execution('explicit')).toBeUndefined();

      expect(chrome.execution('outer').data.result).toBe('chrome');
      expect(contentA.execution('outer')).toBeUndefined();
      expect(contentB.execution('outer')).toBeUndefined();

      expect(contentA.execution('pre-active').data.result).toBe('content');
      expect(contentB.execution('pre-active').data.result).toBe('content');
      expect(chrome.execution('pre-active')).toBeUndefined();
    } finally {
      contentA.close();
      contentB.close();
      chrome.close();
    }
  });
});

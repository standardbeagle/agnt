// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// voice.js proxy capture: start() asks for the microphone, and getUserMedia
// can sit on a permission prompt for as long as the user likes. A stop() or a
// second toggle() in that window must cancel the start — the stream that
// resolves afterwards has its tracks stopped, no voice_start goes out, and no
// second capture is opened beside the first.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const flush = () => new Promise((r) => setTimeout(r, 0));

function fakeStream() {
  const track = { stopped: false, stop() { this.stopped = true; } };
  return { track, getTracks: () => [track] };
}

function boot({ mediaDevices = true } = {}) {
  const requests = []; // one per getUserMedia call: {resolve, reject}
  const contexts = [];
  rt = loadModules(['frames', 'core', 'voice'], {
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      if (mediaDevices) {
        Object.defineProperty(win.navigator, 'mediaDevices', {
          configurable: true,
          value: {
            getUserMedia: () => new win.Promise((resolve, reject) => requests.push({ resolve, reject })),
          },
        });
      }
      win.AudioContext = class {
        constructor() { this.closed = false; this.destination = {}; contexts.push(this); }
        createMediaStreamSource() { return { connect() {}, disconnect() {} }; }
        createScriptProcessor() { return { connect() {}, disconnect() {} }; }
        close() { this.closed = true; }
      };
    },
  });
  expect(rt.errors).toEqual({});
  const sock = rt.sockets[0];
  sock.open(); // provider 'proxy' needs a live control socket
  const voice = rt.window.__devtool_voice;
  const sent = (type) => sock.sent.filter((m) => m.type === type);
  return { voice, requests, contexts, sent, win: rt.window };
}

describe('voice start while the microphone prompt is open', () => {
  it('streams normally when nothing interrupts the start', async () => {
    const { voice, requests, contexts, sent } = boot();
    expect(voice.start().provider).toBe('proxy');
    const stream = fakeStream();
    requests[0].resolve(stream);
    await flush();
    expect(sent('voice_start')).toHaveLength(1);
    expect(contexts).toHaveLength(1);
    expect(voice.getStatus().isListening).toBe(true);

    voice.stop();
    expect(stream.track.stopped).toBe(true);
    expect(contexts[0].closed).toBe(true);
  });

  it('stop() cancels a pending start and releases the stream that arrives later', async () => {
    const { voice, requests, contexts, sent } = boot();
    voice.start();
    voice.stop();
    const stream = fakeStream();
    requests[0].resolve(stream);
    await flush();
    expect(stream.track.stopped, 'the microphone must not stay open after stop()').toBe(true);
    expect(sent('voice_start')).toHaveLength(0);
    expect(contexts).toHaveLength(0);
    expect(voice.getStatus().isListening).toBe(false);
  });

  it('a second toggle() cancels the pending start instead of opening a second capture', async () => {
    const { voice, requests, contexts, sent } = boot();
    voice.toggle();
    voice.toggle();
    expect(requests, 'one microphone request only').toHaveLength(1);
    const stream = fakeStream();
    requests[0].resolve(stream);
    await flush();
    expect(stream.track.stopped).toBe(true);
    expect(sent('voice_start')).toHaveLength(0);
    expect(contexts).toHaveLength(0);
  });

  it('start() twice while pending opens one capture', async () => {
    const { voice, requests, contexts, sent } = boot();
    voice.start();
    voice.start();
    expect(requests).toHaveLength(1);
    requests[0].resolve(fakeStream());
    await flush();
    expect(sent('voice_start')).toHaveLength(1);
    expect(contexts).toHaveLength(1);
  });

  it('reports, rather than throws, when the page has no mediaDevices', () => {
    const { voice } = boot({ mediaDevices: false });
    const errors = [];
    voice.configure({ onError: (e) => errors.push(e.error) });
    let res;
    expect(() => { res = voice.start(); }).not.toThrow();
    expect(res.error).toBeTruthy();
    expect(errors).toEqual(['mic-unavailable']);
  });
});

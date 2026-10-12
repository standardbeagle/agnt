import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { describe, expect, it } from 'vitest';

// The visitor feedback flow on a published walkthrough: public-boot.js mounts
// the widget from feedback-client.js once a share has booted. Both files are
// evaluated verbatim, in bundle order, so these assert the shipped bytes.

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const feedbackClient = readFileSync(join(scriptsDir, 'feedback-client.js'), 'utf8');
const publicBoot = readFileSync(join(scriptsDir, 'public-boot.js'), 'utf8');

const TOKEN = 'a'.repeat(43);
const WALKTHROUGH = {
  version: 'v1',
  id: 'wt',
  title: 'Tour',
  steps: [
    { id: 'intro', title: 'Intro', body: 'hello' },
    { id: 'cart', title: 'Cart', body: 'bye' },
  ],
};

const response = (status, text = '') => ({
  ok: status >= 200 && status < 300,
  status,
  statusText: '',
  text: () => Promise.resolve(text),
});

const flush = () => new Promise((r) => setTimeout(r, 0));

// page builds a served-share window. `feedback` answers the feedback POST: a
// response, or a function returning a promise (for rejections and for holding
// a request open). `walkthrough` is a status code to fail the artifact fetch.
async function page({
  path = `/s/${TOKEN}`,
  publicPlane = true,
  walkthrough = WALKTHROUGH,
  withClient = true,
  feedback = () => Promise.resolve(response(202)),
} = {}) {
  const dom = new JSDOM('<!doctype html><body></body>', {
    runScripts: 'outside-only',
    url: `http://localhost:8899${path}`,
  });
  const w = dom.window;
  if (publicPlane) w.__agnt_public_version = 'test';
  const warnings = [];
  w.console.warn = (...args) => warnings.push(args.join(' '));

  let index = 0;
  w.__walkthroughViewer = {
    create: () => {
      const player = { start: () => player, activeIndex: () => index };
      return player;
    },
  };

  const posts = [];
  w.fetch = (url, opts = {}) => {
    if (opts.method === 'POST') {
      posts.push({ url, opts, body: JSON.parse(opts.body) });
      return feedback();
    }
    if (typeof walkthrough === 'number') return Promise.resolve(response(walkthrough));
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(walkthrough) });
  };

  if (withClient) w.eval(feedbackClient);
  w.eval(publicBoot);
  await flush();

  const doc = w.document;
  return {
    w,
    doc,
    posts,
    warnings,
    setStep: (i) => { index = i; },
    root: doc.getElementById('__agnt_feedback'),
    toggle: doc.querySelector('#__agnt_feedback > button'),
    panel: doc.getElementById('__agnt_feedback_panel'),
    message: doc.getElementById('__agnt_feedback_message'),
    rating: doc.getElementById('__agnt_feedback_rating'),
    status: doc.getElementById('__agnt_feedback_status'),
    send: doc.querySelector('#__agnt_feedback_panel button[type=submit]'),
  };
}

// send fills the form and submits it the way a visitor does: the Send button.
async function send(p, text, rating = '') {
  p.toggle.click();
  p.message.value = text;
  p.rating.value = rating;
  p.send.click();
  await flush();
}

describe('feedback widget enablement', () => {
  it('renders on a booted share, closed, with every control labelled', async () => {
    const p = await page();
    expect(p.w.__agntPublicBoot.player()).not.toBeNull();
    expect(p.root).not.toBeNull();
    expect(p.w.__agntPublicBoot.feedback().root).toBe(p.root);

    expect(p.toggle.textContent).toBe('Feedback');
    expect(p.toggle.getAttribute('aria-expanded')).toBe('false');
    expect(p.toggle.getAttribute('aria-controls')).toBe(p.panel.id);
    expect(p.panel.hidden).toBe(true);
    expect(p.panel.getAttribute('aria-label')).toBe('Send feedback');

    // Each field has a real <label for>, not placeholder text.
    for (const field of [p.message, p.rating]) {
      const label = p.doc.querySelector(`label[for="${field.id}"]`);
      expect(label).not.toBeNull();
      expect(label.textContent).not.toBe('');
    }
    expect(p.status.getAttribute('role')).toBe('status');
    expect(p.status.getAttribute('aria-live')).toBe('polite');
    expect(p.root.dataset.state).toBe('idle');
    // Every control is a native, tabbable element.
    expect([p.toggle, p.send].map((b) => b.tagName)).toEqual(['BUTTON', 'BUTTON']);
    expect(p.warnings).toEqual([]);
  });

  it('is absent outside the public plane', async () => {
    const p = await page({ publicPlane: false });
    expect(p.root).toBeNull();
    expect(p.posts).toEqual([]);
  });

  it('is absent on a public path that is not a share route', async () => {
    const p = await page({ path: `/s/${TOKEN}/walkthrough.json` });
    expect(p.root).toBeNull();
  });

  it('is absent when the share is revoked or unknown', async () => {
    const p = await page({ walkthrough: 404 });
    expect(p.root).toBeNull();
    expect(p.doc.getElementById('__wt_public_notice').textContent)
      .toBe('This walkthrough link is no longer available.');
  });

  it('is absent when the walkthrough has no steps', async () => {
    const p = await page({ walkthrough: { ...WALKTHROUGH, steps: [] } });
    expect(p.root).toBeNull();
  });

  it('leaves the walkthrough running when the feedback client is missing', async () => {
    const p = await page({ withClient: false });
    expect(p.root).toBeNull();
    expect(p.w.__agntPublicBoot.player()).not.toBeNull();
    expect(p.doc.getElementById('__wt_public_notice')).toBeNull();
    expect(p.warnings.join('\n')).toContain('feedback client missing');
  });

  it('leaves the walkthrough running when the widget throws on mount', async () => {
    const p = await page({ withClient: false, walkthrough: 500 });
    // Re-boot by hand with a client whose mount throws.
    p.w.__feedbackClient = { mount: () => { throw new Error('boom'); } };
    p.w.fetch = () => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(WALKTHROUGH) });
    p.w.__agntPublicBoot.boot(TOKEN);
    await flush();
    expect(p.w.__agntPublicBoot.player()).not.toBeNull();
    expect(p.warnings.join('\n')).toContain('feedback widget failed to mount');
  });

  it('mounts once however often it is asked', async () => {
    const p = await page();
    const again = p.w.__feedbackClient.mount({ endpoint: `/s/${TOKEN}/feedback` });
    expect(again.root).toBe(p.root);
    expect(p.doc.querySelectorAll('#__agnt_feedback')).toHaveLength(1);
  });
});

describe('feedback widget submission', () => {
  it('posts the server schema to the share\'s own feedback route and reports success', async () => {
    const p = await page();
    p.setStep(1);
    await send(p, '  loved it  ', '4');

    expect(p.posts).toHaveLength(1);
    const { url, opts, body } = p.posts[0];
    expect(url).toBe(`/s/${TOKEN}/feedback`);
    expect(opts.method).toBe('POST');
    expect(opts.headers['Content-Type']).toBe('application/json');
    expect(opts.credentials).toBe('omit');
    // Exactly the keys the server's strict decoder accepts.
    expect(Object.keys(body).sort()).toEqual(['at', 'message', 'rating', 'stepId']);
    expect(body).toMatchObject({ message: 'loved it', rating: 4, stepId: 'cart' });
    expect(Number.isInteger(body.at)).toBe(true);

    expect(p.root.dataset.state).toBe('success');
    expect(p.status.textContent).toBe('Thanks — your feedback was sent.');
    expect(p.message.value).toBe('');
    expect(p.rating.value).toBe('');
  });

  it('omits the rating when none is chosen', async () => {
    const p = await page();
    await send(p, 'no rating');
    expect(Object.keys(p.posts[0].body).sort()).toEqual(['at', 'message', 'stepId']);
  });

  it('shows a sending state while the request is open and sends only once', async () => {
    let release;
    const p = await page({ feedback: () => new Promise((r) => { release = r; }) });
    await send(p, 'slow');
    expect(p.root.dataset.state).toBe('sending');
    expect(p.status.textContent).toBe('Sending…');
    expect(p.send.getAttribute('aria-disabled')).toBe('true');

    p.send.click();
    await flush();
    expect(p.posts).toHaveLength(1);

    release(response(202));
    await flush();
    expect(p.root.dataset.state).toBe('success');
    expect(p.send.hasAttribute('aria-disabled')).toBe(false);
  });

  it('refuses an empty message without calling the server', async () => {
    const p = await page();
    await send(p, '   ');
    expect(p.posts).toEqual([]);
    expect(p.root.dataset.state).toBe('error');
    expect(p.status.textContent).toBe('Please write a message first.');
  });

  it.each([
    [429, 'rate limited', 'Too many submissions. Please wait a minute and try again.'],
    [413, 'feedback too large', 'That message is too long. Please shorten it and try again.'],
    [503, 'feedback unavailable', 'Feedback is unavailable right now.'],
    [404, '404 page not found', 'This walkthrough link is no longer available.'],
    [422, 'feedback rejected', 'Feedback could not be sent (422: feedback rejected).'],
  ])('surfaces an HTTP %i response as a failure', async (status, text, shown) => {
    const p = await page({ feedback: () => Promise.resolve(response(status, `${text}\n`)) });
    await send(p, 'keep me');
    expect(p.posts).toHaveLength(1);
    expect(p.root.dataset.state).toBe('error');
    expect(p.status.textContent).toBe(shown);
    // The visitor's text survives so a retry needs no retyping.
    expect(p.message.value).toBe('keep me');
    expect(p.send.hasAttribute('aria-disabled')).toBe(false);
  });

  it('renders server refusal text as text, never markup', async () => {
    const p = await page({ feedback: () => Promise.resolve(response(500, '<img src=x onerror=alert(1)>')) });
    await send(p, 'hi');
    expect(p.root.querySelector('img')).toBeNull();
    expect(p.status.textContent).toContain('<img src=x onerror=alert(1)>');
  });

  it('surfaces a network error as a failure', async () => {
    const p = await page({ feedback: () => Promise.reject(new TypeError('Failed to fetch')) });
    await send(p, 'offline');
    expect(p.posts).toHaveLength(1);
    expect(p.root.dataset.state).toBe('error');
    expect(p.status.textContent).toBe('Feedback could not be sent. Check your connection and try again.');
    expect(p.message.value).toBe('offline');
  });

  it('recovers from a failure on the next attempt', async () => {
    let calls = 0;
    const p = await page({
      feedback: () => (++calls === 1 ? Promise.reject(new TypeError('down')) : Promise.resolve(response(202))),
    });
    await send(p, 'retry');
    expect(p.root.dataset.state).toBe('error');
    p.send.click();
    await flush();
    expect(p.posts).toHaveLength(2);
    expect(p.root.dataset.state).toBe('success');
  });
});

describe('feedback widget keyboard', () => {
  it('moves focus into the form on open and back to the button on Escape', async () => {
    const p = await page();
    p.toggle.click();
    expect(p.panel.hidden).toBe(false);
    expect(p.toggle.getAttribute('aria-expanded')).toBe('true');
    expect(p.doc.activeElement).toBe(p.message);

    const esc = new p.w.KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    p.message.dispatchEvent(esc);
    expect(esc.defaultPrevented).toBe(true);
    expect(p.panel.hidden).toBe(true);
    expect(p.toggle.getAttribute('aria-expanded')).toBe('false');
    expect(p.doc.activeElement).toBe(p.toggle);
  });
});

describe('feedback client transport', () => {
  const client = async (opts) => {
    const p = await page(opts);
    return { p, fc: p.w.__feedbackClient.init({ endpoint: `/s/${TOKEN}/feedback` }) };
  };

  it('resolves ok for a 202', async () => {
    const { fc } = await client();
    const res = await fc.submit({ message: 'hi', rating: 5 });
    expect(res).toMatchObject({ ok: true, status: 202, error: '', message: '' });
    expect(res.record).toMatchObject({ message: 'hi', rating: 5 });
  });

  it('resolves a failure carrying the status and server text for a non-2xx', async () => {
    const { fc } = await client({ feedback: () => Promise.resolve(response(429, 'rate limited\n')) });
    const res = await fc.submit({ message: 'hi' });
    expect(res).toMatchObject({ ok: false, status: 429, error: 'http', message: 'rate limited' });
  });

  it('resolves a failure for a rejected fetch instead of swallowing it', async () => {
    const { fc } = await client({ feedback: () => Promise.reject(new TypeError('Failed to fetch')) });
    const res = await fc.submit({ message: 'hi' });
    expect(res).toMatchObject({ ok: false, status: 0, error: 'network', message: 'Failed to fetch' });
  });

  it('refuses a body over the 4096-byte cap locally, counting bytes not characters', async () => {
    const { p, fc } = await client();
    // 1500 three-byte characters: well under 4096 characters, over 4096 bytes.
    const res = await fc.submit({ message: '€'.repeat(1500) });
    expect(res).toMatchObject({ ok: false, status: 0, error: 'too-large' });
    expect(p.posts).toEqual([]);
    // The same count of one-byte characters fits and is sent.
    expect((await fc.submit({ message: 'a'.repeat(1500) })).ok).toBe(true);
  });

  it('never sends a rating or step id the server would reject', async () => {
    const { p, fc } = await client();
    await fc.submit({ message: 'hi', rating: 4.5, stepId: 's'.repeat(129) });
    await fc.submit({ message: 'hi', rating: 9 });
    for (const post of p.posts) {
      expect(Object.keys(post.body).sort()).toEqual(['at', 'message']);
    }
  });

  it.each(['//evil.example/collect', '/\\evil.example', 'https://evil.example/x', 'feedback', ''])(
    'rejects the cross-origin or relative endpoint %j',
    async (endpoint) => {
      const p = await page({ publicPlane: false });
      expect(p.w.__feedbackClient.mount({ endpoint })).toBeNull();
      const res = await p.w.__feedbackClient.init({ endpoint }).submit({ message: 'hi' });
      expect(res).toMatchObject({ ok: false, status: 0, error: 'unavailable' });
      expect(p.posts).toEqual([]);
    },
  );
});

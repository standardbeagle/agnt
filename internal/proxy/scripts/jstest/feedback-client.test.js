import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { afterEach, describe, expect, it, vi } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedFeedbackClient = readFileSync(join(scriptsDir, 'feedback-client.js'), 'utf8');

function runtime() {
  const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only' });
  dom.window.eval(shippedFeedbackClient);
  return dom.window;
}

function jsonResponse(ok, status) {
  return Promise.resolve({ ok, status, json: () => Promise.resolve({}) });
}

describe('shipped feedback client', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('POSTs to the given endpoint and reports success', async () => {
    const window = runtime();
    const fetchMock = vi.fn(() => jsonResponse(true, 202));
    window.fetch = fetchMock;

    const client = window.__feedbackClient.init({ endpoint: '/s/tok123/feedback' });
    let result = null;
    const record = client.submit({ message: 'nice demo', rating: 4 }, (ok, status) => {
      result = { ok, status };
    });

    await Promise.resolve();
    await Promise.resolve();

    expect(record.message).toBe('nice demo');
    expect(record.rating).toBe(4);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/s/tok123/feedback');
    expect(JSON.parse(init.body)).toMatchObject({ message: 'nice demo', rating: 4 });
    expect(result).toEqual({ ok: true, status: 202 });
  });

  it('reports failure when the server rejects the submission', async () => {
    const window = runtime();
    window.fetch = vi.fn(() => jsonResponse(false, 422));

    const client = window.__feedbackClient.init({ endpoint: '/s/tok123/feedback' });
    let result = null;
    client.submit({ message: 'bad' }, (ok, status) => { result = { ok, status }; });

    await Promise.resolve();
    await Promise.resolve();

    expect(result).toEqual({ ok: false, status: 422 });
  });

  it('reports failure when the network request itself throws', async () => {
    const window = runtime();
    window.fetch = vi.fn(() => Promise.reject(new Error('offline')));

    const client = window.__feedbackClient.init({ endpoint: '/s/tok123/feedback' });
    let result = null;
    client.submit({ message: 'bad' }, (ok) => { result = ok; });

    await Promise.resolve();
    await Promise.resolve();

    expect(result).toBe(false);
  });

  it('drops an out-of-range or non-integer rating to null instead of sending it', () => {
    const window = runtime();
    window.fetch = vi.fn(() => jsonResponse(true, 202));
    const client = window.__feedbackClient.init({});

    expect(client.submit({ rating: 5 }).rating).toBe(5);
    expect(client.submit({ rating: 0 }).rating).toBe(0);
    expect(client.submit({ rating: 6 }).rating).toBeNull();
    expect(client.submit({ rating: -1 }).rating).toBeNull();
    expect(client.submit({ rating: 2.5 }).rating).toBeNull();
    expect(client.submit({ rating: 'five' }).rating).toBeNull();
  });

  it('never sends to a protocol-relative or cross-origin endpoint', () => {
    const window = runtime();
    const fetchMock = vi.fn(() => jsonResponse(true, 202));
    window.fetch = fetchMock;

    const client = window.__feedbackClient.init({ endpoint: '//evil.example/collect' });
    client.submit({ message: 'x' });

    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('shipped feedback widget', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  function mountedWidget(fetchImpl) {
    const window = runtime();
    window.fetch = vi.fn(fetchImpl);
    const client = window.__feedbackClient.init({ endpoint: '/s/tok123/feedback' });
    const widget = client.mount({});
    return { window, widget, fetchMock: window.fetch };
  }

  it('renders a rating row, a comment box, a send button, and a status line', () => {
    const { widget } = mountedWidget(() => jsonResponse(true, 202));
    const buttons = widget.root.querySelectorAll('button');
    // 5 rating buttons + 1 Send button.
    expect(buttons.length).toBe(6);
    expect(widget.root.querySelector('textarea')).not.toBeNull();
    expect(widget.root.querySelector('[role="status"]')).not.toBeNull();
  });

  it('refuses to send with neither a rating nor a comment', () => {
    const { widget } = mountedWidget(() => jsonResponse(true, 202));
    const sendBtn = widget.root.querySelectorAll('button')[5];
    sendBtn.click();
    expect(widget.root.querySelector('[role="status"]').textContent).toMatch(/rating or a comment/);
  });

  it('shows a thank-you and clears the form on a successful submission', async () => {
    const { widget, fetchMock } = mountedWidget(() => jsonResponse(true, 202));
    const buttons = widget.root.querySelectorAll('button');
    buttons[3].click(); // rating "4"
    const textarea = widget.root.querySelector('textarea');
    textarea.value = 'great walkthrough';
    buttons[5].click(); // Send

    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(widget.root.querySelector('[role="status"]').textContent).toMatch(/Thanks/);
    expect(textarea.value).toBe('');
    expect(buttons[3].getAttribute('aria-pressed')).toBe('false');
  });

  it('shows a visible failure and leaves the form editable when the POST fails', async () => {
    const { widget } = mountedWidget(() => jsonResponse(false, 429));
    const textarea = widget.root.querySelector('textarea');
    textarea.value = 'please work';
    const sendBtn = widget.root.querySelectorAll('button')[5];
    sendBtn.click();

    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(widget.root.querySelector('[role="status"]').textContent).toMatch(/Could not send/);
    expect(sendBtn.disabled).toBe(false);
    expect(textarea.value).toBe('please work');
  });

  it('destroy() removes the widget from the document', () => {
    const { widget } = mountedWidget(() => jsonResponse(true, 202));
    expect(widget.root.parentNode).not.toBeNull();
    widget.destroy();
    expect(widget.root.parentNode).toBeNull();
  });
});

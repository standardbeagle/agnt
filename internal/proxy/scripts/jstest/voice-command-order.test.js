import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { describe, expect, it } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedVoice = readFileSync(join(scriptsDir, 'voice.js'), 'utf8');

// executeVoiceCommand is a first-match-wins linear scan over commandPatterns,
// so a short pattern that is a plain substring of a longer phrase shadows
// that phrase for every utterance containing the short word. Two instances
// existed: "select" (tool) shadowed "select all" (action), and "text" (tool)
// shadowed "text field"/"input field" (tool: input). This pins that specific,
// multi-word phrases resolve correctly regardless of which single-word
// pattern would otherwise have matched first.
function runtime() {
  const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only' });
  dom.window.eval(shippedVoice);
  return dom.window.__devtool_voice;
}

describe('shipped voice.js command pattern ordering', () => {
  it('"select all" resolves to the selectAll action, not the select tool', () => {
    const voice = runtime();
    expect(voice.executeCommand('select all')).toEqual({ action: 'selectAll' });
  });

  it('bare "select" still resolves to the select tool', () => {
    const voice = runtime();
    expect(voice.executeCommand('select')).toEqual({ tool: 'select' });
  });

  it('"text field" resolves to the input tool, not the text tool', () => {
    const voice = runtime();
    expect(voice.executeCommand('text field')).toEqual({ tool: 'input' });
  });

  it('"input field" resolves to the input tool', () => {
    const voice = runtime();
    expect(voice.executeCommand('input field')).toEqual({ tool: 'input' });
  });

  it('bare "input" still resolves to the input tool', () => {
    const voice = runtime();
    expect(voice.executeCommand('input')).toEqual({ tool: 'input' });
  });

  it('bare "text" still resolves to the text tool', () => {
    const voice = runtime();
    expect(voice.executeCommand('text')).toEqual({ tool: 'text' });
  });

  it('a full sentence containing "text field" still resolves to input, not text', () => {
    const voice = runtime();
    expect(voice.executeCommand('switch to the text field tool')).toEqual({ tool: 'input' });
  });
});

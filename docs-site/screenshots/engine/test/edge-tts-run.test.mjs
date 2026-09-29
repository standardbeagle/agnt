// runEdgeTTS must surface edge-tts's own stderr, status and signal, not a bare throw.
import test from 'node:test';
import assert from 'node:assert/strict';
import {runEdgeTTS} from '../lib/assemble.mjs';

const run = (result) => () => runEdgeTTS('v', '+0%', 'hello world', 'a.mp3', 'a.srt', () => result);

test('success → no throw', () => {
  assert.doesNotThrow(run({status: 0, stderr: ''}));
});

test('nonzero exit → error carries exit code, line and stderr', () => {
  assert.throws(run({status: 1, stderr: '403 Invalid response status\n'}),
    (e) => /exit 1/.test(e.message) && /hello world/.test(e.message) && /403 Invalid response status/.test(e.message));
});

test('killed by signal → error names the signal', () => {
  assert.throws(run({status: null, signal: 'SIGKILL', stderr: ''}), /signal SIGKILL/);
});

test('spawn failure → error says edge-tts could not start', () => {
  assert.throws(run({error: new Error('ENOENT')}), /could not start: ENOENT/);
});

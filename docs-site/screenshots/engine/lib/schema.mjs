// Pure, dependency-free validator for a demo.json spec.
//
//   import {validateDemoSpec} from './lib/schema.mjs';
//   const {ok, errors} = validateDemoSpec(spec);   // errors: string[]
//
// It mirrors the shapes the engine actually consumes (demo.mjs, lib/assemble.mjs)
// so an engine change that breaks a committed demo is caught by `make demo-check`
// instead of by the next hand-run. It NEVER throws on malformed input and NEVER
// touches the filesystem — every problem is collected into `errors` and returned.
// On-disk facts assembly still checks at runtime (a brand.image existing, a take
// being present) are out of scope here; this is a shape check only.

// Keep-range endpoint grammar, mirrored from lib/assemble.mjs endpointSec:
//   "start" | "end" | "mark:<name>[+|-<seconds>]"
const KEEP_ENDPOINT = /^(start|end|mark:[A-Za-z0-9_-]+(?:[+-][0-9.]+)?)$/;
// Narration anchor grammar, mirrored from lib/assemble.mjs anchorSec:
//   "<seg-id>" | "<seg-id>+<seconds>" | "<seg-id>+end"
const NARRATION_ANCHOR = /^([A-Za-z0-9_-]+)(?:\+(?:end|[0-9.]+))?$/;
// Brand overlay positions, mirrored from lib/assemble.mjs BRAND_POS.
const BRAND_POSITIONS = ['top-right', 'top-left', 'bottom-right', 'bottom-left'];
const SEGMENT_TYPES = ['card', 'cli', 'browser'];

const isObject = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);
const isString = (v) => typeof v === 'string';
const isNumber = (v) => typeof v === 'number' && Number.isFinite(v);

export function validateDemoSpec(spec) {
  const errors = [];
  const err = (m) => errors.push(m);

  if (!isObject(spec)) {
    return {ok: false, errors: ['spec must be a JSON object']};
  }

  if (!isString(spec.name) || spec.name.trim() === '') {
    err('name: required non-empty string');
  }
  if (spec.viewport !== undefined) {
    const v = spec.viewport;
    if (!isObject(v) || !isNumber(v.width) || !isNumber(v.height) || v.width <= 0 || v.height <= 0) {
      err('viewport: must be {width>0, height>0}');
    }
  }
  if (spec.fps !== undefined && (!isNumber(spec.fps) || spec.fps <= 0)) {
    err('fps: must be a positive number');
  }
  if (spec.env !== undefined && !isObject(spec.env)) {
    err('env: must be an object');
  }
  if (spec.setup !== undefined) validateSetup(spec.setup, err);

  // Collect segment ids first so narration anchors can be cross-checked even
  // when some segments are otherwise malformed.
  const segIds = new Set();
  if (!Array.isArray(spec.segments) || spec.segments.length === 0) {
    err('segments: required non-empty array');
  } else {
    spec.segments.forEach((seg, i) => {
      if (isObject(seg) && isString(seg.id)) segIds.add(seg.id);
      validateSegment(seg, i, err);
    });
  }

  if (spec.narration !== undefined) validateNarration(spec.narration, segIds, err);
  if (spec.brand !== undefined) validateBrand(spec.brand, err);

  return {ok: errors.length === 0, errors};
}

function validateSetup(setup, err) {
  if (!isObject(setup)) { err('setup: must be an object'); return; }
  if (setup.upstream !== undefined && !isString(setup.upstream)) err('setup.upstream: must be a string');
  if (setup.waitFor !== undefined && !isString(setup.waitFor)) err('setup.waitFor: must be a string URL');
  if (setup.proxy !== undefined) {
    const p = setup.proxy;
    if (!isObject(p)) { err('setup.proxy: must be an object'); return; }
    if (!isString(p.id)) err('setup.proxy.id: required string');
    if (!isString(p.target)) err('setup.proxy.target: required string');
    if (!isNumber(p.port)) err('setup.proxy.port: required number');
  }
}

function validateSegment(seg, i, err) {
  const at = `segments[${i}]`;
  if (!isObject(seg)) { err(`${at}: must be an object`); return; }
  if (!isString(seg.id) || seg.id.trim() === '') err(`${at}.id: required non-empty string`);
  if (!SEGMENT_TYPES.includes(seg.type)) {
    err(`${at} (${seg.id ?? '?'}): unknown segment type ${JSON.stringify(seg.type)} (expected ${SEGMENT_TYPES.join('/')})`);
    return; // without a known type the remaining per-type checks are meaningless
  }
  if (seg.type === 'card') validateCard(seg, at, err);
  else if (seg.type === 'cli') validateCli(seg, at, err);
  else if (seg.type === 'browser') validateBrowser(seg, at, err);
}

function validateCard(seg, at, err) {
  if (!isString(seg.title)) err(`${at}.title: card requires a string title`);
  if (!isString(seg.sub)) err(`${at}.sub: card requires a string sub`);
  if (seg.kicker !== undefined && !isString(seg.kicker)) err(`${at}.kicker: must be a string`);
  if (seg.dur !== undefined && (!isNumber(seg.dur) || seg.dur <= 0)) err(`${at}.dur: must be a positive number`);
}

function validateCli(seg, at, err) {
  if (!Array.isArray(seg.tape) || !seg.tape.every(isString)) {
    err(`${at}.tape: cli requires an array of VHS command strings`);
  }
  if (seg.settings !== undefined && !isObject(seg.settings)) err(`${at}.settings: must be an object`);
  if (seg.trimSeconds !== undefined && !isNumber(seg.trimSeconds)) err(`${at}.trimSeconds: must be a number`);
}

function validateBrowser(seg, at, err) {
  if (!isString(seg.script)) err(`${at}.script: browser requires a string script path`);
  if (seg.raw !== undefined && typeof seg.raw !== 'boolean') err(`${at}.raw: must be a boolean`);
  if (seg.args !== undefined && !isObject(seg.args)) err(`${at}.args: must be an object`);
  if (seg.trimSeconds !== undefined && !isNumber(seg.trimSeconds)) err(`${at}.trimSeconds: must be a number`);
  if (seg.keep !== undefined) validateKeep(seg.keep, at, err);
}

function validateKeep(keep, at, err) {
  if (!Array.isArray(keep)) { err(`${at}.keep: must be an array of [from, to] ranges`); return; }
  keep.forEach((range, j) => {
    const rat = `${at}.keep[${j}]`;
    if (!Array.isArray(range) || range.length !== 2) {
      err(`${rat}: must be a [from, to] pair`);
      return;
    }
    range.forEach((tok, k) => {
      const which = k === 0 ? 'from' : 'to';
      if (!isString(tok) || !KEEP_ENDPOINT.test(tok)) {
        err(`${rat}.${which}: bad keep endpoint ${JSON.stringify(tok)} (expected start|end|mark:<name>[+|-<sec>])`);
      }
    });
  });
}

function validateNarration(narration, segIds, err) {
  if (!isObject(narration)) { err('narration: must be an object'); return; }
  if (!isString(narration.voice)) err('narration.voice: required string');
  if (narration.rate !== undefined && !isString(narration.rate)) err('narration.rate: must be a string');
  if (narration.optional !== undefined && typeof narration.optional !== 'boolean') {
    err('narration.optional: must be a boolean');
  }
  if (narration.segments !== undefined) {
    if (!Array.isArray(narration.segments)) { err('narration.segments: must be an array'); return; }
    narration.segments.forEach((n, i) => {
      const at = `narration.segments[${i}]`;
      if (!isObject(n)) { err(`${at}: must be an object`); return; }
      if (!isString(n.id)) err(`${at}.id: required string`);
      if (!isString(n.text) || n.text.trim() === '') err(`${at}.text: required non-empty string`);
      if (!isString(n.at)) { err(`${at}.at: required anchor string`); return; }
      const m = n.at.match(NARRATION_ANCHOR);
      if (!m) { err(`${at}.at: bad narration anchor ${JSON.stringify(n.at)} (expected <seg-id>[+<sec>|+end])`); return; }
      if (!segIds.has(m[1])) {
        err(`${at}.at: narration anchor ${JSON.stringify(m[1])} references a missing segment id`);
      }
    });
  }
}

function validateBrand(brand, err) {
  if (!isObject(brand)) { err('brand: must be an object'); return; }
  if (!isString(brand.image) || brand.image.trim() === '') err('brand.image: required non-empty string path');
  if (brand.position !== undefined && !BRAND_POSITIONS.includes(brand.position)) {
    err(`brand.position: invalid ${JSON.stringify(brand.position)} (use ${BRAND_POSITIONS.join(', ')})`);
  }
  if (brand.width !== undefined && (!isNumber(brand.width) || brand.width <= 0)) err('brand.width: must be a positive number');
  if (brand.opacity !== undefined && (!isNumber(brand.opacity) || brand.opacity < 0 || brand.opacity > 1)) {
    err('brand.opacity: must be a number in [0, 1]');
  }
}

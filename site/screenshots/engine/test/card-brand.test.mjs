// The title-card wordmark comes from spec.cardBrand so demos of other products
// do not carry agnt's; a spec without one keeps the agnt wordmark unchanged.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {cardHTML} from '../lib/assemble.mjs';
import {validateDemoSpec} from '../lib/schema.mjs';

const view = {width: 1440, height: 900};

test('card without cardBrand keeps the agnt wordmark', () => {
  const html = cardHTML('k', 't', 's', view);
  assert.match(html, /<div class="brand"><b>agnt<\/b><span>·<\/span>dev<\/div>/);
});

test('card with cardBrand renders that wordmark, HTML-escaped', () => {
  const html = cardHTML('k', 't', 's', view, {name: 'mcp-tui', sub: 'standard <beagle>'});
  assert.match(html, /<div class="brand"><b>mcp-tui<\/b><span>·<\/span>standard &lt;beagle&gt;<\/div>/);
  assert.doesNotMatch(html, /<b>agnt<\/b>/);
});

test('cardBrand must name the product', () => {
  const spec = {name: 'd', cardBrand: {sub: 'x'}, segments: [{id: 'c', type: 'card', title: 't', sub: 's'}]};
  const {ok, errors} = validateDemoSpec(spec);
  assert.equal(ok, false);
  assert.ok(errors.some((e) => e.startsWith('cardBrand')), errors.join('\n'));
});

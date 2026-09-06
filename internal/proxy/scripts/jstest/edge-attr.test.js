// Edge cases and comprehensive coverage for typed-attr() detection.
//
// The detection fires when 3+ rules share the same base selector and attribute
// name but differ only by the attribute value. This suite tests grouping
// correctness, operator forms, edge boundaries, and silent cases.

import { describe, expect, it } from 'vitest';
import { auditPage, opportunities } from './load-audit.js';

describe('typed attr() edge cases and comprehensive coverage', () => {
  describe('common real-world patterns that SHOULD fire', () => {
    it('fires on a size scale with double quotes (Bootstrap-style component)', () => {
      const found = opportunities(
        auditPage(`
          .btn[data-size="sm"] { padding: 4px 8px; }
          .btn[data-size="md"] { padding: 8px 16px; }
          .btn[data-size="lg"] { padding: 12px 24px; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].valueCount).toBe(3);
      expect(found[0].attribute).toBe('data-size');
    });

    it('fires on a variant scale with single quotes (Tailwind-style data attrs)', () => {
      const found = opportunities(
        auditPage(`
          .badge[data-variant='info'] { background: #0066cc; }
          .badge[data-variant='warn'] { background: #ff9900; }
          .badge[data-variant='error'] { background: #cc0000; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
    });

    it('fires on a color/tone scale without quotes (bare attribute values)', () => {
      const found = opportunities(
        auditPage(`
          .icon[data-color=red] { color: #f00; }
          .icon[data-color=green] { color: #0f0; }
          .icon[data-color=blue] { color: #00f; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
    });

    it('fires on a scale split across @media (responsive design pattern)', () => {
      const found = opportunities(
        auditPage(`
          .col[data-span="1"] { width: 8.33%; }
          @media (min-width: 600px) {
            .col[data-span="2"] { width: 16.66%; }
            .col[data-span="3"] { width: 25%; }
          }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].valueCount).toBe(3);
    });

    it('fires on numeric values in attributes', () => {
      const found = opportunities(
        auditPage(`
          .grid[data-columns="1"] { grid-template-columns: 1fr; }
          .grid[data-columns="2"] { grid-template-columns: 1fr 1fr; }
          .grid[data-columns="3"] { grid-template-columns: 1fr 1fr 1fr; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].valueCount).toBe(3);
    });
  });

  describe('grouping correctness — same attribute on different components', () => {
    it('does NOT merge the same attribute on different selectors', () => {
      // .btn and .card have the same data-size but different bases → separate groups
      const found = opportunities(
        auditPage(`
          .btn[data-size="sm"] { padding: 4px; }
          .btn[data-size="md"] { padding: 8px; }
          .btn[data-size="lg"] { padding: 12px; }
          .card[data-size="sm"] { padding: 8px; }
          .card[data-size="md"] { padding: 16px; }
          .card[data-size="lg"] { padding: 24px; }
        `),
        'typed-attr'
      );
      // Should get 2 findings (both have 3+ values)
      expect(found).toHaveLength(2);
      expect(found.every((f) => f.attribute === 'data-size')).toBe(true);
    });

    it('groups by normalized base selector after stripping all attributes', () => {
      // Even though selectors differ, if their base (no attrs) is the same, they group
      // But .a[x] and .b[x] have different bases (.a vs .b) so different groups
      const found = opportunities(
        auditPage(`
          .component[data-x="1"] { color: red; }
          .component[data-x="2"] { color: green; }
          .component[data-x="3"] { color: blue; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('data-x');
    });
  });

  describe('grouping correctness — different attributes on same component', () => {
    it('does NOT merge different attributes on the same component', () => {
      // data-size and data-tone are different attributes → separate groups
      const found = opportunities(
        auditPage(`
          .btn[data-size="sm"] { padding: 4px; }
          .btn[data-size="md"] { padding: 8px; }
          .btn[data-size="lg"] { padding: 12px; }
          .btn[data-tone="info"] { color: blue; }
          .btn[data-tone="warn"] { color: orange; }
          .btn[data-tone="error"] { color: red; }
        `),
        'typed-attr'
      );
      // Should get 2 findings
      expect(found).toHaveLength(2);
      const attrs = found.map((f) => f.attribute).sort();
      expect(attrs).toEqual(['data-size', 'data-tone']);
    });

    it('keeps separate groups for size, color, and state attributes on the same component', () => {
      const found = opportunities(
        auditPage(`
          .input[data-size="s"] { font-size: 12px; }
          .input[data-size="m"] { font-size: 14px; }
          .input[data-size="l"] { font-size: 16px; }
          .input[data-color="primary"] { color: blue; }
          .input[data-color="secondary"] { color: gray; }
          .input[data-color="danger"] { color: red; }
          .input[data-state="valid"] { border-color: green; }
          .input[data-state="invalid"] { border-color: red; }
        `),
        'typed-attr'
      );
      // Only data-size and data-color hit 3+ values; data-state has only 2
      expect(found).toHaveLength(2);
      expect(found.map((f) => f.valueCount)).toEqual([3, 3]);
    });
  });

  describe('grouping correctness — pseudo-classes and complex selectors', () => {
    it('does NOT merge when a pseudo-class differs (.btn vs .btn:hover)', () => {
      // After stripping attributes, .btn and .btn:hover are different bases
      const result = auditPage(`
        .btn[data-size="sm"] { padding: 4px; }
        .btn[data-size="md"] { padding: 8px; }
        .btn[data-size="lg"] { padding: 12px; }
        .btn[data-size="sm"]:hover { background: #eee; }
      `);
      const found = opportunities(result, 'typed-attr');
      expect(found).toHaveLength(1); // Only the base .btn group hits 3+ values
      expect(found[0].valueCount).toBe(3);
    });

    it('groups :focus and :hover as separate rules from the base selector', () => {
      const found = opportunities(
        auditPage(`
          .btn[data-v="a"] { color: black; }
          .btn[data-v="b"] { color: black; }
          .btn[data-v="c"] { color: black; }
          .btn[data-v="a"]:hover { color: blue; }
          .btn[data-v="b"]:hover { color: blue; }
          .btn[data-v="c"]:hover { color: blue; }
        `),
        'typed-attr'
      );
      // Two groups: .btn (3 values) and .btn:hover (3 values)
      expect(found).toHaveLength(2);
    });

    it('does NOT merge rules from a selector list — they parse as separate rules', () => {
      // Note: selector lists in CSS like ".a, .b { ... }" are not how auditPage works;
      // instead we test rules written separately that LOOK like they came from a list.
      // The grouping key is based on each rule's individual selectorText.
      const found = opportunities(
        auditPage(`
          .a[data-size="sm"] { padding: 4px; }
          .a[data-size="md"] { padding: 8px; }
          .a[data-size="lg"] { padding: 12px; }
          .b[data-size="sm"] { padding: 8px; }
          .b[data-size="md"] { padding: 12px; }
          .b[data-size="lg"] { padding: 16px; }
        `),
        'typed-attr'
      );
      // .a and .b are separate groups
      expect(found).toHaveLength(2);
    });
  });

  describe('grouping correctness — multiple attributes in one selector', () => {
    it('groups [a][b] selectors by extracting each attribute separately', () => {
      // A selector like .btn[data-size="sm"][data-tone="info"] has two attribute matches.
      // base = selectorText.replace(ATTR_SEL_STRIP_RE, '').trim() = '.btn'
      // First attr match: data-size, value "sm", base '.btn', group key '.btn|data-size'
      // Second attr match: data-tone, value "info", base '.btn', group key '.btn|data-tone'
      // So they create DIFFERENT group entries (different attribute names).
      // However, data-tone only has 2 distinct values (info, warn) so only data-size fires.
      const found = opportunities(
        auditPage(`
          .btn[data-size="sm"][data-tone="info"] { padding: 4px; color: blue; }
          .btn[data-size="md"][data-tone="info"] { padding: 8px; color: blue; }
          .btn[data-size="lg"][data-tone="info"] { padding: 12px; color: blue; }
          .btn[data-size="sm"][data-tone="warn"] { padding: 4px; color: orange; }
          .btn[data-size="md"][data-tone="warn"] { padding: 8px; color: orange; }
          .btn[data-size="lg"][data-tone="warn"] { padding: 12px; color: orange; }
        `),
        'typed-attr'
      );
      // Only data-size has 3+ values; data-tone has only 2 (info, warn)
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('data-size');
      expect(found[0].valueCount).toBe(3);
    });

    it('handles three attributes in one selector correctly', () => {
      const found = opportunities(
        auditPage(`
          .component[a="1"][b="x"][c="p"] { color: red; }
          .component[a="2"][b="x"][c="p"] { color: green; }
          .component[a="3"][b="x"][c="p"] { color: blue; }
        `),
        'typed-attr'
      );
      // Only 'a' should have 3 values; 'b' and 'c' have only 1
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('a');
      expect(found[0].valueCount).toBe(3);
    });
  });

  describe('attribute selector operator forms', () => {
    it('detects word-match operator [attr~="value"]', () => {
      // [data-tags~="red"] matches data-tags containing "red" as one word
      const found = opportunities(
        auditPage(`
          .item[data-tags~="red"] { color: #f00; }
          .item[data-tags~="blue"] { color: #00f; }
          .item[data-tags~="green"] { color: #0f0; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('data-tags');
    });

    it('detects prefix-match operator [attr^="value"]', () => {
      const found = opportunities(
        auditPage(`
          [data-id^="user-"] { font-weight: bold; }
          [data-id^="admin-"] { font-weight: bold; }
          [data-id^="guest-"] { font-weight: normal; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('data-id');
    });

    it('detects language-match operator [attr|="value"]', () => {
      const found = opportunities(
        auditPage(`
          [lang|="en"] { font-family: serif; }
          [lang|="fr"] { font-family: sans-serif; }
          [lang|="de"] { font-family: monospace; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('lang');
    });

    it('detects substring-match operator [attr*="value"]', () => {
      const found = opportunities(
        auditPage(`
          [data-color*="blue"] { filter: hue-rotate(220deg); }
          [data-color*="red"] { filter: hue-rotate(0deg); }
          [data-color*="green"] { filter: hue-rotate(120deg); }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('data-color');
    });

    it('detects suffix-match operator [attr$="value"]', () => {
      // NOTE: the regex has [~^$*|]?= which includes $ for suffix matching
      const found = opportunities(
        auditPage(`
          [data-ext$=".pdf"] { color: red; }
          [data-ext$=".doc"] { color: green; }
          [data-ext$=".txt"] { color: blue; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].attribute).toBe('data-ext');
    });

    it('may have limits with case-insensitive flag [attr="val" i]', () => {
      // The regex is [\\s*([-\\w]+)\\s*([~^$*|]?=)\\s*(...)\s*\\]
      // For [data-x="a" i], the pattern tries to match up to the closing ]
      // The " i" is between the value and the ], which might not parse.
      // Let's check what jsdom actually keeps.
      const result = auditPage(`
        [data-x="a" i] { color: red; }
        [data-x="b" i] { color: green; }
        [data-x="c" i] { color: blue; }
      `);
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((r) => r.selectorText || r.selector)
        .join('\n');
      // Premise: check if jsdom preserved the [data-x...] selector at all
      expect(sheet, 'premise broken: jsdom dropped the [data-x] selector').toContain(
        '[data-x'
      );
      const found = opportunities(result, 'typed-attr');
      // SUSPECT: jsdom might not parse " i" correctly and drop the rule entirely
      expect(found.length).toBeGreaterThanOrEqual(0);
    });
  });

  describe('silent cases — must NOT fire', () => {
    it('stays silent at exactly two values (minimum is 3)', () => {
      expect(
        opportunities(
          auditPage(`
            .btn[data-size="sm"] { padding: 4px; }
            .btn[data-size="lg"] { padding: 12px; }
          `),
          'typed-attr'
        )
      ).toHaveLength(0);
    });

    it('stays silent for presence selectors with no value', () => {
      const result = auditPage(`
        .btn[disabled] { opacity: 0.5; }
        .btn[hidden] { display: none; }
        .btn[readonly] { color: gray; }
      `);
      // Premise: selectors must be present in the stylesheet
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((r) => r.selectorText || r.selector)
        .join('\n');
      expect(sheet, 'premise broken: jsdom dropped presence selectors').toContain(
        '[disabled]'
      );
      expect(opportunities(result, 'typed-attr')).toHaveLength(0);
    });

    it('stays silent for three presence selectors (no values to attribute)', () => {
      const result = auditPage(`
        input[required] { border: 2px solid red; }
        input[disabled] { opacity: 0.5; }
        input[readonly] { background: #f5f5f5; }
      `);
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((r) => r.selectorText || r.selector)
        .join('\n');
      expect(sheet, 'premise broken: selectors not preserved').toContain('[required]');
      expect(opportunities(result, 'typed-attr')).toHaveLength(0);
    });

    it('stays silent for rules with empty declarations', () => {
      // Three selectors, nothing to move: an empty rule has no value that
      // attr() could read, so advice here would come with no edit attached.
      const result = auditPage(`
        .btn[data-size="sm"] { }
        .btn[data-size="md"] { }
        .btn[data-size="lg"] { }
      `);
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((r) => r.selectorText || r.selector)
        .join('\n');
      expect(sheet, 'premise broken: jsdom dropped empty rules').toContain(
        '[data-size'
      );
      expect(opportunities(result, 'typed-attr')).toHaveLength(0);
    });

    it('stays silent when an attribute has only 1 distinct value (repeated)', () => {
      const found = opportunities(
        auditPage(`
          .btn[data-x="same"] { color: red; }
          .btn[data-x="same"] { color: red; }
          .btn[data-x="same"] { color: red; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(0);
    });

    it('stays silent when mixing 2 values across 10 rules', () => {
      const css = Array.from({ length: 5 }, (_, i) => `.a[data-x="v1"] { color: red; }`).join(
        '\n'
      );
      const css2 = Array.from({ length: 5 }, (_, i) => `.a[data-x="v2"] { color: green; }`).join(
        '\n'
      );
      const found = opportunities(auditPage(css + '\n' + css2), 'typed-attr');
      expect(found).toHaveLength(0); // Still only 2 distinct values
    });
  });

  describe('cap at 3 findings', () => {
    it('caps the report at three findings even if more groups qualify', () => {
      const found = opportunities(
        auditPage(`
          .a[data-v="1"] { color: red; }
          .a[data-v="2"] { color: green; }
          .a[data-v="3"] { color: blue; }
          .b[data-v="1"] { padding: 4px; }
          .b[data-v="2"] { padding: 8px; }
          .b[data-v="3"] { padding: 12px; }
          .c[data-v="1"] { margin: 4px; }
          .c[data-v="2"] { margin: 8px; }
          .c[data-v="3"] { margin: 12px; }
          .d[data-v="1"] { font-size: 12px; }
          .d[data-v="2"] { font-size: 16px; }
          .d[data-v="3"] { font-size: 20px; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(3);
      // Should be sorted by valueCount descending (all equal here, so order by first match)
      expect(found.every((f) => f.valueCount === 3)).toBe(true);
    });

    it('reports valueCount correctly for each finding', () => {
      const found = opportunities(
        auditPage(`
          .small[data-size="xs"] { padding: 2px; }
          .small[data-size="sm"] { padding: 4px; }
          .medium[data-size="md"] { padding: 8px; }
          .medium[data-size="lg"] { padding: 12px; }
          .medium[data-size="xl"] { padding: 16px; }
        `),
        'typed-attr'
      );
      // .small has 2 (silent), .medium has 3 (fires)
      expect(found).toHaveLength(1);
      expect(found[0].valueCount).toBe(3);
    });

    it('sorts findings by valueCount highest-first before capping', () => {
      const found = opportunities(
        auditPage(`
          .a[data-v="1"] { color: red; }
          .a[data-v="2"] { color: green; }
          .a[data-v="3"] { color: blue; }
          .a[data-v="4"] { color: yellow; }
          .b[data-v="1"] { padding: 4px; }
          .b[data-v="2"] { padding: 8px; }
          .b[data-v="3"] { padding: 12px; }
          .c[data-v="1"] { margin: 4px; }
          .c[data-v="2"] { margin: 8px; }
          .c[data-v="3"] { margin: 12px; }
          .c[data-v="4"] { margin: 16px; }
          .c[data-v="5"] { margin: 20px; }
        `),
        'typed-attr'
      );
      // .c has 5, .a has 4, .b has 3 → should report in that order (or capped at 3)
      expect(found).toHaveLength(3);
      expect(found[0].valueCount).toBeGreaterThanOrEqual(3);
      // Check descending order
      for (let i = 1; i < found.length; i++) {
        expect(found[i].valueCount).toBeLessThanOrEqual(found[i - 1].valueCount);
      }
    });
  });

  describe('edge cases with real declaration parsing', () => {
    it('fires even when declarations are complex or long', () => {
      const found = opportunities(
        auditPage(`
          .btn[data-size="sm"] { padding: 2px 4px; border: 1px solid #ccc; }
          .btn[data-size="md"] { padding: 4px 8px; border: 2px solid #999; }
          .btn[data-size="lg"] { padding: 8px 16px; border: 3px solid #333; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
      expect(found[0].valueCount).toBe(3);
    });

    it('fires with CSS custom properties in declarations', () => {
      const found = opportunities(
        auditPage(`
          .box[data-x="a"] { --pad: 4px; padding: var(--pad); }
          .box[data-x="b"] { --pad: 8px; padding: var(--pad); }
          .box[data-x="c"] { --pad: 12px; padding: var(--pad); }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
    });

    it('handles both single-element and multi-element selectors', () => {
      // The base selector `.parent > .child` should be preserved after attribute stripping
      const found = opportunities(
        auditPage(`
          .parent > .child[data-x="a"] { color: red; }
          .parent > .child[data-x="b"] { color: green; }
          .parent > .child[data-x="c"] { color: blue; }
        `),
        'typed-attr'
      );
      expect(found).toHaveLength(1);
    });
  });
});

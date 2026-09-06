// DOM tests for the modern-CSS opportunity scan in audit-css.js.
//
// Every case renders a real stylesheet into a real document and runs the
// shipped auditCSS over it. Each detection is paired with the near-miss that
// must stay silent — a scan that fires on everything is as useless as one that
// fires on nothing, and only the negative case can tell the two apart.

import { describe, expect, it } from 'vitest';
import { auditPage, opportunities, types } from './load-audit.js';

describe('alpha() relative color', () => {
  it('fires when one base color is written once per opacity level', () => {
    const found = opportunities(
      auditPage(`
        .card { color: #0b5fff; }
        .card--muted { color: rgba(11, 95, 255, 0.4); }
      `),
      'alpha-shorthand'
    );
    expect(found).toHaveLength(1);
    expect(found[0].opacityLevels).toBe(2);
    expect(found[0].fix).toContain('alpha(');
  });

  it('collapses hex and rgb() spellings of the same base color', () => {
    // #0b5fff and rgb(11 95 255) are the same color written two ways; the
    // translucent sibling has to attach to that one base, not to a third.
    const found = opportunities(
      auditPage(`
        .a { color: #0b5fff; }
        .b { border-color: rgb(11 95 255); }
        .c { background: rgba(11, 95, 255, 0.6); }
      `),
      'alpha-shorthand'
    );
    expect(found).toHaveLength(1);
  });

  it('fires on two translucent levels even with no opaque form', () => {
    const found = opportunities(
      auditPage(`
        .a { color: rgba(11, 95, 255, 0.4); }
        .b { color: rgba(11, 95, 255, 0.8); }
      `),
      'alpha-shorthand'
    );
    expect(found).toHaveLength(1);
    expect(found[0].opacityLevels).toBe(2);
  });

  it('reads inline styles too, not only stylesheets', () => {
    const found = opportunities(
      auditPage(
        '.muted { color: rgba(11, 95, 255, 0.4); }',
        '<div style="color: #0b5fff">brand</div>'
      ),
      'alpha-shorthand'
    );
    expect(found).toHaveLength(1);
  });

  it('stays silent for a color used at one opacity', () => {
    expect(
      opportunities(
        auditPage(`
          .a { color: #0b5fff; }
          .b { background: #0b5fff; }
          .c { color: rgba(255, 0, 0, 0.5); }
        `),
        'alpha-shorthand'
      )
    ).toHaveLength(0);
  });

  it('caps the report at three colors', () => {
    const css = ['aa0000', '00bb00', '0000cc', 'dddd00', '00eeee']
      .map((hex, i) => `.o${i} { color: #${hex}; } .t${i} { color: #${hex}80; }`)
      .join('\n');
    expect(opportunities(auditPage(css), 'alpha-shorthand')).toHaveLength(3);
  });
});

describe('progress()', () => {
  it('fires on a ratio-of-differences in calc()', () => {
    const found = opportunities(
      auditPage('.meter { opacity: calc((var(--v) - var(--min)) / (var(--max) - var(--min))); }'),
      'progress-function'
    );
    expect(found).toHaveLength(1);
    expect(found[0].selector).toBe('.meter');
    expect(found[0].fix).toContain('progress(');
  });

  it('stays silent on division by a plain number', () => {
    // calc((100% - 2 * 10px) / 3) is column math, not normalisation: the
    // divisor is not a second difference.
    expect(
      opportunities(
        auditPage('.col { width: calc((100% - 2 * 10px) / 3); }'),
        'progress-function'
      )
    ).toHaveLength(0);
  });

  it('stays silent when the divisor is not itself a difference', () => {
    // Two subtractions but a plain divisor: gutter math, not normalisation.
    // This is the case that isolates the parenthesised-divisor requirement —
    // the single-subtraction case below is rejected before reaching it.
    expect(
      opportunities(
        auditPage('.grid { width: calc((100% - var(--gutter)) / 3 - 4px); }'),
        'progress-function'
      )
    ).toHaveLength(0);
  });

  it('stays silent on a single subtraction', () => {
    expect(
      opportunities(
        auditPage('.box { width: calc(100% - var(--gutter)); }'),
        'progress-function'
      )
    ).toHaveLength(0);
  });

  it('never sees arithmetic the engine already folded away', () => {
    // A documented limit, not a bug: an engine simplifies purely numeric
    // calc() before any audit can read it — this one comes back as
    // `calc(0.333… * (100% - 20px))`, so the authored shape is gone. The ratio
    // survives only when a var() defers substitution, which is also the only
    // shape where progress() buys anything.
    const result = auditPage('.col { width: calc((100% - 20px) / 3); }');
    const stored = document.styleSheets[0].cssRules[0].style.getPropertyValue('width');
    expect(stored, 'premise broken: the engine kept the authored calc()').not.toBe(
      'calc((100% - 20px) / 3)'
    );
    expect(opportunities(result, 'progress-function')).toHaveLength(0);
  });
});

describe('typed attr()', () => {
  it('fires when three rules differ only by the attribute value', () => {
    const found = opportunities(
      auditPage(`
        .btn[data-size="sm"] { padding: 4px; }
        .btn[data-size="md"] { padding: 8px; }
        .btn[data-size="lg"] { padding: 12px; }
      `),
      'typed-attr'
    );
    expect(found).toHaveLength(1);
    expect(found[0].attribute).toBe('data-size');
    expect(found[0].valueCount).toBe(3);
    expect(found[0].fix).toContain('attr(data-size');
  });

  it('stays silent at two values — two rules are not a pattern', () => {
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

  it('stays silent for presence selectors, which carry no value to pass in', () => {
    expect(
      opportunities(
        auditPage(`
          .btn[disabled] { opacity: 0.5; }
          .btn[hidden] { display: none; }
          .btn[readonly] { color: gray; }
        `),
        'typed-attr'
      )
    ).toHaveLength(0);
  });
});

describe('sibling-index() / sibling-count()', () => {
  it('fires on three rules that differ only by child index', () => {
    const found = opportunities(
      auditPage(`
        .ring li:nth-child(1) { transform: rotate(0deg); }
        .ring li:nth-child(2) { transform: rotate(120deg); }
        .ring li:nth-child(3) { transform: rotate(240deg); }
      `),
      'sibling-index'
    );
    expect(found).toHaveLength(1);
    expect(found[0].indexCount).toBe(3);
    expect(found[0].fix).toContain('sibling-index()');
  });

  it('fires for a rule set nested in @media', () => {
    // The walk has to descend: position-per-rule sets routinely live inside a
    // breakpoint.
    const found = opportunities(
      auditPage(`
        @media (min-width: 600px) {
          .ring li:nth-child(1) { transform: rotate(0deg); }
          .ring li:nth-child(2) { transform: rotate(120deg); }
          .ring li:nth-child(3) { transform: rotate(240deg); }
        }
      `),
      'sibling-index'
    );
    expect(found).toHaveLength(1);
  });

  it('stays silent at two indexes', () => {
    expect(
      opportunities(
        auditPage(`
          .pair li:nth-child(1) { margin-left: 0; }
          .pair li:nth-child(2) { margin-left: 8px; }
        `),
        'sibling-index'
      )
    ).toHaveLength(0);
  });

  it('stays silent for nth-child formulas, which already cover any count', () => {
    expect(
      opportunities(
        auditPage(`
          .zebra li:nth-child(2n+1) { background: #eee; }
          .zebra li:nth-child(2n) { background: #fff; }
          .zebra li:nth-child(odd) { color: #111; }
        `),
        'sibling-index'
      )
    ).toHaveLength(0);
  });
});

describe('text-box-trim / text-box-edge', () => {
  it('fires on a fixed line-height with lopsided vertical padding', () => {
    const found = opportunities(
      auditPage('.chip { line-height: 1.2; padding-top: 10px; padding-bottom: 8px; }'),
      'text-box-trim'
    );
    expect(found).toHaveLength(1);
    expect(found[0].paddingTop).toBe('10px');
    expect(found[0].paddingBottom).toBe('8px');
    expect(found[0].fix).toContain('text-box-trim');
  });

  it('stays silent when the vertical padding is already symmetric', () => {
    expect(
      opportunities(
        auditPage('.chip { line-height: 1.2; padding-top: 8px; padding-bottom: 8px; }'),
        'text-box-trim'
      )
    ).toHaveLength(0);
  });

  it('stays silent without an explicit line-height', () => {
    // Asymmetric padding on its own is a layout choice, not half-leading
    // compensation.
    expect(
      opportunities(
        auditPage('.banner { padding-top: 24px; padding-bottom: 8px; }'),
        'text-box-trim'
      )
    ).toHaveLength(0);
  });
});

describe('max-content-sizing: shrink-to-fit', () => {
  it('fires when an element is sized to its own content', () => {
    const found = opportunities(auditPage('.tag { width: fit-content; }'), 'shrink-to-fit');
    expect(found).toHaveLength(1);
    expect(found[0].fix).toContain('shrink-to-fit');
  });

  it('stays silent for ordinary widths', () => {
    expect(
      opportunities(auditPage('.tag { width: 100%; }'), 'shrink-to-fit')
    ).toHaveLength(0);
  });
});

describe('advisory contract', () => {
  const ADVISORY_PAGE = `
    .card { color: #0b5fff; }
    .card--muted { color: rgba(11, 95, 255, 0.4); }
    .meter { opacity: calc((var(--v) - var(--min)) / (var(--max) - var(--min))); }
    .btn[data-size="sm"] { padding: 4px; }
    .btn[data-size="md"] { padding: 8px; }
    .btn[data-size="lg"] { padding: 12px; }
    .ring li:nth-child(1) { transform: rotate(0deg); }
    .ring li:nth-child(2) { transform: rotate(120deg); }
    .ring li:nth-child(3) { transform: rotate(240deg); }
    .chip { line-height: 1.2; padding-top: 10px; padding-bottom: 8px; }
    .tag { width: fit-content; }
  `;

  it('detects all six shapes on one page', () => {
    expect(new Set(types(auditPage(ADVISORY_PAGE)))).toEqual(
      new Set([
        'alpha-shorthand',
        'progress-function',
        'typed-attr',
        'sibling-index',
        'text-box-trim',
        'shrink-to-fit'
      ])
    );
  });

  it('never marks a page down for an opportunity', () => {
    // This page has no inline styles, no !important and no z-index, so its
    // ONLY findings are advisory. Wire them into the score and this fails.
    const result = auditPage(ADVISORY_PAGE);
    expect(result.modernCSS.opportunities.length).toBeGreaterThan(0);
    expect(result.score).toBe(100);
    expect(result.grade).toBe('A');
  });

  it('carries the support reality on every finding', () => {
    // Without these an adopter cannot tell an interoperable primitive from one
    // an engine silently drops.
    for (const finding of auditPage(ADVISORY_PAGE).modernCSS.opportunities) {
      expect(finding.severity).toBe('info');
      expect(finding.advisory).toBe(true);
      expect(finding.feature).toBeTruthy();
      expect(finding.baseline).toBeTruthy();
      expect(finding.fallback).toBeTruthy();
      expect(finding.fix).toBeTruthy();
    }
  });

  it('registers selector-bearing findings for highlighting', () => {
    const result = auditPage(ADVISORY_PAGE);
    const registry = window.__devtool.audit.findingSelectors;
    for (const finding of result.modernCSS.opportunities) {
      if (finding.selector) {
        expect(registry[finding.id]).toBe(finding.selector);
      }
    }
  });

  it('finds nothing on a page that already uses the primitives', () => {
    const result = auditPage(`
      .card { color: #0b5fff; }
      .card--muted { color: alpha(var(--brand) / 40%); }
      .meter { opacity: progress(var(--v), var(--min), var(--max)); }
      .btn { --pad: attr(data-size type(<length>), 8px); }
      .ring li { transform: rotate(calc(360deg * sibling-index() / sibling-count())); }
      .chip { line-height: 1.2; padding-top: 8px; padding-bottom: 8px; text-box-trim: trim-both; }
    `);

    // Premise: jsdom's CSS parser must have KEPT these values. It drops some
    // declarations it cannot parse (which is why the attr() case is written as
    // a custom property), and a dropped rule would make this test pass for the
    // wrong reason — nothing to audit rather than nothing to report.
    const sheet = Array.from(document.styleSheets[0].cssRules)
      .map((rule) => rule.cssText)
      .join('\n');
    for (const primitive of [
      'alpha(',
      'progress(',
      'attr(data-size',
      'sibling-index()',
      'text-box-trim'
    ]) {
      expect(sheet, `premise broken: the parser dropped ${primitive}`).toContain(primitive);
    }

    expect(result.modernCSS.opportunities).toHaveLength(0);
  });
});

describe('coverage honesty', () => {
  it('reports truncation instead of silently under-reporting', () => {
    // One rule past the 3000-rule cap; the audit must say the walk stopped.
    const css = Array.from({ length: 3200 }, (_, i) => `.r${i} { color: #111; }`).join('\n');
    const result = auditPage(css);
    expect(result.note).toContain('modern-CSS scan stopped');
  });
});

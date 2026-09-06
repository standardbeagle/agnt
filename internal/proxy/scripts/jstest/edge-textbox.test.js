// Tests for text-box-trim detection edge cases and CSS spelling variants.
//
// The detection fires when a rule has:
// 1. A line-height that is not 'normal' and not empty
// 2. Both padding-top and padding-bottom present
// 3. padding-top !== padding-bottom
//
// Max 3 findings per document (MODERN_FINDING_CAP).

import { describe, expect, it } from 'vitest';
import { auditPage, opportunities } from './load-audit.js';

describe('text-box-trim common real-world shapes', () => {
  it('button with asymmetric padding shorthand (10px 16px 8px)', () => {
    // Real code: MUI Button with line-height: 1.5
    const result = auditPage(`
      button { line-height: 1.5; padding: 10px 16px 8px; }
    `);
    const found = opportunities(result, 'text-box-trim');

    // Premise: jsdom expands shorthand padding into longhands
    const style = document.styleSheets[0].cssRules[0].style;
    expect(style.getPropertyValue('padding-top'), 'premise: top from shorthand').toBe('10px');
    expect(style.getPropertyValue('padding-bottom'), 'premise: bottom from shorthand').toBe('8px');

    expect(found).toHaveLength(1);
    expect(found[0].selector).toBe('button');
    expect(found[0].paddingTop).toBe('10px');
    expect(found[0].paddingBottom).toBe('8px');
  });

  it('chip with separate padding-top / padding-bottom longhands', () => {
    // Real code: Material Design chip
    const result = auditPage(`
      .chip { line-height: 1.25; padding-top: 6px; padding-bottom: 4px; }
    `);
    const found = opportunities(result, 'text-box-trim');

    expect(found).toHaveLength(1);
    expect(found[0].paddingTop).toBe('6px');
    expect(found[0].paddingBottom).toBe('4px');
  });

  it('label with unitless line-height and lopsided padding in rem', () => {
    // Real code: form label with unitless multiplier
    const result = auditPage(`
      label { line-height: 1.4; padding-top: 0.5rem; padding-bottom: 0.25rem; }
    `);
    const found = opportunities(result, 'text-box-trim');

    expect(found).toHaveLength(1);
    expect(found[0].paddingTop).toBe('0.5rem');
    expect(found[0].paddingBottom).toBe('0.25rem');
  });
});

describe('shorthand and logical property spellings', () => {
  it('shorthand padding asymmetric (10px 16px 8px) fires', () => {
    // One shorthand value; top and bottom differ.
    const result = auditPage(`
      .btn { line-height: 1.5; padding: 10px 16px 8px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(1);
  });

  it('shorthand padding symmetric (10px 16px) stays silent', () => {
    // Top and bottom are both 10px in a 2-value shorthand.
    const result = auditPage(`
      .btn { line-height: 1.5; padding: 10px 16px; }
    `);

    const style = document.styleSheets[0].cssRules[0].style;
    expect(style.getPropertyValue('padding-top'), 'premise: symmetric shorthand').toBe('10px');
    expect(style.getPropertyValue('padding-bottom'), 'premise: symmetric shorthand').toBe('10px');

    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('padding-block shorthand (10px 8px) is read as the vertical pair', () => {
    // The declaration is NOT dropped — it is kept under its own name. What it
    // does not do is populate padding-top/padding-bottom, because physical and
    // logical properties are separate entries in the CSSOM. A browser behaves
    // the same way, so reading only the physical pair missed these entirely.
    const result = auditPage(`
      .btn { line-height: 1.5; padding-block: 10px 8px; }
    `);

    const style = document.styleSheets[0].cssRules[0].style;
    expect(style.getPropertyValue('padding-block'), 'premise broken: parser dropped padding-block').toBe('10px 8px');
    expect(style.getPropertyValue('padding-top')).toBe('');

    const found = opportunities(result, 'text-box-trim');
    expect(found).toHaveLength(1);
    expect(found[0].paddingTop).toBe('10px');
    expect(found[0].paddingBottom).toBe('8px');
    expect(found[0].spelling).toBe('padding-block');
  });

  it('padding-block with one value is symmetric and stays silent', () => {
    const result = auditPage('.btn { line-height: 1.5; padding-block: 10px; }');
    const style = document.styleSheets[0].cssRules[0].style;
    expect(style.getPropertyValue('padding-block'), 'premise broken: parser dropped padding-block').toBe('10px');
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('padding-block-start / padding-block-end longhands are read as the vertical pair', () => {
    // Kept under their own names, again without touching padding-top/bottom.
    const result = auditPage(`
      .btn { line-height: 1.5; padding-block-start: 10px; padding-block-end: 8px; }
    `);

    const style = document.styleSheets[0].cssRules[0].style;
    expect(style.getPropertyValue('padding-block-start'), 'premise broken: parser dropped the longhand').toBe('10px');
    expect(style.getPropertyValue('padding-top')).toBe('');

    const found = opportunities(result, 'text-box-trim');
    expect(found).toHaveLength(1);
    expect(found[0].spelling).toBe('padding-block-start/padding-block-end');
  });

  it('line-height inside font shorthand (font: 500 14px/1.2 system-ui) — jsdom DOES expand this', () => {
    // jsdom correctly expands the font shorthand to extract line-height.
    const result = auditPage(`
      .btn { font: 500 14px/1.2 system-ui; padding-top: 10px; padding-bottom: 8px; }
    `);

    const style = document.styleSheets[0].cssRules[0].style;
    const lineHeight = style.getPropertyValue('line-height');

    expect(lineHeight, 'jsdom expanded font shorthand line-height').toBe('1.2');
    expect(opportunities(result, 'text-box-trim')).toHaveLength(1);
  });
});

describe('unit forms and normalization', () => {
  it('px values differ (10px vs 8px)', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 10px; padding-bottom: 8px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(1);
  });

  it('rem values differ (0.5rem vs 0.25rem)', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 0.5rem; padding-bottom: 0.25rem; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(1);
  });

  it('em values differ (0.625em vs 0.5em)', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 0.625em; padding-bottom: 0.5em; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(1);
  });

  it('unitless (percentage) values differ (20% vs 10%)', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 20%; padding-bottom: 10%; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(1);
  });

  it('zero written as 0 vs 0px — does jsdom normalize these as equal?', () => {
    // If one is 0 and one is 0px, are they considered equal?
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 0; padding-bottom: 0px; }
    `);

    const style = document.styleSheets[0].cssRules[0].style;
    const top = style.getPropertyValue('padding-top');
    const bottom = style.getPropertyValue('padding-bottom');

    expect(top, 'premise: top after parse').toBe(bottom);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('8px vs 0.5rem: different units kept as-is by jsdom, fires despite same computed value', () => {
    // jsdom keeps units as written; 8px and 0.5rem are NOT normalized to computed values.
    // If 0.5rem computes to 8px in jsdom's default font size (16px), the detection
    // still sees them as different strings and FIRES.
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 8px; padding-bottom: 0.5rem; }
    `);

    const style = document.styleSheets[0].cssRules[0].style;
    const top = style.getPropertyValue('padding-top');
    const bottom = style.getPropertyValue('padding-bottom');

    // SUSPECT: jsdom keeps them as different strings, not normalized to computed px
    expect(top, 'jsdom keeps top as-is').toBe('8px');
    expect(bottom, 'jsdom keeps bottom as-is').toBe('0.5rem');
    expect(top).not.toBe(bottom);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(1);
  });
});

describe('cases that must stay silent', () => {
  it('line-height: normal stays silent', () => {
    const result = auditPage(`
      .btn { line-height: normal; padding-top: 10px; padding-bottom: 8px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('no line-height declaration stays silent', () => {
    const result = auditPage(`
      .btn { padding-top: 10px; padding-bottom: 8px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('symmetric padding (same top and bottom) stays silent', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 10px; padding-bottom: 10px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('padding-top only (no padding-bottom) stays silent', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 10px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('padding-bottom only (no padding-top) stays silent', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-bottom: 8px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });

  it('asymmetric padding on one rule, line-height on different rule, stays silent', () => {
    // The two properties must be in SAME rule for the signal to hold.
    const result = auditPage(`
      .base { line-height: 1.5; }
      .btn { padding-top: 10px; padding-bottom: 8px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(0);
  });
});

describe('3-finding cap and report content', () => {
  it('caps at 3 findings (MODERN_FINDING_CAP)', () => {
    const result = auditPage(`
      .a { line-height: 1.5; padding-top: 10px; padding-bottom: 8px; }
      .b { line-height: 1.4; padding-top: 6px; padding-bottom: 4px; }
      .c { line-height: 1.3; padding-top: 12px; padding-bottom: 10px; }
      .d { line-height: 1.2; padding-top: 7px; padding-bottom: 5px; }
      .e { line-height: 1.1; padding-top: 9px; padding-bottom: 7px; }
    `);
    expect(opportunities(result, 'text-box-trim')).toHaveLength(3);
  });

  it('reports the exact paddingTop and paddingBottom values from the rule', () => {
    const result = auditPage(`
      .btn { line-height: 1.5; padding-top: 6px; padding-bottom: 4px; }
    `);
    const found = opportunities(result, 'text-box-trim');

    expect(found[0]).toHaveProperty('paddingTop', '6px');
    expect(found[0]).toHaveProperty('paddingBottom', '4px');
  });

  it('reports different units as-is (6px / 0.25rem)', () => {
    const result = auditPage(`
      .mixed { line-height: 1.5; padding-top: 6px; padding-bottom: 0.25rem; }
    `);
    const found = opportunities(result, 'text-box-trim');

    expect(found[0].paddingTop).toBe('6px');
    expect(found[0].paddingBottom).toBe('0.25rem');
  });
});

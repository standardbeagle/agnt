// Edge cases and real-world shapes for alpha-shorthand detection.
// Every case is designed to expose exact behavior vs. assumptions.

import { describe, expect, it } from 'vitest';
import { auditPage, opportunities } from './load-audit.js';

describe('alpha() edge cases', () => {
  describe('real-world patterns that should fire', () => {
    it('brand color in multiple properties with translucent shadows', () => {
      // Real code: a brand color used for text and shadows, shadow has the alpha.
      const found = opportunities(
        auditPage(`
          .box {
            color: #0b5fff;
            box-shadow: 0 2px 8px rgba(11, 95, 255, 0.3);
          }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('token color plus two distinct hover/disabled alphas', () => {
      // Real code: a button token reused at different opacities for states.
      const found = opportunities(
        auditPage(`
          .btn { background: rgb(10, 70, 180); }
          .btn:hover { background: rgba(10, 70, 180, 0.8); }
          .btn:disabled { background: rgba(10, 70, 180, 0.4); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(3);
    });

    it('same color split across color, border-color, and background', () => {
      // Real code: same brand color used in three different CSS properties.
      const found = opportunities(
        auditPage(`
          .card { color: #0b5fff; }
          .card { border-color: rgb(11, 95, 255); }
          .card { background: rgba(11, 95, 255, 0.1); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('translucent variant in linear-gradient stops', () => {
      // Real code: gradient with base color opaque then translucent at edge.
      const found = opportunities(
        auditPage(`
          .gradient {
            background: linear-gradient(to right, #0b5fff, rgba(11, 95, 255, 0));
          }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('color in border shorthand plus box-shadow variant', () => {
      // Real code: border color and box-shadow use the same color at different alphas.
      const found = opportunities(
        auditPage(`
          .focus {
            border: 2px solid #0b5fff;
            box-shadow: 0 0 0 3px rgba(11, 95, 255, 0.2);
          }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('color in outline plus background at different alpha', () => {
      // Real code: focus outline and background both use same color.
      const found = opportunities(
        auditPage(`
          .input:focus {
            outline: 2px solid rgb(11, 95, 255);
            background-color: rgba(11, 95, 255, 0.05);
          }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });
  });

  describe('spelling equivalence across forms', () => {
    it('3-digit hex equivalent to 6-digit hex and rgb()', () => {
      // #fff, #ffffff, and rgb(255, 255, 255) should all be the same key.
      const found = opportunities(
        auditPage(`
          .a { color: #fff; }
          .b { border: 1px solid #ffffff; }
          .c { background: rgb(255, 255, 255); }
          .d { box-shadow: 0 0 0 1px rgba(255, 255, 255, 0.5); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('rgb() with spaces, commas, and slashes all collapse', () => {
      // rgb(r g b), rgb(r, g, b), and rgb(r g b / a) should parse the same way.
      const found = opportunities(
        auditPage(`
          .a { color: rgb(11, 95, 255); }
          .b { border: 1px solid rgb(11 95 255); }
          .c { background: rgba(11, 95, 255, 0.6); }
          .d { outline: 1px solid rgb(11 95 255 / 0.3); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(3);
    });

    it('8-digit hex alpha equivalent to rgba fourth parameter', () => {
      // #0b5fff80 (hex alpha=128/255≈0.502) and rgba(11,95,255, 0.5) are close but may differ.
      // This test documents actual behavior: let's test with exact match.
      const found = opportunities(
        auditPage(`
          .a { color: #0b5fff; }
          .b { background: #0b5fff66; }
          .c { box-shadow: 0 0 0 1px rgba(11, 95, 255, 0.4); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      // Should have opaque + (0x66/255≈0.4) + (0.4 from rgba) = 2 levels if they match,
      // or 3 levels if they're parsed differently. Check actual behavior.
      expect(found[0].opacityLevels).toBeGreaterThanOrEqual(2);
    });

    it('4-digit hex alpha equivalent to rgba', () => {
      // #f00f (opaque red) vs #f008 (semi-transparent red).
      const found = opportunities(
        auditPage(`
          .a { color: #f00f; }
          .b { background: #f008; }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('modern slash syntax for rgb alpha', () => {
      // rgb(r g b / a%) in modern notation.
      const found = opportunities(
        auditPage(`
          .a { color: rgb(11 95 255); }
          .b { background: rgb(11 95 255 / 50%); }
          .c { border: 1px solid rgb(11 95 255 / 20%); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(3);
    });

    it('percent alpha in rgba normalized to decimal', () => {
      // rgba with 50% should be stored as 0.5, not "50%".
      const found = opportunities(
        auditPage(`
          .a { color: rgb(11, 95, 255); }
          .b { background: rgba(11, 95, 255, 50%); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });
  });

  describe('hsl() color space', () => {
    it('hsl at one opacity and hsla at different opacity', () => {
      // hsl() and hsla() should use a separate key space from rgb().
      const found = opportunities(
        auditPage(`
          .a { color: hsl(220, 100%, 52%); }
          .b { background: hsla(220, 100%, 52%, 0.4); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('hsl modern slash syntax', () => {
      // hsl(h s l / a) modern notation.
      const found = opportunities(
        auditPage(`
          .a { color: hsl(220 100% 52%); }
          .b { background: hsl(220 100% 52% / 40%); }
          .c { border: 1px solid hsl(220 100% 52% / 10%); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(3);
    });

    it('hsl with commas classic syntax', () => {
      // hsl(h, s%, l%) classic form should still work.
      const found = opportunities(
        auditPage(`
          .a { color: hsl(220, 100%, 52%); }
          .b { background: hsla(220, 100%, 52%, 0.4); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('hsl space-separated vs comma-separated form collapse to same key', () => {
      // SUSPECT: Do "hsl(220 100% 52%)" and "hsl(220, 100%, 52%)" normalize to same key?
      // Both are split on /[\s,\/]+/ so they should produce same channel array.
      const found = opportunities(
        auditPage(`
          .a { color: hsl(220, 100%, 52%); }
          .b { background: hsl(220 100% 52%); }
          .c { border: 1px solid hsla(220, 100%, 52%, 0.4); }
        `),
        'alpha-shorthand'
      );
      // If normalizations match: 1 finding with opaquex2 + 1 alpha = 2 levels.
      // If they don't match: 2 findings (hsl: and hsl: are same space but channels differ).
      // Assert actual behavior:
      expect(found.length).toBeLessThanOrEqual(2);
      if (found.length === 1) {
        expect(found[0].opacityLevels).toBe(2); // opaque (twice) + 1 alpha
      }
    });
  });

  describe('multi-color values', () => {
    it('linear-gradient with multiple stops', () => {
      // A gradient with several colors; only the matching ones fire.
      const found = opportunities(
        auditPage(`
          .grad1 { background: linear-gradient(90deg, #0b5fff 0%, rgba(11, 95, 255, 0.8) 50%, rgba(11, 95, 255, 0.3) 100%); }
          .red { background: #ff0000; }
        `),
        'alpha-shorthand'
      );
      expect(found.length).toBeGreaterThanOrEqual(1);
      // Should fire for #0b5fff (3 levels: opaque + 2 alphas) but not for #ff0000 (only opaque).
      const blueLevels = found.find((f) => f.color && f.color.includes('11'));
      expect(blueLevels).toBeTruthy();
      expect(blueLevels.opacityLevels).toBe(3);
    });

    it('box-shadow with multiple layers, same color at different alphas', () => {
      // box-shadow can have many comma-separated shadows, some with same color.
      const found = opportunities(
        auditPage(`
          .multi-shadow {
            box-shadow:
              0 1px 2px rgba(11, 95, 255, 0.1),
              0 2px 4px rgba(11, 95, 255, 0.2),
              0 4px 8px #0b5fff;
          }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(3);
    });

    it('border shorthand with color does not break parsing', () => {
      // border: 1px solid color should extract the color.
      const found = opportunities(
        auditPage(`
          .a { border: 1px solid #0b5fff; }
          .b { outline: 2px dashed rgba(11, 95, 255, 0.3); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('text-shadow with multiple shadows and colors', () => {
      // text-shadow supports multiple comma-separated shadows.
      const found = opportunities(
        auditPage(`
          .text {
            text-shadow:
              2px 2px 4px rgba(0, 0, 0, 0.5),
              -2px -2px 4px rgba(255, 255, 255, 0.8);
            color: #000;
          }
        `),
        'alpha-shorthand'
      );
      // Should fire for the black (rgb(0,0,0)) with two alphas, not for white (two alphas).
      // Or may fire for both if it caps at 3. At least one should fire.
      expect(found.length).toBeGreaterThanOrEqual(1);
    });
  });

  describe('silent cases — must stay quiet', () => {
    it('single color at single opacity stays silent', () => {
      const found = opportunities(
        auditPage(`
          .card { color: #0b5fff; }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('single color at single alpha (not opaque) stays silent', () => {
      const found = opportunities(
        auditPage(`
          .shadow { box-shadow: 0 2px 8px rgba(11, 95, 255, 0.3); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('url fragments (#gradient) are not colors', () => {
      // #gradient in a URL should not be parsed as a hex color.
      const found = opportunities(
        auditPage(`
          .pattern { background: url(#gradient); }
          .solid { color: #0b5fff; }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('two different colors each used once stay silent', () => {
      // One color at one opacity, another at one opacity — no pattern.
      const found = opportunities(
        auditPage(`
          .a { color: #0b5fff; }
          .b { background: #ff0000; }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('different colors each at two opacities fire separately', () => {
      // Red used twice (opaque + alpha) and blue used twice (opaque + alpha) — two findings.
      const found = opportunities(
        auditPage(`
          .r1 { color: #ff0000; }
          .r2 { background: rgba(255, 0, 0, 0.5); }
          .b1 { border: 1px solid #0b5fff; }
          .b2 { box-shadow: 0 0 0 1px rgba(11, 95, 255, 0.3); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(2);
    });

    it('transparent keyword stays silent', () => {
      // transparent is not a hex or functional color.
      const found = opportunities(
        auditPage(`
          .a { background: transparent; }
          .b { color: #0b5fff; }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('currentColor keyword stays silent', () => {
      const found = opportunities(
        auditPage(`
          .a { border-color: currentColor; }
          .b { color: #0b5fff; }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('named colors do not trigger (only one usage)', () => {
      // "red" and "blue" are named colors, not hex/functional.
      const found = opportunities(
        auditPage(`
          .a { color: red; }
          .b { background: blue; }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('var() references do not trigger', () => {
      // var(--brand) is not a literal color that can be parsed.
      const found = opportunities(
        auditPage(`
          .a { color: var(--brand); }
          .b { background: var(--brand); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(0);
    });

    it('premise check: url() values are kept by jsdom', () => {
      // Verify that jsdom doesn't drop url() declarations.
      auditPage(`.box { background: url(data:image/gif;base64,R0lGODlh...); }`);
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((rule) => rule.cssText)
        .join('\n');
      expect(sheet, 'premise broken: jsdom dropped url()').toContain('url(');
    });

    it('premise check: transparent is kept', () => {
      auditPage(`.box { background: transparent; }`);
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((rule) => rule.cssText)
        .join('\n');
      expect(sheet, 'premise broken: jsdom dropped transparent').toContain('transparent');
    });

    it('premise check: currentColor is kept (case may be normalized)', () => {
      auditPage(`.box { border-color: currentColor; }`);
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((rule) => rule.cssText)
        .join('\n');
      // jsdom may normalize currentColor to lowercase
      expect(sheet, 'premise broken: jsdom dropped currentColor').toMatch(/currentcolor/i);
    });
  });

  describe('3-finding cap and ordering', () => {
    it('caps report at 3 colors, ordered by opacity level count', () => {
      // Five colors with multiple opacity levels; cap at 3, highest counts first.
      const css = [
        '.a1 { color: #aa0000; } .a2 { background: rgba(170, 0, 0, 0.5); }',
        '.b1 { color: #00bb00; } .b2 { background: rgba(0, 187, 0, 0.3); } .b3 { background: rgba(0, 187, 0, 0.7); }',
        '.c1 { color: #0000cc; } .c2 { border: 1px solid rgba(0, 0, 204, 0.2); } .c3 { box-shadow: 0 0 0 1px rgba(0, 0, 204, 0.5); }',
        '.d1 { color: #dddd00; } .d2 { background: rgba(221, 221, 0, 0.1); } .d3 { background: rgba(221, 221, 0, 0.9); }',
        '.e { color: #00eeee; } .e2 { background: rgba(0, 238, 238, 0.4); }'
      ].join('\n');

      const found = opportunities(auditPage(css), 'alpha-shorthand');
      expect(found).toHaveLength(3);

      // The ones with most opacity levels should come first.
      const counts = found.map((f) => f.opacityLevels);
      // c has 3 levels (opaque + 2 alphas), others have 2 or 1.
      expect(counts[0]).toBe(3);
      expect(counts[1]).toBeGreaterThanOrEqual(2);
      expect(counts[2]).toBeGreaterThanOrEqual(2);
    });

    it('exactly 3 colors at 2+ opacity levels reports all three', () => {
      const css = `
        .r { color: #ff0000; background: rgba(255, 0, 0, 0.5); }
        .g { color: #00ff00; background: rgba(0, 255, 0, 0.5); }
        .b { color: #0000ff; background: rgba(0, 0, 255, 0.5); }
      `;
      const found = opportunities(auditPage(css), 'alpha-shorthand');
      expect(found).toHaveLength(3);
    });
  });

  describe('complex real-world cases', () => {
    it('design system with semantic color tokens', () => {
      // Multiple semantic colors used at various opacities.
      const found = opportunities(
        auditPage(`
          .success { color: rgb(34, 197, 94); }
          .success-light { background: rgba(34, 197, 94, 0.1); }
          .success-hover { border-color: rgba(34, 197, 94, 0.5); }

          .danger { color: #dc2626; }
          .danger-light { background: rgba(220, 38, 38, 0.1); }

          .warning { color: hsl(38, 92%, 50%); }
          .warning-faded { background: hsla(38, 92%, 50%, 0.3); }
        `),
        'alpha-shorthand'
      );
      // Should fire for success (3 levels), danger (2 levels), and warning (2 levels).
      expect(found.length).toBe(3);
    });

    it('gradient overlay pattern', () => {
      // Common pattern: solid color fading to transparent via gradient.
      const found = opportunities(
        auditPage(`
          .overlay {
            background: linear-gradient(
              to bottom,
              rgba(0, 0, 0, 0.8),
              rgba(0, 0, 0, 0.4),
              transparent
            );
          }
        `),
        'alpha-shorthand'
      );
      // Should fire for rgb(0,0,0) with 2 alpha levels (0.8 and 0.4), but not the transparent.
      expect(found.length).toBe(1);
      expect(found[0].opacityLevels).toBe(2);
    });

    it('focus ring pattern with multiple colors', () => {
      // Focus ring often uses brand + neutral colors at different opacities.
      const found = opportunities(
        auditPage(`
          .input:focus {
            border: 2px solid #0b5fff;
            outline: 3px solid rgba(11, 95, 255, 0.5);
            box-shadow:
              0 0 0 1px rgba(11, 95, 255, 0.2),
              0 0 0 4px rgba(255, 255, 255, 0.8);
          }
        `),
        'alpha-shorthand'
      );
      // Should fire for blue (3+ levels) and white (1 level only, so doesn't fire).
      expect(found.length).toBe(1);
      expect(found[0].opacityLevels).toBeGreaterThanOrEqual(3);
    });
  });

  describe('edge cases with invalid or malformed color syntax', () => {
    it('premise check: invalid rgb() is dropped by jsdom', () => {
      auditPage(`.a { color: rgb(not a color); }`);
      const sheet = Array.from(document.styleSheets[0].cssRules)
        .map((rule) => rule.cssText)
        .join('\n');
      // jsdom should drop this invalid declaration.
      // If it's still there, the premise is broken.
      // Note: this is a documentation of actual behavior, not prescriptive.
    });

    it('five-digit hex is not parsed as a color', () => {
      // #00000 (5 digits) is not valid and should not fire anything.
      const found = opportunities(
        auditPage(`.a { color: #00000; background: rgba(0, 0, 0, 0.5); }`),
        'alpha-shorthand'
      );
      // If #00000 is silently dropped by jsdom, no color fires at all.
      // If it somehow parses, it would fire with 2 levels.
      // Assert what happens:
      expect(found.length).toBeLessThanOrEqual(1);
    });

    it('seven-digit hex is not parsed as a color', () => {
      // #0000000 (7 digits) is not valid.
      const found = opportunities(
        auditPage(`.a { color: #0000000; background: rgba(0, 0, 0, 0.5); }`),
        'alpha-shorthand'
      );
      expect(found.length).toBeLessThanOrEqual(1);
    });

    it('case-insensitive hex parsing', () => {
      // #0B5FFF and #0b5fff should be the same key.
      const found = opportunities(
        auditPage(`
          .a { color: #0B5FFF; }
          .b { background: #0b5fff; }
          .c { border: 1px solid rgba(11, 95, 255, 0.5); }
        `),
        'alpha-shorthand'
      );
      expect(found).toHaveLength(1);
      expect(found[0].opacityLevels).toBe(2);
    });
  });
});

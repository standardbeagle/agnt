import { test, expect, Page } from '@playwright/test';

/**
 * Wireframe generation tests for the agnt __devtool API.
 *
 * These tests verify that the SVG wireframe generation functions work correctly
 * when invoked through the injected __devtool API.
 *
 * Prerequisites (handled by globalSetup):
 * - Fixture server running on port 8765
 * - agnt daemon running
 * - agnt proxy running on port 12345, targeting fixture server
 */

interface WireframeResult {
  svg: string;
  width: number;
  height: number;
  elementCount: number;
  viewportOnly: boolean;
  truncated: boolean;
  elements: Array<{
    selector: string;
    type: string;
    label: string;
    bounds: { x: number; y: number; width: number; height: number };
  }>;
  error?: string;
}

/**
 * Wait for __devtool API to be available on the page
 */
async function waitForDevtool(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const devtool = (window as any).__devtool;
      return (
        typeof devtool !== 'undefined' &&
        devtool !== null &&
        typeof devtool.indicator !== 'undefined'
      );
    },
    { timeout: 15000 }
  );
}

/**
 * Navigate to a fixture page through the proxy and wait for devtool
 */
async function setupPage(page: Page, fixture: string): Promise<void> {
  await page.goto(`/${fixture}`, { waitUntil: 'networkidle' });
  await waitForDevtool(page);
}

/**
 * Generate a wireframe and return the result
 */
async function generateWireframe(
  page: Page,
  options?: Record<string, unknown>
): Promise<WireframeResult> {
  return await page.evaluate((opts) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    return (window as any).__devtool.generateWireframe(opts);
  }, options);
}

/**
 * Generate a minimal wireframe
 */
async function generateMinimalWireframe(
  page: Page,
  options?: Record<string, unknown>
): Promise<WireframeResult> {
  return await page.evaluate((opts) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    return (window as any).__devtool.generateMinimalWireframe(opts);
  }, options);
}

/**
 * Generate a semantic wireframe
 */
async function generateSemanticWireframe(
  page: Page,
  options?: Record<string, unknown>
): Promise<WireframeResult> {
  return await page.evaluate((opts) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    return (window as any).__devtool.generateSemanticWireframe(opts);
  }, options);
}

test.describe('Wireframe Generation - Basic Functionality', () => {
  test('generateWireframe returns valid SVG structure', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    expect(result.error).toBeUndefined();
    expect(result.svg).toBeDefined();
    expect(result.svg).toContain('<?xml version="1.0" encoding="UTF-8"?>');
    expect(result.svg).toContain('<svg xmlns="http://www.w3.org/2000/svg"');
    expect(result.svg).toContain('</svg>');
  });

  test('generateWireframe returns dimensions', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    expect(result.width).toBeGreaterThan(0);
    expect(result.height).toBeGreaterThan(0);
  });

  test('generateWireframe returns element list', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    expect(result.elements).toBeDefined();
    expect(Array.isArray(result.elements)).toBe(true);
    expect(result.elements.length).toBeGreaterThan(0);
    expect(result.elementCount).toBe(result.elements.length);
  });

  test('element list contains expected properties', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const element = result.elements[0];
    expect(element.selector).toBeDefined();
    expect(element.type).toBeDefined();
    expect(element.bounds).toBeDefined();
    expect(element.bounds.x).toBeDefined();
    expect(element.bounds.y).toBeDefined();
    expect(element.bounds.width).toBeDefined();
    expect(element.bounds.height).toBeDefined();
  });
});

test.describe('Wireframe Generation - Semantic Elements', () => {
  test('identifies header element', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const headerElements = result.elements.filter((e) => e.type === 'header');
    expect(headerElements.length).toBeGreaterThan(0);
  });

  test('identifies nav element', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const navElements = result.elements.filter((e) => e.type === 'nav');
    expect(navElements.length).toBeGreaterThan(0);
  });

  test('identifies main element', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const mainElements = result.elements.filter((e) => e.type === 'main');
    expect(mainElements.length).toBeGreaterThan(0);
  });

  test('identifies footer element', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const footerElements = result.elements.filter((e) => e.type === 'footer');
    expect(footerElements.length).toBeGreaterThan(0);
  });

  test('identifies aside element', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const asideElements = result.elements.filter((e) => e.type === 'aside');
    expect(asideElements.length).toBeGreaterThan(0);
  });

  test('identifies form elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const formElements = result.elements.filter((e) => e.type === 'form');
    expect(formElements.length).toBeGreaterThan(0);
  });

  test('identifies button elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const buttonElements = result.elements.filter((e) => e.type === 'button');
    expect(buttonElements.length).toBeGreaterThan(0);
  });

  test('identifies image elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const imageElements = result.elements.filter((e) => e.type === 'image');
    expect(imageElements.length).toBeGreaterThan(0);
  });

  test('identifies heading elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const headingElements = result.elements.filter((e) => e.type === 'heading');
    expect(headingElements.length).toBeGreaterThan(0);
  });
});

test.describe('Wireframe Generation - Configuration Options', () => {
  test('viewportOnly limits to visible elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    // Scroll to middle of page
    await page.evaluate(() => window.scrollTo(0, 500));
    await page.waitForTimeout(100);

    const fullResult = await generateWireframe(page, { viewportOnly: false });
    const viewportResult = await generateWireframe(page, { viewportOnly: true });

    // Viewport-only should have fewer elements
    expect(viewportResult.elementCount).toBeLessThan(fullResult.elementCount);
    expect(viewportResult.viewportOnly).toBe(true);
    expect(fullResult.viewportOnly).toBe(false);
  });

  test('maxDepth limits DOM traversal', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const deepResult = await generateWireframe(page, { maxDepth: 10 });
    const shallowResult = await generateWireframe(page, { maxDepth: 3 });

    // Shallow depth should have fewer elements
    expect(shallowResult.elementCount).toBeLessThan(deepResult.elementCount);
  });

  test('maxElements limits element count', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const limitedResult = await generateWireframe(page, { maxElements: 20 });

    expect(limitedResult.elementCount).toBeLessThanOrEqual(20);
    expect(limitedResult.truncated).toBe(true);
  });

  test('minWidth/minHeight filters small elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const largeMin = await generateWireframe(page, { minWidth: 100, minHeight: 50 });
    const smallMin = await generateWireframe(page, { minWidth: 5, minHeight: 5 });

    // Larger minimums should result in fewer elements
    expect(largeMin.elementCount).toBeLessThan(smallMin.elementCount);
  });

  test('includeText option adds labels', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const withText = await generateWireframe(page, { includeText: true });
    const withoutText = await generateWireframe(page, { includeText: false });

    // SVG with text should have wireframe-label text elements
    // Note: Viewport indicator text is always present, so we check for label class
    expect(withText.svg).toContain('<text class="wireframe-label"');
    expect(withoutText.svg).not.toContain('<text class="wireframe-label"');
  });

  test('colorScheme mono produces monochrome output', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const result = await generateWireframe(page, { colorScheme: 'mono' });

    // Mono scheme should use gray strokes
    expect(result.svg).toContain('stroke="#666666"');
  });

  test('colorScheme semantic uses colored output', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const result = await generateWireframe(page, { colorScheme: 'semantic' });

    // Semantic scheme should have fill-opacity for colors
    expect(result.svg).toContain('fill-opacity="0.1"');
  });

  test('excludeSelectors filters elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const withFooter = await generateWireframe(page);
    const withoutFooter = await generateWireframe(page, {
      excludeSelectors: ['footer'],
    });

    const footerInFull = withFooter.elements.filter((e) => e.type === 'footer');
    const footerInFiltered = withoutFooter.elements.filter(
      (e) => e.type === 'footer'
    );

    expect(footerInFull.length).toBeGreaterThan(0);
    expect(footerInFiltered.length).toBe(0);
  });
});

test.describe('Wireframe Generation - Variant Functions', () => {
  test('generateMinimalWireframe produces fewer elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const fullResult = await generateWireframe(page);
    const minimalResult = await generateMinimalWireframe(page);

    expect(minimalResult.elementCount).toBeLessThan(fullResult.elementCount);
  });

  test('generateMinimalWireframe has no text labels', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const result = await generateMinimalWireframe(page);

    expect(result.svg).not.toContain('<text class="wireframe-label"');
  });

  test('generateSemanticWireframe uses semantic colors', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const result = await generateSemanticWireframe(page);

    // Should use semantic color fills
    expect(result.svg).toContain('fill-opacity="0.1"');
  });

  test('generateSemanticWireframe includes text labels', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    const result = await generateSemanticWireframe(page);

    expect(result.svg).toContain('<text');
  });
});

test.describe('Wireframe Generation - SVG Output Quality', () => {
  test('SVG contains proper XML declaration', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    expect(result.svg.startsWith('<?xml version="1.0" encoding="UTF-8"?>')).toBe(
      true
    );
  });

  test('SVG contains title and description', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    expect(result.svg).toContain('<title>');
    expect(result.svg).toContain('</title>');
    expect(result.svg).toContain('<desc>');
    expect(result.svg).toContain('</desc>');
  });

  test('SVG contains style definitions', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    expect(result.svg).toContain('<defs>');
    expect(result.svg).toContain('<style');
    expect(result.svg).toContain('.wireframe-rect');
  });

  test('SVG rectangles have data attributes', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    expect(result.svg).toContain('data-selector=');
    expect(result.svg).toContain('data-type=');
  });

  test('SVG properly escapes special characters', async ({ page }) => {
    // Use fixture page with special characters in content
    await setupPage(page, 'special-chars.html');

    const result = await generateWireframe(page, { includeText: true });

    // SVG should be valid - no unescaped ampersands, quotes, or angle brackets
    // The regex checks for ampersands that are NOT followed by xml entity names
    expect(result.svg).not.toMatch(/<text[^>]*>[^<]*&(?!amp;|lt;|gt;|quot;|apos;)/);
    // Also verify the SVG is valid by checking it starts/ends correctly
    expect(result.svg).toContain('<?xml version="1.0" encoding="UTF-8"?>');
    expect(result.svg).toContain('</svg>');
  });
});

test.describe('Wireframe Generation - Element Filtering', () => {
  test('excludes display:none elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    // The hidden-element class has display:none
    const hiddenElements = result.elements.filter((e) =>
      e.selector.includes('hidden-element')
    );
    expect(hiddenElements.length).toBe(0);
  });

  test('excludes visibility:hidden elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const invisibleElements = result.elements.filter((e) =>
      e.selector.includes('invisible-element')
    );
    expect(invisibleElements.length).toBe(0);
  });

  test('excludes opacity:0 elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');
    const result = await generateWireframe(page);

    const zeroOpacityElements = result.elements.filter((e) =>
      e.selector.includes('zero-opacity')
    );
    expect(zeroOpacityElements.length).toBe(0);
  });

  test('excludes devtool indicator elements', async ({ page }) => {
    await setupPage(page, 'wireframe-test.html');

    // Show the indicator to add devtool elements to the page
    await page.evaluate(() => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const devtool = (window as any).__devtool;
      if (devtool?.indicator) {
        devtool.indicator.show();
      }
    });
    await page.waitForTimeout(300);

    const result = await generateWireframe(page);

    // No elements should have __devtool in their selector
    const devtoolElements = result.elements.filter((e) =>
      e.selector.includes('__devtool')
    );
    expect(devtoolElements.length).toBe(0);
  });
});

test.describe('Wireframe Generation - Clean Page', () => {
  test('works on clean baseline page', async ({ page }) => {
    await setupPage(page, 'clean-baseline.html');
    const result = await generateWireframe(page);

    expect(result.error).toBeUndefined();
    expect(result.svg).toBeDefined();
    expect(result.elementCount).toBeGreaterThan(0);
  });

  test('identifies semantic elements on clean page', async ({ page }) => {
    await setupPage(page, 'clean-baseline.html');
    const result = await generateWireframe(page);

    const semanticTypes = new Set(result.elements.map((e) => e.type));

    // Clean page has main, form, button, link, image elements
    expect(semanticTypes.has('main')).toBe(true);
    expect(semanticTypes.has('form')).toBe(true);
    expect(semanticTypes.has('button')).toBe(true);
  });
});

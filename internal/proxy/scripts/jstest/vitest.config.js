import { defineConfig } from 'vitest/config';

// jsdom, not a real browser: these tests read stylesheets, computed styles and
// element attributes, which jsdom serves faithfully. A real-Chrome tier already
// exists for the things jsdom cannot answer for (layout, paint, renderer
// timing) and is deliberately kept out of the default suite — see AGENTS.md
// § Testing, Browser E2E loud-skip policy.
export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['*.test.js'],
    restoreMocks: true
  }
});

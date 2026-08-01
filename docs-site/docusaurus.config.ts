import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// Single source of truth for the description shown in search results and in
// Slack/Discord/X link unfurls.
const SITE_DESCRIPTION =
  'agnt is an MCP server that gives your AI coding agent eyes into the browser: screenshots, DOM and layout inspection, live error capture, quality audits, sketch and design mode, and guided walkthroughs.';

const config: Config = {
  title: 'agnt',
  tagline: 'Give your AI coding agent browser superpowers - Screenshots, DOM inspection, visual debugging, and real-time error capture',
  favicon: 'img/favicon.ico',

  future: {
    v4: true,
  },

  // GitHub Pages configuration
  url: 'https://dev.standardbeagle.com',
  baseUrl: '/agnt/',

  // GitHub repository info
  organizationName: 'standardbeagle',
  projectName: 'agnt',
  trailingSlash: false,
  deploymentBranch: 'gh-pages',

  onBrokenLinks: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  headTags: [
    {
      tagName: 'script',
      attributes: {type: 'application/ld+json'},
      innerHTML: JSON.stringify({
        '@context': 'https://schema.org',
        '@type': 'SoftwareApplication',
        name: 'agnt',
        applicationCategory: 'DeveloperApplication',
        operatingSystem: 'Linux, macOS, Windows',
        description: SITE_DESCRIPTION,
        url: 'https://dev.standardbeagle.com/agnt/',
        offers: {
          '@type': 'Offer',
          price: '0',
          priceCurrency: 'USD',
        },
        sourceOrganization: {
          '@type': 'Organization',
          name: 'Standard Beagle',
          url: 'https://github.com/standardbeagle',
        },
      }),
    },
  ],

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
          editUrl: 'https://github.com/standardbeagle/agnt/tree/main/docs-site/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    // Default social card for every page. Docusaurus expands this to absolute
    // og:image + twitter:image URLs; per-page front matter `image:` overrides it.
    // Regenerate with: npm run social-card (source: scripts/social-card.html).
    image: 'img/agnt-social-card.png',
    metadata: [
      {name: 'keywords', content: 'MCP server, browser debugging, AI coding agent, Claude Code, Cursor, Windsurf, frontend debugging, error tracking, DOM inspection, screenshots'},
      // Site-level tags only. og:title / og:description / og:url / og:image are
      // emitted per page by the theme (from the page title + front-matter
      // `description` + themeConfig.image) — do not duplicate them here or every
      // subpage unfurls with the homepage's copy.
      // NOTE: no hardcoded absolute og:image here on purpose. themeConfig.image
      // is a RELATIVE path that Docusaurus expands against `url` above, so a
      // domain change (e.g. github.io → dev.standardbeagle.com) propagates on
      // its own instead of needing this line edited in lockstep.
      {property: 'og:type', content: 'website'},
      {property: 'og:site_name', content: 'agnt'},
      {property: 'og:image:width', content: '1200'},
      {property: 'og:image:height', content: '630'},
      {property: 'og:image:type', content: 'image/png'},
      {property: 'og:image:alt', content: 'agnt — browser superpowers for AI coding agents'},
      {name: 'twitter:card', content: 'summary_large_image'},
    ],
    colorMode: {
      defaultMode: 'dark',
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'agnt',
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
        {
          type: 'docSidebar',
          sidebarId: 'apiSidebar',
          position: 'left',
          label: 'API Reference',
        },
        {
          type: 'docSidebar',
          sidebarId: 'useCasesSidebar',
          position: 'left',
          label: 'Guides & Use Cases',
        },
        {
          href: 'https://github.com/standardbeagle/agnt',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {
              label: 'Getting Started',
              to: '/getting-started',
            },
            {
              label: 'API Reference',
              to: '/api/detect',
            },
            {
              label: 'Use Cases',
              to: '/use-cases/debugging-web-apps',
            },
          ],
        },
        {
          title: 'Features',
          items: [
            {
              label: 'Project Detection',
              to: '/features/project-detection',
            },
            {
              label: 'Process Management',
              to: '/features/process-management',
            },
            {
              label: 'Reverse Proxy',
              to: '/features/reverse-proxy',
            },
          ],
        },
        {
          title: 'Guides',
          items: [
            {
              label: 'What is MCP?',
              to: '/guides/ecosystem/what-is-mcp',
            },
            {
              label: 'agnt with Claude Code',
              to: '/guides/ai-tools/claude-code',
            },
            {
              label: 'agnt with Next.js',
              to: '/guides/frameworks/next-js',
            },
            {
              label: 'Debug Browser Errors with AI',
              to: '/guides/debug-browser-errors-ai',
            },
          ],
        },
        {
          title: 'More',
          items: [
            {
              label: 'GitHub',
              href: 'https://github.com/standardbeagle/agnt',
            },
            {
              label: 'MCP Protocol',
              href: 'https://modelcontextprotocol.io',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} agnt. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'go', 'javascript', 'typescript'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;

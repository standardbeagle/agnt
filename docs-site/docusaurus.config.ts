import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

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
        description: 'Browser superpowers for AI coding agents. Screenshots, DOM inspection, error capture, and visual debugging via MCP.',
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
    metadata: [
      {name: 'keywords', content: 'MCP server, browser debugging, AI coding agent, Claude Code, Cursor, Windsurf, frontend debugging, error tracking, DOM inspection, screenshots'},
      {name: 'og:type', content: 'website'},
      {name: 'og:image', content: 'https://dev.standardbeagle.com/agnt/img/docusaurus-social-card.jpg'},
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

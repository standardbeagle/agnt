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
  url: 'https://standardbeagle.github.io',
  baseUrl: '/agnt/',

  // GitHub repository info
  organizationName: 'standardbeagle',
  projectName: 'agnt',
  trailingSlash: false,
  deploymentBranch: 'gh-pages',

  onBrokenLinks: 'throw',
  onBrokenMarkdownLinks: 'warn',

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
          label: 'Use Cases',
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

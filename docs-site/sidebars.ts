import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'intro',
    'getting-started',
    {
      type: 'category',
      label: 'Features',
      collapsed: false,
      items: [
        'features/project-detection',
        'features/process-management',
        'features/reverse-proxy',
        'features/frontend-diagnostics',
      ],
    },
    {
      type: 'category',
      label: 'Concepts',
      items: [
        'concepts/architecture',
        'concepts/lock-free-design',
        'concepts/graceful-shutdown',
      ],
    },
  ],
  apiSidebar: [
    {
      type: 'category',
      label: 'MCP Tools',
      collapsed: false,
      items: [
        'api/detect',
        'api/run',
        'api/proc',
        'api/proxy',
        'api/proxylog',
        'api/currentpage',
        'api/tunnel',
        'api/daemon',
      ],
    },
    {
      type: 'category',
      label: 'Frontend API',
      collapsed: false,
      items: [
        'api/frontend/overview',
        {
          type: 'category',
          label: 'Core Primitives',
          collapsed: true,
          items: [
            'api/frontend/element-inspection',
            'api/frontend/tree-walking',
            'api/frontend/visual-state',
            'api/frontend/layout-diagnostics',
            'api/frontend/visual-overlays',
            'api/frontend/interactive',
            'api/frontend/state-capture',
            'api/frontend/accessibility',
            'api/frontend/composite',
          ],
        },
        {
          type: 'category',
          label: 'Quality & Performance',
          collapsed: true,
          items: [
            'api/frontend/layout-robustness',
            'api/frontend/quality-auditing',
          ],
        },
        {
          type: 'category',
          label: 'CSS & Security',
          collapsed: true,
          items: [
            'api/frontend/css-evaluation',
            'api/frontend/security-validation',
          ],
        },
      ],
    },
  ],
  useCasesSidebar: [
    {
      type: 'category',
      label: 'Real-World Use Cases',
      collapsed: false,
      items: [
        'use-cases/debugging-web-apps',
        'use-cases/automated-testing',
        'use-cases/mobile-testing',
        'use-cases/performance-monitoring',
        'use-cases/ci-cd-integration',
        'use-cases/accessibility-auditing',
        'use-cases/frontend-error-tracking',
      ],
    },
  ],
};

export default sidebars;

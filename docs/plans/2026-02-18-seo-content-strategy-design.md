# SEO Content Strategy for agnt Docs Site

**Date**: 2026-02-18
**Status**: Design

## Goal

Create SEO-optimized content for the agnt Docusaurus docs site that captures organic search traffic from:
1. All AI coding agent users (Claude Code, Cursor, Copilot, Windsurf, OpenClaw, ClawdBot, Aider, Gemini CLI)
2. Frontend developers generally (debugging, error tracking, testing, accessibility)
3. MCP ecosystem developers (learning about MCP, finding tools)

All content lives in the docs (no blog). Honest, technically useful content that ranks well because it genuinely helps developers.

## 1. SEO Infrastructure

### docusaurus.config.ts Changes

Add global metadata:
```ts
metadata: [
  {name: 'keywords', content: 'MCP server, browser debugging, AI coding agent, Claude Code, Cursor, frontend debugging, error tracking'},
  {name: 'og:type', content: 'website'},
  {name: 'twitter:card', content: 'summary_large_image'},
],
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
      offers: {price: '0', priceCurrency: 'USD'},
    }),
  },
],
```

### Per-Page SEO Frontmatter

Every new page includes:
```yaml
---
title: "Page Title - Target Keyword"
description: "150-160 char meta description with primary keyword naturally included"
keywords: [keyword1, keyword2, keyword3]
---
```

### Internal Linking Rules

- Every page links to 2-3 related pages within the body text
- Every page has a "See Also" section at the bottom
- Framework guides link to relevant API docs
- Problem-solution pages link to the matching use-case pages

## 2. New Content: Framework Integration Guides

Location: `docs-site/docs/guides/frameworks/`

Each guide: ~800-1200 words. Template:
1. What agnt adds to {framework} development (2-3 sentences)
2. Prerequisites (framework-specific)
3. `.agnt.kdl` configuration (with framework-specific url-matchers)
4. Common workflow (3-4 practical examples)
5. Framework-specific tips (SSR errors, HMR handling, etc.)
6. See Also links

### Pages

#### next-js.md
- **Title**: "Using agnt with Next.js - AI-Powered Browser Debugging"
- **Description**: "Debug Next.js apps with AI. Capture SSR errors, hydration mismatches, and runtime exceptions automatically. Works with Claude Code, Cursor, and any MCP client."
- **Keywords**: next.js debugging AI, next.js MCP server, debug next.js Claude Code
- **Content highlights**: SSR vs client error capture, hydration mismatch debugging, App Router vs Pages Router config, API route error tracking

#### vite-react.md
- **Title**: "Using agnt with Vite + React - AI Browser Debugging"
- **Description**: "Set up agnt with Vite and React for real-time error capture, DOM inspection, and visual debugging with your AI coding agent."
- **Keywords**: vite debugging AI, react error tracking AI, vite MCP server
- **Content highlights**: HMR integration, React error boundary capture, Vite proxy config, component inspection

#### django-flask.md
- **Title**: "Using agnt with Django and Flask - AI Browser Debugging for Python"
- **Description**: "Debug Django and Flask web applications with AI. Capture template errors, API failures, and frontend issues with agnt's MCP tools."
- **Keywords**: django debugging AI, flask debugging AI, python web debugging MCP
- **Content highlights**: manage.py/flask run process management, template error capture, Django REST Framework error tracking, static file proxy config

#### rails.md
- **Title**: "Using agnt with Ruby on Rails - AI-Powered Web Debugging"
- **Description**: "Debug Rails applications with AI coding agents. Automatic error capture, request logging, and visual debugging for Rails development."
- **Keywords**: rails debugging AI, ruby on rails MCP server, rails error tracking
- **Content highlights**: rails server process management, ActionCable/Turbo debugging, asset pipeline proxy config

#### go-htmx.md
- **Title**: "Using agnt with Go, Templ, and HTMX - AI Browser Debugging"
- **Description**: "Debug Go web applications using Templ and HTMX with AI. Capture server errors, HTMX swap failures, and template issues automatically."
- **Keywords**: go web debugging AI, htmx debugging, templ debugging MCP
- **Content highlights**: Go compile error capture via process alerts, HTMX swap error detection, Templ hot reload config

## 3. New Content: AI Tool Integration Guides

Location: `docs-site/docs/guides/ai-tools/`

Each guide: ~600-1000 words. Template:
1. What agnt adds to {tool} (2-3 sentences)
2. Installation for this specific tool
3. Configuration (tool-specific MCP setup)
4. First debugging session walkthrough
5. Tips specific to this tool's workflow
6. See Also links

### Pages

#### claude-code.md
- **Title**: "agnt with Claude Code - Browser Superpowers for Claude"
- **Description**: "Give Claude Code browser debugging superpowers. Screenshots, DOM inspection, error capture, and visual feedback via MCP. Install in one command."
- **Keywords**: claude code browser debugging, claude code MCP browser, claude code screenshot
- **Content highlights**: Marketplace install, `--append-system-prompt` auto-injection, agnt run integration, session management

#### cursor.md
- **Title**: "agnt with Cursor - Browser Debugging for Cursor IDE"
- **Description**: "Add browser debugging to Cursor IDE. Capture JavaScript errors, inspect DOM elements, and take screenshots directly from your AI coding assistant."
- **Keywords**: cursor browser debugging, cursor MCP server browser, cursor IDE debugging
- **Content highlights**: MCP config in Cursor settings, Composer workflow, tool approval settings

#### windsurf-copilot.md
- **Title**: "agnt with Windsurf and GitHub Copilot - Browser Debugging"
- **Description**: "Add browser superpowers to Windsurf and GitHub Copilot. Real-time error capture, visual debugging, and DOM inspection via MCP."
- **Keywords**: windsurf debugging, copilot browser debugging, windsurf MCP server
- **Content highlights**: MCP configuration for each tool, workflow differences

#### openclaw-clawdbot.md
- **Title**: "agnt with OpenClaw and ClawdBot - Browser Debugging"
- **Description**: "Use agnt with OpenClaw and ClawdBot for browser debugging, error capture, and visual feedback. Full MCP integration guide."
- **Keywords**: openclaw browser debugging, clawdbot MCP, openclaw MCP server
- **Content highlights**: MCP setup, stdin injection for system prompts, workflow tips

#### aider-gemini.md
- **Title**: "agnt with Aider and Gemini CLI - Browser Debugging"
- **Description**: "Add browser debugging to Aider and Google Gemini CLI. Capture errors, inspect pages, and debug visually with agnt's MCP tools."
- **Keywords**: aider browser debugging, gemini cli MCP, aider MCP server
- **Content highlights**: agnt run stdin injection, terminal-based workflow

## 4. New Content: Problem-Solution Pages

Location: `docs-site/docs/guides/`

Each page: ~1000-1500 words. Structure:
1. **The problem** (2-3 paragraphs matching the search query — "You've been there...")
2. **The traditional approach** (shows pain: copy-pasting, describing, alt-tabbing)
3. **The agnt approach** (concrete before/after with code examples)
4. **Step-by-step walkthrough** (practical, reproducible)
5. **See Also** (internal links)

### Pages

#### debug-browser-errors-ai.md
- **Title**: "How to Debug Browser Errors with an AI Coding Agent"
- **Description**: "Stop copy-pasting error messages. Let your AI coding agent see browser errors directly with automatic stack traces, source maps, and context."
- **Keywords**: debug browser errors AI, AI frontend debugging, javascript error AI
- **Key content**: Error capture flow, get_errors tool, stack trace reduction, before/after comparison

#### frontend-error-tracking-realtime.md
- **Title**: "Real-Time Frontend Error Tracking for AI Coding Agents"
- **Description**: "Automatic JavaScript error capture with full stack traces, deduplication, and noise filtering. Feed browser errors directly to your AI assistant."
- **Keywords**: frontend error tracking MCP, javascript error monitoring AI, browser error capture realtime
- **Key content**: How the proxy captures errors, get_errors tool, noise filtering, custom error logging via __devtool.log()

#### chaos-engineering-frontend.md
- **Title**: "Chaos Engineering for Frontend Applications"
- **Description**: "Test your frontend against slow networks, API failures, and race conditions. Built-in chaos engineering for web applications using AI coding agents."
- **Keywords**: frontend chaos engineering, chaos testing web app, resilience testing frontend, fault injection
- **Key content**: Presets (flaky-api, mobile-3g, race-condition), custom rules, what to verify

#### accessibility-auditing-ai.md
- **Title**: "AI-Powered Accessibility Auditing for Web Applications"
- **Description**: "Run WCAG accessibility audits with AI. Automatic axe-core testing, contrast checking, focus indicators, and actionable fix suggestions."
- **Keywords**: accessibility audit AI, WCAG testing AI, a11y automation MCP
- **Key content**: Four audit modes (standard, fast, comprehensive, basic), interpreting results, fixing common issues

#### css-layout-debugging-ai.md
- **Title**: "Debugging CSS Layout Issues with AI Coding Agents"
- **Description**: "Debug z-index battles, overflow issues, and flexbox edge cases with AI. Automatic stacking context analysis, overflow detection, and layout diagnostics."
- **Keywords**: CSS layout debugging AI, z-index debugging tool, overflow debugging, stacking context analysis
- **Key content**: inspect(), findOverflows(), findStackingContexts(), real examples of common layout bugs

#### visual-regression-testing-ai.md
- **Title**: "Visual Regression Testing with AI Coding Agents"
- **Description**: "Catch visual regressions before they ship. Screenshot comparison, baseline management, and AI-assisted diff analysis for frontend development."
- **Keywords**: visual regression testing AI, screenshot comparison tool, UI testing automation
- **Key content**: snapshot tool (baseline/compare), threshold configuration, CI integration

#### responsive-design-testing-ai.md
- **Title**: "How to Debug Responsive Design with AI Coding Agents"
- **Description**: "Test responsive layouts across screen sizes with AI. Detect fixed-width elements, touch target issues, and viewport breakpoint problems automatically."
- **Keywords**: responsive design testing AI, mobile layout debugging, viewport testing tool
- **Key content**: checkResponsiveRisk(), checkTextFragility(), tunnel for real device testing

#### mobile-testing-real-devices-ai.md
- **Title**: "How to Test on Real Mobile Devices with AI Coding Agents"
- **Description**: "Test your web app on real phones with full debugging instrumentation. Cloudflare and ngrok tunnels with automatic error capture and performance monitoring."
- **Keywords**: mobile testing AI, real device testing MCP, tunnel debugging mobile
- **Key content**: Tunnel setup (Cloudflare/ngrok), bind_address config, mobile-specific error patterns

#### frontend-performance-monitoring-ai.md
- **Title**: "Frontend Performance Monitoring with AI Coding Agents"
- **Description**: "Monitor Core Web Vitals, load times, and resource performance with AI. Automatic performance capture and AI-assisted optimization suggestions."
- **Keywords**: frontend performance monitoring AI, core web vitals MCP, page speed AI debugging
- **Key content**: Performance log type, paint metrics, resource timing, identifying bottlenecks

## 5. New Content: Ecosystem Pages

Location: `docs-site/docs/guides/ecosystem/`

#### what-is-mcp.md
- **Title**: "What is MCP? Model Context Protocol Explained for Developers"
- **Description**: "Learn what the Model Context Protocol (MCP) is, how it works, and why it matters for AI coding agents. Practical guide with examples."
- **Keywords**: what is MCP, model context protocol explained, MCP tutorial, MCP server guide
- **Content**: What MCP is (protocol for AI tool communication), how it works (client/server over stdio/HTTP), why it matters (standardized tool access), how agnt uses it (practical example), getting started with MCP

#### best-mcp-servers-web-dev.md
- **Title**: "Best MCP Servers for Web Development in 2026"
- **Description**: "Curated list of the most useful MCP servers for web developers. Browser debugging, database access, API testing, and more."
- **Keywords**: best MCP servers, MCP servers list, top MCP tools, MCP servers web development
- **Content**: Category overview (browser/debugging: agnt, database: postgres-mcp, filesystem: built-in, testing: playwright-mcp, etc.), what makes a good MCP server, how to evaluate

## 6. Sidebar & Navigation Changes

### Updated useCasesSidebar in sidebars.ts

```ts
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
      // New problem-solution pages
      'guides/debug-browser-errors-ai',
      'guides/frontend-error-tracking-realtime',
      'guides/chaos-engineering-frontend',
      'guides/accessibility-auditing-ai',
      'guides/css-layout-debugging-ai',
      'guides/visual-regression-testing-ai',
      'guides/responsive-design-testing-ai',
      'guides/mobile-testing-real-devices-ai',
      'guides/frontend-performance-monitoring-ai',
    ],
  },
  {
    type: 'category',
    label: 'Framework Guides',
    collapsed: true,
    items: [
      'guides/frameworks/next-js',
      'guides/frameworks/vite-react',
      'guides/frameworks/django-flask',
      'guides/frameworks/rails',
      'guides/frameworks/go-htmx',
    ],
  },
  {
    type: 'category',
    label: 'AI Tool Guides',
    collapsed: true,
    items: [
      'guides/ai-tools/claude-code',
      'guides/ai-tools/cursor',
      'guides/ai-tools/windsurf-copilot',
      'guides/ai-tools/openclaw-clawdbot',
      'guides/ai-tools/aider-gemini',
    ],
  },
  {
    type: 'category',
    label: 'Ecosystem',
    collapsed: true,
    items: [
      'guides/ecosystem/what-is-mcp',
      'guides/ecosystem/best-mcp-servers-web-dev',
    ],
  },
],
```

### Navbar Update

Add "Guides" tab pointing to useCasesSidebar (rename label from "Use Cases" to "Guides & Use Cases" for clarity).

### Footer Update

Add "Guides" column:
```ts
{
  title: 'Guides',
  items: [
    {label: 'What is MCP?', to: '/guides/ecosystem/what-is-mcp'},
    {label: 'agnt with Claude Code', to: '/guides/ai-tools/claude-code'},
    {label: 'agnt with Next.js', to: '/guides/frameworks/next-js'},
    {label: 'Debug Browser Errors with AI', to: '/guides/debug-browser-errors-ai'},
  ],
},
```

## 7. Implementation Order

Priority based on SEO value and effort:

### Phase 1: Infrastructure + Highest-Value Pages
1. docusaurus.config.ts SEO changes (metadata, structured data)
2. "What is MCP?" (captures ecosystem learners)
3. "agnt with Claude Code" (captures primary user base)
4. "How to Debug Browser Errors with AI" (captures broadest problem search)
5. "Using agnt with Next.js" (captures largest framework audience)
6. Sidebar and nav updates

### Phase 2: AI Tool + Framework Guides
7. "agnt with Cursor"
8. "agnt with Windsurf / Copilot"
9. "Using agnt with Vite + React"
10. "agnt with OpenClaw / ClawdBot"
11. "Using agnt with Django / Flask"
12. "agnt with Aider / Gemini CLI"
13. "Using agnt with Ruby on Rails"
14. "Using agnt with Go (Templ/HTMX)"

### Phase 3: Problem-Solution Pages
15. "Real-Time Frontend Error Tracking"
16. "Chaos Engineering for Frontend Apps"
17. "CSS Layout Debugging with AI"
18. "AI-Powered Accessibility Auditing"
19. "Visual Regression Testing with AI"
20. "Responsive Design Testing with AI"
21. "Mobile Testing on Real Devices with AI"
22. "Frontend Performance Monitoring with AI"

### Phase 4: Ecosystem
23. "Best MCP Servers for Web Development"

## 8. Content Guidelines

### Tone
- Technical but approachable
- Show, don't tell (code examples over claims)
- Honest about what agnt does and doesn't do
- No marketing fluff — developers detect and distrust it

### SEO Writing Rules
- Primary keyword in first 100 words
- H2s contain secondary keywords naturally
- Code examples use realistic, specific scenarios (not foo/bar)
- Meta descriptions are 150-160 characters, include primary keyword, end with value proposition
- Alt text on any images

### Page Length Targets
- Framework guides: 800-1200 words
- AI tool guides: 600-1000 words
- Problem-solution pages: 1000-1500 words
- Ecosystem pages: 1200-2000 words

## 9. Success Metrics

Track after deployment:
- Google Search Console: impressions and clicks by page
- Top queries driving traffic
- Pages with highest click-through rate
- Internal navigation flow (do guide readers visit API docs?)

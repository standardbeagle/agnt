---
title: "Using agnt with Vite + React - AI Browser Debugging"
description: "Set up agnt with Vite and React for real-time error capture, DOM inspection, and visual debugging with your AI coding agent."
keywords: [Vite, React, debugging, AI, MCP server, error tracking, HMR, component inspection]
sidebar_label: "Vite + React"
---

# Using agnt with Vite + React

Vite and React together create a fast development experience, but debugging fragments across multiple surfaces. Vite HMR failures appear in both the terminal and the browser overlay. React error boundaries swallow rendering exceptions, hiding the original stack trace behind fallback UI. Runtime errors scatter between the browser console and process output depending on whether they occur during hot module replacement or normal execution. agnt captures all of these in one unified view and delivers them directly to your AI coding agent.

## Quick Setup

Install the `agnt` binary and register it as an MCP server:

```bash
npm install -g @standardbeagle/agnt
claude mcp add agnt -s user -- agnt mcp
```

> Prefer the Claude Code plugin (skills + slash commands)? Run `/plugin marketplace add standardbeagle/standardbeagle-tools` then `/plugin install agnt@standardbeagle-tools`.

Then create an `.agnt.kdl` file in your Vite + React project root:

```kdl
scripts {
    dev {
        run "npm run dev"
        autostart true
        url-matchers "(Local|Network):\\s*{url}"
    }
}

proxies {
    app {
        script "dev"
        autostart true
    }
}
```

The `url-matchers` pattern matches Vite's startup output — lines like `Local: http://localhost:5173/` — and extracts the URL automatically. The proxy's `script "dev"` links it to the dev script so the target is auto-detected. No hardcoded port needed. If Vite picks a different port because 5173 is in use, agnt follows.

## What agnt Captures in Vite + React

| Error Type | Source | Example |
|------------|--------|---------|
| HMR failures | Browser JS + Process output | `[hmr] Failed to reload /src/App.tsx. This could be due to syntax errors` |
| React rendering errors | Browser JS | `Uncaught Error: Minified React error #418` in a component render |
| Error boundary catches | Browser JS | The underlying exception that triggered the boundary's `componentDidCatch` |
| Unhandled promise rejections | Browser JS | `Unhandled rejection: TypeError` from an async event handler |
| HTTP/fetch errors | Proxy HTTP logs | 500 from `/api/users` or CORS failures |
| Vite compile errors | Process output | `[vite] Internal server error: Transform failed` with file and line |
| TypeScript errors | Process output | `TS2339: Property 'foo' does not exist on type 'Bar'` |

Process output errors are captured by agnt's AlertScanner, which pattern-matches against Vite's stdout and stderr. Browser errors are captured by the injected `window.onerror` and `unhandledrejection` handlers. HTTP errors are logged by the reverse proxy.

## Debugging React Error Boundaries

React error boundaries catch rendering exceptions and display fallback UI. The problem: the original error disappears from the user's view. agnt captures the underlying exception before the boundary handles it.

```tsx
// src/components/UserProfile.tsx
function UserProfile({ userId }: { userId: string }) {
  const { data: user } = useSuspenseQuery({
    queryKey: ['user', userId],
    queryFn: () => fetchUser(userId),
  });

  // If user.settings is undefined, this throws during render
  return <div>{user.settings.theme.toUpperCase()}</div>;
}
```

The error boundary renders its fallback, and the user sees "Something went wrong." The original `TypeError: Cannot read properties of undefined (reading 'theme')` is captured by agnt:

```json
get_incidents {}
// → [error:browser_js] TypeError (1x, 3s ago)
//   Cannot read properties of undefined (reading 'theme')
//   → src/components/UserProfile.tsx:9:34
```

React's `StrictMode` double-renders components in development, which can cause duplicate errors. agnt deduplicates these automatically — you see the error once with a count, not twice.

## Debugging HMR Failures

When Vite's HMR fails, the error appears in two places: the process output shows the transform failure, and the browser shows a JS error from the HMR client. agnt captures both.

A typical scenario: you edit a component and introduce a syntax error.

```json
get_incidents {since: "10s"}
// → [error:build_fail] TRANSFORM ERROR (1x, 2s ago)
//   /src/components/Dashboard.tsx:15:8
//   Unexpected token (Note that you need plugins to import files
//   that are not JavaScript)
// → [error:browser_js] SyntaxError (1x, 2s ago)
//   [hmr] Failed to reload /src/components/Dashboard.tsx
```

When HMR fails entirely and Vite performs a full page reload, the browser-side HMR error clears but the process output error persists. Check both sources with `get_incidents {process_id: "dev"}` and `get_incidents {proxy_id: "app"}`.

## Vite Proxy Configuration

Vite has its own dev server proxy (`server.proxy` in `vite.config.ts`) for forwarding API requests to a backend. agnt's reverse proxy sits in front of the entire Vite dev server, including proxied routes.

```
Browser → agnt proxy (:14832) → Vite dev server (:5173) → backend API (:4000)
```

```ts
// vite.config.ts
export default defineConfig({
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:4000',
        changeOrigin: true,
      },
    },
  },
});
```

agnt's proxy captures the HTTP request and the response from Vite (which itself proxied to the backend). You see the full round-trip in `proxylog`:

```json
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api", status_codes: [400, 500]}
```

There is no conflict between the two proxies. agnt proxies to Vite's listen address, and Vite proxies onward to the backend. They operate at different layers.

## Component Inspection

agnt injects the `window.__devtool` API into every page served through the proxy. Use `proxy exec` to run diagnostics against your React components:

```json
// Inspect a specific component's rendered DOM
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('.user-profile')"}

// Check for layout overflows in a component container
proxy {action: "exec", id: "app", code: "window.__devtool.findOverflows('.dashboard-grid')"}

// Audit accessibility on a form component
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility({root: '.login-form'})"}

// Find elements rendered off-screen (common with conditional rendering bugs)
proxy {action: "exec", id: "app", code: "window.__devtool.findOffscreen()"}
```

## Vite + React-Specific Tips

**Check for errors after every file save.** Vite's fast rebuild means errors surface within milliseconds. A quick check after saving catches compile errors, HMR failures, and runtime exceptions:

```json
get_incidents {since: "10s"}
```

**Vite pre-bundling errors appear in process output only.** When Vite fails to pre-bundle a dependency (common with CJS-only packages), the error shows in the terminal but not the browser. Always check `get_incidents {process_id: "dev"}`.

**React Fast Refresh failures become browser JS errors.** When a component cannot be hot-reloaded (e.g., it exports non-component values alongside components), React Fast Refresh falls back to a full reload. The failure reason appears as a browser JS error that agnt captures.

**Duplicate errors from StrictMode are deduplicated.** React's development-mode double rendering can trigger the same error twice. agnt collapses these into a single entry with a count.

**Use `since` to scope to your current edit cycle.** Vite apps accumulate errors quickly during active development. Narrow the window to avoid noise from earlier edits:

```json
get_incidents {since: "30s", severity: ["critical", "error"]}
```

## Full Workflow Example

A real debugging session when a Vite + React dashboard page is broken:

```
AI: [get_incidents {}]

=== Incidents (3) === [inbox: crit=0 err=3 warn=0 info=0 new=3]

[error:build_fail] TRANSFORM ERROR (1x, 8s ago)
  id: 93707e62451962d0
  Could not resolve "./DashboardChart" from "src/pages/Dashboard.tsx"

[error:browser_js] TypeError (1x, 6s ago)
  id: c32ba962aedc9ca1
  Cannot read properties of undefined (reading 'map')
  at: src/pages/Dashboard.tsx:22:18

[error:browser_js] Error (1x, 6s ago)
  id: 1c9e825987520295
  React will try to recreate this component tree from scratch
  using the error boundary

You: "The dashboard page shows a blank white screen"
AI: There are three related errors. The root cause is a missing
    import — DashboardChart was likely renamed or moved. Vite
    failed to resolve the import, which means the module loaded
    with undefined exports. When Dashboard.tsx tried to call
    .map() on data that never loaded (because the component
    tree partially failed), it threw a TypeError. The error
    boundary caught the render failure and showed a blank
    fallback. Let me check where DashboardChart moved to...
AI: [proxy {action: "exec", id: "app",
     code: "window.__devtool.inspect('.dashboard-container')"}]
  → Element exists but contains only the error boundary fallback div

AI: Found it — DashboardChart.tsx was renamed to Chart.tsx in the
    last refactor. Updating the import...
```

Three errors from two sources, traced to a single moved file. The AI sees the transform failure, the runtime consequence, and the error boundary recovery in one `get_incidents` call, then uses component inspection to confirm the page state.

## See Also

- [Getting Started](/getting-started) -- Installation and project configuration
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- General error debugging workflow
- [get_incidents API Reference](/api/get_incidents) -- Full parameter and output documentation
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) -- Error monitoring patterns

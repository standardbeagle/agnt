---
title: "Using agnt with Next.js - AI-Powered Browser Debugging"
description: "Debug Next.js apps with AI. Capture SSR errors, hydration mismatches, and runtime exceptions automatically. Works with Claude Code, Cursor, and any MCP client."
keywords: [Next.js, debugging, AI, MCP server, SSR errors, hydration, Claude Code, error tracking]
sidebar_label: "Next.js"
---

# Using agnt with Next.js

Next.js applications present a unique debugging challenge. Errors can originate on the server (SSR, API routes, middleware), the client (hydration mismatches, runtime exceptions), or cascade across both. Traditional debugging means checking the terminal for server-side stack traces, the browser console for client errors, and the network tab for failed API calls — three separate views for what is often a single root cause. agnt captures all of these in one place and feeds them directly to your AI coding agent.

## Quick Setup

Install the `agnt` binary and register it as an MCP server:

```bash
npm install -g @standardbeagle/agnt
claude mcp add agnt -s user -- agnt mcp
```

> Prefer the Claude Code plugin (skills + slash commands)? Run `/plugin marketplace add standardbeagle/standardbeagle-tools` then `/plugin install agnt@standardbeagle-tools`.

Then create an `.agnt.kdl` file in your Next.js project root:

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

The `url-matchers` pattern matches Next.js's startup output format — lines like `- Local: http://localhost:3000` — and extracts the URL automatically. The proxy's `script "dev"` links it to the dev script so the proxy target is auto-detected from that output. No hardcoded port needed. If you change your Next.js dev port, agnt picks it up.

## What agnt Captures in Next.js

| Error Type | Source | Example |
|------------|--------|---------|
| SSR errors | Process output | `Error: Cannot read properties of null (reading 'id')` in getServerSideProps |
| API route errors | Process output + HTTP | 500 response from `/api/users` with stack trace |
| Hydration mismatches | Browser JS | `Warning: Text content did not match` |
| Client runtime errors | Browser JS | TypeError in event handlers, useEffect |
| Middleware errors | Process output | Edge runtime crashes |
| Build/compile errors | Process output | TypeScript errors, module resolution failures |

Process output errors are captured by agnt's AlertScanner, which pattern-matches against your dev server's stdout and stderr. Browser errors are captured by the injected `window.onerror` and `unhandledrejection` handlers. HTTP errors are logged by the reverse proxy.

## Debugging SSR Errors

Server-side rendering errors in Next.js only appear in terminal output. A common scenario: a `getServerSideProps` function queries the database, the query returns `null`, and the component destructures the result.

```tsx
// pages/users/[id].tsx
export async function getServerSideProps({ params }) {
  const user = await db.users.findUnique({ where: { id: params.id } });
  // user is null if not found — this crashes during SSR
  return { props: { name: user.name, email: user.email } };
}
```

The error appears in Next.js process output but never reaches the browser. agnt captures it:

```json
// Check process output for server-side errors
get_incidents {process_id: "dev"}

// Or check HTTP responses for 500s
get_incidents {proxy_id: "app", severity: ["critical", "error"]}
```

Both approaches surface the same root cause. The process output shows the stack trace from the SSR crash, while the proxy logs show the 500 response the client received.

## Debugging Hydration Mismatches

Hydration mismatches occur when the server-rendered HTML does not match what React produces on the client. They appear as browser warnings:

```json
get_incidents {}
// → [warning:browser_js] Warning: Text content did not match.
//   Server: "January 15, 2026" Client: "January 15, 2026" (different locale)
```

Common causes in Next.js:

- **Date formatting** without a stable locale — `new Date().toLocaleDateString()` produces different output on server vs. client.
- **Random IDs** generated during render — use `React.useId()` instead.
- **Browser-only APIs in render** — `window.innerWidth`, `navigator.language`, or `matchMedia` used directly in Server Components or during SSR.
- **`window.location` usage** — server has no location object, producing `undefined` or a fallback value.

These mismatches are silent failures. Users see a flash of incorrect content, and React recovers by re-rendering, but the mismatch often indicates a real bug. agnt surfaces them so the AI can fix the root cause.

## Debugging API Routes

API route failures show up in both the process output (server-side exception) and the proxy logs (HTTP 500):

```json
// See all API failures
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api", status_codes: [400, 401, 403, 500]}

// Full error context from all sources
get_incidents {since: "2m"}
```

The `get_incidents` output correlates the server-side stack trace with the HTTP response, giving the AI agent the complete picture: what the API route threw, what status code the client received, and what the client-side error handler did with the failure.

## App Router vs Pages Router

The error capture strategy depends on which Next.js router you use.

**App Router (app/ directory)**:
- Server Component errors appear in process output only — they execute on the server, and the client never runs that code.
- Client Component errors (files with `"use client"`) appear in browser JS errors.
- `error.tsx` boundaries catch rendering errors, but the original exception still shows in process output.
- Server Actions that throw appear in both process output and as HTTP errors.

**Pages Router (pages/ directory)**:
- `getServerSideProps` and `getStaticProps` errors appear in process output.
- Component errors after hydration appear in browser JS errors.
- `_error.tsx` catches both, but agnt captures the original exception before the error page renders.

**Both routers**:
- API route errors (both `app/api` and `pages/api`) are captured via HTTP proxy.
- Middleware errors appear exclusively in process output — middleware runs on the edge runtime, not in the browser.

## Next.js-Specific Tips

**Compilation errors are captured automatically.** Next.js outputs "compiled successfully" and "compiled with errors" to stdout. agnt's AlertScanner detects compilation failures and surfaces them through `get_incidents`.

**Fast Refresh errors show up as browser JS errors.** When a hot reload fails, React logs the error in the browser console. agnt captures it.

**Check for errors after saving a file:**

```json
get_incidents {since: "30s"}
```

This shows whether the hot reload introduced new errors — compile failures, runtime exceptions, or hydration mismatches — within the last 30 seconds.

**Middleware errors are invisible in the browser.** They only appear in process output because middleware runs on the edge runtime before the response reaches the client. Always check `get_incidents {process_id: "dev"}` when debugging request-level issues like authentication failures or redirect loops.

## Full Workflow Example

Here is what a real debugging session looks like when a Next.js page is broken:

```
AI: [get_incidents {}]

=== Incidents (3) === [inbox: crit=0 err=3 warn=0 info=0 new=3]

[error:build_fail] COMPILE ERROR (1x, 2s ago)
  id: 2bd4e282f596903d
  Type error: Property 'email' does not exist on type 'User'

[error:http_5xx] 500 Internal Server Error (1x, 5s ago)
  id: 224bd54a26c4d678
  GET /api/users/123 → "TypeError: Cannot read property 'email' of undefined"

[error:browser_js] TypeError (1x, 5s ago)
  id: 253d2c412d9419d4
  Cannot read properties of null (reading 'email')
  at: src/app/profile/page.tsx:28:15

You: "The user profile page is broken"
AI: There's a cascade of three related errors. The root cause is the
    User type definition — it's missing the 'email' field. This causes
    the TypeScript compile error, which means the API route at
    /api/users/123 fails at runtime trying to access user.email, and
    the client component crashes when it receives null instead of user
    data. Let me fix the type definition first...
```

Three errors, three different sources, one root cause. The AI sees the full picture in a single `get_incidents` call and traces the cascade from type definition to API failure to client crash.

## See Also

- [Getting Started](/getting-started) — Installation and project configuration
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) — General error debugging workflow
- [get_incidents API Reference](/api/get_incidents) — Full parameter and output documentation
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) — Error monitoring patterns

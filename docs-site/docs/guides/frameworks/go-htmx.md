---
title: "Using agnt with Go, Templ, and HTMX - AI Browser Debugging"
description: "Debug Go web applications using Templ and HTMX with AI. Capture server errors, HTMX swap failures, and template issues automatically."
keywords: [Go, HTMX, Templ, debugging, AI, MCP server, web development, server errors]
sidebar_label: "Go + HTMX"
---

# Using agnt with Go, Templ, and HTMX

Go web applications built with Templ and HTMX have a debugging profile unlike JavaScript frameworks. There are no source maps — stack traces reference `file.go:line` directly. Compile errors halt the server entirely. HTMX partial swaps fail silently when a target element is missing. Server-rendered HTML means most errors originate in process output, not the browser console. agnt captures Go compile errors, panics, HTMX swap failures, and HTTP errors in one unified view, feeding them directly to your AI coding agent.

## Quick Setup

Install agnt from the Claude Code marketplace:

```bash
claude mcp add agnt --plugin agnt@agnt-marketplace
```

Create `.agnt.kdl` in your project root. This example uses [air](https://github.com/air-verse/air) for live reloading:

```kdl
scripts {
    dev {
        run "air"
        autostart true
        url-matchers "(?:listening|serving|started).*{url}"
    }
}

proxies {
    app {
        script "dev"
        autostart true
    }
}
```

The `url-matchers` pattern matches typical Go server startup lines — `Listening on http://localhost:8080`, `Serving at http://127.0.0.1:3000` — and extracts the URL. The proxy links to the `dev` script so the target port is auto-detected. No hardcoded port needed.

If you run `templ generate --watch` separately, add a second script:

```kdl
scripts {
    templ {
        run "templ generate --watch"
        autostart true
    }
    dev {
        run "go run ./cmd/server"
        autostart true
        url-matchers "(?:listening|serving|started).*{url}"
    }
}

proxies {
    app {
        script "dev"
        autostart true
    }
}
```

## What agnt Captures

| Error Type | Source | Example |
|------------|--------|---------|
| Go compile errors | Process output | `./handlers/user.go:42:15: undefined: UserResponse` |
| Go panics | Process output | `panic: runtime error: index out of range [3] with length 2` |
| HTMX swap failures | Browser JS | `htmx:targetError - target with id #user-list not found` |
| HTTP 4xx/5xx | Proxy logs | `500 Internal Server Error` on `POST /api/users` |
| Template errors | Process output | `template execution error: nil pointer evaluating *models.User.Name` |
| Build failures (air) | Process output | `build failed` after file change |

Process output errors are captured by agnt's AlertScanner, which includes built-in Go patterns: `go-build-fail`, `go-panic`, and `go-test-fail`. Browser errors come from injected JavaScript handlers. HTTP errors are logged by the reverse proxy.

## Debugging Go Compile Errors

Go compile errors are multi-line. A type error produces output like:

```
# myapp/handlers
./handlers/user.go:42:15: undefined: UserResponse
./handlers/user.go:58:9: cannot use resp (variable of type map[string]any) as UserResponse value
```

agnt's AlertScanner detects the `build failed` line that air outputs after compilation. The full error context is available through `get_errors`:

```json
get_errors {process_id: "dev"}
```

Since Go has no source maps, file:line references point directly to your source code — the AI can open the exact location immediately. When air rebuilds successfully, old errors age out of the deduplication window (60 seconds) and stop appearing.

## Debugging HTMX

HTMX is designed to degrade gracefully — a swap targeting a missing element logs a console error instead of throwing. agnt's injected JavaScript captures these.

**Missing swap targets.** The HTMX response targets `#user-list` but the element does not exist:

```json
get_errors {}
// → [browser:js] htmx:targetError - target with id #user-list not found for hx-swap
```

**Out-of-band swap failures.** HTMX `hx-swap-oob="true"` elements silently disappear if their target ID is missing from the DOM. agnt captures the browser error.

**422 validation responses.** Go handlers commonly return `422 Unprocessable Entity` for validation failures:

```json
get_errors {proxy_id: "app"}
// → [proxy:http] 422 Unprocessable Entity
//   POST /contacts → "email: invalid format"
```

**Empty responses.** If a handler returns an empty body, HTMX swaps in nothing — the target goes blank. Check proxy logs for suspect 200 responses:

```json
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/contacts", status_codes: [200]}
```

## Templ Hot Reload Setup

[Templ](https://templ.guide) generates Go code from `.templ` files. There are two approaches to hot reload.

**Option 1: air handles everything.** Configure `.air.toml` to watch `.templ` files and run `templ generate` before building:

```toml
[build]
  cmd = "templ generate && go build -o ./tmp/main ./cmd/server"
  bin = "./tmp/main"
  include_ext = ["go", "templ", "css"]
  delay = 200
```

One `air` process handles template generation, compilation, and restart. The `.agnt.kdl` config from Quick Setup works as-is.

**Option 2: templ watch + air separately.** Run `templ generate --watch` in its own process. This is faster for large projects because templ only regenerates changed files:

```toml
[build]
  cmd = "go build -o ./tmp/main ./cmd/server"
  bin = "./tmp/main"
  include_ext = ["go", "css"]
  exclude_regex = ["_templ\\.go$"]
  delay = 500
```

Use the two-script `.agnt.kdl` from Quick Setup. The 500ms delay gives templ time to generate before air rebuilds.

## Go + HTMX-Specific Tips

**Panics produce goroutine stack traces.** `get_errors` reduces panic traces to the first application frame, skipping `runtime/`, `net/http`, and standard library frames. The AI sees the line in your code that panicked, not 20 lines of runtime internals.

**Use `get_errors` after saving a file:**

```json
get_errors {since: "30s"}
```

This shows whether the rebuild introduced compile errors, panics, or HTMX swap failures in the last 30 seconds.

**HTMX response headers matter.** If your handler returns HTML without `Content-Type: text/html`, HTMX may not swap the response. agnt's proxy logs include response headers, so the AI can spot incorrect content types.

**Template nil pointer errors look like panics.** If a Templ component receives a nil pointer, Go panics with `runtime error: invalid memory address or nil pointer dereference`. The AlertScanner captures this as a `go-panic` alert. The stack trace points to the generated `_templ.go` file, which maps back to your `.templ` source.

**Air build errors include full compiler output.** When `go build` fails, air prints compiler output followed by `build failed`. agnt captures the trigger, and the full output is available via `proc {action: "output", id: "dev"}`.

## Full Workflow Example

A debugging session with a Go + Templ + HTMX application:

```
You: "The contacts page is broken — clicking delete does nothing"

AI: [get_errors {}]
=== Errors (2) ===
[process:dev] GO PANIC (1x, 3s ago)
  runtime error: invalid memory address or nil pointer dereference
  → handlers/contacts.go:67
[browser:js] htmx:targetError (1x, 3s ago)
  target with id #contact-row-42 not found for hx-swap

AI: Two related errors. The DELETE handler at handlers/contacts.go:67
    panics because the contact lookup returns nil — the handler doesn't
    check for a missing record before accessing fields. The panic causes
    a 500 response, and the HTMX hx-swap-oob that was supposed to remove
    #contact-row-42 never arrives, so HTMX logs a target error.

    The fix: add a nil check after the database lookup in the delete
    handler and return 404 if the contact is not found. Let me update
    handlers/contacts.go...
```

Two errors, two sources, one root cause. The AI sees the full picture in a single `get_errors` call and traces the causal chain from panic to swap failure.

## See Also

- [Getting Started](/getting-started) — Installation and project configuration
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) — General error debugging workflow
- [get_errors API Reference](/api/get_errors) — Full parameter and output documentation
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) — Error monitoring patterns

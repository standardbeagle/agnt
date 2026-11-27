---
title: "Using agnt with Ruby on Rails - AI-Powered Web Debugging"
description: "Debug Rails applications with AI coding agents. Automatic error capture, request logging, and visual debugging for Rails development."
keywords: [Ruby on Rails, Rails, debugging, AI, MCP server, error tracking, ActionCable, Turbo]
sidebar_label: "Ruby on Rails"
---

# Using agnt with Ruby on Rails

Ruby on Rails applications combine server-rendered views, background jobs, and real-time features into a single framework — which means debugging errors requires watching multiple layers at once. A failed ActiveRecord query produces a 500 response with a detailed error page in development, a stack trace in the terminal, and possibly a broken Turbo frame on the client. ActionCable WebSocket failures only show up in the browser console. Asset pipeline errors appear in process output but manifest as missing styles. agnt captures errors from all of these sources and feeds them directly to your AI coding agent in a single unified view.

## Quick Setup

Install the `agnt` binary and register it as an MCP server:

```bash
npm install -g @standardbeagle/agnt
claude mcp add agnt -s user -- agnt mcp
```

> Prefer the Claude Code plugin (skills + slash commands)? Run `/plugin marketplace add standardbeagle/standardbeagle-tools` then `/plugin install agnt@standardbeagle-tools`.

Then create an `.agnt.kdl` file in your Rails project root:

```kdl
scripts {
    server {
        run "bin/rails server"
        autostart true
        url-matchers "Listening on\\s+{url}"
    }
}

proxies {
    app {
        script "server"
        autostart true
    }
}
```

The `url-matchers` pattern matches Puma's startup output — lines like `Listening on http://127.0.0.1:3000` — and extracts the URL automatically. The proxy links to the script via `script "server"` so the target is auto-detected from process output. No hardcoded port needed.

If your project uses a `Procfile.dev` with `foreman` or `bin/dev`, adjust the script:

```kdl
scripts {
    dev {
        run "bin/dev"
        autostart true
        url-matchers "Listening on\\s+{url}"
    }
}
```

## What agnt Captures in Rails

| Error Type | Source | Example |
|------------|--------|---------|
| ActiveRecord errors | Process output | `ActiveRecord::RecordNotFound` in a controller action |
| View rendering errors | Process output + HTTP | `ActionView::Template::Error` with 500 response |
| Routing errors | Process output + HTTP | `ActionController::RoutingError (No route matches)` |
| Turbo frame failures | Browser JS + HTTP | 422 response to a Turbo Stream request, missing `<turbo-frame>` |
| ActionCable errors | Browser JS | WebSocket disconnect, failed subscription |
| Asset pipeline errors | Process output | Sprockets or Propshaft compilation failures |
| Stimulus errors | Browser JS | Missing controller, undefined action |
| Background job failures | Process output | `ActiveJob::DeserializationError` in Sidekiq/Solid Queue output |

Process output errors are captured by agnt's AlertScanner, which pattern-matches against your Rails server's stdout and stderr. Browser errors are captured by the injected `window.onerror` and `unhandledrejection` handlers. HTTP errors are logged by the reverse proxy.

## Debugging Server-Side Errors

Rails development mode outputs detailed stack traces to the terminal for every exception. A common scenario: a controller loads a record that does not exist.

```ruby
# app/controllers/orders_controller.rb
def show
  @order = Order.find(params[:id]) # raises RecordNotFound
  @line_items = @order.line_items.includes(:product)
end
```

The terminal shows the full Ruby stack trace, and the browser gets Rails' detailed error page (in development) or a 500 response. agnt captures both:

```json
get_incidents {process_id: "server"}
// → [error:process_alert] ActiveRecord::RecordNotFound (1x, 3s ago)
//   Couldn't find Order with 'id'=999

get_incidents {proxy_id: "app", severity: ["critical", "error"]}
// → [error:http_5xx] 500 Internal Server Error (1x, 3s ago)
//   GET /orders/999
```

Rails also outputs the HTTP status for every request — lines like `Completed 500 Internal Server Error in 12ms`. The AlertScanner detects these generic error patterns automatically through the built-in `unhandled-exception` and HTTP status output.

## Debugging Turbo and Hotwire

Turbo Drive, Turbo Frames, and Turbo Streams introduce a class of errors that are easy to miss. A Turbo Frame request expects a response containing a matching `<turbo-frame>` tag. When the response is missing the frame — because the controller rendered the wrong template, or a validation failure returned a 422 without the expected frame wrapper — Turbo silently drops the response.

```ruby
# app/controllers/comments_controller.rb
def create
  @comment = @post.comments.build(comment_params)
  if @comment.save
    respond_to do |format|
      format.turbo_stream
      format.html { redirect_to @post }
    end
  else
    # Bug: renders without turbo_frame_tag wrapper
    render :new, status: :unprocessable_entity
  end
end
```

The browser shows no visible error, but agnt captures the failed request:

```json
get_incidents {since: "30s"}
// → [warning:http_4xx] 422 Unprocessable Entity (1x, 5s ago)
//   POST /posts/7/comments
// → [error:browser_js] Turbo error (1x, 5s ago)
//   Response has no matching <turbo-frame id="new_comment">
```

**Stimulus controller errors** also surface as browser JS errors. A typo in a `data-controller` name or a missing action method produces a clear error in agnt's output:

```json
get_incidents {}
// → [error:browser_js] Error (1x, 2s ago)
//   Error connecting Stimulus controller "commentz" (missing)
```

## Rails-Specific Tips

**Development mode error pages are captured automatically.** Rails renders detailed HTML error pages in development with the full backtrace, request parameters, and environment. agnt's proxy logs the HTTP 500 response, and the AlertScanner captures the stack trace from process output. You get the error from both angles.

**Watch for `Completed 500` in process output.** Rails logs every request completion with its status code. Lines like `Completed 500 Internal Server Error in 45ms` are matched by agnt's generic alert patterns, so server errors surface even without framework-specific matchers.

**ActionCable WebSocket connections pass through the proxy.** agnt reserves the `/__devtool_metrics` path for its own WebSocket, but all other WebSocket routes — including ActionCable's default `/cable` endpoint — are forwarded transparently. No special configuration needed.

**Asset pipeline errors appear in process output only.** Sprockets compilation failures or Propshaft missing asset errors produce terminal output but no browser error (the browser just gets a broken stylesheet link). Check process errors when styles or scripts fail to load:

```json
get_incidents {process_id: "server"}
```

**Use `bin/dev` for multi-process setups.** If your Rails app uses `Procfile.dev` (with CSS bundling, JS bundling, or Solid Queue), point the script at `bin/dev` instead of `bin/rails server`. All process output from foreman flows through the same script, so agnt captures errors from every process.

## Full Workflow Example

Here is what a real debugging session looks like when a Rails page is broken:

```
AI: [get_incidents {}]

=== Incidents (2) === [inbox: crit=0 err=2 warn=0 info=0 new=2]

[error:process_alert] NoMethodError (1x, 4s ago)
  id: c74433cddca65363
  undefined method 'quantity' for nil:NilClass
  at: app/views/orders/_line_item.html.erb:8

[error:http_5xx] 500 Internal Server Error (1x, 4s ago)
  id: f13ba0a4d81790e0
  GET /orders/42/line_items → "NoMethodError"

You: "The order page is showing a blank turbo frame instead of line items"
AI: The line_item partial at app/views/orders/_line_item.html.erb:8
    is calling .quantity on a nil object. This means one of the
    line items in the collection has a nil product association.
    The Turbo Frame for line items is blank because the server
    returned a 500 instead of the expected frame content.
    Let me fix the partial to handle the nil case and add an
    includes(:product) to the controller query to prevent N+1...
```

Two errors, two sources, one root cause. The AI sees the template error and the HTTP failure together, traces the issue to a missing association, and fixes both the immediate nil error and the underlying query.

## See Also

- [Getting Started](/getting-started) -- Installation and project configuration
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- General error debugging workflow
- [get_incidents API Reference](/api/get_incidents) -- Full parameter and output documentation
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) -- Error monitoring patterns

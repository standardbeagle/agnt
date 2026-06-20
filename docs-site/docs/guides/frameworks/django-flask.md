---
title: "Using agnt with Django and Flask - AI Browser Debugging for Python"
description: "Debug Django and Flask web applications with AI. Capture template errors, API failures, and frontend issues with agnt's MCP tools."
keywords: [Django, Flask, Python, debugging, AI, MCP server, template errors, API debugging]
sidebar_label: "Django & Flask"
---

# Using agnt with Django and Flask

Django and Flask applications present debugging challenges distinct from JavaScript frameworks. Template rendering errors, Python tracebacks, and API failures surface in the server process output — never in the browser console. A broken template produces a 500 response with a stack trace in the terminal, but the browser only sees an error page. agnt bridges this gap by capturing errors from process output, HTTP responses, and browser JavaScript simultaneously, feeding the complete picture to your AI coding agent.

## Quick Setup

Install the `agnt` binary and register it as an MCP server:

```bash
npm install -g @standardbeagle/agnt
claude mcp add agnt -s user -- agnt mcp
```

> Prefer the Claude Code plugin (skills + slash commands)? Run `/plugin marketplace add standardbeagle/standardbeagle-tools` then `/plugin install agnt@standardbeagle-tools`.

### Django Configuration

Create an `.agnt.kdl` file in your Django project root:

```kdl
scripts {
    dev {
        run "python manage.py runserver"
        autostart true
        url-matchers "http://\\{url\\}" "at\\s+{url}"
    }
}

proxies {
    app {
        script "dev"
        autostart true
    }
}
```

Django's `runserver` outputs `Starting development server at http://127.0.0.1:8000/`. The `url-matchers` patterns extract the URL so the proxy auto-detects the target port.

### Flask Configuration

```kdl
scripts {
    dev {
        run "flask run"
        autostart true
        url-matchers "Running on\\s+{url}"
    }
}

proxies {
    app {
        script "dev"
        autostart true
    }
}
```

Flask outputs `* Running on http://127.0.0.1:5000` at startup. If you use a custom port via `flask run --port 8080`, agnt picks up the change automatically.

## What agnt Captures

| Error Type | Source | Example |
|------------|--------|---------|
| Python tracebacks | Process output | `TemplateSyntaxError: Invalid block tag on line 5` |
| HTTP 500 responses | Proxy logs | `Internal Server Error` from `/api/users/` |
| Template rendering errors | Process output | `django.template.exceptions.TemplateDoesNotExist: profile.html` |
| Jinja2 template errors | Process output | `jinja2.exceptions.UndefinedError: 'user' is undefined` |
| DRF validation errors | Proxy HTTP | `{"email": ["This field is required."]}` with 400 status |
| Static file 404s | Proxy HTTP | `GET /static/css/main.css` returning 404 |
| JavaScript errors in templates | Browser JS | `TypeError` from inline scripts or included JS files |
| Database errors | Process output | `django.db.utils.OperationalError: no such table: auth_user` |

Process output errors are captured by agnt's AlertScanner, which pattern-matches against multi-line Python tracebacks. Browser JavaScript errors are captured by injected error handlers. HTTP errors are logged by the reverse proxy.

## Debugging Django

### Template Errors

Django template errors appear exclusively in process output. The browser receives a debug error page or a bare 500, but neither contains the structured traceback your AI agent needs.

```python
# templates/profile.html
<h1>{{ user.get_full_name }}</h1>
<p>{{ user.profile.bio }}</p>  {# crashes if profile doesn't exist #}
```

When `user.profile` raises `RelatedObjectDoesNotExist`, the traceback appears in `manage.py runserver` output. agnt captures it:

```json
get_errors {process_id: "dev"}
// → [process:dev] RelatedObjectDoesNotExist (1x, 3s ago)
//   User has no profile.
//   → myapp/views.py:42
```

`get_errors` reduces the full Python stack trace to the first frame inside your application code, filtering out Django internals. The AI sees `myapp/views.py:42` instead of 20 lines of middleware frames.

### Django REST Framework API Errors

DRF returns structured JSON error responses. agnt extracts error messages from the response body:

```json
get_errors {proxy_id: "app"}
// → [proxy:http] 400 Bad Request (1x, 2s ago)
//   POST /api/users/ → {"email": ["Enter a valid email address."], "password": ["This field may not be blank."]}
```

For server-side exceptions in DRF views:

```python
# api/views.py
class UserViewSet(viewsets.ModelViewSet):
    def perform_create(self, serializer):
        # Forgot to pass the request user
        serializer.save(created_by=self.request.user.profile)  # crashes if no profile
```

This produces both a process output traceback and a 500 HTTP response. `get_errors {}` returns both, correlated by time.

### Middleware Issues

Middleware errors silently break requests without any browser-visible error. Authentication, CORS, or custom middleware that raises an exception produces a traceback only in process output — always check `get_errors {process_id: "dev"}` when requests fail without a clear HTTP error.

## Debugging Flask

### Jinja2 Template Errors

Flask uses Jinja2 for templates. Like Django, template errors appear only in the process output:

```python
# templates/dashboard.html
{% for item in items %}
  <div>{{ item.name }} - {{ item.prce }}</div>  {# typo: 'prce' instead of 'price' #}
{% endfor %}
```

Jinja2 raises `UndefinedError` for the missing attribute. The browser shows the Werkzeug debugger, but the actual traceback is in the terminal:

```json
get_errors {process_id: "dev"}
// → [process:dev] UndefinedError (1x, 4s ago)
//   'dict object' has no attribute 'prce'
//   → app/routes.py:28
```

### Blueprint and WSGI Errors

Blueprint route errors surface the same way. Flask's Werkzeug server also captures WSGI-level exceptions — connection errors, request parsing failures, and `RequestEntityTooLarge` from malformed uploads all appear in process output.

```json
get_errors {since: "30s"}
// → [proxy:http] 404 Not Found (2x, 10s ago)
//   GET /admin/users/999
// → [process:dev] TemplateSyntaxError (1x, 5s ago)
//   Unexpected end of template. — admin/user.html line 47
```

## Python-Specific Tips

**Debug mode affects error visibility.** With `DEBUG=True` (Django) or `FLASK_DEBUG=1` (Flask), the browser shows detailed error pages. agnt captures the underlying traceback regardless of debug mode, so the AI always gets the structured error.

**Python tracebacks are multi-line.** A Python traceback spans 10-30 lines. agnt's AlertScanner accumulates continuation lines (`File "/path", line N` format) and reports the complete traceback as a single error entry.

**Use `get_errors` after saving a file:**

```json
get_errors {since: "30s"}
```

Both frameworks auto-reload on file changes. This shows whether the reload introduced new errors — import failures, syntax errors, or runtime exceptions.

**Static files work through the proxy transparently.** Django's `STATIC_URL` and Flask's `/static/` routes pass through agnt's reverse proxy without extra configuration. Static file 404s are captured as HTTP errors.

**`manage.py` commands besides `runserver`** can also be tracked. Run migrations or management commands through agnt's `run` tool and their tracebacks are captured the same way.

## Full Workflow Example

A realistic debugging session for a Django application with a broken API endpoint:

```
You: "The user registration endpoint returns a 500 error"

AI: [get_errors {}]
=== Errors (2) ===
[process:dev] IntegrityError (1x, 8s ago)
  UNIQUE constraint failed: auth_user.email
  → accounts/views.py:35
[proxy:http] 500 Internal Server Error (1x, 8s ago)
  POST /api/register/ → "IntegrityError at /api/register/"

AI: The registration endpoint crashes when a user tries to register
    with an email that already exists. The view at accounts/views.py
    line 35 calls User.objects.create_user() without first checking
    for duplicate emails. The fix is to either add a uniqueness
    check before creating the user, or catch the IntegrityError and
    return a 400 response with a clear message. Let me fix the view...
```

Two errors, two sources, one root cause. The process output shows the Python traceback with the exact application line, while the proxy logs confirm what the client received. The AI traces the problem without switching between terminal and browser.

## See Also

- [Getting Started](/getting-started) — Installation and project configuration
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) — General error debugging workflow
- [get_errors API Reference](/api/get_errors) — Full parameter and output documentation
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) — Error monitoring patterns

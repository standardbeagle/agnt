# Python Error Formats — Pattern/Parser Bank Reference (R4)

Survey of REAL, CURRENT error-message formats for CPython 3.11+ tracebacks,
Django, FastAPI/uvicorn/Starlette/Pydantic, and SQLAlchemy. Goal: stable RE2
regex anchors for the two banks in this repo, not guesses.

Banks targeted:
- Broad toast: `internal/overlay/alerts_defaults.go` (`AlertPattern`)
- Narrow structured: `internal/tools/build_error_parsers.go` (`BuildError`)

All regexes below are **RE2-compatible** (no backrefs, no lookaround). Versions
noted per example.

---

## 0. What Python coverage ALREADY exists (do not duplicate)

### Toast bank (`alerts_defaults.go`, category `python`)
| ID | Pattern | Sev |
|----|---------|-----|
| `python-traceback` | `Traceback \(most recent call last\)` | error |
| `python-syntax` | `SyntaxError:` | error |
| `python-module-not-found` | `ModuleNotFoundError:` | error |
| `python-import-error` | `^ImportError:` | error |
| `python-django-exception` | `django\.core\.exceptions` | error |
| `python-runtime-error` | `^RuntimeError:` | error |
| `python-werkzeug-error` | `werkzeug\.` | error |

Note: `python-runtime-error` is shared with Ruby (per the existing comment). The
generic `unhandled-exception`, `segfault`, `out-of-memory`, `addr-in-use`
(`EADDRINUSE`), `connection-refused` patterns also fire for Python processes.

### Structured bank (`build_error_parsers.go`)
- `pytestRe` = `^FAILED\s+([^\s:]+)::([^\s]+)(?:\s+-\s+(.+))?$` (Tool `pytest`).
  This is the ONLY Python-aware structured parser today. No generic traceback
  location parser exists.

**Gaps:** no toast pattern keys on the *exception-type last line* generally
(only the `Traceback` header + a handful of specific types); no structured
parser captures `File "...", line N, in func` location frames; no SQLAlchemy /
Pydantic / uvicorn / asyncio coverage at all.

---

## 1. Real examples (verbatim, ≥3 per area)

### 1a. Bare CPython tracebacks

**Example A — classic, single exception (3.11 / 3.12 / 3.13).** The exception
type + message is ALWAYS the LAST non-indented line; `File` lines carry
location.

```
Traceback (most recent call last):
  File "/app/main.py", line 10, in <module>
    result = compute(data)
             ^^^^^^^^^^^^^
  File "/app/calc.py", line 4, in compute
    return total / count
           ~~~~~~^~~~~~~
ZeroDivisionError: division by zero
```
Source — PEP 657 fine-grained locations (3.11+):
https://peps.python.org/pep-0657/ . The `^^^^` and `~~~^~~` caret/squiggle
lines under a source line are emitted on 3.11+ only.

**Example B — chained exception (`During handling …` / `The above …`).** Two
full tracebacks joined by a chaining sentence; each ends with its own type
line.

```
Traceback (most recent call last):
  File "/app/db.py", line 22, in get
    return cache[key]
           ~~~~~^^^^^
KeyError: 'user:42'

During handling of the above exception, another exception occurred:

Traceback (most recent call last):
  File "/app/main.py", line 8, in <module>
    user = get('user:42')
TypeError: 'NoneType' object is not subscriptable
```
The other connector is `The above exception was the direct cause of the
following exception:` (explicit `raise … from …`). Tutorial:
https://docs.python.org/3/tutorial/errors.html

**Example C — ExceptionGroup (PEP 654, 3.11+).** Group header uses `+ Exception
Group Traceback`, `|`-prefixed frame lines, and `----- N -----` dividers.
Verbatim from the official tutorial
(https://docs.python.org/3/tutorial/errors.html):

```
  + Exception Group Traceback (most recent call last):
  |   File "<stdin>", line 1, in <module>
  |     f()
  |     ~^^
  |   File "<stdin>", line 3, in f
  |     raise ExceptionGroup('there were problems', excs)
  | ExceptionGroup: there were problems (2 sub-exceptions)
  +-+---------------- 1 ----------------
    | OSError: error 1
    +---------------- 2 ----------------
    | SystemError: error 2
    +------------------------------------
```

3.13 note: collapsed recursive frames render as `...<12 lines>...` and
`add_note()` appends free-text note lines AFTER the type line (also verbatim
from the tutorial):

```
Traceback (most recent call last):
  File "<stdin>", line 2, in <module>
    raise TypeError('bad type')
TypeError: bad type
Add some information
Add some more information
```
The trailing notes are a parsing hazard — they are NOT a new exception line.

**Example D — SyntaxError carets (already partly covered).** SyntaxError
location is emitted on the `File` line + a caret line, then `SyntaxError: msg`:

```
  File "/app/bad.py", line 3
    def f(:
          ^
SyntaxError: invalid syntax
```

### 1b. Django

Django dev-server (`runserver`) logs request exceptions through the
`django.request` logger. The unhandled-500 log line + traceback (Django 4.2 /
5.x):

```
Internal Server Error: /accounts/profile/
Traceback (most recent call last):
  File "/venv/lib/python3.12/site-packages/django/core/handlers/exception.py", line 55, in inner
    response = get_response(request)
               ^^^^^^^^^^^^^^^^^^^^^
  File "/venv/lib/python3.12/site-packages/django/core/handlers/base.py", line 197, in _get_response
    response = wrapped_callback(request, *callback_args, **callback_kwargs)
  File "/app/accounts/views.py", line 14, in profile
    return render(request, "profile.html", {"u": user.name})
                                                 ^^^^^^^^^
AttributeError: 'NoneType' object has no attribute 'name'
```
Docs: https://docs.djangoproject.com/en/6.0/howto/error-reporting/ — "a stack
trace of the error will automatically be written to your Django application's
error log." The `Internal Server Error: <path>` header line precedes the
traceback; the logger name `django.request` appears when a logging formatter
includes `%(name)s`.

`django.core.exceptions` types (verbatim type lines, all subclass form
`django.core.exceptions.X`):

```
django.core.exceptions.ImproperlyConfigured: The SECRET_KEY setting must not be empty.
```
```
django.core.exceptions.ValidationError: ['Enter a valid email address.']
```
```
django.core.exceptions.ObjectDoesNotExist: User matching query does not exist.
```
ORM `DoesNotExist` is also emitted unqualified as `<Model>.DoesNotExist:` and
`<Model>.MultipleObjectsReturned:`. The migration-checker line:
```
django.db.utils.OperationalError: no such table: auth_user
```
Source: https://docs.djangoproject.com/en/6.0/ref/exceptions/

### 1c. FastAPI / uvicorn / Starlette / Pydantic

**Uvicorn default log format** is `LEVEL:` + padding-spaces + message (the
default `uvicorn.error` logger uses `levelprefix`, rendering `INFO:`,
`WARNING:`, `ERROR:`, `CRITICAL:` at column start). Startup sequence + failure:

```
INFO:     Will watch for changes in these directories: ['/app']
INFO:     Uvicorn running on http://127.0.0.1:8000 (Press CTRL+C to quit)
INFO:     Started reloader process [1] using WatchFiles
INFO:     Started server process [3]
INFO:     Waiting for application startup.
ERROR:    Traceback (most recent call last):
ERROR:    Application startup failed. Exiting.
```
Source: https://www.uvicorn.org/settings/ and
https://github.com/Kludex/uvicorn/issues/562 (logger name `uvicorn.error`).

Port-bind failure (CPython `OSError` surfaced by uvicorn):
```
ERROR:    [Errno 98] error while attempting to bind on address ('0.0.0.0', 8000): address already in use
```
(`address already in use` text; errno 98 Linux / 48 macOS / 10048 Windows.)

**Pydantic v2 validation error** (FastAPI request body / model construction).
Header is `N validation error[s] for <Model>`, then per-error: location path
line, indented message, indented `[type=…, input_value=…, input_type=…]`:

```
1 validation error for User
age
  Input should be a valid integer, unable to parse string as an integer [type=int_parsing, input_value='abc', input_type=str]
    For further information visit https://errors.pydantic.dev/2.11/v/int_parsing
```
Source: https://docs.pydantic.dev/latest/errors/validation_errors/

FastAPI `RequestValidationError` logged form (422; structured list, NOT a
traceback):
```
ERROR:    RequestValidationError: [{'type': 'int_parsing', 'loc': ('path', 'item_id'), 'msg': 'Input should be a valid integer, unable to parse string as an integer', 'input': 'foo', 'url': 'https://errors.pydantic.dev/2.5/v/int_parsing'}]
```
Source: https://github.com/fastapi/fastapi/discussions/6678 and
https://fastapi.tiangolo.com/tutorial/handling-errors/ . Note the JSON 422
response body is `{"detail":[{"loc":[...],"msg":"...","type":"..."}]}` — that is
HTTP payload, not stdout, and is out of scope for stdout scanning.

### 1d. SQLAlchemy (2.0)

SQLAlchemy wraps the DBAPI driver error in a `sqlalchemy.exc.*` subclass. The
type line is module-qualified, contains the wrapped driver error in
`(driver.module.ErrName)`, and is followed by `[SQL: …]`, `[parameters: …]`,
and a `(Background on this error at: https://sqlalche.me/e/<ver>/<code>)`
suffix.

**IntegrityError — unique violation (psycopg2 / Postgres):**
```
Traceback (most recent call last):
  ...
sqlalchemy.exc.IntegrityError: (psycopg2.errors.UniqueViolation) duplicate key value violates unique constraint "users_email_key"
DETAIL:  Key (email)=(a@b.com) already exists.

[SQL: INSERT INTO users (email, name) VALUES (%(email)s, %(name)s) RETURNING users.id]
[parameters: {'email': 'a@b.com', 'name': 'A'}]
(Background on this error at: https://sqlalche.me/e/20/gkpj)
```
Source: https://github.com/sqlalchemy/sqlalchemy/issues/5300 and
https://docs.sqlalchemy.org/en/20/core/exceptions.html

**OperationalError — connection dropped / cannot connect:**
```
sqlalchemy.exc.OperationalError: (psycopg2.OperationalError) could not connect to server: Connection refused
	Is the server running on host "db" (172.18.0.2) and accepting
	TCP/IP connections on port 5432?

(Background on this error at: https://sqlalche.me/e/20/e3q8)
```

**StatementError / DataError-style (bind param + nested type):**
```
sqlalchemy.exc.StatementError: (sqlalchemy.exc.InvalidRequestError) A value is required for bind parameter 'b', in parameter group 1
[SQL: INSERT INTO t (a, b, c) VALUES (?, ?, ?)]
[parameters: [{'a': 1, 'c': 3}]]
```
Source: https://docs.sqlalchemy.org/en/20/errors.html

The full `sqlalchemy.exc.*` set: `DBAPIError`, `OperationalError`,
`IntegrityError`, `DataError`, `ProgrammingError`, `InternalError`,
`NotSupportedError`, `InvalidRequestError`, `StatementError`,
`NoResultFound`, `MultipleResultsFound`, `PendingRollbackError`,
`TimeoutError`, `ResourceClosedError`, `InterfaceError`, `DisconnectionError`.

---

## 2. Stable regex anchors (RE2)

### The multi-line challenge
A traceback's TRUE error (`Type: message`) is the LAST unindented line; the
location is on one or more interior `File "...", line N, in func` lines. A
line-by-line scanner cannot know in advance which `File` line pairs with the
final type. Strategy: anchor independently on (a) the type line, (b) the File
location line, then let the parser associate the LAST-seen File line with the
NEXT-seen type line within the same traceback block (reset on blank line or new
`Traceback`/`+ Exception Group` header).

### Anchor regexes (validated against §1 text)

**A. Exception-type final line.** Matches dotted-or-bare type + `: message`.
Must allow module-qualified names (`sqlalchemy.exc.IntegrityError`,
`django.core.exceptions.ValidationError`) and bare builtins (`ZeroDivisionError`,
`KeyError`, `TypeError`). Anchor at line start; type ends in
`Error`/`Exception`/`Warning` OR is one of a known-builtin set that does not
(e.g. `KeyboardInterrupt`, `StopIteration`, `SystemExit`, `GeneratorExit`).

```
^([A-Za-z_][\w.]*(?:Error|Exception|Warning)):\s?(.*)$
```
Covers the vast majority (every example in §1 except the no-suffix builtins).
NOTE (verified): this does NOT match `ExceptionGroup: there were problems (2
sub-exceptions)` — `ExceptionGroup` ends in neither `Error`/`Exception`/`Warning`.
Anchor ExceptionGroup separately (see the `\+ Exception Group Traceback` start
marker, or add `ExceptionGroup`/`BaseExceptionGroup` to the suffix-less list
below).

For the suffix-less types, a second alternation (all verified to match):
```
^(KeyboardInterrupt|StopIteration|StopAsyncIteration|SystemExit|GeneratorExit|ExceptionGroup|BaseExceptionGroup):\s?(.*)$
```
(The real outliers are `KeyboardInterrupt`, `StopIteration`, `SystemExit`,
`GeneratorExit`, plus the two `*Group` types.)

**B. File location line.** Captures path, line number, and enclosing function.
Handles quoted path with spaces; func may be `<module>`, `<lambda>`, etc.
Optional leading `| ` for ExceptionGroup frames.

```
^\s*\|?\s*File "(.+?)", line (\d+)(?:, in (.+))?$
```
Validated against `  File "/app/main.py", line 10, in <module>` and the
`|   File "<stdin>", line 1, in <module>` group form.

**C. Uvicorn/logging level prefix.** Default uvicorn `levelprefix` and stdlib
`%(levelname)s:`:
```
^(?:\d{4}-\d{2}-\d{2}[ T][\d:,.]+\s+)?(?:[\w.]+\s+)?(ERROR|CRITICAL):\s
```
Simpler hot-path anchor for the bare uvicorn default (`ERROR:    msg`):
```
^(ERROR|CRITICAL):\s
```

**D. SQLAlchemy wrapped error.** The `sqlalchemy.exc.*` type line that embeds a
`(driver.Err)`:
```
^sqlalchemy\.exc\.(\w+): \((\w[\w.]*)\)\s?(.*)$
```
Validated against `sqlalchemy.exc.IntegrityError: (psycopg2.errors.UniqueViolation) duplicate key …`.

**E. Pydantic v2 detail line.** The `[type=…, input_value=…, input_type=…]`
tail uniquely identifies a pydantic error row:
```
\[type=([\w.]+),\s*input_value=(.+?),\s*input_type=(\w+)\]\s*$
```
And the header:
```
^(\d+) validation errors? for (\w+)\s*$
```

**Top 5 by value:** A (type line), B (File location), D (SQLAlchemy), C
(uvicorn ERROR), E (pydantic).

---

## 3. Signal-vs-noise field map

| Line | Role | Signal? |
|------|------|---------|
| `Traceback (most recent call last):` | block START marker | signal (already keyed) |
| `  + Exception Group Traceback (most recent call last):` | group START marker | signal |
| `  File "X", line N, in func` | LOCATION (file/line/func) | signal → File, Line |
| source echo line (`    result = compute(data)`) | context | noise |
| `             ^^^^^^^^^^^^^` / `~~~^~~` | PEP 657 column marker | noise (could derive Col) |
| `Type: message` (LAST unindented) | the actual error (type + msg) | **primary signal** |
| `During handling of the above exception…` / `The above exception was the direct cause…` | chain connector | noise (block separator) |
| `+-+---------------- 1 ----------------` | ExceptionGroup divider | noise (sub-exc separator) |
| free-text note lines after type (3.11 add_note) | annotation | noise — must NOT be parsed as a new type |
| `Internal Server Error: /path` | Django request prefix | signal (Django marker) |
| `django.request` (logger name) | framework prefix | signal (Django marker) |
| `INFO:` / `WARNING:` | uvicorn/log level | noise (info), filter out |
| `ERROR:` / `CRITICAL:` | uvicorn/log level | signal |
| `ERROR:    Application startup failed. Exiting.` | uvicorn fatal | signal |
| `[SQL: …]` | SQLAlchemy statement | signal-context (attach to msg, don't re-fire) |
| `[parameters: …]` | SQLAlchemy bind params | noise (may contain PII) |
| `(Background on this error at: https://sqlalche.me/…)` | SQLAlchemy doc link | noise (could derive Code from `/e/20/<code>`) |
| `    For further information visit https://errors.pydantic.dev/…` | pydantic doc link | noise |

---

## 4. Parser proposal

### 4a. Toast bank deltas — `internal/overlay/alerts_defaults.go`

Existing python patterns kept as-is (do NOT duplicate `python-traceback`,
`python-syntax`, `python-module-not-found`, `python-import-error`,
`python-django-exception`, `python-runtime-error`, `python-werkzeug-error`).

Proposed NEW `AlertPattern` entries (category `python` unless noted):

| ID | Pattern (RE2) | Severity | Description |
|----|---------------|----------|-------------|
| `python-exception-type` | `^[A-Za-z_][\w.]*(?:Error\|Exception):\s` | error | Generic exception type final line (catches uncovered `*Error`/`*Exception` types: TypeError, AttributeError, KeyError, ValueError, ConnectionError, etc.) |
| `python-exception-group` | `\+ Exception Group Traceback` | error | PEP 654 ExceptionGroup |
| `python-key-attr-error` (optional, if you prefer narrow) | `^(KeyError\|AttributeError\|TypeError\|ValueError\|IndexError\|AssertionError):` | error | Common builtins (redundant if `python-exception-type` added) |
| `python-sqlalchemy-error` | `^sqlalchemy\.exc\.\w+:` | error | SQLAlchemy DBAPI/ORM error |
| `python-pydantic-validation` | `\d+ validation errors? for ` | error | Pydantic v2 validation error |
| `python-django-request-500` | `^Internal Server Error: ` | error | Django dev-server 500 (category `python`) |
| `python-uvicorn-startup-failed` | `Application startup failed` | error | uvicorn ASGI startup failure |
| `python-asyncio-task-exception` | `Task exception was never retrieved` | warning | asyncio unretrieved task error |

Notes / deltas:
- `python-exception-type` is the highest-value add: today only specific named
  types fire. It is broad but low-noise because it anchors at line-start and
  requires the `Error:`/`Exception:` suffix-with-colon shape. It subsumes the
  optional `python-key-attr-error` row — pick ONE.
- Do NOT add a bare `^ERROR:\s` python pattern — it would collide with the
  generic/node banks and over-fire. Scope uvicorn via the `Application startup
  failed` text instead.
- `werkzeug.` already covers Flask; no Flask delta needed.

### 4b. Structured bank deltas — `internal/tools/build_error_parsers.go`

`pytestRe` stays. Propose a generic **traceback parser** that consumes a
`Traceback` block and emits ONE `BuildError{Tool:"python"}` keyed on the LAST
File frame (location) + the final type line (code/message).

New package-level regexes:
```go
// Block start markers (reset state).
pyTracebackStartRe = regexp.MustCompile(`^(?:\s*\+ Exception Group )?Traceback \(most recent call last\):$`)
// File "X", line N, in func  (leading `| ` allowed for ExceptionGroup frames)
pyFrameRe = regexp.MustCompile(`^\s*\|?\s*File "(.+?)", line (\d+)(?:, in (.+))?$`)
// Final type line: dotted/bare type ending in Error|Exception|Warning + msg.
pyExcTypeRe = regexp.MustCompile(`^([A-Za-z_][\w.]*(?:Error|Exception|Warning)):\s?(.*)$`)
// SQLAlchemy wrapped: capture sub-type as Code, driver err in msg.
pySqlAlchemyRe = regexp.MustCompile(`^sqlalchemy\.exc\.(\w+): \((\w[\w.]*)\)\s?(.*)$`)
```

Scanner behavior (block-stateful, like the existing jest/rust look-ahead but
buffering the LAST frame instead of looking forward):
1. On `pyTracebackStartRe` → enter block, clear `lastFile/lastLine/lastFunc`.
2. On `pyFrameRe` while in block → overwrite `lastFile/lastLine/lastFunc` (last
   frame wins = deepest call site).
3. On a chain connector (`During handling…` / `The above exception…`) or blank
   line followed by a new start → keep emitting; treat as new sub-block.
4. On `pySqlAlchemyRe` → emit `BuildError{Tool:"python", Severity:"error",
   File:lastFile, Line:lastLine, Code:m[1] /*IntegrityError*/, Message:"("+m[2]+") "+m[3]}`.
5. Else on `pyExcTypeRe` → emit `BuildError{Tool:"python", Severity:"error",
   File:lastFile, Line:lastLine, Code:m[1] /*exception type*/, Message:m[2]}`,
   exit block. Skip if no preceding frame AND it looks like an add_note line
   (i.e. only fire `pyExcTypeRe` while `inBlock` is true, so post-type notes
   don't re-trigger).

`Col` can optionally be derived from the PEP 657 caret line (count leading
spaces before first `^`) but is low-value; recommend leaving `Col` unset for v1.

Mapping to `BuildError`:
- `Tool` = `"python"`
- `Code` = exception type (e.g. `ZeroDivisionError`, or SQLAlchemy subtype
  `IntegrityError`) — reuses the existing `Code` field semantics (analogous to
  `TS2322`/`E0308`).
- `File`/`Line` = deepest `File "…", line N` frame.
- `Message` = text after `: ` on the type line (driver wrapper prefixed for
  SQLAlchemy).
- `Col` = unset (v1).

Compact render reuses `formatBuildErrorCompact` unchanged, e.g.:
`[python:error] /app/calc.py:4 — ZeroDivisionError: division by zero`
`[python:error] /app/models.py:30 — IntegrityError: (psycopg2.errors.UniqueViolation) duplicate key value violates unique constraint "users_email_key"`

Pydantic note: a dedicated structured parser for pydantic is OPTIONAL — its
multi-line `loc → msg → [type=…]` shape doesn't carry a source File/Line, so it
maps poorly to `BuildError`. Recommend toast-only coverage (§4a) for pydantic
and leaving the structured bank to tracebacks + pytest.

---

## Sources
- PEP 657 (fine-grained locations, 3.11+): https://peps.python.org/pep-0657/
- PEP 654 (Exception Groups): https://peps.python.org/pep-0654/
- CPython tutorial errors (ExceptionGroup/notes verbatim): https://docs.python.org/3/tutorial/errors.html
- traceback module: https://docs.python.org/3/library/traceback.html
- Django error reporting: https://docs.djangoproject.com/en/6.0/howto/error-reporting/
- Django exceptions ref: https://docs.djangoproject.com/en/6.0/ref/exceptions/
- Uvicorn settings / logger name: https://www.uvicorn.org/settings/ , https://github.com/Kludex/uvicorn/issues/562
- FastAPI error handling / 422: https://fastapi.tiangolo.com/tutorial/handling-errors/ , https://github.com/fastapi/fastapi/discussions/6678
- Pydantic v2 validation errors: https://docs.pydantic.dev/latest/errors/validation_errors/
- SQLAlchemy core exceptions: https://docs.sqlalchemy.org/en/20/core/exceptions.html
- SQLAlchemy error messages: https://docs.sqlalchemy.org/en/20/errors.html
- SQLAlchemy IntegrityError real output: https://github.com/sqlalchemy/sqlalchemy/issues/5300

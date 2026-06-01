# Error Formats: Prisma, TypeORM, Sequelize, Drizzle, pg, mysql2

Survey of REAL, CURRENT error-message console formats for Node ORMs and raw SQL
drivers, distilled into STABLE RE2 regex anchors for the agnt scanner banks.

This doc is the spec G3 codes from. All regexes are Go `regexp` (RE2 — no
backreferences, no lookaround). Every anchor below was traced against the
verbatim example text in §1.

**Versions surveyed (as of 2026-05):** Prisma `5.x`/`6.x` (P-codes stable
since 2.x), TypeORM `0.3.x`, Sequelize `v6`/`v7`, Drizzle `drizzle-orm > 0.44`
(DrizzleQueryError wrapper) + earlier raw-driver passthrough, `pg` (node-postgres
`8.x`), `mysql2` `3.x`.

---

## 1. Real examples (verbatim)

### 1.1 Prisma

**Prisma client errors are wrapped, not raw `Error:` lines.** Each error class
prints its name, a human message, then a JSON-ish tail with `code` /
`clientVersion` / `meta`. Query errors are prefixed with an
``Invalid `prisma.<model>.<op>()` invocation`` banner.

P2002 — unique constraint (Prisma 2.23.0; format unchanged through 6.x):

```
Invalid `prisma.user.create()` invocation:
Unique constraint failed on the fields: (`user_id`)
{"code":"P2002","clientVersion":"2.23.0","meta":{"target":["user_id"]}}
```
Source: https://github.com/prisma/prisma/discussions/19227

The thrown object stringifies/inspects as `PrismaClientKnownRequestError` with
`.code === "P2002"`. The class name shows in stack-trace and `instanceof`
contexts:

```
PrismaClientKnownRequestError:
Invalid `prisma.user.create()` invocation:
Unique constraint failed on the fields: (`email`)
    at Object.request (/app/node_modules/@prisma/client/runtime/library.js:121:5876)
  code: 'P2002',
  clientVersion: '5.x.x',
  meta: { target: [ 'email' ] }
```
Source: https://www.prisma.io/docs/orm/reference/error-reference

P2025 — record required but not found:

```
PrismaClientKnownRequestError:
An operation failed because it depends on one or more records that were required but not found. {cause}
  code: 'P2025',
  clientVersion: '5.x.x'
```
Source: https://www.prisma.io/docs/orm/reference/error-reference

P1001 — can't reach database server (init error + migration P3006 wrapper). The
P1001 message is the most distinctive multi-line signal:

```
Error: P1001: Can't reach database server at `localhost`:`3306`

Please make sure your database server is running at `localhost`:`3306`.
```
Source: https://github.com/prisma/prisma/discussions/21666

```
Error: P3006

Migration `20230101000000_init` failed to apply cleanly to the shadow database.
Error: Can't reach database server at `192.168.1.228:5432`
Please make sure your database server is running at `192.168.1.228:5432`.
```
Source: https://github.com/prisma/prisma/issues/27108 ,
https://github.com/prisma/prisma/discussions/25359

P1000 — authentication failed (template, verbatim from error reference):

```
Authentication failed against database server at `localhost`, the provided database credentials for `postgres` are not valid.
```
Source: https://www.prisma.io/docs/orm/reference/error-reference

PrismaClientValidationError — no `code`, distinct class name; carries the
``Invalid `prisma.*.*()` invocation`` banner too:

```
PrismaClientValidationError:
Invalid `prisma.user.create()` invocation:
Argument `email` is missing.
  clientVersion: '5.x.x'
```
Source: https://www.prisma.io/docs/orm/reference/error-reference

### 1.2 TypeORM (`QueryFailedError`, 0.3.x)

First line is always `QueryFailedError: <driver message>`. The driver message is
verbatim from the underlying driver (so it carries pg / mysql wording). The
object dump exposes `query`, `parameters`, `driverError`, and driver-specific
`code`.

Postgres syntax error:

```
QueryFailedError: syntax error at or near "WHERE"
    at PostgresQueryRunner.query (/app/src/driver/postgres/PostgresQueryRunner.ts:299:19)
    at processTicksAndRejections (node:internal/process/task_queues:95:5)
  query: 'SELECT ... LEFT JOIN "task" "User_tasks" ON  WHERE ("User"."username" = $1)',
  parameters: [ 'alice' ],
  driverError: error: syntax error at or near "WHERE"
  code: '42601'
```
Source: https://github.com/typeorm/typeorm/issues/9541

Postgres unique violation (driverError carries pg code 23505):

```
QueryFailedError: duplicate key value violates unique constraint "users_email_key"
  code: '23505',
  detail: 'Key (email)=(a@b.com) already exists.',
  table: 'users',
  constraint: 'users_email_key'
```
Source: https://drdroid.io/framework-diagnosis-knowledge/javascript-typeorm-queryfailederror

MySQL/MariaDB duplicate via TypeORM (driverError code is `ER_DUP_ENTRY`):

```
QueryFailedError: ER_DUP_ENTRY: Duplicate entry 'a@b.com' for key 'users.email'
  code: 'ER_DUP_ENTRY',
  errno: 1062,
  sqlState: '23000',
  sqlMessage: "Duplicate entry 'a@b.com' for key 'users.email'"
```
Source: https://drdroid.io/framework-diagnosis-knowledge/javascript-typeorm-queryfailederror

SQL Server invalid object:

```
QueryFailedError: Error: Invalid object name 'notable'.
```
Source: https://drdroid.io/framework-diagnosis-knowledge/javascript-typeorm-queryfailederror

### 1.3 Sequelize (v6 / v7)

Error class names are the discriminating signal (`error.name`). All extend
`SequelizeDatabaseError` / `SequelizeBaseError`; the first console line is
`<ClassName>: <message>`. Underlying driver error is on `.original` / `.parent`.

Unique constraint:

```
SequelizeUniqueConstraintError: doppelter Schlüsselwert verletzt Unique-Constraint »users_username_key«
    at Query.formatError (/app/node_modules/sequelize/lib/dialects/postgres/query.js:374:16)
```
Source: https://github.com/sequelize/sequelize/issues/10559

Generic database error (MySQL parse error):

```
SequelizeDatabaseError: ER_PARSE_ERROR: You have an error in your SQL syntax; check the manual...
    at Query.formatError (/app/node_modules/sequelize/lib/dialects/mysql/query.js:267:16)
```
Source: https://github.com/sequelize/sequelize/issues/7594

SQLite missing table:

```
SequelizeDatabaseError: SQLITE_ERROR: no such table: Users
```
Source: https://github.com/sequelize/sequelize-typescript/issues/874

Other stable class names (same `<Name>: <msg>` first-line shape):
`SequelizeValidationError`, `SequelizeForeignKeyConstraintError`,
`SequelizeConnectionRefusedError`, `SequelizeConnectionError`,
`SequelizeTimeoutError`.
Source: https://sequelize.org/docs/v6/core-concepts/validations-and-constraints/

### 1.4 Drizzle (`drizzle-orm > 0.44`: DrizzleQueryError wrapper)

Since 0.44 all driver exceptions are wrapped in `DrizzleQueryError`. The console
output is a multi-line block: a `Failed query:` line with the SQL, an optional
`params:` line, and a `cause:` line carrying the raw driver error (which itself
matches the pg / mysql2 anchors below).

Migration auth failure:

```
DrizzleQueryError: Failed query: CREATE SCHEMA IF NOT EXISTS "drizzle"
cause: error: password authentication failed for user "opencut"
  code: '28P01'
```
Source: https://github.com/OpenCut-app/OpenCut/issues/316

Query failure with params:

```
DrizzleQueryError: Failed query: select "id", "active", "created_date" from "roblox_account" where "roblox_account"."user_id" = $1
params: roblox_9206079786
cause: error: ...
```
Source: https://github.com/drizzle-team/drizzle-orm/issues/5024

Transaction abort:

```
DrizzleQueryError: ...
cause: error: current transaction is aborted, commands ignored until end of transaction block
```
Source: https://github.com/payloadcms/payload/issues/14576

**Pre-0.44 / raw passthrough:** Drizzle re-threw the driver error unchanged, so
output looked like the bare pg / mysql2 examples in §1.5 — covered by those
anchors.

### 1.5 Raw drivers

**node-postgres (`pg` 8.x).** First line is `error: <message>` (lowercase
`error:`), followed by a stack into `Parser`/`pg-pool`, then object fields
including `code` (5-char SQLSTATE), `severity`, `detail`, `position`, `file`,
`line`, `routine`.

```
error: relation "users" does not exist
    at Parser.parseErrorMessage (/app/node_modules/pg-protocol/dist/parser.js:287:98)
    at processTicksAndRejections (node:internal/process/task_queues:95:5)
  length: 106,
  severity: 'ERROR',
  code: '42P01',
  position: '15',
  file: 'parse_relation.c',
  routine: 'parserOpenTable'
```
Source: https://github.com/brianc/node-postgres/issues/3259

Other common SQLSTATEs seen verbatim as `error: ...` + `code: 'XXXXX'`:
`23505` (unique_violation, `duplicate key value violates unique constraint ...`),
`23503` (foreign_key_violation), `42601` (syntax_error), `28P01`
(invalid_password, `password authentication failed for user "..."`), `3D000`
(invalid database), `ECONNREFUSED` (TCP, not a SQLSTATE — bare driver error).

**mysql2 (3.x).** Thrown `Error` whose first line is
`Error: <CODE>: <sqlMessage>` where CODE is the `ER_*` name. Object fields:
`code` (`ER_*` string), `errno` (int), `sqlState` (5-char), `sqlMessage`.

```
Error: ER_DUP_ENTRY: Duplicate entry 'a@b.com' for key 'users.email'
    at Packet.asError (/app/node_modules/mysql2/lib/packets/packet.js:728:17)
  code: 'ER_DUP_ENTRY',
  errno: 1062,
  sqlState: '23000',
  sqlMessage: "Duplicate entry 'a@b.com' for key 'users.email'"
```
Source: https://www.javascriptroom.com/blog/how-to-handle-error-er-dup-entry-duplicate-entry-in-nodejs/ ,
https://github.com/strapi/strapi/issues/15681

Other common `ER_*` codes: `ER_NO_SUCH_TABLE` (1146), `ER_PARSE_ERROR` (1064),
`ER_BAD_FIELD_ERROR` (1054), `ER_ACCESS_DENIED_ERROR` (1045),
`ER_NO_REFERENCED_ROW_2` (1452), `ER_LOCK_DEADLOCK` (1213). Connection failures
surface as `ECONNREFUSED` / `PROTOCOL_CONNECTION_LOST` instead of `ER_*`.

---

## 2. Stable regex anchors (RE2)

Anchored on structural signals (class names, code shapes, fixed banner text),
not fragile message wording. Each traced against §1 examples.

| Tool / form | Anchor regex (Go RE2) | Matches |
|---|---|---|
| Prisma known-request class | `PrismaClientKnownRequestError` | class-name line |
| Prisma validation class | `PrismaClientValidationError` | class-name line |
| Prisma init class | `PrismaClientInitializationError` | class-name line |
| Prisma invocation banner | `` Invalid `prisma\.[\w$]+\.[\w$]+\(\) ` invocation `` → see note | banner line |
| Prisma P-code (object tail) | `` [{,"\s]code["']?:\s*["']?P\d{4} `` | `code: 'P2002'` and `{"code":"P2002"...}` (matches both single-quote and JSON double-quote forms) |
| Prisma P-code (Error: prefix) | `^Error: P\d{4}\b` | `Error: P3006`, `Error: P1001: ...` |
| Prisma unreachable DB | `Can't reach database server at` | P1001 / P3006 body |
| Prisma auth failed | `Authentication failed against database server` | P1000 |
| Prisma unique | `Unique constraint failed on the` | P2002 body |
| TypeORM query failed | `^QueryFailedError:` | first line |
| Sequelize any error | `^Sequelize[A-Z]\w+(?:Error):` | first line, all subclasses |
| Sequelize DB error (narrow) | `^SequelizeDatabaseError:` | DB-level only |
| Sequelize unique (narrow) | `^SequelizeUniqueConstraintError:` | unique only |
| Drizzle wrapper | `^DrizzleQueryError:` | first line |
| Drizzle failed-query body | `Failed query:` | SQL body line |
| pg error first line | `^error:\s` | lowercase `error: ` (pg-specific) |
| pg SQLSTATE | `` [{,"\s]code["']?:\s*["']?[0-9A-Z]{5} `` | `code: '42P01'` |
| mysql2 ER first line | `^Error: ER_[A-Z0-9_]+:` | `Error: ER_DUP_ENTRY: ...` |
| mysql2 code field | `` [{,"\s]code["']?:\s*["']?ER_[A-Z0-9_]+ `` | `code: 'ER_DUP_ENTRY'` |

Notes / RE2-safety:
- The Prisma invocation-banner anchor contains a backtick and parens. As a Go
  literal: ``regexp.MustCompile("Invalid `prisma\\.[\\w$]+\\.[\\w$]+\\(\\) ` invocation")``.
  Cheaper/sufficient alternative used in the proposals below: the plain substring
  `` Invalid `prisma. `` via `regexp.QuoteMeta`-style literal. No lookaround needed.
- SQLSTATE `[0-9A-Z]{5}` deliberately also matches `ER_DUP...`? No — `ER_DUP_ENTRY`
  is longer than 5 and contains `_`, so `{5}` with an anchored field colon will not
  false-match mysql codes. Keep pg SQLSTATE keyed off the lowercase `error:` first
  line to disambiguate from mysql2's `Error: ER_` first line.
- `^Sequelize[A-Z]\w+(?:Error):` matches `SequelizeDatabaseError:`,
  `SequelizeUniqueConstraintError:`, `SequelizeConnectionRefusedError:`,
  `SequelizeValidationError:` etc. (`\w` covers the CamelCase tail). RE2-safe.
- All `code:` field anchors use the leading char-class form `[{,"\s]code["']?:\s*["']?`
  so they match BOTH JSON (`{"code":"P2002"`) and util.inspect (`code: 'P2002'`)
  renderings. (Earlier `` `?code`?: `` form was fixed — it silently missed the JSON
  double-quote tail.) This matches the §4.B `prisma_codeRe` char class exactly.

---

## 3. Signal-vs-noise field map

| Tool | Real-message line | Decoration / stack | file:line location | Error code / cause |
|---|---|---|---|---|
| Prisma (query) | line after invocation banner (`Unique constraint failed...`, `Argument X is missing`) | `at Object.request (.../library.js:...)` frames; the banner itself is semi-noise | **none in message** — Prisma points at the model.op, not source file:line | `code: 'PXXXX'` in object tail; `meta.target` = offending fields |
| Prisma (CLI/migrate) | `Error: PXXXX` line + following prose | blank lines, `Please make sure...` filler | `Migration <name>` (logical, not file) | code = `PXXXX` on the `Error:` line |
| TypeORM | text after `QueryFailedError:` (driver verbatim) | `at PostgresQueryRunner.query (...)`, `processTicksAndRejections` | **none** — TypeORM has no source file:line in the message (known limitation, issue #10820) | driver code on `driverError`/`code` field (pg SQLSTATE or `ER_*`); `query`/`parameters` carry SQL context |
| Sequelize | text after `Sequelize*Error:` | `at Query.formatError (.../dialects/*/query.js:...)` | none in message | underlying code on `.original`/`.parent`; constraint name embedded in message |
| Drizzle | `cause:` line (the real driver error) | `Failed query:` SQL + `params:` are context, not the failure; outer `DrizzleQueryError:` is the wrapper | none in message | `code:` under the `cause` (pg SQLSTATE / mysql `ER_*`) |
| pg | text after lowercase `error: ` | `at Parser.parseErrorMessage`, `at processTicksAndRejections`, `pg-pool/index.js` | `file`/`line` fields = **PG C source** (e.g. `parse_relation.c`), NOT user code — treat as noise | `code:` = 5-char SQLSTATE; `detail`/`hint`/`position`/`constraint` = context |
| mysql2 | text after `Error: ER_...:` (== `sqlMessage`) | `at Packet.asError (.../mysql2/lib/packets/packet.js:...)` | none | `code` = `ER_*`, `errno` int, `sqlState` 5-char |

**Cross-cutting noise rule:** for every tool, lines matching the stack frame
shape `^\s+at\s` are decoration. ORM-internal paths
(`node_modules/(@prisma|typeorm|sequelize|drizzle-orm|pg|pg-protocol|mysql2)/`)
never carry the user's bug location — surface the message line + code, drop the
frames.

---

## 4. Parser proposals

### 4.A Broad toast bank — `internal/overlay/alerts_defaults.go`

Shape: `AlertPattern{ID, Pattern, Severity, Category, Description}`. New
`Category: "orm"` and `Category: "sql"`. These are single-line boolean
classifiers for toast surfacing — keep them broad.

**Overlap check against existing patterns:** the existing `node-error`
(`^Error:`) would ALSO match the mysql2 first line and Prisma CLI `Error: PXXXX`
lines. That is acceptable (it already classifies them as errors), but the
proposed ORM/SQL patterns add *category + specific description* which the generic
`^Error:` cannot. No proposed pattern duplicates an existing ID or identical
regex. `connection-refused` already covers `ECONNREFUSED` for all drivers — **do
not** add an ORM-specific connection-refused toast.

Proposed deltas (12 patterns):

```go
// Prisma
{ID: "prisma-known-request", Pattern: regexp.MustCompile(`PrismaClientKnownRequestError`),
 Severity: AlertSeverityError, Category: "orm", Description: "Prisma known request error"},
{ID: "prisma-validation", Pattern: regexp.MustCompile(`PrismaClientValidationError`),
 Severity: AlertSeverityError, Category: "orm", Description: "Prisma validation error"},
{ID: "prisma-init", Pattern: regexp.MustCompile(`PrismaClientInitializationError`),
 Severity: AlertSeverityError, Category: "orm", Description: "Prisma initialization error"},
{ID: "prisma-code", Pattern: regexp.MustCompile(`(?:^Error: )?\bP\d{4}\b`),
 Severity: AlertSeverityError, Category: "orm", Description: "Prisma error code (PXXXX)"},
{ID: "prisma-unreachable-db", Pattern: regexp.MustCompile(`Can't reach database server at`),
 Severity: AlertSeverityError, Category: "orm", Description: "Prisma cannot reach database"},

// TypeORM
{ID: "typeorm-query-failed", Pattern: regexp.MustCompile(`^QueryFailedError:`),
 Severity: AlertSeverityError, Category: "orm", Description: "TypeORM query failed"},

// Sequelize (one broad pattern covers every subclass)
{ID: "sequelize-error", Pattern: regexp.MustCompile(`^Sequelize[A-Z]\w+Error:`),
 Severity: AlertSeverityError, Category: "orm", Description: "Sequelize ORM error"},

// Drizzle
{ID: "drizzle-query-error", Pattern: regexp.MustCompile(`^DrizzleQueryError:`),
 Severity: AlertSeverityError, Category: "orm", Description: "Drizzle query error"},

// Raw drivers
{ID: "pg-error", Pattern: regexp.MustCompile(`^error: .+`),
 Severity: AlertSeverityError, Category: "sql", Description: "node-postgres driver error"},
{ID: "sql-sqlstate", Pattern: regexp.MustCompile(`\bcode: '[0-9A-Z]{5}'`),
 Severity: AlertSeverityError, Category: "sql", Description: "SQL SQLSTATE error code"},
{ID: "mysql2-er-error", Pattern: regexp.MustCompile(`^Error: ER_[A-Z0-9_]+:`),
 Severity: AlertSeverityError, Category: "sql", Description: "mysql2 driver ER_ error"},
{ID: "mysql2-er-code", Pattern: regexp.MustCompile(`\bcode: 'ER_[A-Z0-9_]+'`),
 Severity: AlertSeverityError, Category: "sql", Description: "mysql2 ER_ error code"},
```

Caveat on `pg-error` (`^error: .+`): lowercase `error:` at line start is fairly
pg-specific, but generic enough to risk a false positive on tools that print a
lowercase `error:`. It is gated by the toast surface's existing
batching/dedup, and the SQLSTATE companion (`sql-sqlstate`) gives a
high-precision confirmation. If false positives are a concern in review, tighten
to `^error: .+\b(does not exist|violates|syntax error|duplicate key|authentication failed)\b` —
but that trades the stable structural anchor for fragile wording, so the broad
form is recommended.

### 4.B Narrow structured bank — `internal/tools/build_error_parsers.go`

Shape: `BuildError{Tool, Severity, File, Line, Col, Code, Message, RawLine}`.
Naming convention `<tool>_<form>Re`. Add a new `Tool` value per source. These
ORMs/drivers do **not** emit user `file:line:col` in the message (see §3), so
`File`/`Line`/`Col` stay empty — the value is `Tool` + `Code` + clean `Message`,
which is exactly what the compact formatter (`[tool:sev] — code: message`)
renders well even without a location.

**Overlap check:** no existing parser (`tsc*`, `eslint*`, `vite*`, `webpack*`,
`goCompile`, `rust*`, `pytest`, `jest*`, `goTest*`) shares a first-line shape
with these. `^QueryFailedError:`, `^Sequelize...Error:`, `^DrizzleQueryError:`,
`^error:`, `^Error: ER_` are all distinct prefixes. No collisions; safe to append.

Proposed regex bank entries (single-line forms — capture group order documented
inline):

```go
// Prisma: object-tail code OR `Error: PXXXX` CLI form. Message is the
// preceding human line; captured at parse time from the prior non-stack line.
// Group: 1=code
prisma_codeRe = regexp.MustCompile(`(?:^Error: |[{,"\s]code["']?:\s*["']?)(P\d{4})\b`)

// TypeORM: "QueryFailedError: <message>". Group: 1=message
typeorm_headerRe = regexp.MustCompile(`^QueryFailedError:\s+(.+)$`)

// Sequelize: "<SequelizeXxxError>: <message>". Groups: 1=class, 2=message
sequelize_headerRe = regexp.MustCompile(`^(Sequelize[A-Z]\w+Error):\s+(.+)$`)

// Drizzle wrapper header. Group: 1=failed SQL (context)
drizzle_headerRe = regexp.MustCompile(`^DrizzleQueryError:\s+Failed query:\s+(.+)$`)
// Drizzle cause line (the real error). Group: 1=cause message
drizzle_causeRe  = regexp.MustCompile(`^\s*cause:\s+(.+)$`)

// pg: "error: <message>" header. Group: 1=message
pg_headerRe = regexp.MustCompile(`^error:\s+(.+)$`)
// pg SQLSTATE field. Group: 1=5-char code
pg_codeRe   = regexp.MustCompile(`\bcode:\s*'([0-9A-Z]{5})'`)

// mysql2: "Error: ER_XXX: <sqlMessage>". Groups: 1=code, 2=message
mysql2_headerRe = regexp.MustCompile(`^Error:\s+(ER_[A-Z0-9_]+):\s+(.+)$`)
```

Suggested `Tool` values + parse semantics (mirrors existing multi-line
header→detail handling like `rustHeaderRe`/`rustLocationRe`):

| Tool | Header regex | Code source | Message source | Multi-line? |
|---|---|---|---|---|
| `prisma` | `prisma_codeRe` (or known-request class line) | group 1 (`PXXXX`) | preceding non-stack line (banner+1) | look-back 1–2 lines for message |
| `typeorm` | `typeorm_headerRe` | scan look-ahead ≤8 lines for `pg_codeRe`/`mysql2`-style `code:` | group 1 | optional code from object dump |
| `sequelize` | `sequelize_headerRe` | n/a (in `.original`, rarely printed) | group 2 | single line |
| `drizzle` | `drizzle_headerRe` → `drizzle_causeRe` | from `cause:` via `pg_codeRe`/`mysql2` code | `cause:` group 1 (real error) | yes: header → params? → cause |
| `pg` | `pg_headerRe` → `pg_codeRe` | look-ahead ≤8 lines for `pg_codeRe` | group 1 | yes: message line → object fields |
| `mysql2` | `mysql2_headerRe` | group 1 (`ER_*`) | group 2 (== sqlMessage) | single line |

**Stack-frame guard (shared, recommended):** when folding look-ahead/look-back
lines, skip lines matching `^\s+at\s` and ORM-internal `node_modules/...` paths
so the parser keys on the message + object fields, never the decoration. This
mirrors the jest parser's tolerant look-ahead loop.

Minimal coverage: the 8 regexes above cover all §1 examples across 6 tools. The
two raw-driver patterns (`pg_*`, `mysql2_*`) double as the resolver for
TypeORM/Drizzle/Sequelize `code`/`cause` extraction, so the marginal cost of the
ORM wrappers is just their header regex.

---

## Sources

- Prisma error reference: https://www.prisma.io/docs/orm/reference/error-reference
- Prisma exception handling: https://www.prisma.io/docs/orm/prisma-client/debugging-and-troubleshooting/handling-exceptions-and-errors
- Prisma P2002 console output: https://github.com/prisma/prisma/discussions/19227
- Prisma P1001: https://github.com/prisma/prisma/discussions/21666
- Prisma P3006 shadow DB: https://github.com/prisma/prisma/issues/27108 , https://github.com/prisma/prisma/discussions/25359
- TypeORM QueryFailedError syntax: https://github.com/typeorm/typeorm/issues/9541
- TypeORM no file:line limitation: https://github.com/typeorm/typeorm/issues/10820
- TypeORM error field reference: https://drdroid.io/framework-diagnosis-knowledge/javascript-typeorm-queryfailederror
- Sequelize unique constraint: https://github.com/sequelize/sequelize/issues/10559
- Sequelize parse error: https://github.com/sequelize/sequelize/issues/7594
- Sequelize SQLite no-table: https://github.com/sequelize/sequelize-typescript/issues/874
- Sequelize validations/constraints: https://sequelize.org/docs/v6/core-concepts/validations-and-constraints/
- Drizzle DrizzleQueryError goodies: https://orm.drizzle.team/docs/goodies
- Drizzle migrate auth failure: https://github.com/OpenCut-app/OpenCut/issues/316
- Drizzle failed query params: https://github.com/drizzle-team/drizzle-orm/issues/5024
- Drizzle transaction abort: https://github.com/payloadcms/payload/issues/14576
- node-postgres 42P01 console: https://github.com/brianc/node-postgres/issues/3259
- mysql2 ER_DUP_ENTRY handling: https://www.javascriptroom.com/blog/how-to-handle-error-er-dup-entry-duplicate-entry-in-nodejs/
- mysql2 ER_DUP_ENTRY in Strapi: https://github.com/strapi/strapi/issues/15681
</content>
</invoke>

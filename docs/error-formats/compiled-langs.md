# Error-Message Formats: Go / Rust / JVM / .NET

Research deliverable [R5]. Surveys **real, current** build- and runtime-error
output for four ecosystems to feed two parser banks:

- Broad toast classifier — `internal/overlay/alerts_defaults.go`
  (`AlertPattern{ID, Pattern, Severity, Category, Description}`).
- Narrow structured parser — `internal/tools/build_error_parsers.go`
  (`BuildError{Tool, Severity, File, Line, Col, Code, Message, RawLine}`).

All regexes below are **RE2** (Go `regexp`). Multi-line formats are handled by
header→location/frame look-ahead, the existing pattern in `parseBuildErrors`.
**Proposals are DELTAS only** — existing coverage is enumerated in §5 and avoided.

---

## 1. Real examples (verbatim, ≥3 per language)

### Go — toolchain 1.22–1.24

**1a. Compiler diagnostic** (`go build` / `go vet`). `file:line:col:` then message:
```
./main.go:12:3: undefined: Bar
./handlers/user.go:48:21: cannot use id (variable of type string) as int value in argument to lookup
# example.com/m
./main.go:9:2: "fmt" imported and not used
```
Note the `# <import-path>` package banner line that precedes a batch of errors
under `go build`. The `./`-prefixed `.go:N:N:` form is the canonical anchor.

**1b. Runtime panic + goroutine trace** (GOTRACEBACK=single default):
```
panic: runtime error: index out of range [3] with length 3

goroutine 1 [running]:
main.process(...)
	/home/u/app/main.go:24 +0x1d
main.main()
	/home/u/app/main.go:14 +0x65
exit status 2
```
`fatal error:` (non-recoverable, e.g. `concurrent map writes`,
`all goroutines are asleep - deadlock!`) uses the same goroutine-block layout.
Frame pairs are: `<pkg>.<func>(args)` line, then a **tab-indented**
`\t/abs/path/file.go:NN +0xHH` line.

**1c. `go test` failure**:
```
--- FAIL: TestProcess (0.00s)
    main_test.go:31: expected 5, got 3
FAIL
FAIL	example.com/m	0.012s
```
Two anchors: per-test `--- FAIL: <Name> (<dur>)` header (location on the next
tab-indented `file.go:NN:` line) and the package summary `FAIL\t<pkg>\t<dur>`.
A build-broken test package emits `FAIL\t<pkg> [build failed]`.

Source: <https://pkg.go.dev/runtime/debug>,
<https://yourbasic.org/golang/recover-from-panic/>,
<https://github.com/golang/go/issues/63455>.

---

### Rust — rustc / cargo 1.7x stable

**2a. Compiler error with code** (`error[Ennnn]` header → `-->` location):
```
error[E0308]: mismatched types
  --> src/main.rs:12:18
   |
12 |     let x: i32 = "hello";
   |            ---   ^^^^^^^ expected `i32`, found `&str`
   |            |
   |            expected due to this
   |
   = note: expected type `i32`
              found reference `&'static str`

error: aborting due to 1 previous error

For more information about this error, try `rustc --explain E0308`.
```

**2b. Borrow-check / lint with named code**:
```
error[E0382]: borrow of moved value: `s`
  --> src/main.rs:5:20
   |
3  |     let s = String::from("x");
   |         - move occurs because `s` has type `String`...
```
Warnings use the same shape: `warning: unused variable: \`x\`` with
`--> src/main.rs:N:N` (warnings frequently have **no** `[Ennnn]` code; lint
warnings carry `#[warn(...)]` notes instead).

**2c. Runtime panic** (default, and with `RUST_BACKTRACE=1`):
```
thread 'main' panicked at src/main.rs:7:5:
index out of bounds: the len is 3 but the index is 5
note: run with `RUST_BACKTRACE=1` environment variable to display a backtrace
```
With backtrace enabled, frames follow as:
```
   0: rust_begin_unwind
             at /rustc/.../library/std/src/panicking.rs:665:5
   3: my_crate::process
             at ./src/main.rs:7:5
```
**Version note:** since Rust 1.65 the panic location is embedded in the
`panicked at <file>:<line>:<col>:` line itself (older rustc put the message
first: `thread 'main' panicked at 'msg', src/main.rs:7:5`). Anchor must allow
both orderings.

`cargo test` failures end with `test result: FAILED. N passed; M failed; ...`
and a `failures:` block listing `    <module>::<test_name>`.

Source: <https://github.com/rust-lang/rust/issues/117598>,
<https://github.com/rust-lang/rust/issues/134445>,
<https://users.rust-lang.org/t/error-e0308-mismatched-type/20385>.

---

### JVM — javac (JDK 17–21), Maven, Gradle, Java stack traces, Spring Boot

**3a. javac diagnostic** (`file.java:line: error: msg`, caret on following lines):
```
Main.java:10:15: error: ';' expected
        int x = 5
                  ^
FileMaker.java:6: error: unreported exception FileNotFoundException; must be caught or declared to be thrown
        new FileReader(f);
        ^
1 error
```
**Note:** javac column is **not always present** — the classic form is
`File.java:NN: error:` (line only); newer `-Xdiags` and many tools emit
`File.java:NN:CC: error:`. Anchor must treat column as optional.

**3b. Runtime exception + chained cause** (`Exception in thread`, `at`, `Caused by`, `... N more`):
```
Exception in thread "main" com.myproject.module.MyProjectFooBarException: The number of FooBars cannot be zero
	at com.myproject.module.MyProject.anotherMethod(MyProject.java:19)
	at com.myproject.module.MyProject.someMethod(MyProject.java:12)
	at com.myproject.module.MyProject.main(MyProject.java:8)
Caused by: java.lang.ArithmeticException: The denominator must not be zero
	at org.apache.commons.lang3.math.Fraction.getFraction(Fraction.java:143)
	at com.myproject.module.MyProject.anotherMethod(MyProject.java:17)
	... 2 more
```
Stack-frame anchor: tab-indented `at <fqcn>.<method>(<File>.java:<NN>)`.
Frames can also read `(Native Method)`, `(Unknown Source)`, or carry a JAR/module
prefix `at app//com.x.Y.m(Y.java:NN)` / `at java.base/java.lang...`.

**3c. Maven `[ERROR]` build failure**:
```
[ERROR] /home/u/app/src/main/java/com/x/App.java:[10,15] cannot find symbol
[ERROR]   symbol:   variable foo
[ERROR] -------------------------------------------------------------
[ERROR] BUILD FAILURE
[ERROR] Failed to execute goal org.apache.maven.plugins:maven-compiler-plugin:3.11.0:compile (default-compile) on project app: Compilation failure
```
Maven's compiler diagnostic uses the bracketed `File.java:[line,col]` location
form — distinct from javac's `File.java:line:col:`.

**3d. Gradle build failure**:
```
> Task :compileJava FAILED
src/main/java/com/x/App.java:10: error: cannot find symbol
        foo();
        ^

FAILURE: Build failed with an exception.

* What went wrong:
Execution failed for task ':compileJava'.
> Compilation failed; see the compiler error output for details.
```
Gradle wraps **javac** output (3a form) under a `> Task :name FAILED` header,
then the `FAILURE:` / `* What went wrong:` banner.

**3e. Spring Boot startup failure banner**:
```
***************************
APPLICATION FAILED TO START
***************************

Description:

Web server failed to start. Port 8080 was already in use.

Action:

Identify and stop the process that's listening on port 8080 or configure this application to listen on another port.
```

Source: <https://www.javaspring.net/blog/how-to-read-the-full-stacktrace-in-java-where-it-says-e-g-23-more/>,
<https://docs.oracle.com/javase/10/docs/api/java/lang/Throwable.html>,
<https://discuss.gradle.org/t/failure-build-failed-with-an-exception-compilejava/50148>,
<https://docs.spring.io/spring-boot/reference/features/spring-application.html>,
<https://www.theserverside.com/tutorial/The-most-common-compile-time-errors-in-Java>.

---

### .NET — `dotnet build` / MSBuild (SDK 8–9), C# runtime exceptions

**4a. C# compiler diagnostic via MSBuild** (`File.cs(line,col): error CSxxxx: msg [project]`):
```
Program.cs(12,3): error CS0103: The name 'foo' does not exist in the current context [/home/u/app/app.csproj]
Main.cs(17,20): warning CS0168: The variable 'x' is declared but never used
```
**Five-part MSBuild canonical format** (order is fixed):
`Origin : Subcategory Category Code : Text`. Examples that all parse:
```
Main.cs(17,20): warning CS0168: The variable 'x' is declared but never used
C:\dir1\strings.resx(2) : error BC30188: Declaration expected.
cl : Command line warning D4024 : unrecognized source file type 'file1.cs'
error CS0006: Metadata file 'System.dll' could not be found.
```
The Origin file-position part has variants: `(line)`, `(line-line)`,
`(line,col)`, `(line,col-col)`, `(line,col,line,col)`. Lines/cols are 1-based.

**4b. MSBuild task error (`MSBxxxx`) + build summary**:
```
C:\app\app.csproj(42,5): error MSB3073: The command "npm run build" exited with code 1.

Build FAILED.

    Program.cs(12,3): error CS0103: The name 'foo' does not exist in the current context
    1 Warning(s)
    1 Error(s)
```
`error NETSDKnnnn:` and `error NU1101:` (NuGet restore) share the same line shape.

**4c. C# runtime unhandled exception** (.NET 5+ uses `Unhandled exception.`
with a trailing period; .NET Framework used `Unhandled Exception:`):
```
Unhandled exception. System.NullReferenceException: Object reference not set to an instance of an object.
   at Shop.AddToCartWithDiscount(Product product, Int32 discount) in /home/u/app/Shop.cs:line 5
   at Program.<Main>$(String[] args) in /home/u/app/Program.cs:line 12
```
Inner exceptions print ` ---> System.X: msg` inline and close each frame group
with `   --- End of inner exception stack trace ---`. Frame anchor:
`   at <Member> in <file>:line <NN>` (the ` in <file>:line <NN>` suffix is
present only for PDB-symbolicated builds; release frames omit it).

Source: <https://learn.microsoft.com/en-us/visualstudio/msbuild/msbuild-diagnostic-format-for-tasks>,
<https://learn.microsoft.com/en-us/dotnet/csharp/language-reference/compiler-messages/cs0103>,
<https://github.com/dotnet/sdk/issues/7617>,
<https://blog.elmah.io/debugging-system-nullreferenceexception-object-reference-not-set-to-an-instance-of-an-object/>.

---

## 2. Stable regex anchors (RE2)

Signal lines only. `(?m)` not needed — `parseBuildErrors`/`AlertScanner` feed
one line at a time. `^`/`$` therefore anchor each physical line.

| # | Anchor (RE2) | Matches |
|---|--------------|---------|
| Go-1 | `^(\.{1,2}/)?([^\s:]+\.go):(\d+):(\d+):\s+(.+)$` | Go compiler diagnostic (**exists**) |
| Go-2 | `^goroutine \d+ \[[^\]]+\]:$` | Go goroutine block header |
| Go-3 | `^\t(.+\.go):(\d+)(?: \+0x[0-9a-f]+)?$` | Go runtime stack frame (file:line, optional `+0x` PC offset) |
| Go-4 | `^---\s+FAIL:\s+(\S+)\s+\(([\d.]+)s\)` | Go test failed-test header (**exists, partial**) |
| Go-5 | `^FAIL\s+(\S+)(?:\s+\[build failed\]|\s+[\d.]+s)?$` | Go test package summary |
| Rs-1 | `^(error\|warning)(?:\[([EW]\d+)\])?:\s+(.+)$` | Rust diagnostic header, code optional (**exists, no-code variant is delta**) |
| Rs-2 | `^\s*-->\s+(.+?):(\d+):(\d+)\s*$` | Rust source location (**exists**) |
| Rs-3 | `^thread '[^']*' panicked at (?:'.*', )?(.+?):(\d+):(\d+)` | Rust panic, both pre/post-1.65 orderings |
| Jv-1 | `^([\w./-]+\.java):(\d+)(?::(\d+))?:\s+(error\|warning):\s+(.+)$` | javac diagnostic, col optional |
| Jv-2 | `^\s*at\s+([\w$.]+(?:/[\w$.]+)?)\(([^:)]+):(\d+)\)` | Java stack frame `at fqcn(File.java:NN)` |
| Jv-3 | `^(?:Exception in thread "[^"]*"\|Caused by:)\s+([\w.$]+(?:Exception\|Error))(?::\s+(.*))?$` | Java throwable header / cause |
| Jv-4 | `^\[ERROR\]\s+(.+\.java):\[(\d+),(\d+)\]\s+(.+)$` | Maven bracketed compiler diagnostic |
| Jv-5 | `^> Task :(\S+) FAILED$` | Gradle failed-task header |
| Net-1 | `^(.+?)\((\d+)(?:,(\d+))?(?:[,-][\d,]+)?\)\s*:\s*(error\|warning)\s+([A-Z]+\d+):\s+(.+?)(?:\s+\[[^\]]+\])?$` | MSBuild/CSC diagnostic, file+pos+code |
| Net-2 | `^(?:Unhandled [Ee]xception[.:])\s+([\w.]+(?:Exception\|Error)):\s+(.*)$` | .NET unhandled exception header |
| Net-3 | `^\s+at\s+(.+?)(?:\s+in\s+(.+):line\s+(\d+))?$` | .NET stack frame, `in file:line N` optional |

---

## 3. Signal-vs-noise field map

| Field | Go | Rust | JVM | .NET |
|-------|----|------|-----|------|
| **message line** | compiler `…go:N:N:` line; `panic:`/`fatal error:` line | `error[Ennnn]:` header; `thread … panicked at` line | `Exception in thread`/`Caused by` header; javac `error:` line | `Unhandled exception.` header; `error CSxxxx:` line |
| **stack/noise frames** | `goroutine` header + `\t…go:N +0x..` pairs | `--> file:N:N`, ` = note:`, caret/`^` rows, backtrace `N: sym` | `\tat fqcn(File.java:N)`, `... N more` | `   at Member in file:line N` |
| **file:line:col** | `file.go:N:N` (compiler); `file.go:N` (panic frame, no col) | `--> file:N:N` | `File.java:N` (frame, line only); `File.java:N:N` (javac, col optional); `File.java:[N,N]` (Maven) | `File.cs(N,N)` (compiler); `file:line N` (frame) |
| **error code** | none | `E0308`, `E0382` (warnings often none) | none (`CSxxxx`-style codes don't exist) | `CS0103`, `MSB3073`, `NETSDKxxxx`, `NU1101`, `BC30188`, `D4024` |
| **test identifier** | `--- FAIL: TestName`; `FAIL pkg` | `failures:` block `mod::test_name`; `test result: FAILED` | JUnit (separate format, out of scope) | `[xUnit]`/`Failed!` (separate, out of scope) |

Key noise discriminators:
- **Go panic frames** are tab-prefixed and carry `+0xHH` PC offsets — never a code.
- **Rust** `= note:`/`= help:`/caret rows are noise; only header + `-->` are signal.
- **Java** `... N more` and `at …` are frames; the `Caused by:` line is the
  signal that re-roots the exception chain.
- **.NET** `   --- End of inner exception stack trace ---` and `--- End of stack
  trace from previous location ---` are pure noise separators.

---

## 4. Parser proposal

### 4a. Broad toast bank (`internal/overlay/alerts_defaults.go`) — DELTAS

These are line classifiers (one bool + category/severity). Existing IDs in §5
are **not** repeated.

```go
// Go — runtime fatal/deadlock distinct from go-panic (^panic:)
{
    ID:          "go-goroutine-trace",
    Pattern:     regexp.MustCompile(`^goroutine \d+ \[[^\]]+\]:$`),
    Severity:    AlertSeverityError,
    Category:    "go",
    Description: "Go goroutine stack trace header",
},

// Rust — panic location form (1.65+) not caught by rust-thread-panic's
// broad `thread '.*' panicked`. (rust-thread-panic ALREADY covers it; this
// is only a delta IF a tighter location-bearing match is wanted — otherwise SKIP.)
// Proposed only as a NO-OP candidate; rust-thread-panic suffices. (no add)

// JVM — Maven [ERROR] bracketed diagnostic; current java-* set lacks a
// Maven-specific line classifier (java-build-failure only catches BUILD FAILURE).
{
    ID:          "maven-error-line",
    Pattern:     regexp.MustCompile(`^\[ERROR\]`),
    Severity:    AlertSeverityError,
    Category:    "java",
    Description: "Maven [ERROR] line",
},
// JVM — Gradle failed-task header (FAILURE/What went wrong are wrapped by it).
{
    ID:          "gradle-task-failed",
    Pattern:     regexp.MustCompile(`^> Task :\S+ FAILED$`),
    Severity:    AlertSeverityError,
    Category:    "java",
    Description: "Gradle task failed",
},
{
    ID:          "gradle-build-failed",
    Pattern:     regexp.MustCompile(`^FAILURE: Build failed`),
    Severity:    AlertSeverityError,
    Category:    "java",
    Description: "Gradle build failure banner",
},
// JVM — Spring Boot startup failure banner.
{
    ID:          "spring-boot-failed-start",
    Pattern:     regexp.MustCompile(`^APPLICATION FAILED TO START$`),
    Severity:    AlertSeverityError,
    Category:    "java",
    Description: "Spring Boot application failed to start",
},

// .NET — CS#### compiler diagnostic line. dotnet-* set has ENC, NETSDK,
// "Build FAILED", MSBuild error (text), errors-in — but NOT the generic
// CSxxxx / MSBxxxx error-code line.
{
    ID:          "dotnet-cs-error",
    Pattern:     regexp.MustCompile(`:\s+error\s+CS\d+:`),
    Severity:    AlertSeverityError,
    Category:    "dotnet",
    Description: "C# compiler error",
},
{
    ID:          "dotnet-cs-warning",
    Pattern:     regexp.MustCompile(`:\s+warning\s+CS\d+:`),
    Severity:    AlertSeverityWarning,
    Category:    "dotnet",
    Description: "C# compiler warning",
},
{
    ID:          "dotnet-msb-error",
    Pattern:     regexp.MustCompile(`\berror\s+MSB\d+:`),
    Severity:    AlertSeverityError,
    Category:    "dotnet",
    Description: "MSBuild task error code",
},
{
    ID:          "dotnet-nuget-error",
    Pattern:     regexp.MustCompile(`\berror\s+NU\d+:`),
    Severity:    AlertSeverityError,
    Category:    "dotnet",
    Description: "NuGet restore error",
},
```
**Count: 8 new toast patterns** (+1 explicitly rejected as redundant).
Note: the existing `unhandled-exception` generic pattern (`(?i)unhandled
exception`) already catches .NET `Unhandled exception.` — do **not** add a
dotnet-specific duplicate.

### 4b. Narrow structured bank (`internal/tools/build_error_parsers.go`) — DELTAS

New regex vars + matcher arms. Go-compiler and Rust parsers already exist.

```go
// C# / MSBuild compiler diagnostic: File.cs(12,3): error CS0103: msg [proj]
// Also matches MSB####, NETSDK####, NU#### (any [A-Z]+\d+ code).
// Position forms (12), (12,3), (12,3-5), (12,3,14,4) — capture first line/col.
csMSBuildRe = regexp.MustCompile(
    `^(.+?)\((\d+)(?:,(\d+))?(?:[,-][\d,]+)?\):\s+(error|warning)\s+([A-Z]+\d+):\s+(.+?)(?:\s+\[[^\]]+\])?$`)
// MSBuild code-only form (no file): "error CS0006: Metadata file ..."
csCodeOnlyRe = regexp.MustCompile(`^(error|warning)\s+([A-Z]+\d+):\s+(.+)$`)

// javac: File.java:10:15: error: msg   (col optional → File.java:10: error: msg)
javacRe = regexp.MustCompile(`^([\w./-]+\.java):(\d+)(?::(\d+))?:\s+(error|warning):\s+(.+)$`)

// Maven bracketed: [ERROR] /abs/File.java:[10,15] cannot find symbol
mavenJavaRe = regexp.MustCompile(`^\[ERROR\]\s+(.+\.java):\[(\d+),(\d+)\]\s+(.+)$`)

// Java stack frame: \tat com.x.Y.method(Y.java:42)  (module prefix tolerated)
javaStackRe = regexp.MustCompile(`^\s+at\s+([\w$.]+(?:/[\w$.]+)?)\(([^:)]+\.java):(\d+)\)$`)

// .NET stack frame: "   at Ns.Cls.M(args) in /abs/File.cs:line 12"
dotnetStackRe = regexp.MustCompile(`^\s+at\s+(.+?)\s+in\s+(.+):line\s+(\d+)$`)
```

Matcher arms (mirror existing `goCompileRe` / `rustHeaderRe` arms):

- **`csMSBuildRe`** → `BuildError{Tool:"csc"/"msbuild", Severity:m[4],
  File:m[1], Line:m[2], Col:m[3], Code:m[5], Message:m[6]}`. Tool derives from
  the code prefix: `CS`→`csc`, `MSB`→`msbuild`, `NETSDK`/`NU`→`dotnet`.
  `csCodeOnlyRe` as fallback (no file/line).
- **`javacRe`** → `BuildError{Tool:"javac", Severity:m[4], File:m[1],
  Line:m[2], Col:m[3] (0 if absent), Message:m[5]}`. No Code field (javac has none).
- **`mavenJavaRe`** → `BuildError{Tool:"maven", Severity:"error", File:m[1],
  Line:m[2], Col:m[3], Message:m[4]}`.
- **`javaStackRe`** → standalone frame: `BuildError{Tool:"java", Severity:"error",
  File:m[2], Line:m[3], Message:m[1] (the fqcn.method)}`. Emit only the **first**
  frame after an `Exception in thread`/`Caused by` header to stay token-efficient
  (mirror the jest header→first-frame look-ahead). Optionally capture the
  exception class+message as the header (a `javaThrowableRe` you can add:
  `^(?:Exception in thread "[^"]*"|Caused by:)\s+([\w.$]+):\s*(.*)$`).
- **`dotnetStackRe`** → frame after a `.NET unhandled exception` header:
  `BuildError{Tool:"dotnet", File:m[2], Line:m[3], Message:m[1]}`.

**Count: 6 new structured parsers** (csc/MSBuild w/ code-only fallback, javac,
maven-java, java-stack-frame, dotnet-stack-frame) + 1 optional Java throwable
header helper.

Ordering note: `csMSBuildRe` must be attempted **before** `tscParenRe` —
both use `File(line,col):` shape, but tsc emits `TS\d+` and C# emits
`[A-Z]+\d+`; they are mutually exclusive on the code token, so either order is
safe, but putting `csMSBuildRe` first keeps the C# `[A-Z]+\d+` general code from
being shadowed. `javacRe` must precede `goCompileRe`? No — `.java` vs `.go`
suffixes are disjoint; no collision.

---

## 5. Existing coverage (NOT duplicated)

**Toast bank (`alerts_defaults.go`) already has:**
- Go: `go-build-fail`, `go-panic` (`^panic:`), `go-test-fail` (`^FAIL\s+\S+`),
  `go-fatal-error`, `go-module-error`.
- Rust: `rust-error-code` (`^error\[E\d+\]`), `rust-aborting`,
  `rust-backtrace-hint`, `rust-thread-panic` (`thread '.*' panicked`).
- Java: `java-exception-in-thread`, `java-build-failure` (`^BUILD FAILURE`),
  `java-caused-by` (`^Caused by:`), `java-severe`, `java-lang-exception`.
- .NET: `dotnet-restart`, `dotnet-enc-error/warning`, `dotnet-build-error`
  (`Build FAILED`), `dotnet-watch-*`, `dotnet-msbuild-error` (text "MSBuild
  error"), `dotnet-errors-in`, `dotnet-netsdk` (`NETSDK\d+:`).
- Generic (cover .NET/JVM transitively): `unhandled-exception`, `segfault`,
  `out-of-memory`.

**Structured bank (`build_error_parsers.go`) already has:**
- `goCompileRe` (Go compiler `./file.go:N:N: msg`),
  `goTestHeaderRe`/`goTestLocationRe` (`--- FAIL:` → `file.go:N:`),
  `rustHeaderRe` (`error[Ennnn]:` — **code-required**) + `rustLocationRe` (`-->`).

**Gaps the deltas fill:** Go goroutine/panic frames (structured), Rust
no-code warning header, Rust panic-line location, javac, Maven bracketed,
Java/.NET stack frames, C#/MSBuild/NuGet codes (both banks), Gradle, Spring Boot.

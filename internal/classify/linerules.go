package classify

import "regexp"

// LineRule is one broad, per-line classification pattern. It produces a
// boolean ("this line is an alert") plus severity, category, and a short
// description. Moved verbatim from the former overlay alert pattern bank.
type LineRule struct {
	ID          string
	Pattern     *regexp.Regexp
	Severity    Severity
	Category    string // e.g. "dotnet", "webpack", "go", "generic"
	Description string
}

// DefaultLineRules returns the built-in per-line classification rules for
// common dev-server frameworks and languages.
//
// Adding a new framework: if a single boolean "this line is an error/warning"
// is enough, add a LineRule here. If an agent needs structured location/code
// fields, add a structured parser (build.go / structured.go) instead — the
// structured parsers take precedence over these broad rules in ClassifyLine.
func DefaultLineRules() []LineRule {
	return []LineRule{
		// .NET / dotnet watch
		{
			ID:          "dotnet-restart",
			Pattern:     regexp.MustCompile(`(?i)restart is needed`),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: "dotnet watch requires restart to apply changes",
		},
		{
			ID:          "dotnet-enc-error",
			Pattern:     regexp.MustCompile(`(?i)error ENC\d+:`),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: "Edit and Continue error",
		},
		{
			ID:          "dotnet-enc-warning",
			Pattern:     regexp.MustCompile(`(?i)warning ENC\d+:`),
			Severity:    SeverityWarning,
			Category:    "dotnet",
			Description: "Edit and Continue warning",
		},
		{
			ID:          "dotnet-build-error",
			Pattern:     regexp.MustCompile(`(?i)Build FAILED`),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: ".NET build failure",
		},
		{
			ID:          "dotnet-watch-error",
			Pattern:     regexp.MustCompile(`dotnet watch ❌`),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: "dotnet watch error (emoji prefix)",
		},
		{
			ID:          "dotnet-watch-warning",
			Pattern:     regexp.MustCompile(`dotnet watch ⚠`),
			Severity:    SeverityWarning,
			Category:    "dotnet",
			Description: "dotnet watch warning (emoji prefix)",
		},
		{
			ID:          "dotnet-watch-restore-failed",
			Pattern:     regexp.MustCompile(`dotnet watch 🔨.*Failed to`),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: "dotnet watch restore/build failure",
		},
		{
			ID:          "dotnet-msbuild-error",
			Pattern:     regexp.MustCompile(`(?i)MSBuild error`),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: "MSBuild error",
		},
		{
			ID:          "dotnet-errors-in",
			Pattern:     regexp.MustCompile(`Error\(s\) in `),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: ".NET project error list header",
		},
		{
			ID:          "dotnet-netsdk",
			Pattern:     regexp.MustCompile(`NETSDK\d+:`),
			Severity:    SeverityError,
			Category:    "dotnet",
			Description: ".NET SDK error code",
		},

		// Webpack
		{
			ID:          "webpack-error",
			Pattern:     regexp.MustCompile(`ERROR in`),
			Severity:    SeverityError,
			Category:    "webpack",
			Description: "Webpack compilation error",
		},
		{
			ID:          "webpack-compile-fail",
			Pattern:     regexp.MustCompile(`Failed to compile`),
			Severity:    SeverityError,
			Category:    "webpack",
			Description: "Webpack failed to compile",
		},

		// Vite
		{
			ID:          "vite-hmr-fail",
			Pattern:     regexp.MustCompile(`(?i)hmr.*(fail|error)`),
			Severity:    SeverityError,
			Category:    "vite",
			Description: "Vite HMR failure",
		},

		// Next.js
		{
			ID:          "nextjs-build-error",
			Pattern:     regexp.MustCompile(`(?i)Build error`),
			Severity:    SeverityError,
			Category:    "nextjs",
			Description: "Next.js build error",
		},

		// Go
		{
			ID:          "go-build-fail",
			Pattern:     regexp.MustCompile(`(?i)build failed`),
			Severity:    SeverityError,
			Category:    "go",
			Description: "Go build failure",
		},
		{
			ID:          "go-panic",
			Pattern:     regexp.MustCompile(`^panic:`),
			Severity:    SeverityError,
			Category:    "go",
			Description: "Go panic",
		},
		{
			ID:          "go-test-fail",
			Pattern:     regexp.MustCompile(`^FAIL\s+\S+`),
			Severity:    SeverityError,
			Category:    "go",
			Description: "Go test failure",
		},

		// Python
		{
			ID:          "python-traceback",
			Pattern:     regexp.MustCompile(`Traceback \(most recent call last\)`),
			Severity:    SeverityError,
			Category:    "python",
			Description: "Python traceback",
		},
		{
			ID:          "python-syntax",
			Pattern:     regexp.MustCompile(`SyntaxError:`),
			Severity:    SeverityError,
			Category:    "python",
			Description: "Python syntax error",
		},

		// Generic patterns
		{
			ID:          "connection-refused",
			Pattern:     regexp.MustCompile(`(?i)(ECONNREFUSED|connection refused)`),
			Severity:    SeverityWarning,
			Category:    "generic",
			Description: "Connection refused",
		},
		{
			ID:          "addr-in-use",
			Pattern:     regexp.MustCompile(`EADDRINUSE`),
			Severity:    SeverityError,
			Category:    "generic",
			Description: "Address already in use",
		},
		{
			ID:          "segfault",
			Pattern:     regexp.MustCompile(`Segmentation fault`),
			Severity:    SeverityError,
			Category:    "generic",
			Description: "Segmentation fault",
		},
		{
			ID:          "unhandled-exception",
			Pattern:     regexp.MustCompile(`(?i)unhandled exception`),
			Severity:    SeverityError,
			Category:    "generic",
			Description: "Unhandled exception",
		},
		{
			ID:          "out-of-memory",
			Pattern:     regexp.MustCompile(`(?i)out of memory`),
			Severity:    SeverityError,
			Category:    "generic",
			Description: "Out of memory",
		},

		// Node.js / npm / yarn
		{
			ID:          "node-error",
			Pattern:     regexp.MustCompile(`^Error:`),
			Severity:    SeverityError,
			Category:    "node",
			Description: "Node.js Error: prefix",
		},
		{
			ID:          "node-unhandled-promise",
			Pattern:     regexp.MustCompile(`UnhandledPromiseRejection`),
			Severity:    SeverityError,
			Category:    "node",
			Description: "Node.js unhandled promise rejection",
		},
		{
			ID:          "node-cannot-find-module",
			Pattern:     regexp.MustCompile(`Cannot find module`),
			Severity:    SeverityError,
			Category:    "node",
			Description: "Node.js module not found",
		},
		{
			ID:          "npm-err",
			Pattern:     regexp.MustCompile(`npm ERR!`),
			Severity:    SeverityError,
			Category:    "node",
			Description: "npm error",
		},
		{
			ID:          "yarn-error",
			Pattern:     regexp.MustCompile(`(?i)yarn error`),
			Severity:    SeverityError,
			Category:    "node",
			Description: "yarn error",
		},
		{
			ID:          "nodemon-crash",
			Pattern:     regexp.MustCompile(`\[nodemon\] app crashed`),
			Severity:    SeverityError,
			Category:    "node",
			Description: "nodemon app crash",
		},

		// Python extended
		{
			ID:          "python-module-not-found",
			Pattern:     regexp.MustCompile(`ModuleNotFoundError:`),
			Severity:    SeverityError,
			Category:    "python",
			Description: "Python module not found",
		},
		{
			ID:          "python-import-error",
			Pattern:     regexp.MustCompile(`^ImportError:`),
			Severity:    SeverityError,
			Category:    "python",
			Description: "Python import error",
		},
		{
			ID:          "python-django-exception",
			Pattern:     regexp.MustCompile(`django\.core\.exceptions`),
			Severity:    SeverityError,
			Category:    "python",
			Description: "Django core exception",
		},
		{
			ID:          "python-runtime-error",
			Pattern:     regexp.MustCompile(`^RuntimeError:`),
			Severity:    SeverityError,
			Category:    "python",
			Description: "Python RuntimeError",
		},
		{
			ID:          "python-werkzeug-error",
			Pattern:     regexp.MustCompile(`werkzeug\.`),
			Severity:    SeverityError,
			Category:    "python",
			Description: "Werkzeug exception",
		},

		// Go extended
		{
			ID:          "go-fatal-error",
			Pattern:     regexp.MustCompile(`^fatal error:`),
			Severity:    SeverityError,
			Category:    "go",
			Description: "Go fatal error",
		},
		{
			ID:          "go-module-error",
			Pattern:     regexp.MustCompile(`^go: error`),
			Severity:    SeverityError,
			Category:    "go",
			Description: "Go module/toolchain error",
		},

		// Rust / cargo
		{
			ID:          "rust-error-code",
			Pattern:     regexp.MustCompile(`^error\[E\d+\]`),
			Severity:    SeverityError,
			Category:    "rust",
			Description: "Rust compiler error with error code",
		},
		{
			ID:          "rust-aborting",
			Pattern:     regexp.MustCompile(`^error: aborting`),
			Severity:    SeverityError,
			Category:    "rust",
			Description: "Rust compilation aborting",
		},
		{
			ID:          "rust-backtrace-hint",
			Pattern:     regexp.MustCompile(`RUST_BACKTRACE`),
			Severity:    SeverityWarning,
			Category:    "rust",
			Description: "Rust backtrace environment hint",
		},
		{
			ID:          "rust-thread-panic",
			Pattern:     regexp.MustCompile(`thread '.*' panicked`),
			Severity:    SeverityError,
			Category:    "rust",
			Description: "Rust thread panic",
		},

		// Java / Maven / Gradle
		{
			ID:          "java-exception-in-thread",
			Pattern:     regexp.MustCompile(`Exception in thread`),
			Severity:    SeverityError,
			Category:    "java",
			Description: "Java exception in thread",
		},
		{
			ID:          "java-build-failure",
			Pattern:     regexp.MustCompile(`^BUILD FAILURE`),
			Severity:    SeverityError,
			Category:    "java",
			Description: "Maven/Gradle build failure",
		},
		{
			ID:          "java-caused-by",
			Pattern:     regexp.MustCompile(`^Caused by:`),
			Severity:    SeverityError,
			Category:    "java",
			Description: "Java exception cause chain",
		},
		{
			ID:          "java-severe",
			Pattern:     regexp.MustCompile(`^SEVERE:`),
			Severity:    SeverityError,
			Category:    "java",
			Description: "Java SEVERE log level",
		},
		{
			ID:          "java-lang-exception",
			Pattern:     regexp.MustCompile(`java\.lang\.\w+(?:Error|Exception)`),
			Severity:    SeverityError,
			Category:    "java",
			Description: "java.lang exception or error class",
		},

		// Ruby / Rails
		// Note: RuntimeError: is covered by python-runtime-error (^RuntimeError:) which
		// matches both Python and Ruby output sharing the same format.
		{
			ID:          "ruby-no-method-error",
			Pattern:     regexp.MustCompile(`^NoMethodError`),
			Severity:    SeverityError,
			Category:    "ruby",
			Description: "Ruby NoMethodError",
		},
		{
			ID:          "ruby-argument-error",
			Pattern:     regexp.MustCompile(`^ArgumentError`),
			Severity:    SeverityError,
			Category:    "ruby",
			Description: "Ruby ArgumentError",
		},
		{
			ID:          "ruby-load-error",
			Pattern:     regexp.MustCompile(`^LoadError`),
			Severity:    SeverityError,
			Category:    "ruby",
			Description: "Ruby LoadError",
		},
		{
			ID:          "ruby-errno",
			Pattern:     regexp.MustCompile(`^Errno::`),
			Severity:    SeverityError,
			Category:    "ruby",
			Description: "Ruby Errno system call error",
		},

		// Rebuild signals — info-severity matches that the OutageClassifier
		// reads as evidence the next process stop is part of an intentional
		// rebuild rather than a crash. These never get surfaced as alerts
		// (severity is info, batched only with other info entries) but they
		// stamp lastRebuildSignalAt on the health tracker via the daemon's
		// alert-scanner output hook.
		{
			ID:          "rebuild-generic",
			Pattern:     regexp.MustCompile(`(?i)\b(rebuilding|rebuild finished|recompiling|compiling\.\.\.|restarting)\b`),
			Severity:    SeverityInfo,
			Category:    "rebuild",
			Description: "Generic rebuild/recompile signal",
		},
		{
			ID:          "rebuild-vite",
			Pattern:     regexp.MustCompile(`vite:(reload|hmr update)|page reload`),
			Severity:    SeverityInfo,
			Category:    "rebuild",
			Description: "Vite reload or HMR update",
		},
		{
			ID:          "rebuild-dotnet-watch",
			Pattern:     regexp.MustCompile(`(?i)(watch attached|file changed:|hot reload of changes)`),
			Severity:    SeverityInfo,
			Category:    "rebuild",
			Description: "dotnet watch rebuild signal",
		},
		{
			ID:          "rebuild-go-watch",
			Pattern:     regexp.MustCompile(`(?i)(running\.\.\. ok|build ok|reflex.*starting|air.*restarting)`),
			Severity:    SeverityInfo,
			Category:    "rebuild",
			Description: "Go file-watcher rebuild signal (air, reflex)",
		},
		{
			ID:          "rebuild-build-success",
			Pattern:     regexp.MustCompile(`(?i)build (succeeded|completed)`),
			Severity:    SeverityInfo,
			Category:    "rebuild",
			Description: "Build success signal",
		},
	}
}

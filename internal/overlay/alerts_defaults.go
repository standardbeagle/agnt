package overlay

import "regexp"

// DefaultAlertPatterns returns the built-in set of alert patterns for common
// dev server frameworks and languages.
//
// Coordination note: this pattern bank classifies lines for *toast surfacing*
// in the browser overlay. It produces a single boolean ("this line is an
// error") plus a category, severity, and short description — the toast UI
// is the consumer.
//
// A separate parser bank lives in internal/tools/build_error_parsers.go.
// That bank produces *structured fields* (file, line, col, code, rule, test,
// message) for the `proc output` MCP tool's compact error rendering, and
// runs only when an agent passes `extract: ["error"|"warning"]`. The two
// banks are intentionally not shared:
//   - This bank is framework-specific and broad ("ENC errors", "Build FAILED");
//     toast users want every distinct flavour categorised.
//   - The parser bank is format-specific and narrow ("tsc paren form",
//     "rust error[Eddd] -> location"); agents want zero noise so each
//     parser must produce a token-efficient single line.
//
// Adding a new framework? Decide which surface it serves first. If the
// toast UI needs to react to a new flavour of warning, add it here. If
// an agent needs structured location/code fields for `proc output`, add
// it to the parser bank instead.
func DefaultAlertPatterns() []*AlertPattern {
	return []*AlertPattern{
		// .NET / dotnet watch
		{
			ID:          "dotnet-restart",
			Pattern:     regexp.MustCompile(`(?i)restart is needed`),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: "dotnet watch requires restart to apply changes",
		},
		{
			ID:          "dotnet-enc-error",
			Pattern:     regexp.MustCompile(`(?i)error ENC\d+:`),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: "Edit and Continue error",
		},
		{
			ID:          "dotnet-enc-warning",
			Pattern:     regexp.MustCompile(`(?i)warning ENC\d+:`),
			Severity:    AlertSeverityWarning,
			Category:    "dotnet",
			Description: "Edit and Continue warning",
		},
		{
			ID:          "dotnet-build-error",
			Pattern:     regexp.MustCompile(`(?i)Build FAILED`),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: ".NET build failure",
		},
		{
			ID:          "dotnet-watch-error",
			Pattern:     regexp.MustCompile(`dotnet watch ❌`),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: "dotnet watch error (emoji prefix)",
		},
		{
			ID:          "dotnet-watch-warning",
			Pattern:     regexp.MustCompile(`dotnet watch ⚠`),
			Severity:    AlertSeverityWarning,
			Category:    "dotnet",
			Description: "dotnet watch warning (emoji prefix)",
		},
		{
			ID:          "dotnet-watch-restore-failed",
			Pattern:     regexp.MustCompile(`dotnet watch 🔨.*Failed to`),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: "dotnet watch restore/build failure",
		},
		{
			ID:          "dotnet-msbuild-error",
			Pattern:     regexp.MustCompile(`(?i)MSBuild error`),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: "MSBuild error",
		},
		{
			ID:          "dotnet-errors-in",
			Pattern:     regexp.MustCompile(`Error\(s\) in `),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: ".NET project error list header",
		},
		{
			ID:          "dotnet-netsdk",
			Pattern:     regexp.MustCompile(`NETSDK\d+:`),
			Severity:    AlertSeverityError,
			Category:    "dotnet",
			Description: ".NET SDK error code",
		},

		// Webpack
		{
			ID:          "webpack-error",
			Pattern:     regexp.MustCompile(`ERROR in`),
			Severity:    AlertSeverityError,
			Category:    "webpack",
			Description: "Webpack compilation error",
		},
		{
			ID:          "webpack-compile-fail",
			Pattern:     regexp.MustCompile(`Failed to compile`),
			Severity:    AlertSeverityError,
			Category:    "webpack",
			Description: "Webpack failed to compile",
		},

		// Vite
		{
			ID:          "vite-hmr-fail",
			Pattern:     regexp.MustCompile(`(?i)hmr.*(fail|error)`),
			Severity:    AlertSeverityError,
			Category:    "vite",
			Description: "Vite HMR failure",
		},

		// Next.js
		{
			ID:          "nextjs-build-error",
			Pattern:     regexp.MustCompile(`(?i)Build error`),
			Severity:    AlertSeverityError,
			Category:    "nextjs",
			Description: "Next.js build error",
		},

		// Go
		{
			ID:          "go-build-fail",
			Pattern:     regexp.MustCompile(`(?i)build failed`),
			Severity:    AlertSeverityError,
			Category:    "go",
			Description: "Go build failure",
		},
		{
			ID:          "go-panic",
			Pattern:     regexp.MustCompile(`^panic:`),
			Severity:    AlertSeverityError,
			Category:    "go",
			Description: "Go panic",
		},
		{
			ID:          "go-test-fail",
			Pattern:     regexp.MustCompile(`^FAIL\s+\S+`),
			Severity:    AlertSeverityError,
			Category:    "go",
			Description: "Go test failure",
		},

		// Python
		{
			ID:          "python-traceback",
			Pattern:     regexp.MustCompile(`Traceback \(most recent call last\)`),
			Severity:    AlertSeverityError,
			Category:    "python",
			Description: "Python traceback",
		},
		{
			ID:          "python-syntax",
			Pattern:     regexp.MustCompile(`SyntaxError:`),
			Severity:    AlertSeverityError,
			Category:    "python",
			Description: "Python syntax error",
		},

		// Generic patterns
		{
			ID:          "connection-refused",
			Pattern:     regexp.MustCompile(`(?i)(ECONNREFUSED|connection refused)`),
			Severity:    AlertSeverityWarning,
			Category:    "generic",
			Description: "Connection refused",
		},
		{
			ID:          "addr-in-use",
			Pattern:     regexp.MustCompile(`EADDRINUSE`),
			Severity:    AlertSeverityError,
			Category:    "generic",
			Description: "Address already in use",
		},
		{
			ID:          "segfault",
			Pattern:     regexp.MustCompile(`Segmentation fault`),
			Severity:    AlertSeverityError,
			Category:    "generic",
			Description: "Segmentation fault",
		},
		{
			ID:          "unhandled-exception",
			Pattern:     regexp.MustCompile(`(?i)unhandled exception`),
			Severity:    AlertSeverityError,
			Category:    "generic",
			Description: "Unhandled exception",
		},
		{
			ID:          "out-of-memory",
			Pattern:     regexp.MustCompile(`(?i)out of memory`),
			Severity:    AlertSeverityError,
			Category:    "generic",
			Description: "Out of memory",
		},

		// Node.js / npm / yarn
		{
			ID:          "node-error",
			Pattern:     regexp.MustCompile(`^Error:`),
			Severity:    AlertSeverityError,
			Category:    "node",
			Description: "Node.js Error: prefix",
		},
		{
			ID:          "node-unhandled-promise",
			Pattern:     regexp.MustCompile(`UnhandledPromiseRejection`),
			Severity:    AlertSeverityError,
			Category:    "node",
			Description: "Node.js unhandled promise rejection",
		},
		{
			ID:          "node-cannot-find-module",
			Pattern:     regexp.MustCompile(`Cannot find module`),
			Severity:    AlertSeverityError,
			Category:    "node",
			Description: "Node.js module not found",
		},
		{
			ID:          "npm-err",
			Pattern:     regexp.MustCompile(`npm ERR!`),
			Severity:    AlertSeverityError,
			Category:    "node",
			Description: "npm error",
		},
		{
			ID:          "yarn-error",
			Pattern:     regexp.MustCompile(`(?i)yarn error`),
			Severity:    AlertSeverityError,
			Category:    "node",
			Description: "yarn error",
		},
		{
			ID:          "nodemon-crash",
			Pattern:     regexp.MustCompile(`\[nodemon\] app crashed`),
			Severity:    AlertSeverityError,
			Category:    "node",
			Description: "nodemon app crash",
		},

		// Python extended
		{
			ID:          "python-module-not-found",
			Pattern:     regexp.MustCompile(`ModuleNotFoundError:`),
			Severity:    AlertSeverityError,
			Category:    "python",
			Description: "Python module not found",
		},
		{
			ID:          "python-import-error",
			Pattern:     regexp.MustCompile(`^ImportError:`),
			Severity:    AlertSeverityError,
			Category:    "python",
			Description: "Python import error",
		},
		{
			ID:          "python-django-exception",
			Pattern:     regexp.MustCompile(`django\.core\.exceptions`),
			Severity:    AlertSeverityError,
			Category:    "python",
			Description: "Django core exception",
		},
		{
			ID:          "python-runtime-error",
			Pattern:     regexp.MustCompile(`^RuntimeError:`),
			Severity:    AlertSeverityError,
			Category:    "python",
			Description: "Python RuntimeError",
		},
		{
			ID:          "python-werkzeug-error",
			Pattern:     regexp.MustCompile(`werkzeug\.`),
			Severity:    AlertSeverityError,
			Category:    "python",
			Description: "Werkzeug exception",
		},

		// Go extended
		{
			ID:          "go-fatal-error",
			Pattern:     regexp.MustCompile(`^fatal error:`),
			Severity:    AlertSeverityError,
			Category:    "go",
			Description: "Go fatal error",
		},
		{
			ID:          "go-module-error",
			Pattern:     regexp.MustCompile(`^go: error`),
			Severity:    AlertSeverityError,
			Category:    "go",
			Description: "Go module/toolchain error",
		},

		// Rust / cargo
		{
			ID:          "rust-error-code",
			Pattern:     regexp.MustCompile(`^error\[E\d+\]`),
			Severity:    AlertSeverityError,
			Category:    "rust",
			Description: "Rust compiler error with error code",
		},
		{
			ID:          "rust-aborting",
			Pattern:     regexp.MustCompile(`^error: aborting`),
			Severity:    AlertSeverityError,
			Category:    "rust",
			Description: "Rust compilation aborting",
		},
		{
			ID:          "rust-backtrace-hint",
			Pattern:     regexp.MustCompile(`RUST_BACKTRACE`),
			Severity:    AlertSeverityWarning,
			Category:    "rust",
			Description: "Rust backtrace environment hint",
		},
		{
			ID:          "rust-thread-panic",
			Pattern:     regexp.MustCompile(`thread '.*' panicked`),
			Severity:    AlertSeverityError,
			Category:    "rust",
			Description: "Rust thread panic",
		},

		// Java / Maven / Gradle
		{
			ID:          "java-exception-in-thread",
			Pattern:     regexp.MustCompile(`Exception in thread`),
			Severity:    AlertSeverityError,
			Category:    "java",
			Description: "Java exception in thread",
		},
		{
			ID:          "java-build-failure",
			Pattern:     regexp.MustCompile(`^BUILD FAILURE`),
			Severity:    AlertSeverityError,
			Category:    "java",
			Description: "Maven/Gradle build failure",
		},
		{
			ID:          "java-caused-by",
			Pattern:     regexp.MustCompile(`^Caused by:`),
			Severity:    AlertSeverityError,
			Category:    "java",
			Description: "Java exception cause chain",
		},
		{
			ID:          "java-severe",
			Pattern:     regexp.MustCompile(`^SEVERE:`),
			Severity:    AlertSeverityError,
			Category:    "java",
			Description: "Java SEVERE log level",
		},
		{
			ID:          "java-lang-exception",
			Pattern:     regexp.MustCompile(`java\.lang\.\w+(?:Error|Exception)`),
			Severity:    AlertSeverityError,
			Category:    "java",
			Description: "java.lang exception or error class",
		},

		// Ruby / Rails
		// Note: RuntimeError: is covered by python-runtime-error (^RuntimeError:) which
		// matches both Python and Ruby output sharing the same format.
		{
			ID:          "ruby-no-method-error",
			Pattern:     regexp.MustCompile(`^NoMethodError`),
			Severity:    AlertSeverityError,
			Category:    "ruby",
			Description: "Ruby NoMethodError",
		},
		{
			ID:          "ruby-argument-error",
			Pattern:     regexp.MustCompile(`^ArgumentError`),
			Severity:    AlertSeverityError,
			Category:    "ruby",
			Description: "Ruby ArgumentError",
		},
		{
			ID:          "ruby-load-error",
			Pattern:     regexp.MustCompile(`^LoadError`),
			Severity:    AlertSeverityError,
			Category:    "ruby",
			Description: "Ruby LoadError",
		},
		{
			ID:          "ruby-errno",
			Pattern:     regexp.MustCompile(`^Errno::`),
			Severity:    AlertSeverityError,
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
			Severity:    AlertSeverityInfo,
			Category:    "rebuild",
			Description: "Generic rebuild/recompile signal",
		},
		{
			ID:          "rebuild-vite",
			Pattern:     regexp.MustCompile(`vite:(reload|hmr update)|page reload`),
			Severity:    AlertSeverityInfo,
			Category:    "rebuild",
			Description: "Vite reload or HMR update",
		},
		{
			ID:          "rebuild-dotnet-watch",
			Pattern:     regexp.MustCompile(`(?i)(watch attached|file changed:|hot reload of changes)`),
			Severity:    AlertSeverityInfo,
			Category:    "rebuild",
			Description: "dotnet watch rebuild signal",
		},
		{
			ID:          "rebuild-go-watch",
			Pattern:     regexp.MustCompile(`(?i)(running\.\.\. ok|build ok|reflex.*starting|air.*restarting)`),
			Severity:    AlertSeverityInfo,
			Category:    "rebuild",
			Description: "Go file-watcher rebuild signal (air, reflex)",
		},
		{
			ID:          "rebuild-build-success",
			Pattern:     regexp.MustCompile(`(?i)build (succeeded|completed)`),
			Severity:    AlertSeverityInfo,
			Category:    "rebuild",
			Description: "Build success signal",
		},
	}
}

package overlay

import "regexp"

// DefaultAlertPatterns returns the built-in set of alert patterns for common
// dev server frameworks and languages.
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
	}
}

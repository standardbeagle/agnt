package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestExtractFirstAppFrame(t *testing.T) {
	t.Run("JS stack with node_modules", func(t *testing.T) {
		stack := `TypeError: Cannot read properties of undefined (reading 'map')
    at UserList (src/components/UserList.tsx:28:12)
    at renderWithHooks (node_modules/react-dom/cjs/react-dom.development.js:14985:18)
    at Object.createElement (node_modules/react/cjs/react.development.js:1234:5)`

		result := extractFirstAppFrame(stack)
		assert.Equal(t, "src/components/UserList.tsx:28:12", result)
	})

	t.Run("JS stack without parentheses", func(t *testing.T) {
		stack := `Error: test
    at src/index.js:10:5`

		result := extractFirstAppFrame(stack)
		assert.Equal(t, "src/index.js:10:5", result)
	})

	t.Run("JS stack all internal", func(t *testing.T) {
		stack := `Error: test
    at node_modules/something/index.js:1:1
    at <anonymous>:1:1`

		result := extractFirstAppFrame(stack)
		assert.Equal(t, "", result)
	})

	t.Run("Go panic stack", func(t *testing.T) {
		stack := `goroutine 1 [running]:
runtime/debug.Stack()
	runtime/debug/stack.go:24 +0x5e
main.handler()
	/home/user/project/main.go:42 +0x1a3
runtime.main()
	runtime/proc.go:250 +0x1c4`

		result := extractFirstAppFrame(stack)
		assert.Equal(t, "/home/user/project/main.go:42", result)
	})

	t.Run("empty stack", func(t *testing.T) {
		assert.Equal(t, "", extractFirstAppFrame(""))
	})

	t.Run("Python traceback", func(t *testing.T) {
		stack := `Traceback (most recent call last):
  File "/app/views.py", line 42, in index
    result = process(data)
  File "/app/utils.py", line 10, in process
    return data['key']`

		result := extractFirstAppFrame(stack)
		assert.Equal(t, "/app/views.py:42", result)
	})

	t.Run("JS stack with webpack internal skipped", func(t *testing.T) {
		stack := `Error: fail
    at webpack-internal:///./src/entry.js:5:10
    at AppComponent (src/App.tsx:15:3)`

		result := extractFirstAppFrame(stack)
		assert.Equal(t, "src/App.tsx:15:3", result)
	})
}

func TestExtractErrorMessage(t *testing.T) {
	t.Run("JSON body with message", func(t *testing.T) {
		body := `{"message":"Validation failed: email is required","code":400}`
		result := extractErrorMessage(body, 200)
		assert.Equal(t, "Validation failed: email is required", result)
	})

	t.Run("JSON body with error string", func(t *testing.T) {
		body := `{"error":"not found"}`
		result := extractErrorMessage(body, 200)
		assert.Equal(t, "not found", result)
	})

	t.Run("JSON body with nested error", func(t *testing.T) {
		body := `{"error":{"message":"detailed error"}}`
		result := extractErrorMessage(body, 200)
		assert.Equal(t, "detailed error", result)
	})

	t.Run("HTML body", func(t *testing.T) {
		body := `<html><body><h1>500 Internal Server Error</h1><p>Something went wrong</p></body></html>`
		result := extractErrorMessage(body, 200)
		assert.Contains(t, result, "500 Internal Server Error")
		assert.Contains(t, result, "Something went wrong")
		// Should not contain HTML tags
		assert.NotContains(t, result, "<h1>")
	})

	t.Run("plain text", func(t *testing.T) {
		result := extractErrorMessage("just a plain error message", 200)
		assert.Equal(t, "just a plain error message", result)
	})

	t.Run("truncation", func(t *testing.T) {
		long := "this is a very long message that should be truncated because it exceeds the limit"
		result := extractErrorMessage(long, 30)
		assert.Len(t, result, 30)
		assert.True(t, len(result) <= 30)
		assert.Equal(t, "this is a very long message...", result)
	})

	t.Run("empty body", func(t *testing.T) {
		assert.Equal(t, "", extractErrorMessage("", 200))
	})

	t.Run("JSON with detail field", func(t *testing.T) {
		body := `{"detail":"access denied"}`
		result := extractErrorMessage(body, 200)
		assert.Equal(t, "access denied", result)
	})
}

func TestIsNoiseError(t *testing.T) {
	t.Run("favicon 404", func(t *testing.T) {
		assert.True(t, isNoiseError("/favicon.ico", 404))
	})

	t.Run("source map 404", func(t *testing.T) {
		assert.True(t, isNoiseError("/static/js/main.js.map", 404))
	})

	t.Run("webpack HMR 404", func(t *testing.T) {
		assert.True(t, isNoiseError("/__webpack_hmr", 404))
	})

	t.Run("hot update 404", func(t *testing.T) {
		assert.True(t, isNoiseError("/app.abc123.hot-update.js", 404))
	})

	t.Run("301 redirect", func(t *testing.T) {
		assert.True(t, isNoiseError("/old-page", 301))
	})

	t.Run("302 redirect", func(t *testing.T) {
		assert.True(t, isNoiseError("/redirect", 302))
	})

	t.Run("304 not modified", func(t *testing.T) {
		assert.True(t, isNoiseError("/api/data", 304))
	})

	t.Run("real 404", func(t *testing.T) {
		assert.False(t, isNoiseError("/api/users/123", 404))
	})

	t.Run("500 error", func(t *testing.T) {
		assert.False(t, isNoiseError("/api/users", 500))
	})

	t.Run("200 OK", func(t *testing.T) {
		assert.False(t, isNoiseError("/api/data", 200))
	})
}

func TestFormatTimeAgo(t *testing.T) {
	t.Run("seconds", func(t *testing.T) {
		result := formatTimeAgo(time.Now().Add(-5 * time.Second))
		assert.Equal(t, "5s ago", result)
	})

	t.Run("minutes", func(t *testing.T) {
		result := formatTimeAgo(time.Now().Add(-3 * time.Minute))
		assert.Equal(t, "3m ago", result)
	})

	t.Run("hours", func(t *testing.T) {
		result := formatTimeAgo(time.Now().Add(-2 * time.Hour))
		assert.Equal(t, "2h ago", result)
	})

	t.Run("zero time", func(t *testing.T) {
		result := formatTimeAgo(time.Time{})
		assert.Equal(t, "unknown", result)
	})
}

func TestDeduplicateErrors(t *testing.T) {
	now := time.Now()

	t.Run("merge identical errors", func(t *testing.T) {
		errors := []unifiedError{
			{Source: "browser:js", Severity: "error", Category: "TypeError", Message: "Cannot read 'map'", Location: "app.js:10:5", Count: 1, LastSeen: now.Add(-10 * time.Second)},
			{Source: "browser:js", Severity: "error", Category: "TypeError", Message: "Cannot read 'map'", Location: "app.js:10:5", Count: 1, LastSeen: now.Add(-5 * time.Second)},
			{Source: "browser:js", Severity: "error", Category: "TypeError", Message: "Cannot read 'map'", Location: "app.js:10:5", Count: 1, LastSeen: now.Add(-15 * time.Second)},
		}

		result := deduplicateErrors(errors)
		assert.Len(t, result, 1)
		assert.Equal(t, 3, result[0].Count)
		assert.Equal(t, now.Add(-5*time.Second).Unix(), result[0].LastSeen.Unix())
	})

	t.Run("different errors kept separate", func(t *testing.T) {
		errors := []unifiedError{
			{Source: "browser:js", Severity: "error", Category: "TypeError", Message: "msg1", Count: 1, LastSeen: now},
			{Source: "browser:js", Severity: "error", Category: "ReferenceError", Message: "msg2", Count: 1, LastSeen: now},
			{Source: "proxy:http", Severity: "error", Category: "500", Message: "msg3", Count: 1, LastSeen: now},
		}

		result := deduplicateErrors(errors)
		assert.Len(t, result, 3)
	})

	t.Run("empty input", func(t *testing.T) {
		result := deduplicateErrors(nil)
		assert.Empty(t, result)
	})
}

func TestFormatCompactOutput(t *testing.T) {
	now := time.Now()

	t.Run("mixed errors and warnings", func(t *testing.T) {
		errors := []unifiedError{
			{
				Source:   "process:dev-server",
				Severity: "error",
				Category: "COMPILE ERROR",
				Message:  "CS1002: ; expected",
				Location: "src/Controllers/HomeController.cs:42:15",
				Count:    2,
				LastSeen: now.Add(-3 * time.Second),
			},
			{
				Source:   "browser:js",
				Severity: "error",
				Category: "TypeError",
				Message:  "Cannot read properties of undefined (reading 'map')",
				Location: "src/components/UserList.tsx:28:12",
				Page:     "http://localhost:3000/users",
				Count:    1,
				LastSeen: now.Add(-12 * time.Second),
			},
			{
				Source:   "process:dev-server",
				Severity: "warning",
				Category: "DEPRECATION",
				Message:  "BrowserModule.withServerTransition deprecated",
				Count:    1,
				LastSeen: now.Add(-30 * time.Second),
			},
		}

		result := formatCompactErrors(errors, 2, 1)

		assert.Contains(t, result, "=== Errors (2) ===")
		assert.Contains(t, result, "[process:dev-server] COMPILE ERROR")
		assert.Contains(t, result, "2x, latest")
		assert.Contains(t, result, "CS1002: ; expected")
		assert.Contains(t, result, "→ src/Controllers/HomeController.cs:42:15")

		assert.Contains(t, result, "[browser:js] TypeError")
		assert.Contains(t, result, "Cannot read properties of undefined")
		assert.Contains(t, result, "page: http://localhost:3000/users")

		assert.Contains(t, result, "=== Warnings (1) ===")
		assert.Contains(t, result, "[process:dev-server] DEPRECATION")
		assert.Contains(t, result, "BrowserModule.withServerTransition deprecated")
	})

	t.Run("no errors", func(t *testing.T) {
		result := formatCompactErrors(nil, 0, 0)
		assert.Equal(t, "No errors found.", result)
	})

	t.Run("errors only, no warnings", func(t *testing.T) {
		errors := []unifiedError{
			{
				Source:   "proxy:http",
				Severity: "error",
				Category: "500 Internal Server Error",
				Message:  "POST /api/users → \"Validation failed\"",
				Count:    3,
				LastSeen: now.Add(-8 * time.Second),
			},
		}

		result := formatCompactErrors(errors, 1, 0)
		assert.Contains(t, result, "=== Errors (1) ===")
		assert.NotContains(t, result, "=== Warnings")
		assert.Contains(t, result, "3x, latest")
	})
}

func TestCategorizeHTTPError(t *testing.T) {
	assert.Equal(t, "404 Not Found", categorizeHTTPError(404))
	assert.Equal(t, "500 Internal Server Error", categorizeHTTPError(500))
	assert.Equal(t, "503 Service Unavailable", categorizeHTTPError(503))
	assert.Equal(t, "400 Bad Request", categorizeHTTPError(400))
	assert.Equal(t, "999 Error", categorizeHTTPError(999))
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hel...", truncate("hello world", 6))
	assert.Equal(t, "he", truncate("hello", 2))
	assert.Equal(t, "", truncate("", 10))
}

func TestStripHTMLTags(t *testing.T) {
	// Each '>' inserts a space, so "</h1> <p>" produces " " + " " + " " = 3 spaces
	result := stripHTMLTags("<h1>Hello</h1> <p>World</p>")
	assert.Contains(t, result, "Hello")
	assert.Contains(t, result, "World")
	assert.NotContains(t, result, "<")
	assert.Equal(t, "No tags here", stripHTMLTags("No tags here"))
}

func TestConvertJSError(t *testing.T) {
	t.Run("basic JS error", func(t *testing.T) {
		em := map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"message":   "TypeError: Cannot read property 'length' of undefined",
				"source":    "http://localhost:3000/static/js/main.js",
				"lineno":    float64(42),
				"colno":     float64(15),
				"url":       "http://localhost:3000/dashboard",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		}

		results := convertJSError("dev", em)
		assert.Len(t, results, 1)
		assert.Equal(t, "browser:js", results[0].Source)
		assert.Equal(t, "error", results[0].Severity)
		assert.Equal(t, "TypeError", results[0].Category)
		assert.Contains(t, results[0].Message, "Cannot read property")
		assert.Equal(t, "http://localhost:3000/dashboard", results[0].Page)
	})

	t.Run("JS error with stack trace", func(t *testing.T) {
		em := map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"message":   "ReferenceError: foo is not defined",
				"stack":     "ReferenceError: foo is not defined\n    at App (src/App.tsx:10:5)\n    at node_modules/react/index.js:1:1",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		}

		results := convertJSError("dev", em)
		assert.Len(t, results, 1)
		assert.Equal(t, "src/App.tsx:10:5", results[0].Location)
	})

	t.Run("empty error data", func(t *testing.T) {
		em := map[string]interface{}{
			"type": "error",
		}
		results := convertJSError("dev", em)
		assert.Empty(t, results)
	})
}

func TestConvertHTTPError(t *testing.T) {
	t.Run("500 error with body", func(t *testing.T) {
		em := map[string]interface{}{
			"type": "http",
			"http": map[string]interface{}{
				"method":        "POST",
				"url":           "/api/users",
				"status_code":   float64(500),
				"response_body": `{"message":"Validation failed: email is required"}`,
				"timestamp":     time.Now().Format(time.RFC3339),
			},
		}

		results := convertHTTPError("dev", em)
		assert.Len(t, results, 1)
		assert.Equal(t, "proxy:http", results[0].Source)
		assert.Equal(t, "error", results[0].Severity)
		assert.Equal(t, "500 Internal Server Error", results[0].Category)
		assert.Contains(t, results[0].Message, "POST /api/users")
		assert.Contains(t, results[0].Message, "Validation failed")
	})

	t.Run("404 noise filtered", func(t *testing.T) {
		em := map[string]interface{}{
			"type": "http",
			"http": map[string]interface{}{
				"method":      "GET",
				"url":         "/favicon.ico",
				"status_code": float64(404),
			},
		}

		results := convertHTTPError("dev", em)
		assert.Empty(t, results)
	})

	t.Run("200 OK skipped", func(t *testing.T) {
		em := map[string]interface{}{
			"type": "http",
			"http": map[string]interface{}{
				"method":      "GET",
				"url":         "/api/data",
				"status_code": float64(200),
			},
		}

		results := convertHTTPError("dev", em)
		assert.Empty(t, results)
	})

	t.Run("4xx is warning", func(t *testing.T) {
		em := map[string]interface{}{
			"type": "http",
			"http": map[string]interface{}{
				"method":      "GET",
				"url":         "/api/users/123",
				"status_code": float64(404),
				"timestamp":   time.Now().Format(time.RFC3339),
			},
		}

		results := convertHTTPError("dev", em)
		assert.Len(t, results, 1)
		assert.Equal(t, "warning", results[0].Severity)
	})
}

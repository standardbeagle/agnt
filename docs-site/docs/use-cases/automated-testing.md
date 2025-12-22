---
sidebar_position: 2
---

# Automated Testing

Using agnt for test automation, CI/CD integration, and test result analysis.

## Overview

agnt can assist with:

- Running test suites and analyzing results
- Monitoring test coverage
- Debugging failing tests
- Integration testing with the proxy
- CI/CD pipeline integration

## Running Tests

### Basic Test Execution

```json
// Detect available scripts
detect {path: "."}
→ {scripts: ["test", "test:unit", "test:e2e", "test:coverage"]}

// Run tests (foreground mode waits for completion)
run {script_name: "test", mode: "foreground"}
→ {exit_code: 0, runtime: "45s"}
```

### With Full Output

```json
run {script_name: "test", mode: "foreground-raw"}
→ {
    exit_code: 1,
    stdout: "...",
    stderr: "FAIL src/utils.test.ts..."
  }
```

### Parallel Test Suites

```json
// Run unit and integration tests in parallel
run {script_name: "test:unit", id: "unit-tests"}
run {script_name: "test:integration", id: "integration-tests"}

// Monitor both
proc {action: "list"}
→ Both running in parallel

// Check unit test output
proc {action: "output", process_id: "unit-tests", grep: "FAIL"}

// Check integration test output
proc {action: "output", process_id: "integration-tests", grep: "FAIL"}
```

## Analyzing Test Results

### Find Failing Tests

```json
// Run tests
run {script_name: "test", mode: "foreground"}
→ {exit_code: 1}

// Find failures
proc {action: "output", process_id: "test", grep: "FAIL"}
→ "FAIL src/components/Button.test.tsx
    FAIL src/utils/date.test.ts"

// Get context around a failure
proc {action: "output", process_id: "test", grep: "Button.test", tail: 30}
→ Detailed failure output
```

### Parse Test Summary

```json
// Get test summary lines
proc {action: "output", process_id: "test", grep: "(passed|failed|skipped)"}
→ "Tests: 42 passed, 3 failed, 2 skipped"
```

### Coverage Analysis

```json
// Run with coverage
run {script_name: "test:coverage", mode: "foreground"}

// Find uncovered files
proc {action: "output", process_id: "test:coverage", grep: "0%"}

// Get coverage summary
proc {action: "output", process_id: "test:coverage", tail: 10}
```

## Debugging Failing Tests

### Interactive Debugging

When a test fails, use the proxy to debug:

```json
// Start dev server
run {script_name: "dev"}

// Set up proxy
proxy {action: "start", id: "test-debug", target_url: "http://localhost:3000"}

// Navigate to the problematic component
// (in browser at http://localhost:8080)

// Check for JavaScript errors
proxylog {proxy_id: "test-debug", types: ["error"]}

// Inspect the failing component
proxy {action: "exec", id: "test-debug", code: "window.__devtool.inspect('.failing-component')"}
```

### Capture Test State

```json
// While the dev server is running with the proxy:

// Capture current DOM state
proxy {action: "exec", id: "test-debug", code: "window.__devtool.captureDOM()"}

// Capture application state
proxy {action: "exec", id: "test-debug", code: "window.__devtool.captureState(['localStorage', 'sessionStorage'])"}

// Take screenshot
proxy {action: "exec", id: "test-debug", code: "window.__devtool.screenshot('test-failure')"}
```

## Integration Testing

### API Testing with Proxy

```json
// Start backend
run {script_name: "dev:api", id: "api"}

// Set up proxy to capture API calls
proxy {action: "start", id: "api-test", target_url: "http://localhost:4000"}

// Run integration tests (they hit the proxy)
run {script_name: "test:integration", mode: "foreground"}

// Analyze API calls made during tests
proxylog {proxy_id: "api-test", types: ["http"]}
→ See all API calls with timing

// Check for unexpected errors
proxylog {proxy_id: "api-test", types: ["http"], status_codes: [400, 401, 404, 500]}
```

### E2E Testing Support

```json
// Start app and proxy
run {script_name: "dev"}
proxy {action: "start", id: "e2e", target_url: "http://localhost:3000", port: 8080}

// Run E2E tests against proxy
run {raw: true, command: "playwright", args: ["test", "--base-url=http://localhost:8080"], mode: "foreground"}

// Analyze captured traffic
proxylog {proxy_id: "e2e", types: ["http", "error"]}

// Check page sessions for failed test
currentpage {proxy_id: "e2e"}
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Tests
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install agnt
        run: |
          go install github.com/standardbeagle/agnt@latest

      - name: Run Tests via agnt
        run: |
          # Use agnt's run command
          agnt run --script test --mode foreground-raw
```

### Using Detection in CI

```yaml
      - name: Detect and Run
        run: |
          PROJECT_TYPE=$(agnt detect | jq -r '.type')

          case $PROJECT_TYPE in
            node)
              agnt run --script test
              ;;
            go)
              agnt run --raw --command go --args "test ./..."
              ;;
            python)
              agnt run --raw --command pytest
              ;;
          esac
```

### Test Reporting

```json
// After test run, collect results
proc {action: "output", process_id: "test"}
→ Full test output

// Parse for reporting
proc {action: "output", process_id: "test", grep: "^(PASS|FAIL)"}
→ Individual test results

// Get timing
proc {action: "status", process_id: "test"}
→ {runtime: "45s"}
```

## Flaky Test Detection

### Track Test Runs

```json
// Run tests multiple times
for i in {1..5}; do
  run {script_name: "test", id: "test-run-$i", mode: "foreground"}
done

// Compare results
proc {action: "output", process_id: "test-run-1", grep: "FAIL"}
proc {action: "output", process_id: "test-run-2", grep: "FAIL"}
// ...

// If different tests fail on different runs, they're flaky
```

### Capture Flaky State

```json
// On flaky failure, capture everything:
proxy {action: "exec", id: "debug", code: "window.__devtool.captureDOM()"}
proxy {action: "exec", id: "debug", code: "window.__devtool.captureState(['localStorage'])"}
proxy {action: "exec", id: "debug", code: "window.__devtool.captureNetwork()"}
proxylog {proxy_id: "debug", types: ["http", "error"]}
```

## Test Environment Management

### Clean Up Ports

```json
// Before running tests, clean up stale processes
proc {action: "cleanup_port", port: 3000}
proc {action: "cleanup_port", port: 8080}

// Now start fresh
run {script_name: "dev"}
proxy {action: "start", id: "test", target_url: "http://localhost:3000", port: 8080}
```

### Parallel Test Isolation

```json
// Run tests for different packages in isolation
run {script_name: "test", id: "pkg-a", path: "./packages/a"}
run {script_name: "test", id: "pkg-b", path: "./packages/b"}
run {script_name: "test", id: "pkg-c", path: "./packages/c"}

// Each gets its own output buffer
proc {action: "output", process_id: "pkg-a"}
proc {action: "output", process_id: "pkg-b"}
proc {action: "output", process_id: "pkg-c"}
```

## Real-World Example: Full Test Workflow

```
User: "Run the test suite and fix any failures"

AI: Let me detect the project and run tests.

[detect {path: "."}]
→ Node.js project with pnpm

[run {script_name: "test", mode: "foreground"}]
→ {exit_code: 1, runtime: "32s"}

AI: Tests failed. Let me find the failures.

[proc {action: "output", process_id: "test", grep: "FAIL"}]
→ "FAIL src/components/UserProfile.test.tsx"

[proc {action: "output", process_id: "test", grep: "UserProfile", tail: 30}]
→ "Expected: 'John Doe'
    Received: undefined
    at UserProfile.test.tsx:45"

AI: The UserProfile component is returning undefined for the user name.
    Let me check the component.

[Read UserProfile.tsx]

AI: Found the issue. The component accesses user.name but
    the test mock provides user.fullName.

    Fix: Update test to use user.name or component to use user.fullName.

[Edit test file]

[run {script_name: "test", mode: "foreground"}]
→ {exit_code: 0}

AI: All tests pass now.
```

## Best Practices

1. **Use foreground mode** for CI pipelines
2. **Use foreground-raw** when you need immediate output
3. **Filter output** with grep for large test suites
4. **Clean up ports** before starting test servers
5. **Capture state** when debugging flaky tests
6. **Use unique IDs** when running parallel tests

## See Also

- [Process Management](/features/process-management) - Running and monitoring
- [CI/CD Integration](/use-cases/ci-cd-integration) - Detailed CI guide

# Test Infrastructure

This directory contains test fixtures and utilities for integration testing agnt's browser automation features.

## Directory Structure

```
testdata/
├── README.md                    # This file
├── webapps/                     # Test web applications
│   ├── static/                  # Static HTML test pages
│   │   ├── index.html          # Basic page for proxy testing
│   │   ├── errors.html         # Page that generates JS errors
│   │   ├── forms.html          # Form elements for interaction testing
│   │   └── spa.html            # Single-page app simulation
│   └── server/                  # Go test server
│       └── main.go             # Configurable test web server
├── mockagent/                   # Mock AI agent for PTY testing
│   └── main.go                 # Simulates Claude Code's Ink UI
└── scripts/                     # Test helper scripts
    └── run-integration.sh      # Run full integration test suite
```

## Test Categories

### 1. Proxy Integration Tests
- JS injection verification
- Error capture and logging
- WebSocket metrics forwarding
- Page session tracking

### 2. Browser Automation Tests
- Chrome launch/stop lifecycle
- Headless mode verification
- Proxy routing validation
- Screenshot capture (when chromedp is wired up)

### 3. PTY/Overlay Tests
- Message injection into agent
- Activity detection
- Enter-until-accepted mechanism
- Panel message formatting

### 4. End-to-End Tests
- Full `agnt run` workflow
- Browser → Proxy → Overlay → Agent flow
- Multi-session coordination

## Mock Agent

The `mockagent` simulates Claude Code's terminal UI behavior:

- Ink-style text input that accepts typed characters
- Produces output when "processing" a message
- Configurable response delays
- Controllable via environment variables for test scenarios

## Test Web Server

The test web server (`testdata/webapps/server/main.go`) provides:

- Static file serving from `webapps/static/`
- Configurable error injection
- Request logging for verification
- WebSocket echo for testing connections

## Running Tests

```bash
# Run all tests including integration
make test

# Run only integration tests
go test -v -tags=integration ./...

# Run browser automation tests (requires Chrome)
go test -v -tags=browser ./internal/browser/...

# Run with test web server
go test -v ./internal/proxy/... -webapp-port=18080
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `AGNT_TEST_CHROME_PATH` | Path to Chrome binary | auto-detect |
| `AGNT_TEST_HEADLESS` | Run browser tests headless | `true` |
| `AGNT_TEST_WEBAPP_PORT` | Test webapp server port | `18080` |
| `AGNT_TEST_TIMEOUT` | Test timeout in seconds | `30` |

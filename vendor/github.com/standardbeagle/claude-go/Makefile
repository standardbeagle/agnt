# Claude Go Library Makefile

.PHONY: build test test-integration test-short clean examples docs

# Build the library
build:
	go build -v ./...

# Run all tests (including integration tests that use real Claude CLI)
test: build
	go test -v -timeout=300s ./...

# Run only unit tests (skip integration tests)
test-short: build
	go test -v -short ./...

# Run integration tests only
test-integration: build
	go test -v -timeout=300s -run="Test.*" ./...

# Run specific test
test-basic: build
	go run cmd/test-runner/main.go basic

test-multiturn: build
	go run cmd/test-runner/main.go multiturn

test-files: build
	go run cmd/test-runner/main.go files

test-concurrent: build
	go run cmd/test-runner/main.go concurrent

test-complex: build
	go run cmd/test-runner/main.go complex

# Build examples
examples: build
	@echo "Building examples..."
	go build -o examples/basic/basic examples/basic/main.go
	go build -o examples/concurrent/concurrent examples/concurrent/main.go
	go build -o examples/interactive/interactive examples/interactive/main.go
	@echo "Examples built successfully!"

# Run examples
run-basic: examples
	./examples/basic/basic

run-concurrent: examples
	./examples/concurrent/concurrent

run-interactive: examples
	./examples/interactive/interactive

# Clean build artifacts
clean:
	go clean
	rm -f examples/*/basic examples/*/concurrent examples/*/interactive
	rm -rf /tmp/claude-test-* /tmp/claude-concurrent-test-* /tmp/claude-complex-*

# Install dependencies
deps:
	go mod tidy
	go mod download

# Run linter (if golangci-lint is installed)
lint:
	@which golangci-lint > /dev/null 2>&1 || (echo "golangci-lint not installed, skipping..."; exit 0)
	golangci-lint run

# Generate documentation
docs:
	@echo "Generating documentation..."
	go doc -all . > docs.txt
	@echo "Documentation generated in docs.txt"

# Check if Claude CLI is available
check-claude:
	@which claude > /dev/null 2>&1 || (echo "ERROR: Claude CLI not found in PATH"; exit 1)
	@echo "Claude CLI found: $$(which claude)"
	@claude --version

# Run a comprehensive test suite
test-all: check-claude build test-short test-basic test-multiturn test-files test-concurrent test-complex
	@echo "All tests completed successfully!"

# Development setup
setup: deps examples
	@echo "Development environment setup complete!"
	@echo "Run 'make test-all' to run all tests"
	@echo "Run 'make run-basic' to try the basic example"

# Help
help:
	@echo "Claude Go Library Makefile"
	@echo ""
	@echo "Available targets:"
	@echo "  build           - Build the library"
	@echo "  test            - Run all tests (including integration)"
	@echo "  test-short      - Run only unit tests"
	@echo "  test-integration- Run integration tests only"
	@echo "  test-basic      - Run basic test with test runner"
	@echo "  test-multiturn  - Run multi-turn conversation test"
	@echo "  test-files      - Run file operations test"
	@echo "  test-concurrent - Run concurrent sessions test"
	@echo "  test-complex    - Run complex multi-turn with files test"
	@echo "  examples        - Build all examples"
	@echo "  run-basic       - Run basic example"
	@echo "  run-concurrent  - Run concurrent example"
	@echo "  run-interactive - Run interactive example"
	@echo "  clean           - Clean build artifacts"
	@echo "  deps            - Install/update dependencies"
	@echo "  lint            - Run linter (if available)"
	@echo "  docs            - Generate documentation"
	@echo "  check-claude    - Check if Claude CLI is available"
	@echo "  test-all        - Run comprehensive test suite"
	@echo "  setup           - Set up development environment"
	@echo "  help            - Show this help message"
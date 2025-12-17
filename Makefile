.PHONY: build test clean install install-local run lint

# Binary names
BINARY := devtool-mcp
DAEMON_BINARY := devtool-mcp-daemon
AGENT_BINARY := agnt
AGENT_DAEMON_BINARY := agnt-daemon

# Default target
all: build

# Build both binaries (agnt is the source, devtool-mcp is a copy for MCP compatibility)
# Version is defined in cmd/agnt/main.go and managed by scripts/release.sh
build:
	go build -o $(AGENT_BINARY) ./cmd/agnt/
	@cp $(AGENT_BINARY) $(BINARY)

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Run benchmarks
bench:
	go test -bench=. -benchmem ./...

# Clean build artifacts
clean:
	rm -f $(BINARY) $(AGENT_BINARY)
	rm -f coverage.out coverage.html

# Install to GOPATH/bin (all binaries)
install: build
	@# Stop running daemon before installing new binaries
	@"$$(go env GOPATH)/bin/$(AGENT_BINARY)" daemon stop 2>/dev/null || true
	go install ./cmd/agnt/
	@cp "$$(go env GOPATH)/bin/$(AGENT_BINARY)" "$$(go env GOPATH)/bin/$(BINARY)"
	@cp "$$(go env GOPATH)/bin/$(AGENT_BINARY)" "$$(go env GOPATH)/bin/$(DAEMON_BINARY)"
	@cp "$$(go env GOPATH)/bin/$(AGENT_BINARY)" "$$(go env GOPATH)/bin/$(AGENT_DAEMON_BINARY)"
	@echo "Installed $(AGENT_BINARY), $(BINARY), $(DAEMON_BINARY), and $(AGENT_DAEMON_BINARY) to $$(go env GOPATH)/bin"

# Build and install to ~/.local/bin (all binaries)
install-local: build
	@# Stop running daemon before installing new binaries
	@~/.local/bin/$(AGENT_BINARY) daemon stop 2>/dev/null || true
	@mkdir -p ~/.local/bin
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(AGENT_BINARY)
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(BINARY)
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(DAEMON_BINARY)
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(AGENT_DAEMON_BINARY)
	@echo "Installed $(AGENT_BINARY), $(BINARY), $(DAEMON_BINARY), and $(AGENT_DAEMON_BINARY) to ~/.local/bin"
	@echo "Make sure ~/.local/bin is in your PATH"

# Run the server (for development)
run: build
	./$(AGENT_BINARY) serve

# Format code
fmt:
	go fmt ./...

# Vet code
vet:
	go vet ./...

# Run linter (requires golangci-lint)
lint:
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed" && exit 1)
	golangci-lint run ./...

# Update dependencies
deps:
	go mod tidy
	go mod verify

# Show help
help:
	@echo "Available targets:"
	@echo "  build         - Build agnt and devtool-mcp (copy of agnt)"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage report"
	@echo "  bench         - Run benchmarks"
	@echo "  clean         - Remove build artifacts"
	@echo "  install       - Install all binaries to GOPATH/bin"
	@echo "  install-local - Build and install all binaries to ~/.local/bin"
	@echo "  run           - Build and run the MCP server"
	@echo "  fmt           - Format code"
	@echo "  vet           - Vet code"
	@echo "  lint          - Run linter"
	@echo "  deps          - Update dependencies"
	@echo ""
	@echo "MCP registration (claude_desktop_config.json):"
	@echo '  "devtool": {'
	@echo '    "command": "devtool-mcp",'
	@echo '    "args": ["serve"]'
	@echo '  }'
	@echo ""
	@echo "Agent usage:"
	@echo "  agnt run claude --dangerously-skip-permissions"
	@echo "  agnt serve          # Run as MCP server"
	@echo "  agnt daemon status  # Check daemon status"

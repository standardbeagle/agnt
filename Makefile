.PHONY: build test clean install run lint

# Binary name
BINARY := devtool-mcp
VERSION := 0.1.0

# Build flags
LDFLAGS := -ldflags "-X main.serverVersion=$(VERSION)"

# Default target
all: build

# Build the binary
build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/devtool-mcp/

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
	rm -f $(BINARY)
	rm -f coverage.out coverage.html

# Install to GOPATH/bin
install:
	go install $(LDFLAGS) ./cmd/devtool-mcp/

# Run the server (for development)
run: build
	./$(BINARY)

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
	@echo "  build         - Build the binary"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage report"
	@echo "  bench         - Run benchmarks"
	@echo "  clean         - Remove build artifacts"
	@echo "  install       - Install to GOPATH/bin"
	@echo "  run           - Build and run the server"
	@echo "  fmt           - Format code"
	@echo "  vet           - Vet code"
	@echo "  lint          - Run linter"
	@echo "  deps          - Update dependencies"

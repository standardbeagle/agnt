.PHONY: build release test test-unit test-integration test-browser test-e2e test-isolated test-ssh test-flake clean clean-zombies install install-local install-windows run lint test-webapp mockagent generate generate-check vendor cross-compile cross-compile-check

# Binary names
BINARY := devtool-mcp
DAEMON_BINARY := devtool-mcp-daemon
AGENT_BINARY := agnt
AGENT_DAEMON_BINARY := agnt-daemon

# Default target
all: build

# Regenerate code-generated files. Currently just the __devtool API docs
# catalog (internal/tools/apidocs_gen.go), sourced from JSDoc blocks in
# internal/proxy/scripts/*.js via scripts/gen-apidocs.go.
#
# Run this any time you add / edit / rename a JSDoc block tagged @devtool.
# CI enforces drift via TestAPIDocsNoDrift in internal/tools.
generate:
	go run ./scripts/gen-apidocs.go \
		-scripts internal/proxy/scripts \
		-out internal/tools/apidocs_gen.go

# Check-only variant: non-zero exit if the generated file would change.
# Useful as a pre-commit hook or standalone CI step (the Go test harness
# already covers this in TestAPIDocsNoDrift).
generate-check:
	go run ./scripts/gen-apidocs.go \
		-scripts internal/proxy/scripts \
		-out internal/tools/apidocs_gen.go \
	-check

# Vendor dependencies
vendor:
	go mod vendor

# Build both binaries (agnt is the source, devtool-mcp is a copy for MCP compatibility)
# Version is defined in cmd/agnt/main.go and managed by scripts/release.sh
build:
	go build -o $(AGENT_BINARY) ./cmd/agnt/
	@cp $(AGENT_BINARY) $(BINARY)

# Production release build with optimized flags
# Strips debug info, adds version info, and removes file paths for security
# Version is automatically read from main.go
release:
	@echo "Building production release..."
	@VERSION=$(shell grep 'appVersion = ' cmd/agnt/main.go | sed 's/.*"\(.*\)"/\1/'); \
	LDFLAGS="-s -w -X main.appVersion=$$VERSION -buildid="; \
	go build -ldflags="$$LDFLAGS" -trimpath -o $(AGENT_BINARY) ./cmd/agnt/; \
	cp $(AGENT_BINARY) $(BINARY); \
	echo "Production build complete: $(AGENT_BINARY) v$$VERSION"

# Run tests (cleans up zombie test daemons first)
test: clean-zombies
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Run invasive process-namespace tests inside a PID + mount namespace.
#
# Tests tagged `procisolation` (daemon_orphan_pgid_test.go,
# platform/orphanpgid_unix_test.go) exercise host-global primitives —
# real /proc walks, real kill(2) syscalls against pgids whose leader is
# dead. Running them natively can reap unrelated processes owned by the
# same uid. This target places the test binary inside its own PID
# namespace via `unshare`, so /proc only lists processes spawned inside
# the namespace and kill syscalls cannot reach host pids.
#
# Requires: Linux kernel with user namespaces and unprivileged clone
# enabled (the default on modern distros, Ubuntu/Debian/Fedora/Arch,
# and WSL2). Skip-loud on non-Linux or restricted hosts.
test-isolated:
	@if [ "$$(uname -s)" != "Linux" ]; then \
		echo "SKIPPED: test-isolated requires Linux PID namespaces (host is $$(uname -s))"; \
		exit 0; \
	fi
	@if ! command -v unshare >/dev/null 2>&1; then \
		echo "SKIPPED: test-isolated requires util-linux unshare"; \
		exit 0; \
	fi
	@if ! unshare --user --pid --mount --fork --mount-proc true 2>/dev/null; then \
		echo "SKIPPED: user+pid namespaces unavailable (kernel.unprivileged_userns_clone disabled?)"; \
		exit 0; \
	fi
	@echo "Running procisolation tests inside unshare PID namespace..."
	unshare --user --pid --mount --fork --mount-proc \
		env -u AGNT_DISABLE_ORPHAN_SCAN \
		go test -tags procisolation -count=1 -v \
		./internal/daemon/... ./internal/platform/...

# Run the remote-SSH end-to-end tier. The in-process harness always runs; the
# tagged container smoke test runs when Docker or Podman is available and
# otherwise skips loudly. SSH_E2E_IMAGE can pin an alternate compatible image;
# set SSH_E2E_IMAGE and SSH_E2E_USER together for a conventional image exposing
# sshd on port 22 (SSH_E2E_USER without SSH_E2E_IMAGE is rejected).
test-ssh:
	go test -count=1 -v -tags=sshe2e ./internal/sshclient/...

# Hunt flakes via parallel stress run (50-count, 4-way parallel, shuffled)
test-flake: ## Hunt flakes via parallel stress run
	go test -race -count=50 -p 4 -shuffle=on ./internal/daemon/ ./cmd/agnt/

# Run unit tests only (excludes integration tests)
test-unit:
	go test -v ./...

# Run integration tests (requires external dependencies)
test-integration:
	go test -v -tags=integration ./...

# Run browser automation tests (requires Chrome)
test-browser:
	go test -v -tags=integration ./internal/browser/...

# Run Playwright e2e tests (installs/updates Chromium automatically)
test-e2e:
	cd e2e && npm install && npx playwright test

# Build test webapp server
test-webapp:
	go build -o testdata/webapps/server/webapp ./testdata/webapps/server/

# Build mock agent
mockagent:
	go build -o testdata/mockagent/mockagent ./testdata/mockagent/

# Run benchmarks
bench:
	go test -bench=. -benchmem ./...

# Clean build artifacts
clean: clean-zombies
	rm -f $(BINARY) $(AGENT_BINARY)
	rm -f coverage.out coverage.html

# Kill orphaned test daemon processes left behind by failed tests
# [a]gnt trick prevents pgrep from matching its own parent shell's cmdline
clean-zombies:
	@pids=$$(pgrep -f '[a]gnt daemon start --socket /tmp/Test' 2>/dev/null); \
	if [ -n "$$pids" ]; then \
		count=$$(echo "$$pids" | wc -l); \
		echo "Killing $$count orphaned test daemon(s)..."; \
		echo "$$pids" | xargs kill -TERM 2>/dev/null || true; \
		sleep 1; \
		pids2=$$(pgrep -f '[a]gnt daemon start --socket /tmp/Test' 2>/dev/null); \
		if [ -n "$$pids2" ]; then \
			echo "$$pids2" | xargs kill -9 2>/dev/null || true; \
		fi; \
		echo "Done."; \
	fi

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
	@# Stop running daemon. pkill sweep catches any stale instances that
	@# didn't respond to the graceful stop (e.g. old binaries on /run/user/1000
	@# from before the socket path was fixed to always use /tmp).
	@~/.local/bin/$(AGENT_BINARY) daemon stop 2>/dev/null || true
	@pkill -TERM -f '[a]gnt-daemon daemon start' 2>/dev/null || true
	@sleep 0.3
	@mkdir -p ~/.local/bin
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(AGENT_BINARY)
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(BINARY)
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(DAEMON_BINARY)
	@install -m 755 $(AGENT_BINARY) ~/.local/bin/$(AGENT_DAEMON_BINARY)
	@echo "Installed $(AGENT_BINARY), $(BINARY), $(DAEMON_BINARY), and $(AGENT_DAEMON_BINARY) to ~/.local/bin"
	@echo "Make sure ~/.local/bin is in your PATH"

# Cross-compile and install Windows binaries to Windows ~/.local/bin
WINDOWS_BIN := /mnt/c/Users/andyb/.local/bin
install-windows:
	GOOS=windows GOARCH=amd64 go build -o $(AGENT_BINARY).exe ./cmd/agnt/
	@# Stop running Windows daemon before installing
	@$(WINDOWS_BIN)/$(AGENT_BINARY).exe daemon stop 2>/dev/null || true
	@mkdir -p $(WINDOWS_BIN)
	@cp $(AGENT_BINARY).exe $(WINDOWS_BIN)/$(AGENT_BINARY).exe
	@cp $(AGENT_BINARY).exe $(WINDOWS_BIN)/$(BINARY).exe
	@cp $(AGENT_BINARY).exe $(WINDOWS_BIN)/$(DAEMON_BINARY).exe
	@cp $(AGENT_BINARY).exe $(WINDOWS_BIN)/$(AGENT_DAEMON_BINARY).exe
	@rm -f $(AGENT_BINARY).exe
	@echo "Installed Windows binaries to $(WINDOWS_BIN)"

# Cross-compile sanity check: build for windows/amd64 and darwin/arm64.
#
# Catches the class of bug fixed by the run.go/run_windows.go drift —
# missing platform stubs, divergent signatures, build-constraint typos.
# Exists because manual review caught that drift; CI must catch the next.
#
# CGO_ENABLED=0 is mandatory: the pty deps (creack/pty, aymanbagabas/go-pty)
# resolve to platform-specific pure-Go files when CGO is off, which is what
# enables cross-compilation without a target-platform C toolchain.
#
# Builds the entire module (./...), not just ./cmd/agnt/..., to surface
# drift in internal packages with platform-tagged files (the original
# breakage was in internal/process/run_windows.go).
cross-compile:
	@echo "==> Cross-compiling for windows/amd64..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o agnt-windows-amd64.exe ./cmd/agnt/
	@file agnt-windows-amd64.exe | grep -q 'PE32+ executable' || (echo "FAIL: agnt-windows-amd64.exe is not a PE32+ binary"; exit 1)
	@echo "==> Cross-compiling for darwin/arm64..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o agnt-darwin-arm64 ./cmd/agnt/
	@file agnt-darwin-arm64 | grep -q 'Mach-O 64-bit.*arm64' || (echo "FAIL: agnt-darwin-arm64 is not a Mach-O arm64 binary"; exit 1)
	@echo "==> Building full module for windows/amd64 (catches internal-package drift)..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
	@echo "==> Building full module for darwin/arm64 (catches internal-package drift)..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
	@rm -f agnt-windows-amd64.exe agnt-darwin-arm64
	@echo "Cross-compile check passed (windows/amd64, darwin/arm64)"

# Check-only variant for CI: same as cross-compile but without producing
# named binaries. Faster — `go build` with no -o discards output.
cross-compile-check:
	@echo "==> Checking windows/amd64 build..."
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
	@echo "==> Checking darwin/arm64 build..."
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
	@echo "Cross-compile check passed"

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
	@echo "  build            - Build agnt and devtool-mcp (copy of agnt)"
	@echo "  release          - Build production release with optimizations and version info"
	@echo "  test             - Run all tests (excludes procisolation tag)"
	@echo "  test-isolated    - Run procisolation tests inside unshare PID namespace (Linux)"
	@echo "  test-flake       - Hunt flakes via parallel stress run (50-count, shuffled)"
	@echo "  test-unit        - Run unit tests only"
	@echo "  test-integration - Run integration tests (requires dependencies)"
	@echo "  test-browser     - Run browser automation tests (requires Chrome)"
	@echo "  test-e2e         - Run Playwright e2e tests (auto-installs Chromium)"
	@echo "  test-coverage    - Run tests with coverage report"
	@echo "  test-webapp      - Build test webapp server"
	@echo "  mockagent        - Build mock agent for PTY testing"
	@echo "  bench            - Run benchmarks"
	@echo "  clean            - Remove build artifacts and kill zombie daemons"
	@echo "  clean-zombies    - Kill orphaned test daemon processes"
	@echo "  install          - Install all binaries to GOPATH/bin"
	@echo "  install-local    - Build and install all binaries to ~/.local/bin"
	@echo "  install-windows  - Cross-compile and install Windows binaries"
	@echo "  cross-compile      - Verify windows/amd64 + darwin/arm64 builds (with file-type assertion)"
	@echo "  cross-compile-check - CI-friendly cross-compile check (no binary output)"
	@echo "  run              - Build and run the MCP server"
	@echo "  fmt              - Format code"
	@echo "  vet              - Vet code"
	@echo "  lint             - Run linter"
	@echo "  deps             - Update dependencies"
	@echo ""
	@echo "MCP registration (claude_desktop_config.json):"
	@echo '  "devtool": {'
	@echo '    "command": "devtool-mcp",'
	@echo '    "args": ["mcp"]'
	@echo '  }'
	@echo ""
	@echo "Agent usage:"
	@echo "  agnt run claude --dangerously-skip-permissions"
	@echo "  agnt mcp          # Run as MCP server"
	@echo "  agnt daemon status  # Check daemon status"

.PHONY: build release test test-unit test-integration test-browser test-e2e e2e-publish-browser test-chrome-e2e test-isolated test-ssh test-ssh-coverage test-flake check-dirty-tree clean clean-zombies install install-local install-windows install-hooks run lint test-webapp mockagent generate generate-check vendor cross-compile cross-compile-check demo demo-publish demo-check demo-engine-test demo-mux-check demo-inspect-check

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
	go test -p 1 -v ./...

# Run tests with coverage
test-coverage:
	go test -p 1 -v -coverprofile=coverage.out ./...
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

# Enforce a line-coverage floor on the remote-ssh reconnect packages.
#
# task 10d acceptance criterion 3: internal/sshclient AND internal/sessionhost
# must each hold >= SSH_COVERAGE_MIN% line coverage. Measures each package
# ALONE (not ./...): internal/sshclient/testenv is a separate in-process
# harness package with its own, intentionally lower, coverage and is not part
# of this gate. The reconnect chaos suite (reconnect_chaos_test.go) is one of
# the untagged in-process tests that keeps internal/sshclient above the floor.
#
# Reusable by CI (.github/workflows/ssh-reconnect.yml) so the workflow step
# stays a one-liner. Writes throwaway profiles under $TMPDIR, never the tree.
SSH_COVERAGE_MIN ?= 70
test-ssh-coverage:
	@fail=0; \
	for pkg in internal/sshclient internal/sessionhost; do \
		prof="$$(mktemp)"; \
		if ! go test -count=1 -coverprofile="$$prof" ./$$pkg; then \
			echo "FAIL: tests failed in $$pkg"; rm -f "$$prof"; exit 1; \
		fi; \
		pct="$$(go tool cover -func="$$prof" | awk '/^total:/ {sub(/%/,"",$$3); print $$3}')"; \
		rm -f "$$prof"; \
		if awk "BEGIN{exit !($$pct+0 >= $(SSH_COVERAGE_MIN))}"; then \
			echo "PASS: $$pkg coverage $$pct% (min $(SSH_COVERAGE_MIN)%)"; \
		else \
			echo "FAIL: $$pkg coverage $$pct% below $(SSH_COVERAGE_MIN)%"; fail=1; \
		fi; \
	done; \
	if [ "$$fail" != 0 ]; then exit 1; fi; \
	echo "test-ssh-coverage gate passed (both packages >= $(SSH_COVERAGE_MIN)%)"

# Standing guard: fail the build if the test suite leaves the working tree
# dirty. Runs the full suite twice under a PTY (so PTY-gated tests actually
# EXECUTE — a TTY-less run skips them and gives a FALSE CLEAN) and requires
# `git status --porcelain` to be empty after BOTH runs (idempotent, not merely
# clean-once). On dirtiness it names the offending path(s) and points at
# .claude/rules/testing-parallel-package-flakes.md § source-tree-pollution.
#
# The default suite command is `go test -p 1 -count=1 -v ./...` — -count=1 is
# load-bearing: it defeats the test cache so the SECOND run actually executes
# (a cached run cannot prove idempotency). The logic lives in
# scripts/check-tree-clean.sh so this target — and any CI step — stays a
# one-liner (same pattern as test-ssh-coverage). Env overrides:
# TREE_CLEAN_RUNS, TREE_CLEAN_TEST_CMD.
check-dirty-tree: clean-zombies
	./scripts/check-tree-clean.sh

# Hunt flakes via parallel stress run (50-count, 4-way parallel, shuffled)
test-flake: ## Hunt flakes via parallel stress run
	go test -race -count=50 -p 4 -shuffle=on ./internal/daemon/ ./cmd/agnt/

# Run unit tests only (excludes integration tests)
test-unit:
	go test -p 1 -v ./...

# Run integration tests (requires external dependencies)
test-integration:
	go test -v -tags=integration ./...

# Run browser automation tests (requires Chrome)
test-browser:
	go test -v -tags=integration ./internal/browser/...

# Run real-Chrome end-to-end tests separately from the general suite.
# These tests require Chrome/Chromium and an otherwise unloaded machine;
# renderer scheduling starvation under CPU oversubscription is non-deterministic.
test-chrome-e2e:
	go test -count=1 -v -tags=chromee2e ./internal/proxy/... ./internal/chromedp/...

# Run Playwright e2e tests (installs/updates Chromium automatically)
test-e2e:
	cd e2e && npm install && npx playwright test

# Run the P10 walkthrough-publish E2E security gate — real-browser tier (Tier B).
#
# Tier A (the host-safe, pure-Go end-to-end security/restart/revoke journey plus
# the concurrency gate) needs no special target: it runs under the normal
# `go test ./internal/proxy/... ./internal/daemon/... ./internal/publish/...`,
# including under `-race`.
#
# This target is Tier B: it drives the REAL served /s/{token} artifact in real
# Chrome and asserts the RolePublic bundle loads, window.__devtool is ABSENT, the
# variant applies (SPA reapply), the player advances, and feedback submits end to
# end. It is env-gated by skipIfNoBrowser, so it SKIPS LOUDLY when no Chrome is
# present rather than silently passing. -count=1 defeats the test cache so the
# browser path actually executes.
e2e-publish-browser:
	go test -v -count=1 -tags=chromee2e -run 'TestE2E_PublicPlane_RealBrowser' ./internal/proxy/

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

# Point git at the tracked hook directory (.githooks/pre-commit). Idempotent —
# run once per clone, or again to refresh after pulling a hook change.
install-hooks:
	git config core.hooksPath .githooks
	@echo "core.hooksPath -> .githooks (tracked pre-commit hook is now active)"

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

# Regenerate a docs demo video (scripted engine: VHS for CLI + Playwright for browser)
# Usage: make demo NAME=vhs-spiral [DEMOFLAGS=--only=attempt-1]
demo:
	cd docs-site/screenshots && node engine/demo.mjs demos/$(NAME) $(DEMOFLAGS)

# Publish a rendered demo into the docs-site as committed assets (video + poster
# + animated README/GitHub webp), then print the <ModeVideo/> embed snippet.
# Fails loud if the source webm is missing (run `make demo NAME=<x>` first).
# Usage: make demo-publish NAME=vhs-spiral
demo-publish:
	scripts/demo-publish.sh $(NAME)

# Validate EVERY demos/*/demo.json against the demo-engine schema. Pure node —
# no ffmpeg, no chromium, no daemon. Fails loud naming the offending file.
demo-check:
	cd docs-site/screenshots && node engine/check-demos.mjs

# Unit tests for the demo engine's pure helpers (final-mux graph, narration gating).
demo-engine-test:
	cd docs-site/screenshots && node --test engine/test/

# Integration check for the final-mux upgrades: runs the REAL ffmpeg invocation
# on a synthetic fixture and asserts the brand-logo pixels land in the output and
# the narration measures at the EBU R128 target loudness. Loud-skips without ffmpeg.
demo-mux-check:
	cd docs-site/screenshots && node engine/test/integration-mux.mjs

# Integration check for --inspect: runs REAL ffmpeg on a synthetic take+marks
# fixture and asserts the contact-sheet PNG is produced at the predicted geometry
# and the run stays read-only over mezzanine/output. Loud-skips without ffmpeg.
demo-inspect-check:
	cd docs-site/screenshots && node engine/test/integration-inspect.mjs

# Show help
help:
	@echo "Available targets:"
	@echo "  build            - Build agnt and devtool-mcp (copy of agnt)"
	@echo "  release          - Build production release with optimizations and version info"
	@echo "  test             - Run all tests (excludes procisolation tag)"
	@echo "  test-isolated    - Run procisolation tests inside unshare PID namespace (Linux)"
	@echo "  test-ssh-coverage - Enforce >=70% line coverage on sshclient + sessionhost"
	@echo "  test-flake       - Hunt flakes via parallel stress run (50-count, shuffled)"
	@echo "  check-dirty-tree - Fail if the suite dirties the working tree (2x under PTY)"
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

.PHONY: all vendor-check test-coverage bench test-pkg fmt vet deps help build release test test-unit test-integration test-browser test-e2e e2e-publish-browser test-chrome-e2e test-js test-js-install test-isolated test-ssh test-ssh-coverage test-flake check-dirty-tree clean clean-zombies install install-local install-windows install-hooks run lint test-webapp mockagent generate generate-check vendor cross-compile cross-compile-check demo demo-publish demo-check demo-engine-test demo-mux-check demo-inspect-check demo-publish-check demo-assemble-check

# Binary names
BINARY := devtool-mcp
DAEMON_BINARY := devtool-mcp-daemon
AGENT_BINARY := agnt
AGENT_DAEMON_BINARY := agnt-daemon
LOCAL_BIN ?= $(HOME)/.local/bin

# Default target: build, plus the agnt-daemon copy (`build` alone makes agnt
# and devtool-mcp). agnt-daemon is what daemon auto-start execs, because a
# sandbox can forbid a binary from forking itself. The legacy
# devtool-mcp-daemon copy is made by the install targets only.
all: build
	@cp $(AGENT_BINARY) $(AGENT_DAEMON_BINARY)

# Regenerate code-generated files. Currently just the __devtool API docs
# catalog (internal/tools/apidocs_gen.go), sourced from JSDoc blocks in
# internal/proxy/scripts/*.js via scripts/gen-apidocs.go.
#
# Run this any time you add / edit / rename a JSDoc block tagged @devtool.
# CI enforces drift via TestAPIDocsNoDrift in internal/tools, which the
# full-suite job in .github/workflows/test-linux.yml runs.
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

# Vendor dependencies. vendor/ is a plain copy of the module versions in
# go.mod: never edit a file under it. A fix to a vendored module goes
# upstream and arrives here as a version bump.
vendor:
	GOWORK=off go mod vendor

# Guard for the rule above: fail if vendor/ is not exactly what `go mod vendor`
# would write. Vendors into a temp directory and compares, so the working tree
# is never touched. A hand-edited vendored file, or a go.mod bump that was not
# re-vendored, fails here. Run by CI (test-linux.yml) and the release build.
#
# Files the repo's .gitignore excludes are dropped from the fresh copy before
# comparing: `go mod vendor` writes vendor/cloud.google.com/go/go.work, the
# root .gitignore ignores go.work, so a clean checkout never has it.
#
# GOWORK=off (here and in `vendor`): a go.work in this directory or any parent
# puts go in workspace mode, where `go mod vendor` refuses to run. The vendor
# tree is defined by go.mod alone, which is also what `go install` builds.
vendor-check:
	@tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	GOWORK=off go mod vendor -o "$$tmp/vendor" || exit 1; \
	( cd "$$tmp" && find vendor -type f ) | git check-ignore --stdin | \
		while IFS= read -r f; do rm -f "$$tmp/$$f"; done; \
	if diff -r vendor "$$tmp/vendor" > "$$tmp/diff" 2>&1; then \
		echo "vendor-check passed: vendor/ matches go.mod"; \
	else \
		echo "FAIL: vendor/ differs from what go.mod resolves to:"; \
		head -40 "$$tmp/diff"; \
		echo "A patch must not live only in vendor/ (it never reaches go install"; \
		echo "builds and the next go mod vendor drops it). Send it upstream and"; \
		echo "bump the version, or run make vendor if go.mod changed."; \
		exit 1; \
	fi

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

# Hunt flakes via a shuffled stress run (50-count). Packages stay serial (-p 1):
# -p 4 reintroduces the cross-package port contention that reads as a flake.
test-flake: ## Hunt flakes via shuffled stress run
	go test -race -count=50 -p 1 -shuffle=on ./internal/daemon/ ./cmd/agnt/

# Focused test run: one package (or pattern) and optionally one test regex.
#   make test-pkg PKG=./internal/incident
#   make test-pkg PKG=./internal/overlay RUN='TestPanelRefresh'
# Serial (-p 1) like the full gate, but only the packages you name.
PKG ?= ./...
RUN ?=
test-pkg:
	go test -p 1 -count=1 $(if $(RUN),-run '$(RUN)') $(PKG)

# Default suite without verbose output or the zombie sweep; PKG narrows it.
test-unit:
	go test -p 1 $(PKG)

# Run integration tests (requires external dependencies)
test-integration:
	go test -p 1 -v -tags=integration ./...

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

# DOM tier for the shipped instrumentation scripts: vitest + jsdom drives the
# real audit JS over a real document (internal/proxy/scripts/jstest).
#
# Deliberately OUTSIDE `make test`: the Go suite must never depend on a node
# install, so this target loud-skips when npm is absent rather than failing.
# jsdom answers for stylesheets, attributes and computed styles; anything
# needing layout or paint belongs to the chromee2e tier instead.
JSTEST_DIR := internal/proxy/scripts/jstest
JSFILTER ?=

# Installs from the lockfile only when it is newer than what is installed, so a
# local rerun does not pay for `npm ci` (which deletes and rebuilds node_modules).
# `make test-js-install` forces it.
$(JSTEST_DIR)/node_modules/.package-lock.json: $(JSTEST_DIR)/package-lock.json
	cd $(JSTEST_DIR) && npm ci --no-audit --no-fund

test-js-install:
	cd $(JSTEST_DIR) && npm ci --no-audit --no-fund

# Tests that build their own JSDOM window (load-bundle.js) opt out of the default
# jsdom environment with `// @vitest-environment node`; keep jsdom only where a
# test reads the global document. Narrow a run with JSFILTER=core-transport.
test-js:
	@if ! command -v npm >/dev/null 2>&1; then \
		echo "SKIPPED: test-js requires npm (node) on PATH"; \
		exit 0; \
	fi
	@$(MAKE) --no-print-directory $(JSTEST_DIR)/node_modules/.package-lock.json
	cd $(JSTEST_DIR) && npm test -- $(JSFILTER)

# Build test webapp server
test-webapp:
	go build -o testdata/webapps/server/webapp ./testdata/webapps/server/

# Build mock agent
mockagent:
	go build -o testdata/mockagent/mockagent ./testdata/mockagent/

# Run benchmarks only: -run '^$$' keeps the ordinary tests out of the timing.
#   make bench PKG=./internal/incident BENCH=Bus_EndToEnd
#   make bench PKG=./internal/overlay BENCH=Panel COUNT=5
BENCH ?= .
COUNT ?= 1
bench:
	go test -p 1 -run '^$$' -bench='$(BENCH)' -benchmem -count=$(COUNT) $(PKG)

# Clean build artifacts
clean: clean-zombies
	rm -f $(BINARY) $(AGENT_BINARY) $(AGENT_DAEMON_BINARY)
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
	@set -eu; \
	mkdir -p "$(LOCAL_BIN)"; \
	dest=$$(cd "$(LOCAL_BIN)" && pwd -P); \
	stage=$$(mktemp -d "$$dest/.agnt-install.XXXXXX"); \
	trap 'rm -rf "$$stage"' EXIT HUP INT TERM; \
	for name in $(AGENT_BINARY) $(BINARY) $(DAEMON_BINARY) $(AGENT_DAEMON_BINARY); do \
		install -m 755 "$(AGENT_BINARY)" "$$stage/$$name"; \
	done; \
	"$(CURDIR)/$(AGENT_BINARY)" daemon stop || \
		echo "WARNING: Daemon stop did not succeed. If a daemon is running, restart it before use."; \
	for name in $(AGENT_BINARY) $(BINARY) $(DAEMON_BINARY) $(AGENT_DAEMON_BINARY); do \
		mv -f "$$stage/$$name" "$$dest/$$name"; \
	done; \
	echo "Installed all four binaries to $$dest"; \
	active=$$(command -v $(AGENT_BINARY) || true); \
	echo "PATH resolves $(AGENT_BINARY) to: $${active:-not found}"; \
	if [ "$$active" != "$$dest/$(AGENT_BINARY)" ]; then \
		echo "WARNING: Put $$dest first in PATH and run hash -r in your shell."; \
	fi; \
	echo "Restart existing agnt sessions and MCP clients to load the new binary."

# Cross-compile and install Windows binaries to Windows ~/.local/bin (from WSL).
# WINDOWS_BIN defaults to <Windows profile>/.local/bin, derived by asking
# cmd.exe for %USERPROFILE%; override it on the command line otherwise:
#   make install-windows WINDOWS_BIN=/mnt/c/Users/<you>/.local/bin
WINDOWS_BIN ?= $(shell p="$$(cmd.exe /c 'echo %USERPROFILE%' 2>/dev/null | tr -d '\r')"; [ -n "$$p" ] && wslpath "$$p" 2>/dev/null | sed 's|$$|/.local/bin|')
install-windows:
	@if [ -z "$(WINDOWS_BIN)" ]; then \
		echo "install-windows: could not find the Windows profile directory (not WSL?)."; \
		echo "Set it explicitly: make install-windows WINDOWS_BIN=/mnt/c/Users/<you>/.local/bin"; \
		exit 1; \
	fi
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

# Integration check for --publish: runs the REAL ffmpeg renders on a synthetic
# clip into a temp docs-site and asserts the poster/animation geometry, the
# verbatim clip copy, and that a failed publish writes nothing. Loud-skips
# without ffmpeg.
demo-publish-check:
	cd docs-site/screenshots && node engine/test/integration-publish.mjs

# Assembly smoke: run the committed ci-fixture through `--assemble-only` and
# assert a playable output of the expected duration. ffmpeg + node only — no
# Chrome, VHS, edge-tts, or daemon. Loud-skips without ffmpeg.
demo-assemble-check:
	cd docs-site/screenshots && node engine/test/assemble-smoke.mjs

# Show help
help:
	@echo "Available targets:"
	@echo "  all              - build, plus the agnt-daemon copy"
	@echo "  build            - Build agnt and devtool-mcp (copy of agnt)"
	@echo "  vendor-check     - Fail if vendor/ differs from what go.mod resolves to"
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
	@echo "  test-js          - DOM tests for the injected scripts (vitest + jsdom)"
	@echo "  test-webapp      - Build test webapp server"
	@echo "  mockagent        - Build mock agent for PTY testing"
	@echo "  bench            - Run benchmarks"
	@echo "  clean            - Remove build artifacts and kill zombie daemons"
	@echo "  clean-zombies    - Kill orphaned test daemon processes"
	@echo "  install          - Install all binaries to GOPATH/bin"
	@echo "  install-local    - Install all binaries to LOCAL_BIN (default: ~/.local/bin)"
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

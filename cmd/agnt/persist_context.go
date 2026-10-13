package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/agnt/internal/agentadapter"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// Managed-block markers delimiting the agnt-owned region inside an agent's
// always-loaded context file (AGENTS.md, GEMINI.md, …). Content between them is
// rewritten on every `agnt run`; anything outside is the user's and untouched.
const (
	persistBlockBegin = "<!-- agnt:begin (managed by `agnt run` — do not edit inside this block) -->"
	persistBlockEnd   = "<!-- agnt:end -->"
)

// upsertManagedBlock returns existing with the agnt managed block set to body.
// If the markers already exist, the region between them is replaced in place;
// otherwise the block is appended. The result is idempotent: feeding the output
// back in with the same body yields an identical string. Pure — no I/O — so it
// is unit-testable without a filesystem.
func upsertManagedBlock(existing, body string) string {
	block := persistBlockBegin + "\n" + strings.TrimRight(body, "\n") + "\n" + persistBlockEnd

	begin := strings.Index(existing, persistBlockBegin)
	if begin >= 0 {
		// Find the end marker after the begin marker.
		rest := existing[begin:]
		endRel := strings.Index(rest, persistBlockEnd)
		if endRel >= 0 {
			endAbs := begin + endRel + len(persistBlockEnd)
			return existing[:begin] + block + existing[endAbs:]
		}
		// Begin marker without a matching end (corrupted) — replace from begin
		// to end-of-file with a clean block.
		return existing[:begin] + block + "\n"
	}

	if strings.TrimSpace(existing) == "" {
		return block + "\n"
	}
	// Append, separated by a blank line.
	return strings.TrimRight(existing, "\n") + "\n\n" + block + "\n"
}

// unsteeredAgentNotice returns the one-line message for a coding session whose
// agent receives no agnt guidance by any channel, or "" when guidance was
// delivered or the user switched it off. Delivery in a coding session is
// either a launch flag (Claude) or the persisted context file; a registered
// agent with neither — aider, whose CONVENTIONS.md is read only with `--read`
// — would otherwise run with no agnt context and nothing saying so.
//
// An unrecognized command (no support-matrix row) is not reported: `agnt run
// bash` is not an agent missing its instructions.
func unsteeredAgentNotice(adapter agentadapter.Adapter, persisted bool, baseArgs []string, prompt string) string {
	if adapter == nil || persisted || prompt == "" || agentadapter.InjectionDisabled(adapter) {
		return ""
	}
	support, known := supportMatrix[adapter.Name()]
	if !known || support.ContextFile != "" {
		// Unknown command, or a context-file agent whose write was skipped
		// (persist-context false) or failed (already logged).
		return ""
	}
	if len(adapter.BuildArgs(baseArgs, prompt)) > len(baseArgs) {
		return "" // flag-delivered
	}
	name := adapter.Name()
	return fmt.Sprintf("%s loads no project context file on its own, so agnt's tool guidance is not delivered to it in this session. Give it the guidance through its own mechanism (aider: `--read <file>`), or set `ai { adapters { %s { disabled true } } }` to silence this notice.",
		name, name)
}

// persistAdapterContext is writePersistentContext for a resolved adapter. It
// is the call site's entry point because it can see what the adapter's name
// alone cannot: a `disabled true` override, which must switch off every
// delivery channel, this file write included.
func persistAdapterContext(adapter agentadapter.Adapter, projectDir, prompt string) bool {
	if adapter == nil || agentadapter.InjectionDisabled(adapter) {
		return false
	}
	return writePersistentContext(adapter.Name(), projectDir, prompt)
}

// writePersistentContext writes the agnt steering block into adapterName's
// always-loaded context file under projectDir, so non-Claude agents keep the
// guidance in context every turn without a visible startup stdin message.
//
// It is a no-op (nil error) when the agent has no context file (e.g. Claude,
// which uses --append-system-prompt) or when persistence is disabled in config.
// The write is skipped when the file already contains an identical block, so
// re-running `agnt run` does not churn the file or its mtime. Failures are
// logged and swallowed: a missing AGENTS.md write must never break launch.
//
// It also declines for an adapter name with no support-matrix row. That is the
// Universal fallback adapter — `agnt run <any command>` — and writing the
// generic AGENTS.md there dropped a file into the repo for `agnt run bash`.
// Only an agent agnt knows loads a given file gets that file.
//
// The config is resolved through loadResolvedConfig so a --config override is
// honoured: `persist-context false` in the file the user pointed at must win.
func writePersistentContext(adapterName, projectDir, prompt string) bool {
	if adapterName == "claude" || projectDir == "" || prompt == "" {
		return false
	}
	support, known := supportMatrix[adapterName]
	if !known || support.ContextFile == "" {
		return false
	}
	cfg, err := loadResolvedConfig(projectDir)
	if err != nil {
		cfg = config.DefaultAgntConfig()
	}
	if !cfg.AI.PersistContextEnabled() {
		return false
	}
	file := support.ContextFile
	path := filepath.Join(projectDir, file)

	existing := ""
	if b, err := os.ReadFile(path); err == nil {
		existing = string(b)
	}

	updated := upsertManagedBlock(existing, prompt)
	if updated == existing {
		return true // already current — no churn, but delivery is available.
	}

	// Atomic write: temp file + rename, so a crash mid-write cannot truncate
	// the user's AGENTS.md.
	tmp := path + ".agnt.tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o644); err != nil {
		debug.Log("run", "persist context: write temp %s: %v", tmp, err)
		return false
	}
	if err := os.Rename(tmp, path); err != nil {
		debug.Log("run", "persist context: rename %s: %v", path, err)
		_ = os.Remove(tmp)
		return false
	}
	return true
}

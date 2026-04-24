package agentadapter

import (
	"os"
	"path/filepath"
	"time"
)

// kimiAgentFileFlag is the CLI flag kimi-cli uses to receive a system prompt
// via a file. This is the preferred injection strategy: writing the prompt
// to a temp file and passing its path avoids stdin timing issues and is
// the approach kimi-cli documents for agent-file use.
const kimiAgentFileFlag = "--agent-file"

// KimiTransportMode names the MCP transport modes kimi-cli supports.
type KimiTransportMode string

const (
	// KimiTransportCommand is the default local-executable transport.
	KimiTransportCommand KimiTransportMode = "command"
	// KimiTransportSSE is the server-sent events over HTTP transport.
	KimiTransportSSE KimiTransportMode = "sse"
	// KimiTransportStreamable is the streamable HTTP transport.
	KimiTransportStreamable KimiTransportMode = "streamable"
)

// kimiAdapter injects the agnt system prompt into kimi-cli via the
// --agent-file flag. kimi-cli reads the file on startup and uses its
// contents as additional context for the session.
//
// The adapter supports three MCP transport modes (command, sse,
// streamable) selectable via KimiOptions.Transport; the transport
// is forwarded as --transport <mode> when non-empty. Additional
// kimi-cli flags (--config, --mcp, --verbose, --debug) are passed
// through as ExtraArgs.
//
// Prompt injection strategy: flag-based (BuildArgs appends
// --agent-file <tmp-path>; InitialStdin returns nil). The temp file
// written by BuildArgs is created via os.CreateTemp and its path is
// appended to the returned argv. Callers that do not execute the
// command (e.g. tests) are responsible for removing the file if
// they choose; the normal agnt run path lets the OS clean it up on
// process exit since it is in the system temp dir.
type kimiAdapter struct {
	flag      string // --agent-file or override
	transport KimiTransportMode
	extraArgs []string
}

// newKimiAdapter creates a kimiAdapter with default settings.
func newKimiAdapter() *kimiAdapter {
	return &kimiAdapter{flag: kimiAgentFileFlag}
}

// KimiOptions configures a kimi adapter beyond the defaults.
type KimiOptions struct {
	// Transport selects the MCP transport mode. Empty means kimi uses
	// its default (command). Valid values: "command", "sse", "streamable".
	Transport KimiTransportMode

	// ExtraArgs are additional flags passed verbatim before any
	// injection flags. Useful for --config, --mcp, --verbose, --debug.
	ExtraArgs []string
}

// newKimiAdapterWithOptions creates a kimiAdapter with explicit options.
func newKimiAdapterWithOptions(opts KimiOptions) *kimiAdapter {
	a := newKimiAdapter()
	a.transport = opts.Transport
	a.extraArgs = opts.ExtraArgs
	return a
}

func (k *kimiAdapter) Name() string { return "kimi-cli" }

func (k *kimiAdapter) Matches(command string) bool {
	_, ok := resolveBaseName(command, []string{"kimi-cli", "kimi"})
	return ok
}

// BuildArgs appends --agent-file <tmp> and, when configured,
// --transport <mode> to baseArgs. Returns baseArgs unchanged when
// prompt is empty (no injection). The temp file contains the prompt
// text and is created in the OS temp directory.
func (k *kimiAdapter) BuildArgs(baseArgs []string, prompt string) []string {
	out := make([]string, 0, len(baseArgs)+len(k.extraArgs)+4)
	out = append(out, baseArgs...)
	out = append(out, k.extraArgs...)

	if k.transport != "" && k.transport != KimiTransportCommand {
		out = append(out, "--transport", string(k.transport))
	}

	if prompt == "" {
		return out
	}

	flag := k.flag
	if flag == "" {
		flag = kimiAgentFileFlag
	}

	path, err := writeKimiAgentSpec(prompt)
	if err != nil {
		// Fall back to no injection rather than crashing the run path.
		return out
	}
	out = append(out, flag, path)
	return out
}

// InitialStdin returns nil — kimi uses file-based injection, not stdin.
func (k *kimiAdapter) InitialStdin(_ string) []byte { return nil }

// StdinDelay returns 0 — kimi is flag-based; no stdin delay needed.
func (k *kimiAdapter) StdinDelay() time.Duration { return 0 }

// writeKimiAgentSpec writes the agnt prompt and a kimi-cli agent spec YAML
// to a temp directory and returns the path to the YAML spec file.
//
// kimi-cli's --agent-file flag expects a YAML agent spec (not plain text).
// The spec extends kimi's built-in "default" agent (preserving all default
// tools and subagents) and overrides system_prompt_path with the agnt prompt.
// Callers own the temp directory; the OS cleans it up on exit since it lives
// in os.TempDir().
func writeKimiAgentSpec(prompt string) (string, error) {
	dir, err := os.MkdirTemp("", "agnt-kimi-*")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(prompt), 0600); err != nil {
		os.RemoveAll(dir) //nolint:errcheck
		return "", err
	}
	const specContent = "version: 1\nagent:\n  extend: default\n  name: agnt\n  system_prompt_path: ./prompt.md\n"
	specPath := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(specPath, []byte(specContent), 0600); err != nil {
		os.RemoveAll(dir) //nolint:errcheck
		return "", err
	}
	return specPath, nil
}

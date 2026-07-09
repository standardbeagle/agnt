// Package agentadapter provides a unified abstraction for injecting the
// agnt system prompt into AI coding agents.
//
// Historically, cmd/agnt/run.go and cmd/agnt/run_windows.go both carried
// hardcoded special-cases: Claude Code received the prompt via the
// `--append-system-prompt` flag, while every other known agent
// (gemini, copilot, aider, cursor, cursor-agent, opencode, kimi,
// kimi-cli, auggie) received it as initial stdin text 500ms after launch.
// The list and the per-agent strategy were duplicated across the unix
// and windows entrypoints, and there was no way for a project to
// override behavior per-agent.
//
// This package centralizes that logic. An [Adapter] captures the
// knowledge of how to recognize a given AI CLI and how to hand it the
// agnt system prompt. The built-in registry ([Registry]) ships the same
// set of agents the old hardcoded lists supported, preserving behavior.
// New agents are added by registering a new Adapter; per-agent overrides
// (custom flag name, stdin delay, or disabling injection entirely)
// come from the `ai.adapters` block in `.agnt.kdl`.
package agentadapter

import "time"

// DefaultStdinDelay is the delay between agent launch and setup-mode stdin
// prompt delivery for stdin-capable adapters.
const DefaultStdinDelay = 500 * time.Millisecond

// Adapter describes how to recognize a specific AI coding agent and how
// to hand it the agnt system prompt.
//
// Implementations must be safe for concurrent use and must not mutate
// the inputs they receive. Two injection strategies are supported:
//
//   - Flag-based: BuildArgs returns a modified argv; InitialStdin
//     returns nil. Example: Claude Code (--append-system-prompt).
//   - Stdin-capable: BuildArgs returns baseArgs unchanged; InitialStdin
//     returns the bytes to write to the child's stdin after StdinDelay
//     has elapsed when the caller chooses stdin delivery (setup mode).
//     Example: gemini, aider, cursor, etc.
//
// An adapter may return an empty argv change and nil stdin if the
// prompt is empty — callers must tolerate nil/empty returns without
// injecting anything.
type Adapter interface {
	// Name returns the canonical lowercase identifier for the agent
	// (e.g. "claude", "gemini"). Used for config lookups and logging.
	Name() string

	// Matches reports whether the given command invokes this agent.
	// Implementations must handle bare names ("claude"), absolute paths
	// ("/usr/bin/claude"), relative paths ("./aider"), and — on Windows —
	// .exe suffixes. Matching is case-insensitive on the base name.
	Matches(command string) bool

	// BuildArgs returns the argv to use when launching the agent. For
	// flag-based adapters it appends the injection flag and prompt; for
	// stdin-based adapters it returns baseArgs unchanged. The returned
	// slice must not alias baseArgs — callers may further mutate it.
	BuildArgs(baseArgs []string, prompt string) []string

	// InitialStdin returns the bytes to write to the child's stdin after
	// StdinDelay has elapsed. Returns nil for flag-based adapters or
	// when prompt is empty.
	InitialStdin(prompt string) []byte

	// StdinDelay is how long to wait after launch before writing the
	// InitialStdin bytes. Ignored for flag-based adapters.
	StdinDelay() time.Duration
}

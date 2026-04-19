// Hook rules CLI wiring — check-bash, check-prompt, and `rules` (list /
// test / reload) subcommands. The CLI is the thinnest possible adapter
// around the hookrules package: parse stdin, call Match, emit exit code +
// stderr according to the Claude Code hook contract.
//
// Exit-code contract (Claude Code, https://docs.claude.com/hooks):
//   - 0: allow the tool call, optional stdout is ignored
//   - 2: block the tool call; stderr is surfaced to the model
//   - JSON on stdout: alternative block/ask/allow shape, mutually exclusive
//     with exit 2
//
// We use the exit-2 + stderr shape for block because it is the simplest
// path and interoperates with non-Claude harnesses that respect POSIX
// exit codes. JSON stdout support is intentionally not implemented — the
// agnt interceptor targets Claude Code today and stderr is strictly
// clearer in both the Claude logs and developer terminals.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/standardbeagle/agnt/internal/hookrules"
)

// hookCheckBashCmd is the PreToolUse interceptor for Bash tool calls.
// Wired via:
//
//	"preToolUse": [{ "type": "command",
//	  "command": "agnt hook check-bash --project-path $PWD" }]
//
// Runs independently of `agnt hook pre-tool-use` so operators can wire
// the interceptor without also forwarding telemetry (or vice versa).
var hookCheckBashCmd = &cobra.Command{
	Use:   "check-bash",
	Short: "Claude Code PreToolUse interceptor that redirects Bash toward agnt MCP tools",
	Long: `Read a PreToolUse hook JSON payload on stdin. When tool_name is Bash and
the command matches a block rule, exit 2 with an explanatory stderr message
that cites the recommended agnt MCP invocation. Soft-warn rules emit a
nudge on stderr but exit 0. Non-Bash tools, empty commands, and commands
matching no rule exit 0 silently.

Fail-open on every error path: malformed JSON, scope-guard mismatch, and
internal errors all exit 0 within a 1s budget so a broken agnt install
never wedges the agent.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookCheckBash,
}

// hookCheckPromptCmd is the UserPromptSubmit nudge injector. It never
// blocks — emits <system-reminder> XML on stdout when a rule matches,
// otherwise silent exit 0.
var hookCheckPromptCmd = &cobra.Command{
	Use:   "check-prompt",
	Short: "UserPromptSubmit hook that injects advisory system-reminders",
	Long: `Read a UserPromptSubmit hook JSON payload on stdin. When the prompt text
matches any prompt rule, emit a <system-reminder> block on stdout so the
reminder is injected into the model's context. Multiple matching rules
produce multiple reminders. Never exits non-zero.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookCheckPrompt,
}

// hookRulesCmd groups the inspection verbs (list / test / reload) under
// `agnt hook rules`. Reload is a no-op today because rules are compiled
// on each invocation rather than cached daemon-side, but the subcommand
// exists so operators have a stable discoverable command when a future
// daemon cache lands.
var hookRulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Inspect and dry-run the hook rules catalog",
	Long: `List the merged hook rules (builtin + .agnt.kdl override), dry-run a
single command against the ruleset, or signal a cache reload. The rules
catalog is not cached daemon-side today, so 'reload' is a no-op that
exists for forward compatibility.`,
}

var (
	hookCheckBashProjectPath   string
	hookCheckPromptProjectPath string
	hookRulesProjectPath       string
	hookRulesTestCommand       string
	hookRulesTestPrompt        string
)

func init() {
	hookCheckBashCmd.Flags().StringVar(&hookCheckBashProjectPath, "project-path", "", "Project root (enables .agnt.kdl override merge + scope guard)")
	hookCheckPromptCmd.Flags().StringVar(&hookCheckPromptProjectPath, "project-path", "", "Project root (enables .agnt.kdl override merge)")
	hookRulesCmd.PersistentFlags().StringVar(&hookRulesProjectPath, "project-path", "", "Project root for .agnt.kdl override merge")

	hookRulesListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all active hook rules (builtin + overrides)",
		Args:  cobra.NoArgs,
		RunE:  runHookRulesList,
	}

	hookRulesTestCmd := &cobra.Command{
		Use:   "test",
		Short: "Dry-run a command or prompt through the ruleset",
		Args:  cobra.NoArgs,
		RunE:  runHookRulesTest,
	}
	hookRulesTestCmd.Flags().StringVar(&hookRulesTestCommand, "command", "", "Bash command string to test against bash rules")
	hookRulesTestCmd.Flags().StringVar(&hookRulesTestPrompt, "prompt", "", "Prompt text to test against prompt rules")

	hookRulesReloadCmd := &cobra.Command{
		Use:   "reload",
		Short: "No-op reload placeholder (rules are recompiled per-invocation today)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "hook rules are recompiled on each invocation; nothing to reload")
			return nil
		},
	}

	hookRulesCmd.AddCommand(hookRulesListCmd, hookRulesTestCmd, hookRulesReloadCmd)
	hookCmd.AddCommand(hookCheckBashCmd, hookCheckPromptCmd, hookRulesCmd)
}

// preToolUsePayload is the tiny subset of the Claude Code PreToolUse
// schema we need. Everything else is ignored; the daemon still gets the
// full opaque payload via the telemetry path.
type preToolUsePayload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type bashToolInput struct {
	Command string `json:"command"`
}

// userPromptSubmitPayload mirrors the UserPromptSubmit hook schema.
type userPromptSubmitPayload struct {
	Prompt string `json:"prompt"`
}

func runHookCheckBash(cmd *cobra.Command, _ []string) error {
	// 1s hard deadline for the whole interceptor — matches the task
	// budget. Exceeding it means fail-open exit 0.
	ctx, cancel := context.WithTimeout(cmd.Context(), time.Second)
	defer cancel()
	_ = ctx // reserved for future daemon-probe scope guard

	code := runCheckBashImpl(cmd.InOrStdin(), cmd.ErrOrStderr(), hookCheckBashProjectPath, os.Getenv)
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// runCheckBashImpl is the testable core. Split out so tests can drive
// arbitrary stdin + env without subprocess spawning.
func runCheckBashImpl(stdin io.Reader, stderr io.Writer, projectPath string, getenv func(string) string) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return 0 // fail-open: can't read payload
	}
	// Empty payload is a hook wired without stdin — no-op.
	if len(data) == 0 {
		return 0
	}

	var p preToolUsePayload
	if err := json.Unmarshal(data, &p); err != nil {
		return 0 // fail-open on malformed JSON
	}
	if !strings.EqualFold(p.ToolName, "Bash") {
		return 0 // not a Bash call; nothing to do
	}

	var input bashToolInput
	if err := json.Unmarshal(p.ToolInput, &input); err != nil {
		return 0
	}
	if input.Command == "" {
		return 0
	}

	// Scope guard: only intercept when we're reasonably sure this is an
	// agnt-managed project (or when the caller didn't tell us otherwise).
	if !hookrules.ScopeGuardActive(projectPath) {
		return 0
	}

	rs := hookrules.LoadForProject(projectPath)

	// Env-var bypass. Checked after loading rules so a user with a
	// non-default BypassEnv in their KDL config still wins.
	bypassEnv := rs.BypassEnv
	if bypassEnv == "" {
		bypassEnv = hookrules.DefaultBypassEnv
	}
	if getenv(bypassEnv) != "" {
		return 0
	}

	// Inline bypass marker wins over everything else. The whole point of
	// the marker is "I know what I'm doing, let this one through".
	if hookrules.CommandHasBypassMarker(input.Command) {
		return 0
	}

	decision := rs.MatchBash(input.Command)
	switch decision.Action {
	case hookrules.ActionBlock:
		fmt.Fprintln(stderr, formatBlockMessage(decision, input.Command))
		return 2
	case hookrules.ActionSoftWarn:
		fmt.Fprintln(stderr, formatWarnMessage(decision, input.Command))
		return 0
	default:
		return 0
	}
}

// formatBlockMessage renders the stderr message surfaced to Claude when a
// Bash rule fires with action=block. The message is phrased as an
// imperative recommendation so the model picks it up cleanly on retry.
func formatBlockMessage(d hookrules.Decision, cmd string) string {
	var b strings.Builder
	b.WriteString("[agnt] This Bash command is intercepted — use the agnt MCP tool instead.\n")
	b.WriteString("  command: ")
	b.WriteString(truncateHookMsg(cmd, 200))
	b.WriteString("\n")
	if d.Replacement != "" {
		b.WriteString("  use instead: ")
		b.WriteString(d.Replacement)
		b.WriteString("\n")
	}
	if d.Reason != "" {
		b.WriteString("  why: ")
		b.WriteString(d.Reason)
		b.WriteString("\n")
	}
	b.WriteString("  bypass: append `# agnt-allow` to the command or set ")
	b.WriteString(hookrules.DefaultBypassEnv)
	b.WriteString("=1")
	return b.String()
}

// formatWarnMessage is the soft-warn variant — it does NOT exit non-zero
// so the tool call proceeds, but it flags the better alternative in
// stderr where both the developer and (with Claude Code's debug logging)
// the model can see it.
func formatWarnMessage(d hookrules.Decision, cmd string) string {
	var b strings.Builder
	b.WriteString("[agnt] Consider the agnt MCP tool for this operation (not blocking).\n")
	b.WriteString("  command: ")
	b.WriteString(truncateHookMsg(cmd, 200))
	b.WriteString("\n")
	if d.Replacement != "" {
		b.WriteString("  alternative: ")
		b.WriteString(d.Replacement)
		b.WriteString("\n")
	}
	if d.Reason != "" {
		b.WriteString("  why: ")
		b.WriteString(d.Reason)
	}
	return b.String()
}

func truncateHookMsg(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func runHookCheckPrompt(cmd *cobra.Command, _ []string) error {
	runCheckPromptImpl(cmd.InOrStdin(), cmd.OutOrStdout(), hookCheckPromptProjectPath)
	return nil
}

func runCheckPromptImpl(stdin io.Reader, stdout io.Writer, projectPath string) {
	data, err := io.ReadAll(stdin)
	if err != nil || len(data) == 0 {
		return
	}
	var p userPromptSubmitPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	if p.Prompt == "" {
		return
	}

	rs := hookrules.LoadForProject(projectPath)
	hits := rs.MatchPrompt(p.Prompt)
	if len(hits) == 0 {
		return
	}

	for _, r := range hits {
		// Emit one <system-reminder> block per match. Claude Code's
		// UserPromptSubmit hook treats stdout as additional context
		// appended to the user message.
		fmt.Fprintf(stdout, "<system-reminder>\n%s\n</system-reminder>\n", r.Reminder)
	}
}

func runHookRulesList(cmd *cobra.Command, _ []string) error {
	rs, errs := hookrules.LoadAndValidate(hookRulesProjectPath)
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Bypass env: %s\n", rs.BypassEnv)
	fmt.Fprintf(out, "\nBash rules (%d):\n", len(rs.BashRules))
	for i, r := range rs.BashRules {
		fmt.Fprintf(out, "  %2d. [%s] %s\n", i+1, r.Action, r.Raw)
		if r.Replacement != "" {
			fmt.Fprintf(out, "      → %s\n", r.Replacement)
		}
	}
	fmt.Fprintf(out, "\nPrompt rules (%d):\n", len(rs.PromptRules))
	for i, r := range rs.PromptRules {
		fmt.Fprintf(out, "  %2d. %s\n", i+1, r.Raw)
		if r.Reminder != "" {
			fmt.Fprintf(out, "      → %s\n", truncateHookMsg(r.Reminder, 120))
		}
	}
	if len(errs) > 0 {
		fmt.Fprintf(out, "\nConfig errors (%d):\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(out, "  - %s\n", e)
		}
	}
	return nil
}

func runHookRulesTest(cmd *cobra.Command, _ []string) error {
	if hookRulesTestCommand == "" && hookRulesTestPrompt == "" {
		return fmt.Errorf("specify --command or --prompt")
	}
	rs := hookrules.LoadForProject(hookRulesProjectPath)
	out := cmd.OutOrStdout()
	if hookRulesTestCommand != "" {
		d := rs.MatchBash(hookRulesTestCommand)
		fmt.Fprintf(out, "command: %s\n", hookRulesTestCommand)
		fmt.Fprintf(out, "decision: %s\n", d.Action)
		if d.Rule != nil {
			fmt.Fprintf(out, "matched rule: %s\n", d.Rule.Raw)
			if d.Replacement != "" {
				fmt.Fprintf(out, "replacement: %s\n", d.Replacement)
			}
			if d.Reason != "" {
				fmt.Fprintf(out, "reason: %s\n", d.Reason)
			}
		}
	}
	if hookRulesTestPrompt != "" {
		hits := rs.MatchPrompt(hookRulesTestPrompt)
		fmt.Fprintf(out, "\nprompt: %s\n", hookRulesTestPrompt)
		fmt.Fprintf(out, "matches: %d\n", len(hits))
		for _, h := range hits {
			fmt.Fprintf(out, "  - %s\n    → %s\n", h.Raw, h.Reminder)
		}
	}
	return nil
}

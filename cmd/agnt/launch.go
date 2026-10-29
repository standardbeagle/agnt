package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/project"
)

// launchableAgents lists the coding agents a bare `agnt` can start, in the
// order they are offered. The command is the executable looked up on PATH and
// passed to `agnt run`; every entry has a registered adapter.
var launchableAgents = []struct{ Command, Label string }{
	{"claude", "Claude Code"},
	{"codex", "Codex"},
	{"gemini", "Gemini CLI"},
	{"opencode", "OpenCode"},
	{"cursor-agent", "Cursor Agent"},
	{"copilot", "GitHub Copilot CLI"},
	{"aider", "Aider"},
	{"auggie", "Auggie"},
	{"qwen", "Qwen Code"},
	{"crush", "Crush"},
	{"kimi", "Kimi CLI"},
}

// detectInstalledAgents returns the launchable agents lookPath can resolve, in
// offer order. lookPath is exec.LookPath in production.
func detectInstalledAgents(lookPath func(string) (string, error)) []string {
	var found []string
	for _, a := range launchableAgents {
		if _, err := lookPath(a.Command); err == nil {
			found = append(found, a.Command)
		}
	}
	return found
}

func agentLabel(command string) string {
	for _, a := range launchableAgents {
		if a.Command == command {
			return a.Label
		}
	}
	return command
}

// chooseAgent picks the agent to launch. One installed agent is chosen without
// asking; several are offered as a numbered menu (Enter takes the first).
func chooseAgent(installed []string, in *bufio.Reader, out io.Writer) (string, error) {
	switch len(installed) {
	case 0:
		return "", fmt.Errorf("no supported coding agent found on PATH (looked for %s); install one, or run `agnt run <command>`", agentNames())
	case 1:
		fmt.Fprintf(out, "Starting %s with agnt\n", agentLabel(installed[0]))
		return installed[0], nil
	}
	fmt.Fprintln(out, "Which coding agent should agnt start?")
	for i, c := range installed {
		fmt.Fprintf(out, "  %d) %s\n", i+1, agentLabel(c))
	}
	for {
		fmt.Fprintf(out, "Choose [1-%d, Enter for 1]: ", len(installed))
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" && err != nil {
			return "", fmt.Errorf("no agent chosen: %w", err)
		}
		if line == "" {
			return installed[0], nil
		}
		if n, convErr := strconv.Atoi(line); convErr == nil && n >= 1 && n <= len(installed) {
			return installed[n-1], nil
		}
		fmt.Fprintf(out, "Enter a number from 1 to %d.\n", len(installed))
	}
}

func agentNames() string {
	names := make([]string, len(launchableAgents))
	for i, a := range launchableAgents {
		names[i] = a.Command
	}
	return strings.Join(names, ", ")
}

// launchInteractive is what a bare `agnt` does in a terminal: find the
// installed coding agents, pick one, and hand off to `agnt run`.
func launchInteractive(cmd *cobra.Command) {
	agent, err := chooseAgent(detectInstalledAgents(exec.LookPath), bufio.NewReader(os.Stdin), os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt: %v\n\n", err)
		cmd.Help()
		os.Exit(1)
	}
	runCommand(runCmd, []string{agent})
}

// emptyProjectNotice is the status-bar line shown when the agent starts in a
// directory agnt recognizes as no project (a fresh checkout, nothing scaffolded
// yet). Empty for any detected project.
func emptyProjectNotice(projectPath string) string {
	detected, err := project.Detect(projectPath)
	if err != nil || detected.Type != project.ProjectUnknown {
		return ""
	}
	return "empty project: tell the agent what to build and it will set up the dev server and proxy"
}

package aichannel

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The CLI-mode tests need a real child process standing in for an AI agent.
// They used to exec `echo`, `printf`, `cat`, `sleep` and generated `.sh`
// files, none of which exist as executables on native Windows. Instead the
// test binary re-execs itself: TestMain sees helperModeEnv and behaves as the
// requested stand-in, so the same fixture runs on every OS.
const (
	// helperModeEnv selects the stand-in behaviour (see runHelperAgent).
	helperModeEnv = "AGNT_AICHANNEL_TEST_HELPER"
	// helperOutputEnv carries the canned stdout for the "emit" mode.
	helperOutputEnv = "AGNT_AICHANNEL_TEST_HELPER_OUTPUT"
	// helperArgvFileEnv names the file the "argv" mode writes its arguments to.
	helperArgvFileEnv = "AGNT_AICHANNEL_TEST_HELPER_ARGV_FILE"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(runHelperAgent(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// runHelperAgent is the child side. It never touches the filesystem, so the
// child's working directory is irrelevant to it.
func runHelperAgent(mode string, args []string) int {
	switch mode {
	case "echo": // echo(1): args joined by spaces, newline-terminated
		fmt.Fprintln(os.Stdout, strings.Join(args, " "))
	case "printf": // printf(1) with a literal format: first arg verbatim
		if len(args) > 0 {
			fmt.Fprint(os.Stdout, args[0])
		}
	case "cat": // cat(1): stdin to stdout
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "sleep": // sleep(1): first arg is whole seconds
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "sleep helper: missing duration")
			return 2
		}
		secs, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		time.Sleep(time.Duration(secs) * time.Second)
	case "emit": // a script that prints fixed output and ignores its args
		fmt.Fprint(os.Stdout, os.Getenv(helperOutputEnv))
	case "argv": // record argv exactly as received, one JSON array, to a file
		// A file, not stdout: under a PTY the terminal re-wraps and re-encodes
		// what is printed, so stdout cannot show whether an argument with
		// newlines arrived as one piece.
		data, err := json.Marshal(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(os.Getenv(helperArgvFileEnv), data, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "argv-recorded")
	default:
		fmt.Fprintf(os.Stderr, "unknown %s mode %q\n", helperModeEnv, mode)
		return 2
	}
	return 0
}

// helperAgent returns the Command and Env that make a Channel run this test
// binary as the given stand-in. Merge them into the Config under test.
func helperAgent(t *testing.T, mode string) (command string, env map[string]string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating test binary for helper process: %v", err)
	}
	return exe, map[string]string{helperModeEnv: mode}
}

// helperScript is helperAgent for the "emit" mode: a stand-in for a script
// that prints output regardless of its arguments.
func helperScript(t *testing.T, output string) (command string, env map[string]string) {
	t.Helper()
	command, env = helperAgent(t, "emit")
	env[helperOutputEnv] = output
	return command, env
}

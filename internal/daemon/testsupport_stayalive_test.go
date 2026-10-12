package daemon

import (
	"fmt"
	"runtime"
	"strconv"
)

// stayAliveSeconds is the single source of truth for how long a daemon test's
// "keep this process merely present past the assertion" sleep lasts. Every
// call site below routes through stayAliveCmd / stayAliveArgs instead of a
// bare `sleep 60` literal, for two reasons — one about drift, one about leaks.
//
// Drift: ~30 daemon test spawn sites need a process that is simply alive while
// an assertion races past a start barrier / SCRIPT LIST / progress snapshot.
// A literal per site invites 30 independently-chosen magic numbers; a const
// makes the duration one edit.
//
// Bounded orphan lifetime (the real defect): these processes are ALL
// daemon-managed — the daemon's ProcessManager spawns them from KDL `run`
// blocks or goprocess.ProcessConfig. On a normal test exit NewForTest's
// t.Cleanup(Stop) reaps them. The residual is the abnormal path this repo hits
// often: the test binary is SIGKILLed (a cancelled gate, an agent killing a
// run) BEFORE Stop() executes. On SIGKILL *no* Go cleanup fires — not
// t.Cleanup, and not a hypothetical pgid-kill helper either — so the ONLY
// lever on how long the orphaned sleep lingers is the sleep's own duration.
// startupOrphanPGIDScan reaps it on the next daemon start; until then it idles
// for exactly this long. Cutting 60 -> 20 turns a worst-case one-minute orphan
// into a 20-second one.
//
// 20s keeps a wide margin over every assertion window here (all sub-second to
// a few seconds even under the loaded `-p 1` serial suite), and stays under
// the 30s periodic-reconcile period so no test can race a reconcile against a
// process that exited early.
const stayAliveSeconds = 20

// stayAliveCmd is the shell-command form, for KDL `run "..."` blocks and
// lifecycle-hook command strings.
//
// The daemon runs a `run` string through the platform shell, and on native
// Windows that is cmd.exe, which has no `sleep`. `ping -n N` waits about N-1
// seconds, needs no console (unlike `timeout`), and is the keep-alive this
// package already used on Windows.
func stayAliveCmd() string {
	name, args := stayAliveExec()
	cmd := name
	for _, a := range args {
		cmd += " " + a
	}
	return cmd
}

// stayAliveExec is the direct-exec argv form, for goprocess.ProcessConfig and
// protocol.RunConfig{Raw: true}, where no shell is involved and the command
// must be a real executable on the host: `sleep` on Unix, `ping` on Windows.
func stayAliveExec() (command string, args []string) {
	if runtime.GOOS == "windows" {
		return "ping", []string{"-n", strconv.Itoa(stayAliveSeconds + 1), "127.0.0.1"}
	}
	return "sleep", []string{strconv.Itoa(stayAliveSeconds)}
}

// echoExec is the direct-exec argv for a process that prints text and exits 0.
// `echo` is an executable on Unix but only a cmd.exe builtin on Windows.
func echoExec(text string) (command string, args []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", "echo", text}
	}
	return "echo", []string{text}
}

// failExec is the direct-exec argv for a process that starts and exits 1.
// `false` is an executable on Unix only; without the Windows form a test
// would see a start failure instead of the non-zero exit it means to observe.
func failExec() (command string, args []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/c", "exit", "1"}
	}
	return "false", nil
}

// kdlExec renders a direct-exec argv as the `command` / `args` lines of a
// .agnt.kdl script block.
func kdlExec(command string, args []string) string {
	out := fmt.Sprintf("command %q", command)
	if len(args) > 0 {
		out += "\n        args"
		for _, a := range args {
			out += fmt.Sprintf(" %q", a)
		}
	}
	return out
}

// stayAliveArgs is the `sh -c` argv form, for goprocess.ProcessConfig.Args.
// Unix-only by construction: its callers pair it with Command "sh".
func stayAliveArgs() []string {
	return []string{"-c", fmt.Sprintf("sleep %d", stayAliveSeconds)}
}

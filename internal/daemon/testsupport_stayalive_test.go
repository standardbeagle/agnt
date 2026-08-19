package daemon

import "fmt"

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
func stayAliveCmd() string { return fmt.Sprintf("sleep %d", stayAliveSeconds) }

// stayAliveArgs is the `sh -c` argv form, for goprocess.ProcessConfig.Args.
func stayAliveArgs() []string { return []string{"-c", stayAliveCmd()} }

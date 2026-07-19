package daemon

import (
	"testing"

	"go.uber.org/goleak"
)

// verifyNoLeaks is the shared goroutine-leak assertion for this package's
// sequential goleak stress tests. Call it as:
//
//	defer verifyNoLeaks(t)()
//
// The outer call captures the baseline goroutine set at defer time; the
// returned closure runs the verification at test exit.
//
// Why IgnoreCurrent() alone is NOT enough here (the flake this helper fixes):
// this package mixes these sequential goleak stress tests with many
// t.Parallel() sibling tests. A parallel sibling calls Daemon.Start() (or
// otherwise spins up a background reconciliation loop) in its setup, then
// suspends at t.Parallel(); its daemon keeps running — and its background
// loops periodically shell out (ss / lsof / netstat / tasklist via
// exec.CommandContext) — for the WHOLE remainder of the package run, including
// while these sequential stress tests execute. Each such exec spawns fresh
// os/exec worker goroutines AFTER IgnoreCurrent() snapshotted the goroutine
// set, so they are not in the ignore set and goleak flags them — flakily,
// depending on whether a ticker fires inside a given stress test's short
// window. Those goroutines are NOT leaked by the stress test (the owning
// parallel sibling reaps its daemon at its own cleanup); they are concurrent
// noise from a legitimately-running loop.
//
// The IgnoreAnyFunction filters suppress exactly that noise while leaving the
// checks' teeth intact: a stress test's own real leak (e.g. a stuck
// (*SchedulerStateManager).writeLoop or a leaked hub/drain goroutine) carries
// no os/exec frame and is not in the IgnoreCurrent snapshot, so it still fails
// the assertion.
//
// Frame choice: os/exec spawns up to three workers per exec — the childStdin
// and writerDescriptor copy goroutines (both run under
// os/exec.(*Cmd).Start.func2) and the context watcher
// (os/exec.(*Cmd).watchCtx). Matching those two frames anywhere in the stack
// covers all three regardless of which syscall they are currently blocked in.
//
// See .claude/rules/testing-parallel-package-flakes.md for the sibling
// flake-class (cross-package port contention); this is the intra-package
// mixed-parallel-test variant. hub_router_stress_test.go's routerVerifyNone
// applied the same fix earlier for a single file; this is the shared form.
func verifyNoLeaks(t *testing.T) func() {
	opts := []goleak.Option{
		goleak.IgnoreCurrent(),
		goleak.IgnoreAnyFunction("os/exec.(*Cmd).Start.func2"),
		goleak.IgnoreAnyFunction("os/exec.(*Cmd).watchCtx"),
	}
	return func() { goleak.VerifyNone(t, opts...) }
}

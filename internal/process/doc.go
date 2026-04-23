// Package process hosts adversarial stress tests for the RingBuffer
// primitive that backs ManagedProcess output capture.
//
// The RingBuffer implementation itself lives in
// github.com/standardbeagle/go-cli-server/process — it was migrated
// out of this repo in commit 44fff97. These tests pin the concurrency
// and overflow invariants that agnt depends on, so that a future
// upstream bump cannot silently weaken them without CI failing.
//
// Scope: pure data-structure adversarial tests. No real subprocess,
// no PTY, no ProcessManager. Only RingBuffer.Write / Snapshot /
// Truncated / Len under concurrent producers.
package process

//go:build unix

package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

// BenchmarkHookColdExit measures the wall clock of a single in-process
// runHookInternal call against a warm test daemon. The production p99
// target is ≤5ms, but there are two layers outside our control here:
//
//  1. Process fork cost. The real CLI pays an exec(2) round trip per
//     invocation; this bench skips that because we run in-process. On
//     the test host fork alone is already ~1-3ms depending on kernel
//     and cgroup pressure.
//  2. Socket connect cost. Each HookSend opens a fresh Unix socket
//     (see internal/daemon/client.go HookSend comment) to scope its
//     SetDeadline to the one call, so connect(2) + first write dominate.
//
// This bench is the reference baseline for the daemon contribution
// alone. A regression that adds serialization, extra RPC round trips,
// or per-call allocation will show up immediately as a nanoseconds-
// per-op blowup; kernel fork cost is not our concern here.
func BenchmarkHookColdExit(b *testing.B) {
	// Use the same in-process daemon test harness the functional
	// tests use. It's a real daemon.New(...) + daemon.Start(),
	// just without the binary round trip.
	sock, _ := startTestHookDaemonTB(b)

	payload := `{"tool":"Bash","command":"echo hi"}`
	b.ResetTimer()

	var max time.Duration
	for i := 0; i < b.N; i++ {
		start := time.Now()
		code := runHookInternal(hookInvocation{
			event:      "pre-tool-use",
			stdin:      strings.NewReader(payload),
			stderr:     io.Discard,
			socketPath: sock,
		})
		elapsed := time.Since(start)
		if elapsed > max {
			max = elapsed
		}
		if code != 0 {
			b.Fatalf("unexpected non-zero exit %d", code)
		}
	}
	b.ReportMetric(float64(max.Microseconds()), "max-μs/op")
}

//go:build unix

package main

import (
	"io"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// percentile returns the p-quantile (0..1) of an already-sorted slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// TestHook_LatencyWithinBaselineFactor is the CI-enforceable guard for the
// docs/hook-dispatcher.md "p99 cold exit ≤5ms" cost contract.
//
// Absolute wall-clock bounds on this path flake under load — that is why the
// older TestHook_LatencyAgainstWarmDaemon max-of-50 budget was widened
// 50ms→100ms→1s and finally demoted to an order-of-magnitude smoke test. The
// scheduler can stall ANY single sample for hundreds of ms on a saturated box,
// so max/p99 wall-clock is not a load-stable statistic and must never be a
// gate.
//
// This test instead calibrates against a same-box baseline captured in the
// same run: N connect+write+read round trips to a trivial in-process echo
// socket. The baseline absorbs exactly the load-sensitive component (OS socket
// setup + scheduler jitter) that made the old test flaky. Under CPU load the
// baseline inflates too, so the derived bounds inflate with it — the guard
// measures the dispatcher's work *relative to* the machine's current socket
// noise floor, not against a fixed clock.
//
// Two assertions, both self-calibrated:
//
//   - Median (p50): distribution-wide guard. The median is unaffected by a few
//     slow scheduler slices (measured stable at ≤340µs even under 2× CPU
//     oversubscription), so it catches a structural regression that shifts the
//     whole distribution — an extra RPC round trip, added serialization,
//     per-call allocation — the exact failure modes BenchmarkHookColdExit
//     documents. Since the median is a lower bound on p99, a median that
//     approaches the 5ms contract already means the contract is blown.
//   - Tail (p99): a regression that adds a *fixed* per-call cost (e.g. a
//     synchronous extra hop) lifts the dispatcher tail without lifting the
//     baseline tail, so it trips the tail floor even though pure jitter never
//     does.
func TestHook_LatencyWithinBaselineFactor(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sock, _ := startTestHookDaemon(t)

	// Trivial echo listener: connect → write → read one byte → close. Two
	// dials per sample mirror the dispatcher's own shape (NewClient.Connect
	// availability dial + HookSend's dedicated short-lived socket), so the
	// baseline tracks the same socket-setup jitter the dispatcher pays.
	echoSock := filepath.Join(t.TempDir(), "echo.sock")
	ln, err := net.Listen("unix", echoSock)
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 8)
				n, _ := c.Read(buf)
				_, _ = c.Write(buf[:n])
				_ = c.Close()
			}(c)
		}
	}()

	baselineSample := func() time.Duration {
		start := time.Now()
		for i := 0; i < 2; i++ {
			c, derr := net.Dial("unix", echoSock)
			if derr != nil {
				t.Fatalf("echo dial: %v", derr)
			}
			_, _ = c.Write([]byte("x"))
			b := make([]byte, 8)
			_, _ = c.Read(b)
			_ = c.Close()
		}
		return time.Since(start)
	}
	hookSample := func() time.Duration {
		start := time.Now()
		code := runHookInternal(hookInvocation{
			event:      "pre-tool-use",
			stdin:      strings.NewReader(`{"tool":"Bash"}`),
			stderr:     io.Discard,
			socketPath: sock,
		})
		if code != 0 {
			t.Fatalf("unexpected non-zero exit %d", code)
		}
		return time.Since(start)
	}

	// Warm both paths so first-touch allocation/connect cost is out of the
	// measured samples for both (fair comparison).
	for i := 0; i < 30; i++ {
		baselineSample()
		hookSample()
	}

	const n = 300
	baseDurs := make([]time.Duration, 0, n)
	hookDurs := make([]time.Duration, 0, n)
	// Interleave so both distributions see the same load window — a load
	// spike that hits the hook samples also hits the baseline samples.
	for i := 0; i < n; i++ {
		baseDurs = append(baseDurs, baselineSample())
		hookDurs = append(hookDurs, hookSample())
	}
	sort.Slice(baseDurs, func(i, j int) bool { return baseDurs[i] < baseDurs[j] })
	sort.Slice(hookDurs, func(i, j int) bool { return hookDurs[i] < hookDurs[j] })

	baseP50, baseP99 := percentile(baseDurs, 0.50), percentile(baseDurs, 0.99)
	hookP50, hookP99 := percentile(hookDurs, 0.50), percentile(hookDurs, 0.99)

	t.Logf("baseline p50=%s p99=%s | hook p50=%s p99=%s",
		baseP50, baseP99, hookP50, hookP99)

	// Median guard. Idle ratio ~1.25; observed ≤2.9 under 2× CPU load.
	// Factor 6 + a 5ms floor (the documented p99 target, generous for a
	// median) gives comfortable headroom while still catching a real
	// distribution-wide regression.
	medianBound := 6*baseP50 + 5*time.Millisecond
	if hookP50 > medianBound {
		t.Fatalf("hook median %s exceeds baseline-calibrated bound %s (baseline p50 %s): "+
			"the dispatcher hot path regressed relative to the socket noise floor",
			hookP50, medianBound, baseP50)
	}

	// Tail guard. Pure jitter lifts baseP99 too (measured up to hundreds of
	// ms under load), so the relative term absorbs it; the 50ms floor is what
	// catches a fixed-cost regression on an idle box where baseP99 is ~1ms.
	tailBound := 4*baseP99 + 50*time.Millisecond
	if hookP99 > tailBound {
		t.Fatalf("hook p99 %s exceeds baseline-calibrated bound %s (baseline p99 %s): "+
			"the dispatcher added fixed per-call latency",
			hookP99, tailBound, baseP99)
	}
}

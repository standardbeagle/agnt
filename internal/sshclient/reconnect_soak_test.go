//go:build !windows

package sshclient

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
	"golang.org/x/crypto/ssh"
)

// TestReconnector_ThreeDropSoak_DurableWorkAndNoResourceGrowth is deliberately
// non-parallel: it owns real sockets and exercises process-wide FD/goroutine
// accounting. The remote producer is independent of every SSH transport,
// matching a long-running session-host command that survives reattachment.
func TestReconnector_ThreeDropSoak_DurableWorkAndNoResourceGrowth(t *testing.T) {
	baselineLeaks := goleak.IgnoreCurrent()
	baselineFDs := openFDCount(t)
	baselineGoroutines := runtime.NumGoroutine()

	sim := &sessionSim{}
	stopWork := make(chan struct{})
	workDone := make(chan struct{})
	go func() {
		defer close(workDone)
		for n := 0; ; n++ {
			select {
			case <-stopWork:
				return
			case <-time.After(2 * time.Millisecond):
				sim.append([]byte(fmt.Sprintf("work-%d;", n)))
			}
		}
	}()

	harness := newContentHarness(t, sim)
	privPEM, _ := generateClientKey(t)
	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		t.Fatalf("parse client key: %v", err)
	}
	dial := func(context.Context) (*Client, error) {
		cfg := &ssh.ClientConfig{
			User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 2 * time.Second,
		}
		c, err := ssh.Dial("tcp", harness.Addr(), cfg)
		if err != nil {
			return nil, err
		}
		return &Client{SSH: c, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}, nil
	}
	attach := func(_ context.Context, c *Client) (*PTYSession, error) {
		return OpenPTYSessionWithCommand(c.SSH, RemoteReattachCommand("soak-session"), TermSize{Cols: 80, Rows: 24})
	}

	client, err := dial(context.Background())
	if err != nil {
		t.Fatalf("initial dial: %v", err)
	}
	session, err := attach(context.Background(), client)
	if err != nil {
		t.Fatalf("initial attach: %v", err)
	}
	reader := startChannelReader(session.channel)
	if got := collectAtLeast(reader, 16, 2*time.Second); len(got) < 16 {
		t.Fatalf("remote work did not start: %q", got)
	}

	markers := []string{"CONNECTED"}
	for cycle := 1; cycle <= 3; cycle++ {
		before := len(sim.snapshot())
		if cycle == 2 {
			harness.Freeze()
			sim.append([]byte("FROZEN-WORK-CONTINUES;"))
		}
		markers = append(markers, "RECONNECTING")
		harness.Drop()
		session.Close()
		client.Close()
		if cycle == 2 {
			harness.Resume()
		}

		r := &Reconnector{
			Backoff: BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Millisecond},
			Dial:    dial, Attach: attach,
			OnStatus: func(msg string) { markers = append(markers, msg) },
		}
		client, session, err = r.Run(context.Background())
		if err != nil {
			t.Fatalf("cycle %d reconnect: %v", cycle, err)
		}
		markers = append(markers, "CONNECTED")
		reader = startChannelReader(session.channel)
		want := len(sim.snapshot())
		got := collectAtLeast(reader, want, 3*time.Second)
		if len(got) < want || len(sim.snapshot()) <= before {
			t.Fatalf("cycle %d: in-flight remote work did not survive: replay=%d want>=%d", cycle, len(got), want)
		}
	}

	client.Close()
	session.Close()
	harness.Stop()
	close(stopWork)
	<-workDone

	assertReconnectMarkerOrder(t, markers, 3)
	waitForResourceBaseline(t, baselineFDs, baselineGoroutines)
	goleak.VerifyNone(t, baselineLeaks)
}

func assertReconnectMarkerOrder(t *testing.T, markers []string, cycles int) {
	t.Helper()
	joined := strings.Join(markers, "\n")
	pos := 0
	for cycle := 1; cycle <= cycles; cycle++ {
		for _, want := range []string{"RECONNECTING", "agnt ssh: reconnecting", "agnt ssh: reconnected", "CONNECTED"} {
			n := bytes.Index([]byte(joined[pos:]), []byte(want))
			if n < 0 {
				t.Fatalf("cycle %d missing ordered marker %q in %q", cycle, want, joined)
			}
			pos += n + len(want)
		}
	}
}

func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("relative FD snapshot requires /proc/self/fd: %v", err)
	}
	return len(entries)
}

func waitForResourceBaseline(t *testing.T, wantFDs, wantGoroutines int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		fds, goroutines := openFDCount(t), runtime.NumGoroutine()
		if fds <= wantFDs && goroutines <= wantGoroutines {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resource growth after soak: fds %d -> %d, goroutines %d -> %d", wantFDs, fds, wantGoroutines, goroutines)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

//go:build !windows

package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os/user"
	"runtime"
	"testing"
	"time"

	"go.uber.org/goleak"
	"golang.org/x/crypto/ssh"
)

// This file is the reconnect *chaos* suite (task 10d). It is deliberately
// UNTAGGED (only the shared !windows constraint of the harness it drives), so
// the repo's `go test -p 1 ./...` gate exercises it on every run rather than
// only under `make test-ssh` — the container tier is *_sshe2e_test.go, this is
// the in-process tier. It composes the two drop-simulation harnesses built in
// task 09a (HardCloseHarness / SSHDFreezeHarness) and the sibling
// contentHarness (reconnect_integration_test.go) against the real
// Reconnector state machine (reconnect.go), stressing both transport-death
// modes across many cycles.
//
// Two distinct chaos scenarios live here, filling gaps the existing
// integration (single hard drop) and soak (fixed 3-cycle) tests leave:
//
//  1. TestReconnectChaos_RandomizedDropModes — a seeded, deterministic
//     multi-cycle loop that shuffles hard-close and in-process-freeze drops
//     and drives a full redial+reattach each cycle, asserting durable remote
//     work survives every reconnect and that neither FDs nor goroutines grow.
//
//  2. TestReconnectChaos_RealSSHDFreezeThenReconnect — the genuine black-hole
//     path end to end: a real sshd frozen with SIGSTOP (whole descendant tree,
//     via SSHDFreezeHarness) is detected by the *real* bounded-keepalive
//     Dead() probe (not a raw SendRequest), then recovered and reconnected
//     through Reconnector.Run. Skipped (never failed) when sshd is absent.
//
// Lessons honored (see .claude/rules): backoff is injected at microsecond
// scale — no real 1s–30s waits; time-driven invariants use observed events
// and generous ceilings, never a fixed wall-clock value as the primary
// assertion; the Dead() consumer branches "transport died -> reconnect"
// against "clean exit"; and the freeze harness's whole-tree SIGSTOP with
// multi-scan settle is reused, not reimplemented.

// chaosFastBackoff keeps every reconnect attempt at microsecond scale so the
// chaos loop never waits out a real backoff schedule (lessons-ssh-transport
// #11). MaxDelay is a hair above BaseDelay so growth is still exercised
// without ever reaching human-perceptible latency.
var chaosFastBackoff = BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Millisecond}

// TestReconnectChaos_RandomizedDropModes drives a deterministic-yet-varied
// sequence of drops (a seed-shuffled mix of hard-close and freeze/black-hole)
// through the real Reconnector, proving the state machine survives an
// arbitrary interleaving of both transport-death modes and that a
// long-running remote producer's output is never lost across any of them.
//
// It generalizes the fixed-order 3-cycle soak into a shuffled 6-cycle sweep
// (3 of each mode, guaranteed), extracting more signal per run while staying
// fully reproducible: the schedule is a seeded shuffle, and every timing
// assertion is either an event observation (collectAtLeast) or a generous
// ceiling, never a bare wall-clock invariant.
func TestReconnectChaos_RandomizedDropModes(t *testing.T) {
	baselineLeaks := goleak.IgnoreCurrent()
	baselineFDs := openFDCount(t)
	baselineGoroutines := runtime.NumGoroutine()

	// A live remote producer independent of any TCP transport — models a
	// session-host command that keeps producing output across reattaches
	// (spec §1.4). It appends continuously regardless of how many client
	// connections come and go.
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
				sim.append([]byte(fmt.Sprintf("chaos-%d;", n)))
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
			User:            "test",
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         2 * time.Second,
		}
		c, err := ssh.Dial("tcp", harness.Addr(), cfg)
		if err != nil {
			return nil, err
		}
		return &Client{SSH: c, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}, nil
	}
	attach := func(_ context.Context, c *Client) (*PTYSession, error) {
		return OpenPTYSessionWithCommand(c.SSH, RemoteReattachCommand("chaos-session"), TermSize{Cols: 80, Rows: 24})
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
		t.Fatalf("remote work did not start before chaos: %q", got)
	}

	// Deterministic schedule: exactly 3 freezes and 3 hard-closes, shuffled by
	// a fixed seed. Guaranteeing both modes appear (rather than sampling
	// Intn(2) per cycle, which could degenerate to all-one-mode) keeps the
	// test's coverage claim honest while remaining fully reproducible.
	schedule := []bool{true, true, true, false, false, false} // true = freeze/black-hole
	rng := rand.New(rand.NewSource(0xC0FFEE))
	rng.Shuffle(len(schedule), func(i, j int) { schedule[i], schedule[j] = schedule[j], schedule[i] })

	var markers []string
	freezes, drops := 0, 0
	for cycle, freeze := range schedule {
		before := len(sim.snapshot())
		if freeze {
			freezes++
			// Black-hole: hold the connection (no RST/FIN), append work the
			// client cannot see while blinded, then let the accept path
			// recover shortly after Run is already retrying.
			harness.Freeze()
			sim.append([]byte(fmt.Sprintf("FROZEN-WORK-%d;", cycle)))
		} else {
			drops++
			harness.Drop()
		}
		session.Close()
		client.Close()
		if freeze {
			go func() {
				time.Sleep(50 * time.Millisecond)
				harness.Resume()
			}()
		}

		r := &Reconnector{
			Backoff:  chaosFastBackoff,
			Dial:     dial,
			Attach:   attach,
			OnStatus: func(msg string) { markers = append(markers, msg) },
		}
		client, session, err = r.Run(context.Background())
		if err != nil {
			t.Fatalf("cycle %d (freeze=%v) reconnect: %v", cycle, freeze, err)
		}
		reader = startChannelReader(session.channel)
		// The reattached session must replay at least everything accumulated
		// so far (durable work invariant), and the producer must have kept
		// advancing across the drop (len grew past `before`).
		want := len(sim.snapshot())
		got := collectAtLeast(reader, want, 3*time.Second)
		if len(got) < want || len(sim.snapshot()) <= before {
			t.Fatalf("cycle %d (freeze=%v): in-flight remote work did not survive: replay=%d want>=%d before=%d now=%d",
				cycle, freeze, len(got), want, before, len(sim.snapshot()))
		}
	}

	if freezes == 0 || drops == 0 {
		t.Fatalf("chaos schedule did not exercise both modes: freezes=%d drops=%d", freezes, drops)
	}

	client.Close()
	session.Close()
	harness.Stop()
	close(stopWork)
	<-workDone

	// Every cycle must have logged an ordered reconnecting->reconnected pair.
	assertReconnectMarkerOrder(t, markers, len(schedule))
	// No FD or goroutine growth after an arbitrary interleaving of drops —
	// the resource-cleanliness invariant the reconnect path must uphold.
	waitForResourceBaseline(t, baselineFDs, baselineGoroutines)
	goleak.VerifyNone(t, baselineLeaks)
}

// TestReconnectChaos_RealSSHDFreezeThenReconnect exercises the full genuine
// black-hole arc against a REAL sshd: freeze it (SIGSTOP, whole descendant
// tree), confirm the real bounded-keepalive Dead() probe fires (proving the
// liveness signal advances under a true no-RST black hole — the failure mode
// .claude/rules/lessons-liveness-probes.md exists to prevent), then recover
// the host and reconnect through Reconnector.Run.
//
// This is the missing seam: client_test.go's freeze test asserts a *raw*
// SendRequest hangs while frozen, and the soak uses an in-process banner-hold
// stand-in — neither drives the real keepalive->Dead() detector against a real
// SIGSTOP and then through the reconnect state machine. Skipped (never failed)
// when sshd/ssh-keygen are absent, per acceptance criterion 1. Not t.Parallel:
// it owns a real OS subprocess (AGENTS.md real-process-test convention).
func TestReconnectChaos_RealSSHDFreezeThenReconnect(t *testing.T) {
	privPEM, clientPub := generateClientKey(t)
	harness, err := NewSSHDFreezeHarness(t, clientPub)
	if errors.Is(err, ErrSSHDNotFound) {
		t.Skip("sshd/ssh-keygen not found on PATH; skipping real-sshd freeze chaos test")
	}
	if err != nil {
		t.Fatalf("NewSSHDFreezeHarness: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		t.Fatalf("parse client key: %v", err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("looking up current OS user: %v", err)
	}
	clientCfg := &ssh.ClientConfig{
		// sshd runs unprivileged as this test process's own user and can only
		// authenticate that same OS account (no setuid to an arbitrary user).
		User:            currentUser.Username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	dial := func(context.Context) (*Client, error) {
		sc, err := ssh.Dial("tcp", harness.Addr(), clientCfg)
		if err != nil {
			return nil, err
		}
		return &Client{SSH: sc, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}, nil
	}

	client, err := dial(context.Background())
	if err != nil {
		t.Fatalf("initial dial to real sshd: %v", err)
	}
	// A short keepalive so the *real* detector — not a hand-rolled probe —
	// trips the black hole quickly: interval 30ms > sendTimeout 10ms, so each
	// bounded SendRequest still lets the miss counter advance every tick
	// (lessons-liveness-probes.md). 3 missed ticks (~90-120ms) close Dead().
	client.startKeepalive(30*time.Millisecond, 10*time.Millisecond, 3)

	// Deterministic trigger: SIGSTOP the whole sshd descendant tree.
	if err := harness.Freeze(); err != nil {
		t.Fatalf("Freeze: %v", err)
	}

	// The transport-died branch: Dead() must fire under the black hole. A
	// generous 3s ceiling guards a ~100ms expectation — the ceiling is not the
	// invariant, the channel close is.
	select {
	case <-client.Dead():
		// expected: transport died -> we reconnect (never "clean exit").
	case <-time.After(3 * time.Second):
		t.Fatal("Dead() did not fire while sshd was frozen (black hole undetected)")
	}
	// startKeepalive closed client.SSH on the final miss; the keepalive
	// goroutine has already returned. Nothing further to close on this client.

	// Recover the host and reconnect through the real state machine. A trivial
	// exec attach (echo) stands in for session-host reattach — real sshd has no
	// session-host, but the redial+reopen+exec path is what we are exercising.
	if err := harness.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	const marker = "CHAOS_RECONNECT_OK"
	attach := func(_ context.Context, c *Client) (*PTYSession, error) {
		return OpenPTYSessionWithCommand(c.SSH, "echo "+marker, TermSize{Cols: 80, Rows: 24})
	}
	r := &Reconnector{Backoff: chaosFastBackoff, Dial: dial, Attach: attach}
	newClient, newSession, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Reconnector.Run after real-sshd recovery: %v", err)
	}
	defer newClient.Close()
	defer newSession.Close()

	out := collectAtLeast(startChannelReader(newSession.channel), len(marker), 3*time.Second)
	if !bytes.Contains(out, []byte(marker)) {
		t.Fatalf("reconnected exec did not produce %q; got %q", marker, out)
	}
}

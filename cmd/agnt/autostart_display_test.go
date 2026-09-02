package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// recNotifier captures notifications so tests assert on what the user would
// see regardless of which sink (overlay stack or line writer) is wired.
type recNotifier struct{ got []overlay.Notification }

func (r *recNotifier) Notify(n overlay.Notification) { r.got = append(r.got, n) }

func (r *recNotifier) texts() string {
	var b strings.Builder
	for _, n := range r.got {
		b.WriteString(n.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func TestDisplayAutostartResults_RegistrationErrorIsVisible(t *testing.T) {
	done := make(chan struct{})
	close(done)
	handle := &daemonSessionHandle{
		registrationDone: done,
		registrationErr:  errors.New("connect: socket refused"),
	}

	rec := &recNotifier{}
	var out bytes.Buffer
	displayAutostartResults(handle, rec, nil, &out, time.Second)

	got := rec.texts()
	if !strings.Contains(got, "daemon session unavailable") {
		t.Fatalf("expected daemon registration error, got %q", got)
	}
	if !strings.Contains(got, "startup logs will not run") {
		t.Fatalf("expected startup-log consequence, got %q", got)
	}
	if rec.got[0].Level != overlay.LevelError {
		t.Fatalf("registration failure must be an error notification, got %v", rec.got[0].Level)
	}
	if out.Len() != 0 {
		t.Fatalf("one-line outcomes must not write to the terminal, got %q", out.String())
	}
}

func TestDisplayAutostartResults_RegistrationTimeoutIsVisible(t *testing.T) {
	handle := &daemonSessionHandle{registrationDone: make(chan struct{})}

	rec := &recNotifier{}
	var out bytes.Buffer
	displayAutostartResults(handle, rec, nil, &out, time.Nanosecond)

	got := rec.texts()
	if !strings.Contains(got, "daemon session registration timed out") {
		t.Fatalf("expected registration timeout, got %q", got)
	}
	if !strings.Contains(got, "startup logs may not run") {
		t.Fatalf("expected startup-log consequence, got %q", got)
	}
}

// The in-progress "autostart starting …" notice is pure progress noise, so a
// normal session must stay silent and only debug mode may surface it.
func TestDisplayAutostartResults_InProgressStatusHiddenByDefault(t *testing.T) {
	done := make(chan struct{})
	close(done)
	handle := &daemonSessionHandle{
		registrationDone: done,
		autostartStatus:  "starting",
		autostartHandle:  "/tmp/project",
	}

	rec := &recNotifier{}
	var out bytes.Buffer
	displayAutostartResults(handle, rec, nil, &out, time.Second)

	if got := rec.texts(); strings.Contains(got, "autostart starting") {
		t.Fatalf("in-progress notice should be hidden without debug, got %q", got)
	}
}

func TestDisplayAutostartResults_InProgressStatusVisibleUnderDebug(t *testing.T) {
	debug.Enable()
	t.Cleanup(debug.Disable)

	done := make(chan struct{})
	close(done)
	handle := &daemonSessionHandle{
		registrationDone: done,
		autostartStatus:  "starting",
		autostartHandle:  "/tmp/project",
	}

	rec := &recNotifier{}
	var out bytes.Buffer
	displayAutostartResults(handle, rec, nil, &out, time.Second)

	got := rec.texts()
	if !strings.Contains(got, "autostart starting") {
		t.Fatalf("expected in-progress autostart status under debug, got %q", got)
	}
	if !strings.Contains(got, "daemon startup_log") {
		t.Fatalf("expected next diagnostic command, got %q", got)
	}
}

// Started scripts/proxies are one summary notification; errors keep their
// multi-line dump on the terminal writer, since a stack row cannot hold it.
func TestDisplayAutostartResults_SummaryAndErrorDump(t *testing.T) {
	done := make(chan struct{})
	close(done)
	handle := &daemonSessionHandle{
		registrationDone: done,
		autostartScripts: []string{"dev"},
		autostartProxies: []string{"web"},
		autostartErrors:  []string{"api: exit 1\nbind: address already in use"},
	}

	rec := &recNotifier{}
	var out bytes.Buffer
	displayAutostartResults(handle, rec, nil, &out, time.Second)

	got := rec.texts()
	if !strings.Contains(got, "auto-started: dev, web (1 errors)") {
		t.Fatalf("expected summary notification, got %q", got)
	}
	if rec.got[len(rec.got)-1].Level != overlay.LevelWarn {
		t.Fatalf("summary with errors must be a warning")
	}
	if !strings.Contains(out.String(), "autostart error: api: exit 1") || !strings.Contains(out.String(), "| bind: address already in use") {
		t.Fatalf("expected multi-line error dump on the terminal writer, got %q", out.String())
	}
}

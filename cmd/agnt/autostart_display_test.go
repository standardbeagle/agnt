package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDisplayAutostartResults_RegistrationErrorIsVisible(t *testing.T) {
	done := make(chan struct{})
	close(done)
	handle := &daemonSessionHandle{
		registrationDone: done,
		registrationErr:  errors.New("connect: socket refused"),
	}

	var out bytes.Buffer
	displayAutostartResults(handle, nil, nil, &out, time.Second)

	got := out.String()
	if !strings.Contains(got, "daemon session unavailable") {
		t.Fatalf("expected daemon registration error, got %q", got)
	}
	if !strings.Contains(got, "startup logs will not run") {
		t.Fatalf("expected startup-log consequence, got %q", got)
	}
}

func TestDisplayAutostartResults_RegistrationTimeoutIsVisible(t *testing.T) {
	handle := &daemonSessionHandle{registrationDone: make(chan struct{})}

	var out bytes.Buffer
	displayAutostartResults(handle, nil, nil, &out, time.Nanosecond)

	got := out.String()
	if !strings.Contains(got, "daemon session registration timed out") {
		t.Fatalf("expected registration timeout, got %q", got)
	}
	if !strings.Contains(got, "startup logs may not run") {
		t.Fatalf("expected startup-log consequence, got %q", got)
	}
}

func TestDisplayAutostartResults_InProgressStatusIsVisible(t *testing.T) {
	done := make(chan struct{})
	close(done)
	handle := &daemonSessionHandle{
		registrationDone: done,
		autostartStatus:  "starting",
		autostartHandle:  "/tmp/project",
	}

	var out bytes.Buffer
	displayAutostartResults(handle, nil, nil, &out, time.Second)

	got := out.String()
	if !strings.Contains(got, "autostart starting") {
		t.Fatalf("expected in-progress autostart status, got %q", got)
	}
	if !strings.Contains(got, "daemon startup_log") {
		t.Fatalf("expected next diagnostic command, got %q", got)
	}
}

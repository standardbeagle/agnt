package main

import (
	"reflect"
	"testing"
)

func TestRunHelpRequestedOnlyBeforeWrappedCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "bare long help", args: []string{"--help"}, want: true},
		{name: "bare short help", args: []string{"-h"}, want: true},
		{name: "bare help word", args: []string{"help"}, want: true},
		{name: "help after agnt flag", args: []string{"--no-overlay", "--help"}, want: true},
		{name: "claude long help passes through", args: []string{"claude", "--help"}, want: false},
		{name: "claude short help passes through", args: []string{"claude", "-h"}, want: false},
		{name: "claude help word passes through", args: []string{"claude", "help"}, want: false},
		{name: "claude help after leading agnt flag passes through", args: []string{"--no-overlay", "claude", "--help"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runHelpRequested(tt.args); got != tt.want {
				t.Fatalf("runHelpRequested(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseRunCommandArgsKeepsChildHelp(t *testing.T) {
	resetRunFlagGlobalsForTest()

	got, err := parseRunCommandArgs([]string{"claude", "--help"})
	if err != nil {
		t.Fatalf("parseRunCommandArgs returned error: %v", err)
	}
	want := []string{"claude", "--help"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command args = %v, want %v", got, want)
	}
}

func TestParseRunCommandArgsRemovesMultipleAgntFlagsWithoutDuping(t *testing.T) {
	resetRunFlagGlobalsForTest()

	got, err := parseRunCommandArgs([]string{
		"--no-overlay",
		"--no-autostart",
		"claude",
		"--session",
		"dev",
		"--dangerously-skip-permissions",
	})
	if err != nil {
		t.Fatalf("parseRunCommandArgs returned error: %v", err)
	}
	want := []string{"claude", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command args = %v, want %v", got, want)
	}
	if useTermOverlay {
		t.Fatal("useTermOverlay = true, want false")
	}
	if !skipAutostart {
		t.Fatal("skipAutostart = false, want true")
	}
	if sessionCode != "dev" {
		t.Fatalf("sessionCode = %q, want dev", sessionCode)
	}
}

func TestParseRunCommandArgsRequiresFlagValues(t *testing.T) {
	resetRunFlagGlobalsForTest()

	for _, args := range [][]string{
		{"--overlay-socket"},
		{"--session"},
		{"--debug-log"},
	} {
		t.Run(args[0], func(t *testing.T) {
			if _, err := parseRunCommandArgs(args); err == nil {
				t.Fatalf("parseRunCommandArgs(%v) returned nil error", args)
			}
		})
	}
}

func resetRunFlagGlobalsForTest() {
	overlaySocketPath = ""
	showIndicator = true
	useTermOverlay = true
	sessionCode = ""
	skipAutostart = false
}

package main

import (
	"errors"
	"testing"
)

func TestSetupWindowsConsoleModes_InputVTFailureRestoresAndDoesNotRun(t *testing.T) {
	want := errors.New("input VT rejected")
	modes := map[uintptr]uint32{1: 0x17, 2: 0x03}
	failed := false
	calls := consoleModeCalls{
		get: func(h uintptr) (uint32, error) { return modes[h], nil },
		set: func(h uintptr, mode uint32) error {
			modes[h] = mode // model a partially mutating syscall failure
			if h == 1 && !failed {
				failed = true
				return want
			}
			return nil
		},
	}
	ran := false
	err := runPreparedConsole(func() bool { return true }, func() (func(), error) {
		return setupWindowsConsoleModes(1, 2, calls)
	}, func(func()) error { ran = true; return nil })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if modes[1] != 0x17 {
		t.Fatalf("stdin mode = %#x, want exact original %#x", modes[1], 0x17)
	}
	if ran {
		t.Fatal("ATTACH/resize phase ran after input setup failure")
	}
}

func TestSetupWindowsConsoleModes_OutputVTFailureRestoresBothModes(t *testing.T) {
	want := errors.New("output VT rejected")
	modes := map[uintptr]uint32{1: 0x17, 2: 0x03}
	failed := false
	calls := consoleModeCalls{
		get: func(h uintptr) (uint32, error) { return modes[h], nil },
		set: func(h uintptr, mode uint32) error {
			modes[h] = mode
			if h == 2 && !failed {
				failed = true
				return want
			}
			return nil
		},
	}
	_, err := setupWindowsConsoleModes(1, 2, calls)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if modes[1] != 0x17 || modes[2] != 0x03 {
		t.Fatalf("modes after rollback = stdin %#x stdout %#x", modes[1], modes[2])
	}
}

//go:build windows

package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessNameByPID_Windows(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ProcessNameByPID(os.Getpid()), filepath.Base(exe); !strings.EqualFold(got, want) {
		t.Fatalf("ProcessNameByPID(self) = %q, want %q", got, want)
	}
	for _, pid := range []int{-1, 0, 999999999} {
		if got := ProcessNameByPID(pid); got != "" {
			t.Errorf("ProcessNameByPID(%d) = %q, want empty", pid, got)
		}
	}

	names := ProcessNamesByPIDs(context.Background(), []int{os.Getpid(), 999999999})
	if len(names) != 1 || names[os.Getpid()] == "" {
		t.Fatalf("ProcessNamesByPIDs = %v, want only this process named", names)
	}
}

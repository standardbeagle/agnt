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

// TestNetstatListeners_LocalizedState pins that native-Windows port detection
// does not depend on the display language: netstat prints the state word
// translated (ABHÖREN on German Windows), so a listener is recognised by its
// shape (remote port 0), never by the word LISTENING.
func TestNetstatListeners_LocalizedState(t *testing.T) {
	german := []byte("\r\nAktive Verbindungen\r\n\r\n" +
		"  Proto  Lokale Adresse         Remoteadresse          Status           PID\r\n" +
		"  TCP    0.0.0.0:3000           0.0.0.0:0              ABHÖREN          4242\r\n" +
		"  TCP    [::]:3000              [::]:0                 ABHÖREN          4242\r\n" +
		"  TCP    127.0.0.1:5432         0.0.0.0:0              ABHÖREN          9999\r\n" +
		"  TCP    10.0.0.5:54321         93.184.216.34:443      HERGESTELLT      2222\r\n")
	orig := netstatOutput
	netstatOutput = func(context.Context) ([]byte, error) { return german, nil }
	t.Cleanup(func() { netstatOutput = orig })

	owners := ListListeningPorts(context.Background())
	if len(owners) != 2 || owners[0].Port != 3000 || owners[0].PID != 4242 ||
		owners[1].Port != 5432 || owners[1].PID != 9999 || !owners[0].Windows {
		t.Fatalf("ListListeningPorts = %+v, want ports 3000/4242 and 5432/9999", owners)
	}
	if pids := FindPIDsByPort(context.Background(), 3000); len(pids) != 1 || pids[0] != 4242 {
		t.Fatalf("FindPIDsByPort(3000) = %v, want [4242]", pids)
	}
	if pids := FindPIDsByPort(context.Background(), 54321); pids != nil {
		t.Fatalf("FindPIDsByPort(54321) = %v, want nil (ESTABLISHED is not a listener)", pids)
	}
}

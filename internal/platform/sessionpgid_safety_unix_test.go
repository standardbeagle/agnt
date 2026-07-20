//go:build !windows

package platform

import (
	"errors"
	"testing"
)

func TestMembersOfPGIDSurfacesScanFailure(t *testing.T) {
	want := errors.New("process table unavailable")
	_, err := membersOfPGIDWith(42,
		func() ([]ProcInfo, error) { return nil, want },
		func(int) int { return 0 },
		func(int) (int, error) { return 0, nil },
	)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want scan failure", err)
	}
}

func TestMembersOfPGIDReportsSurvivors(t *testing.T) {
	members, err := membersOfPGIDWith(42,
		func() ([]ProcInfo, error) { return []ProcInfo{{PID: 99}}, nil },
		func(int) int { return 42 },
		func(int) (int, error) { return 0, nil },
	)
	if err != nil || len(members) != 1 || members[0] != 99 {
		t.Fatalf("members=%v err=%v, want survivor 99", members, err)
	}
}

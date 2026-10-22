package daemon

import (
	"os"
	"testing"

	"github.com/standardbeagle/agnt/internal/platform"
)

func TestOwnerConfirmedGoneDetectsRecycledPID(t *testing.T) {
	pid := os.Getpid()
	birth, ok := platform.ProcessBirthID(pid)
	if !ok {
		t.Skip("no process birth token on this platform")
	}

	same := &Session{OwnerPID: pid, ownerBirthID: birth}
	if ownerConfirmedGone(same) {
		t.Fatal("live owner with matching birth token reported gone")
	}
	recycled := &Session{OwnerPID: pid, ownerBirthID: birth + "-stale"}
	if !ownerConfirmedGone(recycled) {
		t.Fatal("PID reused by a different process not reported gone")
	}
	unknown := &Session{OwnerPID: pid}
	if ownerConfirmedGone(unknown) {
		t.Fatal("live owner with no recorded birth token must fail safe")
	}
}

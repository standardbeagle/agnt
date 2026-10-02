//go:build unix

package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
)

// The live-leader case needs a real process group (startPGIDLeader uses
// Setpgid), which only exists on unix; the identity-matching rules in
// daemon_session_cleanup_identity_test.go are platform-neutral and run everywhere.
func TestInspectSessionPGIDIdentity_RecognizesLiveGroupLeader(t *testing.T) {
	pgid, _ := startPGIDLeader(t)

	identity, ok := inspectSessionPGIDIdentity(pgid)
	if !ok || identity.pgid != pgid || identity.members[pgid] == "" {
		t.Fatalf("live pgid leader identity = %+v ok=%v", identity, ok)
	}
	time.Sleep(10 * time.Millisecond)
	if !sessionPGIDIdentityMatches(identity, platform.MembersOfPGID(pgid), platform.ProcessBirthID) {
		t.Fatalf("live leader birth identity changed: %+v", identity)
	}
}

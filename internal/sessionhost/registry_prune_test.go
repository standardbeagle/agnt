package sessionhost

import (
	"testing"
	"time"
)

func TestRegistryPrunesLongExitedSessions(t *testing.T) {
	r := NewRegistry()
	old := &Session{ID: "old"}
	old.exitedAt.Store(time.Now().Add(-2 * exitedRetention).UnixNano())
	recent := &Session{ID: "recent"}
	recent.exitedAt.Store(time.Now().UnixNano())
	running := &Session{ID: "running"}
	for _, s := range []*Session{old, recent, running} {
		r.sessions.Store(s.ID, s)
	}

	r.Add(&Session{ID: "new"})

	if _, ok := r.Get("old"); ok {
		t.Fatal("session exited past the retention window was kept")
	}
	for _, id := range []string{"recent", "running", "new"} {
		if _, ok := r.Get(id); !ok {
			t.Fatalf("session %q was pruned", id)
		}
	}
}

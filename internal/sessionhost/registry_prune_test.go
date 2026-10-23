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

func TestRegistry_PruneExitedWithoutAdd(t *testing.T) {
	r := NewRegistry()
	old := &Session{ID: "old"}
	old.exitedAt.Store(time.Now().Add(-2 * exitedRetention).UnixNano())
	fresh := &Session{ID: "fresh"}
	fresh.exitedAt.Store(time.Now().UnixNano())
	live := &Session{ID: "live"}
	for _, s := range []*Session{old, fresh, live} {
		r.sessions.Store(s.ID, s)
	}

	if got := r.ExitedCount(); got != 2 {
		t.Fatalf("ExitedCount = %d, want 2", got)
	}
	if got := r.PruneExited(); got != 1 {
		t.Fatalf("PruneExited = %d, want 1 (only the long-exited one)", got)
	}
	if _, ok := r.Get("old"); ok {
		t.Fatal("long-exited session still registered")
	}
	if _, ok := r.Get("fresh"); !ok {
		t.Fatal("recently exited session was pruned")
	}
}

package daemon

import (
	"testing"
	"time"
)

// TestFindByDirectory_DeterministicTie verifies that when two active sessions
// share the SAME normalized ProjectPath (a depth tie), FindByDirectory
// deterministically returns the most recently started session, with Code as the
// final tiebreak. Without the tiebreak the winner depends on sync.Map.Range
// order, which is undefined.
func TestFindByDirectory_DeterministicTie(t *testing.T) {
	t.Parallel()

	const projectPath = "/home/user/shared-project"

	// older first, newer second (and a variant the other way around) to prove
	// the winner is order-independent of registration order.
	cases := []struct {
		name       string
		olderFirst bool
		olderCode  string
		newerCode  string
	}{
		{name: "older-registered-first", olderFirst: true, olderCode: "claude-1", newerCode: "claude-2"},
		{name: "newer-registered-first", olderFirst: false, olderCode: "claude-1", newerCode: "claude-2"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewSessionRegistry(60 * time.Second)

			base := time.Now()
			older := &Session{
				Code:        tc.olderCode,
				ProjectPath: projectPath,
				Command:     "claude",
				StartedAt:   base,
				Status:      SessionStatusActive,
			}
			newer := &Session{
				Code:        tc.newerCode,
				ProjectPath: projectPath,
				Command:     "claude",
				StartedAt:   base.Add(5 * time.Second),
				Status:      SessionStatusActive,
			}

			if tc.olderFirst {
				_ = registry.Register(older)
				_ = registry.Register(newer)
			} else {
				_ = registry.Register(newer)
				_ = registry.Register(older)
			}

			for i := 0; i < 200; i++ {
				got, ok := registry.FindByDirectory(projectPath)
				if !ok {
					t.Fatalf("iteration %d: FindByDirectory returned no match", i)
				}
				if got.Code != tc.newerCode {
					t.Fatalf("iteration %d: FindByDirectory returned %q, want newest %q", i, got.Code, tc.newerCode)
				}
			}
		})
	}
}

// TestFindByDirectory_EqualStartedAtCodeTiebreak verifies that when StartedAt is
// identical, the lexicographically greater Code wins, deterministically.
func TestFindByDirectory_EqualStartedAtCodeTiebreak(t *testing.T) {
	t.Parallel()

	const projectPath = "/home/user/equal-time-project"
	ts := time.Now()

	a := &Session{Code: "alpha", ProjectPath: projectPath, Command: "claude", StartedAt: ts, Status: SessionStatusActive}
	z := &Session{Code: "zeta", ProjectPath: projectPath, Command: "claude", StartedAt: ts, Status: SessionStatusActive}

	registry := NewSessionRegistry(60 * time.Second)
	_ = registry.Register(a)
	_ = registry.Register(z)

	for i := 0; i < 200; i++ {
		got, ok := registry.FindByDirectory(projectPath)
		if !ok {
			t.Fatalf("iteration %d: FindByDirectory returned no match", i)
		}
		if got.Code != "zeta" {
			t.Fatalf("iteration %d: FindByDirectory returned %q, want lexicographically-greatest Code %q", i, got.Code, "zeta")
		}
	}
}

// TestFindByDirectory_DeeperWins verifies existing behavior is preserved: a
// deeper nested session path still wins over a shallower parent, regardless of
// recency.
func TestFindByDirectory_DeeperWins(t *testing.T) {
	t.Parallel()

	parent := &Session{
		Code:        "parent",
		ProjectPath: "/home/user/project",
		Command:     "claude",
		StartedAt:   time.Now().Add(10 * time.Second), // more recent, but shallower
		Status:      SessionStatusActive,
	}
	child := &Session{
		Code:        "child",
		ProjectPath: "/home/user/project/sub",
		Command:     "claude",
		StartedAt:   time.Now(), // older, but deeper
		Status:      SessionStatusActive,
	}

	registry := NewSessionRegistry(60 * time.Second)
	_ = registry.Register(parent)
	_ = registry.Register(child)

	for i := 0; i < 50; i++ {
		got, ok := registry.FindByDirectory("/home/user/project/sub")
		if !ok {
			t.Fatalf("iteration %d: FindByDirectory returned no match", i)
		}
		if got.Code != "child" {
			t.Fatalf("iteration %d: deeper match should win; got %q want %q", i, got.Code, "child")
		}
	}

	// And the shallow parent is still found for a directory only it matches.
	got, ok := registry.FindByDirectory("/home/user/project")
	if !ok || got.Code != "parent" {
		t.Fatalf("FindByDirectory(parent dir) = %v ok=%v, want parent", got, ok)
	}
}

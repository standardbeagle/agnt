//go:build unix

package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
)

// TestPgidOwnershipCheck_Owned_AgntRunAncestor covers the positive path:
// a live member whose parent chain contains an ancestor with cmdline
// "agnt run ..." AND cwd inside a known project directory is classified
// as owned by this daemon and eligible for reaping.
func TestPgidOwnershipCheck_Owned_AgntRunAncestor(t *testing.T) {
	projectA := "/home/user/projects/webapp"
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			// Immediate parent: a shell. Doesn't match cmdline, but
			// the ownership gate only needs SOME ancestor to match.
			{PID: 2001, PPID: 1001, Cmdline: "sh -c npm run dev", Cwd: projectA},
			// Grandparent: agnt run itself, with project cwd.
			{PID: 1001, PPID: 500, Cmdline: "agnt run --code claude-1 claude", Cwd: projectA},
		}
	}

	owned, reason := pgidOwnershipCheck(
		[]int{3000}, // live member pid
		[]string{projectA},
		"/usr/local/bin/agnt",
		walkFn,
	)
	if !owned {
		t.Fatalf("expected owned=true, got false (reason=%q)", reason)
	}
	if reason == "" {
		t.Errorf("expected non-empty reason when owned")
	}
}

// TestPgidOwnershipCheck_Owned_DaemonBinary covers matching via the
// daemon's own binary path instead of the "agnt run" substring. A process
// whose cmdline begins with the daemon binary path is considered an agnt
// ancestor.
func TestPgidOwnershipCheck_Owned_DaemonBinary(t *testing.T) {
	projectA := "/srv/projects/api"
	daemonBin := "/opt/agnt/bin/agnt"
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			{PID: 50, PPID: 1, Cmdline: daemonBin + " daemon start --socket /tmp/agnt.sock", Cwd: projectA},
		}
	}
	owned, reason := pgidOwnershipCheck([]int{100}, []string{projectA}, daemonBin, walkFn)
	if !owned {
		t.Fatalf("expected owned=true for daemon-binary ancestor, got false (reason=%q)", reason)
	}
}

// TestPgidOwnershipCheck_Unowned_CwdMismatch covers the multi-daemon bug
// scenario: a second daemon scanning sees the first daemon's live "agnt
// run" ancestor, but its cwd is a project the scanning daemon does NOT
// know about. Must classify as unowned (skip reaping).
func TestPgidOwnershipCheck_Unowned_CwdMismatch(t *testing.T) {
	myProject := "/home/user/projects/myapp"
	otherProject := "/home/user/projects/not-my-app"
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			{PID: 1001, PPID: 500, Cmdline: "agnt run --code other-1 claude", Cwd: otherProject},
		}
	}
	owned, reason := pgidOwnershipCheck(
		[]int{3000},
		[]string{myProject}, // only myProject is known
		"/usr/local/bin/agnt",
		walkFn,
	)
	if owned {
		t.Fatalf("expected owned=false for cwd outside known projects, got true (reason=%q)", reason)
	}
	if reason == "" {
		t.Errorf("expected non-empty reason when unowned")
	}
}

// TestPgidOwnershipCheck_Unowned_CmdlineMismatch covers a pgid whose
// ancestors have matching cwd but none has an agnt-looking cmdline — e.g.
// a random "sh -c sleep 30 &" leaked pgid. Must classify as unowned.
func TestPgidOwnershipCheck_Unowned_CmdlineMismatch(t *testing.T) {
	proj := "/home/user/projects/foo"
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			{PID: 2001, PPID: 1, Cmdline: "sh -c echo hi", Cwd: proj},
			{PID: 200, PPID: 1, Cmdline: "bash", Cwd: proj},
		}
	}
	owned, _ := pgidOwnershipCheck([]int{3000}, []string{proj}, "/usr/local/bin/agnt", walkFn)
	if owned {
		t.Fatalf("expected owned=false when no ancestor cmdline matches agnt, got true")
	}
}

// TestPgidOwnershipCheck_Unowned_EmptyKnownProjects guards the daemon-
// startup case: projectPath is empty AND the session registry is empty,
// so the known-projects set is empty. The gate must refuse to reap
// anything — this is the conservative fix for the cross-daemon bug.
func TestPgidOwnershipCheck_Unowned_EmptyKnownProjects(t *testing.T) {
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			{PID: 1001, PPID: 1, Cmdline: "agnt run claude", Cwd: "/anywhere"},
		}
	}
	owned, reason := pgidOwnershipCheck([]int{3000}, nil, "/usr/local/bin/agnt", walkFn)
	if owned {
		t.Fatalf("expected owned=false with empty known projects, got true (reason=%q)", reason)
	}
}

// TestPgidOwnershipCheck_Unowned_EmptyMembers covers the defensive case
// where ScanOrphanedPGIDs returned zero live members. The gate must not
// classify it as owned (there is nothing to walk).
func TestPgidOwnershipCheck_Unowned_EmptyMembers(t *testing.T) {
	walkFn := func(pid int) []platform.AncestorInfo {
		// Should never be called, but return something clearly matching
		// to prove the gate is bailing BEFORE walking.
		return []platform.AncestorInfo{
			{PID: 1, PPID: 0, Cmdline: "agnt run foo", Cwd: "/proj"},
		}
	}
	owned, _ := pgidOwnershipCheck(nil, []string{"/proj"}, "", walkFn)
	if owned {
		t.Fatalf("expected owned=false for empty members, got true")
	}
}

// TestPgidOwnershipCheck_Owned_CwdSubdir covers cwd matching via subdir.
// An ancestor whose cwd is a descendant of a known project (e.g. a
// subdirectory like "packages/frontend") must count as a cwd match.
func TestPgidOwnershipCheck_Owned_CwdSubdir(t *testing.T) {
	proj := "/home/user/projects/monorepo"
	sub := filepath.Join(proj, "packages", "frontend")
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			{PID: 1001, PPID: 1, Cmdline: "agnt run claude", Cwd: sub},
		}
	}
	owned, _ := pgidOwnershipCheck([]int{3000}, []string{proj}, "/usr/local/bin/agnt", walkFn)
	if !owned {
		t.Fatalf("expected owned=true for ancestor cwd inside known project subdir")
	}
}

// TestPgidOwnershipCheck_Unowned_CwdSiblingPath makes sure a prefix
// collision like /home/user/projects/foo vs /home/user/projects/foobar
// does NOT get matched. filepath.Rel-based subdir detection must be
// path-aware, not plain string prefix.
func TestPgidOwnershipCheck_Unowned_CwdSiblingPath(t *testing.T) {
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			{PID: 1001, PPID: 1, Cmdline: "agnt run claude", Cwd: "/home/user/projects/foobar"},
		}
	}
	owned, _ := pgidOwnershipCheck(
		[]int{3000},
		[]string{"/home/user/projects/foo"},
		"/usr/local/bin/agnt",
		walkFn,
	)
	if owned {
		t.Fatalf("sibling-path prefix collision must NOT match known project")
	}
}

// TestPgidOwnershipCheck_OwnershipSplitAcrossAncestors covers the case
// where the cmdline match and cwd match come from DIFFERENT ancestors in
// the chain. The gate is AND across the whole chain, not per-ancestor.
func TestPgidOwnershipCheck_OwnershipSplitAcrossAncestors(t *testing.T) {
	proj := "/home/user/projects/app"
	walkFn := func(pid int) []platform.AncestorInfo {
		return []platform.AncestorInfo{
			// Immediate parent: cwd is project but cmdline is not agnt.
			{PID: 2001, PPID: 1001, Cmdline: "node dev-server.js", Cwd: proj},
			// Grandparent: cmdline is agnt run but cwd is /tmp.
			{PID: 1001, PPID: 500, Cmdline: "agnt run claude", Cwd: "/tmp"},
		}
	}
	owned, _ := pgidOwnershipCheck([]int{3000}, []string{proj}, "/usr/local/bin/agnt", walkFn)
	if !owned {
		t.Fatalf("expected owned=true when cmdline + cwd match come from different ancestors")
	}
}

// TestPgidOwnershipCheck_MultipleMembers_AnyMatch covers the multi-
// member case: if ANY live member's ancestor chain satisfies both
// predicates, the pgid is owned. Members are independent walks.
func TestPgidOwnershipCheck_MultipleMembers_AnyMatch(t *testing.T) {
	proj := "/home/user/projects/app"
	walkFn := func(pid int) []platform.AncestorInfo {
		switch pid {
		case 100:
			// First member: no match.
			return []platform.AncestorInfo{
				{PID: 50, PPID: 1, Cmdline: "sh", Cwd: "/etc"},
			}
		case 200:
			// Second member: full match.
			return []platform.AncestorInfo{
				{PID: 60, PPID: 1, Cmdline: "agnt run claude", Cwd: proj},
			}
		}
		return nil
	}
	owned, _ := pgidOwnershipCheck([]int{100, 200}, []string{proj}, "/usr/local/bin/agnt", walkFn)
	if !owned {
		t.Fatalf("expected owned=true when any member ancestor chain matches")
	}
}

// TestKnownProjectPaths_DedupesAndIgnoresEmpty verifies the small helper
// that assembles the daemon's known project set from projectPath +
// session registry. Must dedupe and drop empty entries.
func TestKnownProjectPaths_DedupesAndIgnoresEmpty(t *testing.T) {
	d := &Daemon{
		sessionRegistry: NewSessionRegistry(60 * time.Second),
	}
	_ = d.sessionRegistry.Register(&Session{
		Code:        "s1",
		ProjectPath: "/home/user/proj-a",
		OverlayPath: "/tmp/o1",
	})
	_ = d.sessionRegistry.Register(&Session{
		Code:        "s2",
		ProjectPath: "/home/user/proj-a", // duplicate of s1
		OverlayPath: "/tmp/o2",
	})
	_ = d.sessionRegistry.Register(&Session{
		Code:        "s3",
		ProjectPath: "", // empty — must be dropped
		OverlayPath: "/tmp/o3",
	})

	got := d.knownProjectPaths("/home/user/proj-b")
	// Expect proj-a (once), proj-b. Empty dropped.
	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
	}
	if seen["/home/user/proj-a"] != 1 {
		t.Errorf("expected proj-a to appear exactly once, got %d; all=%v", seen["/home/user/proj-a"], got)
	}
	if seen["/home/user/proj-b"] != 1 {
		t.Errorf("expected proj-b to appear exactly once, got %d; all=%v", seen["/home/user/proj-b"], got)
	}
	if seen[""] != 0 {
		t.Errorf("empty path should not appear in known set, got %d", seen[""])
	}
}

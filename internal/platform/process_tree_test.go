package platform

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
)

// fakeTree is an invented process table for killTreeWith. Each process has a
// parent and a birth tick; holders maps a PID to the birth of whoever holds
// it now, which lets a test recycle a PID.
type fakeTree struct {
	mu      sync.Mutex
	procs   []ProcInfo
	holders map[int]string
	killed  []int
	// afterKill runs once, after the first kill, to model a process started
	// while the tree was being killed.
	afterKill func(*fakeTree)
}

type fakeMember struct {
	id ProcessIdentity
	t  *fakeTree
}

func (m fakeMember) identity() ProcessIdentity { return m.id }
func (m fakeMember) release()                  {}
func (m fakeMember) kill(int) error {
	m.t.mu.Lock()
	defer m.t.mu.Unlock()
	if m.t.holders[m.id.PID] != m.id.Birth {
		return fmt.Errorf("pid %d: %w", m.id.PID, ErrProcessIdentityChanged)
	}
	m.t.killed = append(m.t.killed, m.id.PID)
	if f := m.t.afterKill; f != nil {
		m.t.afterKill = nil
		f(m.t)
	}
	return nil
}

func (ft *fakeTree) host() treeHost {
	return treeHost{
		scan: func() ([]ProcInfo, error) {
			ft.mu.Lock()
			defer ft.mu.Unlock()
			return append([]ProcInfo(nil), ft.procs...), nil
		},
		root: func(id ProcessIdentity) (treeMember, error) {
			ft.mu.Lock()
			defer ft.mu.Unlock()
			cur, ok := ft.holders[id.PID]
			if !ok {
				return nil, nil
			}
			if cur != id.Birth {
				return nil, ErrProcessIdentityChanged
			}
			return fakeMember{id: id, t: ft}, nil
		},
		child: func(pid int, parent ProcessIdentity) treeMember {
			ft.mu.Lock()
			defer ft.mu.Unlock()
			var ppid int
			for _, p := range ft.procs {
				if p.PID == pid {
					ppid = p.PPID
				}
			}
			birth := ft.holders[pid]
			if ppid != parent.PID || !birthNotBefore(birth, parent.Birth) || ft.holders[parent.PID] != parent.Birth {
				return nil
			}
			return fakeMember{id: ProcessIdentity{PID: pid, Birth: birth}, t: ft}
		},
	}
}

func (ft *fakeTree) add(pid, ppid int, birth string) {
	ft.procs = append(ft.procs, ProcInfo{PID: pid, PPID: ppid})
	ft.holders[pid] = birth
}

func (ft *fakeTree) killedSorted() []int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	out := append([]int(nil), ft.killed...)
	sort.Ints(out)
	return out
}

func TestKillTreeWith_FollowsOnlyVerifiedLinks(t *testing.T) {
	ft := &fakeTree{holders: map[int]string{}}
	ft.add(10, 1, "100")  // root
	ft.add(11, 10, "110") // child
	ft.add(12, 11, "120") // grandchild
	ft.add(13, 10, "90")  // claims root as parent but is older: a stale link to an earlier holder of PID 10
	ft.add(14, 13, "130") // under the stale link: never reached
	ft.add(20, 1, "50")   // unrelated

	if err := killTreeWith(ProcessIdentity{PID: 10, Birth: "100"}, 0, ft.host()); err != nil {
		t.Fatalf("killTreeWith: %v", err)
	}
	if got := ft.killedSorted(); fmt.Sprint(got) != "[10 11 12]" {
		t.Fatalf("killed %v, want [10 11 12]", got)
	}
}

func TestKillTreeWith_StaleRootKillsNothing(t *testing.T) {
	ft := &fakeTree{holders: map[int]string{}}
	ft.add(10, 1, "200") // PID 10 now belongs to a newer process
	ft.add(11, 10, "210")

	err := killTreeWith(ProcessIdentity{PID: 10, Birth: "100"}, 0, ft.host())
	if !errors.Is(err, ErrProcessIdentityChanged) {
		t.Fatalf("err = %v, want ErrProcessIdentityChanged", err)
	}
	if got := ft.killedSorted(); len(got) != 0 {
		t.Fatalf("killed %v under a stale root", got)
	}
}

func TestKillTreeWith_GoneRootIsSuccess(t *testing.T) {
	ft := &fakeTree{holders: map[int]string{}}
	if err := killTreeWith(ProcessIdentity{PID: 10, Birth: "100"}, 0, ft.host()); err != nil {
		t.Fatalf("a root that no longer exists is a finished kill, got %v", err)
	}
}

// A child started while the first pass was being killed is caught by the
// re-read.
func TestKillTreeWith_CatchesChildStartedDuringKill(t *testing.T) {
	ft := &fakeTree{holders: map[int]string{}}
	ft.add(10, 1, "100")
	ft.afterKill = func(ft *fakeTree) { ft.add(15, 10, "150") }

	if err := killTreeWith(ProcessIdentity{PID: 10, Birth: "100"}, 0, ft.host()); err != nil {
		t.Fatalf("killTreeWith: %v", err)
	}
	if got := ft.killedSorted(); fmt.Sprint(got) != "[10 15]" {
		t.Fatalf("killed %v, want [10 15]", got)
	}
}

// A member that exited and whose PID was reused before its turn is not
// signalled, and that is not an error: the process that was meant is gone.
func TestKillTreeWith_RecycledMemberIsSkippedQuietly(t *testing.T) {
	ft := &fakeTree{holders: map[int]string{}}
	ft.add(10, 1, "100")
	ft.add(11, 10, "110")
	h := ft.host()
	child := h.child
	h.child = func(pid int, parent ProcessIdentity) treeMember {
		m := child(pid, parent)
		if pid == 11 {
			ft.holders[11] = "999" // recycled after it was attached
		}
		return m
	}
	if err := killTreeWith(ProcessIdentity{PID: 10, Birth: "100"}, 0, h); err != nil {
		t.Fatalf("killTreeWith: %v", err)
	}
	if got := ft.killedSorted(); fmt.Sprint(got) != "[10]" {
		t.Fatalf("killed %v, want [10]", got)
	}
}

func TestBirthNotBefore(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"200", "100", true},
		{"100", "100", true},
		{"99", "100", false},
		{"1700000000.000002", "1700000000.000001", true},
		{"1700000000.000001", "1700000001.000000", false},
		{"", "100", false},
		{"x", "100", false},
	}
	for _, c := range cases {
		if got := birthNotBefore(c.a, c.b); got != c.want {
			t.Errorf("birthNotBefore(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

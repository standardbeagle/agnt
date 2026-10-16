package platform

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// ProcInfo describes a running process discovered via OS-level scanning.
type ProcInfo struct {
	PID     int
	PPID    int    // parent PID (0 if unavailable)
	Command string // basename of the executable
	Cmdline string // full command line
	Cwd     string // working directory (may be empty on some platforms)
}

// ProcessIdentity names one process instance: its PID together with the
// kernel birth identity (ProcessBirthID) observed for it. Every OS recycles
// PIDs, Windows quickly; the pair is not recycled. A caller that finds a PID
// on one scan and kills it later carries the identity it observed and kills
// with KillProcess or KillProcessTree, which refuse a PID that now names a
// different process.
type ProcessIdentity struct {
	PID   int    `json:"pid"`
	Birth string `json:"birth"`
}

// ErrProcessIdentityChanged is returned (wrapped) when a PID no longer names
// the process an identity was taken from: the original exited and the PID
// was reused. Nothing is signalled.
var ErrProcessIdentityChanged = errors.New("pid now names a different process")

// IdentifyProcess captures the identity of the process that holds pid now. It
// reports false when no process holds the PID or the platform cannot read a
// birth identity, in which case nothing may be killed by identity.
//
// A process that has exited but still holds its PID (an unreaped zombie on
// Unix; a process whose handle someone still holds on Windows) is identified
// too: the PID is still its own, so the identity is still exact.
func IdentifyProcess(pid int) (ProcessIdentity, bool) {
	if pid <= 0 {
		return ProcessIdentity{}, false
	}
	return identifyProcess(pid)
}

// KillProcess terminates exactly the process id names. On Unix that is
// SIGTERM, up to gracefulTimeout seconds, then SIGKILL, to the PID and — when
// it leads one — its process group, with the birth identity re-checked before
// each signal. On Windows it opens the process, compares its creation time
// with id.Birth and terminates it through that same handle, so the check and
// the kill cannot be separated by a PID reuse; Windows has no SIGTERM for an
// arbitrary process, so gracefulTimeout is not used there.
//
// A process that is already gone is success. A PID that now names another
// process returns ErrProcessIdentityChanged and nothing is signalled.
func KillProcess(id ProcessIdentity, gracefulTimeout int) error {
	if id.PID <= 1 || id.Birth == "" {
		return fmt.Errorf("kill: incomplete process identity %+v", id)
	}
	return killProcess(id, gracefulTimeout)
}

// KillProcessTree terminates the process id names and every descendant.
// Descendants are found through parent-PID links, and a link is trusted only
// when the child was born no earlier than its parent and the parent still
// holds its PID when the child is read, so a PID inherited from an earlier,
// unrelated holder is never followed. On Windows each member's handle stays
// open until the kill is over, which pins its PID for the whole walk. The
// table is read again after the kill to catch a child started meanwhile.
//
// The root's identity is checked like KillProcess's, and a root that names a
// different process returns ErrProcessIdentityChanged with nothing killed.
// A descendant that has exited by the time it is signalled is not an error.
func KillProcessTree(id ProcessIdentity, gracefulTimeout int) error {
	if id.PID <= 1 || id.Birth == "" {
		return fmt.Errorf("kill tree: incomplete process identity %+v", id)
	}
	return killTreeWith(id, gracefulTimeout, processTreeHost())
}

// KillPID identifies pid now and kills it with KillProcess. Use it only for a
// PID that was just obtained; a PID found on an earlier scan must be killed
// through the ProcessIdentity captured at that scan.
func KillPID(pid int, gracefulTimeout int) error {
	return killPID(pid, gracefulTimeout)
}

// treeMember is one process verified to belong to a tree being killed.
type treeMember interface {
	identity() ProcessIdentity
	kill(gracefulTimeout int) error
	release()
}

// treeHost is what killTreeWith needs from the OS. Injected so the walk can be
// tested with invented process tables.
type treeHost struct {
	// scan lists processes with their parent PIDs.
	scan func() ([]ProcInfo, error)
	// root attaches the tree's root, checking id. It returns (nil, nil) when
	// no process holds the PID any more, and an error wrapping
	// ErrProcessIdentityChanged when a different process does.
	root func(id ProcessIdentity) (treeMember, error)
	// child attaches pid only when it is, at this moment, a child of parent:
	// its parent PID is parent.PID, it was born no earlier than parent, and
	// parent still holds its PID. nil otherwise.
	child func(pid int, parent ProcessIdentity) treeMember
}

// treeKillPasses bounds how many times the table is re-read for children
// started while the previous pass was being killed.
const treeKillPasses = 3

func killTreeWith(root ProcessIdentity, gracefulTimeout int, h treeHost) error {
	r, err := h.root(root)
	if err != nil {
		return err
	}
	if r == nil {
		return nil // already gone
	}
	members := map[int]treeMember{root.PID: r}
	defer func() {
		for _, m := range members {
			m.release()
		}
	}()

	var errs []error
	batch, scanErr := collectTree(members, h)
	batch = append([]treeMember{r}, batch...)
	for pass := 0; len(batch) > 0 && pass < treeKillPasses; pass++ {
		errs = append(errs, killMembers(batch, gracefulTimeout)...)
		batch, scanErr = collectTree(members, h)
	}
	if scanErr != nil {
		errs = append(errs, fmt.Errorf("list descendants of pid %d: %w", root.PID, scanErr))
	}
	return errors.Join(errs...)
}

// collectTree adds to members every process reachable from them through
// verified parent links, and returns the ones it added.
func collectTree(members map[int]treeMember, h treeHost) ([]treeMember, error) {
	procs, err := h.scan()
	if err != nil {
		return nil, err
	}
	children := make(map[int][]int, len(procs))
	for _, p := range procs {
		if p.PID > 1 && p.PPID > 0 && p.PPID != p.PID {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}
	queue := make([]int, 0, len(members))
	for pid := range members {
		queue = append(queue, pid)
	}
	var added []treeMember
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, c := range children[parent] {
			if _, seen := members[c]; seen {
				continue
			}
			m := h.child(c, members[parent].identity())
			if m == nil {
				continue
			}
			members[c] = m
			added = append(added, m)
			queue = append(queue, c)
		}
	}
	return added, nil
}

// killMembers kills a batch concurrently, so a graceful timeout is paid once
// per pass rather than once per process. A member whose PID changed hands
// since it was attached has exited, which is what was wanted.
func killMembers(batch []treeMember, gracefulTimeout int) []error {
	errs := make([]error, len(batch))
	var wg sync.WaitGroup
	for i, m := range batch {
		wg.Add(1)
		go func(i int, m treeMember) {
			defer wg.Done()
			if err := m.kill(gracefulTimeout); err != nil && !errors.Is(err, ErrProcessIdentityChanged) {
				errs[i] = fmt.Errorf("pid %d: %w", m.identity().PID, err)
			}
		}(i, m)
	}
	wg.Wait()
	return errs
}

// birthNotBefore reports whether birth identity a was assigned no earlier
// than b. Birth identities are decimal counters on Linux (start ticks) and
// Windows (100ns creation ticks) and "seconds.microseconds" on macOS; within
// one host they always share a format. Anything unparseable is false, which
// makes a tree walk skip the link rather than follow it.
func birthNotBefore(a, b string) bool {
	as, af, ok1 := splitBirth(a)
	bs, bf, ok2 := splitBirth(b)
	if !ok1 || !ok2 {
		return false
	}
	if as != bs {
		return as > bs
	}
	return af >= bf
}

func splitBirth(s string) (whole, frac uint64, ok bool) {
	w, f, hasFrac := strings.Cut(s, ".")
	whole, err := strconv.ParseUint(w, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if hasFrac {
		if frac, err = strconv.ParseUint(f, 10, 64); err != nil {
			return 0, 0, false
		}
	}
	return whole, frac, true
}

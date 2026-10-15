package sshclient

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// probeSeq keeps two probes from colliding when the clock has not ticked
// between them (Windows timer resolution is coarse).
var probeSeq atomic.Uint64

// ProbeSocketPath returns a fresh local socket path for a one-shot probe
// forward, in the same directory as LocalForwardSocketPath. Its name is a
// fixed-length hash of label, pid, a sequence number and the time, so its
// length does not grow with label: a Unix socket path must fit sun_path (104
// bytes on macOS, whose $TMPDIR alone is ~49), and a path built from a
// session or host name stopped fitting once that name passed about ten
// characters.
func ProbeSocketPath(label string) string {
	seed := fmt.Sprintf("%s\x00%d\x00%d\x00%d", label, os.Getpid(), probeSeq.Add(1), time.Now().UnixNano())
	sum := sha256.Sum256([]byte(seed))
	return filepath.Join(filepath.Dir(LocalForwardSocketPath("probe")), fmt.Sprintf("probe-%x.sock", sum[:8]))
}

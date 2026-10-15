package sshclient

import (
	"path/filepath"
	"strings"
	"testing"
)

// macOSSunPathMax is sizeof(sockaddr_un.sun_path) on macOS minus the
// terminating NUL; it is the tightest limit agnt ssh runs under.
const macOSSunPathMax = 103

// A probe path must fit macOS sun_path whatever the session or host name.
// The base directory is a realistic macOS $TMPDIR plus the agnt subdir.
func TestProbeSocketPath_FitsMacOSSunPathForLongNames(t *testing.T) {
	const macTmpAgnt = "/var/folders/zz/zyxvpxvq6csfxvn_n0000000000000/T/agnt"
	for _, label := range []string{"x", "initial-my-long-session-name", strings.Repeat("h", 200)} {
		p := ProbeSocketPath(label)
		if filepath.Dir(p) != filepath.Dir(LocalForwardSocketPath("probe")) {
			t.Fatalf("ProbeSocketPath(%q) = %q, want it beside the forward sockets", label, p)
		}
		onMac := macTmpAgnt + "/" + filepath.Base(p)
		if len(onMac) > macOSSunPathMax {
			t.Fatalf("label %q: probe path %q is %d bytes on macOS, limit %d", label, onMac, len(onMac), macOSSunPathMax)
		}
	}
	if ProbeSocketPath("same") == ProbeSocketPath("same") {
		t.Fatal("two probes with the same label got the same path")
	}
}

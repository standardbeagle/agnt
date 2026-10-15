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

// The forward socket path is built from the host name, so a long host name
// pushed it past macOS sun_path and the bind failed. A name that fits keeps
// its readable form (existing forwards and env hints stay valid); one that
// does not is hashed to a fixed length, deterministically, since the same
// function computes the path both to bind and to dial.
func TestForwardSocketPathIn_FitsMacOSSunPathForLongHosts(t *testing.T) {
	const macTmpAgnt = "/var/folders/zz/zyxvpxvq6csfxvn_n0000000000000/T/agnt"
	if got := forwardSocketPathIn(macTmpAgnt, "devbox"); filepath.Base(got) != "ssh-devbox.sock" {
		t.Fatalf("short host renamed: %q", got)
	}
	long := "user@" + strings.Repeat("build-runner-", 5) + "example.internal"
	other := long + "2"
	for _, host := range []string{long, other, strings.Repeat("h", 300)} {
		p := forwardSocketPathIn(macTmpAgnt, host)
		if len(p) > macOSSunPathMax {
			t.Fatalf("host %q: forward path %q is %d bytes, limit %d", host, p, len(p), macOSSunPathMax)
		}
		if filepath.Dir(p) != filepath.Dir(forwardSocketPathIn(macTmpAgnt, "x")) {
			t.Fatalf("host %q: path %q left the forward directory", host, p)
		}
		if p != forwardSocketPathIn(macTmpAgnt, host) {
			t.Fatalf("host %q: path is not deterministic", host)
		}
	}
	if forwardSocketPathIn(macTmpAgnt, long) == forwardSocketPathIn(macTmpAgnt, other) {
		t.Fatal("two long hosts share one forward socket")
	}
}

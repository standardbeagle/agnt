package proxy

import "testing"

// TestDescribeExecTarget locks the resolved-frame diagnostic used in the hub
// exec-timeout message (feedback #5): a raw "" arg must read as an explicit
// destination, not as a blank "no frame".
func TestDescribeExecTarget(t *testing.T) {
	ps := &ProxyServer{}

	// No active content frame reported yet: empty arg broadcasts to all frames.
	if got := ps.DescribeExecTarget(""); got != `<all frames> (no active content frame reported yet)` {
		t.Errorf("empty target (no active frame) = %q", got)
	}

	// Outer-shell role tokens collapse to the @chrome label.
	for _, tok := range []string{"@chrome", "outer", "shell", "chrome"} {
		if got := ps.DescribeExecTarget(tok); got != "@chrome (outer shell)" {
			t.Errorf("DescribeExecTarget(%q) = %q, want @chrome label", tok, got)
		}
	}

	// An explicit frame id passes through unchanged.
	if got := ps.DescribeExecTarget("frame-42"); got != "frame-42" {
		t.Errorf("explicit id = %q, want frame-42", got)
	}

	// Once a frame is active, inner/active/"" resolve to it.
	ps.SetActiveFrame("frame-7")
	for _, tok := range []string{"", "inner", "active", "content"} {
		if got := ps.DescribeExecTarget(tok); got != "frame-7" {
			t.Errorf("DescribeExecTarget(%q) with active frame = %q, want frame-7", tok, got)
		}
	}
}

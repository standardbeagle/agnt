//go:build !windows

package browser

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckVersionAt_WrapperChildHoldingStdoutDoesNotHang is the regression for
// the version probe outliving its timeout. The fake "chrome" is a wrapper
// script that prints its version and exits, leaving a background child that
// inherited stdout. Output reads stdout to EOF, so without cmd.WaitDelay the
// probe blocked until that child exited (here 20s, in the wild for as long as
// the browser it launched ran) — the context deadline only kills the script,
// which is already gone.
func TestCheckVersionAt_WrapperChildHoldingStdoutDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	chrome := filepath.Join(dir, "chrome")
	script := "#!/bin/sh\nsleep 20 &\necho 'Chromium 121.0.6167.85'\nexit 0\n"
	if err := os.WriteFile(chrome, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	info := CheckVersionAt(chrome)
	elapsed := time.Since(start)

	// versionProbeWaitDelay is 2s; the child lives 20s. Anything near the
	// child's lifetime means the pipe, not the bound, ended the call.
	if elapsed > 8*time.Second {
		t.Fatalf("CheckVersionAt took %s: it waited for the wrapper's child to release stdout", elapsed)
	}
	if info.FullVersion != "121.0.6167.85" || info.MajorVersion != 121 {
		t.Fatalf("version = %q (major %d), want what the wrapper printed before exiting", info.FullVersion, info.MajorVersion)
	}
}

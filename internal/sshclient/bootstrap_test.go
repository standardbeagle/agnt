package sshclient

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBootstrap_MissingRemoteAgntInstallsAndSessionLaunches pins acceptance
// criterion 1: against a fixture sshd with no "agnt" on PATH, CheckRemoteBinary
// detects it's missing, picks SourceLocalCopy (fixture runs on this same
// host, so GOOS/GOARCH always match), InstallRemoteBinary uploads it
// atomically, and a subsequent "agnt --version" — the same command
// RemoteAgntVersion itself uses, i.e. exactly what 'agnt ssh' runs before
// launching a session — now succeeds and reports the newly-installed
// version. That's the fixture-level stand-in for "session launches": the
// remote can now actually run agnt.
func TestBootstrap_MissingRemoteAgntInstallsAndSessionLaunches(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome, remoteHomeEnv(remoteHome)...)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	// Stand-in "local binary": a real, executable shell script that prints
	// the version string in the exact shape parseAgntVersionOutput expects.
	localBinDir := t.TempDir()
	localBinaryPath := filepath.Join(localBinDir, "agnt")
	script := "#!/bin/sh\necho 'agnt v9.9.9'\n"
	if err := os.WriteFile(localBinaryPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake local binary: %v", err)
	}

	opts := BootstrapOptions{
		LocalVersion:    "9.9.9",
		LocalBinaryPath: localBinaryPath,
	}

	decision, err := CheckRemoteBinary(client, opts)
	if err != nil {
		t.Fatalf("CheckRemoteBinary: %v", err)
	}
	if !decision.NeedsInstall {
		t.Fatal("expected NeedsInstall = true for a remote with no agnt on PATH")
	}
	if decision.Source != SourceLocalCopy {
		t.Errorf("Source = %v, want SourceLocalCopy (fixture is same-host, GOOS/GOARCH must match)", decision.Source)
	}
	if decision.RemoteGOOS != runtime.GOOS || decision.RemoteGOARCH != runtime.GOARCH {
		t.Errorf("remote GOOS/GOARCH = %s/%s, want this host's %s/%s", decision.RemoteGOOS, decision.RemoteGOARCH, runtime.GOOS, runtime.GOARCH)
	}
	wantFinal := filepath.Join(remoteHome, ".local", "bin", "agnt")
	if decision.FinalPath != wantFinal {
		t.Errorf("FinalPath = %s, want %s", decision.FinalPath, wantFinal)
	}

	if err := InstallRemoteBinary(client, opts, decision); err != nil {
		t.Fatalf("InstallRemoteBinary: %v", err)
	}

	// Prove the install actually took effect: re-running the exact check
	// bootstrap itself uses now succeeds.
	remoteVersion, err := RemoteAgntVersion(client)
	if err != nil {
		t.Fatalf("RemoteAgntVersion after install: %v", err)
	}
	if remoteVersion != "9.9.9" {
		t.Errorf("post-install remote version = %q, want %q", remoteVersion, "9.9.9")
	}

	// And a second CheckRemoteBinary call now reports no install needed
	// (same major.minor.patch as local — well within the compat window).
	decision2, err := CheckRemoteBinary(client, opts)
	if err != nil {
		t.Fatalf("CheckRemoteBinary (post-install): %v", err)
	}
	if decision2.NeedsInstall {
		t.Errorf("expected NeedsInstall = false after installing a matching version, got true (%s)", decision2.Reason)
	}
}

func TestCheckRemoteBinary_CompatibleVersionNeedsNoInstall(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome, remoteHomeEnv(remoteHome)...)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	// Pre-seed the "remote" with an agnt script reporting a patch-different
	// but same-minor version — inside the compat window.
	binDir := filepath.Join(remoteHome, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "agnt"), []byte("#!/bin/sh\necho 'agnt v1.2.9'\n"), 0o755); err != nil {
		t.Fatalf("seeding remote agnt: %v", err)
	}

	decision, err := CheckRemoteBinary(client, BootstrapOptions{LocalVersion: "1.2.0", LocalBinaryPath: "/unused"})
	if err != nil {
		t.Fatalf("CheckRemoteBinary: %v", err)
	}
	if decision.NeedsInstall {
		t.Errorf("expected NeedsInstall = false for same-minor remote, got true (%s)", decision.Reason)
	}
}

func TestCheckRemoteBinary_OutsideCompatWindowNeedsInstall(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome, remoteHomeEnv(remoteHome)...)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	binDir := filepath.Join(remoteHome, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "agnt"), []byte("#!/bin/sh\necho 'agnt v0.9.0'\n"), 0o755); err != nil {
		t.Fatalf("seeding remote agnt: %v", err)
	}

	decision, err := CheckRemoteBinary(client, BootstrapOptions{LocalVersion: "1.2.0", LocalBinaryPath: "/unused"})
	if err != nil {
		t.Fatalf("CheckRemoteBinary: %v", err)
	}
	if !decision.NeedsInstall {
		t.Fatal("expected NeedsInstall = true for a remote version outside the compat window")
	}
	if decision.Source != SourceLocalCopy {
		t.Errorf("Source = %v, want SourceLocalCopy", decision.Source)
	}
}

func TestRemoteUname_Reports(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	goos, goarch, err := RemoteUname(client)
	if err != nil {
		t.Fatalf("RemoteUname: %v", err)
	}
	if goos != runtime.GOOS {
		t.Errorf("goos = %q, want %q", goos, runtime.GOOS)
	}
	if goarch != runtime.GOARCH {
		t.Errorf("goarch = %q, want %q", goarch, runtime.GOARCH)
	}
}

// TestRemoteUname_UnameFailureIsWindowsUnsupported proves the documented
// loud-fail path: when 'uname -sm' isn't available remote-side (the only
// signal we have from inside a plain SSH exec channel), RemoteUname returns
// ErrWindowsRemoteUnsupported rather than silently guessing a platform.
func TestRemoteUname_UnameFailureIsWindowsUnsupported(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	// PATH stripped down to nothing usable so "uname" resolves to nothing.
	fixture.onSession = execFixtureHandler(t, remoteHome, "PATH=/nonexistent-empty-path")
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	_, _, err := RemoteUname(client)
	if !errors.Is(err, ErrWindowsRemoteUnsupported) {
		t.Fatalf("expected ErrWindowsRemoteUnsupported, got: %v", err)
	}
}

func TestReleaseDownloadURL_MatchesInstallShNamingConvention(t *testing.T) {
	got := ReleaseDownloadURL("standardbeagle/agnt", "1.2.3", "linux", "arm64")
	want := "https://github.com/standardbeagle/agnt/releases/download/v1.2.3/agnt-linux-arm64"
	if got != want {
		t.Errorf("ReleaseDownloadURL = %q, want %q", got, want)
	}

	// "v" prefix on the input version is tolerated (stripped), matching
	// GitHubRelease.GetVersion's own normalization.
	got = ReleaseDownloadURL("standardbeagle/agnt", "v1.2.3", "darwin", "amd64")
	want = "https://github.com/standardbeagle/agnt/releases/download/v1.2.3/agnt-darwin-amd64"
	if got != want {
		t.Errorf("ReleaseDownloadURL(v-prefixed) = %q, want %q", got, want)
	}
}

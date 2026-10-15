package sshclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFixture is a fixture "remote" whose PATH starts with a directory of
// fake `go` and `curl` scripts, so the go-install and release-download paths
// run for real through `sh -c` without a network or a toolchain.
type installFixture struct {
	home    string
	fakeBin string
	final   string
}

// newInstallFixture writes fake go/curl scripts that install a stand-in agnt
// reporting reportVersion. The fake go also records its arguments.
func newInstallFixture(t *testing.T, reportVersion string) (*installFixture, func() *Client) {
	t.Helper()
	f := &installFixture{home: t.TempDir(), fakeBin: t.TempDir()}
	f.final = filepath.Join(f.home, ".local", "bin", "agnt")
	agntScript := "#!/bin/sh\\necho \"agnt v" + reportVersion + "\"\\n" // inside printf '...'
	goScript := "#!/bin/sh\n" +
		"echo \"$@\" > \"" + filepath.Join(f.fakeBin, "go.args") + "\"\n" +
		"printf '" + agntScript + "' > \"$GOBIN/agnt\" && chmod 755 \"$GOBIN/agnt\"\n"
	curlScript := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then out=$2; fi; shift; done\n" +
		"printf '" + agntScript + "' > \"$out\"\n"
	for name, body := range map[string]string{"go": goScript, "curl": curlScript} {
		if err := os.WriteFile(filepath.Join(f.fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f, func() *Client {
		fixture := newFixtureServer(t)
		fixture.onSession = execFixtureHandler(t, f.home, "HOME="+f.home, "PATH="+f.fakeBin+":"+os.Getenv("PATH"))
		t.Cleanup(fixture.serve(t))
		return dialFixtureClient(t, fixture)
	}
}

func (f *installFixture) decision(source Source) BootstrapDecision {
	return BootstrapDecision{NeedsInstall: true, Source: source, RemoteGOOS: "linux", RemoteGOARCH: "arm64", FinalPath: f.final}
}

// The go-install path installed @latest, whatever version the client was,
// so a remote could end up on a release outside the compatibility window
// (or one newer than any reviewed client). It must pin the client's own
// version, which the Go checksum database then authenticates.
func TestInstallRemoteBinary_GoInstallPinsClientVersion(t *testing.T) {
	f, dial := newInstallFixture(t, "1.4.2")
	client := dial()

	opts := BootstrapOptions{LocalVersion: "1.4.2", LocalBinaryPath: "/unused"}
	if err := InstallRemoteBinary(client.SSH, opts, f.decision(SourceGoInstall)); err != nil {
		t.Fatalf("InstallRemoteBinary: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(f.fakeBin, "go.args"))
	if err != nil {
		t.Fatalf("fake go was not run: %v", err)
	}
	if !strings.Contains(string(args), "github.com/standardbeagle/agnt/cmd/agnt@v1.4.2") {
		t.Fatalf("go install was not pinned to the client version; args = %q", args)
	}
	if _, err := os.Stat(f.final); err != nil {
		t.Fatalf("verified install was not activated at %s: %v", f.final, err)
	}
}

// What was installed is checked, not assumed: a binary reporting a version
// other than the one requested is refused and never activated.
func TestInstallRemoteBinary_WrongInstalledVersionIsNotActivated(t *testing.T) {
	for _, source := range []Source{SourceGoInstall, SourceReleaseDownload} {
		t.Run(source.String(), func(t *testing.T) {
			f, dial := newInstallFixture(t, "1.3.0")
			client := dial()

			opts := BootstrapOptions{LocalVersion: "1.4.2", LocalBinaryPath: "/unused"}
			err := InstallRemoteBinary(client.SSH, opts, f.decision(source))
			if err == nil || !strings.Contains(err.Error(), "reports version 1.3.0, want 1.4.2") {
				t.Fatalf("expected a version-mismatch refusal, got %v", err)
			}
			if _, statErr := os.Stat(f.final); statErr == nil {
				t.Fatalf("a binary of the wrong version was activated at %s", f.final)
			}
			entries, _ := os.ReadDir(filepath.Dir(f.final))
			if len(entries) != 0 {
				t.Fatalf("staging left behind after a refused install: %v", entries)
			}
		})
	}
}

// The release-download path activates a binary whose version matches.
func TestInstallRemoteBinary_ReleaseDownloadActivatesMatchingVersion(t *testing.T) {
	f, dial := newInstallFixture(t, "1.4.2")
	client := dial()

	opts := BootstrapOptions{LocalVersion: "v1.4.2", LocalBinaryPath: "/unused"}
	if err := InstallRemoteBinary(client.SSH, opts, f.decision(SourceReleaseDownload)); err != nil {
		t.Fatalf("InstallRemoteBinary: %v", err)
	}
	if _, err := os.Stat(f.final); err != nil {
		t.Fatalf("download was not activated at %s: %v", f.final, err)
	}
}

// A client version that is not a release (a dev build) cannot be pinned, so
// the go-install path refuses rather than falling back to @latest.
func TestInstallRemoteBinary_GoInstallRefusesUnpinnableVersion(t *testing.T) {
	f, dial := newInstallFixture(t, "1.4.2")
	client := dial()

	opts := BootstrapOptions{LocalVersion: "dev", LocalBinaryPath: "/unused"}
	if err := InstallRemoteBinary(client.SSH, opts, f.decision(SourceGoInstall)); err == nil {
		t.Fatal("go install proceeded for an unpinnable client version")
	}
	if _, err := os.Stat(filepath.Join(f.fakeBin, "go.args")); err == nil {
		t.Fatal("go was run although the version could not be pinned")
	}
}

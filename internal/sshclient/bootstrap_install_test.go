package sshclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
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
	// assetSum is the SHA-256 of the bytes the fake curl writes.
	assetSum string
}

// newInstallFixture writes fake go/curl scripts that install a stand-in agnt
// reporting reportVersion. The fake go also records its arguments.
func newInstallFixture(t *testing.T, reportVersion string) (*installFixture, func() *Client) {
	t.Helper()
	f := &installFixture{home: t.TempDir(), fakeBin: t.TempDir()}
	f.final = filepath.Join(f.home, ".local", "bin", "agnt")
	sum := sha256.Sum256([]byte("#!/bin/sh\necho \"agnt v" + reportVersion + "\"\n"))
	f.assetSum = hex.EncodeToString(sum[:])
	agntScript := "#!/bin/sh\\necho \"agnt v" + reportVersion + "\"\\n" // inside printf '...'
	goScript := "#!/bin/sh\n" +
		"echo \"$@\" > \"" + filepath.Join(f.fakeBin, "go.args") + "\"\n" +
		"printf '" + agntScript + "' > \"$GOBIN/agnt\" && chmod 755 \"$GOBIN/agnt\"\n"
	curlScript := "#!/bin/sh\n" +
		"echo \"$@\" > \"" + filepath.Join(f.fakeBin, "curl.args") + "\"\n" +
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

// opts returns options whose SHA256SUMS fetch serves sums (recording the URL
// it was asked for in *fetched), or fails with fetchErr when it is non-nil.
func (f *installFixture) opts(version, sums string, fetchErr error, fetched *string) BootstrapOptions {
	return BootstrapOptions{LocalVersion: version, LocalBinaryPath: "/unused", FetchChecksums: func(url string) ([]byte, error) {
		if fetched != nil {
			*fetched = url
		}
		if fetchErr != nil {
			return nil, fetchErr
		}
		return []byte(sums), nil
	}}
}

// goodSums is a SHA256SUMS listing the fake asset (agnt-linux-arm64, per
// decision) with its real digest, among other assets.
func (f *installFixture) goodSums() string {
	return strings.Repeat("0", 64) + "  agnt-linux-amd64\n" + f.assetSum + "  agnt-linux-arm64\n"
}

// assertNotInstalled checks a refused install left neither the binary nor
// any staging file behind.
func (f *installFixture) assertNotInstalled(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(f.final); err == nil {
		t.Fatalf("an unverified binary was activated at %s", f.final)
	}
	entries, _ := os.ReadDir(filepath.Dir(f.final))
	if len(entries) != 0 {
		t.Fatalf("staging left behind after a refused install: %v", entries)
	}
}

func (f *installFixture) curlRan() bool {
	_, err := os.Stat(filepath.Join(f.fakeBin, "curl.args"))
	return err == nil
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

			// SHA256SUMS matches the downloaded bytes, so the version check
			// is what refuses them.
			opts := f.opts("1.4.2", f.goodSums(), nil, nil)
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

// The release-download path activates a binary whose SHA-256 matches the
// release's SHA256SUMS entry and whose version matches.
func TestInstallRemoteBinary_ReleaseDownloadActivatesMatchingVersion(t *testing.T) {
	f, dial := newInstallFixture(t, "1.4.2")
	client := dial()

	var fetched string
	opts := f.opts("v1.4.2", f.goodSums(), nil, &fetched)
	if err := InstallRemoteBinary(client.SSH, opts, f.decision(SourceReleaseDownload)); err != nil {
		t.Fatalf("InstallRemoteBinary: %v", err)
	}
	if want := "https://github.com/standardbeagle/agnt/releases/download/v1.4.2/SHA256SUMS"; fetched != want {
		t.Errorf("fetched checksums from %q, want %q", fetched, want)
	}
	if _, err := os.Stat(f.final); err != nil {
		t.Fatalf("download was not activated at %s: %v", f.final, err)
	}
}

// The release download fails closed: a digest mismatch, a SHA256SUMS that
// does not list the asset, and a release with no SHA256SUMS each refuse the
// install, and the last two refuse it before anything is downloaded.
func TestInstallRemoteBinary_ReleaseDownloadFailsClosedOnChecksum(t *testing.T) {
	cases := []struct {
		name       string
		sums       func(f *installFixture) string
		fetchErr   error
		wantErr    string
		downloaded bool
	}{
		{"mismatch", func(f *installFixture) string { return strings.Repeat("ab", 32) + "  agnt-linux-arm64\n" }, nil, "SHA256SUMS lists " + strings.Repeat("ab", 32), true},
		{"missing entry", func(f *installFixture) string { return f.assetSum + "  agnt-linux-amd64\n" }, nil, "SHA256SUMS lists no agnt-linux-arm64", false},
		{"missing SHA256SUMS", func(f *installFixture) string { return "" }, errors.New("HTTP 404 Not Found"), "HTTP 404 Not Found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, dial := newInstallFixture(t, "1.4.2")
			client := dial()

			err := InstallRemoteBinary(client.SSH, f.opts("1.4.2", tc.sums(f), tc.fetchErr, nil), f.decision(SourceReleaseDownload))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected a refusal containing %q, got %v", tc.wantErr, err)
			}
			f.assertNotInstalled(t)
			if f.curlRan() != tc.downloaded {
				t.Errorf("download ran = %v, want %v", f.curlRan(), tc.downloaded)
			}
		})
	}
}

// A remote with neither sha256sum nor shasum cannot verify the download, so
// the install is refused with a message naming both tools.
func TestInstallRemoteBinary_ReleaseDownloadRefusedWithoutRemoteSHA256Tool(t *testing.T) {
	f, _ := newInstallFixture(t, "1.4.2")
	fixture := newFixtureServer(t)
	// A PATH holding only the fake curl and the tools the install needs
	// besides a hasher.
	fixture.onSession = execFixtureHandler(t, f.home, "HOME="+f.home, "PATH="+f.fakeBin)
	for _, tool := range []string{"mkdir", "rm", "chmod", "mv"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("looking up %s: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(f.fakeBin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(fixture.serve(t))
	client := dialFixtureClient(t, fixture)

	err := InstallRemoteBinary(client.SSH, f.opts("1.4.2", f.goodSums(), nil, nil), f.decision(SourceReleaseDownload))
	if err == nil || !strings.Contains(err.Error(), "neither sha256sum nor shasum is installed") {
		t.Fatalf("expected a refusal naming the missing tools, got %v", err)
	}
	f.assertNotInstalled(t)
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

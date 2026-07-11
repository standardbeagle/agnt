package sshclient

import "testing"

func TestSelectSource_SameArchAlwaysPrefersLocalCopy(t *testing.T) {
	cases := []struct {
		name        string
		remoteHasGo bool
	}{
		{"remote has no go", false},
		{"remote has go too", true},
	}
	for _, c := range cases {
		got := selectSource("linux", "amd64", "linux", "amd64", c.remoteHasGo)
		if got != SourceLocalCopy {
			t.Errorf("%s: selectSource = %v, want SourceLocalCopy", c.name, got)
		}
	}
}

// TestSelectSource_CrossArchSkipsLocalCopy pins acceptance criterion 2: a
// local amd64 binary talking to a remote arm64 host must never be offered
// as the source (it wouldn't execute), and instead falls through to
// go-install (if available) or release-download.
func TestSelectSource_CrossArchSkipsLocalCopy(t *testing.T) {
	got := selectSource("linux", "amd64", "linux", "arm64", true)
	if got != SourceGoInstall {
		t.Errorf("cross-arch with remote go: selectSource = %v, want SourceGoInstall", got)
	}

	got = selectSource("linux", "amd64", "linux", "arm64", false)
	if got != SourceReleaseDownload {
		t.Errorf("cross-arch without remote go: selectSource = %v, want SourceReleaseDownload", got)
	}
}

func TestSelectSource_CrossOSAlsoSkipsLocalCopy(t *testing.T) {
	got := selectSource("darwin", "arm64", "linux", "arm64", false)
	if got != SourceReleaseDownload {
		t.Errorf("cross-OS same-arch: selectSource = %v, want SourceReleaseDownload", got)
	}
}

func TestSource_String(t *testing.T) {
	cases := map[Source]string{
		SourceLocalCopy:       "local-copy",
		SourceGoInstall:       "go-install",
		SourceReleaseDownload: "release-download",
		Source(99):            "unknown",
	}
	for src, want := range cases {
		if got := src.String(); got != want {
			t.Errorf("Source(%d).String() = %q, want %q", src, got, want)
		}
	}
}

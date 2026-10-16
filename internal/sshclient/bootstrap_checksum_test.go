package sshclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChecksumFor(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("B", 64)
	sums := []byte(a + "  agnt-linux-amd64\n" + b + " *agnt-linux-arm64\r\n" + "zz  agnt-darwin-arm64\n")

	if got, err := checksumFor(sums, "agnt-linux-amd64"); err != nil || got != a {
		t.Errorf("text-mode entry: got %q, %v", got, err)
	}
	if got, err := checksumFor(sums, "agnt-linux-arm64"); err != nil || got != strings.ToLower(b) {
		t.Errorf("binary-mode entry: got %q, %v", got, err)
	}
	if _, err := checksumFor(sums, "agnt-linux-amd"); err == nil || !strings.Contains(err.Error(), "lists no agnt-linux-amd") {
		t.Errorf("a name that is only a prefix of an entry must not match, got %v", err)
	}
	if _, err := checksumFor(sums, "agnt-darwin-arm64"); err == nil || !strings.Contains(err.Error(), "not a SHA-256 digest") {
		t.Errorf("malformed digest: got %v", err)
	}
	if _, err := checksumFor(nil, "agnt-linux-amd64"); err == nil {
		t.Error("empty SHA256SUMS must not yield a digest")
	}
}

func TestFetchReleaseChecksums(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/SHA256SUMS" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("sums"))
	}))
	defer srv.Close()

	if got, err := fetchReleaseChecksums(srv.URL + "/SHA256SUMS"); err != nil || string(got) != "sums" {
		t.Errorf("fetch: got %q, %v", got, err)
	}
	if _, err := fetchReleaseChecksums(srv.URL + "/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a release without SHA256SUMS must be an error naming the status, got %v", err)
	}
}

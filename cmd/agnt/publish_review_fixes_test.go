package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/standardbeagle/agnt/internal/tunnel"
)

// Regression tests for the publish review findings that live in the serve
// command: the tunnel client-address resolver, the one-serve-per-store lock,
// and the read-only `publish feedback`.

func TestTunnelClientAddr_TrustsTheHeaderOnlyBehindItsOwnTunnel(t *testing.T) {
	req := func(remoteAddr, header string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/s/x", nil)
		r.RemoteAddr = remoteAddr
		if header != "" {
			r.Header.Set("CF-Connecting-IP", header)
		}
		return r
	}

	var up atomic.Bool
	resolve := tunnelClientAddr(tunnel.ProviderCloudflare, &up)
	if resolve == nil {
		t.Fatal("cloudflare must have a resolver")
	}

	// Tunnel not up yet: the header is not trusted, even from loopback.
	if got := resolve(req("127.0.0.1:5000", "203.0.113.9")); got != "" {
		t.Fatalf("before the tunnel is up the resolver returned %q, want \"\" (use the TCP peer)", got)
	}

	up.Store(true)
	// The one trusted shape: loopback peer, tunnel up, a parseable address.
	if got := resolve(req("127.0.0.1:5000", "203.0.113.9")); got != "203.0.113.9" {
		t.Fatalf("loopback peer behind the tunnel: got %q, want 203.0.113.9", got)
	}
	if got := resolve(req("[::1]:5000", " 2001:db8::7 ")); got != "2001:db8::7" {
		t.Fatalf("IPv6 loopback peer: got %q, want 2001:db8::7", got)
	}

	refused := []struct {
		name, remote, header string
	}{
		{"direct LAN client spoofing the header", "192.168.1.50:4000", "203.0.113.9"},
		{"direct public client spoofing the header", "198.51.100.4:4000", "203.0.113.9"},
		{"loopback peer, no header", "127.0.0.1:5000", ""},
		{"loopback peer, header is not an address", "127.0.0.1:5000", "not-an-ip"},
		{"loopback peer, header is a list", "127.0.0.1:5000", "203.0.113.9, 10.0.0.1"},
		{"unparseable peer", "garbage", "203.0.113.9"},
	}
	for _, c := range refused {
		if got := resolve(req(c.remote, c.header)); got != "" {
			t.Errorf("%s: resolver returned %q, want \"\" (fall back to the TCP peer)", c.name, got)
		}
	}

	// Cloudflare's rule reads its own header only: X-Forwarded-For is not it.
	xff := req("127.0.0.1:5000", "")
	xff.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := resolve(xff); got != "" {
		t.Errorf("cloudflare resolver read X-Forwarded-For (%q); it must read CF-Connecting-IP only", got)
	}

	// No tunnel, or a provider with no rule: no resolver at all.
	for _, p := range []tunnel.Provider{"", "localtunnel"} {
		if tunnelClientAddr(p, &up) != nil {
			t.Errorf("provider %q must not have a client-address resolver", p)
		}
	}
}

// TestTunnelClientAddr_ForwardedForProviders covers the two providers whose
// local agent appends X-Forwarded-For. The header is trusted only with a
// loopback peer and this run's tunnel up, and only its RIGHTMOST entry — the one
// the trusted local hop appended — is ever read: everything to its left is
// whatever the visitor chose to send.
func TestTunnelClientAddr_ForwardedForProviders(t *testing.T) {
	req := func(remoteAddr string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/s/x", nil)
		r.RemoteAddr = remoteAddr
		for _, line := range xff {
			r.Header.Add("X-Forwarded-For", line)
		}
		return r
	}

	for _, provider := range []tunnel.Provider{tunnel.ProviderNgrok, tunnel.ProviderTailscale} {
		t.Run(string(provider), func(t *testing.T) {
			var up atomic.Bool
			resolve := tunnelClientAddr(provider, &up)
			if resolve == nil {
				t.Fatalf("%s must have a resolver", provider)
			}

			// Tunnel not up yet: not trusted, even from loopback.
			if got := resolve(req("127.0.0.1:5000", "203.0.113.9")); got != "" {
				t.Fatalf("before the tunnel is up the resolver returned %q, want \"\"", got)
			}
			up.Store(true)

			trusted := []struct {
				name   string
				remote string
				xff    []string
				want   string
			}{
				{"single entry", "127.0.0.1:5000", []string{"203.0.113.9"}, "203.0.113.9"},
				{"IPv6 loopback peer", "[::1]:5000", []string{"203.0.113.9"}, "203.0.113.9"},
				// The visitor pre-seeded the header; the agent appended the truth.
				{"spoofed leftmost entry", "127.0.0.1:5000", []string{"198.51.100.66, 203.0.113.9"}, "203.0.113.9"},
				{"several spoofed entries", "127.0.0.1:5000", []string{"10.0.0.1,127.0.0.1 , 198.51.100.66,  203.0.113.9 "}, "203.0.113.9"},
				// Repeated header lines are one list: the last line is rightmost.
				{"spoofed first header line", "127.0.0.1:5000", []string{"198.51.100.66", "203.0.113.9"}, "203.0.113.9"},
				{"spoofed first line, list on the last", "127.0.0.1:5000", []string{"198.51.100.66", "198.51.100.67, 203.0.113.9"}, "203.0.113.9"},
				{"IPv6 client", "127.0.0.1:5000", []string{"198.51.100.66, 2001:db8::7"}, "2001:db8::7"},
				{"entry with a port", "127.0.0.1:5000", []string{"203.0.113.9:44321"}, "203.0.113.9"},
				{"bracketed IPv6 with a port", "127.0.0.1:5000", []string{"[2001:db8::7]:44321"}, "2001:db8::7"},
			}
			for _, c := range trusted {
				if got := resolve(req(c.remote, c.xff...)); got != c.want {
					t.Errorf("%s: got %q, want %q", c.name, got, c.want)
				}
			}

			// Provenance for the spoof rows: the value the visitor planted is
			// never what comes back, whatever position logic is in play.
			for _, c := range trusted {
				if got := resolve(req(c.remote, c.xff...)); got == "198.51.100.66" || got == "10.0.0.1" {
					t.Errorf("%s: the resolver returned the visitor-supplied entry %q", c.name, got)
				}
			}

			fallback := []struct {
				name   string
				remote string
				xff    []string
			}{
				{"direct LAN client sending the header", "192.168.1.50:4000", []string{"203.0.113.9"}},
				{"direct public client sending the header", "198.51.100.4:4000", []string{"198.51.100.66, 203.0.113.9"}},
				{"loopback peer, no header", "127.0.0.1:5000", nil},
				{"loopback peer, empty header", "127.0.0.1:5000", []string{""}},
				{"rightmost entry is not an address", "127.0.0.1:5000", []string{"203.0.113.9, unknown"}},
				// A malformed rightmost entry must NOT make the resolver step left
				// into entries the visitor controls.
				{"rightmost entry empty", "127.0.0.1:5000", []string{"203.0.113.9,"}},
				{"rightmost entry is a hostname", "127.0.0.1:5000", []string{"203.0.113.9, evil.example"}},
				{"rightmost line malformed, earlier line valid", "127.0.0.1:5000", []string{"203.0.113.9", "garbage"}},
				{"unparseable peer", "garbage", []string{"203.0.113.9"}},
			}
			for _, c := range fallback {
				if got := resolve(req(c.remote, c.xff...)); got != "" {
					t.Errorf("%s: resolver returned %q, want \"\" (fall back to the TCP peer)", c.name, got)
				}
			}

			// These providers' rule reads X-Forwarded-For only: Cloudflare's
			// header, which a visitor can also send, is not a way in.
			cf := req("127.0.0.1:5000")
			cf.Header.Set("CF-Connecting-IP", "203.0.113.9")
			if got := resolve(cf); got != "" {
				t.Errorf("%s resolver read CF-Connecting-IP (%q); it must read X-Forwarded-For only", provider, got)
			}
		})
	}
}

func TestServeStoreLock_OneServePerStore(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")

	release, err := acquireServeStoreLock(storeDir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	// A second serve on the same store is refused by the lock, naming the store.
	_, err = acquireServeStoreLock(storeDir)
	if err == nil {
		t.Fatal("a second serve acquired a store that is already locked")
	}
	if !strings.Contains(err.Error(), "already using the store") || !strings.Contains(err.Error(), storeDir) {
		t.Fatalf("the refusal must name the lock and the store, got: %v", err)
	}
	// Near miss: a different store is independent.
	other, err := acquireServeStoreLock(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatalf("a different store must lock independently: %v", err)
	}
	other()

	// Released (as on exit): the store can be served again.
	release()
	again, err := acquireServeStoreLock(storeDir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}

const reviewFixWalkthroughJSON = `{"version":"v1","id":"wt","title":"Tour","steps":[` +
	`{"id":"s1","title":"One","body":"b","advance":{"type":"auto","ms":1000}}]}`

// isolateUserCache points os.UserCacheDir at a temp dir on every platform, so a
// test that resolves the default serve store never touches the real cache.
func isolateUserCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("LocalAppData", dir)   // windows
	t.Setenv("XDG_CACHE_HOME", dir) // linux
	t.Setenv("HOME", dir)           // darwin ($HOME/Library/Caches)
	return dir
}

func TestPublishFeedback_UnknownFolderCreatesNoStore(t *testing.T) {
	isolateUserCache(t)
	base, err := publish.ServeStoreBase()
	if err != nil {
		t.Fatal(err)
	}
	served := t.TempDir() // a folder nobody ever served

	var out bytes.Buffer
	err = runPublishFeedback(publishFeedbackOptions{Dir: served, Out: &out})
	if err == nil || !strings.Contains(err.Error(), "no serve store") {
		t.Fatalf("reading feedback for a never-served folder must fail naming the missing store, got %v", err)
	}
	if entries, statErr := os.ReadDir(base); statErr == nil && len(entries) != 0 {
		t.Fatalf("a read created a store under %s: %v — `publish list` would now report a phantom serve store", base, entries)
	}
}

func TestPublishFeedback_NeverWritesIntoTheStoreItReads(t *testing.T) {
	storeDir := t.TempDir()
	served := t.TempDir()
	if err := writeServeStoreMeta(storeDir, served, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	shares, err := publish.New(filepath.Join(storeDir, "shares"), nil)
	if err != nil {
		t.Fatal(err)
	}
	pw, err := publish.DecodePublishedWalkthrough([]byte(reviewFixWalkthroughJSON))
	if err != nil {
		t.Fatal(err)
	}
	shareID, _, _, err := shares.PublishFile(pw, served, "a.json")
	if err != nil {
		t.Fatal(err)
	}

	// The owner's ring holds one row that is long past retention and one that
	// is fresh: exactly the state in which the ordinary open rewrites the file.
	limits := publish.FeedbackLimits{RatePerMinute: 10, Burst: 5, MaxBodyBytes: 4096, MaxRowsPerShare: 500, RetentionDays: 90}
	// The owner wrote "ancient" 100 days ago and "recent" 80 days ago. When the
	// second row was written the first was only 20 days old, so it is still in
	// the file; read TODAY it is past the 90-day retention and "recent" is not.
	clock := time.Now().Add(-100 * 24 * time.Hour)
	feedbackDir := filepath.Join(storeDir, "feedback")
	owner, err := publish.NewFeedbackStore(feedbackDir, limits, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Accept(shareID, "rev", "1.2.3.4:1", []byte(`{"message":"ancient"}`)); err != nil {
		t.Fatal(err)
	}
	clock = time.Now().Add(-80 * 24 * time.Hour)
	if err := owner.Accept(shareID, "rev", "1.2.3.4:1", []byte(`{"message":"recent"}`)); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		got := map[string]string{}
		err := filepath.WalkDir(storeDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			got[path] = string(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := snapshot()
	expiredOnDisk := false
	for _, data := range before {
		if strings.Contains(data, "ancient") {
			expiredOnDisk = true
		}
	}
	if !expiredOnDisk {
		t.Fatal("premise broken: the expired row must be on disk before the read, or there is nothing for a reader to prune")
	}

	var out bytes.Buffer
	if err := runPublishFeedback(publishFeedbackOptions{StoreDir: storeDir, Out: &out}); err != nil {
		t.Fatalf("publish feedback: %v", err)
	}
	if !strings.Contains(out.String(), "recent") {
		t.Fatalf("the unexpired row must be printed:\n%s", out.String())
	}
	if strings.Contains(out.String(), "ancient") {
		t.Fatalf("retention must still apply to what is printed:\n%s", out.String())
	}

	after := snapshot()
	if len(after) != len(before) {
		t.Fatalf("a read added or removed files in the store: %d before, %d after", len(before), len(after))
	}
	for path, data := range before {
		if after[path] != data {
			t.Fatalf("a read rewrote %s", path)
		}
	}
}

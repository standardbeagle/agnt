package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/publish"
)

// walkthroughJSON renders a minimal valid published walkthrough. upstream is the
// live origin the demo is proxied against; "" makes a self-contained artifact.
func walkthroughJSON(t *testing.T, id, title, upstream string) []byte {
	t.Helper()
	pw := map[string]any{
		"version": "v1",
		"id":      id,
		"title":   title,
		"steps": []map[string]any{{
			"id":      "s1",
			"title":   "First",
			"body":    "Look here",
			"target":  "#cart",
			"advance": map[string]any{"type": "auto", "ms": 2000},
		}},
	}
	if upstream != "" {
		pw["upstream"] = map[string]any{"url": upstream}
	}
	data, err := json.Marshal(pw)
	if err != nil {
		t.Fatalf("marshal walkthrough: %v", err)
	}
	return data
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// syncBuffer is an io.Writer safe for the serve goroutine to write while the
// test reads.
type syncBuffer struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// serveHarness drives runPublishServe in-process against a temp dir. Every file
// it touches lives under t.TempDir().
type serveHarness struct {
	t       *testing.T
	dir     string
	out     *syncBuffer
	addr    string
	origin  string
	cancel  context.CancelFunc
	done    chan error
	publish chan publishPassResult
}

type publishPassResult struct {
	origin string
	shares []publishedShare
}

func startServe(t *testing.T, dir string) *serveHarness {
	return startServeConfigured(t, dir, nil)
}

// startServeConfigured is startServe with a hook to mutate the options before
// the serve goroutine starts — used to inject the upstream test seam.
func startServeConfigured(t *testing.T, dir string, configure func(*publishServeOptions)) *serveHarness {
	t.Helper()
	// Never t.Parallel(): this binds a real listener and, with a tunnel, would
	// start real OS processes (AGENTS.md).
	ctx, cancel := context.WithCancel(context.Background())
	h := &serveHarness{
		t:       t,
		dir:     dir,
		out:     &syncBuffer{},
		cancel:  cancel,
		done:    make(chan error, 1),
		publish: make(chan publishPassResult, 16),
	}
	listening := make(chan string, 1)
	opts := publishServeOptions{
		Dir:          dir,
		Addr:         "127.0.0.1:0",
		StoreDir:     filepath.Join(t.TempDir(), "store"),
		Out:          h.out,
		PollInterval: 20 * time.Millisecond,
		OnListen:     func(addr string) { listening <- addr },
		OnPublish: func(origin string, shares []publishedShare) {
			cp := append([]publishedShare(nil), shares...)
			select {
			case h.publish <- publishPassResult{origin: origin, shares: cp}:
			default:
			}
		},
	}
	if configure != nil {
		configure(&opts)
	}
	go func() { h.done <- runPublishServe(ctx, opts) }()
	select {
	case h.addr = <-listening:
	case err := <-h.done:
		t.Fatalf("serve exited before listening: %v (output: %s)", err, h.out.String())
	case <-time.After(20 * time.Second):
		t.Fatalf("serve never listened (output: %s)", h.out.String())
	}
	h.origin = "http://" + h.addr
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(20 * time.Second):
			t.Errorf("serve did not shut down")
		}
	})
	return h
}

// nextPass waits for the next completed publish pass.
func (h *serveHarness) nextPass() publishPassResult {
	h.t.Helper()
	select {
	case p := <-h.publish:
		return p
	case err := <-h.done:
		h.t.Fatalf("serve exited: %v (output: %s)", err, h.out.String())
	case <-time.After(20 * time.Second):
		h.t.Fatalf("no publish pass (output: %s)", h.out.String())
	}
	return publishPassResult{}
}

func (h *serveHarness) get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func shareFor(t *testing.T, shares []publishedShare, file string) publishedShare {
	t.Helper()
	for _, s := range shares {
		if s.File == file {
			return s
		}
	}
	t.Fatalf("no share for %s in %+v", file, shares)
	return publishedShare{}
}

// TestPublishServeIntegration is the end-to-end drive of `publish serve`: a temp
// dir of walkthroughs (one naming a fake upstream origin) is loaded, validated,
// published, and reachable at a FULL public URL; an edit keeps the same token and
// URL while serving the new revision; a delete revokes.
func TestPublishServeIntegration(t *testing.T) {
	dir := t.TempDir()
	// A fake upstream: publish-time validation only requires an https origin, and
	// the walkthrough route below serves the artifact without fetching it.
	writeFile(t, dir, "checkout.json", walkthroughJSON(t, "checkout", "Checkout demo", "https://shop.example.test/cart"))
	writeFile(t, dir, "onboarding.json", walkthroughJSON(t, "onboarding", "Onboarding", ""))

	h := startServe(t, dir)
	pass := h.nextPass()

	if len(pass.shares) != 2 {
		t.Fatalf("expected 2 shares, got %+v", pass.shares)
	}
	if pass.origin != h.origin {
		t.Fatalf("origin = %q, want %q", pass.origin, h.origin)
	}
	checkout := shareFor(t, pass.shares, "checkout.json")
	onboarding := shareFor(t, pass.shares, "onboarding.json")
	if checkout.Token == "" || onboarding.Token == "" {
		t.Fatalf("shares must carry a printable token: %+v", pass.shares)
	}
	if checkout.Token == onboarding.Token {
		t.Fatalf("two files shared one token")
	}

	// The FULL public URL — origin included — must be printed, not a bare path.
	want := h.origin + "/s/" + checkout.Token
	if got := checkout.URL(pass.origin); got != want {
		t.Fatalf("share URL = %q, want %q", got, want)
	}
	if out := h.out.String(); !strings.Contains(out, want) {
		t.Fatalf("printed output lacks the full share URL %q:\n%s", want, out)
	}
	if strings.Contains(h.out.String(), "\n  checkout.json  \"Checkout demo\"  /s/") {
		t.Fatalf("printed a bare relative share path")
	}

	// The self-contained share serves its artifact document.
	if code, body := h.get(t, onboarding.URL(h.origin)); code != http.StatusOK {
		t.Fatalf("GET self-contained share = %d, body %q", code, body)
	}
	// Both shares expose their walkthrough JSON under the same token.
	code, body := h.get(t, checkout.URL(h.origin)+"/walkthrough.json")
	if code != http.StatusOK {
		t.Fatalf("GET walkthrough = %d, body %q", code, body)
	}
	if !strings.Contains(body, "Checkout demo") {
		t.Fatalf("walkthrough body missing title: %q", body)
	}

	// An unknown token is 404 — no existence oracle.
	if code, _ := h.get(t, h.origin+"/s/not-a-real-token"); code != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404", code)
	}

	// EDIT: same file, new content. Same token, same URL, new revision.
	writeFile(t, dir, "checkout.json", walkthroughJSON(t, "checkout", "Checkout demo v2", "https://shop.example.test/cart"))
	var edited publishedShare
	deadline := time.Now().Add(20 * time.Second)
	for {
		p := h.nextPass()
		edited = shareFor(t, p.shares, "checkout.json")
		if edited.RevisionID != checkout.RevisionID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("edit never republished (output: %s)", h.out.String())
		}
	}
	if edited.Token != checkout.Token {
		t.Fatalf("edit changed the token: %q -> %q", checkout.Token, edited.Token)
	}
	if edited.ID != checkout.ID {
		t.Fatalf("edit changed the share id: %q -> %q", checkout.ID, edited.ID)
	}
	code, body = h.get(t, checkout.URL(h.origin)+"/walkthrough.json")
	if code != http.StatusOK || !strings.Contains(body, "Checkout demo v2") {
		t.Fatalf("edited revision not served at the original URL: %d %q", code, body)
	}

	// DELETE: the share is revoked and every route under its token dies.
	if err := os.Remove(filepath.Join(dir, "checkout.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		code, _ := h.get(t, checkout.URL(h.origin)+"/walkthrough.json")
		if code == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("deleted file's token still verifies (output: %s)", h.out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The surviving share is untouched by the delete.
	if code, _ := h.get(t, onboarding.URL(h.origin)); code != http.StatusOK {
		t.Fatalf("surviving share broke after a sibling was deleted: %d", code)
	}
}

// TestPublishServeInvalidFileFailsLoudAndNamed pins the stated decision: one
// invalid *.json aborts the whole serve and NAMES the file. It is never silently
// skipped, and no sibling is published behind its back.
func TestPublishServeInvalidFileFailsLoudAndNamed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "good.json", walkthroughJSON(t, "good", "Good", ""))
	writeFile(t, dir, "broken.json", []byte(`{"version":"v1","id":"broken","title":"","steps":[]}`))
	storeDir := filepath.Join(t.TempDir(), "store")

	err := runPublishServe(context.Background(), publishServeOptions{
		Dir:      dir,
		Addr:     "127.0.0.1:0",
		StoreDir: storeDir,
		Out:      io.Discard,
	})
	if err == nil {
		t.Fatalf("invalid walkthrough was accepted")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Fatalf("error does not name the offending file: %v", err)
	}

	// Nothing was published — not even the valid sibling. The abort happens
	// before any share exists, so "reported and excluded" is ruled out too.
	store, serr := publish.New(filepath.Join(storeDir, "shares"), nil)
	if serr != nil {
		t.Fatalf("reopen share store: %v", serr)
	}
	if got := store.List(dir); len(got) != 0 {
		t.Fatalf("shares were published despite the abort: %+v", got)
	}
}

// TestPublishServeUnparseableJSONNamed pins that a syntactically broken file is
// also named, not swallowed as "no walkthroughs here".
func TestPublishServeUnparseableJSONNamed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "truncated.json", []byte(`{"version":"v1","id":"x"`))
	_, err := loadWalkthroughDir(dir)
	if err == nil || !strings.Contains(err.Error(), "truncated.json") {
		t.Fatalf("expected a named decode error, got %v", err)
	}
}

// TestLoadWalkthroughDirUnreadableDirErrors is the mass-revoke guard at its root:
// an unreadable directory must be an ERROR, never an empty file set. An empty set
// handed to Store.ReconcileFiles revokes every share of the project, and
// revocation has no undo.
func TestLoadWalkthroughDirUnreadableDirErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := loadWalkthroughDir(missing); err == nil {
		t.Fatalf("a missing directory must not read as an empty walkthrough set")
	}
	if _, err := dirFingerprint(missing); err == nil {
		t.Fatalf("a missing directory must not fingerprint as empty")
	}
}

// TestPublishPassFailedScanLeavesSharesIntact is the mass-revoke regression. A
// scan that fails AFTER shares exist (here: a file becomes invalid, and then the
// directory itself becomes unreadable) must leave every existing share live.
func TestPublishPassFailedScanLeavesSharesIntact(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.json", walkthroughJSON(t, "a", "A", ""))
	writeFile(t, dir, "b.json", walkthroughJSON(t, "b", "B", ""))

	store, err := publish.New(filepath.Join(t.TempDir(), "shares"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv := &publishServer{dir: dir, store: store, out: io.Discard, tokens: map[string]string{}}
	if _, err := srv.publishPass(); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	live := liveShareIDs(store, dir)
	if len(live) != 2 {
		t.Fatalf("expected 2 live shares, got %+v", live)
	}

	// (1) One file goes invalid mid-session — a partial scan. The pass must fail
	// and revoke nothing, including the sibling that is still fine.
	writeFile(t, dir, "b.json", []byte(`{"version":"v1","id":"b","title":"B","steps":[]}`))
	if _, err := srv.publishPass(); err == nil {
		t.Fatalf("an invalid file must abort the pass")
	}
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("a partial scan revoked shares: %+v (was %+v)", got, live)
	}

	// (2) The directory itself becomes unreadable (the transient-mount case). Same
	// requirement: no revocation.
	gone := filepath.Join(t.TempDir(), "vanished")
	srv.dir = gone
	if _, err := srv.publishPass(); err == nil {
		t.Fatalf("an unreadable directory must abort the pass")
	}
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("an unreadable directory revoked shares: %+v", got)
	}

	// And the tokens still verify — "intact" means serving, not merely recorded.
	srv.mu.Lock()
	tokens := map[string]string{}
	for f, tok := range srv.tokens {
		tokens[f] = tok
	}
	srv.mu.Unlock()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tracked tokens, got %+v", tokens)
	}
	for file, tok := range tokens {
		if _, _, ok := store.VerifyToken(tok); !ok {
			t.Fatalf("token for %s stopped verifying after a failed scan", file)
		}
	}
}

// liveShareIDs lists the non-revoked shares owned by projectPath.
func liveShareIDs(store *publish.Store, projectPath string) []string {
	var out []string
	for _, info := range store.List(projectPath) {
		if !info.Revoked {
			out = append(out, info.ID)
		}
	}
	return out
}

// TestPublishServeTunnelOriginFoldsIntoURLs pins that the tunnel origin replaces
// the local one in every printed share URL. The tunnel process itself is not run
// (that needs cloudflared); the origin callback is exercised directly, which is
// exactly what Tunnel.OnURL delivers.
func TestPublishServeTunnelOriginFoldsIntoURLs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "demo.json", walkthroughJSON(t, "demo", "Demo", ""))

	store, err := publish.New(filepath.Join(t.TempDir(), "shares"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	out := &syncBuffer{}
	srv := &publishServer{dir: dir, store: store, out: out, tokens: map[string]string{}, origin: "http://127.0.0.1:8899"}
	if _, err := srv.publishPass(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	token := srv.shares[0].Token
	if !strings.Contains(out.String(), "http://127.0.0.1:8899/s/"+token) {
		t.Fatalf("boot output lacks the local full URL:\n%s", out.String())
	}

	const tunnelOrigin = "https://calm-fox-1234.trycloudflare.com"
	srv.setOrigin(tunnelOrigin)
	if want := tunnelOrigin + "/s/" + token; !strings.Contains(out.String(), want) {
		t.Fatalf("tunnel-up output lacks %q:\n%s", want, out.String())
	}
}

// TestLocalOriginRendersOpenableURL pins that a wildcard bind is reported as
// loopback: "http://:8899" is not a URL a viewer can open, and this command's
// whole job is emitting an openable URL.
func TestLocalOriginRendersOpenableURL(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8899":   "http://127.0.0.1:8899",
		"127.0.0.1:9000": "http://127.0.0.1:9000",
		"[::]:8899":      "http://127.0.0.1:8899",
	}
	for in, want := range cases {
		if got := localOrigin(in); got != want {
			t.Errorf("localOrigin(%q) = %q, want %q", in, got, want)
		}
	}
	if got := portOf("127.0.0.1:9123"); got != 9123 {
		t.Errorf("portOf = %d, want 9123", got)
	}
}

// TestPublishServeRejectsBadFlags pins loud rejection at the boundary: a missing
// --dir, a --dir that is a file, and an unknown --tunnel provider all fail before
// anything is published.
func TestPublishServeRejectsBadFlags(t *testing.T) {
	dir := t.TempDir()
	file := writeFile(t, dir, "demo.json", walkthroughJSON(t, "demo", "Demo", ""))
	store := filepath.Join(t.TempDir(), "store")

	cases := []struct {
		name string
		opts publishServeOptions
		want string
	}{
		{"missing dir", publishServeOptions{Addr: "127.0.0.1:0", StoreDir: store}, "--dir is required"},
		{"dir is a file", publishServeOptions{Dir: file, Addr: "127.0.0.1:0", StoreDir: store}, "not a directory"},
		{"unknown tunnel", publishServeOptions{Dir: dir, Addr: "127.0.0.1:0", StoreDir: store, Tunnel: "wireguard"}, "unknown tunnel provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.Out = io.Discard
			err := runPublishServe(context.Background(), tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestPublishServeCommandWiring drives the cobra command through the ROOT (a
// child command's own Execute() would silently re-run the root with the root's
// args — see .claude/rules/lessons-ssh-transport.md #4) and asserts the missing
// --dir is rejected rather than defaulted.
func TestPublishServeCommandWiring(t *testing.T) {
	if publishServeCmd.Parent() != publishCmd {
		t.Fatalf("publish serve is not wired under publish")
	}
	if _, _, err := rootCmd.Find([]string{"publish", "serve"}); err != nil {
		t.Fatalf("publish serve is not reachable from the root: %v", err)
	}
	for _, flag := range []string{"dir", "addr", "tunnel", "store", "rotate-on-resume"} {
		if publishServeCmd.Flags().Lookup(flag) == nil {
			t.Fatalf("missing --%s flag", flag)
		}
	}
	if got := publishServeCmd.Flags().Lookup("addr").DefValue; got != defaultPublishServeAddr {
		t.Fatalf("--addr default = %q, want %q", got, defaultPublishServeAddr)
	}
}

// TestDefaultPublishServeAddrIsLoopback pins the shipped-default exposure posture:
// `agnt publish serve` with no --addr must bind loopback, not every interface.
// AGENTS.md § Exposure Posture declares this command's shipped default as loopback,
// widened only by explicit operator action (--tunnel or an explicit wider --addr);
// a bare ":8899" default binds all interfaces, i.e. LAN-reachable by install default,
// which the posture rules forbid. The default is the flag's DefValue, so this also
// pins that a no-flag invocation resolves to a loopback host.
func TestDefaultPublishServeAddrIsLoopback(t *testing.T) {
	host, _, err := net.SplitHostPort(defaultPublishServeAddr)
	if err != nil {
		t.Fatalf("defaultPublishServeAddr %q is not a host:port: %v", defaultPublishServeAddr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("defaultPublishServeAddr host = %q (from %q), want a loopback IP — the shipped default must bind loopback (AGENTS.md § Exposure Posture); a bare :port binds all interfaces",
			host, defaultPublishServeAddr)
	}
	// The flag's default value IS this constant, so a no-flag run resolves here.
	if got := publishServeCmd.Flags().Lookup("addr").DefValue; got != defaultPublishServeAddr {
		t.Fatalf("--addr default = %q, want the loopback default %q", got, defaultPublishServeAddr)
	}
}

// TestPublishServeExplicitWideBindStillWarns pins that moving the DEFAULT to
// loopback does not silence the loud non-loopback advisory: an operator who
// explicitly passes a wide bind (0.0.0.0) still gets the WARNING. Deliberate
// exposure stays possible AND stays announced — the warning keys on the bound
// address, never on the default value.
func TestPublishServeExplicitWideBindStillWarns(t *testing.T) {
	// Never t.Parallel(): binds a real listener (here, briefly, on all interfaces
	// on an ephemeral port) and is torn down immediately.
	dir := t.TempDir()
	writeFile(t, dir, "demo.json", walkthroughJSON(t, "demo", "Demo", ""))
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runPublishServe(ctx, publishServeOptions{
			Dir:          dir,
			Addr:         "0.0.0.0:0",
			StoreDir:     filepath.Join(t.TempDir(), "store"),
			Out:          out,
			PollInterval: time.Second,
			OnListen:     func(addr string) { listening <- addr },
		})
	}()
	select {
	case <-listening:
	case err := <-done:
		t.Fatalf("serve exited before listening: %v (output: %s)", err, out.String())
	case <-time.After(20 * time.Second):
		t.Fatalf("serve never listened (output: %s)", out.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("serve did not shut down")
	}
	got := out.String()
	if !strings.Contains(got, "WARNING") || !strings.Contains(got, "all interfaces") {
		t.Fatalf("an explicit 0.0.0.0 bind did not emit the loud non-loopback warning:\n%s", got)
	}
}

// TestIsDrvFSPathShape pins the /mnt/<drive>/ shape test the poll-period choice
// depends on. It only reports true on WSL, so on a non-WSL host every case is
// false — assert the shape logic through the same predicate the caller uses.
func TestIsDrvFSPathShape(t *testing.T) {
	got := isDrvFSPath("/mnt/c/Users/foo/walkthroughs")
	if want := platform.IsWSL(); got != want {
		t.Fatalf("isDrvFSPath(/mnt/c/...) = %v on a host where IsWSL()=%v", got, want)
	}
	for _, p := range []string{"/home/dev/walkthroughs", "/mnt/", "/mnt/C/Users", "/mnt/cd/foo"} {
		if isDrvFSPath(p) {
			t.Errorf("isDrvFSPath(%q) = true, want false", p)
		}
	}
}

// TestDirFingerprintChangesOnEdit pins the poll-side change detector: the
// fallback that carries correctness on DrvFS mounts must actually notice an
// in-place edit and a deletion.
func TestDirFingerprintChangesOnEdit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.json", []byte(`{"a":1}`))
	first, err := dirFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	// A same-size rewrite: mtime carries the change.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, dir, "a.json", []byte(`{"a":2}`))
	second, err := dirFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if second == first {
		t.Fatalf("fingerprint did not change after an edit: %q", first)
	}
	if err := os.Remove(filepath.Join(dir, "a.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	third, err := dirFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if third == second {
		t.Fatalf("fingerprint did not change after a delete")
	}
	// Non-JSON files are not walkthroughs and must not trigger republishes.
	writeFile(t, dir, "notes.txt", []byte("hello"))
	fourth, err := dirFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if fourth != third {
		t.Fatalf("a non-JSON file changed the fingerprint: %q -> %q", third, fourth)
	}
}

// TestPublishServeResumeMintsFreshTokenLoudly pins the resume path: a store from
// an earlier run holds only sha256(token), so the plaintext is unrecoverable. The
// pass must mint a new one and SAY so, rather than print nothing (or a URL it
// cannot construct).
func TestPublishServeResumeMintsFreshTokenLoudly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "demo.json", walkthroughJSON(t, "demo", "Demo", ""))
	shareDir := filepath.Join(t.TempDir(), "shares")

	store, err := publish.New(shareDir, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	first := &publishServer{dir: dir, store: store, out: io.Discard, tokens: map[string]string{}}
	if _, err := first.publishPass(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstToken := first.shares[0].Token

	// Second run: fresh store handle, fresh (empty) token map — exactly a restart.
	reopened, err := publish.New(shareDir, nil)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	out := &syncBuffer{}
	second := &publishServer{dir: dir, store: reopened, out: out, tokens: map[string]string{}}
	if _, err := second.publishPass(); err != nil {
		t.Fatalf("second run: %v", err)
	}
	newToken := second.shares[0].Token
	if newToken == "" {
		t.Fatalf("resume produced no printable token")
	}
	if newToken == firstToken {
		t.Fatalf("resume claimed to recover the old plaintext token")
	}
	if second.shares[0].ID != first.shares[0].ID {
		t.Fatalf("resume created a different share instead of rotating: %q vs %q",
			second.shares[0].ID, first.shares[0].ID)
	}
	// The notice is a LOUD, distinct block: a count of invalidated shares and
	// explicit wording that the earlier run's viewers now get a 404.
	if !strings.Contains(out.String(), "rotated 1 share(s)") {
		t.Fatalf("resume notice lacks the invalidated-share count:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "404") {
		t.Fatalf("resume notice does not warn viewers now get a 404:\n%s", out.String())
	}
	if _, _, ok := reopened.VerifyToken(newToken); !ok {
		t.Fatalf("freshly minted token does not verify")
	}
	if _, _, ok := reopened.VerifyToken(firstToken); ok {
		t.Fatalf("the previous run's token still verifies after a rotate")
	}
	// Sanity: the printed URL uses the fresh token.
	if !strings.Contains(out.String(), "/s/"+newToken) {
		t.Fatalf("printed URL does not carry the fresh token:\n%s", out.String())
	}
}

// tokenSnapshot maps file -> plaintext token for the shares a pass produced.
func tokenSnapshot(srv *publishServer) map[string]string {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	out := map[string]string{}
	for _, sh := range srv.shares {
		out[sh.File] = sh.Token
	}
	return out
}

// TestPublishPassTwoPollGuardAgainstEmptyRead is the highest-severity guard: an
// N>0 -> 0 transition (the shape a transiently-unmounted /mnt directory produces,
// os.ReadDir succeeding with zero entries) must NOT mass-revoke on a single empty
// read. The first empty read is HELD; only a second consecutive empty read is
// trusted to revoke. A transient empty (files reappear before the confirming
// poll) costs nothing. Provenance-per-§8: assert the tokens still VERIFY, not
// merely that records exist.
func TestPublishPassTwoPollGuardAgainstEmptyRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.json", walkthroughJSON(t, "a", "A", ""))
	writeFile(t, dir, "b.json", walkthroughJSON(t, "b", "B", ""))
	store, err := publish.New(filepath.Join(t.TempDir(), "shares"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv := &publishServer{dir: dir, store: store, out: io.Discard, tokens: map[string]string{}}
	if _, err := srv.publishPass(); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("expected 2 live shares, got %+v", got)
	}
	tokens := tokenSnapshot(srv)
	verifyAll := func(when string) {
		for f, tok := range tokens {
			if _, _, ok := store.VerifyToken(tok); !ok {
				t.Fatalf("token for %s stopped verifying %s", f, when)
			}
		}
	}

	remove := func() {
		if err := os.Remove(filepath.Join(dir, "a.json")); err != nil {
			t.Fatalf("remove a: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, "b.json")); err != nil {
			t.Fatalf("remove b: %v", err)
		}
	}

	// (1) First all-gone read is HELD, revokes nothing, keeps tokens verifying.
	remove()
	held, err := srv.publishPass()
	if err != nil {
		t.Fatalf("held pass errored: %v", err)
	}
	if !held {
		t.Fatalf("the first zero-walkthrough read must be HELD, not acted on")
	}
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("a single empty read revoked shares: %+v", got)
	}
	verifyAll("during the first-empty hold")

	// (2) TRANSIENT: files reappear before the confirming poll. No revoke happened;
	// the same tokens still verify.
	writeFile(t, dir, "a.json", walkthroughJSON(t, "a", "A", ""))
	writeFile(t, dir, "b.json", walkthroughJSON(t, "b", "B", ""))
	held, err = srv.publishPass()
	if err != nil {
		t.Fatalf("repopulate pass errored: %v", err)
	}
	if held {
		t.Fatalf("a repopulated read must not hold")
	}
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("a transient empty read cost a share: %+v", got)
	}
	verifyAll("after a transient empty read")

	// (3) SUSTAINED: empty, held once, then a second consecutive empty read DOES
	// revoke — a genuine delete-everything still converges, one poll later.
	remove()
	held, err = srv.publishPass()
	if err != nil || !held {
		t.Fatalf("re-emptied dir should hold once more: held=%v err=%v", held, err)
	}
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("the held re-empty revoked early: %+v", got)
	}
	held, err = srv.publishPass()
	if err != nil {
		t.Fatalf("confirming pass errored: %v", err)
	}
	if held {
		t.Fatalf("the second consecutive empty read must act, not hold")
	}
	if got := liveShareIDs(store, dir); len(got) != 0 {
		t.Fatalf("a sustained empty directory did not converge to revoked: %+v", got)
	}
}

// TestPublishServeWatchFastPathDoesNotStrandEmptyGuard drives the watch loop's
// change-detection cycle across a /mnt flap: drop → heal-to-IDENTICAL-fingerprint
// → drop. A real /mnt remount leaves the files' name|size|mtime untouched, so the
// healed read produces a fingerprint identical to the pre-drop one and takes the
// `fp == last` fast path that bypasses publishPass. If that fast path does not
// reset the two-poll empty guard, the guard state left by the FIRST drop is stale
// on the SECOND drop, and the second drop's FIRST empty read mass-revokes every
// live share — the exact irreversible loss the guard exists to prevent, inside its
// documented threat model. Provenance-per-§8: assert the tokens still VERIFY, not
// merely that records exist.
func TestPublishServeWatchFastPathDoesNotStrandEmptyGuard(t *testing.T) {
	dir := t.TempDir()
	stash := t.TempDir()
	writeFile(t, dir, "a.json", walkthroughJSON(t, "a", "A", ""))
	writeFile(t, dir, "b.json", walkthroughJSON(t, "b", "B", ""))
	store, err := publish.New(filepath.Join(t.TempDir(), "shares"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv := &publishServer{dir: dir, store: store, out: io.Discard, tokens: map[string]string{}}

	// Establish the two live shares, then take the fingerprint the watch loop
	// would carry as `last` for a settled non-empty directory.
	if _, err := srv.publishPass(); err != nil {
		t.Fatalf("initial pass: %v", err)
	}
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("expected 2 live shares, got %+v", got)
	}
	tokens := tokenSnapshot(srv)
	verifyAll := func(when string) {
		t.Helper()
		for f, tok := range tokens {
			if _, _, ok := store.VerifyToken(tok); !ok {
				t.Fatalf("token for %s stopped verifying %s", f, when)
			}
		}
	}

	// os.Rename preserves inode/size/mtime, so moving the files out (drop) and
	// back (heal) reproduces an IDENTICAL fingerprint — the /mnt remount shape.
	drop := func() {
		for _, f := range []string{"a.json", "b.json"} {
			if err := os.Rename(filepath.Join(dir, f), filepath.Join(stash, f)); err != nil {
				t.Fatalf("drop %s: %v", f, err)
			}
		}
	}
	heal := func() {
		for _, f := range []string{"a.json", "b.json"} {
			if err := os.Rename(filepath.Join(stash, f), filepath.Join(dir, f)); err != nil {
				t.Fatalf("heal %s: %v", f, err)
			}
		}
	}

	last, err := dirFingerprint(dir)
	if err != nil {
		t.Fatalf("initial fingerprint: %v", err)
	}

	// (1) First drop: an empty read, HELD once. `last` is not advanced.
	drop()
	last = srv.recheckOnce(last)
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("first drop's held read revoked shares: %+v", got)
	}
	verifyAll("during the first drop's hold")

	// (2) Heal to an identical fingerprint: the fast path (fp == last) bypasses
	// publishPass. It MUST reset the empty guard, because a non-empty directory
	// has now been observed.
	heal()
	healedFP, err := dirFingerprint(dir)
	if err != nil {
		t.Fatalf("healed fingerprint: %v", err)
	}
	if healedFP != last {
		t.Fatalf("test premise broken: heal did not reproduce an identical fingerprint (%q vs %q); the fast path is not exercised", healedFP, last)
	}
	last = srv.recheckOnce(last)
	verifyAll("after the heal-to-identical-fingerprint fast path")

	// (3) Second drop: because the heal reset the guard, this first empty read is
	// HELD again — it must NOT mass-revoke. This is the assertion that fails on
	// the pre-fix code, where the stranded guard state revokes here.
	drop()
	last = srv.recheckOnce(last)
	if got := liveShareIDs(store, dir); len(got) != 2 {
		t.Fatalf("second drop's FIRST empty poll mass-revoked live shares (stale two-poll guard): %+v", got)
	}
	verifyAll("after the second drop's first poll")
	_ = last
}

// TestPublishServeEmptyDirAtStartupRefuses pins the startup decision: a directory
// that reads as empty at boot refuses to serve rather than publishing nothing —
// AND, critically, does not mass-revoke a pre-existing store's shares on the way
// out (the boot form of the unmounted-directory trap).
func TestPublishServeEmptyDirAtStartupRefuses(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(t.TempDir(), "store")

	// Seed the store as if an earlier run had published old.json in this folder.
	seed, err := publish.New(filepath.Join(storeDir, "shares"), nil)
	if err != nil {
		t.Fatalf("seed store: %v", err)
	}
	pw, err := publish.DecodePublishedWalkthrough(walkthroughJSON(t, "old", "Old", ""))
	if err != nil {
		t.Fatalf("decode seed walkthrough: %v", err)
	}
	if _, _, _, err := seed.PublishFile(pw, dir, "old.json"); err != nil {
		t.Fatalf("seed publish: %v", err)
	}

	err = runPublishServe(context.Background(), publishServeOptions{
		Dir: dir, Addr: "127.0.0.1:0", StoreDir: storeDir, Out: io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "no *.json") {
		t.Fatalf("an empty --dir at startup should refuse to serve, got %v", err)
	}

	reopened, err := publish.New(filepath.Join(storeDir, "shares"), nil)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if got := liveShareIDs(reopened, dir); len(got) != 1 {
		t.Fatalf("startup on an empty dir revoked the pre-existing share: %+v", got)
	}
}

// TestPublishServeResumeRotatePolicies pins --rotate-on-resume=yes|no|fail. yes
// (the default) is covered by TestPublishServeResumeMintsFreshTokenLoudly; here
// fail refuses to serve without invalidating anything, no keeps the earlier
// run's links working while declining to reprint them, and an unknown value is
// rejected at the boundary.
func TestPublishServeResumeRotatePolicies(t *testing.T) {
	// setup publishes one file through a first run, then returns the folder, the
	// share dir, and the first run's (now unrecoverable-by-design) token.
	setup := func(t *testing.T) (dir, shareDir, firstToken string) {
		dir = t.TempDir()
		writeFile(t, dir, "demo.json", walkthroughJSON(t, "demo", "Demo", ""))
		shareDir = filepath.Join(t.TempDir(), "shares")
		store, err := publish.New(shareDir, nil)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		first := &publishServer{dir: dir, store: store, out: io.Discard, tokens: map[string]string{}}
		if _, err := first.publishPass(); err != nil {
			t.Fatalf("first run: %v", err)
		}
		return dir, shareDir, first.shares[0].Token
	}

	t.Run("fail refuses without invalidating", func(t *testing.T) {
		dir, shareDir, firstToken := setup(t)
		store, err := publish.New(shareDir, nil)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		srv := &publishServer{dir: dir, store: store, out: io.Discard, tokens: map[string]string{}, rotateOnResume: rotateResumeFail}
		if _, err := srv.publishPass(); err == nil || !strings.Contains(err.Error(), "rotate-on-resume=fail") {
			t.Fatalf("fail policy should refuse the resume, got %v", err)
		}
		// fail is non-destructive: the earlier run's token is untouched.
		if _, _, ok := store.VerifyToken(firstToken); !ok {
			t.Fatalf("fail policy invalidated the earlier run's token")
		}
	})

	t.Run("no keeps old links working, does not reprint or repeat", func(t *testing.T) {
		dir, shareDir, firstToken := setup(t)
		store, err := publish.New(shareDir, nil)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		out := &syncBuffer{}
		srv := &publishServer{dir: dir, store: store, out: out, tokens: map[string]string{}, rotateOnResume: rotateResumeNo}
		if _, err := srv.publishPass(); err != nil {
			t.Fatalf("no pass: %v", err)
		}
		// The earlier run's link still WORKS — that is the whole point of "no".
		if _, _, ok := store.VerifyToken(firstToken); !ok {
			t.Fatalf("no policy broke the earlier run's still-valid token")
		}
		if srv.shares[0].Token != "" {
			t.Fatalf("no policy should not mint a printable token, got %q", srv.shares[0].Token)
		}
		if !strings.Contains(out.String(), "WITHOUT rotating") {
			t.Fatalf("no policy notice missing:\n%s", out.String())
		}
		// The notice does not repeat on an unchanged republish.
		before := out.String()
		if _, err := srv.publishPass(); err != nil {
			t.Fatalf("second no pass: %v", err)
		}
		if added := strings.TrimPrefix(out.String(), before); strings.Contains(added, "WITHOUT rotating") {
			t.Fatalf("no policy notice repeated on an unchanged republish:\n%s", added)
		}
	})

	t.Run("unknown value rejected at the boundary", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "demo.json", walkthroughJSON(t, "demo", "Demo", ""))
		err := runPublishServe(context.Background(), publishServeOptions{
			Dir: dir, Addr: "127.0.0.1:0", StoreDir: filepath.Join(t.TempDir(), "store"),
			RotateOnResume: "maybe", Out: io.Discard,
		})
		if err == nil || !strings.Contains(err.Error(), "rotate-on-resume") {
			t.Fatalf("an unknown --rotate-on-resume must be rejected, got %v", err)
		}
	})
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs %s: %v", p, err)
	}
	return a
}

// TestPublishFeedbackReadsServeStore pins Part 3's read-side integration: a serve
// run's own store is discoverable and its viewer feedback is readable through the
// documented `agnt publish feedback` path — via --store (metadata maps it back to
// the served folder) and via --dir (the default store location is derived).
func TestPublishFeedbackReadsServeStore(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(t.TempDir(), "store")

	// Publish one share into the serve store + drop the discovery metadata, exactly
	// as a serve run does.
	store, err := publish.New(filepath.Join(storeDir, "shares"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	pw, err := publish.DecodePublishedWalkthrough(walkthroughJSON(t, "demo", "Demo", ""))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	id, _, _, err := store.PublishFile(pw, dir, "demo.json")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := writeServeStoreMeta(storeDir, dir, "127.0.0.1:8899"); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	// Seed a viewer feedback row.
	limits := publish.FeedbackLimits{RatePerMinute: 60, Burst: 10, MaxBodyBytes: 4096, MaxRowsPerShare: 100, RetentionDays: 30}
	fb, err := publish.NewFeedbackStore(filepath.Join(storeDir, "feedback"), limits, nil)
	if err != nil {
		t.Fatalf("open feedback store: %v", err)
	}
	if err := fb.Accept(id, "", "1.2.3.4:5", []byte(`{"message":"great demo"}`)); err != nil {
		t.Fatalf("accept feedback: %v", err)
	}

	// Read it back via --store (metadata maps the store to its served folder).
	out := &syncBuffer{}
	if err := runPublishFeedback(publishFeedbackOptions{StoreDir: storeDir, Out: out}); err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	if !strings.Contains(out.String(), "great demo") {
		t.Fatalf("feedback row not surfaced:\n%s", out.String())
	}
	if !strings.Contains(out.String(), id) {
		t.Fatalf("share id not surfaced:\n%s", out.String())
	}

	// --dir resolves the DEFAULT store location (pure derivation, no fs write).
	gotStore, gotDir, err := resolveServeStore("", dir)
	if err != nil {
		t.Fatalf("resolve by dir: %v", err)
	}
	wantStore, err := defaultPublishServeStoreDir(mustAbs(t, dir))
	if err != nil {
		t.Fatalf("want store: %v", err)
	}
	if gotStore != wantStore || gotDir != mustAbs(t, dir) {
		t.Fatalf("--dir resolution wrong: store=%q dir=%q", gotStore, gotDir)
	}

	// The boot path prints both store locations so they are discoverable without
	// reading source.
	boot := &syncBuffer{}
	_ = runPublishServe(context.Background(), publishServeOptions{
		Dir: t.TempDir(), Addr: "127.0.0.1:0", StoreDir: filepath.Join(t.TempDir(), "empty-store"), Out: boot,
	})
	// (empty dir refuses to serve, but the store-path lines print before that)
	if !strings.Contains(boot.String(), "share store ") || !strings.Contains(boot.String(), "feedback store ") {
		t.Fatalf("boot output does not print store paths:\n%s", boot.String())
	}
}

// dialRecorder captures the addresses the guarded fetcher's dialer was handed.
// The serve goroutine writes it while the test reads it, so it is mutex-guarded
// (unlike the in-package proxy testUpstream, whose handler runs synchronously).
type dialRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (d *dialRecorder) record(addr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, addr)
}

func (d *dialRecorder) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.seen...)
}

// servePublicAddr is a genuinely PUBLIC address the seam resolver answers with,
// so the SSRF deny-list runs its real logic and PASSES on its own merits rather
// than being bypassed. A local listener is on loopback, which the deny-list
// refuses by design, so this indirection is the only way to drive the guarded
// document route end to end without weakening the guard.
var servePublicAddr = netip.MustParseAddr("93.184.216.34")

// TestPublishServeProxiesUpstreamDocumentThroughGuard is the serve-level cover
// of the PROXIED document route that S9 could not reach: it drives runPublishServe
// against a fake upstream through the newly-exposed UpstreamSeam and asserts the
// guard OBSERVED it — the dialer received exactly the guard-validated public
// address (publish-security-review-lessons §7/§8). Mutation check: delete the
// pin in guardedUpstreamFetcher.get and the transport re-resolves example.com to
// a DIFFERENT address, the dialer's assertion rejects it, the fetch 502s, and the
// 200/marker/dialed-address assertions below all fail — the refusal is
// attributable to the pin, not to an incidental error.
func TestPublishServeProxiesUpstreamDocumentThroughGuard(t *testing.T) {
	const upstreamMarker = `id="live-hero"`
	upstreamDoc := `<!DOCTYPE html><html><head><title>Live App</title></head>` +
		`<body><h1 ` + upstreamMarker + `>Real upstream page</h1></body></html>`

	upSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A hostile upstream also seeds headers the public plane must NOT propagate.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "script-src 'unsafe-inline' *")
		w.Header().Set("Set-Cookie", "upstream=1")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		io.WriteString(w, upstreamDoc)
	}))
	t.Cleanup(upSrv.Close)

	dialed := &dialRecorder{}
	seam := &proxy.UpstreamSeam{
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "example.com" {
				return nil, errors.New("unexpected resolve of " + host)
			}
			return []netip.Addr{servePublicAddr}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed.record(addr)
			// The pin under test: the dialer must be handed the guard-validated
			// PUBLIC address, never a re-resolution of the hostname.
			if addr != servePublicAddr.String()+":443" {
				return nil, errors.New("dial address is not the guard-validated one: " + addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
		},
		// example.com is the name httptest's cert is issued for, so TLS verifies
		// for real rather than being skipped.
		TLSConfig: upSrv.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}

	dir := t.TempDir()
	writeFile(t, dir, "proxied.json", walkthroughJSON(t, "proxied", "Proxied demo", "https://example.com/app"))

	h := startServeConfigured(t, dir, func(o *publishServeOptions) { o.UpstreamSeam = seam })
	pass := h.nextPass()
	share := shareFor(t, pass.shares, "proxied.json")

	// The DOCUMENT route (not walkthrough.json): fetches the upstream through the
	// guarded seam and serves it with the RolePublic bundle injected. A raw GET so
	// the response HEADERS are inspectable for the wholesale-policy assertion.
	resp, err := http.Get(share.URL(h.origin))
	if err != nil {
		t.Fatalf("GET proxied document route: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read proxied document body: %v", err)
	}
	body := string(raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxied document route = %d, body %q", resp.StatusCode, body)
	}
	if !strings.Contains(body, upstreamMarker) {
		t.Fatalf("served body is not the upstream document (marker %q missing):\n%q", upstreamMarker, body)
	}

	// Provenance (§8): the guard's pin actually ran — the dialer got exactly the
	// validated public address, once. A deleted pin dials a different address and
	// this assertion (and the 200 above) fail.
	if got := dialed.snapshot(); len(got) != 1 || got[0] != servePublicAddr.String()+":443" {
		t.Fatalf("dialer received %v, want exactly [%s:443] — the guard pin was bypassed", got, servePublicAddr)
	}

	// Wholesale header policy (INV-11/INV-12): a hostile upstream's CSP, cookies,
	// and CORS grants must never reach the viewer.
	if csp := resp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("hostile upstream CSP survived onto the served response: %q", csp)
	}
	if sc := resp.Header.Get("Set-Cookie"); sc != "" {
		t.Fatalf("upstream Set-Cookie leaked to the viewer: %q", sc)
	}
	if ao := resp.Header.Get("Access-Control-Allow-Origin"); ao != "" {
		t.Fatalf("upstream CORS grant leaked to the viewer: %q", ao)
	}
}

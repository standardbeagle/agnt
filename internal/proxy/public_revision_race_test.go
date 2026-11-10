package proxy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/publish"
)

// digestingVerifier is fakeVerifier plus the store's persisted digest, which
// the test moves to stand in for a publisher edit.
type digestingVerifier struct {
	fakeVerifier
	current string
}

func (v *digestingVerifier) ShareDigest(string) (string, bool) { return v.current, true }

// editingFetcher re-publishes the share while its document fetch is in flight.
type editingFetcher struct {
	countingFetcher
	onFetch func()
}

func (f *editingFetcher) fetchDocument(ctx context.Context, rawURL string) ([]byte, string, error) {
	f.onFetch()
	return f.countingFetcher.fetchDocument(ctx, rawURL)
}

// TestProxiedArtifact_EditDuringFetchSignsNothing pins the revision boundary
// (§16). The document is fetched from the upstream of the revision the token
// resolved to (A). The references were signed with the store's CURRENT digest,
// read after the fetch, so an edit to revision B landing mid-fetch gave A's
// references B's signature, and they kept verifying after the edit. A
// reference is now bound to the digest of the revision that was fetched, and
// only while that is still current; otherwise nothing is signed.
func TestProxiedArtifact_EditDuringFetchSignsNothing(t *testing.T) {
	rev := upstreamRevision("https://demo.example.com/app")
	revA := string(revisionDigest(rev))
	if revA == "" {
		t.Fatal("test revision has no digest")
	}
	v := &digestingVerifier{fakeVerifier: fakeVerifier{token: validToken, rev: rev, id: subShareID}, current: revA}
	h := NewPublicHandler(v, nil, 0)
	if h.signer == nil {
		t.Fatal("handler has no subresource signer")
	}
	doc := `<!DOCTYPE html><html><head><link rel="stylesheet" href="theme.css"></head><body>x</body></html>`
	f := &editingFetcher{countingFetcher: countingFetcher{body: []byte(doc)}}
	h.upstream = f

	// Control: with no edit, the reference is rewritten to a signed /sub URL.
	f.onFetch = func() {}
	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), subPrefixPath+"?") {
		t.Fatalf("control: got %d, signed reference present=%v", w.Code, strings.Contains(w.Body.String(), subPrefixPath+"?"))
	}

	f.onFetch = func() { v.current = "digest-of-revision-B" }
	w = do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if strings.Contains(w.Body.String(), subPrefixPath+"?") {
		t.Fatal("revision A's document was signed under revision B")
	}
}

// TestRevisionOf_StaleRevisionNamesNone: a revision the token resolved to that
// is no longer the share's current one names no revision, so neither the
// document route nor the subresource route signs or verifies under it.
func TestRevisionOf_StaleRevisionNamesNone(t *testing.T) {
	rev := upstreamRevision("https://demo.example.com/app")
	v := &digestingVerifier{fakeVerifier: fakeVerifier{token: validToken, rev: rev, id: subShareID}, current: "other"}
	h := NewPublicHandler(v, nil, 0)
	if got := h.revisionOf(rev, subShareID); got != "" {
		t.Fatalf("stale revision named %q", got)
	}
	v.current = string(revisionDigest(rev))
	if got := h.revisionOf(rev, subShareID); got != v.current {
		t.Fatalf("current revision named %q, want %q", got, v.current)
	}
}

// TestRevisionOf_RealStoreNamesItsRevision: the real store's persisted digest
// must equal the digest of the walkthrough it resolves a token to, before and
// after a reopen from disk, or revisionOf would name no revision and every
// subresource reference would stop verifying.
func TestRevisionOf_RealStoreNamesItsRevision(t *testing.T) {
	dir := t.TempDir()
	store, err := publish.New(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.Create(upstreamRevision("https://demo.example.com/app"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*publish.Store{store, reopenStore(t, dir)} {
		h := NewPublicHandler(s, nil, 0)
		rev, shareID, ok := s.VerifyToken(token)
		if !ok {
			t.Fatal("token does not verify")
		}
		if h.revisionOf(rev, shareID) == "" {
			t.Fatal("the store's own current revision names no revision")
		}
	}
}

func reopenStore(t *testing.T, dir string) *publish.Store {
	t.Helper()
	s, err := publish.New(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

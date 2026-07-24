package publish

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

)

// fixedClock returns a deterministic, injected clock (no wall-clock sleeps).
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// validWalkthrough builds a minimal valid published walkthrough for tests.
func validWalkthrough(id string) *PublishedWalkthrough {
	return &PublishedWalkthrough{
		Version: SchemaV1,
		ID:      id,
		Title:   "Test Walkthrough",
		Steps: []Step{
			{ID: "s1", Title: "Step one", Body: "hello", Advance: Advance{Type: "auto", MS: 1000}},
		},
	}
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir, fixedClock(time.Unix(1_700_000_000, 0).UTC()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, dir
}

// TestTokenProperties pins spec §3: 256-bit CSPRNG, base64url ~43 chars, unique.
func TestTokenProperties(t *testing.T) {
	s, _ := newStore(t)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		_, token, err := s.Create(validWalkthrough("wt"), "/proj")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if seen[token] {
			t.Fatalf("duplicate token minted: %q", token)
		}
		seen[token] = true

		if l := len(token); l < 42 || l > 44 {
			t.Fatalf("token length %d, want ~43 (256-bit base64url)", l)
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("token not base64url: %v", err)
		}
		if len(raw) != 32 {
			t.Fatalf("token entropy %d bytes, want 32 (256 bits)", len(raw))
		}
	}
}

// TestPlaintextNeverPersisted pins spec §3 / INV-3: only sha256(token) at rest;
// the on-disk record must contain the hash and never the plaintext token.
func TestPlaintextNeverPersisted(t *testing.T) {
	s, dir := newStore(t)
	id, token, err := s.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if strings.Contains(string(data), token) {
		t.Fatalf("plaintext token found in on-disk record")
	}
	if !strings.Contains(string(data), hashToken(token)) {
		t.Fatalf("token hash not found in on-disk record")
	}
}

// TestVerifyConstantTime pins INV-3: right token passes, wrong fails, via the
// constant-time path. (A source-level assertion that ConstantTimeCompare is used
// lives in TestVerifyUsesConstantTimeCompare.)
func TestVerifyConstantTime(t *testing.T) {
	s, _ := newStore(t)
	id, token, err := s.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rev, gotID, ok := s.VerifyToken(token)
	if !ok {
		t.Fatalf("valid token failed to verify")
	}
	if gotID != id {
		t.Fatalf("verify returned share id %q, want %q", gotID, id)
	}
	if rev == nil || rev.ID != "wt" {
		t.Fatalf("verify returned wrong revision: %+v", rev)
	}
	if _, _, ok := s.VerifyToken(token + "x"); ok {
		t.Fatalf("wrong token verified")
	}
	if _, _, ok := s.VerifyToken(""); ok {
		t.Fatalf("empty token verified")
	}
}

// TestRevokeImmediate pins INV-4: revoke kills the token at once.
func TestRevokeImmediate(t *testing.T) {
	s, _ := newStore(t)
	id, token, err := s.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, ok := s.VerifyToken(token); !ok {
		t.Fatalf("token should verify before revoke")
	}
	if err := s.Revoke(id); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, _, ok := s.VerifyToken(token); ok {
		t.Fatalf("token still verifies after revoke")
	}
	// Idempotent.
	if err := s.Revoke(id); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	// Status still reports it (audit trail), marked revoked.
	info, err := s.Status(id)
	if err != nil {
		t.Fatalf("Status after revoke: %v", err)
	}
	if !info.Revoked {
		t.Fatalf("Status should report revoked")
	}
}

// TestRotate pins spec §3: old token dies immediately, new token verifies, new
// token returned once and differs.
func TestRotate(t *testing.T) {
	s, _ := newStore(t)
	id, oldToken, err := s.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	newToken, err := s.Rotate(id)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if newToken == oldToken {
		t.Fatalf("rotate returned same token")
	}
	if _, _, ok := s.VerifyToken(oldToken); ok {
		t.Fatalf("old token still verifies after rotate")
	}
	if _, _, ok := s.VerifyToken(newToken); !ok {
		t.Fatalf("new token does not verify after rotate")
	}
	// Rotate on revoked share is refused.
	if err := s.Revoke(id); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := s.Rotate(id); err != ErrRevoked {
		t.Fatalf("Rotate on revoked = %v, want ErrRevoked", err)
	}
}

// TestReloadSurvivesRestart pins INV-8: shares written by one store still verify
// when a fresh store is constructed from the same dir (daemon restart).
func TestReloadSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clk := fixedClock(time.Unix(1_700_000_000, 0).UTC())
	s1, err := New(dir, clk)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idA, tokA, _ := s1.Create(validWalkthrough("a"), "/proj")
	idB, tokB, _ := s1.Create(validWalkthrough("b"), "/proj")
	// Rotate B and revoke it to exercise reload of both live-token and tombstone.
	_, _ = s1.Rotate(idB)
	tokBrot, _ := s1.Rotate(idB)

	s2, err := New(dir, clk)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	if _, gotID, ok := s2.VerifyToken(tokA); !ok || gotID != idA {
		t.Fatalf("share A did not survive restart")
	}
	if _, _, ok := s2.VerifyToken(tokB); ok {
		t.Fatalf("stale rotated token for B verified after restart")
	}
	if _, _, ok := s2.VerifyToken(tokBrot); !ok {
		t.Fatalf("current token for B did not survive restart")
	}
	_ = idB
}

// TestFilePerms pins spec §8: record files are 0600.
func TestFilePerms(t *testing.T) {
	s, dir := newStore(t)
	id, _, err := s.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("record perms %o, want 0600", perm)
	}
}

// TestCorruptFailsLoud pins spec §8 / INV-8: a corrupt/truncated record makes
// load FAIL LOUD (error), never a silent empty store.
func TestCorruptFailsLoud(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(path string)
	}{
		{"truncated", func(p string) {
			data, _ := os.ReadFile(p)
			_ = os.WriteFile(p, data[:len(data)/2], 0600)
		}},
		{"garbage", func(p string) {
			_ = os.WriteFile(p, []byte("}{ not json"), 0600)
		}},
		{"checksum-tamper", func(p string) {
			data, _ := os.ReadFile(p)
			// Flip a byte in the title so the record still parses but the
			// integrity checksum no longer matches.
			s := strings.Replace(string(data), "Test Walkthrough", "Tampered Walkthru", 1)
			_ = os.WriteFile(p, []byte(s), 0600)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(dir, fixedClock(time.Unix(1, 0)))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			id, _, err := s.Create(validWalkthrough("wt"), "/proj")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			tc.corrupt(filepath.Join(dir, id+".json"))

			_, err = New(dir, fixedClock(time.Unix(1, 0)))
			if err == nil {
				t.Fatalf("corrupt store loaded without error (silent empty-store fallback)")
			}
		})
	}
}

// TestInvalidArtifactRejected pins that Create validates via the P2 validators.
func TestInvalidArtifactRejected(t *testing.T) {
	s, dir := newStore(t)
	bad := validWalkthrough("wt")
	bad.Steps = nil // walkthrough with zero steps is invalid per publish.Validate
	if _, _, err := s.Create(bad, "/proj"); err == nil {
		t.Fatalf("Create accepted an invalid artifact")
	}
	// Nothing should have been persisted.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("invalid create persisted a record: %s", e.Name())
		}
	}
}

// TestRedaction pins INV-9: HashPrefix / ScrubSharePath expose only hash[:8],
// never the plaintext token. Status likewise returns no token.
func TestRedaction(t *testing.T) {
	s, _ := newStore(t)
	id, token, err := s.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	prefix := HashPrefix(token)
	if len(prefix) != 8 {
		t.Fatalf("HashPrefix len %d, want 8", len(prefix))
	}
	if strings.Contains(prefix, token) {
		t.Fatalf("hash prefix leaks token")
	}

	scrubbed := ScrubSharePath("/s/" + token + "/feedback")
	if strings.Contains(scrubbed, token) {
		t.Fatalf("scrubbed path still contains token: %q", scrubbed)
	}
	if scrubbed != "/s/"+prefix+"/feedback" {
		t.Fatalf("scrubbed = %q, want /s/%s/feedback", scrubbed, prefix)
	}
	// Non-share paths pass through untouched.
	if got := ScrubSharePath("/api/thing"); got != "/api/thing" {
		t.Fatalf("non-share path altered: %q", got)
	}

	info, err := s.Status(id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if info.TokenHashPrefix != prefix {
		t.Fatalf("Status prefix %q, want %q", info.TokenHashPrefix, prefix)
	}
}

// TestPublicVerifyIgnoresProjectScope pins INV-1 / Deviations #3 at the store
// layer: the public verify path takes ONLY a token and returns the revision
// regardless of any project — it carries no SessionCode/Directory and cannot be
// used as a project-scoped lookup. List (the control plane) is project-scoped;
// VerifyToken (the public plane) is not.
func TestPublicVerifyIgnoresProjectScope(t *testing.T) {
	s, _ := newStore(t)
	_, token, err := s.Create(validWalkthrough("wt"), "/project-a")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Public verify succeeds with the token alone — no project supplied, and it
	// does not matter which project owns the share.
	if _, _, ok := s.VerifyToken(token); !ok {
		t.Fatalf("public verify must succeed on token alone")
	}
	// Control-plane List is project-scoped: the share appears only under its
	// owning project, never under another.
	if got := s.List("/project-a"); len(got) != 1 {
		t.Fatalf("List(/project-a) = %d shares, want 1", len(got))
	}
	if got := s.List("/project-b"); len(got) != 0 {
		t.Fatalf("List(/project-b) = %d shares, want 0 (cross-project leak)", len(got))
	}
}

// TestUnknownIDErrors pins the not-found paths.
func TestUnknownIDErrors(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Status("nope"); err != ErrNotFound {
		t.Fatalf("Status(nope) = %v, want ErrNotFound", err)
	}
	if err := s.Revoke("nope"); err != ErrNotFound {
		t.Fatalf("Revoke(nope) = %v, want ErrNotFound", err)
	}
	if _, err := s.Rotate("nope"); err != ErrNotFound {
		t.Fatalf("Rotate(nope) = %v, want ErrNotFound", err)
	}
}

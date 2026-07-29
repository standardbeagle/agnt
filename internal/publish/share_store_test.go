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

// fileWalkthrough builds a valid walkthrough whose step body carries body, so
// two calls with different bodies have different content digests and two calls
// with the same body are byte-identical (same digest).
func fileWalkthrough(id, body string) *PublishedWalkthrough {
	pw := validWalkthrough(id)
	pw.Steps[0].Body = body
	return pw
}

// TestPublishFileEditKeepsToken pins the headline S8 behaviour: editing a
// folder-served file mints a NEW immutable revision under the SAME token, so an
// already-shared URL keeps working and now serves the update. It also pins that
// the edit did not disturb the at-rest token representation (INV-3).
func TestPublishFileEditKeepsToken(t *testing.T) {
	s, dir := newStore(t)

	id, token, rev1, err := s.PublishFile(fileWalkthrough("wt", "v1"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile: %v", err)
	}
	if id == "" || token == "" || rev1 == "" {
		t.Fatalf("first publish returned id=%q token=%q rev=%q, want all non-empty", id, token, rev1)
	}
	got, gotID, ok := s.VerifyToken(token)
	if !ok || gotID != id || got.Steps[0].Body != "v1" {
		t.Fatalf("initial verify: ok=%v id=%q body=%q", ok, gotID, got.Steps[0].Body)
	}
	info1, err := s.Status(id)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Edit: same filename, changed content.
	id2, token2, rev2, err := s.PublishFile(fileWalkthrough("wt", "v2"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile edit: %v", err)
	}
	if id2 != id {
		t.Fatalf("edit changed share id: %q -> %q", id, id2)
	}
	if token2 != "" {
		t.Fatalf("edit minted a new token (%q); the existing token must be kept", token2)
	}
	if rev2 == rev1 {
		t.Fatalf("edit reused revision id %q; a new immutable revision is required", rev1)
	}
	info2, err := s.Status(id)
	if err != nil {
		t.Fatalf("Status after edit: %v", err)
	}
	if info2.Digest == info1.Digest {
		t.Fatalf("edit did not change the content digest (%q)", info2.Digest)
	}

	// The ORIGINAL token now serves the NEW revision.
	got, gotID, ok = s.VerifyToken(token)
	if !ok {
		t.Fatalf("original token stopped verifying after an edit")
	}
	if gotID != id {
		t.Fatalf("original token resolved to share %q, want %q", gotID, id)
	}
	if got.Steps[0].Body != "v2" {
		t.Fatalf("original token serves body %q, want the new revision %q", got.Steps[0].Body, "v2")
	}

	// INV-3 continuity: the record still holds only sha256(token), unchanged.
	if info2.TokenHashPrefix != info1.TokenHashPrefix {
		t.Fatalf("token hash changed across an edit: %q -> %q", info1.TokenHashPrefix, info2.TokenHashPrefix)
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if strings.Contains(string(data), token) {
		t.Fatalf("plaintext token found in record after edit")
	}
	if !strings.Contains(string(data), hashToken(token)) {
		t.Fatalf("token hash missing from record after edit")
	}

	// Republishing identical content is a no-op: no new revision, no new token.
	_, token3, rev3, err := s.PublishFile(fileWalkthrough("wt", "v2"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile unchanged: %v", err)
	}
	if rev3 != rev2 {
		t.Fatalf("unchanged content minted revision %q, want no-op on %q", rev3, rev2)
	}
	if token3 != "" {
		t.Fatalf("unchanged content minted a token %q", token3)
	}
}

// TestPublishFileDeleteRevokes pins that a vanished source file revokes the
// share, that a plain (non-file-backed) share is untouched by reconciliation,
// and that a file coming back mints a FRESH token — the old one stays dead.
func TestPublishFileDeleteRevokes(t *testing.T) {
	s, _ := newStore(t)
	id, token, _, err := s.PublishFile(fileWalkthrough("wt", "v1"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile: %v", err)
	}
	plainID, plainToken, err := s.Create(validWalkthrough("plain"), "/proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	revoked, err := s.ReconcileFiles("/proj", nil)
	if err != nil {
		t.Fatalf("ReconcileFiles: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != id {
		t.Fatalf("ReconcileFiles revoked %v, want [%s]", revoked, id)
	}
	if _, _, ok := s.VerifyToken(token); ok {
		t.Fatalf("token still verifies after its source file was deleted")
	}
	info, err := s.Status(id)
	if err != nil || !info.Revoked {
		t.Fatalf("Status after delete: info.Revoked=%v err=%v", info.Revoked, err)
	}
	// A share with no source file is not folder-backed and must survive.
	if _, _, ok := s.VerifyToken(plainToken); !ok {
		t.Fatalf("reconciliation revoked the non-file-backed share %s", plainID)
	}

	// The file comes back: new share, new token; the revoked token stays dead.
	newID, newToken, _, err := s.PublishFile(fileWalkthrough("wt", "v1"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile after revoke: %v", err)
	}
	if newID == id {
		t.Fatalf("republish reused the revoked share id %q", id)
	}
	if newToken == "" || newToken == token {
		t.Fatalf("republish after revoke must mint a fresh token, got %q", newToken)
	}
	if _, _, ok := s.VerifyToken(token); ok {
		t.Fatalf("revoked token resurrected by republishing the file")
	}
	if _, _, ok := s.VerifyToken(newToken); !ok {
		t.Fatalf("fresh token does not verify")
	}
}

// TestPublishFileRenameRevokes pins that a rename revokes the old name's share
// (it is a delete plus an add) and that the new name gets its own token, even
// though the content — and therefore the digest — is byte-identical.
func TestPublishFileRenameRevokes(t *testing.T) {
	s, _ := newStore(t)
	oldID, oldToken, _, err := s.PublishFile(fileWalkthrough("wt", "same"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile: %v", err)
	}
	// Rename: only the new name is present on disk now.
	revoked, err := s.ReconcileFiles("/proj", []string{"tour-v2.html"})
	if err != nil {
		t.Fatalf("ReconcileFiles: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != oldID {
		t.Fatalf("rename revoked %v, want [%s]", revoked, oldID)
	}
	if _, _, ok := s.VerifyToken(oldToken); ok {
		t.Fatalf("token still verifies after its source file was renamed")
	}

	newID, newToken, _, err := s.PublishFile(fileWalkthrough("wt", "same"), "/proj", "tour-v2.html")
	if err != nil {
		t.Fatalf("PublishFile renamed: %v", err)
	}
	if newID == oldID {
		t.Fatalf("renamed file reused the revoked share id")
	}
	if newToken == oldToken {
		t.Fatalf("renamed file reused the revoked token")
	}
	if _, gotID, ok := s.VerifyToken(newToken); !ok || gotID != newID {
		t.Fatalf("renamed share does not verify: ok=%v id=%q", ok, gotID)
	}
	if _, _, ok := s.VerifyToken(oldToken); ok {
		t.Fatalf("old token verifies after the renamed file was published")
	}
}

// TestPublishFileSurvivesRestart pins INV-8 for the file mapping itself: the
// filename -> share association is durable, so after a restart the same token
// still serves the latest revision AND reconciliation still knows which file
// each share tracks.
func TestPublishFileSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clk := fixedClock(time.Unix(1_700_000_000, 0).UTC())
	s1, err := New(dir, clk)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, token, rev1, err := s1.PublishFile(fileWalkthrough("wt", "v1"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile: %v", err)
	}
	_, _, rev2, err := s1.PublishFile(fileWalkthrough("wt", "v2"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile edit: %v", err)
	}
	if rev2 == rev1 {
		t.Fatalf("edit did not mint a new revision")
	}

	s2, err := New(dir, clk)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, gotID, ok := s2.VerifyToken(token)
	if !ok || gotID != id {
		t.Fatalf("token did not survive restart: ok=%v id=%q", ok, gotID)
	}
	if got.Steps[0].Body != "v2" {
		t.Fatalf("after restart the token serves body %q, want the latest revision %q", got.Steps[0].Body, "v2")
	}
	// Editing after restart still keeps the same share/token.
	id3, token3, rev3, err := s2.PublishFile(fileWalkthrough("wt", "v3"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile after restart: %v", err)
	}
	if id3 != id || token3 != "" || rev3 == rev2 {
		t.Fatalf("post-restart edit: id=%q token=%q rev=%q", id3, token3, rev3)
	}

	// Reconciliation after restart knows the mapping and revokes durably.
	revoked, err := s2.ReconcileFiles("/proj", nil)
	if err != nil {
		t.Fatalf("ReconcileFiles: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != id {
		t.Fatalf("post-restart reconcile revoked %v, want [%s]", revoked, id)
	}
	s3, err := New(dir, clk)
	if err != nil {
		t.Fatalf("reopen after revoke: %v", err)
	}
	if _, _, ok := s3.VerifyToken(token); ok {
		t.Fatalf("revocation did not survive restart")
	}
}

// TestPublishFileIdenticalContentDistinctOwners is the regression the prior
// content-addressing defect needed and did not have
// (.claude/rules/publish-security-review-lessons.md §2): two DIFFERENT owners
// publishing BYTE-IDENTICAL content — hence the identical content digest — must
// get independent shares. The digest identifies a revision inside an
// already-owner-scoped share; it is never the key a share is resolved by. If it
// ever becomes one, one owner's edits/revocations bleed into the other's reads.
func TestPublishFileIdenticalContentDistinctOwners(t *testing.T) {
	s, _ := newStore(t)
	content := func() *PublishedWalkthrough { return fileWalkthrough("wt", "identical") }

	idA, tokA, revA, err := s.PublishFile(content(), "/project-a", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile A: %v", err)
	}
	idB, tokB, revB, err := s.PublishFile(content(), "/project-b", "tour.html")
	if err != nil {
		t.Fatalf("PublishFile B: %v", err)
	}

	// Test premise: the two shares really do collide on content digest.
	infoA, _ := s.Status(idA)
	infoB, _ := s.Status(idB)
	if infoA.Digest != infoB.Digest {
		t.Fatalf("premise broken: digests differ (%q vs %q), the collision path is untested", infoA.Digest, infoB.Digest)
	}
	if idA == idB || tokA == tokB || revA == revB {
		t.Fatalf("identical content collapsed two owners: id %q/%q token-equal=%v rev %q/%q",
			idA, idB, tokA == tokB, revA, revB)
	}
	// The owner-scoped lookup key must separate them.
	if sourceKey("/project-a", "tour.html") == sourceKey("/project-b", "tour.html") {
		t.Fatalf("sourceKey ignores the owner: two projects share one key")
	}
	if _, gotID, ok := s.VerifyToken(tokA); !ok || gotID != idA {
		t.Fatalf("token A resolved to %q (ok=%v), want %q", gotID, ok, idA)
	}
	if _, gotID, ok := s.VerifyToken(tokB); !ok || gotID != idB {
		t.Fatalf("token B resolved to %q (ok=%v), want %q", gotID, ok, idB)
	}

	// Editing A's file must not touch B's revision.
	if _, _, _, err := s.PublishFile(fileWalkthrough("wt", "a-edited"), "/project-a", "tour.html"); err != nil {
		t.Fatalf("PublishFile A edit: %v", err)
	}
	if got, _, ok := s.VerifyToken(tokA); !ok || got.Steps[0].Body != "a-edited" {
		t.Fatalf("A did not take its own edit: ok=%v body=%q", ok, got.Steps[0].Body)
	}
	if got, _, ok := s.VerifyToken(tokB); !ok || got.Steps[0].Body != "identical" {
		t.Fatalf("A's edit leaked into B: ok=%v body=%q", ok, got.Steps[0].Body)
	}

	// Revoking A's file must not revoke B's identical-content share.
	revoked, err := s.ReconcileFiles("/project-a", nil)
	if err != nil {
		t.Fatalf("ReconcileFiles A: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != idA {
		t.Fatalf("reconcile A revoked %v, want [%s]", revoked, idA)
	}
	if _, _, ok := s.VerifyToken(tokA); ok {
		t.Fatalf("A's token survived revocation")
	}
	if _, _, ok := s.VerifyToken(tokB); !ok {
		t.Fatalf("revoking A killed B's identical-content share")
	}

	// Control-plane listing stays project-scoped.
	if got := s.List("/project-a"); len(got) != 1 {
		t.Fatalf("List(/project-a) = %d, want 1", len(got))
	}
	if got := s.List("/project-b"); len(got) != 1 {
		t.Fatalf("List(/project-b) = %d, want 1", len(got))
	}
}

// TestPublishFileRejectsBadInput pins the trust boundary: no filename and no
// invalid artifact ever reaches the store.
func TestPublishFileRejectsBadInput(t *testing.T) {
	s, dir := newStore(t)
	if _, _, _, err := s.PublishFile(fileWalkthrough("wt", "v1"), "/proj", ""); err == nil {
		t.Fatalf("PublishFile accepted an empty file name")
	}
	if _, _, _, err := s.PublishFile(nil, "/proj", "tour.html"); err == nil {
		t.Fatalf("PublishFile accepted a nil walkthrough")
	}
	bad := fileWalkthrough("wt", "v1")
	bad.Steps = nil
	if _, _, _, err := s.PublishFile(bad, "/proj", "tour.html"); err == nil {
		t.Fatalf("PublishFile accepted an invalid artifact")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("rejected publish persisted a record: %s", e.Name())
		}
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

// share_store.go is the durable, authoritative on-disk store for public
// walkthrough shares (P6 of the walkthrough-publish epic). It owns the
// share-token lifecycle — create, status, list, revoke, rotate — and the public
// constant-time token verification path.
//
// # Architectural exception (daemon-architecture.md §"Data Ownership")
//
// daemon-architecture.md is emphatic that daemon in-memory state is a *cache,
// never authority*, and that registry/blob/inbox state is rebuilt-from-config or
// evicted on session end and never persisted across sessions. This store is the
// deliberate, documented exception the P1 security spec
// (docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md §8,
// "Deviations" #1) calls out: the FIRST daemon-side artifact whose on-disk record
// IS the source of truth and which must outlive the session (INV-8). It is a
// distinct persistent store, NOT one of the transient caches — the ephemeral
// registry/cache patterns are intentionally not reused here.
//
// # Token secrecy (spec §3, INV-3 / INV-9)
//
//   - Tokens are 256-bit CSPRNG (crypto/rand), base64url (~43 chars). This is the
//     first PRODUCTION use of crypto/rand in internal/ (sshclient's is test-only).
//   - Only sha256(token) is persisted — plaintext never touches disk, logs, or
//     events. The plaintext is returned to the publisher ONCE at create/rotate.
//   - Verification is constant-time (crypto/subtle.ConstantTimeCompare) — the
//     first use of that primitive in this repo. There is no plain-== fallback.
//
// # Durability (spec §8, INV-4 / INV-8)
//
//   - One JSON record file per share, written atomically (temp + fsync + rename)
//     with restrictive 0600 perms in a 0700 directory.
//   - Reloaded on boot so shares survive daemon restart.
//   - Each record carries an integrity checksum; a corrupt/unreadable/truncated
//     record makes New FAIL LOUD (returns an error) rather than silently serving a
//     partial or empty store (Silent Failure Prohibition).
//   - Revoke and rotate invalidate the old token immediately (no grace window).
package publish

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// tokenBytes is the CSPRNG entropy per share token: 256 bits (spec §3).
	tokenBytes = 32
	// idBytes is the entropy of the viewer-safe public share id. It is distinct
	// from the secret token (spec: "viewer-safe id distinct from the secret
	// token") — non-guessable but not itself a credential; it names the share on
	// the control plane (status/revoke/rotate) without carrying the token.
	idBytes = 16
	// recordSuffix is the filename suffix for a persisted share record.
	recordSuffix = ".json"
	// tmpSuffix marks an in-flight atomic write; skipped on load.
	tmpSuffix = ".tmp"
)

// Sentinel errors.
var (
	// ErrNotFound is returned when no share matches the given id.
	ErrNotFound = errors.New("publishstore: share not found")
	// ErrRevoked is returned when an operation targets a revoked share.
	ErrRevoked = errors.New("publishstore: share is revoked")
	// ErrCorrupt is returned by New when a persisted record fails to load or its
	// integrity checksum does not match — the store fails loud rather than
	// silently dropping the record or serving an empty store.
	ErrCorrupt = errors.New("publishstore: corrupt record")
	// ErrUnservable is returned by an operation that would put a QUARANTINED
	// share back in front of viewers (today: Rotate). A quarantined share's
	// record is intact but may not be served under the current rules — see
	// Store.Quarantined. It is NOT a load error: load quarantines the one share
	// and carries on. The only thing a quarantined share accepts is Revoke.
	ErrUnservable = errors.New("publishstore: share is quarantined and may not be served")
	// ErrReadOnly is returned by every mutating operation on a store opened with
	// OpenReadOnly (or OpenFeedbackStoreReadOnly). A read-only store never
	// creates, rewrites, or removes anything on disk.
	ErrReadOnly = errors.New("publishstore: store is read-only")
)

// Share is one persisted published-share record. The plaintext token is NEVER a
// field — only its sha256 hash is stored (spec §3). The embedded walkthrough is
// an immutable, already-validated revision; rotate/revoke operate on the share's
// token, never by mutating the revision.
type Share struct {
	// ID is the viewer-safe public share id (CSPRNG, distinct from the token).
	ID string `json:"id"`
	// TokenHash is hex sha256(token). The plaintext token is never persisted.
	TokenHash string `json:"token_hash"`
	// ProjectPath is the owning project (control-plane scoping only; the public
	// verify path deliberately ignores it — INV-1).
	ProjectPath string `json:"project_path"`
	// Walkthrough is the immutable validated published revision.
	Walkthrough *PublishedWalkthrough `json:"walkthrough"`
	// Digest is the sha256 of the canonical walkthrough encoding — the stable
	// identity of this immutable revision. It is metadata ON an already
	// owner-scoped record; it is never a lookup key (see sourceKey).
	Digest string `json:"digest"`
	// SourceFile is the folder-relative filename this share tracks, for a share
	// published from a served folder; "" for a directly-created share. Together
	// with ProjectPath it forms the share's owner-scoped identity.
	SourceFile string `json:"source_file,omitempty"`
	// RevisionID names the current immutable revision (the one Walkthrough and
	// Digest describe). It is a minted CSPRNG publish-EVENT id (RevisionID), not a
	// content digest — see revision_identity.go. An edit mints a new one; the
	// token does not change.
	RevisionID RevisionID `json:"revision_id,omitempty"`
	// Revisions is the append-only revision history. It records revision
	// identity (id + digest + time) only, NOT the historical artifact bodies:
	// nothing serves an old revision — the token always resolves to the current
	// one — so retaining every past walkthrough would grow the record without
	// a reader. Add bodies here only when something actually reads them.
	Revisions []ShareRevision `json:"revisions,omitempty"`
	// Revoked tombstones the share (kept for audit, not deleted — spec §3).
	Revoked   bool       `json:"revoked"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// Checksum is hex sha256 over the canonical record with Checksum="". It
	// detects truncation and tampering so load can fail loud (spec §8).
	Checksum string `json:"checksum"`
}

// ShareRevision is one immutable published revision of a share: the identity of
// a content state the share pointed at, not the content itself.
type ShareRevision struct {
	// ID is the minted publish-EVENT id (distinct per publish, never reused).
	ID RevisionID `json:"id"`
	// Digest is the CONTENT identity of this revision (collapses across
	// byte-identical content — this is what the feedback path keys on).
	Digest    string    `json:"digest"`
	CreatedAt time.Time `json:"created_at"`
}

// ShareInfo is the redaction-safe view of a share for the control plane. It
// carries NO token plaintext and only a short hash prefix for correlation
// (INV-9). Used by Status and List.
type ShareInfo struct {
	ID              string     `json:"id"`
	ProjectPath     string     `json:"project_path"`
	Title           string     `json:"title"`
	Steps           int        `json:"steps"`
	Digest          string     `json:"digest"`
	TokenHashPrefix string     `json:"token_hash_prefix"` // hash[:8]; never the token
	Revoked         bool       `json:"revoked"`
	CreatedAt       time.Time  `json:"created_at"`
	RotatedAt       *time.Time `json:"rotated_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	// Quarantined is true for a share the store loaded but will not serve (see
	// Store.Quarantined). Its token resolves to not-found. QuarantineReason and
	// RecordPath are set only when it is.
	Quarantined      bool   `json:"quarantined,omitempty"`
	QuarantineReason string `json:"quarantine_reason,omitempty"`
	RecordPath       string `json:"record_path,omitempty"`
}

// QuarantinedShare describes one share the store loaded but refuses to serve.
//
// There is exactly ONE reason a share is quarantined: its stored record is
// live, intact, and carries an addScript op (INV-14) — it was written before
// publish began refusing that op on 2026-08-18. Nothing else quarantines: a
// record that fails to decode or fails its checksum is corruption and still
// fails the whole load (ErrCorrupt).
//
// A quarantined share is never indexed for serving, so its token gets exactly
// the not-found an unknown token gets; it is never served with its script and
// never quietly served without it. Its record file is left untouched. It stays
// visible to its owner (Status/List report it, flagged) and is cleared by
// Revoke — which tombstones it like any share — or, for a folder-served share,
// when its file is republished without the op or deleted.
type QuarantinedShare struct {
	// ID is the viewer-safe share id.
	ID string
	// Title is the stored walkthrough's title, for a human-readable report.
	Title string
	// ProjectPath is the owning project (the scope a report must be sent to).
	ProjectPath string
	// SourceFile is the folder-relative file a `publish serve` share tracks;
	// "" for a share created through the control plane.
	SourceFile string
	// Reason is why it may not be served — the same INV-14 text publish gives.
	Reason string
	// RecordPath is the record file on disk, unchanged by the quarantine.
	RecordPath string
}

// Store is the durable, authoritative publish store. Safe for concurrent use.
type Store struct {
	dir string
	now func() time.Time
	// readOnly makes persist refuse, so no mutating method can reach disk. It
	// is set only by OpenReadOnly, for a reader of a store another process owns.
	readOnly bool

	mu     sync.RWMutex
	byID   map[string]*Share
	byHash map[string]string // token hash -> share id (current tokens only)
	// bySource maps sourceKey(projectPath, fileName) -> share id for
	// folder-served shares, so an edited file resolves back to its existing
	// share (and token). Keyed by OWNER + FILENAME, never by content digest.
	bySource map[string]string
	// quarantined holds the shares load refused to index for serving, by id.
	// Such a share IS in byID (so its owner can see and revoke it) and is in
	// NEITHER byHash NOR bySource (so no token and no file resolves to it).
	// Readers: Quarantined (startup reports, user-visible), Status/List.
	quarantined map[string]QuarantinedShare
}

// New opens (creating if absent) the durable publish store rooted at dir and
// loads every persisted record. If now is nil, time.Now is used (tests inject a
// deterministic clock). A corrupt, truncated, or unreadable record makes New
// return an error wrapping ErrCorrupt — the store fails loud rather than silently
// starting empty (Silent Failure Prohibition; spec §8 / INV-8).
func New(dir string, now func() time.Time) (*Store, error) {
	if dir == "" {
		return nil, errors.New("publishstore: empty dir")
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("publishstore: create dir: %w", err)
	}
	s := &Store{
		dir:         dir,
		now:         now,
		byID:        make(map[string]*Share),
		byHash:      make(map[string]string),
		bySource:    make(map[string]string),
		quarantined: make(map[string]QuarantinedShare),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens an EXISTING publish store for reading only. Unlike New it
// creates nothing — a missing directory is an error, not an empty store to be
// conjured — and every mutating method returns ErrReadOnly. It is what a
// second process uses to read a store that a running `agnt publish serve`
// owns: two writers on these authoritative records is the failure the
// per-store ownership rule exists to prevent.
func OpenReadOnly(dir string, now func() time.Time) (*Store, error) {
	if dir == "" {
		return nil, errors.New("publishstore: empty dir")
	}
	if now == nil {
		now = time.Now
	}
	s := &Store{
		dir:         dir,
		now:         now,
		readOnly:    true,
		byID:        make(map[string]*Share),
		byHash:      make(map[string]string),
		bySource:    make(map[string]string),
		quarantined: make(map[string]QuarantinedShare),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads and integrity-checks every record. Fails loud on the first corrupt
// record.
func (s *Store) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("publishstore: read dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, recordSuffix) || strings.HasSuffix(name, tmpSuffix) {
			continue
		}
		path := filepath.Join(s.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%w: read %s: %v", ErrCorrupt, name, err)
		}
		sh, err := decodeRecord(data)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
		}
		// INV-14 at LOAD. Publish has refused addScript since 2026-08-18, but a
		// record written before that gate carries the op and — because load
		// checked only the checksum — was indexed and served, script and all.
		// A LIVE share carrying it is QUARANTINED: kept in byID so its owner can
		// see and revoke it, and indexed in neither byHash nor bySource, so no
		// token and no file resolves to it. It is neither served with its script
		// nor quietly served without it, and every other share loads normally —
		// one stale record must not take the plane down. The record file is not
		// touched. A REVOKED record carrying the op needs nothing: it is never
		// served. This is the ONLY quarantine reason; corruption still fails.
		if !sh.Revoked && carriesAddScript(sh.Walkthrough) {
			s.byID[sh.ID] = sh
			s.quarantined[sh.ID] = QuarantinedShare{
				ID:          sh.ID,
				Title:       sh.info().Title,
				ProjectPath: sh.ProjectPath,
				SourceFile:  sh.SourceFile,
				Reason:      errAddScriptRefused().Error(),
				RecordPath:  path,
			}
			continue
		}
		s.byID[sh.ID] = sh
		if sh.Revoked {
			continue
		}
		s.byHash[sh.TokenHash] = sh.ID
		if sh.SourceFile != "" {
			key := sourceKey(sh.ProjectPath, sh.SourceFile)
			// Two LIVE shares for one (project, file) is unreachable through
			// PublishFile (it upserts) and would make "which share owns this
			// file" nondeterministic, so treat it as corrupt on-disk state and
			// fail loud rather than silently picking one.
			if prev, dup := s.bySource[key]; dup {
				return fmt.Errorf("%w: %s and %s both claim file %q of %q",
					ErrCorrupt, prev, sh.ID, sh.SourceFile, sh.ProjectPath)
			}
			s.bySource[key] = sh.ID
		}
	}
	return nil
}

// decodeRecord strictly decodes a record and verifies its integrity checksum.
func decodeRecord(data []byte) (*Share, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var sh Share
	if err := dec.Decode(&sh); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("trailing data")
	}
	if sh.ID == "" || sh.TokenHash == "" {
		return nil, errors.New("missing id or token hash")
	}
	if err := sh.verifyChecksum(); err != nil {
		return nil, err
	}
	return &sh, nil
}

// Create validates the artifact via the P2 validators, mints a fresh 256-bit
// token, persists the record atomically, and returns the viewer-safe id plus the
// plaintext token — which is returned ONCE here and never recoverable.
func (s *Store) Create(pw *PublishedWalkthrough, projectPath string) (id, token string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, token, err := s.createShareLocked(pw, projectPath, "")
	if err != nil {
		return "", "", err
	}
	return sh.ID, token, nil
}

// Rotate mints a new token for an existing share, invalidates the prior token in
// the same atomic write, and returns the new plaintext token ONCE. In-flight
// requests bearing the old token stop verifying immediately after commit.
func (s *Store) Rotate(id string) (token string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.byID[id]
	if !ok {
		return "", ErrNotFound
	}
	if cur.Revoked {
		return "", ErrRevoked
	}
	if q, held := s.quarantined[id]; held {
		// A fresh token would index the share for serving again.
		return "", fmt.Errorf("%w: %s — revoke it to clear it", ErrUnservable, q.Reason)
	}
	token, hash, err := mintToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	oldHash := cur.TokenHash
	updated := *cur
	updated.TokenHash = hash
	updated.RotatedAt = &now
	updated.Checksum = updated.computeChecksum()

	if err := s.persist(&updated); err != nil {
		return "", err
	}
	s.byID[id] = &updated
	delete(s.byHash, oldHash) // old token dies immediately
	s.byHash[hash] = id
	return token, nil
}

// Revoke tombstones a share atomically: the token stops verifying and every
// public route dies at once (INV-4). Idempotent. The record is kept (not
// deleted) so a "was published, now revoked" audit trail survives.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeLocked(id)
}

// revokeLocked is Revoke's body; ReconcileFiles revokes a batch under one lock.
// Caller holds the write lock.
func (s *Store) revokeLocked(id string) error {
	cur, ok := s.byID[id]
	if !ok {
		return ErrNotFound
	}
	if cur.Revoked {
		return nil
	}
	now := s.now()
	updated := *cur
	updated.Revoked = true
	updated.RevokedAt = &now
	updated.Checksum = updated.computeChecksum()

	if err := s.persist(&updated); err != nil {
		return err
	}
	s.byID[id] = &updated
	// A quarantined share that is revoked is an ordinary tombstone from here
	// on: this is the supported way to clear a quarantine.
	delete(s.quarantined, id)
	delete(s.byHash, cur.TokenHash) // token verifies no more, immediately
	if cur.SourceFile != "" {
		// Drop the file mapping so the filename coming back mints a FRESH share
		// and token instead of resurrecting this revoked one.
		key := sourceKey(cur.ProjectPath, cur.SourceFile)
		if s.bySource[key] == id {
			delete(s.bySource, key)
		}
	}
	return nil
}

// Status returns the redaction-safe view of a single share (no token).
func (s *Store) Status(id string) (ShareInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sh, ok := s.byID[id]
	if !ok {
		return ShareInfo{}, ErrNotFound
	}
	return s.infoLocked(sh), nil
}

// Quarantined returns the shares this store loaded but refuses to serve, sorted
// by id. It is what a startup report and an owner-facing listing are built
// from: a quarantine that nobody is told about is a share that silently went
// dark. The result is a snapshot; Revoke removes a share from it.
func (s *Store) Quarantined() []QuarantinedShare {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]QuarantinedShare, 0, len(s.quarantined))
	for _, q := range s.quarantined {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// infoLocked is Share.info plus the quarantine flag, which lives on the store
// rather than the record. Caller holds the lock.
func (s *Store) infoLocked(sh *Share) ShareInfo {
	info := sh.info()
	if q, held := s.quarantined[sh.ID]; held {
		info.Quarantined = true
		info.QuarantineReason = q.Reason
		info.RecordPath = q.RecordPath
	}
	return info
}

// List returns redaction-safe views of every share owned by projectPath
// (control-plane, project-scoped). Results are sorted by creation time.
func (s *Store) List(projectPath string) []ShareInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ShareInfo, 0, len(s.byID))
	for _, sh := range s.byID {
		if sh.ProjectPath != projectPath {
			continue
		}
		out = append(out, s.infoLocked(sh))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// VerifyToken is the PUBLIC verification entrypoint. Given a token from an
// anonymous public request it constant-time verifies against the store and
// returns the immutable published revision plus the viewer-safe share id.
//
// This path deliberately carries NO project scope (INV-1 / Deviations #3): it
// takes only a token and returns only the published data. It never consults, and
// structurally cannot satisfy, dev session scoping (resolveProjectScope) — a
// token maps to a revision, never to a SessionCode or Directory. It does NOT use
// scope.Unscoped (which would match every project, the opposite of intended).
//
// A revoked or unknown token returns ok=false with no timing side-channel of
// value: the accept decision is gated by subtle.ConstantTimeCompare (INV-3),
// never a plain == on the hash.
func (s *Store) VerifyToken(token string) (rev *PublishedWalkthrough, shareID string, ok bool) {
	hash := hashToken(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, found := s.byHash[hash]
	if !found {
		return nil, "", false
	}
	sh := s.byID[id]
	if sh == nil || sh.Revoked {
		return nil, "", false
	}
	// A quarantined share is never in byHash, so this cannot be reached for
	// one; the check is the second lock on the same door, and it answers with
	// the SAME not-found as an unknown token — a viewer learns nothing.
	if _, held := s.quarantined[id]; held {
		return nil, "", false
	}
	// Authoritative accept gate: constant-time compare of the stored hash against
	// the candidate hash (INV-3, no plain-== fallback).
	if subtle.ConstantTimeCompare([]byte(sh.TokenHash), []byte(hash)) != 1 {
		return nil, "", false
	}
	return sh.Walkthrough, sh.ID, true
}

// ShareDigest returns the content digest of a live share's CURRENT revision. The
// public plane binds it into every subresource reference it signs, so a
// reference minted for one revision stops verifying the moment the share is
// re-published with different content (publish-security-review-lessons §16).
// A revoked or unknown share reports ok=false.
//
// The digest is used here only as a version key on a record already resolved
// by share id — never as a lookup key (§2).
func (s *Store) ShareDigest(id string) (digest string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sh := s.byID[id]
	if sh == nil || sh.Revoked {
		return "", false
	}
	if _, held := s.quarantined[id]; held {
		return "", false
	}
	return sh.Digest, true
}

// PublishFile publishes pw as the current revision of the share that tracks
// fileName under projectPath — the folder-served-file lifecycle.
//
//   - First publish of a filename mints a share and returns its plaintext token
//     ONCE (same contract as Create).
//   - Re-publishing the SAME filename with CHANGED content appends a new
//     immutable revision and swaps the served artifact, but keeps the token
//     untouched, so an already-shared URL keeps working and now shows the update.
//     token is returned EMPTY in that case: no new secret was minted and the
//     existing plaintext is not recoverable by design (INV-3).
//   - Re-publishing unchanged content is a no-op returning the current revision.
//
// A previously revoked share is never resurrected: a file that comes back is a
// fresh share with a fresh token, so the revoked token stays dead (INV-4).
func (s *Store) PublishFile(pw *PublishedWalkthrough, projectPath, fileName string) (id, token string, revisionID RevisionID, err error) {
	if fileName == "" {
		return "", "", "", errors.New("publishstore: empty file name")
	}
	if pw == nil {
		return "", "", "", errors.New("publishstore: nil walkthrough")
	}
	// Trust boundary: validate before anything is stored or compared.
	if err := pw.Validate(); err != nil {
		return "", "", "", fmt.Errorf("publishstore: invalid artifact: %w", err)
	}
	digest, err := Digest(pw)
	if err != nil {
		return "", "", "", fmt.Errorf("publishstore: digest: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.liveSourceLocked(projectPath, fileName)
	if !ok {
		sh, token, err := s.createShareLocked(pw, projectPath, fileName)
		if err != nil {
			return "", "", "", err
		}
		// The file has now been published clean (pw validated above). A
		// quarantined share that tracked this same file is superseded: tombstone
		// it, so the folder is not left with a quarantine nothing can clear —
		// `publish serve` has no revoke of its own. The new share and token are
		// already committed, so a failure here is reported but does not undo them.
		for id, q := range s.quarantined {
			if q.ProjectPath == projectPath && q.SourceFile == fileName {
				if rerr := s.revokeLocked(id); rerr != nil {
					return sh.ID, token, sh.RevisionID, fmt.Errorf("publishstore: published %s but could not retire its quarantined predecessor %s: %w", fileName, id, rerr)
				}
			}
		}
		return sh.ID, token, sh.RevisionID, nil
	}
	if digest == cur.Digest {
		return cur.ID, "", cur.RevisionID, nil // unchanged content: no new revision
	}
	revID, err := mintRevisionID()
	if err != nil {
		return "", "", "", err
	}
	now := s.now()
	updated := *cur
	updated.Walkthrough = pw
	updated.Digest = digest
	updated.RevisionID = revID
	// Copy-on-append: `updated` is a shallow copy of a record other readers may
	// still hold, so never append into the shared backing array.
	updated.Revisions = append(append(make([]ShareRevision, 0, len(cur.Revisions)+1), cur.Revisions...),
		ShareRevision{ID: revID, Digest: digest, CreatedAt: now})
	updated.UpdatedAt = &now
	// TokenHash is deliberately carried over verbatim: the whole point of this
	// path is that the token — and therefore every URL already handed out —
	// outlives the content it points at.
	updated.Checksum = updated.computeChecksum()

	if err := s.persist(&updated); err != nil {
		return "", "", "", err
	}
	s.byID[cur.ID] = &updated
	return cur.ID, "", revID, nil
}

// ReconcileFiles revokes every file-backed share of projectPath whose source
// file is no longer among presentFiles. Deleting a file revokes its share; a
// rename is a delete plus an add, so the old name is revoked here and the new
// name mints a fresh share (and a fresh token) via PublishFile. Returns the
// revoked share ids, sorted for a deterministic caller-visible result.
//
// Shares with no SourceFile (created via Create, not folder-served) are never
// touched — they have no file whose absence could mean anything.
func (s *Store) ReconcileFiles(projectPath string, presentFiles []string) ([]string, error) {
	present := make(map[string]bool, len(presentFiles))
	for _, f := range presentFiles {
		present[f] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var stale []string
	for id, sh := range s.byID {
		if sh.Revoked || sh.SourceFile == "" || sh.ProjectPath != projectPath {
			continue
		}
		if !present[sh.SourceFile] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	revoked := make([]string, 0, len(stale))
	for _, id := range stale {
		if err := s.revokeLocked(id); err != nil {
			return revoked, err
		}
		revoked = append(revoked, id)
	}
	return revoked, nil
}

// sourceKey is the lookup key for a file-backed share. It is (owner project,
// filename) and deliberately DOES NOT include the content digest.
//
// This is the load-bearing detail of this feature
// (.claude/rules/publish-security-review-lessons.md §2): a prior defect in this
// store keyed a read by content digest, so two projects publishing
// byte-identical content collided and one project's data was served to the
// other. A content digest is a legitimate dedup/versioning key and it does
// identify a revision INSIDE a share — but it must never be the key by which a
// share is resolved, because content is not an identity. Every read here starts
// from the owner (ProjectPath) and the filename; the digest is only ever
// compared against the already-owner-scoped record it belongs to.
// TestPublishFileIdenticalContentDistinctOwners is the regression.
func sourceKey(projectPath, fileName string) string {
	return projectPath + "\x00" + fileName
}

// liveSourceLocked resolves the non-revoked share tracking fileName for
// projectPath. A revoked share is reported absent so a returning file mints a
// new share rather than resurrecting a dead token. Caller holds the lock.
func (s *Store) liveSourceLocked(projectPath, fileName string) (*Share, bool) {
	id, ok := s.bySource[sourceKey(projectPath, fileName)]
	if !ok {
		return nil, false
	}
	sh := s.byID[id]
	if sh == nil || sh.Revoked {
		return nil, false
	}
	return sh, true
}

// createShareLocked mints, persists, and indexes a brand-new share. sourceFile
// is "" for a directly-created (non-folder-served) share. Caller holds the lock.
func (s *Store) createShareLocked(pw *PublishedWalkthrough, projectPath, sourceFile string) (*Share, string, error) {
	if pw == nil {
		return nil, "", errors.New("publishstore: nil walkthrough")
	}
	// Trust boundary: reject an invalid artifact before it is ever stored (spec:
	// "immutable revisions validated via the P2 validators before stored").
	if err := pw.Validate(); err != nil {
		return nil, "", fmt.Errorf("publishstore: invalid artifact: %w", err)
	}
	digest, err := Digest(pw)
	if err != nil {
		return nil, "", fmt.Errorf("publishstore: digest: %w", err)
	}
	id, err := mintID()
	if err != nil {
		return nil, "", err
	}
	token, hash, err := mintToken()
	if err != nil {
		return nil, "", err
	}
	revID, err := mintRevisionID()
	if err != nil {
		return nil, "", err
	}
	now := s.now()
	sh := &Share{
		ID:          id,
		TokenHash:   hash,
		ProjectPath: projectPath,
		Walkthrough: pw,
		Digest:      digest,
		SourceFile:  sourceFile,
		RevisionID:  revID,
		Revisions:   []ShareRevision{{ID: revID, Digest: digest, CreatedAt: now}},
		CreatedAt:   now,
	}
	sh.Checksum = sh.computeChecksum()

	if err := s.persist(sh); err != nil {
		return nil, "", err
	}
	s.byID[id] = sh
	s.byHash[hash] = id
	if sourceFile != "" {
		s.bySource[sourceKey(projectPath, sourceFile)] = id
	}
	return sh, token, nil
}

// persist writes sh to its record file atomically with 0600 perms. Caller holds
// the write lock.
func (s *Store) persist(sh *Share) error {
	if s.readOnly {
		return ErrReadOnly
	}
	data, err := json.MarshalIndent(sh, "", "  ")
	if err != nil {
		return fmt.Errorf("publishstore: marshal: %w", err)
	}
	path := filepath.Join(s.dir, sh.ID+recordSuffix)
	tmp := path + tmpSuffix
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("publishstore: write temp: %w", err)
	}
	// WriteFile honors umask; force exactly 0600 so the secret-bearing record is
	// never group/other readable regardless of the process umask.
	if err := os.Chmod(tmp, 0600); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publishstore: chmod temp: %w", err)
	}
	if err := fsyncFile(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publishstore: fsync temp: %w", err)
	}
	if err := renameDurable(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publishstore: %w", err)
	}
	return nil
}

// fsyncFile flushes the temp file to disk before the atomic rename so a crash
// mid-write never leaves a half-written record (spec §8 atomicity).
func fsyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// info renders the redaction-safe view.
func (sh *Share) info() ShareInfo {
	title := ""
	steps := 0
	if sh.Walkthrough != nil {
		title = sh.Walkthrough.Title
		steps = len(sh.Walkthrough.Steps)
	}
	prefix := sh.TokenHash
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return ShareInfo{
		ID:              sh.ID,
		ProjectPath:     sh.ProjectPath,
		Title:           title,
		Steps:           steps,
		Digest:          sh.Digest,
		TokenHashPrefix: prefix,
		Revoked:         sh.Revoked,
		CreatedAt:       sh.CreatedAt,
		RotatedAt:       sh.RotatedAt,
		RevokedAt:       sh.RevokedAt,
	}
}

// computeChecksum returns the integrity checksum over the record with Checksum
// blanked. encoding/json sorts map keys, so the encoding is deterministic.
func (sh *Share) computeChecksum() string {
	c := *sh
	c.Checksum = ""
	b, _ := json.Marshal(&c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verifyChecksum fails loud when the stored checksum does not match a
// recomputation (truncation or tampering).
func (sh *Share) verifyChecksum() error {
	if sh.Checksum == "" {
		return errors.New("missing checksum")
	}
	if sh.Checksum != sh.computeChecksum() {
		return errors.New("checksum mismatch")
	}
	return nil
}

// mintToken generates a fresh 256-bit CSPRNG token and its hex sha256 hash.
func mintToken() (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("publishstore: csprng: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

// mintID generates a fresh CSPRNG viewer-safe share id.
func mintID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("publishstore: csprng: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// mintRevisionID generates a fresh CSPRNG publish-EVENT id. It shares mintID's
// entropy but is typed RevisionID so a minted event id can never be mistaken for
// (or passed as) a content RevisionDigest — see revision_identity.go.
func mintRevisionID() (RevisionID, error) {
	s, err := mintID()
	if err != nil {
		return "", err
	}
	return RevisionID(s), nil
}

// hashToken returns the hex sha256 of a token — the only representation that
// ever touches disk, logs, or events.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ScrubSharePath redacts a public share URL path for logging. It replaces the
// token in a "/s/{token}" (optionally with a trailing sub-path) with its 8-hex
// hash prefix, so the proxy traffic log (proxy_handler.go:532,
// HTTPLogEntry.URL = r.URL.String()) records only hash[:8], never the raw token
// (INV-9). A path that is not a share path is returned unchanged.
func ScrubSharePath(urlPath string) string {
	const prefix = "/s/"
	if !strings.HasPrefix(urlPath, prefix) {
		return urlPath
	}
	rest := urlPath[len(prefix):]
	if rest == "" {
		return urlPath
	}
	// The token ends at the first '/' (a sub-route like /feedback) or '?' (a
	// query string); everything from there on is a non-secret tail.
	end := len(rest)
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		end = i
	}
	tok := rest[:end]
	tail := rest[end:]
	if tok == "" {
		return urlPath
	}
	return prefix + hashToken(tok)[:8] + tail
}

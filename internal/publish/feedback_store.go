// Feedback ingestion for the public walkthrough-publish plane (P8).
//
// This is the durable, append-only, rate-limited sink for ANONYMOUS viewer
// feedback (spec §7, INV-7). Feedback is DATA crossing a trust boundary, never a
// command: it is size-capped, UTF-8/schema validated, rate-limited per
// (share, IP), stored inert (raw, never reflected), and evicted by a retention
// ring. It mirrors the share_store.go durability discipline — one JSON file per
// share, written atomically (temp + fsync + rename) with 0600 perms in a 0700
// dir, reloaded on boot, and failing LOUD on a corrupt/tampered record rather
// than silently serving a partial or empty store (spec §8, INV-8).
//
// # XSS / no reflection (INV-7)
//
// The stored body is kept RAW and inert. The store has no HTML render path; a
// payload containing "<script>" or "javascript:" is persisted as-is and read
// back byte-identical. Escaping is the control-plane read-back's job (P9's
// `publish feedback list`); the public ingest path never echoes feedback into a
// response, so there is no reflection surface here.
//
// # IP / X-Forwarded-For trust stance
//
// The rate-limit key is built from the client address the WIRING LAYER hands to
// Accept/Admit, never from a header this store reads. By default that is the
// connection peer (r.RemoteAddr): a forwarded-address header is attacker-
// controlled on a public plane, so trusting it blindly would let one client
// rotate the header to evade the per-client bucket. The one sanctioned
// exception lives in the wiring layer (proxy.PublicHandler.WithClientAddr, used
// by `agnt publish serve --tunnel`): when the TCP peer is loopback AND a tunnel
// this process started is the only thing in front of the listener, the
// tunnel's own client-address header is used, because otherwise every viewer
// arrives as 127.0.0.1 and shares one bucket.
//
// The address is reduced by clientRateKey: IPv4 as-is, IPv6 to its /64. A
// per-share aggregate bucket sits behind the per-client one.
package publish

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Sentinel errors from the feedback sink. Callers map these to HTTP status:
// ErrFeedbackRateLimited → 429, the ErrFeedbackInvalid family → 422/400.
var (
	// ErrFeedbackRateLimited signals the (share, IP) token bucket is empty. The
	// request is dropped without persistence and counted in Dropped().
	ErrFeedbackRateLimited = errors.New("publish: feedback rate limited")
	// ErrFeedbackTooLarge is returned when a body exceeds the configured cap.
	ErrFeedbackTooLarge = errors.New("publish: feedback body too large")
	// ErrFeedbackInvalid is returned for a malformed body (bad UTF-8, non-object
	// JSON, unknown field, or an out-of-range field).
	ErrFeedbackInvalid = errors.New("publish: feedback invalid")
	// ErrFeedbackCorrupt is returned by NewFeedbackStore when a persisted record
	// file fails to load or its integrity checksum does not match — the store
	// fails loud rather than silently dropping data (Silent Failure Prohibition).
	ErrFeedbackCorrupt = errors.New("publish: corrupt feedback record")
	// ErrFeedbackCursorUnknown is returned by ReadByShare for a cursor that names
	// no stored row — typically one the retention ring evicted between pages.
	// Restarting silently from the first row would hand the caller duplicates it
	// cannot tell from new rows, so the read fails and the caller restarts.
	ErrFeedbackCursorUnknown = errors.New("publish: feedback cursor does not match a stored row (it may have been evicted); restart from the first page")
)

// feedbackShareAggregateFactor sizes the per-SHARE aggregate bucket as a
// multiple of the per-(share, client) one. The per-client bucket is charged
// first, so one client can draw at most its own rate from the aggregate: it
// takes more than this many distinct clients to exhaust a share, and a flood
// spread over many addresses is bounded per share rather than per address.
const feedbackShareAggregateFactor = 6

// FeedbackLimits are the ingest/retention bounds (mirrors config.FeedbackConfig;
// kept as a plain struct here so package publish stays free of a config import).
type FeedbackLimits struct {
	RatePerMinute   int
	Burst           int
	MaxBodyBytes    int
	MaxRowsPerShare int
	RetentionDays   int
}

// FeedbackRecord is one appended, inert feedback row. The body is the raw
// validated payload bytes; ID and CreatedAt are SERVER-assigned (a client cannot
// forge them). No PII beyond the anonymous body is captured — no raw IP, no
// user-agent, no referrer (spec §7).
type FeedbackRecord struct {
	// ID is a server-assigned CSPRNG id (opaque, unguessable).
	ID string `json:"id"`
	// ShareID is the viewer-safe share this feedback targets.
	ShareID string `json:"share_id"`
	// RevisionDigest is the CONTENT digest of the revision this feedback is keyed
	// to (spec §10: feedback keyed by publish revision content). It is a
	// RevisionDigest, never a minted Share.RevisionID — see revision_identity.go.
	// The JSON key is renamed to revision_digest to match: an on-disk record
	// written under the old revision_id key fails load LOUD (DisallowUnknownFields),
	// which is the correct migration signal for this unreleased feature, never a
	// silent misread.
	RevisionDigest RevisionDigest `json:"revision_digest"`
	// Body is the raw, validated, inert feedback payload (stored verbatim; never
	// reflected).
	Body string `json:"body"`
	// CreatedAt is the SERVER timestamp from the injected clock (never a
	// client-supplied wall-clock value).
	CreatedAt time.Time `json:"created_at"`
}

// shareFeedback is the on-disk container for one share's append-only ring. The
// file-level checksum detects truncation/tampering; a mismatch fails load loud.
type shareFeedback struct {
	ShareID  string           `json:"share_id"`
	Records  []FeedbackRecord `json:"records"`
	Checksum string           `json:"checksum"`
}

// FeedbackStore is the durable, concurrency-safe feedback sink. Safe for
// concurrent use by many public requests.
type FeedbackStore struct {
	dir     string
	now     func() time.Time
	limits  FeedbackLimits
	limiter *tokenBucketLimiter
	// shareLimiter is the per-share aggregate bucket (see
	// feedbackShareAggregateFactor), keyed by share id alone.
	shareLimiter *tokenBucketLimiter
	// readOnly forbids every disk write: no mkdir, no persist, no remove. Set
	// only by OpenFeedbackStoreReadOnly, for reading a store another process owns.
	readOnly bool

	mu             sync.Mutex
	byShare        map[string][]FeedbackRecord
	dropped        int64            // aggregate compatibility metric
	droppedByShare map[string]int64 // owner-scoped observability metric
}

const feedbackFileSuffix = ".feedback.json"

// NewFeedbackStore opens (creating if absent) the durable feedback store rooted
// at dir and loads every persisted share ring. limits is normalized: any
// non-positive field is failed rather than silently unbounded. If now is nil,
// time.Now is used (tests inject a deterministic clock). A corrupt/unreadable/
// checksum-mismatched record file makes NewFeedbackStore return an error wrapping
// ErrFeedbackCorrupt (fail loud; spec §8 / INV-8).
func NewFeedbackStore(dir string, limits FeedbackLimits, now func() time.Time) (*FeedbackStore, error) {
	return openFeedbackStore(dir, limits, now, false)
}

// OpenFeedbackStoreReadOnly opens an EXISTING feedback store for reading only.
// It creates nothing (a missing directory is an error), and retention is applied
// to the rows it returns without being written back: an expired row is never
// served, but the ring file is left exactly as its owner wrote it. Accept and
// AcceptAdmitted return ErrReadOnly.
//
// This is what a second process uses to read the store of a running `agnt
// publish serve`. NewFeedbackStore prunes on load and on read, which makes any
// reader a second writer on the same ring file and the same temp path.
func OpenFeedbackStoreReadOnly(dir string, limits FeedbackLimits, now func() time.Time) (*FeedbackStore, error) {
	return openFeedbackStore(dir, limits, now, true)
}

func openFeedbackStore(dir string, limits FeedbackLimits, now func() time.Time, readOnly bool) (*FeedbackStore, error) {
	if dir == "" {
		return nil, errors.New("publish: empty feedback dir")
	}
	if limits.RatePerMinute <= 0 || limits.Burst <= 0 || limits.MaxBodyBytes <= 0 ||
		limits.MaxRowsPerShare <= 0 || limits.RetentionDays <= 0 {
		return nil, fmt.Errorf("publish: invalid feedback limits: %+v", limits)
	}
	if now == nil {
		now = time.Now
	}
	if !readOnly {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("publish: create feedback dir: %w", err)
		}
	}
	s := &FeedbackStore{
		dir:     dir,
		now:     now,
		limits:  limits,
		limiter: newTokenBucketLimiter(limits.RatePerMinute, limits.Burst, now),
		shareLimiter: newTokenBucketLimiter(
			limits.RatePerMinute*feedbackShareAggregateFactor,
			limits.Burst*feedbackShareAggregateFactor, now),
		readOnly:       readOnly,
		byShare:        make(map[string][]FeedbackRecord),
		droppedByShare: make(map[string]int64),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads and integrity-checks every share ring, then applies retention to
// it. Fails loud on the first corrupt file.
//
// Retention runs here because Accept is not a reliable trigger: a share that
// stops receiving feedback would otherwise keep its expired rows on disk and
// readable forever.
func (s *FeedbackStore) load() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("publish: read feedback dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, feedbackFileSuffix) {
			continue
		}
		path := filepath.Join(s.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%w: read %s: %v", ErrFeedbackCorrupt, name, err)
		}
		sf, err := decodeShareFeedback(data)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrFeedbackCorrupt, name, err)
		}
		// The file name is derived from the share id, so a record whose id does
		// not hash to its own file name was copied or edited in place. Loading it
		// would file one share's rows under another id — and expire would then
		// remove a DIFFERENT file than the one read.
		if want := feedbackFileName(sf.ShareID); name != want {
			return fmt.Errorf("%w: %s: record names share %q, whose file is %s", ErrFeedbackCorrupt, name, sf.ShareID, want)
		}
		s.byShare[sf.ShareID] = sf.Records
		if err := s.expire(sf.ShareID); err != nil {
			return err
		}
	}
	return nil
}

// expire drops a share's rows that have aged past RetentionDays or overflow
// MaxRowsPerShare, and makes the drop durable (a read-only store prunes its
// in-memory view only). The row cap is applied here as well as on Accept so a
// lowered max-rows-per-share takes effect on a share nobody is posting to. It is called on every path that exposes stored rows (load,
// ReadByShare, Count), so retention holds for a share nobody is posting to.
// Caller holds the lock, or is the single-threaded load.
//
// The in-memory ring is pruned even when the durable write fails, so an expired
// row is never served; the error is returned so the caller can surface that the
// on-disk copy still holds it.
func (s *FeedbackStore) expire(shareID string) error {
	records := s.byShare[shareID]
	cutoff := s.now().Add(-time.Duration(s.limits.RetentionDays) * 24 * time.Hour)
	// Rows are kept oldest first, so a fresh head means nothing has expired.
	if len(records) == 0 || (!records[0].CreatedAt.Before(cutoff) && len(records) <= s.limits.MaxRowsPerShare) {
		return nil
	}
	kept := s.prune(records)
	if s.readOnly {
		// Never touch the owner's file: prune the view, leave the disk alone.
		if len(kept) == 0 {
			delete(s.byShare, shareID)
		} else {
			s.byShare[shareID] = kept
		}
		return nil
	}
	if len(kept) == 0 {
		delete(s.byShare, shareID)
		err := os.Remove(filepath.Join(s.dir, feedbackFileName(shareID)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("publish: remove expired feedback: %w", err)
		}
		return nil
	}
	s.byShare[shareID] = kept
	return s.persist(shareID, kept)
}

// decodeShareFeedback strictly decodes and integrity-checks a ring file.
func decodeShareFeedback(data []byte) (*shareFeedback, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var sf shareFeedback
	if err := dec.Decode(&sf); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("trailing data")
	}
	if sf.ShareID == "" {
		return nil, errors.New("missing share id")
	}
	if err := sf.verifyChecksum(); err != nil {
		return nil, err
	}
	return &sf, nil
}

// feedbackPayload is the accepted feedback schema (spec §7 / P4 feedback-client).
// Deny-by-default: DisallowUnknownFields rejects anything outside these keys, so
// a control-shaped or exfil payload is rejected at ingest.
type feedbackPayload struct {
	Message string `json:"message"`
	Rating  *int   `json:"rating,omitempty"`
	StepID  string `json:"stepId,omitempty"`
	At      *int64 `json:"at,omitempty"`
}

// Accept validates, rate-limits, and durably appends one anonymous feedback row.
// It is the FeedbackSink the public handler wires in. remoteAddr is the client
// address the wiring layer resolved (host or host:port) — never a header this
// store read itself (see package doc). Order: rate-limit first (bounds abuse
// work), then strict validation, then atomic append + retention prune.
//
// It is Admit followed by AcceptAdmitted. A caller that can run the two apart
// (the public handler does) calls Admit BEFORE reading the request body, so a
// throttled client costs no body read at all.
func (s *FeedbackStore) Accept(shareID string, revisionDigest RevisionDigest, remoteAddr string, body []byte) error {
	if err := s.Admit(shareID, remoteAddr); err != nil {
		return err
	}
	return s.AcceptAdmitted(shareID, revisionDigest, body)
}

// Admit charges one feedback submission against the (share, client) bucket and
// then the per-share aggregate bucket, and returns ErrFeedbackRateLimited when
// either is empty. A refusal is counted in Dropped/DroppedByShare. It reads no
// body and touches no disk, so it is safe to call before the body is read.
func (s *FeedbackStore) Admit(shareID, remoteAddr string) error {
	if shareID == "" {
		return fmt.Errorf("%w: empty share id", ErrFeedbackInvalid)
	}
	// Per-client first: a single flooding client is refused by its own bucket
	// and so cannot drain the share's aggregate budget for everyone else.
	if !s.limiter.allow(rateKey(shareID, remoteAddr)) || !s.shareLimiter.allow(shareID) {
		s.mu.Lock()
		s.dropped++
		s.droppedByShare[shareID]++
		s.mu.Unlock()
		return ErrFeedbackRateLimited
	}
	return nil
}

// AcceptAdmitted validates and durably appends one feedback row WITHOUT
// charging the rate limiter — the caller already did, through Admit. Calling it
// without a prior Admit bypasses the limiter; use Accept unless the two steps
// genuinely have to be separated.
func (s *FeedbackStore) AcceptAdmitted(shareID string, revisionDigest RevisionDigest, body []byte) error {
	if shareID == "" {
		return fmt.Errorf("%w: empty share id", ErrFeedbackInvalid)
	}
	if s.readOnly {
		return ErrReadOnly
	}

	if err := s.validate(body); err != nil {
		return err
	}

	id, err := mintFeedbackID()
	if err != nil {
		// CSPRNG failed: fail the write loud rather than storing a zeroed,
		// collision-prone id (spec §8 / Silent Failure Prohibition).
		return fmt.Errorf("%w: mint id: %v", ErrFeedbackInvalid, err)
	}
	rec := FeedbackRecord{
		ID:             id,
		ShareID:        shareID,
		RevisionDigest: revisionDigest,
		Body:           string(body),
		CreatedAt:      s.now(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	records := append(s.byShare[shareID], rec)
	records = s.prune(records)
	if err := s.persist(shareID, records); err != nil {
		return err
	}
	s.byShare[shareID] = records
	return nil
}

// validate enforces the ingest contract: size cap, valid UTF-8, and strict
// schema (object, known keys only, bounded fields). Never truncates — rejects.
func (s *FeedbackStore) validate(body []byte) error {
	if len(body) > s.limits.MaxBodyBytes {
		return fmt.Errorf("%w: %d > %d", ErrFeedbackTooLarge, len(body), s.limits.MaxBodyBytes)
	}
	if !utf8.Valid(body) {
		return fmt.Errorf("%w: invalid utf-8", ErrFeedbackInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var p feedbackPayload
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("%w: %v", ErrFeedbackInvalid, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data", ErrFeedbackInvalid)
	}
	if len(p.Message) > s.limits.MaxBodyBytes {
		return fmt.Errorf("%w: message too long", ErrFeedbackInvalid)
	}
	// A row must say something: a message, or at least a rating. "{}" and a
	// whitespace-only message are valid JSON carrying nothing, and each one
	// accepted evicts a real row from the retention ring.
	if strings.TrimSpace(p.Message) == "" && p.Rating == nil {
		return fmt.Errorf("%w: empty feedback (a message or a rating is required)", ErrFeedbackInvalid)
	}
	if p.Rating != nil && (*p.Rating < 0 || *p.Rating > 5) {
		return fmt.Errorf("%w: rating out of range", ErrFeedbackInvalid)
	}
	if len(p.StepID) > MaxIDLength {
		return fmt.Errorf("%w: stepId too long", ErrFeedbackInvalid)
	}
	return nil
}

// prune enforces the retention ring (spec §5): drop rows older than RetentionDays
// (age), then cap to the newest MaxRowsPerShare (count). Records are kept in
// append order (oldest first), so eviction removes from the front — append-only
// safe (prior records are never mutated, only aged-out).
func (s *FeedbackStore) prune(records []FeedbackRecord) []FeedbackRecord {
	cutoff := s.now().Add(-time.Duration(s.limits.RetentionDays) * 24 * time.Hour)
	i := 0
	for i < len(records) && records[i].CreatedAt.Before(cutoff) {
		i++
	}
	records = records[i:]
	if len(records) > s.limits.MaxRowsPerShare {
		records = records[len(records)-s.limits.MaxRowsPerShare:]
	}
	// Copy so the returned slice does not alias a shrunk backing array whose head
	// still references evicted (potentially sensitive) rows.
	out := make([]FeedbackRecord, len(records))
	copy(out, records)
	return out
}

// ReadByShare returns feedback rows for ONE share in append order, paginated by
// an opaque cursor. This is the storage read API P9's control-plane exposure
// (`publish feedback list`) calls.
//
// It is deliberately share-scoped, NOT revision/digest-scoped. Digest is a pure
// content hash, so two different projects that publish the same walkthrough
// content collide on the same digest; a digest-keyed read would union their
// feedback and leak project A's rows to project B's owner (breaks INV-1 /
// spec §2a). A share's feedback lives only in that share's own ring file, so a
// share-scoped read structurally cannot cross a project boundary — the caller's
// ownership gate on the share id is sufficient.
//
// cursor is the ID of the last row a prior call returned (empty = start);
// nextCursor is the last returned row's ID, or "" when the page reached the
// end. limit <= 0 defaults to all remaining rows. A cursor that names no
// stored row — the ring evicted it between pages — returns
// ErrFeedbackCursorUnknown rather than restarting silently from the first row.
//
// The rows are returned RAW/inert (INV-7). The caller (P9) is responsible for
// escaping them before rendering into any HTML surface — the store never
// reflects.
func (s *FeedbackStore) ReadByShare(shareID, cursor string, limit int) (rows []FeedbackRecord, nextCursor string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Retention applies to reads too: an expired row is not returned just
	// because no new feedback arrived to evict it.
	if err := s.expire(shareID); err != nil {
		return nil, "", err
	}
	recs := s.byShare[shareID]
	all := make([]FeedbackRecord, len(recs))
	copy(all, recs)
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	start := 0
	if cursor != "" {
		found := false
		for i := range all {
			if all[i].ID == cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, "", ErrFeedbackCursorUnknown
		}
	}
	end := len(all)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	page := all[start:end]
	rows = make([]FeedbackRecord, len(page))
	copy(rows, page)
	if len(rows) > 0 && end < len(all) {
		nextCursor = rows[len(rows)-1].ID
	}
	return rows, nextCursor, nil
}

// Dropped returns the count of rate-limit-dropped feedback posts — the observable
// signal that the limiter is shedding abuse (spec §5 "429, not a crash").
func (s *FeedbackStore) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// DroppedByShare returns the rate-limit-dropped count for one share. Control
// reads and arrival events use this owner-scoped metric so activity against one
// project's share cannot bleed into another project's observability.
func (s *FeedbackStore) DroppedByShare(shareID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.droppedByShare[shareID]
}

// Count returns the number of stored, unexpired rows for a share.
func (s *FeedbackStore) Count(shareID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A failed durable write still leaves the in-memory ring pruned, which is
	// what this count reports; ReadByShare returns the error.
	_ = s.expire(shareID)
	return len(s.byShare[shareID])
}

// persist atomically writes a share's ring to its file with 0600 perms. Caller
// holds the lock.
func (s *FeedbackStore) persist(shareID string, records []FeedbackRecord) error {
	if s.readOnly {
		return ErrReadOnly
	}
	sf := shareFeedback{ShareID: shareID, Records: records}
	sf.Checksum = sf.computeChecksum()
	data, err := json.MarshalIndent(&sf, "", "  ")
	if err != nil {
		return fmt.Errorf("publish: marshal feedback: %w", err)
	}
	path := filepath.Join(s.dir, feedbackFileName(shareID))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("publish: write temp feedback: %w", err)
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publish: chmod temp feedback: %w", err)
	}
	if err := fsyncFile(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publish: fsync temp feedback: %w", err)
	}
	if err := renameDurable(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("publish: persist feedback: %w", err)
	}
	return nil
}

// feedbackFileName maps a share id to a filesystem-safe record filename. Share
// ids are base64url (already path-safe) but a hex sha256 makes the mapping
// total and collision-free for any input.
func feedbackFileName(shareID string) string {
	sum := sha256.Sum256([]byte(shareID))
	return hex.EncodeToString(sum[:]) + feedbackFileSuffix
}

// computeChecksum is a deterministic integrity checksum over the ring with
// Checksum blanked (encoding/json sorts map keys, arrays keep order).
func (sf *shareFeedback) computeChecksum() string {
	c := *sf
	c.Checksum = ""
	b, _ := json.Marshal(&c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (sf *shareFeedback) verifyChecksum() error {
	if sf.Checksum == "" {
		return errors.New("missing checksum")
	}
	if sf.Checksum != sf.computeChecksum() {
		return errors.New("checksum mismatch")
	}
	return nil
}

// rateKey composes the limiter key from the share id and the client address.
func rateKey(shareID, remoteAddr string) string {
	return shareID + "\x00" + clientRateKey(remoteAddr)
}

// clientRateKey reduces a client address (host or host:port) to the unit one
// client is rate-limited as:
//
//   - IPv4 (including IPv4-mapped IPv6): the address.
//   - IPv6: its /64. A single subscriber is routinely delegated a whole /64 and
//     can rotate through 2^64 source addresses, so keying on the /128 would
//     hand every rotation a fresh full bucket.
//   - anything unparseable: used verbatim, so it still partitions rather than
//     collapsing every such client into one shared bucket.
func clientRateKey(remoteAddr string) string {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

// mintFeedbackID returns a server-assigned CSPRNG id. A crypto/rand failure is
// astronomically unlikely, but if it happens the error is propagated so the
// create fails loud rather than persisting a zeroed, colliding id.
func mintFeedbackID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

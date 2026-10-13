package publish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests for the publish / public-plane review findings. Each test
// asserts the control's own verdict (the sentinel or the message it names), and
// carries a near-miss that must still be accepted, so a test cannot pass merely
// because the call failed for some other reason.

// --- op validation ---------------------------------------------------------

func TestSignature_EveryFieldIsLengthPrefixed(t *testing.T) {
	// With a bare separator these two share a key, and the second op of a
	// variant is refused as a duplicate of the first.
	a := Op{Op: OpSetAttribute, Selector: ".x", Name: "a|b", Value: "c"}
	b := Op{Op: OpSetAttribute, Selector: ".x", Name: "a", Value: "b|c"}
	if a.signature() == b.signature() {
		t.Fatalf("distinct ops share a signature: %q", a.signature())
	}
	// Near miss: the same op twice is still a duplicate.
	c := a
	if a.signature() != c.signature() {
		t.Fatal("identical ops must share a signature")
	}
	// A value shaped like the encoding itself must not forge a field boundary.
	d := Op{Op: OpSetText, Selector: ".x", Value: "1:y|"}
	e := Op{Op: OpSetText, Selector: ".x1:y|"}
	if d.signature() == e.signature() {
		t.Fatal("a value spelled like a length prefix collided with a neighbouring field")
	}
}

func TestSetAttribute_DataAndAriaNamesAreValidated(t *testing.T) {
	op := func(name string) *Op {
		return &Op{Op: OpSetAttribute, Selector: ".x", Name: name, Value: "v"}
	}
	for _, ok := range []string{"data-x", "aria-label", "DATA-Foo", "data-a.b_c-d", "data-" + strings.Repeat("a", MaxAttrNameLength-len("data-"))} {
		if err := op(ok).Validate(); err != nil {
			t.Errorf("name %q must be accepted, got %v", ok, err)
		}
	}
	for _, bad := range []string{
		"data-",           // empty tail
		"aria-",           // empty tail
		"data-x y",        // setAttribute throws on a space
		"data-x\"onclick", // quote
		"data-x=1",        // equals
		"data-x>",         // angle
		" data-x",         // leading space: trimmed prefix must not launder it
		"data-x ",         // trailing space
		"data-é",          // non-ASCII tail
		"data-" + strings.Repeat("a", MaxAttrNameLength), // over the length cap
	} {
		err := op(bad).Validate()
		if err == nil || !strings.Contains(err.Error(), "not on allowlist") {
			t.Errorf("name %q must be refused by the allowlist, got %v", bad, err)
		}
	}
}

func TestAdvanceWait_ElementKindsRequireASelector(t *testing.T) {
	for _, when := range []string{"element-present", "element-visible"} {
		a := Advance{Type: "wait", When: when}
		err := a.Validate()
		if err == nil || !strings.Contains(err.Error(), "requires a selector") {
			t.Errorf("wait %s with no value must be refused for the missing selector, got %v", when, err)
		}
		// Near miss: the same directive with a selector is fine.
		a.Value = ".ready"
		if err := a.Validate(); err != nil {
			t.Errorf("wait %s with a selector must validate, got %v", when, err)
		}
	}
	// url-contains keeps its own (already enforced) non-empty rule.
	if err := (&Advance{Type: "wait", When: "url-contains", Value: "/done"}).Validate(); err != nil {
		t.Fatalf("url-contains regressed: %v", err)
	}
}

func variantWithOps(n int) Variant {
	v := Variant{ID: "a"}
	for i := 0; i < n; i++ {
		v.Ops = append(v.Ops, Op{Op: OpAddClass, Selector: fmt.Sprintf(".c%d", i), Value: "on"})
	}
	return v
}

func TestVariant_OpsPerVariantCap(t *testing.T) {
	atCap := variantWithOps(MaxOpsPerVariant)
	if err := atCap.Validate(); err != nil {
		t.Fatalf("%d ops is at the cap and must validate: %v", MaxOpsPerVariant, err)
	}
	over := variantWithOps(MaxOpsPerVariant + 1)
	err := over.Validate()
	if err == nil || !strings.Contains(err.Error(), "ops exceeds max") {
		t.Fatalf("%d ops must be refused by the op-count cap, got %v", MaxOpsPerVariant+1, err)
	}
}

func TestPublishedWalkthrough_ArtifactSizeCap(t *testing.T) {
	big := func(variants int) *PublishedWalkthrough {
		pw := validWalkthrough("wt")
		vs := &VariantSet{Version: SchemaV1, ID: "set"}
		for v := 0; v < variants; v++ {
			variant := Variant{ID: fmt.Sprintf("v%d", v)}
			for i := 0; i < 20; i++ {
				variant.Ops = append(variant.Ops, Op{
					Op: OpSetHTML, Selector: fmt.Sprintf(".c%d", i), HTML: strings.Repeat("x", MaxRawHTMLBytes),
				})
			}
			vs.Variants = append(vs.Variants, variant)
		}
		pw.VariantSet = vs
		return pw
	}
	// Near miss: every field individually legal, total under the cap.
	small := big(2)
	if enc, _ := CanonicalJSON(small); len(enc) >= MaxArtifactBytes {
		t.Fatalf("premise broken: the small artifact is %d bytes, not under the %d cap", len(enc), MaxArtifactBytes)
	}
	if err := small.Validate(); err != nil {
		t.Fatalf("an artifact under the cap must validate: %v", err)
	}
	// Every field still individually legal, but 12 x 20 x 8 KiB in aggregate.
	huge := big(MaxVariantsPerSet)
	err := huge.Validate()
	if err == nil || !strings.Contains(err.Error(), "exceeds max") || !strings.Contains(err.Error(), "artifact is") {
		t.Fatalf("an oversize artifact must be refused by the whole-artifact cap, got %v", err)
	}
}

func TestPublishedWalkthrough_TitleControlCharacter(t *testing.T) {
	pw := validWalkthrough("wt")
	pw.Title = "Check\x00out"
	err := pw.Validate()
	if err == nil || !strings.Contains(err.Error(), "title has control character") {
		t.Fatalf("a control character in the title must be refused, got %v", err)
	}
	pw.Title = "Checkout — tour"
	if err := pw.Validate(); err != nil {
		t.Fatalf("an ordinary title must validate: %v", err)
	}
}

// --- feedback ring ----------------------------------------------------------

func TestFeedback_EmptySubmissionRefused(t *testing.T) {
	s, _ := newTestStore(t, testLimits(), newFakeClock())
	for i, body := range []string{`{}`, `{"message":""}`, `{"message":"   \n\t"}`, `{"at":5}`, `{"stepId":"s1"}`} {
		// A distinct client per row keeps the rate limiter out of the verdict.
		err := s.Accept("share-1", "rev", fmt.Sprintf("10.0.0.%d:1", i+1), []byte(body))
		if !errors.Is(err, ErrFeedbackInvalid) || !strings.Contains(err.Error(), "empty feedback") {
			t.Errorf("body %s must be refused as empty feedback, got %v", body, err)
		}
	}
	if got := s.Count("share-1"); got != 0 {
		t.Fatalf("an empty submission was stored: Count = %d", got)
	}
	// Near misses: a message alone, and a rating alone, both say something.
	if err := s.Accept("share-1", "rev", "10.0.1.1:1", []byte(`{"message":"hi"}`)); err != nil {
		t.Fatalf("a message must be accepted: %v", err)
	}
	if err := s.Accept("share-1", "rev", "10.0.1.2:1", []byte(`{"rating":0}`)); err != nil {
		t.Fatalf("a rating alone must be accepted: %v", err)
	}
}

func TestFeedback_UnknownCursorIsAnError(t *testing.T) {
	limits := testLimits()
	limits.MaxRowsPerShare = 2
	limits.Burst = 10
	s, _ := newTestStore(t, limits, newFakeClock())
	post := func(msg string) {
		t.Helper()
		if err := s.Accept("share-1", "rev", "1.2.3.4:1", []byte(`{"message":"`+msg+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	post("one")
	post("two")
	page, cursor, err := s.ReadByShare("share-1", "", 1)
	if err != nil || len(page) != 1 || cursor == "" {
		t.Fatalf("first page: rows=%d cursor=%q err=%v", len(page), cursor, err)
	}
	// Two more rows evict the row the cursor names.
	post("three")
	post("four")
	rows, next, err := s.ReadByShare("share-1", cursor, 1)
	if !errors.Is(err, ErrFeedbackCursorUnknown) {
		t.Fatalf("an evicted cursor must be ErrFeedbackCursorUnknown, got rows=%d next=%q err=%v", len(rows), next, err)
	}
	if len(rows) != 0 {
		t.Fatalf("an unknown cursor must return no rows (it used to restart at row 0), got %d", len(rows))
	}
	// Near miss: a cursor that still names a row keeps paging.
	page, cursor, err = s.ReadByShare("share-1", "", 1)
	if err != nil || len(page) != 1 {
		t.Fatalf("restart: %v", err)
	}
	if rows, _, err := s.ReadByShare("share-1", cursor, 1); err != nil || len(rows) != 1 {
		t.Fatalf("a live cursor must page on: rows=%d err=%v", len(rows), err)
	}
}

func TestFeedback_RowCapAppliesWithoutANewSubmission(t *testing.T) {
	clk := newFakeClock()
	limits := testLimits()
	limits.MaxRowsPerShare = 5
	limits.Burst = 10
	s, dir := newTestStore(t, limits, clk)
	for i := 0; i < 5; i++ {
		if err := s.Accept("share-1", "rev", "1.2.3.4:1", []byte(fmt.Sprintf(`{"message":"m%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	// The operator lowers max-rows-per-share; nobody posts again.
	limits.MaxRowsPerShare = 2
	reopened, err := NewFeedbackStore(dir, limits, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := reopened.ReadByShare("share-1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// The fake clock stamps every row identically, so ReadByShare's order
	// within the page is by id; assert membership, not position.
	bodies := ""
	for _, r := range rows {
		bodies += r.Body
	}
	if len(rows) != 2 || !strings.Contains(bodies, "m3") || !strings.Contains(bodies, "m4") {
		t.Fatalf("want the newest 2 rows (m3, m4) after the cap was lowered, got %+v", rows)
	}
	data, err := os.ReadFile(filepath.Join(dir, feedbackFileName("share-1")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "m0") {
		t.Fatal("the evicted rows are still on disk")
	}
}

func TestFeedback_RingFileMustMatchItsShareID(t *testing.T) {
	clk := newFakeClock()
	s, dir := newTestStore(t, testLimits(), clk)
	if err := s.Accept("share-1", "rev", "1.2.3.4:1", []byte(`{"message":"mine"}`)); err != nil {
		t.Fatal(err)
	}
	// Premise: the untouched store reloads.
	if _, err := NewFeedbackStore(dir, testLimits(), clk.now); err != nil {
		t.Fatalf("premise broken: a clean store must reload: %v", err)
	}
	// A valid, checksummed ring copied under another share's file name.
	data, err := os.ReadFile(filepath.Join(dir, feedbackFileName("share-1")))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, feedbackFileName("share-2")), data, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = NewFeedbackStore(dir, testLimits(), clk.now)
	if !errors.Is(err, ErrFeedbackCorrupt) || !strings.Contains(err.Error(), "whose file is") {
		t.Fatalf("a ring under the wrong file name must fail load as corrupt, got %v", err)
	}
}

// --- read-only opens ---------------------------------------------------------

func TestFeedbackReadOnly_NeverWritesAndNeverCreates(t *testing.T) {
	clk := newFakeClock()
	limits := testLimits()
	limits.RetentionDays = 30
	limits.Burst = 10
	owner, dir := newTestStore(t, limits, clk)
	if err := owner.Accept("share-1", "rev", "1.2.3.4:1", []byte(`{"message":"old"}`)); err != nil {
		t.Fatal(err)
	}
	clk.advance(20 * 24 * time.Hour)
	if err := owner.Accept("share-1", "rev", "1.2.3.4:1", []byte(`{"message":"newer"}`)); err != nil {
		t.Fatal(err)
	}
	clk.advance(11 * 24 * time.Hour) // "old" is now 31 days old: expired.

	path := filepath.Join(dir, feedbackFileName("share-1"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte("old")) {
		t.Fatal("premise broken: the expired row must still be on disk before the reader opens the store")
	}

	ro, err := OpenFeedbackStoreReadOnly(dir, limits, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := ro.ReadByShare("share-1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Body, "newer") {
		t.Fatalf("retention must still apply to what a reader is shown, got %+v", rows)
	}
	if got := ro.Count("share-1"); got != 1 {
		t.Fatalf("Count = %d, want 1", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the reader removed or broke the owner's ring file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a read-only open rewrote the owner's ring file")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a read-only open left extra files behind: %v", entries)
	}
	// Writes are refused by the read-only control specifically.
	if err := ro.AcceptAdmitted("share-1", "rev", []byte(`{"message":"x"}`)); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("AcceptAdmitted on a read-only store must be ErrReadOnly, got %v", err)
	}
	if err := ro.Accept("share-1", "rev", "9.9.9.9:1", []byte(`{"message":"x"}`)); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Accept on a read-only store must be ErrReadOnly, got %v", err)
	}

	// Fully expired: the writable store removes the file; the reader must not.
	clk.advance(40 * 24 * time.Hour)
	ro2, err := OpenFeedbackStoreReadOnly(dir, limits, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	if got := ro2.Count("share-1"); got != 0 {
		t.Fatalf("Count = %d, want 0 once everything expired", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a read-only open removed a fully expired ring file: %v", err)
	}

	// A store that does not exist is an error, not a directory to create.
	missing := filepath.Join(t.TempDir(), "nope", "feedback")
	if _, err := OpenFeedbackStoreReadOnly(missing, limits, clk.now); err == nil {
		t.Fatal("opening a missing store read-only must fail")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("a read-only open created %s (stat err = %v)", missing, err)
	}
}

func TestShareStoreReadOnly_NeverWritesAndNeverCreates(t *testing.T) {
	owner, dir := newStore(t)
	id, _, err := owner.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+recordSuffix)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ro.List("/proj"); len(got) != 1 || got[0].ID != id {
		t.Fatalf("a read-only store must still list its shares, got %+v", got)
	}
	if err := ro.Revoke(id); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Revoke on a read-only store must be ErrReadOnly, got %v", err)
	}
	if _, err := ro.Rotate(id); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Rotate on a read-only store must be ErrReadOnly, got %v", err)
	}
	if _, _, err := ro.Create(validWalkthrough("wt2"), "/proj"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Create on a read-only store must be ErrReadOnly, got %v", err)
	}
	// The refused revoke must not have taken effect in memory either.
	if info, _ := ro.Status(id); info.Revoked {
		t.Fatal("a refused revoke still tombstoned the share in memory")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("a read-only store changed the record on disk (err = %v)", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a read-only store left extra files behind: %v", entries)
	}

	missing := filepath.Join(t.TempDir(), "nope", "shares")
	if _, err := OpenReadOnly(missing, nil); err == nil {
		t.Fatal("opening a missing store read-only must fail")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("a read-only open created %s (stat err = %v)", missing, err)
	}
}

// --- rate-limit identity -----------------------------------------------------

func TestClientRateKey(t *testing.T) {
	same := func(a, b string) {
		t.Helper()
		if clientRateKey(a) != clientRateKey(b) {
			t.Errorf("%q and %q must share a bucket: %q vs %q", a, b, clientRateKey(a), clientRateKey(b))
		}
	}
	differ := func(a, b string) {
		t.Helper()
		if clientRateKey(a) == clientRateKey(b) {
			t.Errorf("%q and %q must NOT share a bucket (both %q)", a, b, clientRateKey(a))
		}
	}
	// One /64 is one subscriber, whatever the interface id and port.
	same("[2001:db8:1:2::1]:443", "[2001:db8:1:2:ffff:ffff:ffff:ffff]:8080")
	same("2001:db8:1:2::1", "[2001:db8:1:2::2]:1")
	// The neighbouring /64 is someone else.
	differ("[2001:db8:1:2::1]:443", "[2001:db8:1:3::1]:443")
	// IPv4 stays per address; the port never matters.
	same("203.0.113.7:1000", "203.0.113.7:2000")
	differ("203.0.113.7:1000", "203.0.113.8:1000")
	// An IPv4-mapped address is the IPv4 client, not a member of ::ffff:0:0/64.
	same("[::ffff:203.0.113.7]:1", "203.0.113.7:2")
	differ("[::ffff:203.0.113.7]:1", "[::ffff:203.0.113.8]:1")
	// A zone does not split one client into several.
	same("[fe80::1%eth0]:1", "[fe80::1%eth1]:1")
	// Unparseable values still partition rather than collapsing together.
	differ("not-an-address", "also-not-an-address")

	if got, want := clientRateKey("[2001:db8:1:2::1]:443"), "2001:db8:1:2::/64"; got != want {
		t.Errorf("IPv6 key = %q, want %q", got, want)
	}
	if got, want := clientRateKey("203.0.113.7:1000"), "203.0.113.7"; got != want {
		t.Errorf("IPv4 key = %q, want %q", got, want)
	}
}

func TestFeedback_IPv6RotationWithinA64SharesOneBucket(t *testing.T) {
	limits := testLimits()
	limits.Burst = 3
	limits.RatePerMinute = 1
	s, _ := newTestStore(t, limits, newFakeClock())
	admitted := 0
	for i := 0; i < 20; i++ {
		// A fresh source address every request, all inside one /64.
		if err := s.Admit("share-1", fmt.Sprintf("[2001:db8:1:2::%x]:443", i+1)); err == nil {
			admitted++
		} else if !errors.Is(err, ErrFeedbackRateLimited) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if admitted != limits.Burst {
		t.Fatalf("rotating through one /64 admitted %d submissions, want exactly the burst of %d", admitted, limits.Burst)
	}
	// Near miss: a different /64 has its own, untouched bucket.
	if err := s.Admit("share-1", "[2001:db8:1:3::1]:443"); err != nil {
		t.Fatalf("a client in another /64 must not be throttled by this one: %v", err)
	}
}

func TestFeedback_PerShareAggregateBucket(t *testing.T) {
	limits := testLimits()
	limits.Burst = 2
	limits.RatePerMinute = 1
	s, _ := newTestStore(t, limits, newFakeClock())
	aggregate := limits.Burst * feedbackShareAggregateFactor

	// One client alone exhausts only its own bucket...
	for i := 0; i < limits.Burst; i++ {
		if err := s.Admit("share-1", "198.51.100.1:1"); err != nil {
			t.Fatalf("burst submission %d refused: %v", i, err)
		}
	}
	if err := s.Admit("share-1", "198.51.100.1:1"); !errors.Is(err, ErrFeedbackRateLimited) {
		t.Fatalf("the client's own bucket must refuse past its burst, got %v", err)
	}
	// ...and hammering on does not drain the share for anyone else: the
	// per-client refusal comes first and never touches the aggregate.
	for i := 0; i < 10*aggregate; i++ {
		_ = s.Admit("share-1", "198.51.100.1:1")
	}
	if err := s.Admit("share-1", "198.51.100.2:1"); err != nil {
		t.Fatalf("a single flooding client starved the share for a second viewer: %v", err)
	}

	// Many distinct clients are bounded by the aggregate. Three submissions are
	// already charged to it (two from .1, one from .2).
	admitted := 3
	var refusedAt string
	for i := 0; i < 4*aggregate && refusedAt == ""; i++ {
		addr := fmt.Sprintf("203.0.113.%d:1", i+1)
		if err := s.Admit("share-1", addr); err != nil {
			if !errors.Is(err, ErrFeedbackRateLimited) {
				t.Fatalf("unexpected error: %v", err)
			}
			refusedAt = addr
			break
		}
		admitted++
	}
	if refusedAt == "" || admitted != aggregate {
		t.Fatalf("the share admitted %d submissions from distinct clients, want exactly the aggregate burst of %d", admitted, aggregate)
	}
	// Provenance: the refused client's OWN bucket was fresh — it is admitted on
	// another share — so the refusal above was the aggregate bucket's.
	if err := s.Admit("share-2", "203.0.113.250:1"); err != nil {
		t.Fatalf("another share must have its own aggregate budget: %v", err)
	}
	if got := s.DroppedByShare("share-2"); got != 0 {
		t.Fatalf("share-2 dropped = %d, want 0", got)
	}
}

func TestFeedback_AdmitThenAcceptAdmittedChargesOnce(t *testing.T) {
	limits := testLimits()
	limits.Burst = 2
	limits.RatePerMinute = 1
	s, _ := newTestStore(t, limits, newFakeClock())
	for i := 0; i < limits.Burst; i++ {
		if err := s.Admit("share-1", "1.2.3.4:1"); err != nil {
			t.Fatalf("Admit %d: %v", i, err)
		}
		if err := s.AcceptAdmitted("share-1", "rev", []byte(`{"message":"ok"}`)); err != nil {
			t.Fatalf("AcceptAdmitted %d: %v", i, err)
		}
	}
	if got := s.Count("share-1"); got != limits.Burst {
		t.Fatalf("Count = %d, want %d — AcceptAdmitted must not charge the limiter a second time", got, limits.Burst)
	}
	if err := s.Admit("share-1", "1.2.3.4:1"); !errors.Is(err, ErrFeedbackRateLimited) {
		t.Fatalf("the third Admit must be rate limited, got %v", err)
	}
	if got := s.DroppedByShare("share-1"); got != 1 {
		t.Fatalf("a refused Admit must be counted: dropped = %d, want 1", got)
	}
}

// --- revision binding --------------------------------------------------------

func TestShareDigest_TracksTheCurrentRevision(t *testing.T) {
	s, _ := newStore(t)
	id, _, _, err := s.PublishFile(fileWalkthrough("wt", "first"), "/proj", "a.json")
	if err != nil {
		t.Fatal(err)
	}
	first, ok := s.ShareDigest(id)
	if !ok || first == "" {
		t.Fatalf("a live share must report a digest, got %q ok=%v", first, ok)
	}
	if _, _, _, err := s.PublishFile(fileWalkthrough("wt", "second"), "/proj", "a.json"); err != nil {
		t.Fatal(err)
	}
	second, ok := s.ShareDigest(id)
	if !ok || second == first {
		t.Fatalf("an edit must change the digest references are bound to: %q -> %q", first, second)
	}
	if _, ok := s.ShareDigest("no-such-share"); ok {
		t.Fatal("an unknown share must report ok=false")
	}
	if err := s.Revoke(id); err != nil {
		t.Fatal(err)
	}
	if d, ok := s.ShareDigest(id); ok || d != "" {
		t.Fatalf("a revoked share must report no digest, got %q ok=%v", d, ok)
	}
}

// --- INV-14 at load -----------------------------------------------------------

// writeStoredShare writes a share record to dir exactly as Store.persist would:
// a valid checksum over the record, in a file named for its id. It is how a
// record from an older build — one no current publish path can produce — gets
// onto disk for a load test.
func writeStoredShare(t *testing.T, dir string, sh Share) string {
	t.Helper()
	sh.Checksum = sh.computeChecksum()
	data, err := json.MarshalIndent(&sh, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sh.ID+recordSuffix)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// legacyShare is a live share as a pre-2026-08-18 build stored it. withScript
// adds the addScript op that build still accepted.
func legacyShare(id, token string, withScript bool) Share {
	pw := validWalkthrough("wt-" + id)
	ops := []Op{{Op: OpSetText, Selector: ".x", Value: "hi"}}
	if withScript {
		ops = append(ops, Op{Op: OpAddScript, Code: "document.getElementById('agnt-demo-indicator').remove()"})
	}
	pw.VariantSet = &VariantSet{Version: SchemaV1, ID: "vs", Variants: []Variant{{ID: "a", Ops: ops}}}
	return Share{
		ID:          id,
		TokenHash:   hashToken(token),
		ProjectPath: "/proj",
		Walkthrough: pw,
		Digest:      "d-" + id,
		CreatedAt:   time.Unix(1_690_000_000, 0).UTC(),
	}
}

// TestStoredAddScriptShareIsRefusedAtLoad closes the INV-14 gap on the load
// path. Publish has refused addScript since 2026-08-18, but Store.load checked
// only a record's checksum, so a share stored before that gate was indexed and
// served — its variants.json carried the op, script-src was widened by its
// hash, and the public bundle ran it. It must now be refused, loudly, by the
// INV-14 rule: neither served with its script nor quietly served without it.
func TestStoredAddScriptShareIsRefusedAtLoad(t *testing.T) {
	const token = "legacy-token"

	t.Run("live share carrying addScript fails the load by INV-14", func(t *testing.T) {
		dir := t.TempDir()
		path := writeStoredShare(t, dir, legacyShare("legacy1", token, true))

		// Premise: the record is intact — it decodes and its checksum matches —
		// so the refusal below is the INV-14 rule's and not a corruption error.
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeRecord(raw); err != nil {
			t.Fatalf("premise broken: the legacy record must decode with a valid checksum, got %v", err)
		}

		for name, open := range map[string]func() (*Store, error){
			"New":          func() (*Store, error) { return New(dir, nil) },
			"OpenReadOnly": func() (*Store, error) { return OpenReadOnly(dir, nil) },
		} {
			s, err := open()
			if !errors.Is(err, ErrUnservable) {
				t.Fatalf("%s: a stored share carrying addScript must fail the load with ErrUnservable, got store=%v err=%v", name, s != nil, err)
			}
			if errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s: the record is intact; the refusal must not be reported as corruption: %v", name, err)
			}
			msg := err.Error()
			// The verdict, as literals: the rule, the record, and what to do.
			for _, want := range []string{"addScript is not permitted on a published walkthrough", "INV-14", "legacy1", path, "delete"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("%s: the refusal must contain %q, got: %s", name, want, msg)
				}
			}
			// The dangerous side effect is unreachable: there is no store to
			// verify the token against, so nothing can serve the revision —
			// with the script or without it.
			if s != nil {
				t.Fatalf("%s: a refused load must not hand back a usable store", name)
			}
		}
		// The load refused; it did not rewrite or remove the record.
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, after) {
			t.Fatalf("a refused load must leave the record as it found it (err = %v)", err)
		}
	})

	t.Run("near miss: the same share without the op loads and serves", func(t *testing.T) {
		dir := t.TempDir()
		writeStoredShare(t, dir, legacyShare("clean1", token, false))
		s, err := New(dir, nil)
		if err != nil {
			t.Fatalf("a legacy share with no addScript must still load: %v", err)
		}
		rev, id, ok := s.VerifyToken(token)
		if !ok || id != "clean1" || rev == nil {
			t.Fatalf("the clean legacy share must verify and serve, got id=%q ok=%v", id, ok)
		}
	})

	t.Run("near miss: a REVOKED share carrying the op does not take the store down", func(t *testing.T) {
		dir := t.TempDir()
		revoked := legacyShare("gone1", token, true)
		revoked.Revoked = true
		writeStoredShare(t, dir, revoked)
		writeStoredShare(t, dir, legacyShare("clean2", "other-token", false))

		s, err := New(dir, nil)
		if err != nil {
			t.Fatalf("a tombstone carrying addScript must not fail the load: %v", err)
		}
		// It stays a tombstone: listed for audit, never served.
		if _, _, ok := s.VerifyToken(token); ok {
			t.Fatal("the revoked share's token verified — a revoked revision carrying addScript was served")
		}
		if info, err := s.Status("gone1"); err != nil || !info.Revoked {
			t.Fatalf("the tombstone must remain visible as revoked, got %+v err=%v", info, err)
		}
		if _, _, ok := s.VerifyToken("other-token"); !ok {
			t.Fatal("the unrelated live share must still serve")
		}
	})

	t.Run("one refused share fails the whole store, live siblings included", func(t *testing.T) {
		dir := t.TempDir()
		writeStoredShare(t, dir, legacyShare("clean3", "other-token", false))
		writeStoredShare(t, dir, legacyShare("legacy2", token, true))
		if _, err := New(dir, nil); !errors.Is(err, ErrUnservable) {
			t.Fatalf("the load must fail loud rather than serve around the refused share, got %v", err)
		}
	})
}

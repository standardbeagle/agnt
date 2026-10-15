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

// quarantineReasonLiteral is the INV-14 refusal, spelled out. Assertions compare
// against this literal and not against errAddScriptRefused(), so a reworded or
// emptied reason fails here.
const quarantineReasonLiteral = "addScript is not permitted on a published walkthrough"

// TestStoredAddScriptShareIsQuarantinedAtLoad closes the INV-14 gap on the load
// path. Publish has refused addScript since 2026-08-18, but Store.load checked
// only a record's checksum, so a share stored before that gate was indexed and
// served — its variants.json carried the op, script-src was widened by its
// hash, and the public bundle ran it.
//
// Such a share is now QUARANTINED: the store loads, every other share serves,
// and the one carrying the op is neither served with its script nor quietly
// served without it. It is reported (Quarantined, Status, List) and cleared by
// Revoke. Nothing but this one reason quarantines — corruption still fails.
func TestStoredAddScriptShareIsQuarantinedAtLoad(t *testing.T) {
	const legacyToken = "legacy-token"
	const healthyToken = "healthy-token"

	// setup writes one quarantined-to-be share and one healthy share.
	setup := func(t *testing.T) (dir, legacyPath string, legacyBytes []byte) {
		t.Helper()
		dir = t.TempDir()
		legacyPath = writeStoredShare(t, dir, legacyShare("legacy1", legacyToken, true))
		writeStoredShare(t, dir, legacyShare("healthy1", healthyToken, false))
		raw, err := os.ReadFile(legacyPath)
		if err != nil {
			t.Fatal(err)
		}
		// Premise: the record is intact — it decodes and its checksum matches —
		// so what happens to it is the INV-14 rule's doing, not corruption's.
		if _, err := decodeRecord(raw); err != nil {
			t.Fatalf("premise broken: the legacy record must decode with a valid checksum, got %v", err)
		}
		return dir, legacyPath, raw
	}

	for name, open := range map[string]func(string) (*Store, error){
		"New":          func(dir string) (*Store, error) { return New(dir, nil) },
		"OpenReadOnly": func(dir string) (*Store, error) { return OpenReadOnly(dir, nil) },
	} {
		t.Run(name+": the store loads, the healthy share serves, the quarantined one does not", func(t *testing.T) {
			dir, legacyPath, before := setup(t)
			s, err := open(dir)
			if err != nil {
				t.Fatalf("one stored share carrying addScript must not fail the load: %v", err)
			}

			// The healthy sibling is untouched by its neighbour.
			rev, id, ok := s.VerifyToken(healthyToken)
			if !ok || id != "healthy1" || rev == nil {
				t.Fatalf("the healthy share must serve, got id=%q ok=%v", id, ok)
			}
			if d, ok := s.ShareDigest("healthy1"); !ok || d == "" {
				t.Fatalf("the healthy share must name its revision, got %q ok=%v", d, ok)
			}

			// The quarantined share is not served. Its token gets EXACTLY what a
			// token that never existed gets — same values, no distinguishing
			// answer for an anonymous viewer.
			qRev, qID, qOK := s.VerifyToken(legacyToken)
			uRev, uID, uOK := s.VerifyToken("a-token-nobody-ever-minted")
			if qOK || qRev != nil || qID != "" {
				t.Fatalf("the quarantined share was served: rev=%v id=%q ok=%v", qRev != nil, qID, qOK)
			}
			if qOK != uOK || qID != uID || (qRev == nil) != (uRev == nil) {
				t.Fatalf("a quarantined token must be indistinguishable from an unknown one: (%v,%q,%v) vs (%v,%q,%v)",
					qRev != nil, qID, qOK, uRev != nil, uID, uOK)
			}
			// Nor can its revision be named for a subresource reference.
			if d, ok := s.ShareDigest("legacy1"); ok || d != "" {
				t.Fatalf("a quarantined share must name no revision, got %q ok=%v", d, ok)
			}

			// It is reported, with everything a startup log or a listing needs.
			q := s.Quarantined()
			if len(q) != 1 {
				t.Fatalf("Quarantined() = %+v, want exactly the one legacy share", q)
			}
			got := q[0]
			if got.ID != "legacy1" || got.ProjectPath != "/proj" || got.RecordPath != legacyPath || got.Title == "" {
				t.Fatalf("Quarantined() entry is incomplete: %+v (want id legacy1, project /proj, record %s)", got, legacyPath)
			}
			if !strings.Contains(got.Reason, quarantineReasonLiteral) || !strings.Contains(got.Reason, "INV-14") {
				t.Fatalf("the quarantine reason must be the INV-14 rule, got %q", got.Reason)
			}

			// The owner is not left guessing: Status and List flag it.
			info, err := s.Status("legacy1")
			if err != nil || !info.Quarantined || info.Revoked ||
				!strings.Contains(info.QuarantineReason, quarantineReasonLiteral) || info.RecordPath != legacyPath {
				t.Fatalf("Status must flag the quarantine with reason and record, got %+v err=%v", info, err)
			}
			flagged := map[string]bool{}
			for _, sh := range s.List("/proj") {
				flagged[sh.ID] = sh.Quarantined
			}
			if len(flagged) != 2 || !flagged["legacy1"] || flagged["healthy1"] {
				t.Fatalf("List must show both shares and flag only the quarantined one, got %v", flagged)
			}
			if healthy, _ := s.Status("healthy1"); healthy.Quarantined || healthy.QuarantineReason != "" || healthy.RecordPath != "" {
				t.Fatalf("a healthy share must carry no quarantine fields, got %+v", healthy)
			}

			// A fresh token would put it back in front of viewers: refused.
			if _, err := s.Rotate("legacy1"); !errors.Is(err, ErrUnservable) && !errors.Is(err, ErrReadOnly) {
				t.Fatalf("Rotate on a quarantined share must be refused, got %v", err)
			}
			if _, _, ok := s.VerifyToken(legacyToken); ok {
				t.Fatal("the quarantined share became servable after a refused Rotate")
			}

			// Loading (and everything above) left the record exactly as found.
			after, err := os.ReadFile(legacyPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("the quarantined record file must be untouched (err = %v)", err)
			}
		})
	}

	t.Run("Rotate refusal is the quarantine's, by its own sentinel", func(t *testing.T) {
		dir, _, _ := setup(t)
		s, err := New(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Rotate("legacy1")
		if !errors.Is(err, ErrUnservable) || !strings.Contains(err.Error(), quarantineReasonLiteral) {
			t.Fatalf("Rotate must be refused by ErrUnservable carrying the reason, got %v", err)
		}
		// Near miss: the healthy share rotates.
		if _, err := s.Rotate("healthy1"); err != nil {
			t.Fatalf("a healthy share must still rotate: %v", err)
		}
	})

	t.Run("Revoke clears the quarantine, and it stays cleared across a restart", func(t *testing.T) {
		dir, _, _ := setup(t)
		s, err := New(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Revoke("legacy1"); err != nil {
			t.Fatalf("Revoke is the supported way to clear a quarantine; it must work: %v", err)
		}
		if q := s.Quarantined(); len(q) != 0 {
			t.Fatalf("a revoked share must leave the quarantine list, got %+v", q)
		}
		info, err := s.Status("legacy1")
		if err != nil || !info.Revoked || info.Quarantined {
			t.Fatalf("after Revoke it is an ordinary tombstone, got %+v err=%v", info, err)
		}
		if _, _, ok := s.VerifyToken(legacyToken); ok {
			t.Fatal("a revoked quarantined share was served")
		}
		// Restart: the tombstone still carries the op, and is simply ignored.
		again, err := New(dir, nil)
		if err != nil {
			t.Fatalf("reload after clearing: %v", err)
		}
		if q := again.Quarantined(); len(q) != 0 {
			t.Fatalf("a revoked record carrying the op must not be re-quarantined, got %+v", q)
		}
		if _, _, ok := again.VerifyToken(legacyToken); ok {
			t.Fatal("the tombstone was served after a restart")
		}
		if _, _, ok := again.VerifyToken(healthyToken); !ok {
			t.Fatal("the healthy share must still serve after the restart")
		}
	})

	t.Run("a record stored already revoked is ignored, not quarantined", func(t *testing.T) {
		dir := t.TempDir()
		revoked := legacyShare("gone1", legacyToken, true)
		revoked.Revoked = true
		writeStoredShare(t, dir, revoked)
		writeStoredShare(t, dir, legacyShare("healthy1", healthyToken, false))
		s, err := New(dir, nil)
		if err != nil {
			t.Fatalf("a tombstone carrying addScript must not fail the load: %v", err)
		}
		if q := s.Quarantined(); len(q) != 0 {
			t.Fatalf("a revoked record needs no quarantine, got %+v", q)
		}
		if _, _, ok := s.VerifyToken(legacyToken); ok {
			t.Fatal("a revoked revision carrying addScript was served")
		}
		if info, err := s.Status("gone1"); err != nil || !info.Revoked || info.Quarantined {
			t.Fatalf("the tombstone must stay visible as revoked only, got %+v err=%v", info, err)
		}
	})

	t.Run("quarantine is not widened: real corruption still fails the load", func(t *testing.T) {
		dir, legacyPath, raw := setup(t)
		// Flip one byte of the quarantined record's body: now it is corrupt.
		broken := bytes.Replace(raw, []byte("wt-legacy1"), []byte("wt-legacyX"), 1)
		if bytes.Equal(broken, raw) {
			t.Fatal("premise broken: the tamper did not change the record")
		}
		if err := os.WriteFile(legacyPath, broken, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(dir, nil); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("a checksum mismatch must fail the load as ErrCorrupt, got %v", err)
		}
		// And a truncated healthy record fails it too.
		dir2, _, _ := setup(t)
		healthyPath := filepath.Join(dir2, "healthy1"+recordSuffix)
		if err := os.WriteFile(healthyPath, []byte(`{"id":"healthy1"`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(dir2, nil); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("a truncated record must fail the load as ErrCorrupt, got %v", err)
		}
	})

	t.Run("a folder-served share is superseded when its file publishes clean, and revoked when the file goes", func(t *testing.T) {
		dir := t.TempDir()
		a := legacyShare("legacyA", legacyToken, true)
		a.SourceFile = "a.json"
		b := legacyShare("legacyB", "token-b", true)
		b.SourceFile = "b.json"
		writeStoredShare(t, dir, a)
		writeStoredShare(t, dir, b)
		s, err := New(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Quarantined()) != 2 {
			t.Fatalf("premise broken: both folder shares must start quarantined, got %+v", s.Quarantined())
		}

		// a.json is fixed and republished: a NEW share and token, the old one retired.
		newID, newToken, _, err := s.PublishFile(fileWalkthrough("wt-a", "clean"), "/proj", "a.json")
		if err != nil {
			t.Fatal(err)
		}
		if newID == "legacyA" || newToken == "" {
			t.Fatalf("the clean publish must mint a fresh share, got id=%q token-empty=%v", newID, newToken == "")
		}
		if _, id, ok := s.VerifyToken(newToken); !ok || id != newID {
			t.Fatal("the freshly published share must serve")
		}
		if _, _, ok := s.VerifyToken(legacyToken); ok {
			t.Fatal("the quarantined predecessor's token was served")
		}
		if info, _ := s.Status("legacyA"); !info.Revoked || info.Quarantined {
			t.Fatalf("the superseded share must be a tombstone, got %+v", info)
		}

		// b.json is deleted: the ordinary reconcile revokes it.
		revoked, err := s.ReconcileFiles("/proj", []string{"a.json"})
		if err != nil || len(revoked) != 1 || revoked[0] != "legacyB" {
			t.Fatalf("reconcile must revoke the share whose file is gone, got %v err=%v", revoked, err)
		}
		if q := s.Quarantined(); len(q) != 0 {
			t.Fatalf("nothing should remain quarantined, got %+v", q)
		}
		// The store reloads cleanly: no two live shares claim a.json.
		if _, err := New(dir, nil); err != nil {
			t.Fatalf("reload after supersede + reconcile: %v", err)
		}
	})
}

// TestStoreLoadRejectsRecordFiledUnderAnotherName pins that a share record is
// loaded only from its own file, <id>.json. A copy left under any other name
// (an operator's backup taken while the share was live) used to load too:
// ReadDir sorts "<id>.old.json" after "<id>.json", so the stale live copy
// overwrote the revoked record in byID and re-indexed the revoked token —
// INV-4 undone by a restart. persist writes <id>.json, so a record under any
// other name is corrupt on-disk state and fails the load loudly, as a
// feedback record filed under the wrong name already does.
func TestStoreLoadRejectsRecordFiledUnderAnotherName(t *testing.T) {
	s, dir := newStore(t)
	id, token, err := s.Create(validWalkthrough("wt"), "/proj")
	if err != nil {
		t.Fatal(err)
	}
	live, err := os.ReadFile(filepath.Join(dir, id+recordSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".old"+recordSuffix), live, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(id); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(dir, nil)
	if err == nil {
		if _, _, ok := reopened.VerifyToken(token); ok {
			t.Fatal("a revoked token serves again after reload: the backup copy was loaded over the revoked record")
		}
		t.Fatal("a record filed under a name other than <id>.json loaded without error")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

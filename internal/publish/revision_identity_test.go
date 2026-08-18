package publish

import (
	"reflect"
	"testing"
)

// TestFeedbackAcceptRevisionParamIsDistinctType pins the guard from task
// 01KYQHDTYJESKDGMW8A0MDV7X0: the feedback sink's revision parameter must be a
// DISTINCT named type (a content digest), never a bare string. A bare-string
// parameter is exactly the hole that let two different "RevisionID" meanings be
// swapped silently — Share.RevisionID is a minted CSPRNG publish-event id, while
// the feedback path keys by a content digest, and while both were plain strings
// nothing stopped a caller threading the minted id in where a digest was wanted.
//
// Mutation check: revert FeedbackStore.Accept's revision parameter back to
// `string` and this test fails — the assertion has teeth.
func TestFeedbackAcceptRevisionParamIsDistinctType(t *testing.T) {
	m, ok := reflect.TypeOf((*FeedbackStore)(nil)).MethodByName("Accept")
	if !ok {
		t.Fatal("FeedbackStore has no Accept method")
	}
	// Method func type carries the receiver as In(0): In(1)=shareID, In(2)=revision.
	revParam := m.Type.In(2)
	if revParam.Kind() != reflect.String || revParam.Name() != "RevisionDigest" {
		t.Fatalf("Accept revision param is %q (kind %v); want a distinct RevisionDigest string type so a minted RevisionID cannot be passed as a content digest",
			revParam.String(), revParam.Kind())
	}
}

// TestPublishFileRevisionIdentity_V1V2V1 documents the defined answer for the
// v1 -> v2 -> v1 case (acceptance criterion 2/4): three publishes mint three
// DISTINCT event ids (event identity never collapses), while the third
// revision reuses the first's content digest (content identity DOES collapse).
// This test is type-agnostic on purpose so it compiles both before and after the
// fix; the runtime rule it pins holds either way — the fix protects it in the
// type system.
func TestPublishFileRevisionIdentity_V1V2V1(t *testing.T) {
	s, _ := newStore(t)

	id, _, rev1, err := s.PublishFile(fileWalkthrough("wt", "v1"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	id2, _, rev2, err := s.PublishFile(fileWalkthrough("wt", "v2"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	// Restore v1's exact content as a third publish event.
	id3, _, rev3, err := s.PublishFile(fileWalkthrough("wt", "v1"), "/proj", "tour.html")
	if err != nil {
		t.Fatalf("re-publish v1: %v", err)
	}
	if id != id2 || id2 != id3 {
		t.Fatalf("one file must stay one share across edits: %q %q %q", id, id2, id3)
	}
	// Event identity: three publish events -> three DISTINCT minted revision ids,
	// even though publish #3 restores publish #1's exact content.
	if rev1 == rev2 || rev2 == rev3 || rev1 == rev3 {
		t.Fatalf("revision ids must be distinct per publish event: %v %v %v", rev1, rev2, rev3)
	}
	sh := s.byID[id]
	if len(sh.Revisions) != 3 {
		t.Fatalf("want 3 revisions in history, got %d", len(sh.Revisions))
	}
	r := sh.Revisions
	if r[0].ID != rev1 || r[1].ID != rev2 || r[2].ID != rev3 {
		t.Fatalf("history ids drifted from returned ids: (%v,%v,%v) vs (%v,%v,%v)",
			r[0].ID, r[1].ID, r[2].ID, rev1, rev2, rev3)
	}
	// Content identity: v1 -> v2 -> v1 means revision 3's digest equals revision
	// 1's (content collapses) and differs from revision 2's.
	if r[2].Digest != r[0].Digest {
		t.Fatalf("re-publishing v1 content must reuse v1's digest: r3=%q r1=%q", r[2].Digest, r[0].Digest)
	}
	if r[1].Digest == r[0].Digest {
		t.Fatalf("v2 digest must differ from v1 digest: %q", r[1].Digest)
	}
	// The share's CURRENT digest tracks the latest content (v1 again).
	info, _ := s.Status(id)
	if info.Digest != r[2].Digest {
		t.Fatalf("current share digest %q != latest revision digest %q", info.Digest, r[2].Digest)
	}
}

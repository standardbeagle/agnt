package publish

// Revision identity: a published revision has TWO identities that answer
// different questions, so they get two DISTINCT named types. The compiler then
// refuses to swap one for the other — the guard is a type, not a comment.
//
//   - RevisionID — a minted CSPRNG id assigned to each publish EVENT and recorded
//     in the share's append-only Revisions history. It identifies "this act of
//     publishing"; re-publishing mints a NEW RevisionID every time.
//
//   - RevisionDigest — the sha256 over the canonical artifact encoding: the
//     identity of the CONTENT. Two revisions with byte-identical content share
//     one RevisionDigest. Feedback is keyed by RevisionDigest (spec §10): a
//     viewer's feedback is about the content they saw, not the publish event.
//
// # The v1 -> v2 -> v1 answer
//
// Publishing v1, then v2, then v1's content again has a defined answer under this
// split:
//
//   - three publishes mint three DISTINCT RevisionIDs — event identity never
//     collapses, so the history stays append-only and every re-publish is
//     visible; and
//   - the third revision carries the SAME RevisionDigest as the first — content
//     identity DOES collapse, so feedback captured on the v1 content and feedback
//     captured after the v1-restoring re-publish are tagged with one digest,
//     because they are about the same content state. That is intentional.
//
// # Why the types exist
//
// Before this split both fields were called RevisionID and both were plain
// strings: Share.RevisionID held a minted id while the feedback record held a
// digest. Nothing stopped a caller threading PublishFile's minted RevisionID
// into the feedback sink where a digest is expected, silently splitting a
// share's feedback history (new rows keyed by a random id, old rows by a
// digest). These two types make that a compile error.
type (
	// RevisionID is a minted CSPRNG identifier for one publish EVENT.
	RevisionID string
	// RevisionDigest is the content-hash identity of a revision's artifact — the
	// key the feedback path uses.
	RevisionDigest string
)

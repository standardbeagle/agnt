package sessionhost

import (
	"testing"

	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// The empty-slot sentinel for primaryID is "", so an attach id of "" must not
// compare equal to it: SESSION-HOST STDIN with an omitted attach_id would
// otherwise be accepted as primary whenever no primary is attached, bypassing
// the primary-only stdin rule.
func TestIsPrimary_EmptyAttachIDNeverPrimary(t *testing.T) {
	s := &Session{
		subs:       make(map[string]*subscriber),
		scrollback: goprocess.NewRingBuffer(1024),
		doneCh:     make(chan struct{}),
	}
	s.primaryID.Store("")

	if s.IsPrimary("") {
		t.Fatal("empty attach id is primary before any attach")
	}

	_, id, primary := s.Attach(4)
	if !primary || !s.IsPrimary(id) {
		t.Fatalf("first attach %q should hold the primary slot", id)
	}
	if s.IsPrimary("") {
		t.Fatal("empty attach id is primary while a primary is attached")
	}

	s.Detach(id)
	if s.IsPrimary("") {
		t.Fatal("empty attach id is primary after the primary detached")
	}
	if s.IsPrimary(id) {
		t.Fatalf("detached attach %q still primary", id)
	}
}

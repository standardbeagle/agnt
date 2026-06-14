package scope

import (
	"path/filepath"
	"testing"
)

func TestProjectScope_MatchesOnlyOwnProject(t *testing.T) {
	a, _ := filepath.Abs("/tmp/space")
	b, _ := filepath.Abs("/tmp/rpg")

	s := Project(a)
	if !s.Valid() {
		t.Fatal("Project scope should be valid")
	}
	if s.IsUnscoped() {
		t.Fatal("Project scope must not be unscoped")
	}
	if !s.Match(a) {
		t.Errorf("scope for %s should match itself", a)
	}
	if s.Match(b) {
		t.Errorf("scope for %s must NOT match foreign project %s", a, b)
	}
}

func TestProjectScope_NormalizesBothSides(t *testing.T) {
	// Raw relative + trailing-dot forms must compare equal to absolute.
	s := Project("/tmp/space/")
	if !s.Match("/tmp/space") {
		t.Errorf("normalized match failed: %q vs %q", s.ProjectPath(), "/tmp/space")
	}
	if !s.Match("/tmp/space/.") {
		t.Errorf("trailing-dot form should match")
	}
}

func TestUnscoped_MatchesEverything(t *testing.T) {
	s := Unscoped("test: reap all")
	if !s.IsUnscoped() {
		t.Fatal("Unscoped must report unscoped")
	}
	if s.Reason() != "test: reap all" {
		t.Errorf("reason not preserved: %q", s.Reason())
	}
	if !s.Match("/tmp/space") || !s.Match("/anything") || !s.Match("") {
		t.Error("unscoped scope must match every project including empty")
	}
}

func TestZeroScope_MatchesNothing(t *testing.T) {
	var s Scope // zero value
	if s.Valid() {
		t.Fatal("zero scope must be invalid")
	}
	if s.Match("/tmp/space") || s.Match("") {
		t.Error("zero scope must match nothing — prevents accidental global")
	}
}

func TestEmptyProject_MatchesNothing(t *testing.T) {
	s := Project("")
	if s.Match("/tmp/space") {
		t.Error("empty-path project scope must not match a real project")
	}
}

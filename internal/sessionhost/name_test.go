package sessionhost

import (
	"errors"
	"testing"
)

// A session id is "<name>-<n>" and travels as a whitespace-delimited
// positional argument of every later SESSION-HOST verb. A name the wire
// cannot carry must be refused before the PTY child is spawned; otherwise the
// session runs but can never be attached to or killed.
func TestCreate_RejectsNameTheWireCannotCarry(t *testing.T) {
	for _, name := range []string{"my session", "tab\tname", "line\nbreak", "a;;b", "x -- y"} {
		s, err := Create(CreateConfig{Name: name, Command: "agnt-sessionhost-name-test-never-runs"})
		if s != nil {
			_ = s.Close()
			t.Fatalf("Create(%q) spawned a session", name)
		}
		if !errors.Is(err, ErrInvalidName) {
			t.Errorf("Create(%q) error = %v, want ErrInvalidName", name, err)
		}
	}
}

func TestValidateName_AcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"", "dev", "feature-x", "claude_2", "my.session", "--name"} {
		if err := validateName(name); err != nil {
			t.Errorf("validateName(%q) = %v, want nil", name, err)
		}
	}
}

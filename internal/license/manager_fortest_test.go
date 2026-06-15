package license

import (
	"testing"
	"time"
)

// TestNewManagerForTest verifies the test-only constructor yields a Manager that
// grants a capability when seeded with a serving, granting Status.
func TestNewManagerForTest(t *testing.T) {
	p := &Payload{
		Email:        "test@example.com",
		IssuedAt:     time.Now(),
		Expiry:       time.Now().Add(24 * time.Hour),
		Capabilities: []string{string(CapAdvancedTesting)},
	}
	st := Evaluate(p, time.Now())
	if st.State != StateValid {
		t.Fatalf("expected StateValid, got %v", st.State)
	}

	m := NewManagerForTest(st)
	if _, err := m.Check(CapAdvancedTesting); err != nil {
		t.Fatalf("Check(CapAdvancedTesting) = %v, want nil", err)
	}

	// A capability the payload does not grant must still be denied.
	if _, err := m.Check(CapWholeSite); err == nil {
		t.Fatalf("Check(CapWholeSite) = nil, want GateError")
	}
}

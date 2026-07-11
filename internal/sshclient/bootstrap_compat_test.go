package sshclient

import "testing"

func TestVersionsCompatible_SameMajorMinor(t *testing.T) {
	cases := []struct {
		local, remote string
		want          bool
	}{
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.9", true},  // patch differs, same window
		{"1.2.9", "1.2.0", true},  // patch differs the other way
		{"1.2.3", "1.3.0", false}, // minor differs
		{"1.2.3", "0.2.3", false}, // major differs (lower)
		{"1.2.3", "2.2.3", false}, // major differs (higher)
		{"0.13.34", "0.13.0", true},
		{"0.13.34", "0.14.0", false},
	}
	for _, c := range cases {
		got, err := versionsCompatible(c.local, c.remote)
		if err != nil {
			t.Fatalf("versionsCompatible(%q, %q) returned error: %v", c.local, c.remote, err)
		}
		if got != c.want {
			t.Errorf("versionsCompatible(%q, %q) = %v, want %v", c.local, c.remote, got, c.want)
		}
	}
}

func TestVersionsCompatible_UnparsableVersionErrors(t *testing.T) {
	if _, err := versionsCompatible("not-a-version", "1.2.3"); err == nil {
		t.Error("expected error for unparsable local version")
	}
	if _, err := versionsCompatible("1.2.3", "not-a-version"); err == nil {
		t.Error("expected error for unparsable remote version")
	}
}

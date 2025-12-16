package daemon

import (
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input       string
		wantMajor   int
		wantMinor   int
		wantPatch   int
		wantErr     bool
		description string
	}{
		{"0.6.5", 0, 6, 5, false, "basic version"},
		{"v0.6.5", 0, 6, 5, false, "with v prefix"},
		{"V0.6.5", 0, 6, 5, false, "with V prefix"},
		{"1.0.0", 1, 0, 0, false, "major version 1"},
		{"10.20.30", 10, 20, 30, false, "double-digit versions"},
		{"0.6.5-beta", 0, 6, 5, false, "pre-release suffix (ignored)"},
		{"0.6.5+build123", 0, 6, 5, false, "build metadata (ignored)"},
		{"invalid", 0, 0, 0, true, "invalid format"},
		{"1.2", 0, 0, 0, true, "missing patch"},
		{"1.2.3.4", 0, 0, 0, true, "too many parts"},
		{"a.b.c", 0, 0, 0, true, "non-numeric"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			major, minor, patch, err := ParseVersion(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseVersion(%q) expected error, got nil", tt.input)
				}
				return
			}

			if err != nil {
				t.Errorf("ParseVersion(%q) unexpected error: %v", tt.input, err)
				return
			}

			if major != tt.wantMajor || minor != tt.wantMinor || patch != tt.wantPatch {
				t.Errorf("ParseVersion(%q) = (%d, %d, %d), want (%d, %d, %d)",
					tt.input, major, minor, patch, tt.wantMajor, tt.wantMinor, tt.wantPatch)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a           string
		b           string
		want        int
		wantErr     bool
		description string
	}{
		// Equal versions
		{"0.6.5", "0.6.5", 0, false, "equal versions"},
		{"v0.6.5", "0.6.5", 0, false, "equal with v prefix"},
		{"0.6.5", "v0.6.5", 0, false, "equal with v prefix (reverse)"},
		{"v0.6.5", "V0.6.5", 0, false, "equal with different prefix case"},

		// Less than
		{"0.6.4", "0.6.5", -1, false, "patch less than"},
		{"0.5.9", "0.6.0", -1, false, "minor less than"},
		{"0.9.9", "1.0.0", -1, false, "major less than"},

		// Greater than
		{"0.6.5", "0.6.4", 1, false, "patch greater than"},
		{"0.6.0", "0.5.9", 1, false, "minor greater than"},
		{"1.0.0", "0.9.9", 1, false, "major greater than"},

		// Pre-release versions
		{"0.6.5-beta", "0.6.5", 0, false, "pre-release equal to release (numeric parts)"},
		{"0.6.5", "0.6.5-beta", 0, false, "release equal to pre-release (numeric parts)"},

		// Error cases
		{"invalid", "0.6.5", 0, true, "invalid first version"},
		{"0.6.5", "invalid", 0, true, "invalid second version"},
		{"a.b.c", "0.6.5", 0, true, "non-numeric first version"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got, err := CompareVersions(tt.a, tt.b)

			if tt.wantErr {
				if err == nil {
					t.Errorf("CompareVersions(%q, %q) expected error, got nil", tt.a, tt.b)
				}
				return
			}

			if err != nil {
				t.Errorf("CompareVersions(%q, %q) unexpected error: %v", tt.a, tt.b, err)
				return
			}

			if got != tt.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestVersionsMatch(t *testing.T) {
	tests := []struct {
		a           string
		b           string
		want        bool
		description string
	}{
		{"0.6.5", "0.6.5", true, "exact match"},
		{"v0.6.5", "0.6.5", true, "match with v prefix"},
		{"0.6.5", "v0.6.5", true, "match with v prefix (reverse)"},
		{"V0.6.5", "v0.6.5", true, "match with different prefix case"},
		{"0.6.5-beta", "0.6.5", true, "pre-release matches release (numeric only)"},
		{"0.6.5", "0.6.4", false, "patch mismatch"},
		{"0.5.5", "0.6.5", false, "minor mismatch"},
		{"1.6.5", "0.6.5", false, "major mismatch"},
		{"invalid", "0.6.5", false, "invalid version"},
		{"0.6.5", "invalid", false, "invalid version (reverse)"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := VersionsMatch(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("VersionsMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestValidateVersion(t *testing.T) {
	tests := []struct {
		input       string
		wantErr     bool
		description string
	}{
		{"0.6.5", false, "valid version"},
		{"v0.6.5", false, "valid with v prefix"},
		{"1.0.0", false, "major version 1"},
		{"10.20.30", false, "double-digit versions"},
		{"0.6.5-beta", false, "with pre-release"},
		{"invalid", true, "invalid format"},
		{"1.2", true, "missing patch"},
		{"a.b.c", true, "non-numeric"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			err := ValidateVersion(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateVersion(%q) expected error, got nil", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateVersion(%q) unexpected error: %v", tt.input, err)
			}
		})
	}
}

func TestFormatVersion(t *testing.T) {
	tests := []struct {
		major       int
		minor       int
		patch       int
		want        string
		description string
	}{
		{0, 6, 5, "v0.6.5", "basic version"},
		{1, 0, 0, "v1.0.0", "major version 1"},
		{10, 20, 30, "v10.20.30", "double-digit versions"},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			got := FormatVersion(tt.major, tt.minor, tt.patch)
			if got != tt.want {
				t.Errorf("FormatVersion(%d, %d, %d) = %q, want %q",
					tt.major, tt.minor, tt.patch, got, tt.want)
			}
		})
	}
}

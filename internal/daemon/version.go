package daemon

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseVersion parses a semantic version string (e.g., "0.6.5" or "v0.6.5")
// and returns the major, minor, and patch numbers.
// Returns an error if the version format is invalid.
func ParseVersion(v string) (major, minor, patch int, err error) {
	// Handle "v" prefix
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	// Split by dots
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("invalid version format: %q (expected X.Y.Z)", v)
	}

	// Parse major
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid major version: %q", parts[0])
	}

	// Parse minor
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid minor version: %q", parts[1])
	}

	// Parse patch (may have pre-release suffix like "0-beta")
	// For now, we just take the numeric part before any hyphen
	patchStr := parts[2]
	if idx := strings.IndexAny(patchStr, "-+"); idx >= 0 {
		patchStr = patchStr[:idx]
	}

	patch, err = strconv.Atoi(patchStr)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid patch version: %q", parts[2])
	}

	return major, minor, patch, nil
}

// CompareVersions compares two semantic version strings.
// Returns:
//   - -1 if a < b
//   - 0 if a == b
//   - 1 if a > b
//   - error if either version is invalid
func CompareVersions(a, b string) (int, error) {
	aMajor, aMinor, aPatch, err := ParseVersion(a)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", a, err)
	}

	bMajor, bMinor, bPatch, err := ParseVersion(b)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", b, err)
	}

	// Compare major
	if aMajor != bMajor {
		if aMajor < bMajor {
			return -1, nil
		}
		return 1, nil
	}

	// Compare minor
	if aMinor != bMinor {
		if aMinor < bMinor {
			return -1, nil
		}
		return 1, nil
	}

	// Compare patch
	if aPatch != bPatch {
		if aPatch < bPatch {
			return -1, nil
		}
		return 1, nil
	}

	return 0, nil
}

// VersionsMatch checks if two versions are exactly equal.
// Returns true if versions match, false otherwise.
// Handles "v" prefix gracefully (v0.6.5 == 0.6.5).
func VersionsMatch(a, b string) bool {
	cmp, err := CompareVersions(a, b)
	if err != nil {
		return false
	}
	return cmp == 0
}

// ValidateVersion checks if a version string is valid.
// Returns nil if valid, error otherwise.
func ValidateVersion(v string) error {
	_, _, _, err := ParseVersion(v)
	return err
}

// FormatVersion formats a version with the "v" prefix.
// Example: "0.6.5" → "v0.6.5"
func FormatVersion(major, minor, patch int) string {
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch)
}

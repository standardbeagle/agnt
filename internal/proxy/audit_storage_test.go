package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- Path Traversal Tests for Audit Storage ---

func TestSanitizeFilename_NoSlashesOrBackslashes(t *testing.T) {
	// The critical security property: no path separators survive sanitization.
	// Without separators, the filename stays in its target directory regardless
	// of dots or other characters.
	inputs := []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32",
		"foo/../../../bar",
		"/absolute/path",
		"C:\\Windows\\System32",
		"sub/dir/file",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			result := sanitizeFilename(input)
			assert.NotContains(t, result, "/",
				"sanitized filename must not contain forward slash")
			assert.NotContains(t, result, "\\",
				"sanitized filename must not contain backslash")
		})
	}
}

func TestSanitizeFilename_NullByteStripped(t *testing.T) {
	// Null bytes can truncate paths on some OS calls
	tests := []struct {
		name  string
		input string
	}{
		{"middle null", "audit\x00evil"},
		{"leading null", "\x00leading"},
		{"trailing null", "trailing\x00"},
		{"multiple nulls", "a\x00b\x00c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			assert.NotContains(t, result, "\x00",
				"sanitized filename must not contain null bytes")
		})
	}
}

func TestSanitizeFilename_NoLeadingDots(t *testing.T) {
	// Leading dots create hidden files on Unix and can be part of traversal
	tests := []struct {
		name     string
		input    string
		notStart string
	}{
		{"single dot prefix", ".hidden", "."},
		{"double dot prefix", "..config", "."},
		{"triple dot prefix", "...tricky", "."},
		// After slash replacement, ../../../x becomes ..-..-..-x, leading dot stripped
		{"traversal becomes dotted", "../../../etc/passwd", "."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			if result != "" {
				assert.NotEqual(t, tt.notStart, string(result[0]),
					"sanitized filename should not start with a dot")
			}
		})
	}
}

func TestSanitizeFilename_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		mustNot []string
	}{
		{
			name:    "pipe and redirection",
			input:   "audit|cat /etc/passwd",
			mustNot: []string{"|", "/"},
		},
		{
			name:    "angle brackets (XSS)",
			input:   "audit<script>alert(1)</script>",
			mustNot: []string{"<", ">"},
		},
		{
			name:    "quotes (SQL injection pattern)",
			input:   `audit"';DROP TABLE--`,
			mustNot: []string{`"`},
		},
		{
			name:    "asterisk glob",
			input:   "audit*",
			mustNot: []string{"*"},
		},
		{
			name:    "question mark glob",
			input:   "audit?",
			mustNot: []string{"?"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeFilename(tt.input)
			for _, banned := range tt.mustNot {
				assert.NotContains(t, result, banned,
					"sanitized filename should not contain %q", banned)
			}
			assert.NotEmpty(t, result, "sanitized filename should not be empty")
		})
	}
}

func TestSanitizeFilename_LengthLimit(t *testing.T) {
	longInput := ""
	for i := 0; i < 200; i++ {
		longInput += "a"
	}

	result := sanitizeFilename(longInput)
	assert.LessOrEqual(t, len(result), 50, "sanitized filename should be at most 50 chars")
}

func TestSanitizeFilename_SafeCharacters(t *testing.T) {
	result := sanitizeFilename("my-audit-2024")
	assert.Equal(t, "my-audit-2024", result)

	result = sanitizeFilename("hello world")
	assert.Equal(t, "hello_world", result)
}

func TestSanitizeFilename_EmptyAndWhitespace(t *testing.T) {
	result := sanitizeFilename("")
	assert.Equal(t, "_", result, "empty input should produce safe fallback")

	result = sanitizeFilename("   ")
	assert.Equal(t, "___", result)
}

func TestSanitizeFilename_ColonReplacement(t *testing.T) {
	result := sanitizeFilename("project:name:host-port")
	assert.NotContains(t, result, ":")
	assert.Equal(t, "project-name-host-port", result)
}

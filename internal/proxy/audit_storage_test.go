package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// --- cwd-containment tests ---
//
// GetAuditDir used to call os.Getwd() internally. The daemon that owns proxies
// is long-lived and shared across projects, so its cwd belongs to whichever
// project happened to start it: audit data for project B landed in project A's
// tree, and under test inside the source tree. These assertions are POSITIVE —
// they prove the artifact was really written AND that it landed under the
// caller-supplied root. An absence-only check ("nothing appeared in the cwd")
// would pass just as happily if the code under test never ran.

func TestGetAuditDir_WritesUnderSuppliedRootNotProcessCwd(t *testing.T) {
	projectRoot := t.TempDir()
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	dir, err := GetAuditDir(projectRoot)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(projectRoot, ".agnt", AuditDirName), dir)

	// POSITIVE containment: a write through the returned dir really lands there.
	written := filepath.Join(dir, "containment.json")
	require.NoError(t, os.WriteFile(written, []byte(`{"ok":true}`), 0644))
	got, err := os.ReadFile(written)
	require.NoError(t, err, "artifact must exist under the supplied root")
	require.Equal(t, `{"ok":true}`, string(got))

	// NEGATIVE half: nothing was created relative to the process cwd.
	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr), "no .agnt tree may be created relative to the process cwd")
}

func TestGetAuditDir_EmptyRootFailsLoud(t *testing.T) {
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	dir, err := GetAuditDir("")
	require.Error(t, err, "a caller with no project root must fail loud, not fall back to cwd")
	require.Empty(t, dir)
	require.Contains(t, err.Error(), "project root is required")

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestSaveAuditData_LandsUnderSuppliedRoot(t *testing.T) {
	projectRoot := t.TempDir()
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	filePath, err := SaveAuditData(projectRoot, "accessibility", "home page", json.RawMessage(`{"issues":3}`))
	require.NoError(t, err)

	// POSITIVE containment: the file exists, holds the payload, and is inside
	// the supplied root's audit dir.
	require.Equal(t, filepath.Join(projectRoot, ".agnt", AuditDirName), filepath.Dir(filePath))
	raw, err := os.ReadFile(filePath)
	require.NoError(t, err, "audit JSON must exist under the supplied root")
	require.NotEmpty(t, raw)

	var saved map[string]any
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, "accessibility", saved["auditType"])
	require.Equal(t, "home page", saved["label"])

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestSaveAuditData_EmptyRootFailsLoud(t *testing.T) {
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	filePath, err := SaveAuditData("", "accessibility", "home page", json.RawMessage(`{}`))
	require.Error(t, err)
	require.Empty(t, filePath)

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestUpdateAuditSummary_LandsUnderSuppliedRoot(t *testing.T) {
	projectRoot := t.TempDir()
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	_, err := SaveAuditData(projectRoot, "performance", "checkout", json.RawMessage(`{"lcp":2100}`))
	require.NoError(t, err)
	require.NoError(t, UpdateAuditSummary(projectRoot))

	// POSITIVE containment: SUMMARY.md exists under the supplied root and
	// actually indexes the audit file written there.
	summaryPath := filepath.Join(projectRoot, ".agnt", AuditDirName, AuditSummaryFile)
	body, err := os.ReadFile(summaryPath)
	require.NoError(t, err, "SUMMARY.md must exist under the supplied root")
	require.NotEmpty(t, body)
	require.Contains(t, string(body), "audit-performance-checkout-")

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}

func TestUpdateAuditSummary_EmptyRootFailsLoud(t *testing.T) {
	sentinelCwd := t.TempDir()
	t.Chdir(sentinelCwd)

	require.Error(t, UpdateAuditSummary(""))

	_, statErr := os.Stat(filepath.Join(sentinelCwd, ".agnt"))
	require.True(t, os.IsNotExist(statErr))
}

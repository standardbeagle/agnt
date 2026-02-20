package pathutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafePathComponent_TraversalSequences(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"dot-dot-slash", "../../../etc/passwd"},
		{"backslash-traversal", "..\\..\\windows\\system32"},
		{"embedded-traversal", "normal/../../../escape"},
		{"bare-dot-dot", ".."},
		{"dot-dot-with-trailing-slash", "../"},
		{"double-dot-dot", "...."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafePathComponent(tt.input)
			assert.NotContains(t, result, "..", "result must not contain traversal sequence")
			assert.NotContains(t, result, "/", "result must not contain forward slash")
			assert.NotContains(t, result, "\\", "result must not contain backslash")
			assert.NotEmpty(t, result, "result must not be empty")
		})
	}
}

func TestSafePathComponent_NullBytes(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"null-in-middle", "file\x00evil"},
		{"null-only", "\x00"},
		{"null-with-traversal", "valid\x00/../../../etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafePathComponent(tt.input)
			assert.NotContains(t, result, "\x00", "result must not contain null bytes")
			assert.NotEmpty(t, result, "result must not be empty")
		})
	}
}

func TestSafePathComponent_DirectorySeparators(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"forward-slashes", "path/to/file"},
		{"backslashes", "path\\to\\file"},
		{"mixed-separators", "path/to\\file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafePathComponent(tt.input)
			assert.NotContains(t, result, "/", "result must not contain forward slash")
			assert.NotContains(t, result, "\\", "result must not contain backslash")
		})
	}
}

func TestSafePathComponent_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"shell-injection", "proxy; rm -rf /"},
		{"newline", "proxy\ninjection"},
		{"tabs", "proxy\t\ttabs"},
		{"quotes", "proxy\"quotes\""},
		{"pipes", "proxy|piped"},
		{"wildcards", "proxy*glob"},
		{"angle-brackets", "proxy<html>"},
		{"colons", "proxy:colon"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SafePathComponent(tt.input)
			assert.NotContains(t, result, "/")
			assert.NotContains(t, result, "\\")
			assert.NotContains(t, result, "\x00")
			assert.NotEmpty(t, result)
		})
	}
}

func TestSafePathComponent_LongInput(t *testing.T) {
	long := string(make([]byte, 10000))
	for i := range long {
		long = long[:i] + "a"
	}
	long = ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	result := SafePathComponent(long)
	assert.LessOrEqual(t, len(result), 50, "result should be truncated to max 50 chars")
}

func TestSafePathComponent_EmptyAndMinimal(t *testing.T) {
	assert.Equal(t, "_", SafePathComponent(""), "empty input should return fallback")
	assert.Equal(t, "_", SafePathComponent("\x00"), "null-only input should return fallback")
	assert.Equal(t, "_", SafePathComponent(".."), "traversal-only input should return fallback")
	assert.Equal(t, "_", SafePathComponent("../"), "traversal-slash should return fallback")
}

func TestSafePathComponent_NormalInputPreserved(t *testing.T) {
	assert.Equal(t, "claude-1", SafePathComponent("claude-1"))
	assert.Equal(t, "gemini-2", SafePathComponent("gemini-2"))
	assert.Equal(t, "my_session", SafePathComponent("my session"))
	assert.Equal(t, "test-label", SafePathComponent("test-label"))
}

func TestSafePathComponent_HiddenFiles(t *testing.T) {
	result := SafePathComponent(".hidden")
	assert.False(t, result[0] == '.', "result must not start with dot")

	result = SafePathComponent("...triple")
	assert.False(t, result[0] == '.', "result must not start with dot")
}

func TestSafePathComponent_SessionCodeVectors(t *testing.T) {
	// Simulate session codes that could be crafted for socket path traversal
	// Socket path: filepath.Join(dir, fmt.Sprintf("devtool-overlay-%s.sock", sessionCode))
	vectors := []string{
		"../../../tmp/evil",
		"../../.ssh/authorized_keys",
		"foo\x00/../../bar",
		"../evil.sock\x00.legitimate",
		"claude-1; rm -rf /",
	}

	for _, v := range vectors {
		t.Run(v, func(t *testing.T) {
			result := SafePathComponent(v)
			assert.NotContains(t, result, "..")
			assert.NotContains(t, result, "/")
			assert.NotContains(t, result, "\\")
			assert.NotContains(t, result, "\x00")
			assert.NotEmpty(t, result)
		})
	}
}

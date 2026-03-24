package overlay

import (
	"bytes"
	"strings"
	"testing"
)

// These tests reproduce the Claude Code status line interleaving bug.
// Claude Code uses \r to overwrite its status line in place, plus cursor
// save/restore for tool call rendering. The ProtectedWriter must track
// cursor position correctly through these sequences.

// TestProtectedWriter_CarriageReturnOverwrite verifies that \r followed
// by new text overwrites the current line without creating extra lines.
// This is Claude Code's basic spinner pattern.
func TestProtectedWriter_CarriageReturnOverwrite(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Write status line, then \r, then overwrite, then \r\n for next line
	pw.Write([]byte("Mustering...\r"))
	pw.Write([]byte("Running...  \r"))
	pw.Write([]byte("Done.       \r\n"))

	// After \r\n: cursor should be row 2, col 1
	row := int(pw.cursorRow.Load())
	col := int(pw.cursorCol.Load())
	if row != 2 {
		t.Errorf("Cursor row should be 2 after \\r\\n, got %d", row)
	}
	if col != 1 {
		t.Errorf("Cursor col should be 1 after \\r\\n, got %d", col)
	}
}

// TestProtectedWriter_StatusLineThenToolOutput verifies that a status line
// followed by tool call output on the next line doesn't interleave.
func TestProtectedWriter_StatusLineThenToolOutput(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Claude Code pattern: spinner + tool call output
	pw.Write([]byte("● plugin:slop-mcp - execute_tool (MCP)\r"))
	pw.Write([]byte("  ⎿  Running…\r"))
	pw.Write([]byte("  ⎿  {\n"))
	pw.Write([]byte("       \"name\": \"dev\"\n"))
	pw.Write([]byte("     }\n"))

	output := buf.String()

	// The tool output should appear cleanly — no status text embedded in it
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Running") && strings.Contains(line, "name") {
			t.Errorf("Status text and tool output interleaved on same line: %q", line)
		}
	}
}

// TestProtectedWriter_CursorColResetOnCR verifies that \r properly resets
// the column to 1 regardless of current position.
func TestProtectedWriter_CursorColResetOnCR(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	pw.Write([]byte("Hello World"))
	col := int(pw.cursorCol.Load())
	if col != 12 { // "Hello World" = 11 chars, cursor at 12
		t.Errorf("After 'Hello World', col should be 12, got %d", col)
	}

	pw.Write([]byte("\r"))
	col = int(pw.cursorCol.Load())
	if col != 1 {
		t.Errorf("After \\r, col should be 1, got %d", col)
	}

	// Row should not change on \r
	row := int(pw.cursorRow.Load())
	if row != 1 {
		t.Errorf("After \\r, row should still be 1, got %d", row)
	}
}

// TestProtectedWriter_MultibyteCharsColumnTracking verifies that multi-byte
// UTF-8 characters (emoji, CJK) are tracked as the correct column width.
// Wide chars take 2 columns; narrow chars take 1.
func TestProtectedWriter_MultibyteCharsColumnTracking(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// "●" is a single-width Unicode char (1 column, 3 bytes UTF-8)
	pw.Write([]byte("●"))
	col := int(pw.cursorCol.Load())
	if col != 2 {
		t.Errorf("After '●' (1 col), cursor should be at 2, got %d", col)
	}
}

// TestProtectedWriter_WideEmojiColumnTracking verifies that wide emoji
// characters advance the cursor by 2 columns.
func TestProtectedWriter_WideEmojiColumnTracking(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// "🔗" is a wide emoji (2 columns, 4 bytes UTF-8)
	pw.Write([]byte("🔗"))
	col := int(pw.cursorCol.Load())
	if col != 3 { // started at 1, advance 2 = 3
		t.Errorf("After '🔗' (2 cols), cursor should be at 3, got %d", col)
	}
}

// TestProtectedWriter_MixedASCIIAndEmoji verifies column tracking for
// mixed ASCII and emoji content like the status bar.
func TestProtectedWriter_MixedASCIIAndEmoji(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// "🔗 ● │ ⚙ ●" — status bar fragment
	// 🔗(2) + space(1) + ●(1) + space(1) + │(1) + space(1) + ⚙(1) + space(1) + ●(1) = 10 cols
	input := "🔗 ● │ ⚙ ●"
	pw.Write([]byte(input))
	col := int(pw.cursorCol.Load())
	// 🔗=2, rest are 1 each (8 chars) = 10 total, cursor at 11
	expected := 11
	if col != expected {
		t.Errorf("After %q (%d cols), cursor should be at %d, got %d", input, expected-1, expected, col)
	}
}

// TestProtectedWriter_StatusBarDoesNotOverflow verifies that a full status bar
// with emoji doesn't cause line wrapping that corrupts the protected row.
func TestProtectedWriter_StatusBarDoesNotOverflow(t *testing.T) {
	var buf bytes.Buffer
	// 80-col terminal, 1 protected row, aggressive mode for ConPTY testing
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1, AggressiveMode: true})
	defer pw.Stop()

	// Move to last safe row
	pw.Write([]byte("\x1b[23;1H"))

	// Write a line with emoji that's exactly 80 columns
	// Each 🔗 = 2 cols, so 40 of them = 80 cols
	for i := 0; i < 40; i++ {
		pw.Write([]byte("🔗"))
	}

	// Should NOT have scrolled (exactly fits)
	row := int(pw.cursorRow.Load())
	if row != 23 {
		t.Errorf("Row should still be 23 after exactly fitting, got %d", row)
	}

	// One more char should trigger scroll
	pw.Write([]byte("X"))
	row = int(pw.cursorRow.Load())
	// Should have wrapped and scrolled since we're at the protected boundary
	if row >= 24 {
		t.Errorf("Cursor should not enter protected row 24, got row %d", row)
	}
}

// TestProtectedWriter_RapidCROverwriteNearBottom verifies that rapid \r
// overwrites near the protected row don't trigger false scroll protection.
func TestProtectedWriter_RapidCROverwriteNearBottom(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Move cursor to row 22 (one above protected row 23 on a 24-row terminal)
	pw.Write([]byte("\x1b[22;1H"))

	// Now do rapid \r overwrites — these should NOT trigger scrolling
	// because we're overwriting the same row, not advancing
	pw.Write([]byte("Status 1...\r"))
	pw.Write([]byte("Status 2...\r"))
	pw.Write([]byte("Status 3...\r"))

	row := int(pw.cursorRow.Load())
	if row != 22 {
		t.Errorf("Row should still be 22 after \\r overwrites, got %d", row)
	}

	// Output should not contain scroll sequences
	output := buf.String()
	if strings.Contains(output, "\x1b[S") {
		t.Error("Scroll-up sequence should not appear for \\r overwrites on same row")
	}
}

// TestProtectedWriter_CUPPassthroughPreservesOriginal verifies that cursor
// position sequences are passed through with original bytes when no clamping
// is needed — not rewritten with explicit params.
func TestProtectedWriter_CUPPassthroughPreservesOriginal(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// \x1b[5H means "move to row 5" — should pass through as-is
	pw.Write([]byte("\x1b[5H"))
	output := buf.String()
	if output != "\x1b[5H" {
		t.Errorf("CUP should pass through as \\x1b[5H, got %q", output)
	}

	buf.Reset()
	pw.Write([]byte("\x1b[3;10H"))
	output = buf.String()
	if output != "\x1b[3;10H" {
		t.Errorf("CUP should pass through as \\x1b[3;10H, got %q", output)
	}
}

// TestProtectedWriter_CursorSaveRestoreTracking verifies that cursor
// save/restore sequences are tracked correctly for position.
func TestProtectedWriter_CursorSaveRestoreTracking(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Move to row 5, col 10
	pw.Write([]byte("\x1b[5;10H"))
	row := int(pw.cursorRow.Load())
	col := int(pw.cursorCol.Load())
	if row != 5 || col != 10 {
		t.Errorf("After CUP(5,10), expected row=5 col=10, got row=%d col=%d", row, col)
	}

	// Save cursor
	pw.Write([]byte("\x1b[s"))

	// Move somewhere else
	pw.Write([]byte("\x1b[1;1H"))
	row = int(pw.cursorRow.Load())
	col = int(pw.cursorCol.Load())
	if row != 1 || col != 1 {
		t.Errorf("After CUP(1,1), expected row=1 col=1, got row=%d col=%d", row, col)
	}

	// Restore cursor — should go back to 5,10
	pw.Write([]byte("\x1b[u"))
	row = int(pw.cursorRow.Load())
	col = int(pw.cursorCol.Load())
	if row != 5 || col != 10 {
		t.Errorf("After restore, expected row=5 col=10, got row=%d col=%d", row, col)
	}
}

// TestProtectedWriter_ProtectedRowNotCorrupted verifies that output never
// writes into the protected bottom row, even with rapid status updates.
func TestProtectedWriter_ProtectedRowNotCorrupted(t *testing.T) {
	var buf bytes.Buffer
	// Small terminal: 80x5 with 1 protected row
	pw := NewProtectedWriter(&buf, 80, 5, FilterConfig{ProtectBottomRows: 1, AggressiveMode: true})
	defer pw.Stop()

	// Fill the terminal with lines — should scroll, never write to row 5
	for i := 0; i < 10; i++ {
		pw.Write([]byte("Line of output\n"))
	}

	// Move cursor directly to protected row (row 5) — should be blocked
	pw.Write([]byte("\x1b[5;1H"))
	pw.Write([]byte("SHOULD NOT APPEAR"))

	row := int(pw.cursorRow.Load())
	if row >= 5 {
		t.Errorf("Cursor should be clamped above protected row 5, got row %d", row)
	}
}

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

// TestProtectedWriter_LinefeedAtScrollBoundary verifies that \n at the
// bottom of the scroll region doesn't drift the cursor row upward.
// This is the core bug: repeated \n at scrollBottom caused cursorRow to
// increment past the scroll region, making scroll protection fire on
// subsequent output at wrong positions.
func TestProtectedWriter_LinefeedAtScrollBoundary(t *testing.T) {
	var buf bytes.Buffer
	// 80x24, 1 protected row → scroll region 1-23
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Move cursor to scroll region bottom (row 23)
	pw.Write([]byte("\x1b[23;1H"))
	row := int(pw.cursorRow.Load())
	if row != 23 {
		t.Fatalf("Setup: expected row 23, got %d", row)
	}

	// Send multiple linefeeds at the boundary. The terminal scrolls content
	// up but the cursor stays on row 23. Our tracker must not drift.
	for i := 0; i < 5; i++ {
		pw.Write([]byte("\n"))
	}

	row = int(pw.cursorRow.Load())
	if row != 23 {
		t.Errorf("After linefeeds at scroll boundary, row should stay 23, got %d", row)
	}
}

// TestProtectedWriter_SpinnerThenMultilineOutput reproduces the exact Claude
// Code interleaving pattern: spinner overwrites on one line, then multi-line
// tool output. The cursor row must not drift during the spinner phase so
// that tool output lines are tracked correctly.
func TestProtectedWriter_SpinnerThenMultilineOutput(t *testing.T) {
	var buf bytes.Buffer
	// Small terminal: 80x10, 1 protected row → scroll region 1-9
	pw := NewProtectedWriter(&buf, 80, 10, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Fill terminal to push cursor to the scroll boundary
	for i := 0; i < 8; i++ {
		pw.Write([]byte("line\n"))
	}
	// Cursor is now at row 9 (scroll region bottom) after 8 linefeeds

	// Spinner phase: rapid \r overwrites on the same row at scroll boundary
	pw.Write([]byte("Mustering...\r"))
	pw.Write([]byte("Running...  \r"))
	pw.Write([]byte("Executing...\r"))

	row := int(pw.cursorRow.Load())
	if row != 9 {
		t.Errorf("After spinner phase, row should be 9, got %d", row)
	}

	// Tool output phase: \n moves to next line (causes scroll)
	buf.Reset()
	pw.Write([]byte("Result line 1\n"))
	pw.Write([]byte("Result line 2\n"))
	pw.Write([]byte("Result line 3"))

	row = int(pw.cursorRow.Load())
	if row != 9 {
		t.Errorf("After tool output at scroll boundary, row should be 9, got %d", row)
	}

	// Output must contain the tool results without scroll-up sequences
	// (scroll-up is for aggressive/ConPTY mode only)
	output := buf.String()
	if strings.Contains(output, "\x1b[S") {
		t.Error("Non-aggressive mode should not emit scroll-up sequences")
	}
	if !strings.Contains(output, "Result line 1") {
		t.Error("Tool output missing from written output")
	}
}

// TestProtectedWriter_CursorRestoreClampedInAggressive verifies that
// restoring a cursor position saved in the protected region gets clamped
// in aggressive mode.
func TestProtectedWriter_CursorRestoreClampedInAggressive(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1, AggressiveMode: true})
	defer pw.Stop()

	// Save cursor at a safe position
	pw.Write([]byte("\x1b[23;1H"))
	pw.Write([]byte("\x1b[s"))

	// Move to protected row and save (simulating a buggy program)
	pw.cursorRow.Store(24)
	pw.savedRow = 24
	pw.savedCol = 1

	// Restore should clamp to row 23
	pw.Write([]byte("\x1b[u"))

	row := int(pw.cursorRow.Load())
	if row != 23 {
		t.Errorf("Restored cursor should be clamped to 23, got %d", row)
	}
}

// TestProtectedWriter_ScrollRegionTrackingAfterReset verifies that scroll
// region bounds are updated when a DECSTBM command is processed.
func TestProtectedWriter_ScrollRegionTrackingAfterReset(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Default scroll region should be 1-23
	if pw.scrollTop != 1 || pw.scrollBottom != 23 {
		t.Errorf("Initial scroll region should be 1-23, got %d-%d", pw.scrollTop, pw.scrollBottom)
	}

	// Set scroll region to 5-20
	pw.Write([]byte("\x1b[5;20r"))

	if pw.scrollTop != 5 || pw.scrollBottom != 20 {
		t.Errorf("After DECSTBM 5;20, scroll region should be 5-20, got %d-%d", pw.scrollTop, pw.scrollBottom)
	}

	// Reset scroll region (no params)
	pw.Write([]byte("\x1b[r"))

	if pw.scrollTop != 1 || pw.scrollBottom != 23 {
		t.Errorf("After DECSTBM reset, scroll region should be 1-23, got %d-%d", pw.scrollTop, pw.scrollBottom)
	}
}

// TestProtectedWriter_RapidCRLinefeedCycleNearBottom is the complete
// reproduction of the interleaving bug. Claude Code does:
//  1. Write status text
//  2. \r to go back to column 1
//  3. Overwrite with new status
//  4. \n to advance to next line
//  5. Write tool output
//
// When step 4 happens at the scroll boundary, the old code incremented
// cursorRow past scrollBottom, causing all subsequent tracking to be wrong.
func TestProtectedWriter_RapidCRLinefeedCycleNearBottom(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 10, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Move to scroll boundary (row 9 in 10-row terminal with 1 protected)
	pw.Write([]byte("\x1b[9;1H"))

	// Simulate 10 cycles of status line overwrite + linefeed
	for i := 0; i < 10; i++ {
		pw.Write([]byte("Status update\r"))
		pw.Write([]byte("New status   \n"))
	}

	// The cursor row must never exceed scrollBottom (9)
	row := int(pw.cursorRow.Load())
	if row > 9 {
		t.Errorf("Cursor row drifted past scroll boundary: got %d, want <= 9", row)
	}
	if row < 1 {
		t.Errorf("Cursor row underflowed: got %d", row)
	}
}

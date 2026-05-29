//go:build !windows

package platform

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseStatFieldsAfterComm exercises the shared /proc/<pid>/stat
// post-comm field splitter that backs readProcInfo (ppid), readProcPPID
// (ppid), and readPGID (pgrp). The whole point of scanning to the LAST ')'
// is to survive a comm value that itself contains spaces or parentheses —
// a process named "(sh -c)" or "((weird))" would defeat any naive
// field-index or first-paren split and corrupt ppid/pgid extraction, which
// feeds the orphan-scan and session-containment kill paths. A wrong pgid
// there means killing the wrong process group.
func TestParseStatFieldsAfterComm(t *testing.T) {
	// Canonical stat layout after comm:
	//   field[0]=state field[1]=ppid field[2]=pgrp field[3]=session ...
	tests := []struct {
		name      string
		input     string
		wantState string
		wantPPID  int // field[1]
		wantPGID  int // field[2]
		minFields int
	}{
		{
			// Distinct pid/ppid/pgrp so an index slip is detectable:
			// after comm -> state=S ppid=4000 pgrp=4100.
			name:      "normal comm",
			input:     "4242 (bash) S 4000 4100 4100 34816 4242 4194304 ...",
			wantState: "S",
			wantPPID:  4000,
			wantPGID:  4100,
			minFields: 5,
		},
		{
			// comm contains a space; naive whitespace split would shift
			// every index. After last ')': state=R ppid=100 pgrp=200.
			name:      "comm with spaces",
			input:     "512 (sh -c) R 100 200 200 0 -1 4194560 ...",
			wantState: "R",
			wantPPID:  100,
			wantPGID:  200,
			minFields: 5,
		},
		{
			// Nested parens — scanning to the LAST ')' is what makes this
			// work. After last ')': state=Z ppid=1 pgrp=700.
			name:      "comm with nested parens",
			input:     "777 ((weird)) Z 1 700 700 0 -1 4194304 ...",
			wantState: "Z",
			wantPPID:  1,
			wantPGID:  700,
			minFields: 5,
		},
		{
			// comm itself embeds close-parens. Last ')' wins.
			// After last ')': state=T ppid=2 pgrp=88.
			name:      "comm is a lone close paren-heavy name",
			input:     "9 (a)b)c) T 2 88 88 0 ...",
			wantState: "T",
			wantPPID:  2,
			wantPGID:  88,
			minFields: 4,
		},
		{
			// Trailing spaces inside comm before the close paren.
			// After last ')': state=D ppid=30 pgrp=31.
			name:      "comm with trailing spaces before close paren",
			input:     "33 (node  ) D 30 31 31 0 ...",
			wantState: "D",
			wantPPID:  30,
			wantPGID:  31,
			minFields: 4,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := parseStatFieldsAfterComm([]byte(tc.input))
			require.GreaterOrEqual(t, len(fields), tc.minFields,
				"must yield at least state/ppid/pgrp fields")
			assert.Equal(t, tc.wantState, fields[0], "state is first post-comm field")

			ppid, err := strconv.Atoi(fields[1])
			require.NoError(t, err, "ppid field must parse as int")
			assert.Equal(t, tc.wantPPID, ppid, "ppid is field index 1")

			pgid, err := strconv.Atoi(fields[2])
			require.NoError(t, err, "pgrp field must parse as int")
			assert.Equal(t, tc.wantPGID, pgid, "pgrp is field index 2")
		})
	}
}

// TestParseStatFieldsAfterComm_Degenerate covers the fallback paths that
// every caller guards with a len() check: malformed records with no ')',
// empty input, a record whose ')' is the final byte, and a record with
// fewer than the fields a caller wants. The helper must never panic and
// must return something a len() check can reject.
func TestParseStatFieldsAfterComm_Degenerate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantLen   int
		wantNil   bool
		wantFirst string // only checked when wantLen > 0
	}{
		{
			name:    "empty input",
			input:   "",
			wantLen: 0,
			wantNil: true,
		},
		{
			name:    "no closing paren at all",
			input:   "4242 bash S 1 4242",
			wantLen: 0,
			wantNil: true,
		},
		{
			name:    "close paren is final byte yields no fields",
			input:   "4242 (bash)",
			wantLen: 0,
		},
		{
			name:      "fewer than 3 fields after comm",
			input:     "5 (x) S",
			wantLen:   1,
			wantFirst: "S",
		},
		{
			name:      "exactly two fields after comm (ppid present, pgrp absent)",
			input:     "6 (y) R 99",
			wantLen:   2,
			wantFirst: "R",
		},
		{
			name:    "only whitespace after comm",
			input:   "7 (z)    ",
			wantLen: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fields := parseStatFieldsAfterComm([]byte(tc.input))
			assert.Len(t, fields, tc.wantLen)
			if tc.wantNil {
				assert.Nil(t, fields, "no-paren / empty input returns nil for uniform handling")
			}
			if tc.wantLen > 0 {
				assert.Equal(t, tc.wantFirst, fields[0])
			}
			// A pgrp (index 2) caller must reject anything with <3 fields.
			if tc.wantLen < 3 {
				assert.Less(t, len(fields), 3, "caller's len<3 guard rejects this record")
			}
		})
	}
}

// TestParseStatFieldsAfterComm_RealReadPGIDContract pins the exact behavior
// readPGID depends on: pgrp is post-comm index 2, and a real kernel-shaped
// line resolves to the expected group. This is the regression anchor for
// the session-containment kill path — if the index ever drifts, the orphan
// scanner kills the wrong group.
func TestParseStatFieldsAfterComm_RealReadPGIDContract(t *testing.T) {
	// A faithful /proc/<pid>/stat prefix. pid=1234, comm has spaces, then
	// state=S ppid=1 pgrp=1200 — all three numbers distinct so an index
	// slip is unambiguous.
	line := "1234 (my dev server) S 1 1200 1200 0 -1 4194560 1500 0 0 0"
	fields := parseStatFieldsAfterComm([]byte(line))
	require.GreaterOrEqual(t, len(fields), 3)

	// readPPID contract: field[1].
	ppid, err := strconv.Atoi(fields[1])
	require.NoError(t, err)
	assert.Equal(t, 1, ppid)

	// readPGID contract: field[2].
	pgid, err := strconv.Atoi(fields[2])
	require.NoError(t, err)
	assert.Equal(t, 1200, pgid, "pgrp must be distinct from pid and ppid")
	assert.NotEqual(t, 1234, pgid, "comm-with-spaces must not shift the pgrp index onto pid")
}

package browser

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantFull    string
		wantMajor   int
		wantOutdate bool
	}{
		{
			name:        "google chrome prefix",
			raw:         "Google Chrome 146.0.7680.164",
			wantFull:    "146.0.7680.164",
			wantMajor:   146,
			wantOutdate: false,
		},
		{
			name:        "chromium prefix",
			raw:         "Chromium 120.0.6099.0",
			wantFull:    "120.0.6099.0",
			wantMajor:   120,
			wantOutdate: false,
		},
		{
			name:        "bare version no prefix",
			raw:         "146.0.7680.164",
			wantFull:    "146.0.7680.164",
			wantMajor:   146,
			wantOutdate: false,
		},
		{
			name:        "trailing whitespace and newline",
			raw:         "Google Chrome 130.0.1.2\n",
			wantFull:    "130.0.1.2",
			wantMajor:   130,
			wantOutdate: false,
		},
		{
			name:        "boundary one below minimum is outdated",
			raw:         "Google Chrome " + strconv.Itoa(MinMajorVersion-1) + ".0.1.2",
			wantFull:    strconv.Itoa(MinMajorVersion-1) + ".0.1.2",
			wantMajor:   MinMajorVersion - 1,
			wantOutdate: true,
		},
		{
			name:        "boundary exactly minimum is not outdated",
			raw:         "Google Chrome " + strconv.Itoa(MinMajorVersion) + ".0.1.2",
			wantFull:    strconv.Itoa(MinMajorVersion) + ".0.1.2",
			wantMajor:   MinMajorVersion,
			wantOutdate: false,
		},
		{
			name:        "empty string yields zero value",
			raw:         "",
			wantFull:    "",
			wantMajor:   0,
			wantOutdate: false,
		},
		{
			name:        "only whitespace yields zero value",
			raw:         "   \n\t  ",
			wantFull:    "",
			wantMajor:   0,
			wantOutdate: false,
		},
		{
			name: "garbage with no dot keeps full but zero major",
			raw:  "Google Chrome notaversion",
			// last field captured as full version, no dot so major stays 0
			wantFull:    "notaversion",
			wantMajor:   0,
			wantOutdate: false,
		},
		{
			name:        "non-numeric major before dot keeps zero major",
			raw:         "vX.0.1.2",
			wantFull:    "vX.0.1.2",
			wantMajor:   0,
			wantOutdate: false,
		},
		{
			name:        "single field bare version",
			raw:         "121.0.0.0",
			wantFull:    "121.0.0.0",
			wantMajor:   121,
			wantOutdate: false,
		},
		{
			name:        "single field old version outdated",
			raw:         "99.0.0.0",
			wantFull:    "99.0.0.0",
			wantMajor:   99,
			wantOutdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := parseVersionOutput(tt.raw)
			require.NotNil(t, info, "parseVersionOutput must never return nil")
			assert.Equal(t, tt.wantFull, info.FullVersion, "FullVersion")
			assert.Equal(t, tt.wantMajor, info.MajorVersion, "MajorVersion")
			assert.Equal(t, tt.wantOutdate, info.IsOutdated, "IsOutdated")
			// Pure parser never sets Path.
			assert.Empty(t, info.Path, "pure parser must not set Path")
		})
	}
}

func TestFormatWarning(t *testing.T) {
	t.Run("nil info reports not found with instructions", func(t *testing.T) {
		msg := FormatWarning(nil)
		assert.Contains(t, msg, "Chrome not found")
		assert.Contains(t, msg, strconv.Itoa(MinMajorVersion))
		assert.Contains(t, msg, UpdateInstructions())
		assert.NotEmpty(t, msg)
	})

	t.Run("up-to-date returns empty string", func(t *testing.T) {
		info := &VersionInfo{
			FullVersion:  "146.0.7680.164",
			MajorVersion: 146,
			IsOutdated:   false,
		}
		assert.Equal(t, "", FormatWarning(info))
	})

	t.Run("exactly minimum returns empty string", func(t *testing.T) {
		info := &VersionInfo{
			FullVersion:  strconv.Itoa(MinMajorVersion) + ".0.0.0",
			MajorVersion: MinMajorVersion,
			IsOutdated:   false,
		}
		assert.Equal(t, "", FormatWarning(info))
	})

	t.Run("outdated mentions version major minimum and instructions", func(t *testing.T) {
		info := &VersionInfo{
			FullVersion:  "100.0.1.2",
			MajorVersion: 100,
			IsOutdated:   true,
		}
		msg := FormatWarning(info)
		require.NotEmpty(t, msg)
		assert.Contains(t, msg, "100.0.1.2", "must contain full version")
		assert.Contains(t, msg, "v100", "must contain major version")
		assert.Contains(t, msg, "v"+strconv.Itoa(MinMajorVersion), "must contain minimum recommended")
		assert.Contains(t, msg, "outdated")
		assert.Contains(t, msg, UpdateInstructions())
	})
}

func TestUpdateInstructions(t *testing.T) {
	instr := UpdateInstructions()
	require.NotEmpty(t, instr)
	assert.Contains(t, instr, "Linux:")
	assert.Contains(t, instr, "macOS:")
	assert.Contains(t, instr, "Windows:")
	assert.Contains(t, instr, "Playwright")
	// Sanity: the package-manager hints are present.
	assert.True(t, strings.Contains(instr, "google-chrome-stable"))
	assert.True(t, strings.Contains(instr, "winget"))
}

// TestStopByProjectPath_EmptyMatch verifies the empty-set short circuit:
// no registered browsers means (nil, nil) with no Chrome launched.
func TestStopByProjectPath_EmptyMatch(t *testing.T) {
	m := NewManager()
	ids, err := m.StopByProjectPath(context.Background(), "/no/such/project")
	require.NoError(t, err)
	assert.Nil(t, ids)
	assert.Equal(t, 0, m.ActiveCount(), "no browsers should be touched")
}

// TestStopByProjectPath_EmptyMatchCancelledCtx confirms the empty-match path
// short circuits before the ctx select, so a cancelled context is irrelevant.
func TestStopByProjectPath_EmptyMatchCancelledCtx(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ids, err := m.StopByProjectPath(ctx, "/no/such/project")
	require.NoError(t, err, "empty match returns before ctx is consulted")
	assert.Nil(t, ids)
}

// TestStopAll_NoBrowsers verifies StopAll on an empty manager returns (nil, nil):
// the Range adds nothing to the WaitGroup so done closes immediately.
func TestStopAll_NoBrowsers(t *testing.T) {
	m := NewManager()
	ids, err := m.StopAll(context.Background())
	require.NoError(t, err)
	assert.Nil(t, ids)
	assert.Equal(t, 0, m.ActiveCount())
}

// NOTE: A cancelled-context StopAll test is intentionally omitted. With an empty
// manager wg.Wait() returns immediately, so the select between the closed done
// channel and a cancelled ctx.Done() is randomized by the runtime — asserting a
// specific error there would be flaky. The empty-match StopByProjectPath path
// (TestStopByProjectPath_EmptyMatchCancelledCtx) is deterministic because it
// returns before any select on ctx.

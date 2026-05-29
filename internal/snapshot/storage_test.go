package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chdir changes the working directory to dir and returns a restore func.
func chdir(t *testing.T, dir string) func() {
	t.Helper()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	return func() { _ = os.Chdir(orig) }
}

// writeFile is a thin os.WriteFile wrapper used by tests.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

func TestNewStorage_DefaultPath(t *testing.T) {
	// Empty basePath -> ~/.agnt/baselines default. Point HOME at a temp dir
	// so we don't touch the real home directory.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	s, err := NewStorage("")
	require.NoError(t, err)
	require.NotNil(t, s)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	want := filepath.Join(home, ".agnt", "baselines")
	assert.Equal(t, want, s.basePath)

	info, err := os.Stat(want)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "default dir created")
}

func TestNewStorage_ProvidedPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "baselines")
	s, err := NewStorage(dir)
	require.NoError(t, err)
	require.NotNil(t, s)

	assert.Equal(t, dir, s.basePath)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "provided dir created (incl. parents)")

	// Idempotent: a second NewStorage on the same path succeeds.
	s2, err := NewStorage(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, s2.basePath)
}

func TestStorage_SaveLoadBaseline_RoundTrip(t *testing.T) {
	s, err := NewStorage(t.TempDir())
	require.NoError(t, err)

	ts := time.Date(2024, 3, 14, 9, 26, 53, 0, time.UTC)
	in := &Baseline{
		Name:      "round",
		Timestamp: ts,
		GitCommit: "abc1234",
		GitBranch: "main",
		Pages: []PageState{
			{
				URL:        "http://a/",
				Viewport:   Viewport{Width: 100, Height: 200},
				Screenshot: "0_a_deadbeef.png",
				Timestamp:  ts,
				Metadata:   map[string]string{"k": "v"},
			},
		},
		Config: Config{DiffThreshold: 0.03},
	}
	require.NoError(t, s.SaveBaseline(in))

	out, err := s.LoadBaseline("round")
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.Equal(t, in.Name, out.Name)
	assert.True(t, in.Timestamp.Equal(out.Timestamp))
	assert.Equal(t, in.GitCommit, out.GitCommit)
	assert.Equal(t, in.GitBranch, out.GitBranch)
	assert.InDelta(t, in.Config.DiffThreshold, out.Config.DiffThreshold, 1e-9)
	require.Len(t, out.Pages, 1)
	assert.Equal(t, in.Pages[0].URL, out.Pages[0].URL)
	assert.Equal(t, in.Pages[0].Viewport, out.Pages[0].Viewport)
	assert.Equal(t, in.Pages[0].Screenshot, out.Pages[0].Screenshot)
	assert.Equal(t, in.Pages[0].Metadata, out.Pages[0].Metadata)
}

func TestStorage_LoadBaseline_Missing(t *testing.T) {
	s, err := NewStorage(t.TempDir())
	require.NoError(t, err)

	b, err := s.LoadBaseline("nope")
	require.Error(t, err)
	assert.Nil(t, b)
	assert.Contains(t, err.Error(), "not found")
	assert.Contains(t, err.Error(), "nope")
}

func TestStorage_LoadBaseline_CorruptMetadata(t *testing.T) {
	base := t.TempDir()
	s, err := NewStorage(base)
	require.NoError(t, err)

	dir := filepath.Join(base, "broken")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, writeFile(filepath.Join(dir, "metadata.json"), []byte("{ this is not json")))

	b, err := s.LoadBaseline("broken")
	require.Error(t, err)
	assert.Nil(t, b)
	assert.Contains(t, err.Error(), "parse metadata")
}

func TestStorage_ListBaselines_EmptyAndNonexistent(t *testing.T) {
	// Existing-but-empty base dir.
	s, err := NewStorage(t.TempDir())
	require.NoError(t, err)
	list, err := s.ListBaselines()
	require.NoError(t, err)
	assert.Empty(t, list)

	// Nonexistent base dir -> explicit empty slice (non-nil), not an error.
	s2 := &Storage{basePath: filepath.Join(t.TempDir(), "missing")}
	list2, err := s2.ListBaselines()
	require.NoError(t, err)
	assert.NotNil(t, list2, "IsNotExist branch returns []*Baseline{}")
	assert.Empty(t, list2)
}

func TestStorage_ListBaselines_SkipsInvalidAndSortsNewestFirst(t *testing.T) {
	base := t.TempDir()
	s, err := NewStorage(base)
	require.NoError(t, err)

	older := &Baseline{Name: "older", Timestamp: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)}
	newer := &Baseline{Name: "newer", Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	require.NoError(t, s.SaveBaseline(older))
	require.NoError(t, s.SaveBaseline(newer))

	// A stray non-directory entry must be skipped (not a dir).
	require.NoError(t, writeFile(filepath.Join(base, "loose.txt"), []byte("x")))
	// A directory without metadata.json must be skipped (LoadBaseline errors).
	require.NoError(t, os.MkdirAll(filepath.Join(base, "no-meta"), 0755))

	list, err := s.ListBaselines()
	require.NoError(t, err)
	require.Len(t, list, 2, "only valid baselines counted")

	assert.Equal(t, "newer", list[0].Name, "newest first")
	assert.Equal(t, "older", list[1].Name)
	assert.True(t, list[0].Timestamp.After(list[1].Timestamp))
}

func TestStorage_SaveScreenshot_RoundTrip(t *testing.T) {
	base := t.TempDir()
	s, err := NewStorage(base)
	require.NoError(t, err)

	payload := []byte{0x89, 0x50, 0x4e, 0x47, 1, 2, 3}
	require.NoError(t, s.SaveScreenshot("shot", "0_x_abcd1234.png", payload))

	path, err := s.GetScreenshotPath("shot", "0_x_abcd1234.png")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "shot", "0_x_abcd1234.png"), path)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// Parent dir was created by SaveScreenshot.
	info, err := os.Stat(filepath.Join(base, "shot"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestStorage_SaveDiff_RoundTrip(t *testing.T) {
	base := t.TempDir()
	s, err := NewStorage(base)
	require.NoError(t, err)

	result := &CompareResult{
		BaselineName: "diffme",
		Timestamp:    time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
		Pages: []PageComparison{
			{URL: "http://a/", DiffPercentage: 2.5, HasChanges: true, Description: "Moderate changes (2.50%)"},
		},
		Summary: Summary{TotalPages: 1, PagesChanged: 1, AverageDiff: 2.5, HasRegressions: true},
	}
	require.NoError(t, s.SaveDiff(result))

	// SaveDiff writes report.json under <base>/../diffs/<name-timestamp>/.
	diffsRoot := s.GetDiffsPath()
	entries, err := os.ReadDir(diffsRoot)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "diff directory created")

	var reportPath string
	for _, e := range entries {
		if e.IsDir() {
			reportPath = filepath.Join(diffsRoot, e.Name(), "report.json")
		}
	}
	require.NotEmpty(t, reportPath)

	data, err := os.ReadFile(reportPath)
	require.NoError(t, err)

	var rt CompareResult
	require.NoError(t, json.Unmarshal(data, &rt))
	assert.Equal(t, "diffme", rt.BaselineName)
	require.Len(t, rt.Pages, 1)
	assert.Equal(t, "http://a/", rt.Pages[0].URL)
	assert.InDelta(t, 2.5, rt.Pages[0].DiffPercentage, 1e-9)
	assert.Equal(t, 1, rt.Summary.PagesChanged)
	assert.True(t, rt.Summary.HasRegressions)
}

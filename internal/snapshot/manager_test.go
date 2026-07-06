package snapshot

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodePNGBase64 builds a solid-color RGBA PNG of the given dimensions and
// returns its base64-encoded bytes, suitable for PageCapture.ScreenshotData.
func encodePNGBase64(t *testing.T, w, h int, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// oneWhitePixelPNGBase64 builds a w×h all-black PNG with a single white pixel at
// (0,0), giving a deterministic 1/(w*h) diff against an all-black baseline.
func oneWhitePixelPNGBase64(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{A: 255})
		}
	}
	img.Set(0, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// writePNGFile decodes base64 PNG data and writes it to dir/name, returning the
// absolute path — used to exercise the screenshot_path bridge.
func writePNGFile(t *testing.T, dir, name, b64 string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

func TestGenerateFilename(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	cases := []struct {
		name     string
		url      string
		index    int
		wantPref string
		mustHave []string
		mustMiss []string
	}{
		{
			name:     "http stripped",
			url:      "http://example.com/path",
			index:    0,
			wantPref: "0_",
			mustHave: []string{"example.com_path"},
			mustMiss: []string{"http://", "https://", "/"},
		},
		{
			name:     "https stripped and colon replaced",
			url:      "https://localhost:3000/app",
			index:    2,
			wantPref: "2_",
			mustHave: []string{"localhost_3000_app"},
			mustMiss: []string{"https://", ":", "/"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := m.generateFilename(tc.url, tc.index)

			// Determinism: same inputs -> same output.
			again := m.generateFilename(tc.url, tc.index)
			assert.Equal(t, got, again, "must be deterministic")

			assert.True(t, strings.HasPrefix(got, tc.wantPref), "index prefix: %q", got)
			assert.True(t, strings.HasSuffix(got, ".png"), ".png suffix: %q", got)
			for _, sub := range tc.mustHave {
				assert.Contains(t, got, sub)
			}
			for _, sub := range tc.mustMiss {
				assert.NotContains(t, got, sub)
			}

			// 8-char sha256 hex suffix immediately before .png.
			stem := strings.TrimSuffix(got, ".png")
			parts := strings.Split(stem, "_")
			hashPart := parts[len(parts)-1]
			assert.Len(t, hashPart, 8, "8-char hash suffix: %q", hashPart)
			assert.Regexp(t, "^[0-9a-f]{8}$", hashPart)
		})
	}
}

func TestGenerateFilename_TruncatedAt30(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	longURL := "http://" + strings.Repeat("a", 100) + ".com"
	got := m.generateFilename(longURL, 5)

	// Layout: <index>_<cleaned(<=30)>_<8hex>.png
	stem := strings.TrimSuffix(got, ".png")
	require.True(t, strings.HasPrefix(stem, "5_"))
	body := strings.TrimPrefix(stem, "5_")
	parts := strings.Split(body, "_")
	require.GreaterOrEqual(t, len(parts), 2)
	hashPart := parts[len(parts)-1]
	cleaned := strings.TrimSuffix(body, "_"+hashPart)

	assert.Len(t, cleaned, 30, "cleaned URL truncated to 30 chars")
	assert.Len(t, hashPart, 8)
	assert.True(t, strings.HasSuffix(got, ".png"))
	// Different index -> different name but same hash (hash is URL-only).
	other := m.generateFilename(longURL, 6)
	assert.NotEqual(t, got, other)
	assert.True(t, strings.HasSuffix(strings.TrimSuffix(other, ".png"), hashPart), "hash depends only on URL")
}

func TestGenerateDiffDescription(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	// Input is a fraction (0-1); compared after *100.
	cases := []struct {
		name     string
		fraction float64
		want     string
	}{
		{"zero", 0.0, "No visual changes detected"},
		{"minimal below 0.1pct", 0.0005, "Minimal changes (< 0.1%)"},
		{"boundary 0.001 -> 0.1pct is Minor", 0.001, "Minor changes (0.10%)"},
		{"minor mid", 0.005, "Minor changes (0.50%)"},
		{"boundary 0.01 -> 1.0pct is Moderate", 0.01, "Moderate changes (1.00%)"},
		{"moderate mid", 0.03, "Moderate changes (3.00%)"},
		{"boundary 0.05 -> 5.0pct is Significant", 0.05, "Significant changes (5.00%)"},
		{"significant high", 0.5, "Significant changes (50.00%)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, m.generateDiffDescription(tc.fraction))
		})
	}
}

func TestCreateBaseline_Success(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, 0.02)
	require.NoError(t, err)

	pages := []PageCapture{
		{
			URL:            "http://localhost:3000/",
			Viewport:       Viewport{Width: 100, Height: 50},
			ScreenshotData: encodePNGBase64(t, 4, 4, color.RGBA{R: 10, G: 20, B: 30, A: 255}),
		},
		{
			URL:            "http://localhost:3000/about",
			Viewport:       Viewport{Width: 200, Height: 100},
			ScreenshotData: encodePNGBase64(t, 2, 2, color.RGBA{R: 1, G: 2, B: 3, A: 255}),
		},
	}

	baseline, err := m.CreateBaseline("home", pages)
	require.NoError(t, err)
	require.NotNil(t, baseline)

	assert.Equal(t, "home", baseline.Name)
	assert.Len(t, baseline.Pages, 2)
	assert.False(t, baseline.Timestamp.IsZero())
	assert.InDelta(t, 0.02, baseline.Config.DiffThreshold, 1e-9)

	for i, ps := range baseline.Pages {
		assert.Equal(t, pages[i].URL, ps.URL)
		assert.Equal(t, pages[i].Viewport, ps.Viewport)
		assert.NotEmpty(t, ps.Screenshot, "filename set")
		assert.True(t, strings.HasSuffix(ps.Screenshot, ".png"))
		assert.False(t, ps.Timestamp.IsZero())
	}

	// Metadata persisted: round-trip via storage / GetBaseline.
	loaded, err := m.GetBaseline("home")
	require.NoError(t, err)
	assert.Equal(t, "home", loaded.Name)
	assert.Len(t, loaded.Pages, 2)
	assert.Equal(t, baseline.Pages[0].Screenshot, loaded.Pages[0].Screenshot)
	assert.Equal(t, baseline.Pages[1].URL, loaded.Pages[1].URL)
}

func TestCreateBaseline_DecodeError(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	pages := []PageCapture{
		{
			URL:            "http://x/",
			ScreenshotData: "!!!not-valid-base64!!!",
		},
	}

	baseline, err := m.CreateBaseline("bad", pages)
	require.Error(t, err)
	assert.Nil(t, baseline)
	assert.Contains(t, err.Error(), "decode screenshot")
	assert.Contains(t, err.Error(), "http://x/", "error names the page URL")

	// Nothing persisted.
	_, loadErr := m.GetBaseline("bad")
	assert.Error(t, loadErr)
}

func TestCompareToBaseline_MissingBaseline(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	res, err := m.CompareToBaseline("does-not-exist", nil, 0)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "load baseline")
	assert.Contains(t, err.Error(), "not found")
}

func TestCompareToBaseline_PageAbsentInCurrent(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	img := encodePNGBase64(t, 4, 4, color.RGBA{R: 5, G: 5, B: 5, A: 255})
	_, err = m.CreateBaseline("b", []PageCapture{
		{URL: "http://a/", Viewport: Viewport{Width: 1, Height: 1}, ScreenshotData: img},
		{URL: "http://b/", Viewport: Viewport{Width: 1, Height: 1}, ScreenshotData: img},
	})
	require.NoError(t, err)

	// Current capture omits http://b/ entirely.
	res, err := m.CompareToBaseline("b", []PageCapture{
		{URL: "http://a/", ScreenshotData: img},
	}, 0)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, 2, res.Summary.TotalPages)
	// http://a/ identical => unchanged; http://b/ missing => changed.
	assert.Equal(t, 1, res.Summary.PagesChanged)
	assert.Equal(t, 1, res.Summary.PagesUnchanged)
	assert.True(t, res.Summary.HasRegressions)

	var missing *PageComparison
	for i := range res.Pages {
		if res.Pages[i].URL == "http://b/" {
			missing = &res.Pages[i]
		}
	}
	require.NotNil(t, missing)
	assert.True(t, missing.HasChanges)
	assert.Equal(t, "Page not captured in current snapshot", missing.Description)
}

func TestCompareToBaseline_CurrentDecodeError(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	img := encodePNGBase64(t, 4, 4, color.RGBA{R: 9, G: 9, B: 9, A: 255})
	_, err = m.CreateBaseline("c", []PageCapture{
		{URL: "http://a/", Viewport: Viewport{Width: 1, Height: 1}, ScreenshotData: img},
	})
	require.NoError(t, err)

	res, err := m.CompareToBaseline("c", []PageCapture{
		{URL: "http://a/", ScreenshotData: "@@bad-base64@@"},
	}, 0)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "decode current screenshot")
}

func TestCompareToBaseline_DimensionMismatchRecorded(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	baseImg := encodePNGBase64(t, 4, 4, color.RGBA{R: 7, G: 7, B: 7, A: 255})
	_, err = m.CreateBaseline("d", []PageCapture{
		{URL: "http://a/", Viewport: Viewport{Width: 4, Height: 4}, ScreenshotData: baseImg},
	})
	require.NoError(t, err)

	// Current image has different dimensions -> Differ returns an error,
	// which is recorded as a comparison error (not fatal to CompareToBaseline).
	currImg := encodePNGBase64(t, 8, 8, color.RGBA{R: 7, G: 7, B: 7, A: 255})
	res, err := m.CompareToBaseline("d", []PageCapture{
		{URL: "http://a/", ScreenshotData: currImg},
	}, 0)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.Len(t, res.Pages, 1)
	assert.True(t, res.Pages[0].HasChanges)
	assert.Contains(t, res.Pages[0].Description, "Comparison error")
	assert.Equal(t, 1, res.Summary.PagesChanged)
	assert.Equal(t, 0, res.Summary.PagesUnchanged)
	assert.True(t, res.Summary.HasRegressions)
}

func TestCompareToBaseline_SummaryMath(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.01)
	require.NoError(t, err)

	identical := encodePNGBase64(t, 4, 4, color.RGBA{R: 0, G: 0, B: 0, A: 255})
	_, err = m.CreateBaseline("e", []PageCapture{
		{URL: "http://a/", Viewport: Viewport{Width: 4, Height: 4}, ScreenshotData: identical},
		{URL: "http://b/", Viewport: Viewport{Width: 4, Height: 4}, ScreenshotData: identical},
	})
	require.NoError(t, err)

	// Both current pages are identical to baseline -> zero diff everywhere.
	res, err := m.CompareToBaseline("e", []PageCapture{
		{URL: "http://a/", ScreenshotData: identical},
		{URL: "http://b/", ScreenshotData: identical},
	}, 0)
	require.NoError(t, err)

	assert.Equal(t, 2, res.Summary.TotalPages)
	assert.Equal(t, 0, res.Summary.PagesChanged)
	assert.Equal(t, 2, res.Summary.PagesUnchanged)
	assert.False(t, res.Summary.HasRegressions)
	// AverageDiff = (sum of fractions / pages) * 100 = 0 here.
	assert.InDelta(t, 0.0, res.Summary.AverageDiff, 1e-9)
	for _, p := range res.Pages {
		assert.False(t, p.HasChanges)
		assert.InDelta(t, 0.0, p.DiffPercentage, 1e-9)
		assert.Equal(t, "No visual changes detected", p.Description)
	}
}

// TestCompareToBaseline_PerCallThreshold verifies the per-call diff_threshold
// overrides the manager default: the same small diff is a regression under a
// tight threshold and clean under a loose one.
func TestCompareToBaseline_PerCallThreshold(t *testing.T) {
	m, err := NewManager(t.TempDir(), 0.5) // loose default
	require.NoError(t, err)

	// 4x4 baseline all-black; current flips exactly one of 16 px => 6.25% diff.
	base := encodePNGBase64(t, 4, 4, color.RGBA{A: 255})
	_, err = m.CreateBaseline("t", []PageCapture{
		{URL: "http://a/", Viewport: Viewport{Width: 4, Height: 4}, ScreenshotData: base},
	})
	require.NoError(t, err)

	cur := oneWhitePixelPNGBase64(t, 4, 4)

	// Tight threshold (1%): 6.25% diff is a regression.
	res, err := m.CompareToBaseline("t", []PageCapture{{URL: "http://a/", ScreenshotData: cur}}, 0.01)
	require.NoError(t, err)
	assert.True(t, res.Summary.HasRegressions, "6.25%% diff exceeds 1%% threshold")

	// Loose threshold (20%): same diff is clean. Proves the param is read, not
	// the manager's 0.5 default (which would also pass) — pair with the tight case.
	res, err = m.CompareToBaseline("t", []PageCapture{{URL: "http://a/", ScreenshotData: cur}}, 0.2)
	require.NoError(t, err)
	assert.False(t, res.Summary.HasRegressions, "6.25%% diff under 20%% threshold is clean")
}

// TestCompareToBaseline_ScreenshotPathBridge verifies pages can be sourced from
// a filesystem path (the bridge from the screenshot action) instead of inline
// base64.
func TestCompareToBaseline_ScreenshotPathBridge(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, 0.01)
	require.NoError(t, err)

	img := encodePNGBase64(t, 4, 4, color.RGBA{R: 3, G: 3, B: 3, A: 255})
	pngPath := writePNGFile(t, dir, "shot.png", img)

	_, err = m.CreateBaseline("b", []PageCapture{
		{URL: "http://a/", Viewport: Viewport{Width: 4, Height: 4}, ScreenshotPath: pngPath},
	})
	require.NoError(t, err)

	res, err := m.CompareToBaseline("b", []PageCapture{
		{URL: "http://a/", ScreenshotPath: pngPath},
	}, 0)
	require.NoError(t, err)
	require.Len(t, res.Pages, 1)
	assert.False(t, res.Pages[0].HasChanges, "same file compared to itself is unchanged")
}

func TestGetGitInfo_NonGitDir(t *testing.T) {
	// Run inside a temp dir that is not a git repo. getGitInfo shells out to
	// git relative to the process cwd, so change cwd for the duration.
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()

	m, err := NewManager(dir, 0.01)
	require.NoError(t, err)

	commit, branch := m.getGitInfo()
	// Graceful empty when not in a git repo. (If the temp dir somehow sits
	// inside a repo, the commit must still be truncated to 7 chars.)
	if commit != "" {
		assert.LessOrEqual(t, len(commit), 7, "commit truncated to 7 chars")
	} else {
		assert.Empty(t, commit)
	}
	_ = branch
	assert.NotPanics(t, func() { m.getGitInfo() })
}

func TestManager_Delegators(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, 0.01)
	require.NoError(t, err)

	img := encodePNGBase64(t, 2, 2, color.RGBA{R: 4, G: 4, B: 4, A: 255})
	_, err = m.CreateBaseline("one", []PageCapture{
		{URL: "http://a/", Viewport: Viewport{Width: 1, Height: 1}, ScreenshotData: img},
	})
	require.NoError(t, err)
	_, err = m.CreateBaseline("two", []PageCapture{
		{URL: "http://b/", Viewport: Viewport{Width: 1, Height: 1}, ScreenshotData: img},
	})
	require.NoError(t, err)

	// ListBaselines delegates to storage.
	list, err := m.ListBaselines()
	require.NoError(t, err)
	assert.Len(t, list, 2)

	// GetBaseline delegates to storage.LoadBaseline.
	got, err := m.GetBaseline("one")
	require.NoError(t, err)
	assert.Equal(t, "one", got.Name)

	// DeleteBaseline delegates to storage.
	require.NoError(t, m.DeleteBaseline("one"))
	_, err = m.GetBaseline("one")
	assert.Error(t, err)

	list, err = m.ListBaselines()
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, "two", list[0].Name)
}

func TestNewManager_StorageError(t *testing.T) {
	// A path whose parent is a regular file cannot be created as a directory,
	// so NewStorage's MkdirAll fails and NewManager propagates it.
	dir := t.TempDir()
	filePath := dir + "/afile"
	require.NoError(t, writeFile(filePath, []byte("x")))

	m, err := NewManager(filePath+"/sub", 0.01)
	require.Error(t, err)
	assert.Nil(t, m)
	assert.Contains(t, err.Error(), "create storage")
}

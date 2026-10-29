package snapshot

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePNG encodes an image to a PNG file at path and fails the test on error.
func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(f, img))
	require.NoError(t, f.Close())
}

// solidImage builds a w x h RGBA image filled with c.
func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestNewDiffer(t *testing.T) {
	cases := []struct {
		name  string
		input float64
		want  float64
	}{
		{"zero defaults", 0, 0.01},
		{"negative defaults", -0.5, 0.01},
		{"small negative defaults", -0.0001, 0.01},
		{"positive preserved", 0.05, 0.05},
		{"large positive preserved", 0.5, 0.5},
		{"tiny positive preserved", 0.0001, 0.0001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDiffer(tc.input)
			require.NotNil(t, d)
			assert.Equal(t, tc.want, d.threshold)
		})
	}
}

func TestColorsEqual(t *testing.T) {
	// The tolerance constant in differ.go is 256 in 16-bit color space.
	// color.RGBA{} values are scaled to 16-bit by RGBA(): an 8-bit value v
	// becomes v*0x101 (e.g. 255 -> 0xffff, 1 -> 0x0101 = 257).
	// So a 1-step difference in 8-bit space is 257 > 256 tolerance -> NOT equal.
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}

	cases := []struct {
		name string
		c1   color.Color
		c2   color.Color
		want bool
	}{
		{
			name: "identical",
			c1:   color.RGBA{R: 100, G: 150, B: 200, A: 255},
			c2:   color.RGBA{R: 100, G: 150, B: 200, A: 255},
			want: true,
		},
		{
			// Gray16 lets us hit a 16-bit delta exactly at the tolerance boundary.
			name: "within 16-bit tolerance (delta 256)",
			c1:   color.Gray16{Y: 1000},
			c2:   color.Gray16{Y: 1000 + 256},
			want: true,
		},
		{
			name: "beyond 16-bit tolerance (delta 257)",
			c1:   color.Gray16{Y: 1000},
			c2:   color.Gray16{Y: 1000 + 257},
			want: false,
		},
		{
			name: "red channel differs beyond tolerance",
			c1:   black,
			c2:   color.RGBA{R: 50, G: 0, B: 0, A: 255},
			want: false,
		},
		{
			name: "green channel differs beyond tolerance",
			c1:   black,
			c2:   color.RGBA{R: 0, G: 50, B: 0, A: 255},
			want: false,
		},
		{
			name: "blue channel differs beyond tolerance",
			c1:   black,
			c2:   color.RGBA{R: 0, G: 0, B: 50, A: 255},
			want: false,
		},
		{
			// Alpha differs: RGBA() returns premultiplied values, so a lower
			// alpha on an otherwise-white pixel changes every channel. This
			// exercises the a1/a2 comparison path among others.
			name: "alpha channel differs",
			c1:   color.RGBA{R: 255, G: 255, B: 255, A: 255},
			c2:   color.RGBA{R: 255, G: 255, B: 255, A: 0},
			want: false,
		},
		{
			name: "fully opaque vs fully transparent black",
			c1:   color.RGBA{R: 0, G: 0, B: 0, A: 255},
			c2:   color.RGBA{R: 0, G: 0, B: 0, A: 0},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, colorsEqual(tc.c1, tc.c2))
			// Symmetry: order must not matter.
			assert.Equal(t, tc.want, colorsEqual(tc.c2, tc.c1))
		})
	}
}

func TestAbs(t *testing.T) {
	cases := []struct {
		a, b uint32
		want uint32
	}{
		{0, 0, 0},
		{5, 5, 0},
		{10, 3, 7},
		{3, 10, 7},
		{0, 100, 100},
		{100, 0, 100},
		{0xffffffff, 0, 0xffffffff},
	}
	for _, tc := range cases {
		// Result correct regardless of argument order.
		assert.Equal(t, tc.want, abs(tc.a, tc.b))
		assert.Equal(t, tc.want, abs(tc.b, tc.a))
		// Explicit symmetry assertion.
		assert.Equal(t, abs(tc.a, tc.b), abs(tc.b, tc.a))
	}
}

func TestCompareIdentical(t *testing.T) {
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	base := filepath.Join(dir, "base.png")
	cur := filepath.Join(dir, "cur.png")
	writePNG(t, base, solidImage(10, 8, white))
	writePNG(t, cur, solidImage(10, 8, white))

	d := NewDiffer(0.01)
	ratio, diff, err := d.Compare(base, cur)
	require.NoError(t, err)
	require.NotNil(t, diff)
	assert.Equal(t, 0.0, ratio)
	assert.Equal(t, 10, diff.Bounds().Dx())
	assert.Equal(t, 8, diff.Bounds().Dy())
	// Identical pixels are rendered dimmed (not the red diff marker).
	r, g, b, _ := diff.At(0, 0).RGBA()
	assert.NotEqual(t, uint32(0xffff), r, "unchanged pixel should be dimmed, not full-red")
	assert.False(t, r == 0xffff && g == 0 && b == 0, "unchanged pixel must not be the red diff color")
}

func TestCompareFullyDifferent(t *testing.T) {
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	black := color.RGBA{R: 0, G: 0, B: 0, A: 255}
	base := filepath.Join(dir, "base.png")
	cur := filepath.Join(dir, "cur.png")
	writePNG(t, base, solidImage(6, 6, white))
	writePNG(t, cur, solidImage(6, 6, black))

	d := NewDiffer(0.01)
	ratio, diff, err := d.Compare(base, cur)
	require.NoError(t, err)
	require.NotNil(t, diff)
	assert.Equal(t, 1.0, ratio)
	// Every pixel should be the red diff marker.
	for y := 0; y < 6; y++ {
		for x := 0; x < 6; x++ {
			r, g, b, a := diff.At(x, y).RGBA()
			require.Equal(t, uint32(0xffff), r, "x=%d y=%d red", x, y)
			require.Equal(t, uint32(0), g, "x=%d y=%d green", x, y)
			require.Equal(t, uint32(0), b, "x=%d y=%d blue", x, y)
			require.Equal(t, uint32(0xffff), a, "x=%d y=%d alpha", x, y)
		}
	}
}

func TestCompareKnownChangedPixels(t *testing.T) {
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	red := color.RGBA{R: 255, G: 0, B: 0, A: 255}

	// 4x5 = 20 pixels. Change exactly 3 pixels -> ratio 3/20 = 0.15.
	const w, h = 4, 5
	base := solidImage(w, h, white)
	cur := solidImage(w, h, white)
	changed := [][2]int{{0, 0}, {3, 4}, {2, 2}}
	for _, p := range changed {
		cur.Set(p[0], p[1], red)
	}

	basePath := filepath.Join(dir, "base.png")
	curPath := filepath.Join(dir, "cur.png")
	writePNG(t, basePath, base)
	writePNG(t, curPath, cur)

	d := NewDiffer(0.01)
	ratio, diff, err := d.Compare(basePath, curPath)
	require.NoError(t, err)
	require.NotNil(t, diff)
	assert.InDelta(t, 3.0/20.0, ratio, 1e-9)

	// Each changed pixel is red in the diff; unchanged are not.
	isRed := func(x, y int) bool {
		r, g, b, a := diff.At(x, y).RGBA()
		return r == 0xffff && g == 0 && b == 0 && a == 0xffff
	}
	for _, p := range changed {
		assert.True(t, isRed(p[0], p[1]), "changed pixel (%d,%d) must be red", p[0], p[1])
	}
	// A pixel we did not change must not be red.
	assert.False(t, isRed(1, 1), "unchanged pixel (1,1) must not be red")
}

// TestCompareIgnoreRegionExcludesChangedPixels proves an ignore region drops
// a changed pixel from both the diff numerator AND the totalPixels
// denominator — not just skips flagging it, which would still deflate the
// ratio by leaving totalPixels unchanged.
func TestCompareIgnoreRegionExcludesChangedPixels(t *testing.T) {
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	red := color.RGBA{R: 255, G: 0, B: 0, A: 255}

	// 4x5 = 20 pixels. Change 3 pixels; one of them, (0,0), sits inside an
	// ignore region covering the top-left 2x2 block.
	const w, h = 4, 5
	base := solidImage(w, h, white)
	cur := solidImage(w, h, white)
	changed := [][2]int{{0, 0}, {3, 4}, {2, 2}}
	for _, p := range changed {
		cur.Set(p[0], p[1], red)
	}

	basePath := filepath.Join(dir, "base.png")
	curPath := filepath.Join(dir, "cur.png")
	writePNG(t, basePath, base)
	writePNG(t, curPath, cur)

	d := NewDiffer(0.01)
	region := IgnoreRegion{Selector: ".clock", Width: 2, Height: 2} // covers (0,0),(1,0),(0,1),(1,1)

	ratio, diff, err := d.Compare(basePath, curPath, region)
	require.NoError(t, err)
	require.NotNil(t, diff)
	// 1 remaining diff pixel (2,2) or (3,4) each, minus 4 excluded pixels:
	// 2 diffs / 16 pixels = 0.125, vs 3/20 = 0.15 without the region.
	assert.InDelta(t, 2.0/16.0, ratio, 1e-9)

	// The excluded pixel is neither flagged red nor left as an unmarked
	// mismatch — it's rendered dimmed, same treatment as a true match.
	r, g, b, _ := diff.At(0, 0).RGBA()
	assert.False(t, r == 0xffff && g == 0 && b == 0, "excluded pixel (0,0) must not be flagged red")
}

// TestCompareIgnoreRegionOutsideBoundsIsNoop confirms a region that covers
// nothing (zero width/height, or positioned off-image) changes nothing.
func TestCompareIgnoreRegionOutsideBoundsIsNoop(t *testing.T) {
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	red := color.RGBA{R: 255, G: 0, B: 0, A: 255}

	const w, h = 4, 5
	base := solidImage(w, h, white)
	cur := solidImage(w, h, white)
	cur.Set(1, 1, red)

	basePath := filepath.Join(dir, "base.png")
	curPath := filepath.Join(dir, "cur.png")
	writePNG(t, basePath, base)
	writePNG(t, curPath, cur)

	d := NewDiffer(0.01)

	withNoop, _, err := d.Compare(basePath, curPath, IgnoreRegion{Width: 0, Height: 0}, IgnoreRegion{X: 100, Y: 100, Width: 5, Height: 5})
	require.NoError(t, err)
	withNone, _, err := d.Compare(basePath, curPath)
	require.NoError(t, err)
	assert.Equal(t, withNone, withNoop)
}

func TestIgnoreRegionContains(t *testing.T) {
	r := IgnoreRegion{X: 2, Y: 3, Width: 4, Height: 5}
	assert.True(t, r.Contains(2, 3), "top-left corner is inclusive")
	assert.True(t, r.Contains(5, 7), "bottom-right-1 is inclusive")
	assert.False(t, r.Contains(6, 3), "x at X+Width is exclusive")
	assert.False(t, r.Contains(2, 8), "y at Y+Height is exclusive")
	assert.False(t, r.Contains(1, 3), "x before X is outside")

	zero := IgnoreRegion{X: 0, Y: 0, Width: 0, Height: 10}
	assert.False(t, zero.Contains(0, 0), "zero width matches nothing")
}

func TestCompareDimensionMismatch(t *testing.T) {
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	base := filepath.Join(dir, "base.png")
	cur := filepath.Join(dir, "cur.png")
	writePNG(t, base, solidImage(10, 10, white))
	writePNG(t, cur, solidImage(10, 12, white))

	d := NewDiffer(0.01)
	ratio, diff, err := d.Compare(base, cur)
	require.Error(t, err)
	assert.Nil(t, diff)
	// Dimension mismatch returns 1.0 (treated as maximal difference).
	assert.Equal(t, 1.0, ratio)
	assert.Contains(t, err.Error(), "dimensions differ")
}

func TestCompareLoadErrors(t *testing.T) {
	dir := t.TempDir()
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	good := filepath.Join(dir, "good.png")
	writePNG(t, good, solidImage(4, 4, white))

	d := NewDiffer(0.01)

	t.Run("missing baseline", func(t *testing.T) {
		ratio, diff, err := d.Compare(filepath.Join(dir, "nope.png"), good)
		require.Error(t, err)
		assert.Nil(t, diff)
		assert.Equal(t, 0.0, ratio)
		assert.Contains(t, err.Error(), "load baseline")
	})

	t.Run("missing current", func(t *testing.T) {
		ratio, diff, err := d.Compare(good, filepath.Join(dir, "nope.png"))
		require.Error(t, err)
		assert.Nil(t, diff)
		assert.Equal(t, 0.0, ratio)
		assert.Contains(t, err.Error(), "load current")
	})

	t.Run("corrupt baseline data", func(t *testing.T) {
		bad := filepath.Join(dir, "bad.png")
		require.NoError(t, os.WriteFile(bad, []byte("not a png"), 0o644))
		ratio, diff, err := d.Compare(bad, good)
		require.Error(t, err)
		assert.Nil(t, diff)
		assert.Equal(t, 0.0, ratio)
		assert.Contains(t, err.Error(), "load baseline")
	})
}

func TestHasSignificantChanges(t *testing.T) {
	d := NewDiffer(0.05)
	cases := []struct {
		name string
		diff float64
		want bool
	}{
		{"below threshold", 0.04, false},
		{"well below threshold", 0.0, false},
		{"exactly at threshold is not significant", 0.05, false},
		{"just above threshold", 0.0500001, true},
		{"well above threshold", 0.5, true},
		{"max diff", 1.0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, d.HasSignificantChanges(tc.diff))
		})
	}
}

func TestSaveDiffImageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := solidImage(5, 3, color.RGBA{R: 12, G: 34, B: 56, A: 255})
	out := filepath.Join(dir, "diff.png")

	d := NewDiffer(0.01)
	require.NoError(t, d.SaveDiffImage(src, out))

	// File exists and is non-empty.
	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))

	// Decode back and verify dimensions + a sample pixel survive the round-trip.
	loaded, err := loadImage(out)
	require.NoError(t, err)
	assert.Equal(t, 5, loaded.Bounds().Dx())
	assert.Equal(t, 3, loaded.Bounds().Dy())
	wr, wg, wb, wa := src.At(2, 1).RGBA()
	gr, gg, gb, ga := loaded.At(2, 1).RGBA()
	assert.Equal(t, wr, gr)
	assert.Equal(t, wg, gg)
	assert.Equal(t, wb, gb)
	assert.Equal(t, wa, ga)
}

func TestSaveDiffImageErrors(t *testing.T) {
	dir := t.TempDir()
	d := NewDiffer(0.01)
	img := solidImage(2, 2, color.RGBA{R: 1, G: 2, B: 3, A: 255})

	// Path whose parent directory does not exist cannot be created.
	bad := filepath.Join(dir, "no-such-dir", "diff.png")
	err := d.SaveDiffImage(img, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create file")

	// Path that is an existing directory also cannot be opened as a file.
	errDir := d.SaveDiffImage(img, dir)
	require.Error(t, errDir)
}

func TestLoadImageErrors(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, err := loadImage(filepath.Join(dir, "absent.png"))
		require.Error(t, err)
		assert.True(t, os.IsNotExist(err), "expected not-exist error, got %v", err)
	})

	t.Run("corrupt data", func(t *testing.T) {
		bad := filepath.Join(dir, "corrupt.png")
		require.NoError(t, os.WriteFile(bad, []byte("\x89PNG\r\n garbage"), 0o644))
		_, err := loadImage(bad)
		require.Error(t, err)
	})

	t.Run("valid png decodes", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, solidImage(3, 3, color.RGBA{R: 9, G: 9, B: 9, A: 255})))
		valid := filepath.Join(dir, "valid.png")
		require.NoError(t, os.WriteFile(valid, buf.Bytes(), 0o644))
		img, err := loadImage(valid)
		require.NoError(t, err)
		require.NotNil(t, img)
		assert.Equal(t, 3, img.Bounds().Dx())
		assert.Equal(t, 3, img.Bounds().Dy())
	})
}

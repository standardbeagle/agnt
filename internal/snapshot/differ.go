package snapshot

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// Differ handles image comparison
type Differ struct {
	threshold float64
}

// NewDiffer creates a new image differ
func NewDiffer(threshold float64) *Differ {
	if threshold <= 0 {
		threshold = 0.01 // Default 1% difference threshold
	}
	return &Differ{threshold: threshold}
}

// Compare compares two images and returns diff percentage and diff image.
// Pixels falling inside any of ignoreRegions are excluded from both the
// pixel-diff count and totalPixels, so a region that legitimately changes
// between captures (a clock, an ad slot) never contributes to
// diffPercentage; the diff image still renders them dimmed, not red, so a
// human reviewing the image can see what was excluded and why.
func (d *Differ) Compare(baselinePath, currentPath string, ignoreRegions ...IgnoreRegion) (float64, image.Image, error) {
	// Load baseline image
	baseline, err := loadImage(baselinePath)
	if err != nil {
		return 0, nil, fmt.Errorf("load baseline: %w", err)
	}

	// Load current image
	current, err := loadImage(currentPath)
	if err != nil {
		return 0, nil, fmt.Errorf("load current: %w", err)
	}

	// Check dimensions match
	baselineBounds := baseline.Bounds()
	currentBounds := current.Bounds()

	if baselineBounds.Dx() != currentBounds.Dx() || baselineBounds.Dy() != currentBounds.Dy() {
		return 1.0, nil, fmt.Errorf("image dimensions differ: baseline %dx%d vs current %dx%d",
			baselineBounds.Dx(), baselineBounds.Dy(),
			currentBounds.Dx(), currentBounds.Dy())
	}

	// Generate diff image
	diff := image.NewRGBA(baselineBounds)
	totalPixels := baselineBounds.Dx() * baselineBounds.Dy()
	diffPixels := 0

	for y := baselineBounds.Min.Y; y < baselineBounds.Max.Y; y++ {
		for x := baselineBounds.Min.X; x < baselineBounds.Max.X; x++ {
			baselineColor := baseline.At(x, y)
			currentColor := current.At(x, y)

			if inAnyRegion(ignoreRegions, x, y) {
				// Excluded region: render dimmed like a match, but don't
				// count it toward totalPixels or diffPixels either way —
				// it must not affect diffPercentage regardless of whether
				// the pixels actually match.
				r, g, b, a := currentColor.RGBA()
				diff.Set(x, y, color.RGBA{
					R: uint8(r >> 9),
					G: uint8(g >> 9),
					B: uint8(b >> 9),
					A: uint8(a >> 8),
				})
				totalPixels--
				continue
			}

			if colorsEqual(baselineColor, currentColor) {
				// No difference - show current pixel dimmed
				r, g, b, a := currentColor.RGBA()
				diff.Set(x, y, color.RGBA{
					R: uint8(r >> 9), // Darken
					G: uint8(g >> 9),
					B: uint8(b >> 9),
					A: uint8(a >> 8),
				})
			} else {
				// Difference detected - highlight in red
				diff.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
				diffPixels++
			}
		}
	}

	diffPercentage := 0.0
	if totalPixels > 0 {
		diffPercentage = float64(diffPixels) / float64(totalPixels)
	}

	return diffPercentage, diff, nil
}

// SaveDiffImage saves a diff image to disk
func (d *Differ) SaveDiffImage(img image.Image, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		return fmt.Errorf("encode png: %w", err)
	}

	return nil
}

// HasSignificantChanges checks if diff percentage exceeds threshold
func (d *Differ) HasSignificantChanges(diffPercentage float64) bool {
	return diffPercentage > d.threshold
}

// Helper functions

func loadImage(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	return img, nil
}

func colorsEqual(c1, c2 color.Color) bool {
	r1, g1, b1, a1 := c1.RGBA()
	r2, g2, b2, a2 := c2.RGBA()

	// Allow small tolerance (1 unit in 16-bit color space)
	const tolerance uint32 = 256

	return abs(r1, r2) <= tolerance &&
		abs(g1, g2) <= tolerance &&
		abs(b1, b2) <= tolerance &&
		abs(a1, a2) <= tolerance
}

// inAnyRegion reports whether (x, y) falls inside any of regions.
func inAnyRegion(regions []IgnoreRegion, x, y int) bool {
	for _, r := range regions {
		if r.Contains(x, y) {
			return true
		}
	}
	return false
}

func abs(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

package chromedp

import (
	"context"
	"strings"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// ViewportPreset defines a standard viewport configuration for responsive testing.
type ViewportPreset struct {
	Name   string  // Preset name (e.g., "mobile-portrait")
	Width  int64   // Viewport width in pixels
	Height int64   // Viewport height in pixels
	Scale  float64 // Device scale factor (e.g., 2.0 for retina)
	Mobile bool    // Emulate mobile device
	Touch  bool    // Enable touch events
}

// DefaultViewports contains the 6 standard viewport presets for responsive testing.
var DefaultViewports = []ViewportPreset{
	{
		Name:   "mobile-portrait",
		Width:  375,
		Height: 667,
		Scale:  2.0,
		Mobile: true,
		Touch:  true,
	},
	{
		Name:   "mobile-landscape",
		Width:  667,
		Height: 375,
		Scale:  2.0,
		Mobile: true,
		Touch:  true,
	},
	{
		Name:   "tablet-portrait",
		Width:  768,
		Height: 1024,
		Scale:  2.0,
		Mobile: true,
		Touch:  true,
	},
	{
		Name:   "tablet-landscape",
		Width:  1024,
		Height: 768,
		Scale:  2.0,
		Mobile: true,
		Touch:  true,
	},
	{
		Name:   "desktop",
		Width:  1280,
		Height: 800,
		Scale:  1.0,
		Mobile: false,
		Touch:  false,
	},
	{
		Name:   "desktop-large",
		Width:  1920,
		Height: 1080,
		Scale:  1.0,
		Mobile: false,
		Touch:  false,
	},
}

// GetViewport retrieves a viewport preset by name with fuzzy matching.
// It supports exact matches, partial matches, and common aliases.
// Returns nil if no matching preset is found.
func GetViewport(name string) *ViewportPreset {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}

	// Define aliases for common terms
	aliases := map[string]string{
		"phone":           "mobile-portrait",
		"mobile":          "mobile-portrait",
		"phone-portrait":  "mobile-portrait",
		"phone-landscape": "mobile-landscape",
		"iphone":          "mobile-portrait",
		"android":         "mobile-portrait",
		"tablet":          "tablet-portrait",
		"ipad":            "tablet-portrait",
		"ipad-portrait":   "tablet-portrait",
		"ipad-landscape":  "tablet-landscape",
		"laptop":          "desktop",
		"1080p":           "desktop-large",
		"fullhd":          "desktop-large",
		"hd":              "desktop-large",
	}

	// Check aliases first
	if aliased, ok := aliases[name]; ok {
		name = aliased
	}

	// Try exact match
	for i := range DefaultViewports {
		if DefaultViewports[i].Name == name {
			return &DefaultViewports[i]
		}
	}

	// Try partial match (contains)
	for i := range DefaultViewports {
		if strings.Contains(DefaultViewports[i].Name, name) {
			return &DefaultViewports[i]
		}
	}

	// Try reverse partial match (input contains preset name part)
	for i := range DefaultViewports {
		parts := strings.Split(DefaultViewports[i].Name, "-")
		for _, part := range parts {
			if strings.Contains(name, part) {
				return &DefaultViewports[i]
			}
		}
	}

	return nil
}

// GetViewportByDimensions finds a preset closest to the given dimensions.
// Returns nil if no reasonably close preset is found.
func GetViewportByDimensions(width, height int64) *ViewportPreset {
	var best *ViewportPreset
	bestDiff := int64(^uint64(0) >> 1) // Max int64

	for i := range DefaultViewports {
		v := &DefaultViewports[i]
		diff := abs(v.Width-width) + abs(v.Height-height)
		if diff < bestDiff {
			bestDiff = diff
			best = v
		}
	}

	// Only return if within reasonable tolerance (200px total)
	if bestDiff <= 200 {
		return best
	}
	return nil
}

// ListViewportNames returns the names of all available viewport presets.
func ListViewportNames() []string {
	names := make([]string, len(DefaultViewports))
	for i, v := range DefaultViewports {
		names[i] = v.Name
	}
	return names
}

// ToEmulateActions converts a ViewportPreset to chromedp emulation actions.
// It uses CDP commands directly to properly set mobile and touch emulation.
func (v *ViewportPreset) ToEmulateActions() chromedp.Tasks {
	tasks := chromedp.Tasks{
		// Set device metrics with mobile flag
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetDeviceMetricsOverride(v.Width, v.Height, v.Scale, v.Mobile).Do(ctx)
		}),
	}

	// Enable touch emulation if needed
	if v.Touch {
		tasks = append(tasks, chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetTouchEmulationEnabled(true).Do(ctx)
		}))
	}

	return tasks
}

// abs returns the absolute value of an int64.
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

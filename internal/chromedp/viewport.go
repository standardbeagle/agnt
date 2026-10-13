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

// emulationStep is one CDP emulation command a preset issues, in a form a test
// can inspect without a browser. Exactly one of the two shapes is used:
// metrics != nil sets (or, with clear, removes) the device-metrics override;
// touch != nil sets touch emulation to that value.
type emulationStep struct {
	metrics *ViewportPreset
	clear   bool
	touch   *bool
}

// emulateSteps is what applying the preset sends. Touch emulation is set
// EXPLICITLY from the preset every time, in both directions: it is browser
// state that persists across commands, so a preset that only ever turned it
// on left every later desktop capture in the session running with a coarse
// pointer and touch events.
func (v *ViewportPreset) emulateSteps() []emulationStep {
	touch := v.Touch
	return []emulationStep{
		{metrics: v},
		{touch: &touch},
	}
}

// resetEmulationSteps is what returning the session to its un-emulated state
// sends: no device-metrics override (the window's own size applies again) and
// touch off. Nothing in this package installs a persistent override, so "the
// state before a capture" is always this one.
func resetEmulationSteps() []emulationStep {
	off := false
	return []emulationStep{
		{clear: true},
		{touch: &off},
	}
}

func (s emulationStep) action() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		switch {
		case s.clear:
			return emulation.ClearDeviceMetricsOverride().Do(ctx)
		case s.metrics != nil:
			m := s.metrics
			return emulation.SetDeviceMetricsOverride(m.Width, m.Height, m.Scale, m.Mobile).Do(ctx)
		case s.touch != nil:
			return emulation.SetTouchEmulationEnabled(*s.touch).Do(ctx)
		}
		return nil
	})
}

func stepsToTasks(steps []emulationStep) chromedp.Tasks {
	tasks := make(chromedp.Tasks, 0, len(steps))
	for _, s := range steps {
		tasks = append(tasks, s.action())
	}
	return tasks
}

// ToEmulateActions converts a ViewportPreset to chromedp emulation actions.
// It uses CDP commands directly to properly set mobile and touch emulation.
// The emulation persists until ResetEmulationActions runs.
func (v *ViewportPreset) ToEmulateActions() chromedp.Tasks {
	return stepsToTasks(v.emulateSteps())
}

// ResetEmulationActions returns the actions that undo ToEmulateActions.
func ResetEmulationActions() chromedp.Tasks {
	return stepsToTasks(resetEmulationSteps())
}

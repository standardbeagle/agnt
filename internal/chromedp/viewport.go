package chromedp

import (
	"context"
	"fmt"
	"sort"
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

// viewportAliases maps common device words to a preset name.
var viewportAliases = map[string]string{
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

// minViewportPrefix is the shortest abbreviation GetViewport accepts. Shorter
// input is too likely to be a typo or a stray letter to be read as a choice
// of device: "p" used to select a preset by substring match.
const minViewportPrefix = 3

// GetViewport resolves a viewport preset by name. It accepts a preset name, an
// alias (phone, tablet, laptop, …), or a prefix of a preset name at least
// minViewportPrefix characters long that matches exactly one preset ("tablet-l").
// Anything else — an unknown, too short or ambiguous name — is an error that
// lists the valid names, never a guess.
func GetViewport(name string) (*ViewportPreset, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil, fmt.Errorf("viewport name is empty; %s", validViewportNames())
	}
	if aliased, ok := viewportAliases[key]; ok {
		key = aliased
	}
	for i := range DefaultViewports {
		if DefaultViewports[i].Name == key {
			return &DefaultViewports[i], nil
		}
	}

	if len(key) >= minViewportPrefix {
		var matches []*ViewportPreset
		for i := range DefaultViewports {
			if strings.HasPrefix(DefaultViewports[i].Name, key) {
				matches = append(matches, &DefaultViewports[i])
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
		default:
			names := make([]string, len(matches))
			for i, m := range matches {
				names[i] = m.Name
			}
			return nil, fmt.Errorf("viewport %q is ambiguous (matches %s); %s",
				name, strings.Join(names, ", "), validViewportNames())
		}
	}
	return nil, fmt.Errorf("unknown viewport %q; %s", name, validViewportNames())
}

// validViewportNames is the error suffix naming every accepted spelling.
func validViewportNames() string {
	presets := make([]string, len(DefaultViewports))
	for i := range DefaultViewports {
		presets[i] = DefaultViewports[i].Name
	}
	aliases := make([]string, 0, len(viewportAliases))
	for a := range viewportAliases {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	return fmt.Sprintf("valid viewports: %s (aliases: %s; a unique prefix of at least %d characters also works)",
		strings.Join(presets, ", "), strings.Join(aliases, ", "), minViewportPrefix)
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

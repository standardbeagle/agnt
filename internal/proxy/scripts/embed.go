// Package scripts provides embedded JavaScript for the DevTool proxy instrumentation.
package scripts

import (
	_ "embed"
	"strings"
	"sync"
)

// Individual script files embedded at compile time
var (
	//go:embed core.js
	coreJS string

	//go:embed utils.js
	utilsJS string

	//go:embed overlay.js
	overlayJS string

	//go:embed inspection.js
	inspectionJS string

	//go:embed tree.js
	treeJS string

	//go:embed visual.js
	visualJS string

	//go:embed layout.js
	layoutJS string

	//go:embed interactive.js
	interactiveJS string

	//go:embed capture.js
	captureJS string

	//go:embed accessibility.js
	accessibilityJS string

	//go:embed audit.js
	auditJS string

	//go:embed interaction.js
	interactionJS string

	//go:embed mutation.js
	mutationJS string

	//go:embed toast.js
	toastJS string

	//go:embed indicator.js
	indicatorJS string

	//go:embed sketch.js
	sketchJS string

	//go:embed design.js
	designJS string

	//go:embed voice.js
	voiceJS string

	//go:embed snapshot-helper.js
	snapshotHelperJS string

	//go:embed diagnostics.js
	diagnosticsJS string

	//go:embed api.js
	apiJS string
)

var (
	combinedScript     string
	combinedScriptOnce sync.Once
)

// GetCombinedScript returns all JavaScript modules combined into a single script.
// The script is wrapped in appropriate tags and ordered for correct initialization.
// The result is cached after first call.
func GetCombinedScript() string {
	combinedScriptOnce.Do(func() {
		combinedScript = buildCombinedScript()
	})
	return combinedScript
}

// buildCombinedScript assembles all modules in the correct order.
func buildCombinedScript() string {
	var sb strings.Builder

	// External dependencies (html2canvas-pro for screenshots)
	// Using html2canvas-pro instead of html2canvas because it supports modern CSS
	// color functions (lab, oklch, oklab, lch) that Firefox and modern browsers use.
	// See: https://github.com/nickt26/html2canvas-pro
	sb.WriteString(`<script src="https://cdn.jsdelivr.net/npm/html2canvas-pro@1.5.8/dist/html2canvas-pro.min.js"></script>`)
	sb.WriteString("\n")

	// Main script block
	sb.WriteString("<script>\n")
	sb.WriteString("(function() {\n")
	sb.WriteString("  'use strict';\n\n")

	// Order matters: dependencies must load before dependents
	// 1. Core (WebSocket, send function)
	sb.WriteString("  // Core module\n")
	sb.WriteString(wrapModule(coreJS))
	sb.WriteString("\n\n")

	// 2. Utils (shared helpers)
	sb.WriteString("  // Utils module\n")
	sb.WriteString(wrapModule(utilsJS))
	sb.WriteString("\n\n")

	// 3. Overlay (visual system, depends on utils)
	sb.WriteString("  // Overlay module\n")
	sb.WriteString(wrapModule(overlayJS))
	sb.WriteString("\n\n")

	// 4. Inspection (depends on utils)
	sb.WriteString("  // Inspection module\n")
	sb.WriteString(wrapModule(inspectionJS))
	sb.WriteString("\n\n")

	// 5. Tree (depends on utils)
	sb.WriteString("  // Tree module\n")
	sb.WriteString(wrapModule(treeJS))
	sb.WriteString("\n\n")

	// 6. Visual (depends on utils)
	sb.WriteString("  // Visual module\n")
	sb.WriteString(wrapModule(visualJS))
	sb.WriteString("\n\n")

	// 7. Layout (depends on utils, inspection, visual)
	sb.WriteString("  // Layout module\n")
	sb.WriteString(wrapModule(layoutJS))
	sb.WriteString("\n\n")

	// 8. Interactive (depends on utils)
	sb.WriteString("  // Interactive module\n")
	sb.WriteString(wrapModule(interactiveJS))
	sb.WriteString("\n\n")

	// 9. Capture (depends on utils)
	sb.WriteString("  // Capture module\n")
	sb.WriteString(wrapModule(captureJS))
	sb.WriteString("\n\n")

	// 10. Accessibility (depends on utils)
	sb.WriteString("  // Accessibility module\n")
	sb.WriteString(wrapModule(accessibilityJS))
	sb.WriteString("\n\n")

	// 11. Audit (depends on utils)
	sb.WriteString("  // Audit module\n")
	sb.WriteString(wrapModule(auditJS))
	sb.WriteString("\n\n")

	// 12. Interaction tracking (depends on utils, core)
	sb.WriteString("  // Interaction tracking module\n")
	sb.WriteString(wrapModule(interactionJS))
	sb.WriteString("\n\n")

	// 13. Mutation tracking (depends on utils, core)
	sb.WriteString("  // Mutation tracking module\n")
	sb.WriteString(wrapModule(mutationJS))
	sb.WriteString("\n\n")

	// 14. Toast notifications (no dependencies)
	sb.WriteString("  // Toast notification module\n")
	sb.WriteString(wrapModule(toastJS))
	sb.WriteString("\n\n")

	// 15. Voice transcription (depends on core)
	sb.WriteString("  // Voice transcription module\n")
	sb.WriteString(wrapModule(voiceJS))
	sb.WriteString("\n\n")

	// 16. Sketch mode (depends on core, voice)
	sb.WriteString("  // Sketch mode module\n")
	sb.WriteString(wrapModule(sketchJS))
	sb.WriteString("\n\n")

	// 17. Design mode (depends on core, utils)
	sb.WriteString("  // Design mode module\n")
	sb.WriteString(wrapModule(designJS))
	sb.WriteString("\n\n")

	// 18. Floating indicator (depends on core, utils, sketch, design, toast)
	sb.WriteString("  // Floating indicator module\n")
	sb.WriteString(wrapModule(indicatorJS))
	sb.WriteString("\n\n")

	// 19. Snapshot helper (depends on core)
	sb.WriteString("  // Snapshot helper module\n")
	sb.WriteString(wrapModule(snapshotHelperJS))
	sb.WriteString("\n\n")

	// 20. Diagnostics (depends on utils, core)
	sb.WriteString("  // Diagnostics module\n")
	sb.WriteString(wrapModule(diagnosticsJS))
	sb.WriteString("\n\n")

	// 21. API (assembles all modules, must be last)
	sb.WriteString("  // API assembly module\n")
	sb.WriteString(wrapModule(apiJS))
	sb.WriteString("\n")

	sb.WriteString("})();\n")
	sb.WriteString("</script>\n")

	return sb.String()
}

// wrapModule removes the outer IIFE from a module since we're wrapping everything
// in a single IIFE. It also indents the code for readability.
func wrapModule(js string) string {
	// Remove leading/trailing whitespace
	js = strings.TrimSpace(js)

	// Remove outer IIFE wrapper if present: (function() { ... })();
	if strings.HasPrefix(js, "(function()") {
		// Find the matching closing
		depth := 0
		start := -1
		end := -1

		for i, c := range js {
			if c == '{' {
				if start == -1 {
					start = i + 1
				}
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}

		if start != -1 && end != -1 {
			// Extract content between braces
			content := js[start:end]
			// Remove 'use strict' if present (we add it at the top level)
			content = strings.Replace(content, "'use strict';", "", 1)
			content = strings.Replace(content, `"use strict";`, "", 1)
			return strings.TrimSpace(content)
		}
	}

	return js
}

// GetScriptNames returns the list of embedded script names for debugging.
func GetScriptNames() []string {
	return []string{
		"core.js",
		"utils.js",
		"overlay.js",
		"inspection.js",
		"tree.js",
		"visual.js",
		"layout.js",
		"interactive.js",
		"capture.js",
		"accessibility.js",
		"audit.js",
		"interaction.js",
		"mutation.js",
		"toast.js",
		"voice.js",
		"sketch.js",
		"design.js",
		"indicator.js",
		"snapshot-helper.js",
		"diagnostics.js",
		"api.js",
	}
}

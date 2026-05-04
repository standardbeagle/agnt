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

	//go:embed shadow-root.js
	shadowRootJS string

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

	//go:embed audit-utils.js
	auditUtilsJS string

	//go:embed audit-report.js
	auditReportJS string

	//go:embed audit-dom.js
	auditDomJS string

	//go:embed audit-css.js
	auditCssJS string

	//go:embed audit-security.js
	auditSecurityJS string

	//go:embed audit-performance.js
	auditPerformanceJS string

	//go:embed audit-quality.js
	auditQualityJS string

	//go:embed interaction.js
	interactionJS string

	//go:embed framework-detector.js
	frameworkDetectorJS string

	//go:embed api-tracker.js
	apiTrackerJS string

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

	//go:embed palette.js
	paletteJS string

	//go:embed style-editor.js
	styleEditorJS string

	//go:embed voice.js
	voiceJS string

	//go:embed snapshot-helper.js
	snapshotHelperJS string

	//go:embed diagnostics.js
	diagnosticsJS string

	//go:embed session.js
	sessionJS string

	//go:embed store.js
	storeJS string

	//go:embed content.js
	contentJS string

	//go:embed text-fragility.js
	textFragilityJS string

	//go:embed responsive-risk.js
	responsiveRiskJS string

	//go:embed wireframe.js
	wireframeJS string

	//go:embed responsive.js
	responsiveJS string

	//go:embed api.js
	apiJS string

	//go:embed html2canvas-pro.min.js
	html2canvasProJS string

	//go:embed axe-core.min.js
	axeCoreJS string
)

var (
	combinedScript     string
	combinedScriptOnce sync.Once
)

// Version is the agnt version to inject into scripts.
// Set this before calling GetCombinedScript() for the first time.
var Version = "dev"

// moduleEntry declares a JS module and its hard dependencies.
// Dependencies must appear earlier in moduleOrder.
type moduleEntry struct {
	name string
	deps []string
}

// moduleOrder defines the load order for all embedded modules.
// Each entry names the module and lists modules it requires at load time.
// The buildCombinedScript function iterates this slice instead of a
// hardcoded sequence so that the dependency declarations are authoritative
// and verified at build time by TestModuleDependencyOrder.
var moduleOrder = []moduleEntry{
	{"core", nil},
	// shadow-root.js MUST be loaded before any module that mounts UI so that
	// window.__devtoolGetMountRoot() is available when indicator/overlay init.
	// It has no dependencies of its own — pure DOM bootstrap.
	{"shadow-root", nil},
	{"framework-detector", nil},
	{"api-tracker", nil},
	{"utils", nil},
	{"overlay", []string{"utils", "shadow-root"}},
	{"inspection", []string{"utils"}},
	{"tree", []string{"utils"}},
	{"visual", []string{"utils"}},
	{"layout", []string{"utils", "inspection", "visual"}},
	{"interactive", []string{"utils"}},
	{"capture", []string{"utils"}},
	{"accessibility", []string{"utils"}},
	{"audit-utils", nil},
	{"audit-report", nil},
	{"audit-dom", []string{"utils"}},
	{"audit-css", []string{"utils"}},
	{"audit-security", []string{"utils", "audit-utils"}},
	{"audit-performance", []string{"utils", "audit-utils"}},
	{"audit-quality", []string{"utils", "audit-dom", "audit-css", "audit-security", "audit-performance"}},
	{"interaction", []string{"utils", "core"}},
	{"mutation", []string{"utils", "core"}},
	{"toast", nil},
	{"voice", []string{"core"}},
	{"sketch", []string{"core", "voice"}},
	{"design", []string{"core", "utils"}},
	{"palette", []string{"core", "utils"}},
	{"style-editor", []string{"core", "utils"}},
	{"indicator", []string{"core", "utils", "sketch", "design", "style-editor", "toast", "framework-detector", "api-tracker", "shadow-root"}},
	{"snapshot-helper", []string{"core"}},
	{"diagnostics", []string{"utils", "core"}},
	{"session", []string{"core"}},
	{"store", []string{"core"}},
	{"content", []string{"utils"}},
	{"text-fragility", []string{"utils"}},
	{"responsive-risk", []string{"utils"}},
	{"wireframe", []string{"utils"}},
	{"responsive", []string{"utils"}},
	{"api", []string{
		"core", "utils", "overlay", "inspection", "tree", "visual",
		"layout", "interactive", "capture", "accessibility",
		"audit-quality", "audit-report", "interaction", "mutation",
		"voice", "indicator", "sketch", "design", "palette",
		"diagnostics", "session", "store", "content",
		"wireframe", "responsive", "style-editor",
		"text-fragility", "responsive-risk", "snapshot-helper", "toast",
	}},
}

// moduleScript maps module names to their embedded JS variables.
var moduleScript = map[string]string{
	"core":               coreJS,
	"shadow-root":        shadowRootJS,
	"framework-detector": frameworkDetectorJS,
	"api-tracker":        apiTrackerJS,
	"utils":              utilsJS,
	"overlay":            overlayJS,
	"inspection":         inspectionJS,
	"tree":               treeJS,
	"visual":             visualJS,
	"layout":             layoutJS,
	"interactive":        interactiveJS,
	"capture":            captureJS,
	"accessibility":      accessibilityJS,
	"audit-utils":        auditUtilsJS,
	"audit-report":       auditReportJS,
	"audit-dom":          auditDomJS,
	"audit-css":          auditCssJS,
	"audit-security":     auditSecurityJS,
	"audit-performance":  auditPerformanceJS,
	"audit-quality":      auditQualityJS,
	"interaction":        interactionJS,
	"mutation":           mutationJS,
	"toast":              toastJS,
	"voice":              voiceJS,
	"sketch":             sketchJS,
	"design":             designJS,
	"palette":            paletteJS,
	"style-editor":       styleEditorJS,
	"indicator":          indicatorJS,
	"snapshot-helper":    snapshotHelperJS,
	"diagnostics":        diagnosticsJS,
	"session":            sessionJS,
	"store":              storeJS,
	"content":            contentJS,
	"text-fragility":     textFragilityJS,
	"responsive-risk":    responsiveRiskJS,
	"wireframe":          wireframeJS,
	"responsive":         responsiveJS,
	"api":                apiJS,
}

// GetCombinedScript returns all JavaScript modules combined into a single script.
// The script is wrapped in appropriate tags and ordered for correct initialization.
// The result is cached after first call.
func GetCombinedScript() string {
	combinedScriptOnce.Do(func() {
		combinedScript = buildCombinedScript()
	})
	return combinedScript
}

// buildCombinedScript assembles all modules in dependency order defined by moduleOrder.
func buildCombinedScript() string {
	var sb strings.Builder

	// Main script block
	sb.WriteString("<script>\n")

	// External dependencies (html2canvas-pro for screenshots)
	// Using html2canvas-pro instead of html2canvas because it supports modern CSS
	// color functions (lab, oklch, oklab, lch) that Firefox and modern browsers use.
	// See: https://github.com/nickt26/html2canvas-pro
	// Bundled at compile time to avoid CDN dependency (jsdelivr.net can be unreachable)
	sb.WriteString(html2canvasProJS)
	sb.WriteString("\n")

	sb.WriteString("(function() {\n")
	sb.WriteString("  'use strict';\n\n")

	// Inject version as first thing in the script
	sb.WriteString("  // Version injected at build/runtime\n")
	sb.WriteString("  window.__devtool_version = '")
	sb.WriteString(Version)
	sb.WriteString("';\n\n")

	// Load modules in dependency order from moduleOrder
	for _, m := range moduleOrder {
		sb.WriteString("  // ")
		sb.WriteString(m.name)
		sb.WriteString(" module\n")
		sb.WriteString(wrapModule(moduleScript[m.name]))
		sb.WriteString("\n\n")
	}

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

// GetAxeCore returns the bundled axe-core JavaScript library.
func GetAxeCore() string {
	return axeCoreJS
}

// GetScriptNames returns the list of embedded script names for debugging.
func GetScriptNames() []string {
	names := make([]string, len(moduleOrder))
	for i, m := range moduleOrder {
		names[i] = m.name + ".js"
	}
	return names
}

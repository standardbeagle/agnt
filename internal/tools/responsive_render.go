package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// responsiveViewportJSON mirrors one viewport entry of the formatJSON shape
// produced by internal/proxy/scripts/responsive.js.
type responsiveViewportJSON struct {
	Width       int                     `json:"width"`
	Issues      []AuditFinding          `json:"issues"`
	Error       string                  `json:"error,omitempty"`
	CheckErrors []responsiveCheckErrJSON `json:"checkErrors,omitempty"`
}

type responsiveCheckErrJSON struct {
	Check string `json:"check"`
	Error string `json:"error"`
}

// responsiveViewportEntry keeps a viewport's name next to its decoded body so
// compact rendering preserves the JS object insertion order (encoding/json
// maps would lose it).
type responsiveViewportEntry struct {
	name string
	vp   responsiveViewportJSON
}

// parseResponsiveViewports decodes the "viewports" object of the raw audit
// JSON in key order. All other top-level keys are skipped.
func parseResponsiveViewports(raw []byte) ([]responsiveViewportEntry, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // opening '{'
		return nil, fmt.Errorf("invalid audit JSON: %w", err)
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid audit JSON: %w", err)
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("invalid audit JSON: non-string key")
		}
		if key != "viewports" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("invalid audit JSON: %w", err)
			}
			continue
		}
		if _, err := dec.Token(); err != nil { // opening '{' of viewports
			return nil, fmt.Errorf("invalid audit JSON: %w", err)
		}
		var out []responsiveViewportEntry
		for dec.More() {
			nt, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("invalid audit JSON: %w", err)
			}
			name, ok := nt.(string)
			if !ok {
				return nil, fmt.Errorf("invalid audit JSON: non-string viewport key")
			}
			var vp responsiveViewportJSON
			if err := dec.Decode(&vp); err != nil {
				return nil, fmt.Errorf("invalid viewport %q: %w", name, err)
			}
			out = append(out, responsiveViewportEntry{name: name, vp: vp})
		}
		if _, err := dec.Token(); err != nil { // closing '}' of viewports
			return nil, fmt.Errorf("invalid audit JSON: %w", err)
		}
		return out, nil
	}
	return nil, fmt.Errorf("invalid audit JSON: no viewports key")
}

// renderResponsiveCompact renders the responsive_audit raw JSON result
// (formatJSON shape from internal/proxy/scripts/responsive.js) as compact
// text. The JS side is the finding producer; the Go side owns the profile
// projection (ProjectFindingsByProfile) and this rendering. Every finding
// line is followed by its `id:` and `next:` lines.
func renderResponsiveCompact(rawJSON []byte, profile string) (string, error) {
	vps, err := parseResponsiveViewports(rawJSON)
	if err != nil {
		return "", err
	}

	// Flatten findings in render order, project, and rebuild the kept set.
	var flat []AuditFinding
	for _, e := range vps {
		flat = append(flat, e.vp.Issues...)
	}
	projected := ProjectFindingsByProfile(flat, profile)
	kept := make(map[AuditFinding]int, len(projected))
	for _, f := range projected {
		kept[f]++
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("=== Responsive Audit: %d viewports ===", len(vps)), "")

	total, critical, minor := 0, 0, 0
	failedChecks := 0
	// selector -> message -> viewport names (for pattern detection)
	occurrences := map[string]map[string][]string{}
	viewportNames := make([]string, 0, len(vps))

	for _, e := range vps {
		viewportNames = append(viewportNames, e.name)
		// Apply the projection to this viewport, preserving order.
		var issues []AuditFinding
		for _, f := range e.vp.Issues {
			if kept[f] > 0 {
				kept[f]--
				issues = append(issues, f)
			}
		}

		lines = append(lines, fmt.Sprintf("%s (%dpx) - %d issues", strings.ToUpper(e.name), e.vp.Width, len(issues)))

		if e.vp.Error != "" {
			lines = append(lines, "  ERROR: "+e.vp.Error)
		} else if len(e.vp.CheckErrors) > 0 {
			for _, ce := range e.vp.CheckErrors {
				lines = append(lines, fmt.Sprintf("  CHECK FAILED [%s]: %s (results incomplete)", ce.Check, ce.Error))
			}
		}
		failedChecks += len(e.vp.CheckErrors)

		if e.vp.Error == "" && len(issues) > 0 {
			shown := issues
			if len(shown) > 10 {
				shown = shown[:10]
			}
			for _, issue := range shown {
				icon := "o"
				if issue.Severity == "critical" || issue.Severity == "warning" {
					icon = "!"
				}
				idSuffix := ""
				if issue.ID != "" {
					idSuffix = " #" + issue.ID
				}
				lines = append(lines, fmt.Sprintf("  %s [%s] %s - %s%s", icon, issue.Type, issue.Selector, issue.Message, idSuffix))
				lines = append(lines, "    id: "+issue.ID)
				lines = append(lines, "    next: "+issue.Next)
			}
			if len(issues) > 10 {
				lines = append(lines, fmt.Sprintf("  ... and %d more issues", len(issues)-10))
			}
		}

		for _, issue := range issues {
			total++
			switch issue.Severity {
			case "critical":
				critical++
			case "warning", "info":
				minor++
			}
			if occurrences[issue.Selector] == nil {
				occurrences[issue.Selector] = map[string][]string{}
			}
			occurrences[issue.Selector][issue.Message] = append(occurrences[issue.Selector][issue.Message], e.name)
		}

		lines = append(lines, "")
	}

	lines = append(lines, fmt.Sprintf("SUMMARY: %d issues (%d critical, %d minor)", total, critical, minor))

	if failedChecks > 0 {
		lines = append(lines, fmt.Sprintf("WARNING: %d check(s) failed — results are incomplete", failedChecks))
	}

	// Patterns, mirroring calculatePatterns() in responsive.js over the
	// projected finding set. The JS returns early with fewer than two
	// viewports — no pattern is computable from one size — so all zeros.
	mobileOnly, tabletOnly, crossViewport := 0, 0, 0
	sels := make([]string, 0, len(occurrences))
	for sel := range occurrences {
		sels = append(sels, sel)
	}
	sort.Strings(sels)
	contains := func(list []string, s string) bool {
		for _, v := range list {
			if v == s {
				return true
			}
		}
		return false
	}
	if len(viewportNames) >= 2 {
		for _, sel := range sels {
			msgs := make([]string, 0, len(occurrences[sel]))
			for m := range occurrences[sel] {
				msgs = append(msgs, m)
			}
			sort.Strings(msgs)
			for _, m := range msgs {
				names := occurrences[sel][m]
				switch {
				case len(names) == len(viewportNames):
					crossViewport++
				case contains(names, "mobile") && len(names) == 1:
					mobileOnly++
				case contains(names, "tablet") && len(names) == 1:
					tabletOnly++
				}
			}
		}
	}
	lines = append(lines, fmt.Sprintf("PATTERNS: %d mobile-only, %d tablet-only, %d cross-viewport", mobileOnly, tabletOnly, crossViewport))

	return strings.Join(lines, "\n"), nil
}

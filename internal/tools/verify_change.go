package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// VerifyChangeInput is the input schema for the verify_change tool.
type VerifyChangeInput struct {
	ProxyID    string   `json:"proxy_id,omitempty"    jsonschema:"Proxy ID used for conditional visual-baseline screenshots (default: the investigation's active proxy)"`
	FindingIDs []string `json:"finding_ids,omitempty" jsonschema:"Finding ids/fingerprints to recheck (default: every finding recorded in the session Investigation)"`
	Since      string   `json:"since,omitempty"       jsonschema:"Cursor forwarded to producers that accept one (e.g. get_incidents since)"`
	Raw        bool     `json:"raw,omitempty"         jsonschema:"Return full JSON instead of compact text"`
}

// verifyFindingLine is one per-finding outcome row.
type verifyFindingLine struct {
	ID        string `json:"id"`
	Status    string `json:"status"` // resolved | persist | unknown
	Producer  string `json:"producer,omitempty"`
	VisualRef string `json:"visual_ref,omitempty"`
}

// VerifyChangeOutput is the structured output of verify_change.
type VerifyChangeOutput struct {
	Status             string              `json:"status"` // PASS | FAIL
	Header             string              `json:"header"`
	Findings           []verifyFindingLine `json:"findings"`
	Resolved           []string            `json:"resolved"`
	Persisting         []string            `json:"persisting"`
	New                []string            `json:"new"`
	VisualBaselineRef  string              `json:"visual_baseline_ref,omitempty"`
	CollectionWarnings []string            `json:"collection_warnings,omitempty"`
}

// verifyProducerFunc re-runs one finding producer in-process and returns the
// ids that producer reports NOW. It must call the producer's own handler —
// never a broad audit or the MCP surface.
type verifyProducerFunc func(ctx context.Context, args map[string]any) ([]string, error)

// verifyDeps bundles every side effect runVerifyChange needs so tests drive
// the core with fixtures and the MCP handler wires the real daemon client.
type verifyDeps struct {
	investigation func(ctx context.Context) (*protocol.Investigation, error)
	merge         func(ctx context.Context, patch protocol.InvestigationPatch) error
	producers     map[string]verifyProducerFunc
	screenshot    func(ctx context.Context, proxyID, name string) (string, error)
}

// runVerifyChange is the orchestration core: read the session Investigation,
// re-run each target finding's producer in-process, compare by id, screenshot
// visual findings that resolved or persist, and merge the outcome back.
func runVerifyChange(ctx context.Context, input VerifyChangeInput, deps verifyDeps) (VerifyChangeOutput, error) {
	out := VerifyChangeOutput{
		Findings:   []verifyFindingLine{},
		Resolved:   []string{},
		Persisting: []string{},
		New:        []string{},
	}

	inv, err := deps.investigation(ctx)
	if err != nil {
		return out, fmt.Errorf("read investigation: %w", err)
	}
	refsByID := map[string]protocol.FindingRef{}
	if inv != nil {
		for _, r := range inv.Findings {
			refsByID[r.Fingerprint] = r
		}
	}

	targetIDs := input.FindingIDs
	if len(targetIDs) == 0 {
		for id := range refsByID {
			targetIDs = append(targetIDs, id)
		}
		sort.Strings(targetIDs)
	}
	if len(targetIDs) == 0 {
		return out, fmt.Errorf("no findings to verify: the session Investigation is empty and no finding_ids were given")
	}

	// Resolve targets. An id absent from the record has no producer, so it
	// cannot be rechecked: warn stale_finding_id and move on — never abort.
	type target struct {
		id  string
		ref protocol.FindingRef
	}
	targets := []target{}
	for _, id := range targetIDs {
		r, ok := refsByID[id]
		if !ok || r.Producer == nil || r.Producer.Tool == "" {
			out.CollectionWarnings = append(out.CollectionWarnings,
				fmt.Sprintf("stale_finding_id: %q is not in the session investigation record and has no producer; skipped", id))
			continue
		}
		targets = append(targets, target{id: id, ref: r})
	}

	// Re-run producers, deduped by (tool, args): identical producer calls run
	// once. Only the producers of target findings are ever invoked.
	type runKey struct{ tool, args string }
	runs := map[runKey][]string{}
	runErrs := map[runKey]error{}
	argsByTool := map[string]map[string]any{}
	targetByTool := map[string]map[string]bool{}
	for _, tg := range targets {
		tool := tg.ref.Producer.Tool
		runner, ok := deps.producers[tool]
		if !ok {
			out.CollectionWarnings = append(out.CollectionWarnings,
				fmt.Sprintf("stale_finding_id: %q names unknown producer %q; skipped", tg.id, tool))
			continue
		}
		argsJSON, _ := json.Marshal(tg.ref.Producer.Args)
		key := runKey{tool: tool, args: string(argsJSON)}
		if _, done := runs[key]; !done {
			if _, failed := runErrs[key]; !failed {
				ids, rerr := runner(ctx, tg.ref.Producer.Args)
				runs[key] = ids
				if rerr != nil {
					runErrs[key] = rerr
				}
			}
		}
		if argsByTool[tool] == nil {
			argsByTool[tool] = tg.ref.Producer.Args
		}
		if targetByTool[tool] == nil {
			targetByTool[tool] = map[string]bool{}
		}
		targetByTool[tool][tg.id] = true
	}

	// Compare by id.
	idIn := func(ids []string, id string) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}
	proxyID := input.ProxyID
	if proxyID == "" && inv != nil {
		proxyID = inv.ActiveProxyID
	}
	var removeIDs []string
	for _, tg := range targets {
		tool := tg.ref.Producer.Tool
		if _, ok := deps.producers[tool]; !ok {
			continue // already warned
		}
		argsJSON, _ := json.Marshal(tg.ref.Producer.Args)
		key := runKey{tool: tool, args: string(argsJSON)}
		line := verifyFindingLine{ID: tg.id, Producer: tool}
		if rerr, failed := runErrs[key]; failed {
			// A producer error must never read as resolved (false PASS hides a
			// regression): count as persist and warn.
			line.Status = "persist"
			out.CollectionWarnings = append(out.CollectionWarnings,
				fmt.Sprintf("producer %s failed for %q: %v (counted as persist)", tool, tg.id, rerr))
		} else if idIn(runs[key], tg.id) {
			line.Status = "persist"
		} else {
			line.Status = "resolved"
		}
		// Screenshots: Visual=true findings that resolved or changed only.
		if tg.ref.Visual && deps.screenshot != nil {
			ref, serr := deps.screenshot(ctx, proxyID, "verify-change-"+tg.id)
			if serr != nil {
				out.CollectionWarnings = append(out.CollectionWarnings,
					fmt.Sprintf("visual screenshot for %q failed: %v", tg.id, serr))
			} else {
				line.VisualRef = ref
				out.VisualBaselineRef = ref
			}
		}
		switch line.Status {
		case "resolved":
			out.Resolved = append(out.Resolved, tg.id)
			removeIDs = append(removeIDs, tg.id)
		default:
			out.Persisting = append(out.Persisting, tg.id)
		}
		out.Findings = append(out.Findings, line)
	}

	// New: ids a target producer reports now that were not among its targets.
	newSet := map[string]bool{}
	var newRefs []protocol.FindingRef
	for key, ids := range runs {
		if runErrs[key] != nil {
			continue
		}
		for _, id := range ids {
			if targetByTool[key.tool][id] || newSet[id] {
				continue
			}
			newSet[id] = true
			out.New = append(out.New, id)
			newRefs = append(newRefs, protocol.FindingRef{
				Fingerprint: id,
				Source:      key.tool,
				Producer:    &finding.Producer{Tool: key.tool, Args: argsByTool[key.tool]},
			})
		}
	}
	sort.Strings(out.New)

	out.Status = "PASS"
	if len(out.Persisting) > 0 {
		out.Status = "FAIL"
	}
	out.Header = fmt.Sprintf("verify_change: %s (%d resolved, %d persist, %d new)",
		out.Status, len(out.Resolved), len(out.Persisting), len(out.New))

	// Merge the outcome back: resolved ids removed, new appended.
	if deps.merge != nil && inv != nil && (len(removeIDs) > 0 || len(newRefs) > 0 || out.VisualBaselineRef != "") {
		if err := deps.merge(ctx, protocol.InvestigationPatch{
			RemoveFindings:    removeIDs,
			Findings:          newRefs,
			VisualBaselineRef: out.VisualBaselineRef,
		}); err != nil {
			out.CollectionWarnings = append(out.CollectionWarnings, "investigation merge failed: "+err.Error())
		}
	}
	return out, nil
}

// formatVerifyChangeCompact renders the compact wire form: header line,
// per-finding lines, then collection warnings.
func formatVerifyChangeCompact(out VerifyChangeOutput) string {
	var sb strings.Builder
	sb.WriteString(out.Header + "\n")
	for _, f := range out.Findings {
		sb.WriteString(fmt.Sprintf("  %s %s", f.Status, f.ID))
		if f.Producer != "" {
			sb.WriteString(" (" + f.Producer + ")")
		}
		if f.VisualRef != "" {
			sb.WriteString(" visual:" + f.VisualRef)
		}
		sb.WriteString("\n")
	}
	for _, w := range out.CollectionWarnings {
		sb.WriteString("!! " + w + "\n")
	}
	return sb.String()
}

// RegisterVerifyChangeTool registers the verify_change MCP tool.
func RegisterVerifyChangeTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "verify_change",
		Description: `Recheck only the findings this session already recorded.

Reads the session Investigation, re-runs each target finding's producer
(get_incidents / responsive_audit / api_audit / loading_audit / diagnose)
IN-PROCESS with the exact recorded args — never a broad audit — and compares
by id: resolved (absent now), persists (still reported), new (reported by the
same producer but not a target). Screenshots via __devtool.screenshot are
captured ONLY for Visual=true findings that resolved or changed, recorded as
visual_baseline_ref. Unknown ids warn per-id (stale_finding_id) without
aborting. The outcome merges back into the Investigation.

Header: verify_change: PASS|FAIL (<n> resolved, <m> persist, <k> new);
FAIL whenever any target persists.

Examples:
  verify_change {}
  verify_change {finding_ids: ["a1b2c3d4"]}
  verify_change {proxy_id: "dev", raw: true}`,
	}, makeVerifyChangeHandler(dt))
}

func makeVerifyChangeHandler(dt *DaemonTools) func(context.Context, *mcp.CallToolRequest, VerifyChangeInput) (*mcp.CallToolResult, VerifyChangeOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input VerifyChangeInput) (*mcp.CallToolResult, VerifyChangeOutput, error) {
		if dt == nil {
			return fail[VerifyChangeOutput]("verify_change requires daemon mode")
		}
		if err := dt.ensureConnected(); err != nil {
			return fail[VerifyChangeOutput]("verify_change failed: cannot reach daemon: " + err.Error())
		}
		deps := verifyDeps{
			investigation: func(ctx context.Context) (*protocol.Investigation, error) {
				res, err := dt.client.InvestigationGet(dt.SessionCode())
				if err != nil {
					return nil, err
				}
				if !res.Found {
					return nil, nil
				}
				return res.Investigation, nil
			},
			merge: func(ctx context.Context, patch protocol.InvestigationPatch) error {
				_, err := dt.client.InvestigationMerge(dt.SessionCode(), patch)
				return err
			},
			producers:  defaultVerifyProducers(dt, input.Since),
			screenshot: defaultVerifyScreenshot(dt),
		}
		out, err := runVerifyChange(ctx, input, deps)
		if err != nil {
			return fail[VerifyChangeOutput]("verify_change failed: " + err.Error())
		}
		if input.Raw {
			b, _ := json.Marshal(out)
			return mcpText(string(b)), out, nil
		}
		return mcpText(formatVerifyChangeCompact(out)), out, nil
	}
}

// defaultVerifyProducers is the dispatch table from producer tool name to an
// in-process runner that calls the producer's own handler function directly.
func defaultVerifyProducers(dt *DaemonTools, since string) map[string]verifyProducerFunc {
	roundtrip := func(args map[string]any, dst any) error {
		b, err := json.Marshal(args)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, dst)
	}
	return map[string]verifyProducerFunc{
		"get_incidents": func(ctx context.Context, args map[string]any) ([]string, error) {
			var in GetIncidentsInput
			if err := roundtrip(args, &in); err != nil {
				return nil, err
			}
			if since != "" && in.Since == "" {
				in.Since = since
			}
			_, out, err := makeGetIncidentsHandler(dt)(ctx, nil, in)
			if err != nil {
				return nil, err
			}
			ids := make([]string, 0, len(out.Incidents))
			for _, v := range out.Incidents {
				ids = append(ids, v.Fingerprint)
			}
			return ids, nil
		},
		"responsive_audit": func(ctx context.Context, args map[string]any) ([]string, error) {
			var in ResponsiveAuditInput
			if err := roundtrip(args, &in); err != nil {
				return nil, err
			}
			_, out, err := dt.makeResponsiveAuditHandler()(ctx, nil, in)
			if err != nil {
				return nil, err
			}
			return extractRawFindingIDs(out.Raw), nil
		},
		"api_audit": func(ctx context.Context, args map[string]any) ([]string, error) {
			var in APIAuditInput
			if err := roundtrip(args, &in); err != nil {
				return nil, err
			}
			_, out, err := dt.makeAPIAuditHandler()(ctx, nil, in)
			if err != nil {
				return nil, err
			}
			return extractRawFindingIDs(out.Raw), nil
		},
		"loading_audit": func(ctx context.Context, args map[string]any) ([]string, error) {
			var in LoadingAuditInput
			if err := roundtrip(args, &in); err != nil {
				return nil, err
			}
			_, out, err := dt.makeLoadingAuditHandler()(ctx, nil, in)
			if err != nil {
				return nil, err
			}
			return extractRawFindingIDs(out.Raw), nil
		},
		"diagnose": func(ctx context.Context, args map[string]any) ([]string, error) {
			var in DiagnoseInput
			if err := roundtrip(args, &in); err != nil {
				return nil, err
			}
			_, out, err := dt.makeDiagnoseHandler()(ctx, nil, in)
			if err != nil {
				return nil, err
			}
			return extractRawFindingIDs(out.Raw), nil
		},
	}
}

// extractRawFindingIDs walks a producer's raw JSON payload and collects the
// "id" of every object that looks like a finding (id plus severity/type/
// category), so audit producers need no per-tool parsing here.
func extractRawFindingIDs(raw any) []string {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if id, ok := t["id"].(string); ok && id != "" && !seen[id] {
				if _, sev := t["severity"]; sev {
					seen[id] = true
					ids = append(ids, id)
				} else if _, typ := t["type"]; typ {
					seen[id] = true
					ids = append(ids, id)
				} else if _, cat := t["category"]; cat {
					seen[id] = true
					ids = append(ids, id)
				}
			}
			for _, v2 := range t {
				walk(v2)
			}
		case []any:
			for _, v2 := range t {
				walk(v2)
			}
		}
	}
	walk(decoded)
	sort.Strings(ids)
	return ids
}

// defaultVerifyScreenshot captures a visual baseline through the existing
// __devtool.screenshot path and returns the saved file ref.
func defaultVerifyScreenshot(dt *DaemonTools) func(ctx context.Context, proxyID, name string) (string, error) {
	return func(ctx context.Context, proxyID, name string) (string, error) {
		if proxyID == "" {
			return "", fmt.Errorf("no proxy_id for visual screenshot")
		}
		optsJSON, _ := json.Marshal(map[string]any{"name": name})
		result, err := dt.client.ProxyExec(proxyID, fmt.Sprintf("await __devtool.screenshot(%s)", optsJSON))
		if err != nil {
			return "", err
		}
		if !getBool(result, "success") {
			return "", fmt.Errorf("screenshot failed: %s", getString(result, "error"))
		}
		entries, _, _, err := dt.client.ProxyLogQueryFull(proxyID, protocol.LogQueryFilter{Types: []string{"screenshot"}, Limit: 1})
		if err != nil {
			return "", err
		}
		if len(entries) == 0 || entries[0].Screenshot == nil {
			return "", fmt.Errorf("screenshot log entry not found")
		}
		return entries[0].Screenshot.FilePath, nil
	}
}

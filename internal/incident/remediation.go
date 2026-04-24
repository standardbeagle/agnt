package incident

import "time"

// IncidentView is the pull-side projection of an InboxEntry, enriched with
// remediation guidance. Used by get_incidents output and AggregateRemediation.
type IncidentView struct {
	ID          string      `json:"id"`
	Fingerprint string      `json:"fingerprint"`
	FirstSeen   time.Time   `json:"first_seen"`
	LastSeen    time.Time   `json:"last_seen"`
	Count       int         `json:"count"`
	Severity    Severity    `json:"severity"`
	Source      Source      `json:"source"`
	Category    string      `json:"category,omitempty"`
	Summary     string      `json:"summary,omitempty"`
	Payload     *string     `json:"payload,omitempty"` // hydrated when detail="full"
	Ctx         Context     `json:"context,omitempty"`
	Remediation Remediation `json:"remediation,omitempty"`
	Read        bool        `json:"read"`
}

// ToolSuggestion is a pre-filled tool call with a one-line rationale.
type ToolSuggestion struct {
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args,omitempty"`
	Rationale string         `json:"rationale"`
}

type toolCall struct {
	tool string
	args map[string]any
}

type route struct {
	primary  toolCall
	fallback toolCall // zero = none
	skill    string
	// context names keys from Context to inject into tool args
	context []string
}

// routes maps every Source to remediation guidance. Each Source must have an entry.
var routes = map[Source]route{
	SourceBrowserJS: {
		primary: toolCall{"proxy", map[string]any{
			"action": "exec",
			"code":   `window.__devtool.getElementInfo(selector)`,
		}},
		fallback: toolCall{"proxylog", map[string]any{
			"types": []string{"error", "custom"},
		}},
		skill:   "agnt-browser-debug",
		context: []string{"url", "page", "proxy_id"},
	},
	SourceHTTP5xx: {
		primary: toolCall{"proxylog", map[string]any{
			"types":        []string{"http"},
			"status_codes": []int{500, 502, 503, 504},
		}},
		fallback: toolCall{"proc", map[string]any{
			"action": "output",
			"tail":   200,
		}},
		skill:   "systematic-debugging",
		context: []string{"proxy_id", "process_id"},
	},
	SourceHTTP4xx: {
		primary: toolCall{"proxylog", map[string]any{
			"types":        []string{"http"},
			"status_codes": []int{400, 401, 403, 404},
		}},
		skill:   "systematic-debugging",
		context: []string{"proxy_id"},
	},
	SourceTransportErr: {
		primary:  toolCall{"proxy", map[string]any{"action": "status"}},
		fallback: toolCall{"proc", map[string]any{"action": "status"}},
		skill:    "daemon-debug",
		context:  []string{"proxy_id", "process_id"},
	},
	SourceProxyDiag: {
		primary: toolCall{"daemon", map[string]any{"action": "doctor"}},
		skill:   "daemon-health-check",
	},
	SourceProcessAlert: {
		primary:  toolCall{"proc", map[string]any{"action": "output", "tail": 500}},
		fallback: toolCall{"proc", map[string]any{"action": "script_output"}},
		skill:    "agnt-process-manager",
		context:  []string{"process_id"},
	},
	SourceProcessOutput: {
		primary: toolCall{"proc", map[string]any{"action": "output", "tail": 200}},
		skill:   "agnt-process-manager",
		context: []string{"process_id"},
	},
	SourceProcessCrash: {
		primary:  toolCall{"proc", map[string]any{"action": "output", "tail": 200}},
		fallback: toolCall{"daemon", map[string]any{"action": "doctor"}},
		skill:    "agnt-process-manager",
		context:  []string{"process_id"},
	},
	SourceBuildFail: {
		primary:  toolCall{"proc", map[string]any{"action": "output", "tail": 500}},
		fallback: toolCall{"proc", map[string]any{"action": "script_output"}},
		skill:    "kill-dev-processes",
		context:  []string{"process_id"},
	},
	SourcePortConflict: {
		primary:  toolCall{"proc", map[string]any{"action": "cleanup_port"}},
		fallback: toolCall{"daemon", map[string]any{"action": "doctor"}},
		skill:    "daemon-health-check",
		context:  []string{"port"},
	},
	SourceShutdown: {
		primary:  toolCall{"daemon", map[string]any{"action": "status"}},
		fallback: toolCall{"daemon", map[string]any{"action": "startup_log"}},
		skill:    "daemon-debug",
	},
	SourceHookStopFail: {
		primary: toolCall{"get_incidents", map[string]any{
			"severity": []string{"error", "critical"},
			"since":    "5m",
		}},
		skill: "systematic-debugging",
	},
}

// genericFallback is used when no route is registered for a Source.
var genericFallback = route{
	primary: toolCall{"get_incidents", map[string]any{
		"severity": []string{"error", "critical"},
	}},
	skill: "systematic-debugging",
}

// Resolve returns remediation guidance for ev. Never returns a zero Remediation.
func Resolve(ev *IncidentEvent) Remediation {
	r, ok := routes[ev.Source]
	if !ok {
		r = genericFallback
	}
	return Remediation{
		PrimaryTool:  r.primary.tool,
		PrimaryArgs:  mergeContext(r.primary.args, ev.Ctx, r.context),
		FallbackTool: r.fallback.tool,
		FallbackArgs: mergeContext(r.fallback.args, ev.Ctx, r.context),
		SkillHint:    r.skill,
	}
}

// AggregateRemediation picks the dominant skill+tools across a slice of incidents,
// weighted by occurrence count.
func AggregateRemediation(incidents []IncidentView) (skill string, tools []ToolSuggestion) {
	skillWeight := map[string]int{}
	seen := map[string]bool{}

	for _, iv := range incidents {
		w := iv.Count
		if w <= 0 {
			w = 1
		}
		if iv.Remediation.SkillHint != "" {
			skillWeight[iv.Remediation.SkillHint] += w
		}
		if iv.Remediation.PrimaryTool != "" && !seen[iv.Remediation.PrimaryTool] {
			seen[iv.Remediation.PrimaryTool] = true
			tools = append(tools, ToolSuggestion{
				Tool:      iv.Remediation.PrimaryTool,
				Args:      iv.Remediation.PrimaryArgs,
				Rationale: "primary remediation for " + string(iv.Source),
			})
		}
	}

	// dominant skill by weighted count
	best := 0
	for s, w := range skillWeight {
		if w > best {
			best = w
			skill = s
		}
	}
	return skill, tools
}

// contextValue extracts a named field from a Context struct.
func contextValue(ctx Context, key string) (any, bool) {
	switch key {
	case "process_id":
		if ctx.ProcessID != "" {
			return ctx.ProcessID, true
		}
	case "proxy_id":
		if ctx.ProxyID != "" {
			return ctx.ProxyID, true
		}
	case "session_id":
		if ctx.SessionID != "" {
			return ctx.SessionID, true
		}
	case "project_path":
		if ctx.ProjectPath != "" {
			return ctx.ProjectPath, true
		}
	case "url", "page":
		if ctx.URL != "" {
			return ctx.URL, true
		}
	case "port":
		if ctx.Port != 0 {
			return ctx.Port, true
		}
	}
	return nil, false
}

// mergeContext copies contextKeys from ctx into a clone of base args.
func mergeContext(base map[string]any, ctx Context, contextKeys []string) map[string]any {
	if len(base) == 0 && len(contextKeys) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(contextKeys))
	for k, v := range base {
		out[k] = v
	}
	for _, k := range contextKeys {
		if v, ok := contextValue(ctx, k); ok {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

package agentbench

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// maxFollowSteps bounds a replay; a contract workflow never legitimately
// exceeds it, so hitting the bound means a next: cycle.
const maxFollowSteps = 24

var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Follow replays the contract workflow for scenario against ex: it starts
// from the initial state step (currentpage), then repeatedly executes the
// next action the previous response carried — Response.Next (the raw-JSON
// path) or the first `next:` line of Response.Text (the compact-text
// path) — until a response carries no next action. The recorded trace is
// returned ready for Score.
//
// Follow fails loudly: an executor error, an unparsable next action, or a
// next: cycle all return an error quoting the offending response; a run
// never silently ends early.
func Follow(scenario string, ex Executor) (*Trace, error) {
	if ex == nil {
		return nil, errors.New("agentbench: nil executor")
	}
	tr := &Trace{Scenario: scenario, Provenance: "contract replay via agentbench.Follow"}

	resp, err := ex.Execute(Call{Tool: "currentpage"})
	if err != nil {
		return nil, fmt.Errorf("agentbench: %s: initial currentpage: %w", scenario, err)
	}
	tr.Steps = append(tr.Steps, stepFrom(Call{Tool: "currentpage"}, resp))

	for len(tr.Steps) < maxFollowSteps {
		next := strings.TrimSpace(resp.Next)
		if next == "" {
			next = firstNextLine(resp.Text)
		}
		if next == "" {
			if err := tr.validate(); err != nil {
				return nil, fmt.Errorf("agentbench: %s: %w", scenario, err)
			}
			return tr, nil
		}
		call, err := ParseNext(next)
		if err != nil {
			return nil, fmt.Errorf("agentbench: %s: step %d: %w\noffending response:\n%s",
				scenario, len(tr.Steps), err, resp.Text)
		}
		resp, err = ex.Execute(call)
		if err != nil {
			return nil, fmt.Errorf("agentbench: %s: step %d (%s): %w",
				scenario, len(tr.Steps), call.Tool, err)
		}
		tr.Steps = append(tr.Steps, stepFrom(call, resp))
	}
	return nil, fmt.Errorf("agentbench: %s: exceeded %d steps — next: cycle?", scenario, maxFollowSteps)
}

// firstNextLine returns the first `next:` line of a compact response body.
func firstNextLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "next:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "next:"))
		}
	}
	return ""
}

// ParseNext parses one shipped next-action string into a Call. Supported
// forms (all produced by the shipped compact formatters):
//
//	proxy exec <code>                      (diagnose next actions)
//	__devtool.helper(...)                  (bare helper -> proxy exec)
//	tool {k:"v", k2:"v2"}                  (buffer-audit / responsive nexts)
//	tool k=v k2=v2                         (get_incidents remediation nexts)
//	tool                                   (bare tool name)
func ParseNext(s string) (Call, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Call{}, errors.New("agentbench: empty next action")
	}
	if strings.HasPrefix(s, "proxy exec ") {
		code := strings.TrimSpace(strings.TrimPrefix(s, "proxy exec "))
		if code == "" {
			return Call{}, fmt.Errorf("agentbench: next %q: proxy exec without code", s)
		}
		return Call{Tool: "proxy", Action: "exec", Code: code}, nil
	}
	if strings.HasPrefix(s, "__devtool.") || strings.HasPrefix(s, "window.__devtool") ||
		strings.HasPrefix(s, "await __devtool") {
		return Call{Tool: "proxy", Action: "exec", Code: s}, nil
	}

	tool, rest := s, ""
	if i := strings.IndexAny(s, " {"); i >= 0 {
		tool, rest = s[:i], strings.TrimSpace(s[i:])
	}
	if !toolNameRE.MatchString(tool) {
		return Call{}, fmt.Errorf("agentbench: unparseable next action %q", s)
	}
	c := Call{Tool: tool}
	switch {
	case rest == "":
	case strings.HasPrefix(rest, "{"):
		if !strings.HasSuffix(rest, "}") {
			return Call{}, fmt.Errorf("agentbench: next %q: unterminated brace args", s)
		}
		args, err := parseBraceArgs(rest[1 : len(rest)-1])
		if err != nil {
			return Call{}, fmt.Errorf("agentbench: next %q: %w", s, err)
		}
		c.Args = args
	default:
		args, err := parseKVArgs(rest)
		if err != nil {
			return Call{}, fmt.Errorf("agentbench: next %q: %w", s, err)
		}
		c.Args = args
	}
	if a, ok := c.Args["action"].(string); ok {
		c.Action = a
		delete(c.Args, "action")
		if len(c.Args) == 0 {
			c.Args = nil
		}
	}
	// get_incidents remediation renders proxy exec as k=v args
	// (`proxy action=exec code=...`); normalize code into Call.Code.
	if c.Tool == "proxy" && c.Action == "exec" && c.Code == "" {
		if code, ok := c.Args["code"].(string); ok {
			c.Code = code
			delete(c.Args, "code")
		}
	}
	return c, nil
}

// parseBraceArgs parses `k:"v", k2:v2` (the buffer-audit style).
func parseBraceArgs(body string) (map[string]any, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, nil
	}
	args := map[string]any{}
	for _, part := range strings.Split(body, ",") {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("malformed brace arg %q", strings.TrimSpace(part))
		}
		args[strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
	}
	return args, nil
}

// parseKVArgs parses `k=v k2=v2` (the formatArgs style). A [a,b] value is
// a list.
func parseKVArgs(rest string) (map[string]any, error) {
	args := map[string]any{}
	for _, tok := range strings.Fields(rest) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("malformed arg token %q", tok)
		}
		if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
			inner := v[1 : len(v)-1]
			if inner == "" {
				args[k] = []any{}
			} else {
				items := strings.Split(inner, ",")
				list := make([]any, len(items))
				for i, it := range items {
					list[i] = it
				}
				args[k] = list
			}
			continue
		}
		args[k] = unquote(v)
	}
	return args, nil
}

func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// stepFrom builds the trace step for one executed call.
func stepFrom(c Call, resp Response) Step {
	return Step{
		Tool:          c.Tool,
		Action:        c.Action,
		Args:          c.Args,
		Code:          c.Code,
		ResponseBytes: len(resp.Text),
		Kind:          classify(c),
		Useful:        resp.Useful,
		FindingKind:   resp.FindingKind,
	}
}

// classify maps a call to its StepKind.
func classify(c Call) StepKind {
	switch {
	case c.Tool == "proxy" && c.Action == "exec":
		if strings.Contains(c.Code, "__devtool.screenshot") {
			return KindScreenshot
		}
		return KindExec
	case c.Tool == "currentpage" || (c.Tool == "proxy" && c.Action == "status") || c.Tool == "daemon":
		return KindState
	case c.Tool == "get_incidents" || c.Tool == "proxylog":
		return KindIncidents
	case strings.HasSuffix(c.Tool, "_audit"):
		return KindAudit
	case c.Tool == "snapshot" || c.Tool == "replaytest":
		return KindComposite
	default:
		return KindOther
	}
}

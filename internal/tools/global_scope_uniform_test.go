package tools

import (
	"reflect"
	"strings"
	"testing"
)

// TestGatedMCPTools_ExposeGlobalFlagUniformly pins the C6 acceptance: every
// gated MCP tool input exposes the SAME `global` override — a bool field with
// json tag "global,omitempty" — so an agent toggles cross-project scope the
// identical way on every tool. Adding a new gated tool without the uniform
// flag (or renaming it) makes this RED.
//
// Exceptions are intentional and NOT listed here:
//   - get_incidents: the incident inbox is per-session hard-isolated
//     (daemon-architecture.md "Cross-session isolation" contract), a stronger
//     guarantee than project scoping — a cross-session global would violate it.
//   - watch: emits an `agnt monitor` command; monitor stream scoping is a
//     separate STREAM-EVENTS concern, not a result-returning query, so a
//     global flag there would be a no-op (silent-failure-forbidden).
func TestGatedMCPTools_ExposeGlobalFlagUniformly(t *testing.T) {
	gated := []struct {
		name string
		typ  reflect.Type
	}{
		{"GetErrorsInput", reflect.TypeOf(GetErrorsInput{})},
		{"ProcInput", reflect.TypeOf(ProcInput{})},
		{"ProxyInput", reflect.TypeOf(ProxyInput{})},
		{"TunnelInput", reflect.TypeOf(TunnelInput{})},
		{"SessionInput", reflect.TypeOf(SessionInput{})},
		{"DaemonInput", reflect.TypeOf(DaemonInput{})},
	}

	for _, g := range gated {
		t.Run(g.name, func(t *testing.T) {
			f, ok := g.typ.FieldByName("Global")
			if !ok {
				t.Fatalf("%s must expose a Global field for the uniform global override", g.name)
			}
			if f.Type.Kind() != reflect.Bool {
				t.Errorf("%s.Global must be bool, got %s", g.name, f.Type.Kind())
			}
			jsonTag := f.Tag.Get("json")
			if !strings.HasPrefix(jsonTag, "global") {
				t.Errorf("%s.Global must use json tag \"global,omitempty\", got %q", g.name, jsonTag)
			}
			if !strings.Contains(jsonTag, "omitempty") {
				t.Errorf("%s.Global json tag should be omitempty (optional, default false), got %q", g.name, jsonTag)
			}
			// Description must document the flag so the UX is self-explanatory.
			if schema := f.Tag.Get("jsonschema"); strings.TrimSpace(schema) == "" {
				t.Errorf("%s.Global must carry a jsonschema description documenting the override", g.name)
			}
		})
	}
}

package tools

import (
	"encoding/json"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/standardbeagle/go-sdk/mcp"
)

// pickProxyID returns the canonical proxy_id when set, otherwise falls back to
// the `id` alias. Use in handlers to accept both forms transparently. When both
// are set, the canonical proxy_id wins.
func pickProxyID(id, proxyID string) string {
	if proxyID != "" {
		return proxyID
	}
	return id
}

// pickProcessID is the process_id counterpart to pickProxyID.
func pickProcessID(id, processID string) string {
	if processID != "" {
		return processID
	}
	return id
}

// addLenientTool registers an MCP tool that ignores unknown properties in input.
//
// The MCP SDK's jsonschema inference sets additionalProperties: false on structs,
// causing hard validation errors when AI agents send unrecognized parameters.
// This helper generates the same schema but removes that constraint, so unknown
// properties are silently ignored (Go's JSON unmarshalling already drops them).
func addLenientTool[In, Out any](s *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	rt := reflect.TypeFor[In]()
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	schema, err := jsonschema.ForType(rt, &jsonschema.ForOptions{})
	if err == nil {
		schema.AdditionalProperties = nil
		t.InputSchema = schema
	}
	// An `any`-typed output field infers an unrestricted schema, which marshals
	// to the boolean shorthand `true`. That is valid JSON Schema, but Claude
	// Code's SDK entrypoint validates tools/list strictly and rejects boolean
	// property schemas — and one rejected tool fails the WHOLE list, so an
	// SDK-spawned agent sees no agnt tools at all. Pre-generate the output
	// schema and give every unrestricted subschema an explicit annotated form.
	ot := reflect.TypeFor[Out]()
	if ot.Kind() == reflect.Pointer {
		ot = ot.Elem()
	}
	if ot.Kind() == reflect.Struct {
		if outSchema, err := jsonschema.ForType(ot, &jsonschema.ForOptions{}); err == nil {
			normalizeUnrestricted(outSchema)
			t.OutputSchema = outSchema
		}
	}
	mcp.AddTool(s, t, h)
}

// normalizeUnrestricted rewrites subschemas that would marshal to the boolean
// shorthand `true` into an annotated form. Detection is by the marshalled
// bytes — exactly the representation the strict client rejects.
func normalizeUnrestricted(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	for name, p := range s.Properties {
		if p == nil {
			continue
		}
		if b, err := json.Marshal(p); err == nil && string(b) == "true" {
			s.Properties[name] = &jsonschema.Schema{Description: "Arbitrary JSON value"}
			continue
		}
		normalizeUnrestricted(p)
	}
	normalizeUnrestricted(s.Items)
	normalizeUnrestricted(s.AdditionalProperties)
}

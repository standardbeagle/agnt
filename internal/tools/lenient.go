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
		normalizeUnrestricted(schema)
		t.InputSchema = schema
	}
	// An `any`-typed field infers an unrestricted schema, which marshals to the
	// boolean shorthand `true`. That is valid JSON Schema, but Claude Code's SDK
	// entrypoint validates tools/list strictly and rejects boolean subschemas —
	// and one rejected tool fails the WHOLE list, so an SDK-spawned agent sees
	// no agnt tools at all. tools/list carries both schemas, so both are
	// normalized: a `map[string]any` input field infers
	// `additionalProperties:true` exactly as an `any` output field infers a
	// boolean property.
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

// normalizeUnrestricted rewrites every subschema that would marshal to the
// boolean shorthand `true` into an annotated object form. Detection is by the
// marshalled bytes — exactly the representation the strict client rejects.
//
// Traversal is reflective over the three schema-valued field shapes
// (*Schema, []*Schema, map[string]*Schema) rather than a hand-written list of
// keywords, so a boolean is replaced wherever it sits: a property, an `items`,
// an `additionalProperties` (what a map[string]any infers), a `$defs` entry, a
// oneOf branch. An earlier keyword-walk recursed into `items` and
// `additionalProperties` but could only replace entries in `properties`, since
// that was the only settable slot it held — the recursion read as coverage it
// did not provide, and left `map[string]any` fields carrying
// `"additionalProperties":true`.
//
// A `false` subschema is left alone: it marshals to `false`, never `true`, so
// the closed-struct `additionalProperties:false` that ForType emits is never
// widened into a permissive one.
func normalizeUnrestricted(s *jsonschema.Schema) {
	normalizeSchemaTree(s, make(map[*jsonschema.Schema]bool))
}

var (
	schemaPtrType   = reflect.TypeFor[*jsonschema.Schema]()
	schemaSliceType = reflect.TypeFor[[]*jsonschema.Schema]()
	schemaMapType   = reflect.TypeFor[map[string]*jsonschema.Schema]()
)

// normalizeSchemaTree walks s and its subschemas. seen guards against a shared
// or cyclic subschema pointer sending the walk round forever.
func normalizeSchemaTree(s *jsonschema.Schema, seen map[*jsonschema.Schema]bool) {
	if s == nil || seen[s] {
		return
	}
	seen[s] = true

	v := reflect.ValueOf(s).Elem()
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		switch f.Type {
		case schemaPtrType:
			child := fv.Interface().(*jsonschema.Schema)
			if isUnrestricted(child) {
				fv.Set(reflect.ValueOf(unrestrictedReplacement()))
				continue
			}
			normalizeSchemaTree(child, seen)

		case schemaSliceType:
			for j, child := range fv.Interface().([]*jsonschema.Schema) {
				if isUnrestricted(child) {
					fv.Index(j).Set(reflect.ValueOf(unrestrictedReplacement()))
					continue
				}
				normalizeSchemaTree(child, seen)
			}

		case schemaMapType:
			m := fv.Interface().(map[string]*jsonschema.Schema)
			for name, child := range m {
				if isUnrestricted(child) {
					m[name] = unrestrictedReplacement()
					continue
				}
				normalizeSchemaTree(child, seen)
			}
		}
	}
}

// isUnrestricted reports whether s marshals to the bare `true` a strict client
// rejects.
func isUnrestricted(s *jsonschema.Schema) bool {
	if s == nil {
		return false
	}
	b, err := json.Marshal(s)
	return err == nil && string(b) == "true"
}

// unrestrictedReplacement is the annotated stand-in for an unrestricted schema:
// still accepts any value, but never marshals to a boolean.
func unrestrictedReplacement() *jsonschema.Schema {
	return &jsonschema.Schema{Description: "Arbitrary JSON value"}
}

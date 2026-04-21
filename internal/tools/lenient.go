package tools

import (
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/standardbeagle/go-sdk/mcp"
)

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
	mcp.AddTool(s, t, h)
}

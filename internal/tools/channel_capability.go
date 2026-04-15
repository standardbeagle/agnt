package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/standardbeagle/agnt/internal/config"
)

// ChannelCapabilityName is the experimental capability key used to declare
// support for the claude/channel push-based event protocol.
const ChannelCapabilityName = "claude/channel"

// ChannelInstructions is appended to the standard MCP Instructions when
// channel mode is active. It tells the AI agent how to interpret incoming
// channel events.
const ChannelInstructions = `

Channel events arrive as real-time XML-like tags injected into your context:
<channel source="agnt" type="..." proxy="..." severity="...">body</channel>

Event types: error, diagnostic, interaction, process, panel_message, sketch, design
Severity levels: trace, debug, info, warning, error
The proxy attribute is the agnt proxy ID (stable per dev server).

When the channel-reply tool is registered, you can send messages back to the
developer's browser overlay via that tool.`

// ChannelServerOptions returns a ServerOptions with channel-specific
// modifications applied. When channel is disabled (cfg is nil or not enabled),
// it returns opts unchanged -- preserving byte-for-byte identical default
// behavior. When channel is enabled, it sets the Experimental capabilities map
// and appends channel instructions.
func ChannelServerOptions(opts *mcp.ServerOptions, cfg *config.ChannelConfig) *mcp.ServerOptions {
	if cfg == nil || !cfg.IsEnabled() {
		return opts
	}

	// Clone to avoid mutating caller's struct.
	result := *opts

	// Set or augment the Experimental capabilities map.
	if result.Capabilities == nil {
		result.Capabilities = &mcp.ServerCapabilities{}
	} else {
		// Clone capabilities to avoid mutating caller.
		cp := *result.Capabilities
		result.Capabilities = &cp
	}

	if result.Capabilities.Experimental == nil {
		result.Capabilities.Experimental = make(map[string]any, 1)
	} else {
		// Clone the map to avoid mutating caller.
		result.Capabilities.Experimental = cloneMap(result.Capabilities.Experimental)
	}
	result.Capabilities.Experimental[ChannelCapabilityName] = struct{}{}

	// Append channel instructions to the base instructions.
	result.Instructions = opts.Instructions + ChannelInstructions

	return &result
}

func cloneMap(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

# devtool-mcp (DEPRECATED)

> **This package has been renamed to [`agnt`](https://pypi.org/project/agnt/).** Please install `agnt` instead.

## Migration

```bash
pip uninstall devtool-mcp
pip install agnt
```

Then update your MCP configuration:

**Before:**
```json
{
  "mcpServers": {
    "devtool": {
      "command": "devtool-mcp"
    }
  }
}
```

**After:**
```json
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    }
  }
}
```

## Why the rename?

The project has evolved beyond just development tooling into a full AI coding agent toolkit. The new name `agnt` better reflects its capabilities:

- Process management for development workflows
- Reverse proxy with traffic logging and browser instrumentation
- 50+ diagnostic primitives for frontend debugging
- Sketch mode for wireframing directly on your UI
- PTY wrapper for AI coding tools (Claude Code, Gemini, etc.)

## New Features in agnt

- `agnt run claude` - Wrap AI tools with overlay features
- `agnt mcp` - Run as MCP server
- Sketch mode for wireframing
- Floating indicator panel
- Toast notifications

See the [agnt documentation](https://standardbeagle.github.io/agnt/) for details.

## Backward Compatibility

This wrapper package will continue to work but will not receive updates. The `devtool-mcp` command will forward to `agnt` with a deprecation notice.

## License

MIT

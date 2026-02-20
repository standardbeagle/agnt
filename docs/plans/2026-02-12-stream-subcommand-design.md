# agnt stream: Bidirectional JSON Middleware

## Problem

Clients that want to use agnt as middleware between themselves and Claude have no clean path. `agnt run` wraps processes in a PTY that corrupts structured JSON streams. `agnt ai claude` provides clean JSONL output but is one-shot with no stdin streaming or browser event injection during execution.

## Solution

A new `agnt stream claude` subcommand that acts as a transparent JSONL middleware. No PTY. Direct pipes. Browser events injected as stdin messages via the existing overlay socket protocol.

## Architecture

```
Client stdin ──→ ┌─────────────┐     ┌────────────────┐
                 │  stdin mux  │────→│ Claude process  │
Overlay socket ─→│  (channel)  │     │ (pipes, no PTY) │
                 └─────────────┘     └───────┬────────┘
                                             │ stdout
Client stdout ←── line reader ←──────────────┘
                      │
                      └──→ activity tee (daemon)
```

Three goroutines:

1. **stdin relay** - reads JSONL lines from os.Stdin, sends to channel.
2. **stdin writer** - drains channel, writes to Claude's stdin pipe. Sole owner of that pipe.
3. **stdout relay** - reads lines from Claude's stdout, writes to os.Stdout, optionally tees to daemon for activity detection.

Browser events arrive on the overlay Unix socket (same protocol the proxy uses today), get formatted as stream-json input messages, and sent to the same channel as client stdin.

## Command Interface

```
agnt stream claude [flags] [-- extra-claude-args]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--session <code>` | auto | Session code for overlay identification |
| `--no-agnt-prompt` | false | Skip system prompt injection |
| `--bypass-permissions` | true | Bypass Claude permission checks |
| `--allowed-tools` | all | Tool whitelist (comma-separated) |
| `--disallowed-tools` | none | Tool blacklist (comma-separated) |
| `--mcp-config <path>` | none | MCP config file |
| `--model <model>` | default | Model selection |
| `--max-turns` | none | Max conversation turns |
| `--max-budget` | none | Cost ceiling (USD) |
| `--no-overlay` | false | Disable overlay socket (pure pipe) |

### What agnt does automatically

- Adds `--input-format stream-json --output-format stream-json` to claude CLI args
- Injects agnt system prompt via `--append-system-prompt`
- Starts overlay socket listener for browser event injection
- Registers with daemon for activity tracking
- Passes anything after `--` directly to claude

### Examples

```bash
# Basic - client pipes JSONL in/out
agnt stream claude

# With extra claude flags
agnt stream claude -- --allowedTools "Read,Write"

# No browser integration
agnt stream claude --no-overlay

# Custom model
agnt stream claude --model opus
```

## Stdin Channel Protocol

```go
stdinCh := make(chan []byte, 64) // buffered to absorb bursts
```

### Producers

1. **Client stdin relay** - bufio.Scanner on os.Stdin, sends complete lines to stdinCh. On EOF, signals done via WaitGroup.

2. **Overlay event handler** - receives browser events on Unix socket, formats as stream-json input messages, sends []byte to stdinCh. Signals done via WaitGroup on shutdown.

### Consumer

3. **Stdin writer** - drains stdinCh, writes each []byte + newline to Claude's stdin pipe. When channel closes, closes the pipe.

### Shutdown coordination

sync.WaitGroup with count 2 (one per producer). Separate goroutine waits on WaitGroup then closes stdinCh. Writer drains until channel close, then closes Claude's stdin pipe.

### Backpressure

If Claude isn't reading (channel full), producers block on send. This naturally throttles the client and overlay.

## Stdout Relay

Single goroutine. Reads lines from Claude's stdout via bufio.Scanner (1MB max line buffer for large tool results), writes to os.Stdout. Optionally tees to daemon as a lightweight activity notification (no ANSI stripping needed - stream-json is clean JSON).

No output transformation. Client gets byte-identical output to what Claude emits.

When Claude exits, stdout closes, scanner returns false, goroutine exits. This signals the stream command to clean up and exit with Claude's exit code.

## Overlay Event Formatting

Browser events arrive on the overlay socket and need to become valid stream-json stdin messages.

Reuses existing formatters from overlay.go (formatPanelMessage, formatDesignStateMessage, etc.) to produce text, then wraps in a stream-json user message envelope:

```json
{"type":"user","message":{"role":"user","content":"[Browser Event: panel_message]\nUser says: Fix the button styling\n\n[Attachments]\n- screenshot: /tmp/agnt/sc-001.png (800x600)"}}
```

The stream-json input format (verified from Claude Agent SDK docs) is:
```json
{"type":"user","message":{"role":"user","content":"text here"}}
```

Or with structured content blocks (for future image support):
```json
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"message"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}]}}
```

Event types handled (same set as current overlay):
- panel_message
- sketch
- design_state, design_request, design_chat
- screenshot

## File Layout

### New files

- `cmd/agnt/stream.go` - command definition, flag parsing, process lifecycle, goroutine plumbing
- `cmd/agnt/stream_claude.go` - Claude-specific arg building

### Modified files

- `cmd/agnt/root.go` - register stream subcommand
- `cmd/agnt/overlay.go` - extract event formatters if needed

### Reused as-is

- Overlay socket protocol
- Event formatters
- System prompt builder (buildAgntSystemPrompt)
- Daemon registration
- Autostart logic from .agnt.kdl

## Not Included (YAGNI)

- Custom transport layer
- Message deserialization
- Output-side transformation
- Session resume management (client owns via claude flags after --)
- Reconnection logic

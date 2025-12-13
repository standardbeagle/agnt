---
sidebar_position: 8
---

# tunnel

Manage tunnel connections for exposing local services publicly. Perfect for mobile device testing and sharing development builds.

## Synopsis

```json
tunnel {action: "<action>", ...params}
```

## Actions

| Action | Description |
|--------|-------------|
| `start` | Start a tunnel to expose a local port |
| `stop` | Stop a running tunnel |
| `status` | Get tunnel status and public URL |
| `list` | List all active tunnels |

## Supported Providers

| Provider | Binary | Description |
|----------|--------|-------------|
| `cloudflare` | `cloudflared` | Free quick tunnels via trycloudflare.com |
| `ngrok` | `ngrok` | Popular tunneling service |

## start

Start a tunnel to expose a local port publicly.

```json
tunnel {
  action: "start",
  id: "app",
  provider: "cloudflare",
  local_port: 8080
}
```

Parameters:
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `id` | string | Yes | - | Unique tunnel identifier |
| `provider` | string | Yes | - | Tunnel provider: `cloudflare` or `ngrok` |
| `local_port` | integer | Yes | - | Local port to tunnel |
| `local_host` | string | No | `localhost` | Local host to tunnel |
| `binary_path` | string | No | (from PATH) | Path to tunnel binary |
| `proxy_id` | string | No | - | Proxy ID to auto-configure with public URL |

Response:
```json
{
  "id": "app",
  "provider": "cloudflare",
  "state": "connected",
  "public_url": "https://random-words-here.trycloudflare.com",
  "local_addr": "localhost:8080"
}
```

### Auto-Configure Proxy

When `proxy_id` is specified, the tunnel automatically updates the proxy's `public_url` once the tunnel URL is discovered:

```json
// Start proxy first
proxy {action: "start", id: "app", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
// Response: {listen_addr: "0.0.0.0:45849"}

// Start tunnel with proxy auto-configuration
tunnel {
  action: "start",
  id: "app",
  provider: "cloudflare",
  local_port: 45849,
  proxy_id: "app"
}
```

The proxy will now correctly rewrite URLs to use the tunnel's HTTPS scheme.

## stop

Stop a running tunnel.

```json
tunnel {action: "stop", id: "app"}
```

Response:
```json
{
  "success": true,
  "id": "app",
  "message": "Tunnel stopped"
}
```

## status

Get tunnel status and public URL.

```json
tunnel {action: "status", id: "app"}
```

Response:
```json
{
  "id": "app",
  "provider": "cloudflare",
  "state": "connected",
  "public_url": "https://random-words-here.trycloudflare.com",
  "local_addr": "localhost:8080"
}
```

### Tunnel States

| State | Description |
|-------|-------------|
| `idle` | Not started |
| `starting` | Starting up, waiting for URL |
| `connected` | Running with public URL available |
| `failed` | Failed to start or crashed |
| `stopped` | Stopped by user |

## list

List all active tunnels.

```json
tunnel {action: "list"}
```

Response:
```json
{
  "count": 2,
  "tunnels": [
    {
      "id": "frontend",
      "provider": "cloudflare",
      "state": "connected",
      "public_url": "https://abc.trycloudflare.com",
      "local_addr": "localhost:8080"
    },
    {
      "id": "api",
      "provider": "ngrok",
      "state": "connected",
      "public_url": "https://xyz.ngrok-free.app",
      "local_addr": "localhost:4000"
    }
  ]
}
```

## Installation Requirements

### Cloudflare (cloudflared)

```bash
# macOS
brew install cloudflare/cloudflare/cloudflared

# Linux
curl -L --output cloudflared.deb https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
sudo dpkg -i cloudflared.deb

# Windows
winget install --id Cloudflare.cloudflared
```

### ngrok

```bash
# macOS
brew install ngrok/ngrok/ngrok

# Linux/Windows
# Download from https://ngrok.com/download

# Configure auth token (required)
ngrok config add-authtoken <your-token>
```

## Real-World Patterns

### Mobile Device Testing

```json
// 1. Start your dev server
run {script_name: "dev"}

// 2. Wait for it to be ready
proc {action: "output", process_id: "dev", grep: "ready"}

// 3. Start proxy on all interfaces
proxy {
  action: "start",
  id: "app",
  target_url: "http://localhost:3000",
  bind_address: "0.0.0.0"
}
// Note the listen_addr port from the response

// 4. Start tunnel with auto-configuration
tunnel {
  action: "start",
  id: "app",
  provider: "cloudflare",
  local_port: 45849,
  proxy_id: "app"
}
// Share the public_url with your mobile device
```

### Multiple Environments

```json
// Frontend tunnel
tunnel {action: "start", id: "frontend", provider: "cloudflare", local_port: 3000}

// API tunnel
tunnel {action: "start", id: "api", provider: "cloudflare", local_port: 4000}

// List all
tunnel {action: "list"}
```

### BrowserStack Integration

Use agnt tunnels with [BrowserStack's MCP server](https://github.com/browserstack/mcp-server) for automated mobile testing:

```json
// claude_desktop_config.json
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["serve"]
    },
    "browserstack": {
      "command": "npx",
      "args": ["@anthropic-ai/browserstack-mcp"],
      "env": {
        "BROWSERSTACK_USERNAME": "your_username",
        "BROWSERSTACK_ACCESS_KEY": "your_key"
      }
    }
  }
}
```

Then use both tools together:
1. Start your proxy and tunnel with agnt
2. Use BrowserStack MCP to run tests on the tunneled URL
3. Capture errors and screenshots through the instrumented proxy

## Error Responses

### Tunnel Not Found

```json
{
  "error": "tunnel not found",
  "id": "nonexistent"
}
```

### Binary Not Found

```json
{
  "error": "cloudflared not found in PATH"
}
```

### Tunnel Already Exists

```json
{
  "error": "tunnel already exists",
  "id": "app"
}
```

## See Also

- [proxy](/api/proxy) - Reverse proxy with tunnel support
- [Mobile Testing Guide](/use-cases/mobile-testing) - Complete workflow

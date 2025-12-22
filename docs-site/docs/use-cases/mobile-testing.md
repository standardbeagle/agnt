---
sidebar_position: 7
---

# Mobile Device Testing

Test your web application on real mobile devices during development using agnt's tunnel integration with Cloudflare or ngrok, plus optional BrowserStack automation.

## The Challenge

Testing on real mobile devices during development typically requires:
- Complex network configuration or device proxies
- Manual URL sharing and constant re-typing
- No visibility into mobile-specific errors
- Separate tools for automation

**agnt solves this** by providing integrated tunneling with full proxy instrumentation:
- One-command tunnel setup with automatic URL discovery
- All proxy features work through the tunnel (error capture, screenshots, interactions)
- Optional BrowserStack MCP integration for automated device testing

## Quick Start

### 1. Start Your Dev Server

```json
run {script_name: "dev"}
```

Wait for it to be ready:
```json
proc {action: "output", process_id: "dev", grep: "ready", tail: 5}
```

### 2. Start the Instrumented Proxy

```json
proxy {
  action: "start",
  id: "app",
  target_url: "http://localhost:3000",
  bind_address: "0.0.0.0"
}
```

Note the `listen_addr` from the response (e.g., `0.0.0.0:45849`).

:::caution Security
Using `bind_address: "0.0.0.0"` exposes the proxy on all network interfaces. Only use this when you need external access (tunnels, mobile testing on local network).
:::

### 3. Start the Tunnel

```json
tunnel {
  action: "start",
  id: "app",
  provider: "cloudflare",
  local_port: 45849,
  proxy_id: "app"
}
```

The response includes your public URL:
```json
{
  "id": "app",
  "provider": "cloudflare",
  "state": "connected",
  "public_url": "https://random-words.trycloudflare.com",
  "local_addr": "localhost:45849"
}
```

### 4. Test on Your Device

Open the `public_url` on your mobile device. You now have:

- **Full proxy instrumentation** - The `window.__devtool` API works on mobile
- **Error capture** - Mobile-specific JavaScript errors are logged
- **Floating indicator** - Send messages from your phone to the AI agent
- **Screenshot capability** - Capture mobile layouts

## Checking Mobile Errors

After testing on your device:

```json
// Check for JavaScript errors
proxylog {proxy_id: "app", types: ["error"]}

// See all HTTP traffic
proxylog {proxy_id: "app", types: ["http"], limit: 20}

// Check page sessions
currentpage {proxy_id: "app"}
```

## BrowserStack Integration

For automated testing across many devices, combine agnt with [BrowserStack's MCP server](https://github.com/browserstack/mcp-server).

### Setup

Add both MCP servers to your configuration:

```json title="claude_desktop_config.json"
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    },
    "browserstack": {
      "command": "npx",
      "args": ["@anthropic-ai/browserstack-mcp"],
      "env": {
        "BROWSERSTACK_USERNAME": "your_username",
        "BROWSERSTACK_ACCESS_KEY": "your_key",
        "BROWSERSTACK_LOCAL": "true"
      }
    }
  }
}
```

### Workflow

1. **Start your tunneled proxy** (as shown above)
2. **Use BrowserStack MCP** to launch tests on real devices pointing to your tunnel URL
3. **Capture results** through both tools:
   - agnt: Error logs, HTTP traffic, page sessions
   - BrowserStack: Screenshots, video recordings, device logs

### What Each Tool Provides

| agnt Captures | BrowserStack Provides |
|---------------|----------------------|
| JavaScript errors with stack traces | Screenshots on demand |
| All HTTP request/response data | Video recordings of sessions |
| Page load performance metrics | Native device logs (iOS/Android) |
| User interaction history | Automated test execution |
| DOM mutations and changes | Cross-browser/device matrix |
| Custom diagnostic data | Real device hardware |

### Example: Automated Mobile Testing

```json
// 1. Start dev server and instrumented proxy
run {script_name: "dev"}
proxy {action: "start", id: "mobile-qa", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}

// 2. Start tunnel
tunnel {action: "start", id: "mobile-qa", provider: "cloudflare", local_port: 45849, proxy_id: "mobile-qa"}
// Response: {public_url: "https://abc-xyz.trycloudflare.com"}

// 3. BrowserStack runs tests on the tunnel URL across devices:
//    - iPhone 15 Pro (iOS 17)
//    - Samsung Galaxy S24 (Android 14)
//    - iPad Pro (iPadOS 17)

// 4. After tests complete, analyze captured data:
proxylog {proxy_id: "mobile-qa", types: ["error"]}
// Shows any JS errors that occurred on any device

proxylog {proxy_id: "mobile-qa", types: ["performance"]}
// Shows load times across all devices

currentpage {proxy_id: "mobile-qa"}
// Shows page sessions from each device
```

## Tunnel Providers

### Cloudflare (Recommended)

**Best for**: Quick testing, no signup required.

**Pros:**
- Free, no account required
- Fast and reliable
- HTTPS by default
- No bandwidth limits

**Requirements:**
```bash
# Install cloudflared
brew install cloudflare/cloudflare/cloudflared  # macOS
```

**Usage:**
```json
tunnel {action: "start", id: "app", provider: "cloudflare", local_port: 8080}
```

### ngrok

**Best for**: Stable URLs, webhook testing.

**Pros:**
- Stable URLs (with paid plan)
- Request inspection dashboard
- Webhooks and integrations

**Requirements:**
```bash
# Install and configure
brew install ngrok/ngrok/ngrok  # macOS
ngrok config add-authtoken <your-token>
```

**Usage:**
```json
tunnel {action: "start", id: "app", provider: "ngrok", local_port: 8080}
```

### Tailscale Funnel

**Best for**: Teams using Tailscale, persistent URLs, maximum privacy.

**Pros:**
- Persistent, memorable URLs (based on machine name)
- End-to-end encrypted
- No third-party account if you use Tailscale
- Works with Tailscale ACLs

**Requirements:**
- Tailscale installed and authenticated
- Funnel enabled on your tailnet (admin approval required)

**Usage (manual setup):**
```bash
# 1. Start agnt proxy on all interfaces
proxy {action: "start", id: "app", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
# Note the port (e.g., 45849)

# 2. Start Tailscale Funnel in terminal
tailscale funnel 45849
# Output: https://your-machine.tailnet-name.ts.net

# 3. Update proxy with public URL
proxy {action: "start", id: "app", target_url: "http://localhost:3000", bind_address: "0.0.0.0", public_url: "https://your-machine.tailnet-name.ts.net"}
```

## Debugging Device-Specific Issues

### Mobile-Specific Diagnostics

When testing on mobile, use these diagnostics to catch common issues:

```json
// Check for touch target issues (buttons too small for fingers)
proxy {action: "exec", id: "app", code: "window.__devtool.findSmallTouchTargets && window.__devtool.findSmallTouchTargets()"}

// Check responsive layout issues
proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}

// Check text readability (truncation, overflow)
proxy {action: "exec", id: "app", code: "window.__devtool.checkTextFragility()"}

// Audit accessibility (critical for mobile screen readers)
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility()"}
```

### Capturing Device Context

When a user reports an issue on a specific device:

```json
// Capture complete device state
proxy {action: "exec", id: "app", code: "window.__devtool.captureState()"}
// Returns: viewport, URL, localStorage, sessionStorage, cookies, user agent

// Check what the user just interacted with
proxy {action: "exec", id: "app", code: "window.__devtool.interactions.getLastClickContext()"}
// Returns: element, position, surrounding context, mouse trail

// Check recent DOM changes
proxy {action: "exec", id: "app", code: "window.__devtool.mutations.getHistory()"}
```

### Common Mobile Issues to Check

| Issue | Diagnostic Command |
|-------|-------------------|
| Buttons too small | `findSmallTouchTargets()` |
| Text gets cut off | `checkTextFragility()` |
| Layout breaks on rotation | `checkResponsiveRisk()` |
| Forms hard to use | `auditAccessibility()` |
| Slow page loads | `proxylog {types: ["performance"]}` |
| JavaScript errors | `proxylog {types: ["error"]}` |

## Troubleshooting

### Tunnel Not Starting

Check if the binary is installed:
```bash
which cloudflared  # or: which ngrok
```

Verify the port is correct:
```json
proxy {action: "status", id: "app"}
// Check the listen_addr in the response
```

### Mobile Device Can't Connect

1. Verify the tunnel is running:
   ```json
   tunnel {action: "status", id: "app"}
   ```

2. Check the public URL is accessible from another device

3. Ensure your dev server is running and the proxy can reach it

### No Errors Being Captured

The floating indicator and `__devtool` API should work through the tunnel. If not:

1. Check if JavaScript is loading (view page source)
2. Verify the proxy is correctly forwarding to your dev server
3. Check browser console for any CORS or security errors

## Best Practices

### Use Separate Tunnels for Frontend and API

```json
// Frontend
proxy {action: "start", id: "frontend", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
tunnel {action: "start", id: "frontend", provider: "cloudflare", local_port: 45849, proxy_id: "frontend"}

// API
proxy {action: "start", id: "api", target_url: "http://localhost:4000", bind_address: "0.0.0.0"}
tunnel {action: "start", id: "api", provider: "cloudflare", local_port: 45850, proxy_id: "api"}
```

### Clean Up After Testing

```json
tunnel {action: "stop", id: "app"}
proxy {action: "stop", id: "app"}
```

### Monitor Tunnel Health

```json
tunnel {action: "list"}
```

Check for `state: "connected"` on all tunnels.

## Complete Example Session

```json
// 1. Start dev server
run {script_name: "dev", mode: "background"}

// 2. Wait for ready
proc {action: "output", process_id: "dev", grep: "ready", tail: 5}

// 3. Start proxy on all interfaces
proxy {
  action: "start",
  id: "mobile-test",
  target_url: "http://localhost:3000",
  bind_address: "0.0.0.0"
}
// Response: {listen_addr: "0.0.0.0:45849"}

// 4. Start Cloudflare tunnel with proxy integration
tunnel {
  action: "start",
  id: "mobile-test",
  provider: "cloudflare",
  local_port: 45849,
  proxy_id: "mobile-test"
}
// Response: {public_url: "https://random-words.trycloudflare.com"}

// 5. Test on mobile device using the public_url...

// 6. Check for errors
proxylog {proxy_id: "mobile-test", types: ["error"]}

// 7. View page sessions
currentpage {proxy_id: "mobile-test"}

// 8. Clean up
tunnel {action: "stop", id: "mobile-test"}
proxy {action: "stop", id: "mobile-test"}
proc {action: "stop", process_id: "dev"}
```

## See Also

- [tunnel API Reference](/api/tunnel) - Full tunnel tool documentation
- [proxy API Reference](/api/proxy) - Proxy configuration options
- [proxylog](/api/proxylog) - Query captured traffic and errors

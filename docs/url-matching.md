# URL Matching and Proxy ID Resolution

This document explains how agnt detects development server URLs and resolves proxy IDs.

## Overview

When you run a development server (e.g., `pnpm dev`, `npm run start`), agnt automatically:
1. Scans the process output for localhost URLs
2. Creates reverse proxies targeting those URLs
3. Generates compound proxy IDs for uniqueness

## URL Detection

### What URLs Are Detected

agnt only tracks **localhost-like** URLs to avoid creating duplicate proxies for the same server:

| Detected | Not Detected |
|----------|--------------|
| `http://localhost:3000` | `http://192.168.1.10:3000` |
| `http://127.0.0.1:8080` | `http://10.255.255.254:3737` |
| `http://0.0.0.0:5173` | `https://example.com` |
| `http://[::1]:3000` | External URLs |

### Why Only Localhost?

Development servers often broadcast their availability on multiple interfaces:

```
  VITE v5.0.0  ready in 500 ms

  ➜  Local:   http://localhost:5173/
  ➜  Network: http://192.168.1.100:5173/
```

Both URLs point to the **same server**. If agnt tracked both, you'd get:
- Duplicate proxies consuming resources
- Confusing proxy lists with multiple entries for one server
- Ambiguous proxy ID lookups

By filtering to localhost-only URLs, each development server gets exactly **one proxy**.

### What Gets Ignored

URLs are also filtered out if they contain:
- Query strings (`?debug=true`)
- API paths (`/api/`, `/error`, `/debug`)
- Static asset paths (`/static/`, `/assets/`, `/node_modules/`)
- Favicon requests

### Custom URL Matchers

For advanced use cases, you can configure custom URL matchers in your project's `.claude/autostart.kdl`:

```kdl
script "dev" {
    command "pnpm dev"
    proxy "dev"
    url-matchers "Local:\\s*{url}"  // Only match "Local:" lines
}
```

Matcher patterns:
- `{url}` - Matches any localhost URL on the line
- `Local:\\s*{url}` - Only lines containing "Local:"
- `(Local|Network):\\s*{url}` - Lines with "Local:" or "Network:"

## Proxy ID Resolution

### Compound IDs

When agnt auto-creates proxies from URL detection, it generates **compound IDs**:

```
{project-hash}:{proxy-name}:{host-port}
```

Example: `library-e2c4:dev:localhost-3465`

Components:
- `library-e2c4`: Short hash of project path (ensures uniqueness across projects)
- `dev`: The proxy name from configuration
- `localhost-3465`: Sanitized target URL

### Fuzzy ID Lookup

You don't need to type the full compound ID. agnt supports **fuzzy matching**:

```bash
# All of these work:
currentpage {proxy_id: "library-e2c4:dev:localhost-3465"}  # Full ID
currentpage {proxy_id: "dev"}                               # Just proxy name
proxy {action: "status", id: "dev"}                        # Works in all tools
```

### Resolution Order

1. **Exact match**: Try the ID as provided
2. **Component match**: Split compound IDs by `:` and match any component

### Ambiguous Lookups

If multiple proxies match a fuzzy lookup, you'll get an error:

```
proxy ID is ambiguous - multiple matches
```

This happens when you have multiple proxies with the same proxy name but different URLs:
- `myapp-abc1:dev:localhost-3000`
- `myapp-abc1:dev:localhost-4000`

In this case, use the full ID or a more specific component like `localhost-3000`.

## Implementation Details

### URL Detection Code

The regex for URL detection (`internal/daemon/urltracker.go`):

```go
var devServerURLRegex = regexp.MustCompile(
    `https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):\d+[^\s\)\]\}'"<>]*`
)
```

### Fuzzy Lookup Code

Proxy ID resolution (`internal/proxy/manager.go`):

```go
func (pm *ProxyManager) Get(id string) (*ProxyServer, error) {
    // 1. Try exact match first (fast path)
    if val, ok := pm.proxies.Load(id); ok {
        return val.(*ProxyServer), nil
    }

    // 2. Fuzzy match: check if ID matches any component
    var matches []*ProxyServer
    pm.proxies.Range(func(key, value any) bool {
        parts := strings.Split(key.(string), ":")
        for _, part := range parts {
            if part == id {
                matches = append(matches, value.(*ProxyServer))
                break
            }
        }
        return true
    })

    if len(matches) == 0 {
        return nil, ErrProxyNotFound
    }
    if len(matches) > 1 {
        return nil, ErrProxyAmbiguous
    }
    return matches[0], nil
}
```

## Troubleshooting

### "proxy not found" Error

1. Run `proxy {action: "list", global: true}` to see all proxies
2. Check if the proxy ID matches what you're using
3. Use the full compound ID if fuzzy lookup is ambiguous

### Duplicate Proxies Appearing

If you see proxies for both localhost and IP addresses:
1. Check if you're using an old version of agnt
2. Verify the URL detection regex excludes network IPs
3. Clear existing proxies: `proxy {action: "stop", id: "..."}` for each duplicate

### URLs Not Being Detected

1. Ensure the URL appears in the first 8KB of process output (startup phase)
2. Check that the URL uses a recognized localhost format
3. Add custom URL matchers if using non-standard output format

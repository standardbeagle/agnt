---
sidebar_position: 6
---

# Chaos Engineering

agnt includes a built-in chaos engineering system for testing how your frontend handles network failures, slow connections, and unreliable APIs. Test the unhappy paths without changing your code.

## Why Chaos Testing for Frontend?

Most frontend bugs happen under conditions developers never test:
- **Slow networks** - 3G connections, hotel WiFi, subway tunnels
- **Flaky APIs** - 500 errors, timeouts, rate limits
- **Race conditions** - Responses arriving out of order
- **Stale tabs** - Browser tabs open for hours with expired tokens

agnt lets you simulate all of these with a single command.

## Quick Start

```bash
# Start proxy with chaos preset
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
proxy {action: "chaos", id: "app", preset: "flaky-api"}
```

Your app now experiences random 500 errors, timeouts, and variable latency. Watch what breaks.

## Built-in Presets

| Preset | What It Simulates | Use For |
|--------|-------------------|---------|
| `mobile-3g` | 200-2000ms latency, 2% packet loss | Mobile network testing |
| `mobile-4g` | 50-500ms latency, 0.5% packet loss | LTE network testing |
| `flaky-api` | Random 500s, timeouts, variable latency | API resilience testing |
| `race-condition` | Out-of-order responses, high variance delays | Race condition bugs |
| `stale-tab` | 3-hour delays | Token expiry, stale state |
| `slow-connection` | 5KB/s bandwidth throttling | Slow network handling |
| `connection-drops` | 10% mid-response disconnects | Retry logic testing |
| `data-corruption` | 5% truncated responses | Partial data handling |
| `rate-limited` | 20% 429 errors | Rate limit UI testing |
| `auth-failures` | 10% 401/403 errors | Auth error handling |
| `service-degradation` | Mixed latency + errors + truncation | Graceful degradation |
| `pressure-test` | Everything at once | Stress testing |

### Preset Examples

**Test mobile network conditions:**
```bash
proxy {action: "chaos", id: "app", preset: "mobile-3g"}
# Your app now has 200-2000ms latency and 2% packet loss
```

**Expose race conditions:**
```bash
proxy {action: "chaos", id: "app", preset: "race-condition"}
# API responses arrive out of order - does your state get corrupted?
```

**Test stale browser tabs:**
```bash
proxy {action: "chaos", id: "app", preset: "stale-tab"}
# Simulates a tab that's been open for hours - test token refresh
```

## Custom Chaos Rules

For fine-grained control, define custom rules:

```bash
proxy {
  action: "chaos",
  id: "app",
  rules: [
    {
      "id": "api-latency",
      "name": "Slow API calls",
      "type": "latency",
      "enabled": true,
      "url_pattern": "/api/.*",
      "min_latency_ms": 500,
      "max_latency_ms": 3000,
      "probability": 0.5
    },
    {
      "id": "checkout-errors",
      "name": "Checkout failures",
      "type": "http_error",
      "enabled": true,
      "url_pattern": "/api/checkout",
      "error_codes": [500, 503],
      "probability": 0.1
    }
  ]
}
```

## Chaos Types

### Network Chaos

| Type | Description | Configuration |
|------|-------------|---------------|
| `latency` | Add delays to responses | `min_latency_ms`, `max_latency_ms`, `jitter_ms` |
| `bandwidth` | Limit data transfer rate | (use `slow_drip` instead) |
| `packet_loss` | Drop random requests entirely | `probability` |
| `disconnect` | Drop connection mid-response | `drop_after_percent`, `drop_after_bytes` |
| `slow_drip` | Trickle bytes slowly | `bytes_per_ms`, `chunk_size` |
| `timeout` | Never respond (simulate timeout) | `probability` |

### Response Timing

| Type | Description | Configuration |
|------|-------------|---------------|
| `out_of_order` | Deliver responses in random order | `reorder_min_requests`, `reorder_max_wait_ms` |
| `stale` | Very long delays (hours) | `stale_delay_ms` |

### HTTP Errors

| Type | Description | Configuration |
|------|-------------|---------------|
| `http_error` | Inject HTTP error codes | `error_codes[]`, `error_message` |
| `rate_limit` | Simulate 429 rate limiting | (use `http_error` with code 429) |

### Data Corruption

| Type | Description | Configuration |
|------|-------------|---------------|
| `truncate` | Cut off response body | `truncate_percent` (0.0-1.0, portion to keep) |
| `bit_flip` | Random byte changes | (advanced) |
| `corrupt_json` | Malform JSON responses | (advanced) |

## Rule Configuration

### Matching Criteria

```javascript
{
  "url_pattern": "/api/users.*",  // Regex pattern for URLs
  "methods": ["POST", "PUT"],      // HTTP methods (empty = all)
  "probability": 0.3               // 0.0-1.0, chance of applying
}
```

### Latency Configuration

```javascript
{
  "type": "latency",
  "min_latency_ms": 100,    // Minimum delay
  "max_latency_ms": 2000,   // Maximum delay
  "jitter_ms": 500          // Random +/- jitter
}
```

### Slow-Drip (Bandwidth Throttling)

```javascript
{
  "type": "slow_drip",
  "bytes_per_ms": 5,    // 5 bytes/ms = 5KB/s
  "chunk_size": 10      // Write 10 bytes at a time
}
```

### Connection Drops

```javascript
{
  "type": "disconnect",
  "drop_after_percent": 0.5,  // Drop after 50% of body sent
  // OR
  "drop_after_bytes": 1024    // Drop after 1KB sent
}
```

### Response Reordering

```javascript
{
  "type": "out_of_order",
  "reorder_min_requests": 3,    // Batch this many before shuffling
  "reorder_max_wait_ms": 500    // Max time to wait for batch
}
```

## Managing Chaos

### Check Status

```bash
proxy {action: "chaos", id: "app", status: true}
```

Returns:
```javascript
{
  "enabled": true,
  "preset": "flaky-api",
  "stats": {
    "total_requests": 142,
    "affected_count": 38,
    "latency_injected_ms": 45000,
    "errors_injected": 7,
    "drops_injected": 2
  }
}
```

### Disable Chaos

```bash
proxy {action: "chaos", id: "app", enabled: false}
```

### Clear All Rules

```bash
proxy {action: "chaos", id: "app", clear: true}
```

### Enable/Disable Specific Rules

```bash
proxy {action: "chaos", id: "app", enable_rule: "api-latency"}
proxy {action: "chaos", id: "app", disable_rule: "checkout-errors"}
```

## Real-World Testing Scenarios

### Testing Loading States

```bash
# Add 2-5 second delay to all API calls
proxy {
  action: "chaos",
  id: "app",
  rules: [{
    "type": "latency",
    "min_latency_ms": 2000,
    "max_latency_ms": 5000,
    "probability": 1.0
  }]
}
```
Now verify: Do your loading spinners appear? Is the UI responsive during loads?

### Testing Error Handling

```bash
# 50% of API calls fail with server errors
proxy {
  action: "chaos",
  id: "app",
  rules: [{
    "type": "http_error",
    "error_codes": [500, 502, 503],
    "probability": 0.5
  }]
}
```
Now verify: Do error messages appear? Can users retry? Does state stay consistent?

### Testing Race Conditions

```bash
# Responses arrive in random order
proxy {action: "chaos", id: "app", preset: "race-condition"}
```
Now verify: If user types in search and gets responses out of order, does the UI show stale results?

### Testing Offline Behavior

```bash
# Drop 100% of requests
proxy {
  action: "chaos",
  id: "app",
  rules: [{
    "type": "packet_loss",
    "probability": 1.0
  }]
}
```
Now verify: Does offline detection trigger? Are there helpful error messages?

### Testing Token Expiry

```bash
# Return 401 for all API calls
proxy {
  action: "chaos",
  id: "app",
  rules: [{
    "type": "http_error",
    "url_pattern": "/api/.*",
    "error_codes": [401],
    "probability": 1.0
  }]
}
```
Now verify: Does the app redirect to login? Is state preserved for after re-auth?

## Reproducible Chaos

For consistent test runs, set a random seed:

```bash
proxy {
  action: "chaos",
  id: "app",
  seed: 12345,
  rules: [...]
}
```

Same seed = same sequence of chaos decisions.

## Integration with Testing

### Manual Exploratory Testing

1. Start your dev server and proxy
2. Apply a chaos preset
3. Click through your app
4. Watch for broken states, missing loaders, poor error messages

### Automated E2E Tests

```javascript
// In Playwright/Cypress test
beforeEach(async () => {
  // Enable chaos via agnt API
  await fetch('http://localhost:45849/__chaos', {
    method: 'POST',
    body: JSON.stringify({ preset: 'flaky-api' })
  });
});

test('handles API errors gracefully', async ({ page }) => {
  await page.goto('/dashboard');
  // Error states should be visible due to chaos
  await expect(page.locator('.error-message')).toBeVisible();
  await expect(page.locator('.retry-button')).toBeEnabled();
});
```

## Best Practices

1. **Start with presets** - They cover common scenarios well
2. **Test one thing at a time** - Don't combine too many chaos types
3. **Document expected behavior** - What SHOULD happen under chaos?
4. **Add chaos to CI** - Run a subset of E2E tests with chaos enabled
5. **Use probability < 1.0** - Intermittent failures are harder to handle than consistent ones

## See Also

- [Reverse Proxy](/features/reverse-proxy) - Proxy setup and configuration
- [Mobile Testing](/use-cases/mobile-testing) - Testing on real devices
- [Automated Testing](/use-cases/automated-testing) - CI/CD integration

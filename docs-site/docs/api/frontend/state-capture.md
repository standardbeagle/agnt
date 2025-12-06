---
sidebar_position: 8
---

# State Capture

Functions for capturing page state, DOM snapshots, and browser storage.

## captureDOM

Capture a full HTML snapshot of the page.

```javascript
window.__devtool.captureDOM()
```

**Parameters:** None

**Returns:**
```javascript
{
  html: "<!DOCTYPE html><html lang=\"en\">...",
  hash: "a1b2c3d4e5f6",  // Hash for comparison
  timestamp: "2024-01-15T10:30:00.000Z",
  size: 45678,  // bytes
  elementCount: 342,
  meta: {
    title: "My App - Dashboard",
    url: "http://localhost:8080/dashboard",
    charset: "UTF-8"
  }
}
```

**Example:**
```javascript
// Capture before and after
const before = window.__devtool.captureDOM()
// ... make changes ...
const after = window.__devtool.captureDOM()

if (before.hash !== after.hash) {
  console.log('DOM changed!')
  console.log(`Elements: ${before.elementCount} → ${after.elementCount}`)
}
```

## captureStyles

Capture all styles for an element.

```javascript
window.__devtool.captureStyles(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  selector: ".my-button",
  inline: {
    color: "red",
    "font-size": "16px"
  },
  computed: {
    display: "inline-flex",
    position: "relative",
    width: "auto",
    height: "40px",
    padding: "8px 16px",
    margin: "0px",
    color: "rgb(255, 0, 0)",
    "background-color": "rgb(59, 130, 246)",
    "font-family": "Inter, sans-serif",
    "font-size": "16px",
    "font-weight": "500",
    "border-radius": "8px",
    // ... all computed styles
  },
  appliedRules: [
    {
      selector: ".btn",
      source: "styles.css:45",
      properties: {
        display: "inline-flex",
        padding: "8px 16px"
      }
    },
    {
      selector: ".btn-primary",
      source: "styles.css:52",
      properties: {
        "background-color": "#3b82f6"
      }
    }
  ]
}
```

**Example:**
```javascript
const styles = window.__devtool.captureStyles('.broken-button')
console.log('Inline styles:', styles.inline)
console.log('Computed color:', styles.computed.color)
```

## captureState

Capture browser storage state.

```javascript
window.__devtool.captureState(keys)
```

**Parameters:**
- `keys` (string[]): Storage types to capture
  - `'localStorage'`
  - `'sessionStorage'`
  - `'cookies'`
  - `'indexedDB'` (database names only)

**Returns:**
```javascript
{
  localStorage: {
    theme: "dark",
    user: "{\"id\":123,\"name\":\"John\"}",
    preferences: "{...}"
  },
  sessionStorage: {
    cart: "[{\"id\":1},{\"id\":2}]",
    tempData: "..."
  },
  cookies: {
    session_id: "abc123",
    csrf_token: "xyz789",
    analytics_id: "ua-12345"
  },
  indexedDB: {
    databases: ["myapp-db", "cache-db"]
  },
  timestamp: "2024-01-15T10:30:00.000Z"
}
```

**Examples:**

Capture all:
```javascript
window.__devtool.captureState(['localStorage', 'sessionStorage', 'cookies'])
```

Just localStorage:
```javascript
window.__devtool.captureState(['localStorage'])
→ {localStorage: {...}}
```

## captureNetwork

Get resource timing data from Performance API.

```javascript
window.__devtool.captureNetwork()
```

**Parameters:** None

**Returns:**
```javascript
{
  entries: [
    {
      name: "http://localhost:8080/api/users",
      type: "fetch",
      initiatorType: "fetch",
      startTime: 1234.5,
      duration: 145.2,
      transferSize: 2340,
      encodedBodySize: 2340,
      decodedBodySize: 2340,
      responseStatus: 200,
      protocol: "http/1.1"
    },
    {
      name: "http://localhost:8080/static/js/main.js",
      type: "script",
      initiatorType: "script",
      startTime: 100.0,
      duration: 89.5,
      transferSize: 45678,
      cached: false
    },
    {
      name: "http://localhost:8080/static/css/styles.css",
      type: "link",
      initiatorType: "link",
      startTime: 50.0,
      duration: 34.2,
      transferSize: 8901,
      cached: true
    }
  ],
  summary: {
    totalResources: 42,
    totalTransferSize: 234567,
    totalDuration: 892,
    byType: {
      script: {count: 5, size: 156789},
      link: {count: 3, size: 23456},
      fetch: {count: 12, size: 34567},
      img: {count: 22, size: 19755}
    }
  },
  timestamp: "2024-01-15T10:30:00.000Z"
}
```

**Example:**
```javascript
const network = window.__devtool.captureNetwork()
console.log('Total resources:', network.summary.totalResources)
console.log('Total size:', (network.summary.totalTransferSize / 1024).toFixed(1), 'KB')

// Find slow resources
const slow = network.entries.filter(e => e.duration > 1000)
console.log('Slow resources:', slow.map(e => e.name))
```

## Common Patterns

### Debug State Changes

```javascript
// Capture initial state
const before = window.__devtool.captureState(['localStorage'])

// User performs action
document.querySelector('#save-settings').click()

// Wait and capture new state
await new Promise(r => setTimeout(r, 500))
const after = window.__devtool.captureState(['localStorage'])

// Compare
Object.keys(after.localStorage).forEach(key => {
  if (before.localStorage[key] !== after.localStorage[key]) {
    console.log(`${key} changed:`, before.localStorage[key], '→', after.localStorage[key])
  }
})
```

### DOM Diff Detection

```javascript
async function watchForChanges(interval = 1000) {
  let lastHash = null

  while (true) {
    const dom = window.__devtool.captureDOM()

    if (lastHash && dom.hash !== lastHash) {
      console.log(`DOM changed! Elements: ${dom.elementCount}`)
    }

    lastHash = dom.hash
    await new Promise(r => setTimeout(r, interval))
  }
}

watchForChanges()
```

### Performance Analysis

```javascript
const network = window.__devtool.captureNetwork()

// Find blocking resources
const blocking = network.entries
  .filter(e => e.type === 'script' && !e.name.includes('async'))
  .sort((a, b) => b.duration - a.duration)

console.log('Potentially blocking scripts:')
blocking.forEach(e => {
  console.log(`  ${e.name}: ${e.duration.toFixed(0)}ms`)
})

// Total JS size
const jsSize = network.summary.byType.script?.size || 0
console.log(`Total JS: ${(jsSize / 1024).toFixed(1)} KB`)
```

### Session State Backup

```javascript
function backupSession() {
  const state = window.__devtool.captureState([
    'localStorage',
    'sessionStorage',
    'cookies'
  ])

  // Store in localStorage for later
  localStorage.setItem('__devtool_backup', JSON.stringify({
    state,
    url: window.location.href,
    timestamp: new Date().toISOString()
  }))

  return state
}

function restoreSession(backup) {
  Object.entries(backup.localStorage || {}).forEach(([k, v]) => {
    localStorage.setItem(k, v)
  })
  Object.entries(backup.sessionStorage || {}).forEach(([k, v]) => {
    sessionStorage.setItem(k, v)
  })
  // Note: cookies require document.cookie manipulation
}
```

### Full Page Capture for Debugging

```javascript
function captureEverything() {
  return {
    dom: window.__devtool.captureDOM(),
    state: window.__devtool.captureState(['localStorage', 'sessionStorage', 'cookies']),
    network: window.__devtool.captureNetwork(),
    viewport: {
      width: window.innerWidth,
      height: window.innerHeight,
      scrollX: window.scrollX,
      scrollY: window.scrollY
    },
    errors: window.__devtool.captureNetwork().entries.filter(e => e.responseStatus >= 400)
  }
}

const snapshot = captureEverything()
console.log('Full page snapshot:', snapshot)
```

## Security Notes

- `captureState` returns raw storage values
- Be careful with sensitive data (tokens, credentials)
- Clear captured data when done
- Don't log captured state in production

## See Also

- [Accessibility](/api/frontend/accessibility) - Capture a11y state
- [Composite Functions](/api/frontend/composite) - High-level capture

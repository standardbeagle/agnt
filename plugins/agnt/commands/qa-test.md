---
description: "Comprehensive QA testing suite for the current page"
allowed-tools: ["mcp__devtool__proxy", "mcp__devtool__proxylog", "mcp__devtool__currentpage"]
---

Run a comprehensive QA (Quality Assurance) test suite on the current page using agnt's diagnostic tools.

## Steps

### 1. Page Quality Check
```
proxy {action: "exec", id: "dev", code: "__devtool.auditPageQuality()"}
```

### 2. Accessibility Audit
```
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility()"}
```

### 3. Security Audit
```
proxy {action: "exec", id: "dev", code: "__devtool.auditSecurity()"}
```

### 4. DOM Complexity Check
```
proxy {action: "exec", id: "dev", code: "__devtool.auditDOMComplexity()"}
```

### 5. CSS Quality Check
```
proxy {action: "exec", id: "dev", code: "__devtool.auditCSS()"}
```

### 6. Check for JavaScript Errors
```
proxylog {proxy_id: "dev", types: ["error"], limit: 50}
```

### 7. Review HTTP Requests for Errors
```
proxylog {proxy_id: "dev", types: ["http"], status_codes: [400, 401, 403, 404, 500, 502, 503], limit: 20}
```

### 8. Get Page Session Overview
```
currentpage {proxy_id: "dev"}
```

### 9. Capture Test Evidence
```
proxy {action: "exec", id: "dev", code: "__devtool.screenshot('qa-test')"}
```

## QA Test Categories

### Functional Testing

| Test | Command |
|------|---------|
| Check all forms | `document.querySelectorAll('form').length` |
| Check all buttons work | `__devtool.interactions.getHistory()` |
| Verify navigation | `document.querySelectorAll('a[href]').length` |
| Test user flows | Review interaction history |

### Visual Testing

| Test | Command |
|------|---------|
| Layout issues | `__devtool.findOverflows()` |
| Hidden content | `__devtool.findOffscreen()` |
| Z-index problems | `__devtool.findStackingContexts()` |
| Take screenshots | `__devtool.screenshot('test-name')` |

### Performance Testing

| Test | Command |
|------|---------|
| Network resources | `__devtool.captureNetwork()` |
| DOM size | `__devtool.auditDOMComplexity()` |
| Load metrics | `proxylog {types: ["performance"]}` |

### Error Detection

| Test | Command |
|------|---------|
| JS errors | `proxylog {types: ["error"]}` |
| Console warnings | `proxylog {types: ["custom"]}` |
| Failed requests | `proxylog {types: ["http"], status_codes: [4xx, 5xx]}` |

## Interactive QA Commands

```
// Track all user interactions during testing
proxy {action: "exec", id: "dev", code: "__devtool.interactions.getHistory()"}

// Monitor DOM changes (catches unexpected UI updates)
proxy {action: "exec", id: "dev", code: "__devtool.mutations.getHistory()"}

// Highlight recently changed elements
proxy {action: "exec", id: "dev", code: "__devtool.mutations.highlightRecent(10000)"}

// Check specific element visibility
proxy {action: "exec", id: "dev", code: "__devtool.isVisible('#submit-button')"}

// Verify element is in viewport
proxy {action: "exec", id: "dev", code: "__devtool.isInViewport('#important-content')"}

// Check element overlap (z-index issues)
proxy {action: "exec", id: "dev", code: "__devtool.checkOverlap('#element1', '#element2')"}
```

## QA Checklist

### Critical (P0)
- [ ] No JavaScript errors
- [ ] No 4xx/5xx HTTP errors
- [ ] All forms submit correctly
- [ ] Authentication works
- [ ] Core user flows complete

### High Priority (P1)
- [ ] Accessibility audit passes
- [ ] Security audit passes
- [ ] Mobile responsive
- [ ] Cross-browser tested
- [ ] Performance acceptable

### Medium Priority (P2)
- [ ] SEO audit passes
- [ ] All images load
- [ ] Links work (no 404s)
- [ ] Edge cases handled
- [ ] Error messages helpful

### Low Priority (P3)
- [ ] CSS is clean
- [ ] No console warnings
- [ ] Animations smooth
- [ ] Micro-interactions work
- [ ] Analytics tracking works

## Regression Testing

After making changes, compare before/after:

```
// Capture current state
proxy {action: "exec", id: "dev", code: "__devtool.captureState()"}

// Take baseline screenshot
proxy {action: "exec", id: "dev", code: "__devtool.screenshot('regression-baseline')"}

// After changes, compare
proxy {action: "exec", id: "dev", code: "__devtool.screenshot('regression-after')"}
```

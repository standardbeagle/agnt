---
description: "Analyze user experience including layout, interactions, and usability"
allowed-tools: ["mcp__devtool__proxy", "mcp__devtool__proxylog"]
---

Analyze the user experience (UX) of the current page using agnt's diagnostic tools.

## Steps

1. Check for layout issues and overflow problems:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.findOverflows()"}
   ```

2. Find elements outside the viewport (hidden content):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.findOffscreen()"}
   ```

3. Get recent user interactions to understand user flow:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.interactions.getHistory()"}
   ```

4. Check recent DOM mutations (dynamic UI feedback):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.mutations.getHistory()"}
   ```

5. Get keyboard navigation order (tab order):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.getTabOrder()"}
   ```

6. Take a screenshot of current state:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.screenshot('ux-analysis')"}
   ```

## UX Diagnostics Available

### Layout Analysis

| Function | Purpose |
|----------|---------|
| `findOverflows()` | Find scrollable or hidden overflow content |
| `findOffscreen()` | Find elements outside viewport |
| `findStackingContexts()` | Find z-index layering issues |
| `diagnoseLayout()` | Comprehensive layout report |

### Interaction Tracking

| Function | Purpose |
|----------|---------|
| `interactions.getHistory()` | All tracked user interactions |
| `interactions.getLastClick()` | Details of most recent click |
| `interactions.getLastClickContext()` | Full context of last click |
| `interactions.getClicksOn(selector)` | Clicks on specific element |
| `interactions.getMouseTrail()` | Mouse movement path |

### Mutation Tracking

| Function | Purpose |
|----------|---------|
| `mutations.getHistory()` | All DOM mutations |
| `mutations.getAdded()` | Recently added elements |
| `mutations.getRemoved()` | Recently removed elements |
| `mutations.getModified()` | Recently modified elements |
| `mutations.highlightRecent(ms)` | Visually highlight recent changes |

## UX Assessment Checklist

### Visual Design
- [ ] Consistent spacing and alignment
- [ ] Clear visual hierarchy
- [ ] Adequate color contrast
- [ ] Readable font sizes (min 16px body)
- [ ] Touch targets min 44x44px

### Navigation
- [ ] Logical tab order
- [ ] Focus indicators visible
- [ ] Clear navigation structure
- [ ] Breadcrumbs where appropriate
- [ ] Back button works correctly

### Feedback
- [ ] Loading states shown
- [ ] Error messages helpful
- [ ] Success confirmations given
- [ ] Interactive elements have hover states
- [ ] Form validation is immediate

### Content
- [ ] Important content above fold
- [ ] Scannable headings
- [ ] Concise, clear copy
- [ ] CTAs are prominent
- [ ] No content clipping

## Interactive Debugging

```
// Highlight recent DOM changes
proxy {action: "exec", id: "dev", code: "__devtool.mutations.highlightRecent(5000)"}

// Get context of last user click
proxy {action: "exec", id: "dev", code: "__devtool.interactions.getLastClickContext()"}

// Check if specific element is visible
proxy {action: "exec", id: "dev", code: "__devtool.isVisible('#my-element')"}

// Check if element is in viewport
proxy {action: "exec", id: "dev", code: "__devtool.isInViewport('#my-element')"}
```

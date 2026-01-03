# agnt Roadmap

## Vision

**"Storybook in production context, on all your devices, with a commit button"**

agnt transforms from a debugging bridge into a full-featured design-in-context platform where you can edit components and styles on real devices with real data, and commit changes directly to code.

---

## Phase 1: Foundation (Current + Near-term)

### 1.1 QR Code Mobile Sharing ✨ NEW
**Status:** Planned
**Effort:** Low
**Impact:** High (removes friction for mobile testing)

Add QR code to the floating indicator for instant mobile device connection.

```
┌─────────────────────────────────────┐
│  agnt connected                     │
│  ┌─────────────┐                    │
│  │ █▀▀▀▀▀▀▀█  │  Scan to connect   │
│  │ █ ▄▄▄▄ █  │  on mobile          │
│  │ █ █  █ █  │                      │
│  │ █▀▀▀▀▀▀▀█  │  192.168.1.42:45849│
│  └─────────────┘  (or tunnel URL)   │
│                                     │
│  [Copy URL]  [Start Tunnel]         │
└─────────────────────────────────────┘
```

**Features:**
- QR code in indicator panel (click bug → see QR)
- Auto-detects: local IP, tunnel URL, or proxy URL
- One-tap tunnel creation if not running
- Copy URL button for pasting to other devices

**Implementation:**
- QR generation: client-side library (~3KB) or server-generated SVG
- IP detection: already available via proxy
- Tunnel integration: existing tunnel tool

---

### 1.2 Session Recording Sync
**Status:** Recording done locally, sync planned
**Effort:** Medium
**Impact:** High

Upgrade session recordings from localStorage to daemon-synced.

```javascript
// Record on desktop, replay on all devices
proxy {action: "record", id: "app", mode: "sync"}
proxy {action: "replay", id: "app", targets: "all"}
proxy {action: "replay", id: "app", targets: ["mobile-safari"]}
```

**Architecture:**
- Daemon stores recordings (not sessionStorage)
- WebSocket broadcast for synchronized replay
- Relative timing with checkpoint sync across devices
- Device-specific viewport normalization

---

## Phase 2: Live CSS Sync

### 2.1 CSS Patch Broadcasting
**Status:** Planned
**Effort:** Medium
**Impact:** Very High

Real-time CSS changes synced to all connected devices.

```javascript
// Start live CSS session
proxy {action: "css", id: "app", mode: "start"}

// Apply change (broadcasts instantly)
proxy {action: "css", id: "app", patch: ".card { border-radius: 12px }"}

// Targeted property change
proxy {action: "css", id: "app", selector: ".card", property: "padding", value: "16px"}

// Commit to file (generates diff)
proxy {action: "css", id: "app", mode: "commit"}
```

**Features:**
- Changes apply instantly to all connected browsers
- Undo/redo stack per session
- Export as CSS diff or direct file modification
- Visual indicator showing which device triggered change

---

### 2.2 In-Browser CSS Editor
**Status:** Planned
**Effort:** Medium
**Impact:** High

Click-to-edit CSS panel (better than DevTools for our use case).

```
┌─────────────────────────────────────────┐
│  .product-card                          │
│  ├─ padding:       [16px     ] ◀── slider
│  ├─ border-radius: [8px      ] ◀── slider
│  ├─ background:    [#ffffff  ] ◀── picker
│  ├─ box-shadow:    [0 2px 4px rgba...]  │
│  └─ + Add property                      │
│                                         │
│  [Reset] [Copy CSS] [Commit to File]    │
└─────────────────────────────────────────┘
```

**Features:**
- Visual controls (sliders, color pickers)
- Computed vs authored style toggle
- Changes broadcast to all devices
- Sourcemap-aware: knows which file to edit

---

### 2.3 DevTools Change Detection (Polling)
**Status:** Planned
**Effort:** Low
**Impact:** Medium

Detect CSS changes made in browser DevTools via polling.

```javascript
// Poll stylesheets every 500ms
// Diff against previous snapshot
// Broadcast changes to other devices
```

**Limitation:** 500ms latency, but catches DevTools edits without extension.

---

### 2.4 DevTools Change Detection (Extension)
**Status:** Future
**Effort:** High
**Impact:** High

Browser extension using Chrome DevTools Protocol for instant detection.

```javascript
// Extension listens to CDP events
CSS.styleSheetChanged → forward to agnt → broadcast to all devices
```

**Features:**
- Zero-latency DevTools change detection
- Full fidelity (exact rule changed)
- Also captures: console, network, performance

---

## Phase 3: Component Integration

### 3.1 Framework Detection
**Status:** Planned
**Effort:** Low
**Impact:** Foundation for Phase 3

Auto-detect React, Vue, Angular, Svelte, Solid.

```javascript
__devtool.component.detect()
→ {
    frameworks: [{name: "react", version: "18.2.0"}],
    components: 142,
    roots: ["#app"]
  }
```

**Detection methods:**
| Framework | Hook |
|-----------|------|
| React | `__REACT_DEVTOOLS_GLOBAL_HOOK__` |
| Vue 3 | `__VUE__` |
| Angular | `ng.probe()` |
| Svelte | `__svelte` (dev mode) |
| Solid | `_$DEVTOOLS` (dev mode) |

---

### 3.2 Element → Component Mapping (React/Vue)
**Status:** Planned
**Effort:** Medium
**Impact:** High

Given a DOM element, find its owning component.

```javascript
__devtool.component.fromElement('.product-card')
→ {
    framework: "react",
    name: "ProductCard",
    file: "src/components/ProductCard.tsx:24",
    props: {id: 123, title: "Widget", price: 29.99},
    state: {isHovered: false, quantity: 1},
    children: ["CardImage", "CardBody"]
  }
```

**Priority:** React first (largest market share), then Vue.

---

### 3.3 Props/State Editing (React/Vue)
**Status:** Planned
**Effort:** High
**Impact:** Very High

Modify component props/state, trigger re-render.

```javascript
// Change props - component re-renders
__devtool.component.setProps('.product-card', {price: 0, onSale: true})

// Change state - triggers re-render
__devtool.component.setState('.product-card', {quantity: 99})
```

**Architecture:**
- React: Access fiber, modify memoizedProps/State, trigger update
- Vue: Directly modify reactive props/data
- Broadcast changes to all devices

---

### 3.4 Component Inspector Panel
**Status:** Planned
**Effort:** High
**Impact:** Very High

Full component editing UI in browser.

```
┌─────────────────────────────────────────────────────────────────┐
│ 📦 ProductCard                       src/components/Card.tsx:24 │
├─────────────────────────────────────────────────────────────────┤
│  ┌────────────┐  ┌────────────┐  ┌────────────┐                │
│  │  Desktop   │  │   Phone    │  │   Tablet   │                │
│  │   [img]    │  │   [img]    │  │   [img]    │                │
│  └────────────┘  └────────────┘  └────────────┘                │
│                    Live previews                                │
├─────────────────────────────────────────────────────────────────┤
│ Props                                                           │
│ ├─ title:    [Widget_______________]                           │
│ ├─ price:    [29.99________________]                           │
│ ├─ onSale:   [✓]                                               │
│ └─ variant:  ○ default  ● featured  ○ compact                  │
├─────────────────────────────────────────────────────────────────┤
│ State                                                           │
│ ├─ isHovered: [ ]                                              │
│ └─ quantity:  [1_____]                                         │
├─────────────────────────────────────────────────────────────────┤
│ Styles                                                          │
│ ├─ padding:       [16px_____]                                  │
│ └─ border-radius: [8px______]                                  │
├─────────────────────────────────────────────────────────────────┤
│ [Reset] [Copy JSX] [Commit to Code] [Save as Story]            │
└─────────────────────────────────────────────────────────────────┘
```

---

## Phase 4: Code Integration

### 4.1 Commit to Code
**Status:** Planned
**Effort:** High
**Impact:** Very High

Generate code diffs from live edits.

```javascript
// After editing props and styles...
proxy {action: "commit", id: "app"}

→ Generated diff:

// src/pages/Checkout.tsx
- <ProductCard variant="default" />
+ <ProductCard variant="featured" onSale={true} />

// src/components/ProductCard.module.css
- padding: 12px;
+ padding: 16px;
```

**Features:**
- Sourcemap-aware (knows actual file locations)
- Props changes → JSX diff
- Style changes → CSS/SCSS/module diff
- Preview diff before applying
- Direct commit or PR creation

---

### 4.2 Save as Story
**Status:** Planned
**Effort:** Medium
**Impact:** High

Generate Storybook stories from live sessions.

```javascript
// After tweaking a component in live app...
proxy {action: "save_story", id: "app", component: "ProductCard"}

→ Generated: stories/ProductCard.stories.tsx

export const FeaturedOnSale: Story = {
  args: {
    title: "Widget",
    price: 29.99,
    variant: "featured",
    onSale: true
  },
  parameters: {
    viewport: {width: 375, height: 812},
    context: {theme: "dark"}
  }
};
```

---

### 4.3 AI-Suggested Edge Cases
**Status:** Planned
**Effort:** Medium
**Impact:** High

AI suggests test cases based on component context.

```
agnt: "Editing ProductCard. Suggested edge cases:"

      [Empty title]  [Price: $0]  [Price: $9999.99]
      [Long title]   [On sale]    [Out of stock]

      Click any to load across all devices.
```

---

## Phase 5: Concurrent Audits

### 5.1 Live Audit Pipeline
**Status:** Planned
**Effort:** High
**Impact:** High

Run audits on every CSS/prop change.

```
CSS Change Event
       │
       ├─→ A11y Audit (contrast, touch targets)
       ├─→ Responsive Audit (overflow at breakpoints)
       ├─→ Performance Audit (layout thrash)
       └─→ Consistency Audit (matches design system)
       │
       ▼
Aggregated Results + AI Suggestions
```

**Stream to AI:**
```json
{
  "type": "css_audit",
  "change": ".card { border-radius: 12px }",
  "issues": [
    {"severity": "warning", "message": "Other cards use 8px radius"},
    {"severity": "info", "message": "Consider CSS variable --radius-md"}
  ],
  "devices": {
    "desktop": {"status": "ok"},
    "mobile": {"status": "warning", "issue": "Cards overflow at 320px"}
  }
}
```

---

## Quick Reference: Feature Matrix

| Feature | Phase | Effort | Impact | Deps |
|---------|-------|--------|--------|------|
| QR Code sharing | 1.1 | Low | High | - |
| Recording sync | 1.2 | Med | High | - |
| CSS broadcasting | 2.1 | Med | V.High | - |
| CSS editor UI | 2.2 | Med | High | 2.1 |
| DevTools polling | 2.3 | Low | Med | 2.1 |
| DevTools extension | 2.4 | High | High | - |
| Framework detection | 3.1 | Low | Found. | - |
| Component mapping | 3.2 | Med | High | 3.1 |
| Props/state editing | 3.3 | High | V.High | 3.2 |
| Component panel | 3.4 | High | V.High | 3.3 |
| Commit to code | 4.1 | High | V.High | 3.x |
| Save as story | 4.2 | Med | High | 3.x |
| AI edge cases | 4.3 | Med | High | 3.x |
| Live audit pipeline | 5.1 | High | High | 2.x, 3.x |

---

## Implementation Priority

**Immediate (This Week):**
1. QR code in indicator panel

**Short-term (This Month):**
2. CSS patch broadcasting
3. In-browser CSS editor
4. Recording sync to daemon

**Medium-term (Next Quarter):**
5. React component detection + mapping
6. Props/state editing (React)
7. Component inspector panel
8. Vue support

**Long-term:**
9. Commit to code
10. Save as story
11. Live audit pipeline
12. Browser extension for DevTools sync

---

## Non-Goals (For Now)

- **Svelte/Solid component editing** - They compile away; revisit with community input
- **Visual regression in CI** - Playwright/Chromatic do this well
- **Full Storybook replacement** - Complement, not replace
- **Production monitoring** - This is a dev tool

---

## Success Metrics

| Metric | Target |
|--------|--------|
| Time to connect mobile device | < 10 seconds (with QR) |
| CSS change → all devices update | < 100ms |
| Prop change → re-render | < 200ms |
| Commit to code accuracy | 100% (no manual fixup) |
| Supported frameworks | React, Vue (80% market) |

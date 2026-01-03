# agnt Feature Licensing

**BETA STATUS**: All features are currently free and unlocked during beta.
When agnt reaches stable release (v1.0), premium features will require a paid license.

## Free Forever Features

These features will always be free:

- ✅ **Basic reverse proxy** - HTTP proxy with traffic logging and instrumentation
- ✅ **Process management** - Run and manage development processes
- ✅ **Project detection** - Auto-detect project types (Go, Node.js, Python)
- ✅ **Basic accessibility audit** - Essential a11y checks (fallback mode)
- ✅ **Standard accessibility audit** - Industry-standard axe-core WCAG 2.1 testing
- ✅ **Fast accessibility mode** - Quick wins beyond axe-core
- ✅ **Screenshot capture** - Take screenshots from browser
- ✅ **Floating indicator** - Browser panel for quick access
- ✅ **JavaScript error capture** - Automatic frontend error logging
- ✅ **Performance monitoring** - Page load and resource timing
- ✅ **Interaction tracking** - User click, keyboard, scroll tracking
- ✅ **DOM mutation tracking** - Track element additions, removals, modifications
- ✅ **Basic diagnostics** - 50+ DOM inspection and debugging primitives

## Premium Features (Beta - Free Now, Paid Later)

These features are **currently free during beta** but will require a license after v1.0:

### 🎨 Design & Wireframing

- **Sketch Mode** - Excalidraw-like wireframing directly on your UI
  - Shape tools (rectangle, ellipse, line, arrow, free-draw)
  - Wireframe elements (buttons, inputs, sticky notes, image placeholders)
  - Sketchy rendering with configurable roughness
  - Full editing (select, move, resize, delete, undo/redo)
  - JSON export/import

- **Design Mode** - AI-assisted UI design iteration
  - Visual element selection overlay
  - Context-aware design alternative generation
  - Live preview of alternatives
  - Natural language design refinement
  - Navigation between design options

### ♿ Advanced Accessibility

- **Comprehensive Accessibility Audit** - CSS-aware state validation
  - Builds reverse index of CSS rules and media queries
  - Tracks which media queries affect each element
  - Tests contrast in current state (default + focus)
  - Discovers and reports all breakpoints and color schemes
  - Enumerates exact testing requirements
  - Flags cross-origin CSS access issues
  - Warns about untested states

### 🌐 Mobile & Remote Testing

- **Tunnel Integration** - Expose local servers publicly
  - Cloudflare Quick Tunnels support
  - ngrok integration
  - Auto-configuration of proxy public URLs
  - Mobile device testing support

### 📸 Quality Assurance

- **Visual Regression Testing** - Snapshot and baseline comparison
  - Create baselines from screenshots
  - Compare current vs baseline with diff detection
  - Configurable sensitivity thresholds
  - Multi-page and multi-viewport support

### 🌪️ Chaos Engineering

- **Chaos Proxy** - Network condition simulation
  - Latency injection (min/max with jitter)
  - Packet drops and truncation
  - Error injection (custom HTTP status codes)
  - Request reordering
  - Stale data simulation
  - Bandwidth throttling
  - Presets (mobile-3g, mobile-4g, flaky-api, race-condition, etc.)

## Licensing Model (Post-Beta)

**Planned pricing** (subject to change):

- **Free Tier**: All free features forever, no credit card required
- **Pro License**: ~$49/year per developer
  - All premium features unlocked
  - Email support
  - License file-based (offline validation)
  - Perpetual fallback (if you stop paying, features still work but no updates)

- **Team License**: ~$199/year for 5 developers
  - All pro features
  - Priority support
  - Shared license server option

## Beta Access

During beta (current state):
- **All features are free** - Try everything without restrictions
- **Beta feedback encouraged** - Help shape the final feature set
- **Early adopter discount** - Beta testers will get launch discount codes
- **Feature finalization** - Features may move between tiers based on feedback

## Implementation Status

### License System (Planned)
- ✅ Feature flag constants defined (`internal/license/license.go`)
- ✅ Stub functions that always return true during beta
- ✅ Clear labeling of premium features in code
- ⏳ Ed25519 signature validation (planned post-beta)
- ⏳ License file format and loading (planned post-beta)
- ⏳ License generation server (planned post-beta)

### Feature Gates (Planned)
Premium features will check licenses via:
```go
import "github.com/standardbeagle/agnt/internal/license"

if err := license.RequireFeature(license.FeatureSketchMode); err != nil {
    return fmt.Errorf("sketch mode requires a license: %w\nGet yours at https://agnt.dev/pricing", err)
}
```

Currently, `RequireFeature()` always returns `nil` (all features unlocked).

## Philosophy

**Why free tier is generous:**
- agnt is a developer tool - developers should have great free tools
- Free tier is fully functional for most development workflows
- Premium features are nice-to-haves, not must-haves
- We want agnt to be accessible to students and open source projects

**Why premium tier exists:**
- Supports ongoing development and maintenance
- Enables faster feature development
- Funds infrastructure (license server, CDN, support)
- Aligns incentives (we build what users pay for)

## Questions?

- 💬 Feedback on pricing/tiers: https://github.com/standardbeagle/agnt/discussions
- 📧 Licensing questions: license@agnt.dev
- 🐛 Bug reports: https://github.com/standardbeagle/agnt/issues

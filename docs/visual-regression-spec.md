# Visual Regression Testing - Implementation Spec

## Overview
Add visual regression testing capability to agnt, allowing before/after screenshot comparison to verify only expected changes occurred during development.

## User Flow

### 1. Before Making Changes
```bash
# Capture baseline snapshots
agnt snapshot baseline --name "before-header-refactor" --url "http://localhost:3000"

# Or capture multiple pages
agnt snapshot baseline --name "before-auth-update" --urls "
  http://localhost:3000/login
  http://localhost:3000/signup
  http://localhost:3000/dashboard
"

# Or use current proxy
agnt snapshot baseline --proxy dev --pages "/, /about, /contact"
```

**What happens:**
- Captures screenshots of specified pages
- Stores in `~/.agnt/baselines/{name}/`
- Saves metadata (URL, viewport, timestamp, git commit)
- Returns snapshot ID for later reference

### 2. Make Changes
Developer or AI agent makes code changes, refactors, upgrades dependencies, etc.

### 3. After Changes - Compare
```bash
# Compare current state to baseline
agnt snapshot compare --baseline "before-header-refactor"

# Or auto-compare to last baseline
agnt snapshot compare --last

# With specific viewports
agnt snapshot compare --baseline "before-auth-update" --viewports "mobile,tablet,desktop"
```

**What happens:**
- Captures new screenshots of same pages
- Performs pixel-level diff
- Generates diff images highlighting changes
- Sends to Claude with vision API for analysis
- Returns intelligent report

### 4. Claude Analysis Output
```
Visual Regression Report: before-header-refactor → current
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

📄 Page: / (Home)
  ✓ Header height: 80px → 64px (expected - header refactor)
  ✓ Logo position: adjusted (expected - mentioned in task)
  ⚠️  Navigation spacing changed slightly (minor)

📄 Page: /about
  ✓ No visual changes detected

📄 Page: /contact
  ❌ Footer position: moved down 50px (UNEXPECTED)
  ❌ Form submit button: partially cut off (UNEXPECTED)

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Summary: 2 unexpected changes found
View diffs: ~/.agnt/diffs/before-header-refactor/
```

## Architecture

### Storage Structure
```
~/.agnt/
├── baselines/
│   ├── before-header-refactor/
│   │   ├── metadata.json
│   │   ├── home.png
│   │   ├── about.png
│   │   └── contact.png
│   └── before-auth-update/
│       └── ...
├── diffs/
│   └── before-header-refactor-20251213/
│       ├── home-diff.png
│       ├── home-baseline.png
│       ├── home-current.png
│       └── report.json
└── config.json
```

### Metadata Format
```json
{
  "name": "before-header-refactor",
  "timestamp": "2025-12-13T22:30:00Z",
  "git_commit": "a645d91",
  "git_branch": "feature/header-refactor",
  "pages": [
    {
      "url": "http://localhost:3000/",
      "viewport": { "width": 1920, "height": 1080 },
      "screenshot": "home.png",
      "dom_snapshot": "home.html",
      "computed_styles": "home.css.json"
    }
  ],
  "config": {
    "diff_threshold": 0.01,
    "ignore_regions": [
      { "selector": ".timestamp", "reason": "Dynamic content" },
      { "selector": ".ad-banner", "reason": "Third-party" }
    ]
  }
}
```

## Implementation Components

### 1. New MCP Tool: `snapshot`
```javascript
{
  name: "snapshot",
  description: "Capture and compare visual snapshots for regression testing",
  inputSchema: {
    action: "baseline" | "compare" | "list" | "delete",
    name: "string",           // Baseline name
    baseline: "string",       // For compare action
    urls: "string[]",         // Pages to capture
    proxy_id: "string",       // Use existing proxy
    viewports: "string[]",    // mobile, tablet, desktop
    ignore: "string[]",       // CSS selectors to ignore
    threshold: "number"       // Diff sensitivity (0-1)
  }
}
```

### 2. Backend Components

**`internal/snapshot/manager.go`**
- Manage baseline storage
- Coordinate capture process
- Perform diff operations

**`internal/snapshot/capture.go`**
- Use html2canvas (existing) or headless browser
- Capture screenshots via proxy
- Save DOM snapshots

**`internal/snapshot/differ.go`**
- Pixel-level comparison using image library
- Generate diff images
- Calculate change percentage
- Ignore specified regions

**`internal/snapshot/analyzer.go`**
- Send images to Claude vision API
- Provide context about expected changes
- Parse AI response into structured report

### 3. Image Diffing Library
Options:
- **pixelmatch** (JS) - Fast pixel diffing
- **go-diff** (Go) - Native Go image comparison
- **playwright** - Built-in screenshot comparison

### 4. Claude Vision Integration
```go
func AnalyzeDiff(baseline, current, diff image.Image, context string) (*Report, error) {
    prompt := fmt.Sprintf(`
Compare these before/after screenshots of a web application.

Context: %s

Expected changes:
- [List from commit message or task description]

Analyze:
1. Visual differences between baseline and current
2. Which changes were expected vs unexpected
3. Any regressions or bugs introduced
4. Layout issues, cut-off elements, overlaps

Return structured report.
`, context)

    // Send to Claude with vision
    response := claude.AnalyzeImages(prompt, baseline, current, diff)

    return parseReport(response)
}
```

## Advanced Features

### Ignore Dynamic Content
```json
{
  "ignore_regions": [
    { "selector": ".timestamp", "reason": "Dynamic date/time" },
    { "selector": ".user-avatar", "reason": "User-specific" },
    { "selector": "[data-testid='ad']", "reason": "Third-party ads" }
  ]
}
```

### Multiple Viewports
```bash
agnt snapshot baseline --viewports "
  mobile: 375x667,
  tablet: 768x1024,
  desktop: 1920x1080
"
```

### Git Integration
```bash
# Auto-baseline on feature branch creation
git checkout -b feature/new-header
agnt snapshot baseline --auto --git

# Auto-compare before committing
git commit -m "Update header"
# → Triggers: agnt snapshot compare --baseline feature/new-header
```

### CI/CD Integration
```yaml
# .github/workflows/visual-regression.yml
- name: Visual Regression Test
  run: |
    agnt snapshot compare --baseline main
    if [ $? -ne 0 ]; then
      echo "Visual regressions detected!"
      exit 1
    fi
```

### Interactive Approval
```bash
agnt snapshot compare --interactive

# Shows each diff:
# ┌─────────────────────────────────────┐
# │ home.png - Header changed           │
# │ [Baseline] [Current] [Diff]         │
# │                                     │
# │ Expected change? [y/n/skip]:        │
# └─────────────────────────────────────┘

# Approved diffs become new baseline
```

## Benefits for AI-Assisted Development

### Scenario 1: Refactoring
```
Human: "Refactor the header component to use flexbox"
Claude: *refactors code*
Claude: "Let me verify only expected changes occurred..."
Claude: agnt snapshot compare --baseline before-refactor
Claude: "✓ Changes look good! Only header layout changed as expected."
```

### Scenario 2: Dependency Upgrade
```
Human: "Upgrade React from 18.2 to 18.3"
Claude: agnt snapshot baseline --name before-react-upgrade
Claude: *upgrades package.json, runs build*
Claude: agnt snapshot compare --baseline before-react-upgrade
Claude: "⚠️ Warning: Button styling changed unexpectedly.
         React 18.3 may have different default styles."
```

### Scenario 3: CSS Changes
```
Human: "Make the login button bigger"
Claude: *updates CSS*
Claude: agnt snapshot compare --last
Claude: "❌ Problem detected! The bigger button is now pushing
         the footer off-screen on mobile. Let me fix that..."
```

## Implementation Priority

### Phase 1: MVP (P0) - ~2-3 days
- [x] Basic baseline capture via existing screenshot
- [x] Simple pixel diff (Go image library)
- [x] File storage in ~/.agnt/baselines/
- [x] MCP tool integration
- [x] Basic diff report

### Phase 2: Intelligence (P1) - ~2-3 days
- [x] Claude vision API integration
- [x] Intelligent diff analysis
- [x] Expected vs unexpected changes
- [x] Structured report generation

### Phase 3: Polish (P2) - ~1-2 days
- [ ] Multiple viewports
- [ ] Ignore regions
- [ ] Git integration
- [ ] Interactive approval

### Phase 4: Advanced (P3) - Future
- [ ] CI/CD runners
- [ ] Headless browser option
- [ ] DOM diffing (not just pixels)
- [ ] Performance regression detection
- [ ] A/B comparison mode

## API Examples

### Programmatic Usage
```javascript
// In browser
__devtool.snapshot.capture('before-changes')

// Later
__devtool.snapshot.compare('before-changes', {
  threshold: 0.01,
  ignore: ['.timestamp', '.ad'],
  viewports: ['mobile', 'desktop']
})
```

### MCP Tool Usage
```json
{
  "tool": "snapshot",
  "input": {
    "action": "baseline",
    "name": "before-upgrade",
    "proxy_id": "dev",
    "pages": ["/", "/about", "/contact"]
  }
}
```

## Configuration

### Global Config (~/.agnt/config.json)
```json
{
  "snapshot": {
    "storage_path": "~/.agnt/baselines",
    "diff_threshold": 0.01,
    "max_baselines": 10,
    "claude_api_key": "sk-...",
    "default_viewports": {
      "mobile": { "width": 375, "height": 667 },
      "tablet": { "width": 768, "height": 1024 },
      "desktop": { "width": 1920, "height": 1080 }
    },
    "auto_git_baseline": false,
    "ci_mode": false
  }
}
```

### Project Config (.agnt.yml)
```yaml
snapshot:
  pages:
    - url: /
      name: home
    - url: /login
      name: auth
    - url: /dashboard
      name: dashboard
  ignore:
    - selector: .timestamp
      reason: Dynamic content
    - selector: .user-avatar
      reason: User-specific
  git_hooks:
    pre_commit: compare
    post_checkout: baseline
```

## Success Metrics

- ✅ Catches 90%+ of visual regressions
- ✅ <5 second capture time per page
- ✅ <10 second analysis time with Claude
- ✅ <5% false positive rate
- ✅ Zero configuration for basic usage
- ✅ Works in CI/CD pipelines

## Notes

- Use existing html2canvas for screenshots (already in codebase)
- Leverage existing proxy infrastructure
- Store baselines locally (no cloud dependency for MVP)
- Claude vision API calls are optional (can do pixel diff only)
- Compatible with existing agnt workflow

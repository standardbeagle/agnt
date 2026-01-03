# Audit System Overhaul Tasks

## Problem Statement

When AI agents run audits through the agnt UI, they:
1. Receive verbose, unstructured data that's hard to parse quickly
2. Waste time re-running audits because results don't clearly indicate what was checked
3. Can't easily identify which issues are actionable vs informational
4. Lack element selectors to target fixes
5. Don't get prioritized action items
6. See no clear "what to do next" guidance

## Overarching Principles

All audits should return responses optimized for LLM consumption:
- **Action-first**: Separate `fixable` issues (with selectors) from `informational` issues
- **Unique IDs**: Each issue gets a stable ID for tracking fixes across runs
- **Priority scoring**: 1-10 impact score for prioritization
- **Selectors included**: Every fixable issue includes CSS selector(s)
- **Concise summaries**: Top-level `summary` field with 1-2 sentence actionable overview
- **Deduplication**: Similar issues grouped with count, not repeated
- **Recommended actions**: `actions` array with specific fix instructions

## Common Response Schema (apply to all audits)

```javascript
{
  summary: "3 critical issues require immediate action: missing alt text on 5 images, 2 form inputs without labels",
  score: 72,           // 0-100 quality score
  grade: "C",          // A-F letter grade
  checkedAt: "...",    // ISO timestamp
  checksRun: [...],    // List of check IDs that were executed

  fixable: [
    {
      id: "img-alt-missing-1",
      type: "missing-alt",
      severity: "error",      // error | warning | info
      impact: 9,              // 1-10
      selector: "img.hero-image",
      element: "<img src='...' class='hero-image'>",  // truncated HTML
      message: "Image missing alt text",
      fix: "Add alt='description of hero image' attribute",
      wcag: "1.1.1"           // standard reference if applicable
    }
  ],

  informational: [
    {
      id: "dom-depth-high",
      type: "dom-complexity",
      severity: "info",
      message: "DOM depth of 24 levels detected (threshold: 20)",
      context: { depth: 24, threshold: 20 }
    }
  ],

  actions: [
    "Add alt text to 5 images (selectors: img.hero-image, img.product-*)",
    "Associate labels with form inputs #email and #phone"
  ],

  stats: {
    errors: 3,
    warnings: 5,
    info: 2,
    fixable: 6,
    informational: 4
  }
}
```

---

## Task 1: Overhaul auditAccessibility

**File**: `internal/proxy/scripts/accessibility.js`

**Current Problems**:
- axe-core results are verbose and not action-oriented
- Basic fallback audit is too minimal
- No element selectors for fixes
- Violations not grouped by fix type
- No priority scoring

**Required Changes**:

1. **Transform axe-core output** to action-oriented format:
   - Extract CSS selectors from axe nodes
   - Group violations by fix type (e.g., all missing alt texts together)
   - Add fix instructions for each violation type
   - Include WCAG references

2. **Enhance basic fallback audit**:
   - Check all form inputs have labels
   - Check all images have alt text
   - Check heading hierarchy (no skipped levels)
   - Check link text is descriptive (not "click here")
   - Check buttons have accessible names
   - Check focus indicators exist

3. **Add new checks**:
   - Color contrast issues with actual color values
   - Keyboard trap detection
   - ARIA misuse (roles without required attributes)
   - Missing skip links
   - Form error association

4. **Response format**:
```javascript
{
  summary: "5 accessibility errors blocking screen reader users",
  score: 65,
  fixable: [
    {
      id: "alt-1",
      type: "missing-alt",
      selector: "img[src*='hero']",
      impact: 9,
      fix: "Add alt='[describe image]' attribute",
      wcag: "1.1.1"
    }
  ],
  actions: [
    "Add alt text to 5 images",
    "Associate labels with 2 form inputs"
  ]
}
```

**Acceptance Criteria**:
- [ ] All fixable issues include CSS selectors
- [ ] Issues grouped by type with counts
- [ ] Actions array provides clear next steps
- [ ] axe-core results transformed (not raw)
- [ ] Basic audit covers 10+ check types

---

## Task 2: Overhaul auditDOMComplexity

**File**: `internal/proxy/scripts/audit.js`

**Current Problems**:
- Only reports counts, not issues
- No actionable recommendations
- Doesn't identify problem areas
- Missing depth analysis per subtree

**Required Changes**:

1. **Add issue detection**:
   - Identify elements with >10 children (candidates for componentization)
   - Find deeply nested elements (>15 levels) with selectors
   - Detect large subtrees (>100 descendants)
   - Find elements with excessive attributes
   - Identify duplicate ID violations

2. **Add performance concerns**:
   - Large lists without virtualization hints
   - Tables with >100 rows
   - Forms with >20 inputs
   - Excessive event handlers on single elements

3. **Provide actionable recommendations**:
```javascript
{
  summary: "DOM complexity is high (2847 elements). 3 areas need refactoring",
  score: 58,
  fixable: [
    {
      id: "deep-nest-1",
      type: "excessive-depth",
      selector: ".sidebar > .menu > .submenu > .item > .content > ...",
      depth: 18,
      impact: 6,
      fix: "Flatten nesting or extract to component"
    }
  ],
  hotspots: [
    { selector: ".product-grid", descendants: 450, recommendation: "Consider virtualization" }
  ],
  actions: [
    "Refactor .sidebar menu structure (18 levels deep)",
    "Virtualize .product-grid (450 elements)"
  ]
}
```

**Acceptance Criteria**:
- [ ] Identifies specific problem elements with selectors
- [ ] Provides refactoring recommendations
- [ ] Hotspots array for large subtrees
- [ ] Actions array with specific guidance

---

## Task 3: Overhaul auditCSS

**File**: `internal/proxy/scripts/audit.js`

**Current Problems**:
- Only checks inline styles and !important
- Doesn't analyze actual CSS rules
- No specificity issues detected
- Missing modern CSS problems

**Required Changes**:

1. **Expand checks**:
   - Overly specific selectors (>3 IDs or >5 classes)
   - Unused CSS detection (if stylesheet accessible)
   - Conflicting rules on same element
   - Vendor prefix without standard property
   - Deprecated properties (e.g., `-webkit-appearance`)
   - Hardcoded colors (not using variables)
   - Hardcoded sizes (not using rems/variables)

2. **Inline style analysis**:
   - Categorize inline styles (layout vs visual vs animation)
   - Identify inline styles that should be classes
   - Find duplicate inline style patterns

3. **Layout issues**:
   - Fixed width/height on responsive elements
   - Absolute positioning overuse
   - Z-index inflation (values >100)

4. **Response format**:
```javascript
{
  summary: "45 inline styles found, 12 should be extracted to classes",
  score: 71,
  fixable: [
    {
      id: "inline-1",
      type: "inline-style-pattern",
      selector: "[style*='display: flex']",
      count: 8,
      pattern: "display: flex; justify-content: center",
      fix: "Extract to .flex-center class"
    }
  ],
  informational: [
    { type: "important-count", count: 23, message: "23 !important declarations" }
  ],
  actions: [
    "Create .flex-center utility class (used 8 times inline)",
    "Review 23 !important declarations for necessity"
  ]
}
```

**Acceptance Criteria**:
- [ ] Detects inline style patterns that should be classes
- [ ] Checks specificity issues
- [ ] Identifies hardcoded values
- [ ] Provides class extraction suggestions

---

## Task 4: Overhaul auditSecurity

**File**: `internal/proxy/scripts/audit.js`

**Current Problems**:
- Only 4 checks total
- Missing many client-side security issues
- No severity prioritization
- No context about exploitability

**Required Changes**:

1. **Expand security checks**:
   - XSS vectors: `innerHTML`, `outerHTML`, `document.write` usage
   - Eval usage detection
   - Insecure localStorage/sessionStorage of sensitive data patterns
   - Exposed API keys in scripts or HTML
   - Clickjacking vulnerability (missing X-Frame-Options check)
   - Open redirects (window.location with user input)
   - Postmessage without origin check
   - Third-party scripts from untrusted origins

2. **Form security**:
   - Password fields without autocomplete="new-password"
   - Forms missing CSRF tokens
   - Login forms over HTTP
   - Sensitive data in GET parameters

3. **Content security**:
   - Inline scripts without nonce
   - External resources without SRI
   - Mixed content (already exists, enhance)

4. **Response format**:
```javascript
{
  summary: "2 critical security issues: exposed API key, insecure form",
  score: 45,
  critical: [
    {
      id: "api-key-exposed",
      type: "exposed-secret",
      selector: "script:contains('sk_live_')",
      pattern: "sk_live_*****",
      impact: 10,
      fix: "Move API key to server-side environment variable"
    }
  ],
  fixable: [...],
  actions: [
    "URGENT: Remove exposed API key from client-side code",
    "Add rel='noopener' to 15 external links"
  ]
}
```

**Acceptance Criteria**:
- [ ] Detects exposed secrets patterns
- [ ] Checks XSS vector usage
- [ ] Form security validation
- [ ] Critical issues separated and prioritized
- [ ] 15+ security check types

---

## Task 5: Overhaul auditPageQuality (SEO)

**File**: `internal/proxy/scripts/audit.js`

**Current Problems**:
- Only 6 basic checks
- No content quality analysis
- Missing structured data validation
- No mobile/responsive checks

**Required Changes**:

1. **Meta tag expansion**:
   - Open Graph tags (og:title, og:description, og:image)
   - Twitter Card tags
   - Canonical URL
   - Robots meta
   - Hreflang for internationalization

2. **Content quality**:
   - Title length (50-60 chars optimal)
   - Description length (150-160 chars optimal)
   - Heading hierarchy validation
   - Image alt text coverage percentage
   - Link text quality (avoid "click here", "read more")
   - Content-to-code ratio

3. **Structured data**:
   - JSON-LD presence and validity
   - Schema.org type detection
   - Required properties check

4. **Technical SEO**:
   - Canonical self-reference
   - Mobile viewport
   - Crawlable links (no javascript:void)
   - Image optimization hints (WebP, lazy loading)

5. **Response format**:
```javascript
{
  summary: "SEO score 72/100. Missing OG tags and 3 images without alt",
  score: 72,
  grade: "C+",
  meta: {
    title: { value: "Page Title", length: 45, optimal: true },
    description: { value: "...", length: 180, tooLong: true }
  },
  fixable: [
    {
      id: "og-missing",
      type: "missing-og-tags",
      fix: "Add og:title, og:description, og:image meta tags"
    }
  ],
  actions: [
    "Add Open Graph meta tags for social sharing",
    "Shorten meta description from 180 to 160 characters"
  ]
}
```

**Acceptance Criteria**:
- [ ] Validates all major meta tags
- [ ] Checks Open Graph and Twitter cards
- [ ] Analyzes content quality metrics
- [ ] Structured data validation
- [ ] 20+ quality check types

---

## Task 6: Overhaul auditPerformance

**File**: `internal/proxy/scripts/audit.js`

**Current Problems**:
- Resource list is too verbose
- Missing actionable recommendations
- No prioritization of slow resources
- Limited Core Web Vitals context

**Required Changes**:

1. **Enhanced metrics**:
   - CLS (Cumulative Layout Shift) if available
   - INP (Interaction to Next Paint) if available
   - Bundle size analysis
   - Third-party script impact

2. **Resource optimization**:
   - Unoptimized images (large dimensions, no lazy loading)
   - Render-blocking resources
   - Unused JavaScript detection hints
   - Font loading optimization (display: swap)
   - Cache header analysis

3. **Network analysis**:
   - Slow domains (group by origin)
   - Large payloads (>100KB)
   - Redirect chains
   - Connection reuse

4. **Actionable format**:
```javascript
{
  summary: "LCP 3.2s (poor). 2 render-blocking scripts, 5 unoptimized images",
  score: 58,
  coreWebVitals: {
    lcp: { value: 3200, rating: "poor", target: 2500 },
    fcp: { value: 1800, rating: "needs-improvement", target: 1800 },
    cls: { value: 0.15, rating: "needs-improvement", target: 0.1 }
  },
  fixable: [
    {
      id: "render-block-1",
      type: "render-blocking",
      selector: "script[src*='analytics']",
      impact: 8,
      fix: "Add async or defer attribute"
    },
    {
      id: "img-unopt-1",
      type: "unoptimized-image",
      selector: "img.hero",
      size: "2.4MB",
      dimensions: "4000x3000",
      fix: "Resize to 1200px width, convert to WebP, add loading='lazy'"
    }
  ],
  slowestResources: [
    { url: "/api/data", duration: 1200, type: "fetch" }
  ],
  actions: [
    "Defer analytics script (blocking LCP by ~400ms)",
    "Optimize hero image: resize, compress, lazy load",
    "Investigate slow /api/data endpoint (1.2s)"
  ]
}
```

**Acceptance Criteria**:
- [ ] Core Web Vitals with ratings
- [ ] Render-blocking resource detection
- [ ] Image optimization recommendations with specifics
- [ ] Slowest resources highlighted
- [ ] Actions include estimated impact

---

## Task 7: Add Unified auditAll Function

**File**: `internal/proxy/scripts/audit.js`

Create a master audit function that runs all audits and provides a unified report.

**Requirements**:

1. **Parallel execution** where possible
2. **Unified summary** across all audit types
3. **Prioritized actions** list combining all audits
4. **Overall score** computed from individual scores

**Response format**:
```javascript
{
  summary: "Overall score 68/100. 3 critical issues, 12 high priority fixes",
  overallScore: 68,
  grade: "D+",

  audits: {
    accessibility: { score: 72, errors: 3, warnings: 5 },
    security: { score: 45, critical: 2, errors: 1 },
    performance: { score: 58, coreWebVitals: {...} },
    seo: { score: 78, errors: 1, warnings: 4 },
    dom: { score: 82, hotspots: 2 },
    css: { score: 71, inlineStyles: 45 }
  },

  prioritizedActions: [
    { priority: 1, audit: "security", action: "Remove exposed API key", impact: 10 },
    { priority: 2, audit: "accessibility", action: "Add alt text to images", impact: 9 },
    { priority: 3, audit: "performance", action: "Defer render-blocking scripts", impact: 8 }
  ],

  criticalIssues: [...],  // Top 5 most impactful
  quickWins: [...]        // Low effort, high impact
}
```

**Acceptance Criteria**:
- [ ] Runs all audits efficiently
- [ ] Provides unified scoring
- [ ] Prioritizes actions across audit types
- [ ] Identifies quick wins vs critical issues

---

## Task 8: Update Indicator UI for Better Summaries

**File**: `internal/proxy/scripts/indicator.js`

**Current Problems**:
- `formatAuditSummary` produces generic summaries
- Results displayed as raw JSON in attachments
- No action items shown prominently

**Required Changes**:

1. **Improve formatAuditSummary**:
   - Use the new `summary` field from audit results
   - Show score/grade prominently
   - List top 3 actions

2. **Attachment display**:
   - Show `actions` array as bullet list
   - Collapse full result JSON by default
   - Highlight critical/error items

3. **Panel integration**:
   - Show overall score badge
   - Quick action buttons for common fixes

**Acceptance Criteria**:
- [ ] Summaries use new audit format
- [ ] Actions displayed as actionable list
- [ ] Critical issues highlighted
- [ ] Results collapsible for detail

---

## Implementation Order

1. **Task 7** (auditAll) - Define unified schema first
2. **Task 1** (accessibility) - Highest user impact
3. **Task 4** (security) - Critical for production sites
4. **Task 6** (performance) - Core Web Vitals focus
5. **Task 5** (pageQuality/SEO) - Common use case
6. **Task 2** (DOM complexity) - Developer focused
7. **Task 3** (CSS) - Developer focused
8. **Task 8** (indicator UI) - Polish after audits improved

---

## Testing Requirements

For each audit:
1. Test on minimal HTML page (should find no issues)
2. Test on page with known issues (verify detection)
3. Test on complex production-like page (performance)
4. Verify selectors work with `document.querySelector()`
5. Verify actions are actionable (agent can execute fixes)

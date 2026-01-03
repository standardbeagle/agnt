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

## Task 9: Build CLI Automation Layer with claude-go

**Files**: `internal/automation/` (new package)

**Purpose**: Create a general-purpose CLI automation layer using [claude-go](https://github.com/standardbeagle/claude-go) (local: `~/work/claude-go`) that enables agent-based processing throughout agnt. This is the foundation for audit processing and other automation tasks.

**Local Development**:
```bash
# Add to go.mod for local development
replace github.com/standardbeagle/claude-go => ../claude-go
```

**Why claude-go**:
- Go SDK for Claude Code CLI automation
- Concurrent session management
- In-process MCP tool definition
- Hooks for tool execution control
- Streaming responses
- Thread-safe design

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         agnt daemon                              │
├─────────────────────────────────────────────────────────────────┤
│  internal/automation/                                            │
│  ├── processor.go      # Core Processor type                    │
│  ├── tasks.go          # Task definitions (audit, summarize)    │
│  ├── prompts.go        # System prompts for each task type      │
│  └── pool.go           # Worker pool for concurrent processing  │
├─────────────────────────────────────────────────────────────────┤
│  Daemon Integration                                              │
│  └── hub_handlers.go   # AUTOMATE verb handlers                 │
└─────────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────────┐
│  github.com/standardbeagle/claude-go                            │
│  ├── Query()           # One-shot processing                    │
│  ├── Client            # Session management                     │
│  └── AgentOptions      # Model, permissions, tools              │
└─────────────────────────────────────────────────────────────────┘
```

### Core Components

**1. Processor Type** (`internal/automation/processor.go`):
```go
package automation

import (
    claude "github.com/standardbeagle/claude-go"
)

// Processor handles agent-based automation tasks
type Processor struct {
    client      *claude.Client
    model       string           // "haiku" for fast tasks, "sonnet" for complex
    maxBudget   float64          // Cost limit per task
    prompts     *PromptRegistry  // Task-specific system prompts
}

// ProcessorConfig configures the automation processor
type ProcessorConfig struct {
    Model           string  // Default model (haiku recommended for most tasks)
    MaxBudgetUSD    float64 // Max cost per task
    MaxTurns        int     // Max conversation turns
    WorkingDir      string  // Working directory context
    AllowedTools    []string
    DisallowedTools []string
}

// DefaultConfig returns config optimized for automation tasks
func DefaultConfig() ProcessorConfig {
    return ProcessorConfig{
        Model:        "haiku",     // Fast and cheap for processing
        MaxBudgetUSD: 0.01,        // $0.01 limit per task
        MaxTurns:     3,           // Usually 1-2 turns needed
        DisallowedTools: []string{ // No file/bash access for processing
            "Bash", "Write", "Edit", "Read",
        },
    }
}

// New creates a new automation processor
func New(cfg ProcessorConfig) (*Processor, error)

// Process runs a task and returns the result
func (p *Processor) Process(ctx context.Context, task Task) (*Result, error)

// ProcessBatch runs multiple tasks concurrently
func (p *Processor) ProcessBatch(ctx context.Context, tasks []Task) ([]*Result, error)

// Close shuts down the processor
func (p *Processor) Close() error
```

**2. Task Definition** (`internal/automation/tasks.go`):
```go
// TaskType identifies the type of automation task
type TaskType string

const (
    TaskTypeAuditProcess    TaskType = "audit_process"    // Process raw audit data
    TaskTypeSummarize       TaskType = "summarize"        // Summarize content
    TaskTypePrioritize      TaskType = "prioritize"       // Prioritize action items
    TaskTypeGenerateFixes   TaskType = "generate_fixes"   // Generate fix suggestions
    TaskTypeCorrelate       TaskType = "correlate"        // Correlate related issues
)

// Task represents an automation task to process
type Task struct {
    Type     TaskType               // Task type
    Input    interface{}            // Task-specific input data
    Context  map[string]interface{} // Additional context (page URL, etc.)
    Options  TaskOptions            // Processing options
}

// TaskOptions configures task processing
type TaskOptions struct {
    Model       string  // Override default model for this task
    MaxTokens   int     // Max response tokens
    Temperature float64 // 0.0-1.0, lower = more deterministic
}

// Result represents the processed output
type Result struct {
    Type      TaskType
    Output    interface{}            // Task-specific output
    Tokens    int                    // Tokens used
    Cost      float64                // Cost in USD
    Duration  time.Duration          // Processing time
    Error     error                  // Any error
}

// AuditProcessInput is input for audit processing tasks
type AuditProcessInput struct {
    AuditType string                 // accessibility, security, etc.
    RawData   map[string]interface{} // Raw audit output from browser
    PageURL   string                 // URL being audited
    PageTitle string                 // Page title
}

// AuditProcessOutput is output from audit processing
type AuditProcessOutput struct {
    Summary          string                   // 1-2 sentence summary
    Score            int                      // 0-100 score
    Grade            string                   // A-F grade
    Fixable          []FixableIssue           // Issues with selectors
    Informational    []InformationalIssue     // Non-actionable info
    Actions          []string                 // Prioritized actions
    CorrelatedGroups []CorrelatedGroup        // Related issues grouped
}

// FixableIssue represents an actionable issue
type FixableIssue struct {
    ID       string `json:"id"`
    Type     string `json:"type"`
    Severity string `json:"severity"`
    Impact   int    `json:"impact"`
    Selector string `json:"selector"`
    Message  string `json:"message"`
    Fix      string `json:"fix"`
    Standard string `json:"standard,omitempty"` // WCAG, etc.
}
```

**3. Prompt Registry** (`internal/automation/prompts.go`):
```go
// PromptRegistry holds system prompts for each task type
type PromptRegistry struct {
    prompts map[TaskType]string
}

// DefaultPromptRegistry returns prompts optimized for automation
func DefaultPromptRegistry() *PromptRegistry {
    return &PromptRegistry{
        prompts: map[TaskType]string{
            TaskTypeAuditProcess: auditProcessPrompt,
            TaskTypeSummarize:    summarizePrompt,
            TaskTypePrioritize:   prioritizePrompt,
        },
    }
}

var auditProcessPrompt = `You are an audit result processor. Transform raw audit data into actionable output.

RULES:
1. Generate a 1-2 sentence summary focusing on the most impactful issues
2. Calculate a 0-100 score based on issue severity and count
3. Separate issues into "fixable" (have CSS selectors) and "informational"
4. For each fixable issue, provide a specific fix instruction
5. Group related issues (e.g., all missing alt texts together)
6. Prioritize actions by impact (1-10 scale)
7. Use the exact CSS selectors from the input - do not modify them

OUTPUT FORMAT (JSON):
{
  "summary": "...",
  "score": N,
  "grade": "A-F",
  "fixable": [...],
  "informational": [...],
  "actions": ["action 1", "action 2", ...],
  "correlatedGroups": [...]
}

Do not include explanations outside the JSON. Output only valid JSON.`
```

**4. Worker Pool** (`internal/automation/pool.go`):
```go
// Pool manages concurrent task processing
type Pool struct {
    processor   *Processor
    workers     int
    taskQueue   chan Task
    resultQueue chan *Result
    wg          sync.WaitGroup
}

// NewPool creates a worker pool for batch processing
func NewPool(processor *Processor, workers int) *Pool

// Submit adds a task to the queue
func (p *Pool) Submit(task Task)

// Results returns a channel of results
func (p *Pool) Results() <-chan *Result

// Wait blocks until all tasks are processed
func (p *Pool) Wait()

// Close shuts down the pool
func (p *Pool) Close()
```

### Daemon Integration

**5. Hub Handlers** (`internal/daemon/hub_handlers.go`):
```go
// Add AUTOMATE verb handlers

func (d *Daemon) hubHandleAutomate(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
    switch cmd.SubVerb {
    case "PROCESS":
        return d.hubHandleAutomateProcess(ctx, conn, cmd)
    case "BATCH":
        return d.hubHandleAutomateBatch(ctx, conn, cmd)
    case "STATUS":
        return d.hubHandleAutomateStatus(ctx, conn, cmd)
    default:
        return conn.WriteInvalidAction(cmd.Verb, cmd.SubVerb, []string{"PROCESS", "BATCH", "STATUS"})
    }
}

func (d *Daemon) hubHandleAutomateProcess(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
    var input struct {
        Type    string                 `json:"type"`
        Data    map[string]interface{} `json:"data"`
        Options map[string]interface{} `json:"options,omitempty"`
    }

    if err := json.Unmarshal(cmd.Data, &input); err != nil {
        return conn.WriteErr(protocol.ErrInvalidArgs, "invalid JSON")
    }

    task := automation.Task{
        Type:  automation.TaskType(input.Type),
        Input: input.Data,
    }

    result, err := d.processor.Process(ctx, task)
    if err != nil {
        return conn.WriteErr(protocol.ErrInternal, err.Error())
    }

    return conn.WriteJSONStruct(result)
}
```

**6. Protocol Extension** (`internal/protocol/commands.go`):
```go
// Add automation verbs
const (
    VerbAutomate = "AUTOMATE"
)

const (
    SubVerbProcess = "PROCESS"
    SubVerbBatch   = "BATCH"
)
```

### MCP Tool Integration

**7. MCP Tool for Agent Access** (`internal/tools/automate.go`):
```go
// AutomateProcessInput is the MCP tool input
type AutomateProcessInput struct {
    Type    string                 `json:"type" jsonschema:"enum=audit_process,enum=summarize,enum=prioritize"`
    Data    map[string]interface{} `json:"data" jsonschema:"description=Raw data to process"`
    Options AutomateOptions        `json:"options,omitempty"`
}

type AutomateOptions struct {
    Model string `json:"model,omitempty" jsonschema:"enum=haiku,enum=sonnet,description=Model to use (haiku recommended)"`
}

// AutomateProcessOutput is the MCP tool output
type AutomateProcessOutput struct {
    Success  bool        `json:"success"`
    Result   interface{} `json:"result,omitempty"`
    Error    string      `json:"error,omitempty"`
    Tokens   int         `json:"tokens_used"`
    CostUSD  float64     `json:"cost_usd"`
    Duration string      `json:"duration"`
}

func (h *Handlers) handleAutomateProcess(ctx context.Context, input AutomateProcessInput) (*mcp.CallToolResult, AutomateProcessOutput, error) {
    // Implementation
}
```

### Audit Integration

**8. Connect Audits to Processor**:

Update `internal/proxy/scripts/indicator.js` to route audit results through processor:
```javascript
async function runAuditWithProcessing(auditFn, auditType) {
    // Run raw audit in browser
    const rawResult = await auditFn();

    // Send to daemon for agent processing
    const processed = await fetch('/__devtool_api/automate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            type: 'audit_process',
            data: {
                auditType: auditType,
                rawData: rawResult,
                pageURL: window.location.href,
                pageTitle: document.title
            }
        })
    }).then(r => r.json());

    return processed.result;
}
```

### Cost Management

**9. Budget Tracking**:
```go
// BudgetTracker tracks automation costs
type BudgetTracker struct {
    dailyLimit  float64
    dailySpent  atomic.Value // float64
    lastReset   atomic.Value // time.Time
    mu          sync.Mutex
}

func (b *BudgetTracker) CanSpend(amount float64) bool
func (b *BudgetTracker) RecordSpend(amount float64)
func (b *BudgetTracker) DailySpent() float64
func (b *BudgetTracker) RemainingBudget() float64
```

### Configuration

**10. Config File Support** (`~/.config/agnt/automation.kdl`):
```kdl
automation {
    enabled true
    default-model "haiku"

    budget {
        daily-limit-usd 1.00
        per-task-limit-usd 0.05
    }

    tasks {
        audit_process {
            model "haiku"
            max-tokens 2000
            temperature 0.1
        }
        summarize {
            model "haiku"
            max-tokens 500
        }
    }
}
```

### Acceptance Criteria

- [ ] `Processor` type with `Process()` and `ProcessBatch()` methods
- [ ] Task types for audit processing, summarization, prioritization
- [ ] Optimized system prompts for each task type
- [ ] Worker pool for concurrent processing
- [ ] AUTOMATE verb in daemon protocol
- [ ] MCP tool exposure for agent access
- [ ] Budget tracking with daily limits
- [ ] KDL configuration support
- [ ] Integration with audit pipeline
- [ ] Unit tests with VCR recordings

### Testing Strategy

1. **Unit tests** with claude-go VCR recordings (no live API calls)
2. **Integration tests** with mock processor
3. **Cost tracking tests** verifying budget enforcement
4. **Benchmark tests** for processing latency

---

## Implementation Order

1. **Task 9** (automation layer) - Foundation for all agent processing
2. **Task 7** (auditAll) - Define unified schema first
3. **Task 1** (accessibility) - Highest user impact
4. **Task 4** (security) - Critical for production sites
5. **Task 6** (performance) - Core Web Vitals focus
6. **Task 5** (pageQuality/SEO) - Common use case
7. **Task 2** (DOM complexity) - Developer focused
8. **Task 3** (CSS) - Developer focused
9. **Task 8** (indicator UI) - Polish after audits improved

---

## Testing Requirements

For each audit:
1. Test on minimal HTML page (should find no issues)
2. Test on page with known issues (verify detection)
3. Test on complex production-like page (performance)
4. Verify selectors work with `document.querySelector()`
5. Verify actions are actionable (agent can execute fixes)

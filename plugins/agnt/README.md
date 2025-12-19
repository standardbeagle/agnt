# agnt Plugin

**Give your AI coding agent browser superpowers.**

MCP server plugin for Claude Code that bridges your AI agent and the browser, extending what's possible during vibe coding sessions.

## Features

- **Browser Superpowers** - Screenshots, DOM inspection, visual debugging
- **Floating Indicator** - Send messages from browser to agent
- **Sketch Mode** - Draw wireframes directly on your UI
- **Real-Time Errors** - Capture JS errors automatically
- **Process Management** - Run and manage dev servers
- **Token Efficiency** - Structured data uses fewer tokens than descriptions
- **50+ Diagnostic Functions** - Comprehensive frontend analysis tools

## Installation

### From Marketplace

```bash
# Add the marketplace
/plugin marketplace add standardbeagle/agnt

# Install the plugin
/plugin install agnt@agnt
```

### Manual Installation

```bash
# Clone and install from source
git clone https://github.com/standardbeagle/agnt.git
cd agnt
make install
```

Or install via npm:

```bash
npm install -g @standardbeagle/agnt
```

## Slash Commands

### Development Workflow

| Command | Description |
|---------|-------------|
| `/dev-proxy` | Start a dev server with reverse proxy for browser debugging |
| `/check-errors` | Check for JavaScript errors in the browser |
| `/screenshot` | Take a screenshot of the current browser page |
| `/sketch-mode` | Open sketch mode for wireframing on the browser page |
| `/browser-debug` | Debug browser issues using agnt diagnostic tools |
| `/schedule` | Schedule a message to be sent to an AI agent session |
| `/stop-all` | Stop all running processes and proxies |

### Quality Audits

| Command | Description |
|---------|-------------|
| `/audit-a11y` | Run comprehensive accessibility audit |
| `/audit-performance` | Analyze page performance and load times |
| `/audit-seo` | Audit page for SEO best practices |
| `/audit-security` | Audit page for security vulnerabilities |

### Analysis Tools

| Command | Description |
|---------|-------------|
| `/analyze-frontend` | Comprehensive frontend analysis (DOM, CSS, layout) |
| `/analyze-ux` | Analyze user experience and usability |
| `/qa-test` | Run comprehensive QA test suite |
| `/review-api` | Review API calls and network traffic |

## Skills

| Skill | Description |
|-------|-------------|
| `schedule` | Schedule messages to AI agent sessions with time delays |

## Subagents

| Agent | Description |
|-------|-------------|
| `browser-debugger` | Specialized agent for debugging browser issues |
| `process-manager` | Specialized agent for managing development processes |
| `ui-designer` | Specialized agent for UI design feedback and wireframing |

## MCP Tools

| Tool | Description |
|------|-------------|
| `detect` | Detect project type and available scripts |
| `run` | Run scripts or commands |
| `proc` | Manage processes: status, output, stop, list |
| `proxy` | Reverse proxy: start, stop, exec, toast |
| `proxylog` | Query proxy traffic logs |
| `currentpage` | View active page sessions |
| `session` | Manage sessions and schedule messages |
| `daemon` | Manage background daemon |

## Quick Start

1. Start a dev server with proxy:
   ```
   /dev-proxy
   ```

2. Open the proxy URL in your browser (shown in output)

3. Run a full QA test:
   ```
   /qa-test
   ```

4. Or run specific audits:
   ```
   /audit-a11y
   /audit-performance
   /audit-security
   ```

5. Schedule a follow-up message:
   ```
   /schedule claude-1 5m "Verify the tests passed and report any failures"
   ```

## Browser API

The proxy injects `window.__devtool` into all proxied pages with 50+ diagnostic functions:

### Logging & Screenshots
- `log(message, level, data)` - Send custom log
- `screenshot(name, selector?)` - Capture screenshot
- `toast.success/error/info/warning(msg)` - Show toast notification

### Element Inspection
- `inspect(selector)` - Comprehensive element inspection
- `getElementInfo(selector)` - Basic element info
- `getComputed(selector)` - Computed styles
- `getBox(selector)` - Box model (margin, border, padding)
- `getLayout(selector)` - Flexbox/Grid layout info
- `getStacking(selector)` - Z-index and stacking context

### Layout Diagnostics
- `findOverflows()` - Find scroll/hidden overflow elements
- `findStackingContexts()` - Find z-index layers
- `findOffscreen()` - Find elements outside viewport
- `diagnoseLayout(selector?)` - Comprehensive layout report

### Accessibility
- `auditAccessibility()` - Full accessibility audit
- `getA11yInfo(selector)` - ARIA and role information
- `getContrast(fg, bg)` - Color contrast ratio check
- `getTabOrder()` - Keyboard navigation order
- `getScreenReaderText(selector)` - Screen reader output

### Quality Audits
- `auditDOMComplexity()` - DOM size and depth analysis
- `auditCSS()` - CSS quality issues
- `auditSecurity()` - Security vulnerability check
- `auditPageQuality()` - SEO and page quality

### Interaction Tracking
- `interactions.getHistory()` - All user interactions
- `interactions.getLastClick()` - Last click details
- `interactions.getLastClickContext()` - Full click context

### Mutation Tracking
- `mutations.getHistory()` - DOM change history
- `mutations.highlightRecent(ms)` - Highlight recent changes
- `mutations.getAdded/Removed/Modified()` - Filtered mutations

### Visual Tools
- `highlight(selector)` - Highlight element
- `isVisible(selector)` - Check visibility
- `isInViewport(selector)` - Check if in viewport
- `checkOverlap(sel1, sel2)` - Check element overlap

### Sketch Mode
- `sketch.open()` / `sketch.close()` - Toggle sketch mode
- `sketch.save()` - Save and send sketch to agent
- `sketch.setTool(name)` - Select drawing tool

### Session Scheduling
- `session.list()` - List active agent sessions
- `session.get(code)` - Get session details
- `session.send(code, message)` - Send message immediately
- `session.schedule(code, duration, message)` - Schedule message delivery
- `session.tasks()` - List pending scheduled tasks
- `session.cancel(taskId)` - Cancel a scheduled task

### State Capture
- `captureDOM()` - Full DOM snapshot
- `captureStyles(selector)` - Element styles
- `captureState()` - Storage and cookies
- `captureNetwork()` - Network resources

## Keyboard Shortcuts

When running with `agnt run`:
- `Ctrl+P`: Toggle overlay menu

## Configuration

Example MCP configuration (`.mcp.json`):

```json
{
  "agnt": {
    "command": "agnt",
    "args": ["mcp"],
    "env": {}
  }
}
```

## License

MIT

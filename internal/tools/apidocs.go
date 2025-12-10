package tools

// APIFunction describes a single function in the __devtool API.
type APIFunction struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Signature   string   `json:"signature"`
	Parameters  []string `json:"parameters,omitempty"`
	Returns     string   `json:"returns"`
	Example     string   `json:"example"`
}

// APICategory describes a category of functions.
type APICategory struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DevToolAPIDocs contains the full API documentation.
var DevToolAPIDocs = struct {
	Categories []APICategory
	Functions  []APIFunction
}{
	Categories: []APICategory{
		{Name: "logging", Description: "Send custom log messages to the proxy server"},
		{Name: "screenshot", Description: "Capture screenshots of the page or elements"},
		{Name: "inspection", Description: "Get detailed information about DOM elements"},
		{Name: "tree", Description: "Walk and navigate the DOM tree"},
		{Name: "visual", Description: "Check visibility and viewport state"},
		{Name: "layout", Description: "Diagnose layout issues (overflow, stacking, offscreen)"},
		{Name: "overlay", Description: "Highlight elements visually on the page"},
		{Name: "interactive", Description: "Interactive element selection and measurement"},
		{Name: "capture", Description: "Capture page state, styles, and network info"},
		{Name: "accessibility", Description: "Accessibility auditing and information"},
		{Name: "audit", Description: "Page quality audits (DOM complexity, CSS, security)"},
		{Name: "interactions", Description: "Track and query user interactions (clicks, keyboard, scroll)"},
		{Name: "mutations", Description: "Track and query DOM mutations (added, removed, modified)"},
		{Name: "indicator", Description: "Control the floating indicator bug"},
		{Name: "sketch", Description: "Wireframing and annotation mode"},
		{Name: "connection", Description: "WebSocket connection status"},
	},
	Functions: []APIFunction{
		// Logging
		{
			Name:        "log",
			Category:    "logging",
			Description: "Send a custom log message to the proxy server",
			Signature:   "log(message, level?, data?)",
			Parameters:  []string{"message: string - The log message", "level: string - Log level: debug, info, warn, error (default: info)", "data: object - Optional additional data"},
			Returns:     "void",
			Example:     `__devtool.log("User clicked button", "info", {buttonId: "submit"})`,
		},
		{
			Name:        "debug",
			Category:    "logging",
			Description: "Send a debug-level log message",
			Signature:   "debug(message, data?)",
			Parameters:  []string{"message: string - The log message", "data: object - Optional additional data"},
			Returns:     "void",
			Example:     `__devtool.debug("Component rendered", {props: {id: 1}})`,
		},
		{
			Name:        "info",
			Category:    "logging",
			Description: "Send an info-level log message",
			Signature:   "info(message, data?)",
			Parameters:  []string{"message: string - The log message", "data: object - Optional additional data"},
			Returns:     "void",
			Example:     `__devtool.info("Page loaded successfully")`,
		},
		{
			Name:        "warn",
			Category:    "logging",
			Description: "Send a warning-level log message",
			Signature:   "warn(message, data?)",
			Parameters:  []string{"message: string - The log message", "data: object - Optional additional data"},
			Returns:     "void",
			Example:     `__devtool.warn("Deprecated API used", {api: "oldMethod"})`,
		},
		{
			Name:        "error",
			Category:    "logging",
			Description: "Send an error-level log message",
			Signature:   "error(message, data?)",
			Parameters:  []string{"message: string - The log message", "data: object - Optional additional data"},
			Returns:     "void",
			Example:     `__devtool.error("Failed to load data", {status: 500})`,
		},
		// Screenshot
		{
			Name:        "screenshot",
			Category:    "screenshot",
			Description: "Capture a screenshot of the page or a specific element",
			Signature:   "screenshot(name?, selector?)",
			Parameters:  []string{"name: string - Screenshot name (default: screenshot_<timestamp>)", "selector: string - CSS selector for element to capture (default: body)"},
			Returns:     "Promise<{name, width, height, selector}>",
			Example:     `await __devtool.screenshot("homepage")\nawait __devtool.screenshot("header", "#main-header")`,
		},
		// Inspection
		{
			Name:        "getElementInfo",
			Category:    "inspection",
			Description: "Get comprehensive information about an element",
			Signature:   "getElementInfo(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{tag, id, classes, attributes, text, html, dimensions, position}",
			Example:     `__devtool.getElementInfo("#submit-btn")`,
		},
		{
			Name:        "getPosition",
			Category:    "inspection",
			Description: "Get element position (bounding rect)",
			Signature:   "getPosition(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{top, right, bottom, left, width, height, x, y}",
			Example:     `__devtool.getPosition(".modal")`,
		},
		{
			Name:        "getComputed",
			Category:    "inspection",
			Description: "Get computed CSS styles for an element",
			Signature:   "getComputed(selector, properties?)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element", "properties: string[] - Specific properties to get (default: common properties)"},
			Returns:     "{property: value, ...}",
			Example:     `__devtool.getComputed("#header", ["display", "position", "z-index"])`,
		},
		{
			Name:        "getBox",
			Category:    "inspection",
			Description: "Get box model dimensions (content, padding, border, margin)",
			Signature:   "getBox(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{content, padding, border, margin} with {top, right, bottom, left}",
			Example:     `__devtool.getBox(".container")`,
		},
		{
			Name:        "getLayout",
			Category:    "inspection",
			Description: "Get layout information (display, position, flexbox/grid)",
			Signature:   "getLayout(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{display, position, float, flexbox?, grid?}",
			Example:     `__devtool.getLayout(".flex-container")`,
		},
		{
			Name:        "getContainer",
			Category:    "inspection",
			Description: "Get containing block and scroll container",
			Signature:   "getContainer(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{containingBlock, scrollContainer, offsetParent}",
			Example:     `__devtool.getContainer(".absolute-element")`,
		},
		{
			Name:        "getStacking",
			Category:    "inspection",
			Description: "Get stacking context information (z-index, creates context)",
			Signature:   "getStacking(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{zIndex, createsContext, reason?, parentContext}",
			Example:     `__devtool.getStacking(".modal-overlay")`,
		},
		{
			Name:        "getTransform",
			Category:    "inspection",
			Description: "Get CSS transform information",
			Signature:   "getTransform(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{transform, transformOrigin, matrix}",
			Example:     `__devtool.getTransform(".rotated-element")`,
		},
		{
			Name:        "getOverflow",
			Category:    "inspection",
			Description: "Get overflow and scroll information",
			Signature:   "getOverflow(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{overflow, overflowX, overflowY, scrollWidth, scrollHeight, isScrollable}",
			Example:     `__devtool.getOverflow(".scrollable-panel")`,
		},
		// Tree Walking
		{
			Name:        "walkChildren",
			Category:    "tree",
			Description: "Get all child elements with optional filtering",
			Signature:   "walkChildren(selector, options?)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element", "options: {maxDepth?, filter?} - Walk options"},
			Returns:     "[{element, depth, path}, ...]",
			Example:     `__devtool.walkChildren("#container", {maxDepth: 2})`,
		},
		{
			Name:        "walkParents",
			Category:    "tree",
			Description: "Get all parent elements up to document",
			Signature:   "walkParents(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "[{element, depth}, ...]",
			Example:     `__devtool.walkParents(".nested-element")`,
		},
		{
			Name:        "findAncestor",
			Category:    "tree",
			Description: "Find the closest ancestor matching a condition",
			Signature:   "findAncestor(selector, condition)",
			Parameters:  []string{"selector: string|Element - Starting element", "condition: string|function - CSS selector or predicate function"},
			Returns:     "Element|null",
			Example:     `__devtool.findAncestor(".button", "[data-modal]")`,
		},
		// Visual State
		{
			Name:        "isVisible",
			Category:    "visual",
			Description: "Check if an element is visible (not hidden by CSS)",
			Signature:   "isVisible(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{visible: boolean, reasons?: string[]}",
			Example:     `__devtool.isVisible(".dropdown-menu")`,
		},
		{
			Name:        "isInViewport",
			Category:    "visual",
			Description: "Check if an element is within the viewport",
			Signature:   "isInViewport(selector, threshold?)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element", "threshold: number - Percentage visible required (default: 0)"},
			Returns:     "{inViewport: boolean, percentVisible: number, position}",
			Example:     `__devtool.isInViewport("#footer", 0.5)`,
		},
		{
			Name:        "checkOverlap",
			Category:    "visual",
			Description: "Check if two elements overlap",
			Signature:   "checkOverlap(selector1, selector2)",
			Parameters:  []string{"selector1: string|Element - First element", "selector2: string|Element - Second element"},
			Returns:     "{overlaps: boolean, intersection?: {x, y, width, height}}",
			Example:     `__devtool.checkOverlap(".modal", ".tooltip")`,
		},
		// Layout Diagnostics
		{
			Name:        "findOverflows",
			Category:    "layout",
			Description: "Find elements causing horizontal overflow",
			Signature:   "findOverflows()",
			Parameters:  []string{},
			Returns:     "[{element, overflow, width, parentWidth}, ...]",
			Example:     `__devtool.findOverflows()`,
		},
		{
			Name:        "findStackingContexts",
			Category:    "layout",
			Description: "Find all stacking contexts in the document",
			Signature:   "findStackingContexts()",
			Parameters:  []string{},
			Returns:     "[{element, zIndex, reason}, ...]",
			Example:     `__devtool.findStackingContexts()`,
		},
		{
			Name:        "findOffscreen",
			Category:    "layout",
			Description: "Find elements positioned outside the viewport",
			Signature:   "findOffscreen()",
			Parameters:  []string{},
			Returns:     "[{element, position, distance}, ...]",
			Example:     `__devtool.findOffscreen()`,
		},
		// Visual Overlays
		{
			Name:        "highlight",
			Category:    "overlay",
			Description: "Highlight an element with a colored overlay",
			Signature:   "highlight(selector, options?)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element", "options: {color?, label?, duration?} - Highlight options"},
			Returns:     "string - Overlay ID for removal",
			Example:     `__devtool.highlight("#form", {color: "blue", label: "Main Form"})`,
		},
		{
			Name:        "removeHighlight",
			Category:    "overlay",
			Description: "Remove a specific highlight overlay",
			Signature:   "removeHighlight(id)",
			Parameters:  []string{"id: string - Overlay ID returned by highlight()"},
			Returns:     "void",
			Example:     `__devtool.removeHighlight("overlay-123")`,
		},
		{
			Name:        "clearAllOverlays",
			Category:    "overlay",
			Description: "Remove all highlight overlays",
			Signature:   "clearAllOverlays()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.clearAllOverlays()`,
		},
		// Interactive
		{
			Name:        "selectElement",
			Category:    "interactive",
			Description: "Enter interactive mode to select an element by clicking",
			Signature:   "selectElement()",
			Parameters:  []string{},
			Returns:     "Promise<Element> - The selected element",
			Example:     `const el = await __devtool.selectElement()`,
		},
		{
			Name:        "waitForElement",
			Category:    "interactive",
			Description: "Wait for an element to appear in the DOM",
			Signature:   "waitForElement(selector, timeout?)",
			Parameters:  []string{"selector: string - CSS selector", "timeout: number - Max wait time in ms (default: 5000)"},
			Returns:     "Promise<Element>",
			Example:     `const modal = await __devtool.waitForElement(".modal-open")`,
		},
		{
			Name:        "ask",
			Category:    "interactive",
			Description: "Display a prompt dialog and wait for user input",
			Signature:   "ask(question, options?)",
			Parameters:  []string{"question: string - Question to display", "options: {choices?, default?} - Dialog options"},
			Returns:     "Promise<string> - User's answer",
			Example:     `const answer = await __devtool.ask("What color?", {choices: ["red", "blue"]})`,
		},
		{
			Name:        "measureBetween",
			Category:    "interactive",
			Description: "Measure distance between two elements",
			Signature:   "measureBetween(selector1, selector2)",
			Parameters:  []string{"selector1: string|Element - First element", "selector2: string|Element - Second element"},
			Returns:     "{horizontal, vertical, diagonal}",
			Example:     `__devtool.measureBetween(".header", ".footer")`,
		},
		// State Capture
		{
			Name:        "captureDOM",
			Category:    "capture",
			Description: "Capture serialized DOM snapshot",
			Signature:   "captureDOM(selector?)",
			Parameters:  []string{"selector: string - Root element selector (default: body)"},
			Returns:     "{html, text, elementCount, timestamp}",
			Example:     `__devtool.captureDOM("#main-content")`,
		},
		{
			Name:        "captureStyles",
			Category:    "capture",
			Description: "Capture all stylesheets and computed styles",
			Signature:   "captureStyles()",
			Parameters:  []string{},
			Returns:     "{stylesheets: [...], inlineStyles: [...]}",
			Example:     `__devtool.captureStyles()`,
		},
		{
			Name:        "captureState",
			Category:    "capture",
			Description: "Capture comprehensive page state",
			Signature:   "captureState()",
			Parameters:  []string{},
			Returns:     "{url, title, viewport, scroll, forms, localStorage, sessionStorage}",
			Example:     `__devtool.captureState()`,
		},
		{
			Name:        "captureNetwork",
			Category:    "capture",
			Description: "Get performance timing and resource information",
			Signature:   "captureNetwork()",
			Parameters:  []string{},
			Returns:     "{timing, resources: [...], paintTiming}",
			Example:     `__devtool.captureNetwork()`,
		},
		// Accessibility
		{
			Name:        "getA11yInfo",
			Category:    "accessibility",
			Description: "Get accessibility information for an element",
			Signature:   "getA11yInfo(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{role, name, description, state, properties}",
			Example:     `__devtool.getA11yInfo("#submit-button")`,
		},
		{
			Name:        "getContrast",
			Category:    "accessibility",
			Description: "Calculate color contrast ratio for text",
			Signature:   "getContrast(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{ratio, foreground, background, passesAA, passesAAA}",
			Example:     `__devtool.getContrast(".body-text")`,
		},
		{
			Name:        "getTabOrder",
			Category:    "accessibility",
			Description: "Get focusable elements in tab order",
			Signature:   "getTabOrder()",
			Parameters:  []string{},
			Returns:     "[{element, tabIndex, natural}, ...]",
			Example:     `__devtool.getTabOrder()`,
		},
		{
			Name:        "getScreenReaderText",
			Category:    "accessibility",
			Description: "Get text as a screen reader would announce it",
			Signature:   "getScreenReaderText(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "string",
			Example:     `__devtool.getScreenReaderText("#nav-menu")`,
		},
		{
			Name:        "auditAccessibility",
			Category:    "accessibility",
			Description: "Run accessibility audit on the page",
			Signature:   "auditAccessibility()",
			Parameters:  []string{},
			Returns:     "{issues: [...], summary: {critical, serious, moderate, minor}}",
			Example:     `__devtool.auditAccessibility()`,
		},
		// Quality Audits
		{
			Name:        "auditDOMComplexity",
			Category:    "audit",
			Description: "Analyze DOM complexity and depth",
			Signature:   "auditDOMComplexity()",
			Parameters:  []string{},
			Returns:     "{elementCount, maxDepth, avgDepth, deepElements, widest}",
			Example:     `__devtool.auditDOMComplexity()`,
		},
		{
			Name:        "auditCSS",
			Category:    "audit",
			Description: "Analyze CSS usage and potential issues",
			Signature:   "auditCSS()",
			Parameters:  []string{},
			Returns:     "{unusedRules, duplicates, specificity, importantCount}",
			Example:     `__devtool.auditCSS()`,
		},
		{
			Name:        "auditSecurity",
			Category:    "audit",
			Description: "Check for common security issues",
			Signature:   "auditSecurity()",
			Parameters:  []string{},
			Returns:     "{issues: [...], summary}",
			Example:     `__devtool.auditSecurity()`,
		},
		{
			Name:        "auditPageQuality",
			Category:    "audit",
			Description: "Comprehensive page quality audit",
			Signature:   "auditPageQuality()",
			Parameters:  []string{},
			Returns:     "{dom, css, accessibility, security, performance}",
			Example:     `__devtool.auditPageQuality()`,
		},
		// Interactions
		{
			Name:        "interactions.getHistory",
			Category:    "interactions",
			Description: "Get recent interaction history",
			Signature:   "interactions.getHistory(count?)",
			Parameters:  []string{"count: number - Number of interactions to return (default: 50)"},
			Returns:     "[{event_type, target, position?, timestamp}, ...]",
			Example:     `__devtool.interactions.getHistory(10)`,
		},
		{
			Name:        "interactions.getLastClick",
			Category:    "interactions",
			Description: "Get the most recent click event",
			Signature:   "interactions.getLastClick()",
			Parameters:  []string{},
			Returns:     "{event_type, target, position, timestamp}|null",
			Example:     `__devtool.interactions.getLastClick()`,
		},
		{
			Name:        "interactions.getClicksOn",
			Category:    "interactions",
			Description: "Get all clicks on elements matching selector",
			Signature:   "interactions.getClicksOn(selector)",
			Parameters:  []string{"selector: string - Selector pattern to match in target"},
			Returns:     "[{event_type, target, position, timestamp}, ...]",
			Example:     `__devtool.interactions.getClicksOn("button")`,
		},
		{
			Name:        "interactions.getMouseTrail",
			Category:    "interactions",
			Description: "Get mouse movement samples around a timestamp",
			Signature:   "interactions.getMouseTrail(timestamp, windowMs?)",
			Parameters:  []string{"timestamp: number - Center timestamp", "windowMs: number - Time window in ms (default: 5000)"},
			Returns:     "[{position, wall_time, interaction_time}, ...]",
			Example:     `__devtool.interactions.getMouseTrail(Date.now() - 1000)`,
		},
		{
			Name:        "interactions.getLastClickContext",
			Category:    "interactions",
			Description: "Get last click with surrounding mouse trail",
			Signature:   "interactions.getLastClickContext(trailMs?)",
			Parameters:  []string{"trailMs: number - Trail window in ms (default: 2000)"},
			Returns:     "{click, mouseTrail}|null",
			Example:     `__devtool.interactions.getLastClickContext()`,
		},
		{
			Name:        "interactions.clear",
			Category:    "interactions",
			Description: "Clear interaction history",
			Signature:   "interactions.clear()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.interactions.clear()`,
		},
		// Mutations
		{
			Name:        "mutations.getHistory",
			Category:    "mutations",
			Description: "Get recent mutation history",
			Signature:   "mutations.getHistory(count?)",
			Parameters:  []string{"count: number - Number of mutations to return (default: 50)"},
			Returns:     "[{mutation_type, target, added?, removed?, attribute?, timestamp}, ...]",
			Example:     `__devtool.mutations.getHistory(20)`,
		},
		{
			Name:        "mutations.getAdded",
			Category:    "mutations",
			Description: "Get elements added to DOM since timestamp",
			Signature:   "mutations.getAdded(since?)",
			Parameters:  []string{"since: number - Timestamp filter (default: 0)"},
			Returns:     "[{mutation_type: 'added', target, added, timestamp}, ...]",
			Example:     `__devtool.mutations.getAdded(Date.now() - 5000)`,
		},
		{
			Name:        "mutations.getRemoved",
			Category:    "mutations",
			Description: "Get elements removed from DOM since timestamp",
			Signature:   "mutations.getRemoved(since?)",
			Parameters:  []string{"since: number - Timestamp filter (default: 0)"},
			Returns:     "[{mutation_type: 'removed', target, removed, timestamp}, ...]",
			Example:     `__devtool.mutations.getRemoved(Date.now() - 5000)`,
		},
		{
			Name:        "mutations.getModified",
			Category:    "mutations",
			Description: "Get attribute changes since timestamp",
			Signature:   "mutations.getModified(since?)",
			Parameters:  []string{"since: number - Timestamp filter (default: 0)"},
			Returns:     "[{mutation_type: 'attributes', target, attribute, timestamp}, ...]",
			Example:     `__devtool.mutations.getModified(Date.now() - 5000)`,
		},
		{
			Name:        "mutations.highlightRecent",
			Category:    "mutations",
			Description: "Visually highlight recently added elements",
			Signature:   "mutations.highlightRecent(duration?)",
			Parameters:  []string{"duration: number - How far back to look in ms (default: 5000)"},
			Returns:     "void",
			Example:     `__devtool.mutations.highlightRecent(3000)`,
		},
		{
			Name:        "mutations.clear",
			Category:    "mutations",
			Description: "Clear mutation history",
			Signature:   "mutations.clear()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.mutations.clear()`,
		},
		{
			Name:        "mutations.pause",
			Category:    "mutations",
			Description: "Pause mutation tracking",
			Signature:   "mutations.pause()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.mutations.pause()`,
		},
		{
			Name:        "mutations.resume",
			Category:    "mutations",
			Description: "Resume mutation tracking",
			Signature:   "mutations.resume()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.mutations.resume()`,
		},
		// Indicator
		{
			Name:        "indicator.show",
			Category:    "indicator",
			Description: "Show the floating indicator",
			Signature:   "indicator.show()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.indicator.show()`,
		},
		{
			Name:        "indicator.hide",
			Category:    "indicator",
			Description: "Hide the floating indicator",
			Signature:   "indicator.hide()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.indicator.hide()`,
		},
		{
			Name:        "indicator.toggle",
			Category:    "indicator",
			Description: "Toggle the floating indicator visibility",
			Signature:   "indicator.toggle()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.indicator.toggle()`,
		},
		{
			Name:        "indicator.togglePanel",
			Category:    "indicator",
			Description: "Toggle the indicator's expanded panel",
			Signature:   "indicator.togglePanel()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.indicator.togglePanel()`,
		},
		{
			Name:        "indicator.destroy",
			Category:    "indicator",
			Description: "Remove the floating indicator completely",
			Signature:   "indicator.destroy()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.indicator.destroy()`,
		},
		// Sketch Mode
		{
			Name:        "sketch.open",
			Category:    "sketch",
			Description: "Open sketch mode for wireframing",
			Signature:   "sketch.open()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.sketch.open()`,
		},
		{
			Name:        "sketch.close",
			Category:    "sketch",
			Description: "Close sketch mode",
			Signature:   "sketch.close()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.sketch.close()`,
		},
		{
			Name:        "sketch.toggle",
			Category:    "sketch",
			Description: "Toggle sketch mode on/off",
			Signature:   "sketch.toggle()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.sketch.toggle()`,
		},
		{
			Name:        "sketch.setTool",
			Category:    "sketch",
			Description: "Set the active drawing tool",
			Signature:   "sketch.setTool(tool)",
			Parameters:  []string{"tool: string - Tool name: select, rectangle, ellipse, line, arrow, freedraw, text, note, button, input, image, eraser"},
			Returns:     "void",
			Example:     `__devtool.sketch.setTool("rectangle")`,
		},
		{
			Name:        "sketch.save",
			Category:    "sketch",
			Description: "Save sketch and send to proxy server",
			Signature:   "sketch.save()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.sketch.save()`,
		},
		{
			Name:        "sketch.toJSON",
			Category:    "sketch",
			Description: "Export sketch data as JSON",
			Signature:   "sketch.toJSON()",
			Parameters:  []string{},
			Returns:     "object - Serialized sketch data",
			Example:     `const data = __devtool.sketch.toJSON()`,
		},
		{
			Name:        "sketch.fromJSON",
			Category:    "sketch",
			Description: "Load sketch data from JSON",
			Signature:   "sketch.fromJSON(data)",
			Parameters:  []string{"data: object - Sketch data from toJSON()"},
			Returns:     "void",
			Example:     `__devtool.sketch.fromJSON(savedData)`,
		},
		{
			Name:        "sketch.toDataURL",
			Category:    "sketch",
			Description: "Export sketch as PNG data URL",
			Signature:   "sketch.toDataURL()",
			Parameters:  []string{},
			Returns:     "string - PNG data URL",
			Example:     `const png = __devtool.sketch.toDataURL()`,
		},
		{
			Name:        "sketch.undo",
			Category:    "sketch",
			Description: "Undo last sketch action",
			Signature:   "sketch.undo()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.sketch.undo()`,
		},
		{
			Name:        "sketch.redo",
			Category:    "sketch",
			Description: "Redo previously undone action",
			Signature:   "sketch.redo()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.sketch.redo()`,
		},
		{
			Name:        "sketch.clear",
			Category:    "sketch",
			Description: "Clear all sketch elements",
			Signature:   "sketch.clear()",
			Parameters:  []string{},
			Returns:     "void",
			Example:     `__devtool.sketch.clear()`,
		},
		// Connection
		{
			Name:        "isConnected",
			Category:    "connection",
			Description: "Check if WebSocket is connected to proxy server",
			Signature:   "isConnected()",
			Parameters:  []string{},
			Returns:     "boolean",
			Example:     `if (__devtool.isConnected()) { ... }`,
		},
		{
			Name:        "getStatus",
			Category:    "connection",
			Description: "Get detailed WebSocket connection status",
			Signature:   "getStatus()",
			Parameters:  []string{},
			Returns:     "string - connecting, connected, closing, closed, not_initialized, unknown",
			Example:     `console.log(__devtool.getStatus())`,
		},
		// Composite
		{
			Name:        "inspect",
			Category:    "inspection",
			Description: "Get comprehensive inspection of an element (combines multiple inspection calls)",
			Signature:   "inspect(selector)",
			Parameters:  []string{"selector: string|Element - CSS selector or DOM element"},
			Returns:     "{info, position, box, layout, stacking, container, visibility, viewport}",
			Example:     `__devtool.inspect("#main-form")`,
		},
		{
			Name:        "diagnoseLayout",
			Category:    "layout",
			Description: "Run comprehensive layout diagnostics",
			Signature:   "diagnoseLayout(selector?)",
			Parameters:  []string{"selector: string - Optional element to focus analysis on"},
			Returns:     "{overflows, stackingContexts, offscreen, element?}",
			Example:     `__devtool.diagnoseLayout()`,
		},
	},
}

// GetAPIOverview returns a high-level overview of all API categories and functions.
func GetAPIOverview() string {
	overview := "# __devtool API Reference\n\n"
	overview += "The proxy injects a `window.__devtool` object with diagnostic functions.\n\n"

	overview += "## Categories\n\n"
	for _, cat := range DevToolAPIDocs.Categories {
		overview += "- **" + cat.Name + "**: " + cat.Description + "\n"
	}

	overview += "\n## Quick Reference\n\n"
	currentCategory := ""
	for _, fn := range DevToolAPIDocs.Functions {
		if fn.Category != currentCategory {
			currentCategory = fn.Category
			overview += "\n### " + currentCategory + "\n"
		}
		overview += "- `" + fn.Signature + "` - " + fn.Description + "\n"
	}

	overview += "\n## Common Examples\n\n"
	overview += "```javascript\n"
	overview += "// Take a screenshot\n"
	overview += "await __devtool.screenshot(\"homepage\")\n\n"
	overview += "// Log a message\n"
	overview += "__devtool.log(\"User clicked\", \"info\", {target: \"button\"})\n\n"
	overview += "// Get last click with mouse trail\n"
	overview += "__devtool.interactions.getLastClickContext()\n\n"
	overview += "// Highlight recent DOM changes\n"
	overview += "__devtool.mutations.highlightRecent(5000)\n\n"
	overview += "// Inspect an element\n"
	overview += "__devtool.inspect(\"#submit-btn\")\n\n"
	overview += "// Run accessibility audit\n"
	overview += "__devtool.auditAccessibility()\n"
	overview += "```\n\n"

	overview += "Use `proxy {action: \"exec\", id: \"...\", describe: \"functionName\"}` for detailed function docs.\n"

	return overview
}

// GetFunctionDescription returns detailed documentation for a specific function.
func GetFunctionDescription(name string) (string, bool) {
	for _, fn := range DevToolAPIDocs.Functions {
		if fn.Name == name {
			doc := "# " + fn.Name + "\n\n"
			doc += fn.Description + "\n\n"
			doc += "**Category:** " + fn.Category + "\n\n"
			doc += "**Signature:**\n```javascript\n" + fn.Signature + "\n```\n\n"

			if len(fn.Parameters) > 0 {
				doc += "**Parameters:**\n"
				for _, p := range fn.Parameters {
					doc += "- " + p + "\n"
				}
				doc += "\n"
			}

			doc += "**Returns:** " + fn.Returns + "\n\n"
			doc += "**Example:**\n```javascript\n" + fn.Example + "\n```\n"

			return doc, true
		}
	}
	return "", false
}

// ListFunctionNames returns all function names for auto-completion.
func ListFunctionNames() []string {
	names := make([]string, len(DevToolAPIDocs.Functions))
	for i, fn := range DevToolAPIDocs.Functions {
		names[i] = fn.Name
	}
	return names
}

// Code generated from legacy apidocs.go as a one-time seed. Thereafter
// this file is edited by hand: each JSDoc block is the authoritative
// source of truth for one __devtool.* public function. Run
// `make generate` after any change here to refresh
// internal/tools/apidocs_gen.go.
//
// This file intentionally contains no executable statements — only JSDoc
// annotations plus no-op declarations — so it is safe to embed via
// embed.go for parsing at build time without affecting runtime behavior.
// It is NOT loaded into the page.

(function() {
  'use strict';
  // noop — this file exists only for JSDoc annotations read by scripts/gen-apidocs.go.

  /**
   * Send a custom log message to the proxy server
   *
   * @devtool log
   * @category logging
   * @signature log(message, level?, data?)
   * @param {string} message - The log message
   * @param {string} [level=info] - Log level: debug, info, warn, error
   * @param {object} [data] - Optional additional data
   * @returns {sent: boolean} - sent:false means the message was dropped (socket not connected)
   * @example
   * __devtool.log("User clicked button", "info", {buttonId: "submit"})
   */
  var __jsdoc_log = null;

  /**
   * Send a debug-level log message
   *
   * @devtool debug
   * @category logging
   * @signature debug(message, data?)
   * @param {string} message - The log message
   * @param {object} [data] - Optional additional data
   * @returns {sent: boolean}
   * @example
   * __devtool.debug("Component rendered", {props: {id: 1}})
   */
  var __jsdoc_debug = null;

  /**
   * Send an info-level log message
   *
   * @devtool info
   * @category logging
   * @signature info(message, data?)
   * @param {string} message - The log message
   * @param {object} [data] - Optional additional data
   * @returns {sent: boolean}
   * @example
   * __devtool.info("Page loaded successfully")
   */
  var __jsdoc_info = null;

  /**
   * Send a warning-level log message
   *
   * @devtool warn
   * @category logging
   * @signature warn(message, data?)
   * @param {string} message - The log message
   * @param {object} [data] - Optional additional data
   * @returns {sent: boolean}
   * @example
   * __devtool.warn("Deprecated API used", {api: "oldMethod"})
   */
  var __jsdoc_warn = null;

  /**
   * Send an error-level log message
   *
   * @devtool error
   * @category logging
   * @signature error(message, data?)
   * @param {string} message - The log message
   * @param {object} [data] - Optional additional data
   * @returns {sent: boolean}
   * @example
   * __devtool.error("Failed to load data", {status: 500})
   */
  var __jsdoc_error = null;

  /**
   * Capture a screenshot of the page or a specific element
   *
   * @devtool screenshot
   * @category screenshot
   * @signature screenshot(nameOrOptions?, selector?)
   * @param {string|object} [nameOrOptions] - Screenshot name, a CSS selector (".x"/"#x"/"[x]"), or an options object
   * @param {string} [selector=body] - CSS selector for element to capture
   * @param {string} [nameOrOptions.name=screenshot_<timestamp>] - Screenshot name
   * @param {string} [nameOrOptions.selector] - CSS selector for element to capture
   * @param {object} [nameOrOptions.region] - Pixel region {x, y, width, height}
   * @param {boolean} [nameOrOptions.fullPage=false] - Capture full page height
   * @param {boolean} [nameOrOptions.overview=true] - For long pages, capture scaled overview + detail sections
   * @param {number} [nameOrOptions.maxHeight=2000] - Page height (px) that triggers overview mode
   * @param {number} [nameOrOptions.overviewScale=0.25] - Scale factor for the overview image
   * @param {number} [nameOrOptions.detailHeight=600] - Height of top/bottom detail sections
   * @param {string} [nameOrOptions.format=png] - "png" or "jpeg"
   * @param {number} [nameOrOptions.quality=0.92] - JPEG quality 0-1
   * @returns Promise<{name, width, height, selector, fullPage?, mode?, pageHeight?, overview?, top?, viewport?, bottom?, message?}>
   * @example
   * await __devtool.screenshot("homepage")\nawait __devtool.screenshot({selector: ".card", name: "card"})\nawait __devtool.screenshot({region: {x: 0, y: 0, width: 800, height: 600}})
   */
  var __jsdoc_screenshot = null;

  /**
   * Get comprehensive information about an element
   *
   * @devtool getElementInfo
   * @category inspection
   * @signature getElementInfo(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {selector, tag, id, classes[], attributes{}} - JSON-safe; no live DOM node, the selector is the element handle
   * @example
   * __devtool.getElementInfo("#submit-btn")
   */
  var __jsdoc_getElementInfo = null;

  /**
   * Get element position (bounding rect)
   *
   * @devtool getPosition
   * @category inspection
   * @signature getPosition(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {rect:{x,y,width,height,top,right,bottom,left}, viewport:{x,y}, document:{x,y}, scroll:{x,y}}
   * @example
   * __devtool.getPosition(".modal")
   */
  var __jsdoc_getPosition = null;

  /**
   * Get computed CSS styles for an element
   *
   * @devtool getComputed
   * @category inspection
   * @signature getComputed(selector, properties?)
   * @param {string|Element} selector - CSS selector or DOM element
   * @param {string[]} [properties=common properties] - Specific properties to get
   * @returns {property: value, ...}
   * @example
   * __devtool.getComputed("#header", ["display", "position", "z-index"])
   */
  var __jsdoc_getComputed = null;

  /**
   * Get box model dimensions (content, padding, border, margin)
   *
   * @devtool getBox
   * @category inspection
   * @signature getBox(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {content, padding, border, margin} with {top, right, bottom, left}
   * @example
   * __devtool.getBox(".container")
   */
  var __jsdoc_getBox = null;

  /**
   * Get layout information (display, position, flexbox/grid)
   *
   * @devtool getLayout
   * @category inspection
   * @signature getLayout(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {display, position, float, flexbox?, grid?}
   * @example
   * __devtool.getLayout(".flex-container")
   */
  var __jsdoc_getLayout = null;

  /**
   * Resolve the containing block for a positioned element and surface the
   * ancestor property that traps a position:fixed element (transform,
   * filter, will-change, contain) — the distant cause behind "my fixed
   * element scrolls away / is mispositioned" that is invisible in source.
   *
   * @devtool getContainer
   * @category inspection
   * @signature getContainer(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {type, name, contain, position, expectedContainingBlock, actualContainingBlock, trappedBy:{selector,property,value}|null, escaped}
   * @example
   * __devtool.getContainer(".fixed-header")
   */
  var __jsdoc_getContainer = null;

  /**
   * Get stacking context information for an element: whether it creates its
   * own context (and via which CSS property), the nearest ancestor stacking
   * root that its z-index actually resolves against, the property that made
   * that root a context (rootTrigger), and the full ancestor chain. Use this
   * before changing z-index — the "z-index does nothing" bug is almost always
   * a cross-stacking-context comparison, fixed by removing/moving rootTrigger,
   * not by a bigger number.
   *
   * @devtool getStacking
   * @category inspection
   * @signature getStacking(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {zIndex, position, createsContext, selfTriggers:[{property,value}], stackingRoot, rootTrigger:{property,value}|null, chain:[{selector,triggers}], opacity, transform, filter}
   * @example
   * __devtool.getStacking(".modal-overlay")
   */
  var __jsdoc_getStacking = null;

  /**
   * Get CSS transform information
   *
   * @devtool getTransform
   * @category inspection
   * @signature getTransform(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {matrix, transform, transformOrigin, translate:{x,y,z?}, rotate, scale:{x,y}, is2D?} - translate/rotate/scale decomposed via DOMMatrix (null if unparseable); identity values when transform is none
   * @example
   * __devtool.getTransform(".rotated-element")
   */
  var __jsdoc_getTransform = null;

  /**
   * Get overflow and scroll information
   *
   * @devtool getOverflow
   * @category inspection
   * @signature getOverflow(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {x, y, scrollWidth, scrollHeight, clientWidth, clientHeight, scrollTop, scrollLeft, hasOverflow} - x/y are the computed overflow-x/overflow-y values
   * @example
   * __devtool.getOverflow(".scrollable-panel")
   */
  var __jsdoc_getOverflow = null;

  /**
   * Get all child elements with optional filtering
   *
   * @devtool walkChildren
   * @category tree
   * @signature walkChildren(selector, depthOrOptions?, filter?)
   * @param {string|Element} selector - CSS selector or DOM element
   * @param {number|{maxDepth?, filter?}} [depthOrOptions=1] - Max depth as a number (positional form) or an options object {maxDepth, filter}
   * @param {function} [filter] - Predicate (positional form): include child when filter(el) is truthy
   * @returns {elements:[{selector, tag, depth}], count} - JSON-safe; no live DOM nodes
   * @example
   * __devtool.walkChildren("#container", {maxDepth: 2})\n__devtool.walkChildren("#container", 2, function(el) { return el.tagName === "LI"; })
   */
  var __jsdoc_walkChildren = null;

  /**
   * Get all parent elements up to document
   *
   * @devtool walkParents
   * @category tree
   * @signature walkParents(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {parents:[{selector, tag}], count} - ordered nearest-first up to <html>; JSON-safe, no live DOM nodes
   * @example
   * __devtool.walkParents(".nested-element")
   */
  var __jsdoc_walkParents = null;

  /**
   * Find the closest ancestor matching a condition
   *
   * @devtool findAncestor
   * @category tree
   * @signature findAncestor(selector, condition)
   * @param {string|Element} selector - Starting element
   * @param {string|function} condition - CSS selector (matched via el.matches) or predicate function
   * @returns {found: boolean, selector?, tag?} - found:false when no ancestor matches
   * @example
   * __devtool.findAncestor(".button", "[data-modal]")
   */
  var __jsdoc_findAncestor = null;

  /**
   * Check if an element is visible (not hidden by CSS)
   *
   * @devtool isVisible
   * @category visual
   * @signature isVisible(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {visible: boolean, reason?: string, area?: number} - reason (singular) names the first hiding cause: display none, visibility hidden, opacity 0, zero size, no bounding rect
   * @example
   * __devtool.isVisible(".dropdown-menu")
   */
  var __jsdoc_isVisible = null;

  /**
   * Check if an element is within the viewport
   *
   * @devtool isInViewport
   * @category visual
   * @signature isInViewport(selector, threshold?)
   * @param {string|Element} selector - CSS selector or DOM element
   * @param {number} [threshold=0] - Minimum visible-area ratio (0..1) required; 0 means any intersection counts
   * @returns {inViewport: boolean, intersecting: boolean, ratio: number, percentVisible: number, rect:{x,y,width,height,top,right,bottom,left}, fullyVisible: boolean}
   * @example
   * __devtool.isInViewport("#footer", 0.5)
   */
  var __jsdoc_isInViewport = null;

  /**
   * Check if two elements overlap
   *
   * @devtool checkOverlap
   * @category visual
   * @signature checkOverlap(selector1, selector2)
   * @param {string|Element} selector1 - First element
   * @param {string|Element} selector2 - Second element
   * @returns {overlaps: boolean, area?: number, rect?: {left, right, top, bottom}} - area/rect describe the intersection when overlaps is true
   * @example
   * __devtool.checkOverlap(".modal", ".tooltip")
   */
  var __jsdoc_checkOverlap = null;

  /**
   * Find elements causing horizontal overflow
   *
   * @devtool findOverflows
   * @category layout
   * @signature findOverflows()
   * @returns {overflows:[{selector, type, scrollWidth, scrollHeight, clientWidth, clientHeight}], count, total, scanned, capped} - bounded scan (4000 elements, 100 results); capped:true when truncated, total is all matches seen
   * @example
   * __devtool.findOverflows()
   */
  var __jsdoc_findOverflows = null;

  /**
   * Find every stacking context in the document, each with the exact CSS
   * trigger(s) that created it. Detects the full spec trigger set —
   * positioned+z-index, opacity, transform, filter, backdrop-filter,
   * perspective, clip-path, mask, mix-blend-mode, isolation:isolate,
   * will-change, contain, and flex/grid children with z-index — not just the
   * obvious four. Each trigger is {property, value} so the agent sees the
   * removable cause.
   *
   * @devtool findStackingContexts
   * @category layout
   * @signature findStackingContexts()
   * @returns {contexts:[{selector, zIndex, triggers:[{property,value}], reason:[string]}], count, total, scanned, capped} - bounded scan (4000 elements, 100 results); capped:true when truncated
   * @example
   * __devtool.findStackingContexts()
   */
  var __jsdoc_findStackingContexts = null;

  /**
   * Find elements positioned outside the viewport
   *
   * @devtool findOffscreen
   * @category layout
   * @signature findOffscreen()
   * @returns {offscreen:[{selector, direction:[above|below|left|right], rect}], count, total, scanned, capped} - bounded scan (4000 elements, 100 results); capped:true when truncated
   * @example
   * __devtool.findOffscreen()
   */
  var __jsdoc_findOffscreen = null;

  /**
   * Highlight an element with a colored overlay
   *
   * @devtool highlight
   * @category overlay
   * @signature highlight(selector, options?)
   * @param {string|Element} selector - CSS selector or DOM element
   * @param {{color?, label?, duration?}} options - Highlight options
   * @returns string - Overlay ID for removal
   * @example
   * __devtool.highlight("#form", {color: "blue", label: "Main Form"})
   */
  var __jsdoc_highlight = null;

  /**
   * Remove a specific highlight overlay
   *
   * @devtool removeHighlight
   * @category overlay
   * @signature removeHighlight(id)
   * @param {string} id - Overlay ID returned by highlight()
   * @returns {void}
   * @example
   * __devtool.removeHighlight("overlay-123")
   */
  var __jsdoc_removeHighlight = null;

  /**
   * Remove all highlight overlays
   *
   * @devtool clearAllOverlays
   * @category overlay
   * @signature clearAllOverlays()
   * @returns {void}
   * @example
   * __devtool.clearAllOverlays()
   */
  var __jsdoc_clearAllOverlays = null;

  /**
   * Enter interactive mode to select an element by clicking
   *
   * @devtool selectElement
   * @category interactive
   * @signature selectElement()
   * @returns Promise<string> - CSS selector of the clicked element; rejects on Escape
   * @example
   * const selector = await __devtool.selectElement()
   */
  var __jsdoc_selectElement = null;

  /**
   * Wait for an element to appear in the DOM
   *
   * @devtool waitForElement
   * @category interactive
   * @signature waitForElement(selector, timeout?)
   * @param {string} selector - CSS selector
   * @param {number} [timeout=5000] - Max wait time in ms
   * @returns Promise<{found, selector, tag, id, classes[]}> - JSON-safe element info; rejects on timeout
   * @example
   * const modal = await __devtool.waitForElement(".modal-open")
   */
  var __jsdoc_waitForElement = null;

  /**
   * Display a prompt dialog and wait for user input
   *
   * @devtool ask
   * @category interactive
   * @signature ask(question, options?)
   * @param {string} question - Question to display
   * @param {{choices?, default?}} options - Dialog options
   * @returns Promise<string> - User's answer
   * @example
   * const answer = await __devtool.ask("What color?", {choices: ["red", "blue"]})
   */
  var __jsdoc_ask = null;

  /**
   * Measure distance between two elements
   *
   * @devtool measureBetween
   * @category interactive
   * @signature measureBetween(selector1, selector2)
   * @param {string|Element} selector1 - First element
   * @param {string|Element} selector2 - Second element
   * @returns {distance:{x, y, diagonal}, direction:{horizontal, vertical}} - center-to-center distances in px
   * @example
   * __devtool.measureBetween(".header", ".footer")
   */
  var __jsdoc_measureBetween = null;

  /**
   * Capture serialized DOM snapshot
   *
   * @devtool captureDOM
   * @category capture
   * @signature captureDOM(selector?)
   * @param {string} [selector=body] - Root element selector
   * @returns {html, text, elementCount, timestamp}
   * @example
   * __devtool.captureDOM("#main-content")
   */
  var __jsdoc_captureDOM = null;

  /**
   * Capture all stylesheets and computed styles
   *
   * @devtool captureStyles
   * @category capture
   * @signature captureStyles()
   * @returns {stylesheets: [...], inlineStyles: [...]}
   * @example
   * __devtool.captureStyles()
   */
  var __jsdoc_captureStyles = null;

  /**
   * Capture comprehensive page state
   *
   * @devtool captureState
   * @category capture
   * @signature captureState()
   * @returns {url, title, viewport, scroll, forms, localStorage, sessionStorage}
   * @example
   * __devtool.captureState()
   */
  var __jsdoc_captureState = null;

  /**
   * Get performance timing and resource information
   *
   * @devtool captureNetwork
   * @category capture
   * @signature captureNetwork()
   * @returns {timing, resources: [...], paintTiming}
   * @example
   * __devtool.captureNetwork()
   */
  var __jsdoc_captureNetwork = null;

  /**
   * Get accessibility information for an element
   *
   * @devtool getA11yInfo
   * @category accessibility
   * @signature getA11yInfo(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {role, name, description, state, properties}
   * @example
   * __devtool.getA11yInfo("#submit-button")
   */
  var __jsdoc_getA11yInfo = null;

  /**
   * Calculate color contrast ratio for text
   *
   * @devtool getContrast
   * @category accessibility
   * @signature getContrast(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {ratio, foreground, background, passesAA, passesAAA}
   * @example
   * __devtool.getContrast(".body-text")
   */
  var __jsdoc_getContrast = null;

  /**
   * Get focusable elements in tab order
   *
   * @devtool getTabOrder
   * @category accessibility
   * @signature getTabOrder()
   * @returns [{element, tabIndex, natural}, ...]
   * @example
   * __devtool.getTabOrder()
   */
  var __jsdoc_getTabOrder = null;

  /**
   * Get text as a screen reader would announce it
   *
   * @devtool getScreenReaderText
   * @category accessibility
   * @signature getScreenReaderText(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {string}
   * @example
   * __devtool.getScreenReaderText("#nav-menu")
   */
  var __jsdoc_getScreenReaderText = null;

  /**
   * Run accessibility audit on the page
   *
   * @devtool auditAccessibility
   * @category accessibility
   * @signature auditAccessibility()
   * @returns {issues: [...], summary: {critical, serious, moderate, minor}}
   * @example
   * __devtool.auditAccessibility()
   */
  var __jsdoc_auditAccessibility = null;

  /**
   * Analyze DOM complexity and depth
   *
   * @devtool auditDOMComplexity
   * @category audit
   * @signature auditDOMComplexity()
   * @returns {elementCount, maxDepth, avgDepth, deepElements, widest}
   * @example
   * __devtool.auditDOMComplexity()
   */
  var __jsdoc_auditDOMComplexity = null;

  /**
   * Analyze CSS usage and potential issues
   *
   * @devtool auditCSS
   * @category audit
   * @signature auditCSS()
   * @returns {unusedRules, duplicates, specificity, importantCount}
   * @example
   * __devtool.auditCSS()
   */
  var __jsdoc_auditCSS = null;

  /**
   * Check for common security issues
   *
   * @devtool auditSecurity
   * @category audit
   * @signature auditSecurity()
   * @returns {issues: [...], summary}
   * @example
   * __devtool.auditSecurity()
   */
  var __jsdoc_auditSecurity = null;

  /**
   * Comprehensive page quality audit
   *
   * @devtool auditPageQuality
   * @category audit
   * @signature auditPageQuality()
   * @returns {dom, css, accessibility, security, performance}
   * @example
   * __devtool.auditPageQuality()
   */
  var __jsdoc_auditPageQuality = null;

  /**
   * Get recent interaction history
   *
   * @devtool interactions.getHistory
   * @category interactions
   * @signature interactions.getHistory(count?)
   * @param {number} [count=50] - Number of interactions to return
   * @returns [{event_type, target, position?, timestamp}, ...]
   * @example
   * __devtool.interactions.getHistory(10)
   */
  var __jsdoc_interactions_getHistory = null;

  /**
   * Get the most recent click event
   *
   * @devtool interactions.getLastClick
   * @category interactions
   * @signature interactions.getLastClick()
   * @returns {event_type, target, position, timestamp}|null
   * @example
   * __devtool.interactions.getLastClick()
   */
  var __jsdoc_interactions_getLastClick = null;

  /**
   * Get all clicks on elements matching selector
   *
   * @devtool interactions.getClicksOn
   * @category interactions
   * @signature interactions.getClicksOn(selector)
   * @param {string} selector - Selector pattern to match in target
   * @returns [{event_type, target, position, timestamp}, ...]
   * @example
   * __devtool.interactions.getClicksOn("button")
   */
  var __jsdoc_interactions_getClicksOn = null;

  /**
   * Get mouse movement samples around a timestamp
   *
   * @devtool interactions.getMouseTrail
   * @category interactions
   * @signature interactions.getMouseTrail(timestamp, windowMs?)
   * @param {number} timestamp - Center timestamp
   * @param {number} [windowMs=5000] - Time window in ms
   * @returns [{position, wall_time, interaction_time}, ...]
   * @example
   * __devtool.interactions.getMouseTrail(Date.now() - 1000)
   */
  var __jsdoc_interactions_getMouseTrail = null;

  /**
   * Get last click with surrounding mouse trail
   *
   * @devtool interactions.getLastClickContext
   * @category interactions
   * @signature interactions.getLastClickContext(trailMs?)
   * @param {number} [trailMs=2000] - Trail window in ms
   * @returns {click, mouseTrail}|null
   * @example
   * __devtool.interactions.getLastClickContext()
   */
  var __jsdoc_interactions_getLastClickContext = null;

  /**
   * Clear interaction history
   *
   * @devtool interactions.clear
   * @category interactions
   * @signature interactions.clear()
   * @returns {void}
   * @example
   * __devtool.interactions.clear()
   */
  var __jsdoc_interactions_clear = null;

  /**
   * Get recent mutation history
   *
   * @devtool mutations.getHistory
   * @category mutations
   * @signature mutations.getHistory(count?)
   * @param {number} [count=50] - Number of mutations to return
   * @returns [{mutation_type, target, added?, removed?, attribute?, timestamp}, ...]
   * @example
   * __devtool.mutations.getHistory(20)
   */
  var __jsdoc_mutations_getHistory = null;

  /**
   * Get elements added to DOM since timestamp
   *
   * @devtool mutations.getAdded
   * @category mutations
   * @signature mutations.getAdded(since?)
   * @param {number} [since=0] - Timestamp filter
   * @returns [{mutation_type: 'added', target, added, timestamp}, ...]
   * @example
   * __devtool.mutations.getAdded(Date.now() - 5000)
   */
  var __jsdoc_mutations_getAdded = null;

  /**
   * Get elements removed from DOM since timestamp
   *
   * @devtool mutations.getRemoved
   * @category mutations
   * @signature mutations.getRemoved(since?)
   * @param {number} [since=0] - Timestamp filter
   * @returns [{mutation_type: 'removed', target, removed, timestamp}, ...]
   * @example
   * __devtool.mutations.getRemoved(Date.now() - 5000)
   */
  var __jsdoc_mutations_getRemoved = null;

  /**
   * Get attribute changes since timestamp
   *
   * @devtool mutations.getModified
   * @category mutations
   * @signature mutations.getModified(since?)
   * @param {number} [since=0] - Timestamp filter
   * @returns [{mutation_type: 'attributes', target, attribute, timestamp}, ...]
   * @example
   * __devtool.mutations.getModified(Date.now() - 5000)
   */
  var __jsdoc_mutations_getModified = null;

  /**
   * Visually highlight recently added elements
   *
   * @devtool mutations.highlightRecent
   * @category mutations
   * @signature mutations.highlightRecent(duration?)
   * @param {number} [duration=5000] - How far back to look in ms
   * @returns {void}
   * @example
   * __devtool.mutations.highlightRecent(3000)
   */
  var __jsdoc_mutations_highlightRecent = null;

  /**
   * Clear mutation history
   *
   * @devtool mutations.clear
   * @category mutations
   * @signature mutations.clear()
   * @returns {void}
   * @example
   * __devtool.mutations.clear()
   */
  var __jsdoc_mutations_clear = null;

  /**
   * Pause mutation tracking
   *
   * @devtool mutations.pause
   * @category mutations
   * @signature mutations.pause()
   * @returns {void}
   * @example
   * __devtool.mutations.pause()
   */
  var __jsdoc_mutations_pause = null;

  /**
   * Resume mutation tracking
   *
   * @devtool mutations.resume
   * @category mutations
   * @signature mutations.resume()
   * @returns {void}
   * @example
   * __devtool.mutations.resume()
   */
  var __jsdoc_mutations_resume = null;

  /**
   * Show the floating indicator
   *
   * @devtool indicator.show
   * @category indicator
   * @signature indicator.show()
   * @returns {void}
   * @example
   * __devtool.indicator.show()
   */
  var __jsdoc_indicator_show = null;

  /**
   * Hide the floating indicator
   *
   * @devtool indicator.hide
   * @category indicator
   * @signature indicator.hide()
   * @returns {void}
   * @example
   * __devtool.indicator.hide()
   */
  var __jsdoc_indicator_hide = null;

  /**
   * Toggle the floating indicator visibility
   *
   * @devtool indicator.toggle
   * @category indicator
   * @signature indicator.toggle()
   * @returns {void}
   * @example
   * __devtool.indicator.toggle()
   */
  var __jsdoc_indicator_toggle = null;

  /**
   * Toggle the indicator's expanded panel
   *
   * @devtool indicator.togglePanel
   * @category indicator
   * @signature indicator.togglePanel()
   * @returns {void}
   * @example
   * __devtool.indicator.togglePanel()
   */
  var __jsdoc_indicator_togglePanel = null;

  /**
   * Remove the floating indicator completely
   *
   * @devtool indicator.destroy
   * @category indicator
   * @signature indicator.destroy()
   * @returns {void}
   * @example
   * __devtool.indicator.destroy()
   */
  var __jsdoc_indicator_destroy = null;

  /**
   * Open sketch mode for wireframing
   *
   * @devtool sketch.open
   * @category sketch
   * @signature sketch.open()
   * @returns {void}
   * @example
   * __devtool.sketch.open()
   */
  var __jsdoc_sketch_open = null;

  /**
   * Close sketch mode
   *
   * @devtool sketch.close
   * @category sketch
   * @signature sketch.close()
   * @returns {void}
   * @example
   * __devtool.sketch.close()
   */
  var __jsdoc_sketch_close = null;

  /**
   * Toggle sketch mode on/off
   *
   * @devtool sketch.toggle
   * @category sketch
   * @signature sketch.toggle()
   * @returns {void}
   * @example
   * __devtool.sketch.toggle()
   */
  var __jsdoc_sketch_toggle = null;

  /**
   * Set the active drawing tool
   *
   * @devtool sketch.setTool
   * @category sketch
   * @signature sketch.setTool(tool)
   * @param {string} tool - Tool name: select, rectangle, ellipse, line, arrow, freedraw, text, note, button, input, image, eraser
   * @returns {void}
   * @example
   * __devtool.sketch.setTool("rectangle")
   */
  var __jsdoc_sketch_setTool = null;

  /**
   * Save sketch and send to proxy server
   *
   * @devtool sketch.save
   * @category sketch
   * @signature sketch.save()
   * @returns {void}
   * @example
   * __devtool.sketch.save()
   */
  var __jsdoc_sketch_save = null;

  /**
   * Export sketch data as JSON
   *
   * @devtool sketch.toJSON
   * @category sketch
   * @signature sketch.toJSON()
   * @returns object - Serialized sketch data
   * @example
   * const data = __devtool.sketch.toJSON()
   */
  var __jsdoc_sketch_toJSON = null;

  /**
   * Load sketch data from JSON
   *
   * @devtool sketch.fromJSON
   * @category sketch
   * @signature sketch.fromJSON(data)
   * @param {object} data - Sketch data from toJSON()
   * @returns {void}
   * @example
   * __devtool.sketch.fromJSON(savedData)
   */
  var __jsdoc_sketch_fromJSON = null;

  /**
   * Export sketch as PNG data URL
   *
   * @devtool sketch.toDataURL
   * @category sketch
   * @signature sketch.toDataURL()
   * @returns string - PNG data URL
   * @example
   * const png = __devtool.sketch.toDataURL()
   */
  var __jsdoc_sketch_toDataURL = null;

  /**
   * Undo last sketch action
   *
   * @devtool sketch.undo
   * @category sketch
   * @signature sketch.undo()
   * @returns {void}
   * @example
   * __devtool.sketch.undo()
   */
  var __jsdoc_sketch_undo = null;

  /**
   * Redo previously undone action
   *
   * @devtool sketch.redo
   * @category sketch
   * @signature sketch.redo()
   * @returns {void}
   * @example
   * __devtool.sketch.redo()
   */
  var __jsdoc_sketch_redo = null;

  /**
   * Clear all sketch elements
   *
   * @devtool sketch.clear
   * @category sketch
   * @signature sketch.clear()
   * @returns {void}
   * @example
   * __devtool.sketch.clear()
   */
  var __jsdoc_sketch_clear = null;

  /**
   * Extract all links from the page with context (navigation, footer, etc.)
   *
   * @devtool content.extractLinks
   * @category content
   * @signature content.extractLinks(options?)
   * @param {boolean} options.internal - Only internal links
   * @param {boolean} options.external - Only external links
   * @param {boolean} options.includeAnchors - Include anchor-only links
   * @param {string} options.selector - Limit to links within selector
   * @param {number} [options.maxLinks=200] - Max link entries returned across all categories; stats still count everything
   * @returns {url, internal[], external[], anchors[], mailto[], tel[], other[], truncated, stats}
   * @example
   * __devtool.content.extractLinks({internal: true})
   */
  var __jsdoc_content_extractLinks = null;

  /**
   * Extract navigation structure from the page (nav elements, header, footer, breadcrumbs)
   *
   * @devtool content.extractNavigation
   * @category content
   * @signature content.extractNavigation()
   * @returns {url, navElements[], header, footer, breadcrumbs, sidebar}
   * @example
   * __devtool.content.extractNavigation()
   */
  var __jsdoc_content_extractNavigation = null;

  /**
   * Extract page content as markdown
   *
   * @devtool content.extractContent
   * @category content
   * @signature content.extractContent(options?)
   * @param {string} options.selector - Selector for main content (auto-detected if not provided)
   * @param {boolean} [options.includeImages=true] - Include image references
   * @param {boolean} [options.includeLinks=true] - Include link URLs
   * @param {number} [options.maxLength=50000] - Maximum content length
   * @returns {url, title, selector, markdown, meta, headings[], wordCount, truncated}
   * @example
   * __devtool.content.extractContent({selector: "article"})
   */
  var __jsdoc_content_extractContent = null;

  /**
   * Extract heading hierarchy for page outline
   *
   * @devtool content.extractHeadings
   * @category content
   * @signature content.extractHeadings(scope?)
   * @param {Element} [scope=document] - Optional scope element
   * @returns [{level, text, id}]
   * @example
   * __devtool.content.extractHeadings()
   */
  var __jsdoc_content_extractHeadings = null;

  /**
   * Build sitemap structure from internal links on the page
   *
   * @devtool content.buildSitemap
   * @category content
   * @signature content.buildSitemap(options?)
   * @param {number} [options.maxDepth=5] - Maximum URL depth to include
   * @returns {url, baseUrl, pages{}, tree{}, stats}
   * @example
   * __devtool.content.buildSitemap()
   */
  var __jsdoc_content_buildSitemap = null;

  /**
   * Extract structured data (JSON-LD, Open Graph, Twitter Cards)
   *
   * @devtool content.extractStructuredData
   * @category content
   * @signature content.extractStructuredData()
   * @returns {url, jsonLd[], openGraph{}, twitter{}, microdata[]}
   * @example
   * __devtool.content.extractStructuredData()
   */
  var __jsdoc_content_extractStructuredData = null;

  /**
   * Check if WebSocket is connected to proxy server
   *
   * @devtool isConnected
   * @category connection
   * @signature isConnected()
   * @returns {boolean}
   * @example
   * if (__devtool.isConnected()) { ... }
   */
  var __jsdoc_isConnected = null;

  /**
   * Get detailed WebSocket connection status
   *
   * @devtool getStatus
   * @category connection
   * @signature getStatus()
   * @returns string - connecting, connected, closing, closed, not_initialized, unknown
   * @example
   * console.log(__devtool.getStatus())
   */
  var __jsdoc_getStatus = null;

  /**
   * Get comprehensive inspection of an element (combines multiple inspection calls)
   *
   * @devtool inspect
   * @category inspection
   * @signature inspect(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {info, position, box, layout, stacking, container, visibility, viewport}
   * @example
   * __devtool.inspect("#main-form")
   */
  var __jsdoc_inspect = null;

  /**
   * Run comprehensive layout diagnostics
   *
   * @devtool diagnoseLayout
   * @category layout
   * @signature diagnoseLayout(selector?)
   * @param {string} [selector] - Optional element to focus analysis on
   * @returns {overflows, stackingContexts, offscreen, element?}
   * @example
   * __devtool.diagnoseLayout()
   */
  var __jsdoc_diagnoseLayout = null;

  /**
   * Cause-to-symptom layout diagnostics: one bounded synchronous pass
   * (~30-80ms) over four high-diagnostic-distance CSS bug classes, naming the
   * OFFENDING ANCESTOR and the correct fix plus the common wrong fix to avoid.
   * Checks: containing-block-trap (fixed/absolute captured by an ancestor
   * transform/filter/will-change/contain), ineffective-zindex (z-index on
   * position:static, silently ignored), click-interception (an overlay eats a
   * visible control's clicks), clipped-descendant (content cut off by an
   * ancestor overflow:hidden/clip).
   *
   * @devtool diagnoseLayoutIssues
   * @category layout
   * @signature diagnoseLayoutIssues()
   * @returns {findings:[{check, severity, selector, cause, cause_property, detail, fix, avoid}], count, scanned, capped, by_check} - max 15 findings per check, 4000-element scan budget; capped:true when the budget was exceeded
   * @example
   * __devtool.diagnoseLayoutIssues()
   */
  var __jsdoc_diagnoseLayoutIssues = null;

  /**
   * Fill a form control React-safely: sets the value through the native
   * prototype value setter (so controlled components observe the change) and
   * dispatches input + change events. Handles input, textarea, select,
   * checkbox/radio (pass true/false), and contenteditable.
   *
   * @devtool fill
   * @category interactive
   * @signature fill(selector, value)
   * @param {string|Element} selector - CSS selector or DOM element
   * @param {string|boolean} value - Value to set (boolean for checkbox/radio)
   * @returns {found, selector, tag, id, classes[], filled, value?, checked?} - or {error}
   * @example
   * __devtool.fill("#email", "user@example.com")
   */
  var __jsdoc_fill = null;

  /**
   * Click an element with a realistic event sequence (pointerdown, mousedown,
   * pointerup, mouseup, click) dispatched at the element center, then focus.
   * Frameworks gating on pointer/mouse events see the full sequence, unlike
   * el.click().
   *
   * @devtool clickElement
   * @category interactive
   * @signature clickElement(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {found, selector, tag, id, classes[], clicked, x, y} - or {error}
   * @example
   * __devtool.clickElement("#submit-btn")
   */
  var __jsdoc_clickElement = null;

  /**
   * Wait for an element to be removed from the DOM
   *
   * @devtool waitForRemoved
   * @category interactive
   * @signature waitForRemoved(selector, timeout?)
   * @param {string} selector - CSS selector
   * @param {number} [timeout=5000] - Max wait time in ms
   * @returns Promise<{removed: true, selector}> - rejects on timeout
   * @example
   * await __devtool.waitForRemoved(".loading-spinner")
   */
  var __jsdoc_waitForRemoved = null;

  /**
   * Wait for an element to exist AND be visible (rendered, non-zero size, not
   * display:none / visibility:hidden / opacity:0)
   *
   * @devtool waitForVisible
   * @category interactive
   * @signature waitForVisible(selector, timeout?)
   * @param {string} selector - CSS selector
   * @param {number} [timeout=5000] - Max wait time in ms
   * @returns Promise<{found, selector, tag, id, classes[], visible: true}> - rejects on timeout
   * @example
   * await __devtool.waitForVisible(".modal-dialog")
   */
  var __jsdoc_waitForVisible = null;

  /**
   * Scroll an element into view (centered) and report its resulting rect
   *
   * @devtool scrollIntoView
   * @category interactive
   * @signature scrollIntoView(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {found, selector, tag, id, classes[], scrolled, rect} - or {error}
   * @example
   * __devtool.scrollIntoView("#pricing-section")
   */
  var __jsdoc_scrollIntoView = null;

  /**
   * Check text elements for fragility: overflow risk from long words, layout
   * shift potential, and problematic breakpoints
   *
   * @devtool checkTextFragility
   * @category layout
   * @signature checkTextFragility()
   * @returns {issues:[{selector, issues[], longestWord, minWidth}], summary:{total, errors, warnings, elementsAnalyzed}}
   * @example
   * __devtool.checkTextFragility()
   */
  var __jsdoc_checkTextFragility = null;

  /**
   * Check elements for responsive design risks: fixed dimensions, small touch
   * targets, horizontal scroll, fragile positioning, text sizing, table layout
   *
   * @devtool checkResponsiveRisk
   * @category layout
   * @signature checkResponsiveRisk()
   * @returns {issues:[{selector, tagName, issues:[{severity, ...}]}], summary:{total, errors, warnings, elementsAnalyzed}} - sorted errors first
   * @example
   * __devtool.checkResponsiveRisk()
   */
  var __jsdoc_checkResponsiveRisk = null;

  /**
   * Run a responsive design audit across multiple viewport sizes: loads the
   * page in hidden iframes at each size and runs layout/overflow/a11y checks
   *
   * @devtool responsiveAudit
   * @category layout
   * @signature responsiveAudit(options?)
   * @param {array} [options.viewports] - Custom viewports [{name, width, height}]
   * @param {array} [options.checks] - Checks to run: layout, overflow, a11y
   * @param {number} [options.timeout=10000] - Load timeout per viewport in ms
   * @param {boolean} [options.raw=false] - Return raw JSON instead of compact text
   * @returns Promise<object|string> - audit results; rejects if the module is not loaded
   * @example
   * await __devtool.responsiveAudit({checks: ["layout", "overflow"]})
   */
  var __jsdoc_responsiveAudit = null;

  /**
   * Show a toast notification in the browser (developer-facing UI; toasts are
   * NOT forwarded to the agent event queue)
   *
   * @devtool toast.show
   * @category toast
   * @signature toast.show(message, options?)
   * @param {string} message - Toast text
   * @param {{type?, duration?, title?}} options - type: success|error|warning|info
   * @returns string - toast id usable with toast.dismiss(id)
   * @example
   * __devtool.toast.show("Saved", {type: "success"})
   */
  var __jsdoc_toast_show = null;

  /**
   * Toast helpers for each level; toast.dismiss(id) / toast.dismissAll()
   * remove toasts, toast.configure(opts) sets corner/duration defaults
   *
   * @devtool toast.success
   * @category toast
   * @signature toast.success(message, options?)
   * @param {string} message - Toast text (same for toast.error / toast.warning / toast.info)
   * @returns string - toast id
   * @example
   * __devtool.toast.error("Request failed")
   */
  var __jsdoc_toast_success = null;

  /**
   * Get a value from the persistent key-value store (daemon-backed; survives
   * reloads). Scopes: global (all projects), folder (URL folder), page (exact
   * URL). Convenience namespaces store.global/store.folder/store.page pre-bind
   * the scope.
   *
   * @devtool store.get
   * @category store
   * @signature store.get(key, options?)
   * @param {string} key - Key to read
   * @param {{scope?, scopeKey?}} options - scope: global|folder|page
   * @returns Promise<any> - rejects fast when the DevTool socket is disconnected
   * @example
   * await __devtool.store.get("theme", {scope: "global"})
   */
  var __jsdoc_store_get = null;

  /**
   * Set a value in the persistent key-value store
   *
   * @devtool store.set
   * @category store
   * @signature store.set(key, value, options?)
   * @param {string} key - Key to write
   * @param {any} value - JSON-serializable value
   * @param {{scope?, scopeKey?, metadata?}} options - scope: global|folder|page
   * @returns Promise<object>
   * @example
   * await __devtool.store.set("draft", {text: "hello"}, {scope: "page"})
   */
  var __jsdoc_store_set = null;

  /**
   * Delete a key from the persistent store
   *
   * @devtool store.delete
   * @category store
   * @signature store.delete(key, options?)
   * @param {string} key - Key to delete
   * @param {{scope?, scopeKey?}} options - scope: global|folder|page
   * @returns Promise<object>
   * @example
   * await __devtool.store.delete("draft", {scope: "page"})
   */
  var __jsdoc_store_delete = null;

  /**
   * List keys in the persistent store for a scope
   *
   * @devtool store.list
   * @category store
   * @signature store.list(options?)
   * @param {{scope?, scopeKey?}} options - scope: global|folder|page
   * @returns Promise<object> - key listing
   * @example
   * await __devtool.store.list({scope: "global"})
   */
  var __jsdoc_store_list = null;

  /**
   * Get all key-value pairs in a scope
   *
   * @devtool store.getAll
   * @category store
   * @signature store.getAll(options?)
   * @param {{scope?, scopeKey?}} options - scope: global|folder|page
   * @returns Promise<object>
   * @example
   * await __devtool.store.getAll({scope: "folder"})
   */
  var __jsdoc_store_getAll = null;

  /**
   * Clear all keys in a scope of the persistent store
   *
   * @devtool store.clear
   * @category store
   * @signature store.clear(options?)
   * @param {{scope?, scopeKey?}} options - scope: global|folder|page
   * @returns Promise<object>
   * @example
   * await __devtool.store.clear({scope: "page"})
   */
  var __jsdoc_store_clear = null;

  /**
   * List active agnt run sessions (daemon query over the DevTool socket)
   *
   * @devtool session.list
   * @category session
   * @signature session.list(global?)
   * @param {boolean} [global=false] - List sessions from all directories
   * @returns Promise<object> - sessions list with count
   * @example
   * await __devtool.session.list()
   */
  var __jsdoc_session_list = null;

  /**
   * Get details for a specific session
   *
   * @devtool session.get
   * @category session
   * @signature session.get(code)
   * @param {string} code - Session code
   * @returns Promise<object>
   * @example
   * await __devtool.session.get("abc123")
   */
  var __jsdoc_session_get = null;

  /**
   * Send a message to a session immediately (injected into the AI tool's PTY)
   *
   * @devtool session.send
   * @category session
   * @signature session.send(code, message)
   * @param {string} code - Session code
   * @param {string} message - Message to deliver
   * @returns Promise<object> - success status
   * @example
   * await __devtool.session.send("abc123", "please rerun the tests")
   */
  var __jsdoc_session_send = null;

  /**
   * Schedule a message for future delivery to a session
   *
   * @devtool session.schedule
   * @category session
   * @signature session.schedule(code, duration, message)
   * @param {string} code - Session code
   * @param {string} duration - Delay, e.g. "5m" or "1h30m"
   * @param {string} message - Message to deliver
   * @returns Promise<object> - {task_id, deliver_at}
   * @example
   * await __devtool.session.schedule("abc123", "10m", "check the deploy")
   */
  var __jsdoc_session_schedule = null;

  /**
   * List scheduled session tasks
   *
   * @devtool session.tasks
   * @category session
   * @signature session.tasks(global?)
   * @param {boolean} [global=false] - List tasks from all directories
   * @returns Promise<object> - tasks list with count
   * @example
   * await __devtool.session.tasks()
   */
  var __jsdoc_session_tasks = null;

  /**
   * Cancel a scheduled session task
   *
   * @devtool session.cancel
   * @category session
   * @signature session.cancel(taskId)
   * @param {string} taskId - Task ID from session.schedule / session.tasks
   * @returns Promise<object> - success status
   * @example
   * await __devtool.session.cancel("task-42")
   */
  var __jsdoc_session_cancel = null;

  /**
   * Outline every element with nesting-depth colors (CSS injection overlay)
   *
   * @devtool diagnostics.outlineAll
   * @category diagnostics
   * @signature diagnostics.outlineAll()
   * @returns {success, mode}
   * @example
   * __devtool.diagnostics.outlineAll()
   */
  var __jsdoc_diagnostics_outlineAll = null;

  /**
   * Highlight all elements with a non-auto z-index and list them sorted by
   * z-index descending
   *
   * @devtool diagnostics.showStacking
   * @category diagnostics
   * @signature diagnostics.showStacking()
   * @returns {success, mode, total, zIndexElements:[{selector, zIndex}], capped} - list capped at 30, total is the full count
   * @example
   * __devtool.diagnostics.showStacking()
   */
  var __jsdoc_diagnostics_showStacking = null;

  /**
   * Panel + summary of every unique color in use (text and background)
   *
   * @devtool diagnostics.showColorPalette
   * @category diagnostics
   * @signature diagnostics.showColorPalette()
   * @returns {success, mode, panelId, uniqueColors, colors:[{color, count}], capped} - list capped at 30
   * @example
   * __devtool.diagnostics.showColorPalette()
   */
  var __jsdoc_diagnostics_showColorPalette = null;

  /**
   * Panel + summary of the spacing scale (margin/padding values in use)
   *
   * @devtool diagnostics.showSpacingScale
   * @category diagnostics
   * @signature diagnostics.showSpacingScale()
   * @returns {success, mode, panelId, uniqueValues, values:[{value, count}], capped} - list capped at 40
   * @example
   * __devtool.diagnostics.showSpacingScale()
   */
  var __jsdoc_diagnostics_showSpacingScale = null;

  /**
   * Panel + summary of unique typography styles (size/family/weight/
   * line-height/color combinations, with usage counts)
   *
   * @devtool diagnostics.showTypographyPanel
   * @category diagnostics
   * @signature diagnostics.showTypographyPanel()
   * @returns {success, mode, panelId, uniqueStyles, styles:[...], capped} - list capped at 30
   * @example
   * __devtool.diagnostics.showTypographyPanel()
   */
  var __jsdoc_diagnostics_showTypographyPanel = null;

  /**
   * Capture a serialized DOM snapshot (tags, attributes, text, key computed
   * styles) for later diffing with compareDOMSnapshots
   *
   * @devtool diagnostics.captureDOMSnapshot
   * @category diagnostics
   * @signature diagnostics.captureDOMSnapshot(options?)
   * @param {Element} [options.root=document.body] - Snapshot root
   * @param {boolean} [options.includeStyles=true] - Capture key computed styles per node
   * @param {boolean} [options.captureAllStyles=false] - Capture every computed property (large)
   * @param {number} [options.maxNodes=1500] - Node budget; subtrees beyond it are dropped and capped:true is set
   * @returns {timestamp, url, viewport, root, capped, stats:{totalElements, totalTextNodes, maxDepth}, options}
   * @example
   * const before = __devtool.diagnostics.captureDOMSnapshot()
   */
  var __jsdoc_diagnostics_captureDOMSnapshot = null;

  /**
   * Diff two DOM snapshots: added/removed/modified nodes with changed
   * attributes and changed computed-style properties (before/after values)
   *
   * @devtool diagnostics.compareDOMSnapshots
   * @category diagnostics
   * @signature diagnostics.compareDOMSnapshots(baseline, current)
   * @param {object} baseline - Snapshot from captureDOMSnapshot
   * @param {object} current - Later snapshot
   * @returns {added[], removed[], modified:[{path, changes[], styleChanges:[{property,before,after}], before, after}], capped, summary, baseline, current} - 100 entries per bucket, 10 style changes per node; node fields are compact summaries, not full subtrees
   * @example
   * __devtool.diagnostics.compareDOMSnapshots(before, __devtool.diagnostics.captureDOMSnapshot())
   */
  var __jsdoc_diagnostics_compareDOMSnapshots = null;

  /**
   * Render a DOM diff as an on-page panel (calls compareDOMSnapshots)
   *
   * @devtool diagnostics.showDOMDiff
   * @category diagnostics
   * @signature diagnostics.showDOMDiff(baseline, current)
   * @param {object} baseline - Snapshot from captureDOMSnapshot
   * @param {object} current - Later snapshot
   * @returns {success, mode, panelId, diff}
   * @example
   * __devtool.diagnostics.showDOMDiff(before, after)
   */
  var __jsdoc_diagnostics_showDOMDiff = null;

  /**
   * Clear one diagnostic mode (CSS + panel), or everything when no mode given
   *
   * @devtool diagnostics.clear
   * @category diagnostics
   * @signature diagnostics.clear(mode?)
   * @param {string} [mode] - Mode name from diagnostics.list(); omit to clear all
   * @returns {success, cleared?}
   * @example
   * __devtool.diagnostics.clear("outline-all")
   */
  var __jsdoc_diagnostics_clear = null;

  /**
   * List active diagnostic modes
   *
   * @devtool diagnostics.list
   * @category diagnostics
   * @signature diagnostics.list()
   * @returns {activeModes: [string], count}
   * @example
   * __devtool.diagnostics.list()
   */
  var __jsdoc_diagnostics_list = null;

})();

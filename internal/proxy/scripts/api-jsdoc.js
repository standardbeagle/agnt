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
   * @returns {void}
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
   * @returns {void}
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
   * @returns {void}
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
   * @returns {void}
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
   * @returns {void}
   * @example
   * __devtool.error("Failed to load data", {status: 500})
   */
  var __jsdoc_error = null;

  /**
   * Capture a screenshot of the page or a specific element
   *
   * @devtool screenshot
   * @category screenshot
   * @signature screenshot(name?, selector?)
   * @param {string} [name=screenshot_<timestamp>] - Screenshot name
   * @param {string} [selector=body] - CSS selector for element to capture
   * @returns Promise<{name, width, height, selector}>
   * @example
   * await __devtool.screenshot("homepage")\nawait __devtool.screenshot("header", "#main-header")
   */
  var __jsdoc_screenshot = null;

  /**
   * Get comprehensive information about an element
   *
   * @devtool getElementInfo
   * @category inspection
   * @signature getElementInfo(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {tag, id, classes, attributes, text, html, dimensions, position}
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
   * @returns {top, right, bottom, left, width, height, x, y}
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
   * Get containing block and scroll container
   *
   * @devtool getContainer
   * @category inspection
   * @signature getContainer(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {containingBlock, scrollContainer, offsetParent}
   * @example
   * __devtool.getContainer(".absolute-element")
   */
  var __jsdoc_getContainer = null;

  /**
   * Get stacking context information (z-index, creates context)
   *
   * @devtool getStacking
   * @category inspection
   * @signature getStacking(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns {zIndex, createsContext, reason?, parentContext}
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
   * @returns {transform, transformOrigin, matrix}
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
   * @returns {overflow, overflowX, overflowY, scrollWidth, scrollHeight, isScrollable}
   * @example
   * __devtool.getOverflow(".scrollable-panel")
   */
  var __jsdoc_getOverflow = null;

  /**
   * Get all child elements with optional filtering
   *
   * @devtool walkChildren
   * @category tree
   * @signature walkChildren(selector, options?)
   * @param {string|Element} selector - CSS selector or DOM element
   * @param {{maxDepth?, filter?}} options - Walk options
   * @returns [{element, depth, path}, ...]
   * @example
   * __devtool.walkChildren("#container", {maxDepth: 2})
   */
  var __jsdoc_walkChildren = null;

  /**
   * Get all parent elements up to document
   *
   * @devtool walkParents
   * @category tree
   * @signature walkParents(selector)
   * @param {string|Element} selector - CSS selector or DOM element
   * @returns [{element, depth}, ...]
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
   * @param {string|function} condition - CSS selector or predicate function
   * @returns {Element|null}
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
   * @returns {visible: boolean, reasons?: string[]}
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
   * @param {number} [threshold=0] - Percentage visible required
   * @returns {inViewport: boolean, percentVisible: number, position}
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
   * @returns {overlaps: boolean, intersection?: {x, y, width, height}}
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
   * @returns [{element, overflow, width, parentWidth}, ...]
   * @example
   * __devtool.findOverflows()
   */
  var __jsdoc_findOverflows = null;

  /**
   * Find all stacking contexts in the document
   *
   * @devtool findStackingContexts
   * @category layout
   * @signature findStackingContexts()
   * @returns [{element, zIndex, reason}, ...]
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
   * @returns [{element, position, distance}, ...]
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
   * @returns Promise<Element> - The selected element
   * @example
   * const el = await __devtool.selectElement()
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
   * @returns {Promise<Element>}
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
   * @returns {horizontal, vertical, diagonal}
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
   * @returns {url, internal[], external[], anchors[], mailto[], tel[], other[], stats}
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

})();

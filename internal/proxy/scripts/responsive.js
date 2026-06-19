// Responsive audit module for DevTool
// Runs layout, overflow, and accessibility audits across multiple viewport sizes

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Default viewports for responsive testing
  var DEFAULT_VIEWPORTS = [
    { name: 'mobile', width: 375, height: 667 },
    { name: 'tablet', width: 768, height: 1024 },
    { name: 'desktop', width: 1440, height: 900 }
  ];

  // Iframe pool for sequential viewport testing
  var iframePool = {
    active: null,
    container: null
  };

  // Audit state for concurrent request handling
  var auditState = {
    pending: null,       // Current pending audit Promise
    lastResult: null,    // Cached result from last audit
    lastTime: 0          // Timestamp of last audit
  };

  // Debounce window in ms - return cached result if within this window
  var DEBOUNCE_WINDOW = 500;

  // Map from finding id to selector for highlight lookups.
  // Lazily references the shared registry on window.__devtool.audit so findings
  // from other audit modules (a11y, quality, css, dom) are also highlightable.
  function getSharedRegistry() {
    if (!window.__devtool) { window.__devtool = {}; }
    if (!window.__devtool.audit) { window.__devtool.audit = {}; }
    if (!window.__devtool.audit.findingSelectors) { window.__devtool.audit.findingSelectors = {}; }
    return window.__devtool.audit.findingSelectors;
  }

  // Legacy alias kept for backward-compat within this module's closures.
  var findingSelectors = {};

  /**
   * Generate a stable 8-char hex finding ID from type, selector, and message.
   * Uses a simple two-pass hash (FNV-1a 32-bit) producing 8 lowercase hex chars.
   * The same inputs always produce the same output across runs.
   * @param {string} type - Issue type (layout/overflow/a11y)
   * @param {string} selector - CSS selector for the element
   * @param {string} message - Issue message
   * @returns {string} 8-char lowercase hex string
   */
  function computeFindingID(type, selector, message) {
    var input = type + '\x00' + selector + '\x00' + message;
    var h = 0x811c9dc5;
    for (var i = 0; i < input.length; i++) {
      h = h ^ input.charCodeAt(i);
      h = (h + (h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24)) >>> 0;
    }
    return ('00000000' + h.toString(16)).slice(-8);
  }

  /**
   * Create a hidden iframe container for viewport testing
   */
  function createContainer() {
    if (iframePool.container) {
      return iframePool.container;
    }

    var container = document.createElement('div');
    container.id = '__devtool_responsive_container';
    container.style.cssText = [
      'position: fixed',
      'top: -9999px',
      'left: -9999px',
      'visibility: hidden',
      'pointer-events: none',
      'z-index: -9999'
    ].join('; ');

    document.body.appendChild(container);
    iframePool.container = container;

    return container;
  }

  /**
   * Create an iframe at specific viewport dimensions
   */
  function createIframe(viewport) {
    var iframe = document.createElement('iframe');
    iframe.style.cssText = [
      'position: absolute',
      'top: 0',
      'left: 0',
      'width: ' + viewport.width + 'px',
      'height: ' + viewport.height + 'px',
      'border: none',
      'margin: 0',
      'padding: 0',
      'overflow: auto'
    ].join('; ');

    // Same origin to inherit injected scripts — but carry the content-frame
    // marker so the proxy serves the page UNWRAPPED rather than as another
    // chrome shell (always-wrap model — docs/responsive-canonical-target.md
    // §4/§7). Without the marker this would load shell-in-shell.
    iframe.src = markedContentSrc();

    return iframe;
  }

  // markedContentSrc returns the current page URL with the content-frame marker
  // appended so an off-screen sweep iframe loads the page unwrapped.
  function markedContentSrc() {
    var param = (window.__devtool && window.__devtool.FRAME_PARAM) || '__devtool_frame';
    var rid = 'sweep-' + Date.now();
    try {
      var u = new URL(window.location.href);
      u.searchParams.set(param, rid);
      return u.toString();
    } catch (e) {
      var base = window.location.href.split('#')[0];
      return base + (base.indexOf('?') >= 0 ? '&' : '?') + param + '=' + rid;
    }
  }

  /**
   * Wait for iframe to load
   */
  function waitForIframeLoad(iframe, timeout) {
    timeout = timeout || 10000;

    return new Promise(function(resolve, reject) {
      var timeoutId = setTimeout(function() {
        reject(new Error('Iframe load timeout after ' + timeout + 'ms'));
      }, timeout);

      iframe.onload = function() {
        clearTimeout(timeoutId);
        resolve(iframe);
      };

      iframe.onerror = function(err) {
        clearTimeout(timeoutId);
        reject(new Error('Iframe load error: ' + err));
      };

      // Check if already loaded
      if (iframe.contentDocument && iframe.contentDocument.readyState === 'complete') {
        clearTimeout(timeoutId);
        resolve(iframe);
      }
    });
  }

  /**
   * Remove iframe from DOM
   */
  function cleanupIframe(iframe) {
    if (iframe && iframe.parentNode) {
      iframe.parentNode.removeChild(iframe);
    }
    if (iframePool.active === iframe) {
      iframePool.active = null;
    }
  }

  /**
   * Run a single viewport audit
   */
  function auditViewport(viewport, options) {
    return new Promise(function(resolve, reject) {
      var container = createContainer();
      var iframe = createIframe(viewport);

      iframePool.active = iframe;
      container.appendChild(iframe);

      waitForIframeLoad(iframe, options.timeout || 10000)
        .then(function() {
          // Run audits in iframe context
          var issues = [];
          var iframeWindow = iframe.contentWindow;
          var iframeDoc = iframe.contentDocument;

          if (!iframeWindow || !iframeDoc) {
            throw new Error('Cannot access iframe content');
          }

          // Run enabled checks
          var checks = options.checks || ['layout', 'overflow', 'a11y'];

          if (checks.indexOf('layout') !== -1) {
            try {
              var layoutIssues = detectLayoutIssues(iframeWindow, viewport.width);
              issues = issues.concat(layoutIssues);
            } catch (e) {
              console.warn('[DevTool] Layout check failed:', e.message);
            }
          }

          if (checks.indexOf('overflow') !== -1) {
            try {
              var overflowIssues = detectOverflowIssues(iframeWindow, viewport.width);
              issues = issues.concat(overflowIssues);
            } catch (e) {
              console.warn('[DevTool] Overflow check failed:', e.message);
            }
          }

          // Handle a11y check (async)
          var a11yPromise;
          if (checks.indexOf('a11y') !== -1) {
            a11yPromise = runResponsiveA11yCheck(iframeWindow, viewport.width, options)
              .catch(function(e) {
                console.warn('[DevTool] A11y check failed:', e.message);
                return [];
              });
          } else {
            a11yPromise = Promise.resolve([]);
          }

          a11yPromise.then(function(a11yIssues) {
            issues = issues.concat(a11yIssues);

            // Cleanup
            cleanupIframe(iframe);

            resolve({
              name: viewport.name,
              width: viewport.width,
              height: viewport.height,
              issues: issues,
              issueCount: issues.length,
              timestamp: Date.now()
            });
          });
        })
        .catch(function(err) {
          cleanupIframe(iframe);
          reject(err);
        });
    });
  }

  /**
   * Detect layout issues in the given window context
   */
  function detectLayoutIssues(win, viewportWidth) {
    var doc = win.document;
    var issues = [];
    var isMobile = viewportWidth < 768;

    var elements = doc.querySelectorAll('*');
    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];

      // Skip hidden elements
      if (!isVisibleInContext(el, win)) {
        continue;
      }

      var rect = el.getBoundingClientRect();
      var style = win.getComputedStyle(el);
      var selector = generateSelectorInContext(el, win);

      // Collapsed content
      if (rect.height < 1 && el.textContent && el.textContent.trim().length > 0) {
        var collapsedMsg = 'collapsed content, element has text but zero height';
        var collapsedID = computeFindingID('layout', selector, collapsedMsg);
        findingSelectors[collapsedID] = selector;
        issues.push({
          id: collapsedID,
          type: 'layout',
          severity: 'critical',
          selector: selector,
          message: collapsedMsg,
          viewportWidth: viewportWidth
        });
        continue; // Skip other checks for collapsed elements
      }

      // Fixed elements covering viewport (mobile only)
      if (isMobile && style.position === 'fixed') {
        var coverage = rect.height / win.innerHeight;
        if (coverage > 0.25) {
          var fixedMsg = 'fixed element covers ' + Math.round(coverage * 100) + '% of viewport';
          var fixedID = computeFindingID('layout', selector, fixedMsg);
          findingSelectors[fixedID] = selector;
          issues.push({
            id: fixedID,
            type: 'layout',
            severity: coverage > 0.4 ? 'warning' : 'info',
            selector: selector,
            message: fixedMsg,
            viewportWidth: viewportWidth,
            details: {
              coverage: Math.round(coverage * 100) + '%',
              height: Math.round(rect.height),
              viewportHeight: win.innerHeight
            }
          });
        }
      }

      // Margin/padding squeeze
      var containerWidth = el.parentElement
        ? el.parentElement.getBoundingClientRect().width
        : viewportWidth;
      var totalSqueeze =
        parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) +
        parseFloat(style.marginLeft) + parseFloat(style.marginRight);

      if (totalSqueeze > rect.width * 0.3 && rect.width < 200) {
        var squeezeMsg = 'margins/padding squeeze content to ' + Math.round(rect.width) + 'px';
        var squeezeID = computeFindingID('layout', selector, squeezeMsg);
        findingSelectors[squeezeID] = selector;
        issues.push({
          id: squeezeID,
          type: 'layout',
          severity: 'warning',
          selector: selector,
          message: squeezeMsg,
          viewportWidth: viewportWidth,
          details: {
            contentWidth: Math.round(rect.width),
            containerWidth: Math.round(containerWidth),
            totalSqueeze: Math.round(totalSqueeze)
          }
        });
      }
    }

    return issues;
  }

  /**
   * Detect overflow issues in the given window context
   */
  function detectOverflowIssues(win, viewportWidth) {
    var doc = win.document;
    var issues = [];

    var elements = doc.querySelectorAll('*');
    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];

      // Skip hidden elements
      if (!isVisibleInContext(el, win)) {
        continue;
      }

      var rect = el.getBoundingClientRect();
      var style = win.getComputedStyle(el);
      var selector = generateSelectorInContext(el, win);

      // Horizontal overflow
      if (rect.right > viewportWidth + 5) {
        if (style.overflowX !== 'auto' && style.overflowX !== 'scroll') {
          var hScrollMsg = 'forces horizontal scroll +' + Math.round(rect.right - viewportWidth) + 'px';
          var hScrollID = computeFindingID('overflow', selector, hScrollMsg);
          findingSelectors[hScrollID] = selector;
          issues.push({
            id: hScrollID,
            type: 'overflow',
            severity: 'critical',
            selector: selector,
            message: hScrollMsg,
            viewportWidth: viewportWidth,
            details: {
              overflow: Math.round(rect.right - viewportWidth),
              elementRight: Math.round(rect.right),
              viewportWidth: viewportWidth
            }
          });
        }
      }

      // Content clipped by overflow:hidden
      if (style.overflow === 'hidden' || style.overflowX === 'hidden') {
        if (el.scrollWidth > el.clientWidth + 10) {
          var clippedMsg = 'content clipped by overflow:hidden';
          var clippedID = computeFindingID('overflow', selector, clippedMsg);
          findingSelectors[clippedID] = selector;
          issues.push({
            id: clippedID,
            type: 'overflow',
            severity: 'warning',
            selector: selector,
            message: clippedMsg,
            viewportWidth: viewportWidth,
            details: {
              scrollWidth: el.scrollWidth,
              clientWidth: el.clientWidth,
              clipped: el.scrollWidth - el.clientWidth
            }
          });
        }
      }

      // Truncated text without tooltip
      if (style.textOverflow === 'ellipsis' || style.textOverflow === 'clip') {
        if (el.scrollWidth > el.clientWidth && !el.title && !el.getAttribute('aria-label')) {
          var truncMsg = 'truncated text without title/tooltip';
          var truncID = computeFindingID('overflow', selector, truncMsg);
          findingSelectors[truncID] = selector;
          issues.push({
            id: truncID,
            type: 'overflow',
            severity: 'info',
            selector: selector,
            message: truncMsg,
            viewportWidth: viewportWidth
          });
        }
      }

      // Images hidden/squeezed by container
      if (el.tagName === 'IMG' && (rect.width < 10 || rect.height < 10)) {
        if (el.naturalWidth * el.naturalHeight > 1000) {
          var imgMsg = 'image squeezed or hidden by container';
          var imgID = computeFindingID('overflow', selector, imgMsg);
          findingSelectors[imgID] = selector;
          issues.push({
            id: imgID,
            type: 'overflow',
            severity: 'warning',
            selector: selector,
            message: imgMsg,
            viewportWidth: viewportWidth,
            details: {
              displayedWidth: Math.round(rect.width),
              displayedHeight: Math.round(rect.height),
              naturalWidth: el.naturalWidth,
              naturalHeight: el.naturalHeight
            }
          });
        }
      }
    }

    return issues;
  }

  /**
   * Filter general accessibility results for viewport relevance
   * Reuses existing auditAccessibility() results and filters for viewport-specific issues
   * @param {object} a11yResults - Results from auditAccessibility()
   * @param {number} viewportWidth - Current viewport width
   * @returns {array} Filtered issues relevant to this viewport
   */
  function filterViewportRelevant(a11yResults, viewportWidth) {
    if (!a11yResults) return [];

    var isMobile = viewportWidth < 768;
    var issues = [];
    var fixable = a11yResults.fixable || [];

    for (var i = 0; i < fixable.length; i++) {
      var issue = fixable[i];

      if (isMobile) {
        // On mobile, promote touch target and font size issues
        // Also include critical severity issues
        var isTouchTarget = issue.type && (
          issue.type.indexOf('touch') !== -1 ||
          issue.type.indexOf('target') !== -1
        );
        var isFontIssue = issue.message && (
          issue.message.indexOf('font-size') !== -1 ||
          issue.message.indexOf('zoom') !== -1
        );
        var isCritical = issue.severity === 'error' || issue.severity === 'critical';

        if (isTouchTarget || isFontIssue || isCritical) {
          var mobileMsg = issue.message;
          var mobileSel = issue.selector || '';
          var mobileID = computeFindingID('a11y', mobileSel, mobileMsg);
          findingSelectors[mobileID] = mobileSel;
          issues.push({
            id: mobileID,
            type: 'a11y',
            severity: issue.severity === 'error' ? 'critical' : issue.severity,
            selector: mobileSel,
            message: mobileMsg,
            viewportWidth: viewportWidth,
            wcag: issue.wcag || '',
            source: 'auditAccessibility'
          });
        }
      } else {
        // Desktop: filter out mobile-only issues
        var isMobileOnly = issue.type && (
          issue.type.indexOf('touch') !== -1 ||
          issue.type.indexOf('zoom') !== -1
        );

        if (!isMobileOnly) {
          var desktopMsg = issue.message;
          var desktopSel = issue.selector || '';
          var desktopID = computeFindingID('a11y', desktopSel, desktopMsg);
          findingSelectors[desktopID] = desktopSel;
          issues.push({
            id: desktopID,
            type: 'a11y',
            severity: issue.severity === 'error' ? 'critical' : issue.severity,
            selector: desktopSel,
            message: desktopMsg,
            viewportWidth: viewportWidth,
            wcag: issue.wcag || '',
            source: 'auditAccessibility'
          });
        }
      }
    }

    return issues;
  }

  /**
   * Detect viewport-specific accessibility issues (direct DOM scan)
   * Checks for issues not covered by standard accessibility audits:
   * - Touch target size (mobile)
   * - iOS zoom trigger (input font-size < 16px)
   * - Readability (font-size < 12px on mobile)
   */
  function detectViewportA11yIssues(win, viewportWidth) {
    var doc = win.document;
    var issues = [];
    var isMobile = viewportWidth < 768;

    // Only run viewport-specific checks on mobile
    if (!isMobile) {
      return issues;
    }

    var elements = doc.querySelectorAll('*');
    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];

      // Skip hidden elements
      if (!isVisibleInContext(el, win)) {
        continue;
      }

      var rect = el.getBoundingClientRect();
      var style = win.getComputedStyle(el);
      var selector = generateSelectorInContext(el, win);
      var tagName = el.tagName.toLowerCase();

      // Touch target size (mobile only)
      var isInteractive = (
        tagName === 'a' ||
        tagName === 'button' ||
        tagName === 'input' ||
        tagName === 'select' ||
        tagName === 'textarea' ||
        el.onclick ||
        el.getAttribute('role') === 'button' ||
        el.getAttribute('tabindex') !== null
      );

      if (isInteractive && rect.width > 0 && rect.height > 0) {
        var minSize = 44; // Apple HIG minimum
        if (rect.width < minSize || rect.height < minSize) {
          var touchMsg = 'touch target smaller than 44x44px minimum';
          var touchID = computeFindingID('a11y', selector, touchMsg);
          findingSelectors[touchID] = selector;
          issues.push({
            id: touchID,
            type: 'a11y',
            severity: 'warning',
            selector: selector,
            message: touchMsg,
            viewportWidth: viewportWidth,
            details: {
              width: Math.round(rect.width),
              height: Math.round(rect.height),
              recommended: '44x44px minimum'
            },
            source: 'viewport-scan'
          });
        }
      }

      // iOS zoom trigger (input font-size < 16px)
      if (tagName === 'input' || tagName === 'select' || tagName === 'textarea') {
        var fontSize = parseFloat(style.fontSize);
        if (fontSize < 16) {
          var zoomMsg = 'font-size ' + fontSize + 'px triggers iOS zoom (min 16px)';
          var zoomID = computeFindingID('a11y', selector, zoomMsg);
          findingSelectors[zoomID] = selector;
          issues.push({
            id: zoomID,
            type: 'a11y',
            severity: 'warning',
            selector: selector,
            message: zoomMsg,
            viewportWidth: viewportWidth,
            details: {
              fontSize: fontSize,
              recommended: '16px minimum to prevent zoom'
            },
            source: 'viewport-scan'
          });
        }
      }

      // Readability (font-size < 12px)
      if (rect.width > 0 && rect.height > 0) {
        var textFontSize = parseFloat(style.fontSize);
        if (textFontSize < 12) {
          var readMsg = 'font-size ' + textFontSize + 'px may be hard to read on mobile';
          var readID = computeFindingID('a11y', selector, readMsg);
          findingSelectors[readID] = selector;
          issues.push({
            id: readID,
            type: 'a11y',
            severity: 'info',
            selector: selector,
            message: readMsg,
            viewportWidth: viewportWidth,
            details: {
              fontSize: textFontSize,
              recommended: '14px minimum for body text'
            },
            source: 'viewport-scan'
          });
        }
      }
    }

    return issues;
  }

  /**
   * Run full accessibility check combining auditAccessibility with viewport-specific checks
   * @param {Window} win - Window context to audit
   * @param {number} viewportWidth - Current viewport width
   * @param {object} options - Audit options
   * @returns {Promise<array>} Combined accessibility issues
   */
  function runResponsiveA11yCheck(win, viewportWidth, options) {
    return new Promise(function(resolve) {
      var allIssues = [];
      var isMobile = viewportWidth < 768;

      // Run viewport-specific checks (touch targets, iOS zoom)
      var viewportIssues = detectViewportA11yIssues(win, viewportWidth);
      allIssues = allIssues.concat(viewportIssues);

      // Try to run existing accessibility audit and filter results
      if (win.__devtool_accessibility && win.__devtool_accessibility.auditAccessibility) {
        win.__devtool_accessibility.auditAccessibility({ mode: 'fast', useBasic: true })
          .then(function(a11yResults) {
            var filteredIssues = filterViewportRelevant(a11yResults, viewportWidth);
            allIssues = allIssues.concat(filteredIssues);
            resolve(allIssues);
          })
          .catch(function() {
            // If accessibility audit fails, return viewport issues only
            resolve(allIssues);
          });
      } else {
        resolve(allIssues);
      }
    });
  }

  /**
   * Check if element is visible in context
   */
  function isVisibleInContext(el, win) {
    if (!el || !win) return false;

    var style = win.getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') {
      return false;
    }

    var rect = el.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) {
      return false;
    }

    return true;
  }

  /**
   * Generate a stable selector in the given window context
   */
  function generateSelectorInContext(el, win) {
    // Try to use the utils from the iframe if available
    if (win.__devtool_utils && win.__devtool_utils.generateSelector) {
      return win.__devtool_utils.generateSelector(el);
    }

    // Fallback selector generation
    if (el.id) {
      return '#' + el.id;
    }

    var path = [];
    var current = el;

    while (current && current !== win.document) {
      var tag = current.tagName.toLowerCase();

      if (current.id) {
        path.unshift('#' + current.id);
        break;
      }

      var parent = current.parentElement;
      if (parent) {
        var siblings = parent.querySelectorAll(tag);
        if (siblings.length > 1) {
          var index = 1;
          for (var i = 0; i < siblings.length; i++) {
            if (siblings[i] === current) {
              break;
            }
            index++;
          }
          path.unshift(tag + ':nth-of-type(' + index + ')');
        } else {
          path.unshift(tag);
        }
      } else {
        path.unshift(tag);
      }

      current = parent;
    }

    return path.join(' > ');
  }

  /**
   * Run the full responsive audit across all viewports
   * @param {object} options - Audit options
   * @param {array} options.viewports - Custom viewports to test
   * @param {array} options.checks - Checks to run: ['layout', 'overflow', 'a11y']
   * @param {number} options.timeout - Load timeout per viewport (ms)
   * @param {boolean} options.raw - Return raw JSON instead of compact text
   * @returns {Promise<object>} Audit results
   */
  function audit(options) {
    options = options || {};

    var viewports = options.viewports || DEFAULT_VIEWPORTS;
    var checks = options.checks || ['layout', 'overflow', 'a11y'];
    var timeout = options.timeout || 10000;

    // Debounce: if an audit is already in progress, return the existing promise
    if (auditState.pending) {
      return auditState.pending.then(function(result) {
        // After pending resolves, check if we should return cached result
        var now = Date.now();
        if (auditState.lastResult && (now - auditState.lastTime) < DEBOUNCE_WINDOW) {
          // Options might differ for raw mode, so format accordingly
          if (options.raw) {
            return formatJSON(auditState.lastResult);
          }
          return formatCompactResults(auditState.lastResult);
        }
        // If cache is stale, run a new audit
        return runAuditInternal(viewports, checks, timeout, options);
      });
    }

    // Check if we have a recent cached result
    var now = Date.now();
    if (auditState.lastResult && (now - auditState.lastTime) < DEBOUNCE_WINDOW) {
      var cachedPromise = new Promise(function(resolve) {
        if (options.raw) {
          resolve(formatJSON(auditState.lastResult));
        } else {
          resolve(formatCompactResults(auditState.lastResult));
        }
      });
      return cachedPromise;
    }

    return runAuditInternal(viewports, checks, timeout, options);
  }

  /**
   * Internal audit execution (bypasses debouncing)
   */
  function runAuditInternal(viewports, checks, timeout, options) {
    var auditPromise = new Promise(function(resolve, reject) {
      var results = {
        viewports: {},
        summary: {
          total: 0,
          critical: 0,
          warning: 0,
          info: 0
        },
        patterns: {
          mobileOnly: 0,
          tabletOnly: 0,
          desktopOnly: 0,
          crossViewport: 0
        },
        timestamp: Date.now(),
        duration: 0
      };

      var startTime = Date.now();
      var viewportIndex = 0;

      // Sequential processing to avoid memory pressure
      function processNextViewport() {
        if (viewportIndex >= viewports.length) {
          // All done - calculate patterns
          calculatePatterns(results);
          results.duration = Date.now() - startTime;

          if (options.raw) {
            // Transform to match spec format for raw JSON output
            resolve(formatJSON(results));
          } else {
            resolve(formatCompactResults(results));
          }
          return;
        }

        var viewport = viewports[viewportIndex];
        viewportIndex++;

        auditViewport(viewport, { checks: checks, timeout: timeout })
          .then(function(viewportResult) {
            // Store viewport results
            results.viewports[viewport.name] = {
              width: viewport.width,
              height: viewport.height,
              issues: viewportResult.issues,
              issueCount: viewportResult.issueCount
            };

            // Update summary counts
            for (var i = 0; i < viewportResult.issues.length; i++) {
              var issue = viewportResult.issues[i];
              results.summary.total++;
              if (issue.severity === 'critical') {
                results.summary.critical++;
              } else if (issue.severity === 'warning') {
                results.summary.warning++;
              } else {
                results.summary.info++;
              }
            }

            // Process next viewport
            processNextViewport();
          })
          .catch(function(err) {
            // Store error for this viewport but continue
            results.viewports[viewport.name] = {
              width: viewport.width,
              height: viewport.height,
              error: err.message,
              issues: [],
              issueCount: 0
            };

            processNextViewport();
          });
      }

      // Start processing
      processNextViewport();
    });

    // Store pending promise and handle completion
    auditState.pending = auditPromise;

    return auditPromise.then(function(result) {
      // Cache result and clear pending state
      auditState.lastResult = result;
      auditState.lastTime = Date.now();
      auditState.pending = null;
      return result;
    }).catch(function(err) {
      // Clear pending on error
      auditState.pending = null;
      throw err;
    });
  }

  /**
   * Calculate cross-viewport patterns
   */
  function calculatePatterns(results) {
    var viewportNames = Object.keys(results.viewports);
    if (viewportNames.length < 2) return;

    // Count issues per selector across viewports
    var selectorOccurrences = {};

    viewportNames.forEach(function(name) {
      var viewport = results.viewports[name];
      if (viewport.issues) {
        viewport.issues.forEach(function(issue) {
          var selector = issue.selector;
          if (!selectorOccurrences[selector]) {
            selectorOccurrences[selector] = {};
          }
          if (!selectorOccurrences[selector][issue.message]) {
            selectorOccurrences[selector][issue.message] = [];
          }
          selectorOccurrences[selector][issue.message].push(name);
        });
      }
    });

    // Classify patterns
    Object.keys(selectorOccurrences).forEach(function(selector) {
      Object.keys(selectorOccurrences[selector]).forEach(function(message) {
        var viewports = selectorOccurrences[selector][message];
        if (viewports.length === viewportNames.length) {
          results.patterns.crossViewport++;
        } else if (viewports.indexOf('mobile') !== -1 && viewports.length === 1) {
          results.patterns.mobileOnly++;
        } else if (viewports.indexOf('tablet') !== -1 && viewports.length === 1) {
          results.patterns.tabletOnly++;
        } else if (viewports.indexOf('desktop') !== -1 && viewports.length === 1) {
          results.patterns.desktopOnly++;
        }
      });
    });
  }

  /**
   * Format results in compact text format
   * Output format optimized for AI consumption
   */
  function formatCompactResults(results) {
    var lines = [];
    var viewportNames = Object.keys(results.viewports);

    lines.push('=== Responsive Audit: ' + viewportNames.length + ' viewports ===');
    lines.push('');

    viewportNames.forEach(function(name) {
      var viewport = results.viewports[name];
      var issueCount = viewport.issueCount || 0;

      lines.push(name.toUpperCase() + ' (' + viewport.width + 'px) - ' + issueCount + ' issues');

      if (viewport.error) {
        lines.push('  ERROR: ' + viewport.error);
      } else if (viewport.issues && viewport.issues.length > 0) {
        viewport.issues.slice(0, 10).forEach(function(issue) {
          // Icons: ! = warning/critical, o = info
          // Design spec uses ⚠ and ○, but ASCII ensures terminal compatibility
          var icon = issue.severity === 'critical' || issue.severity === 'warning' ? '!' : 'o';
          // Single line format: icon [type] selector - message #id
          var idSuffix = issue.id ? ' #' + issue.id : '';
          lines.push('  ' + icon + ' [' + issue.type + '] ' + issue.selector + ' - ' + issue.message + idSuffix);
        });

        if (viewport.issues.length > 10) {
          lines.push('  ... and ' + (viewport.issues.length - 10) + ' more issues');
        }
      }

      lines.push('');
    });

    // Summary: total issues (critical, minor) where minor = warning + info
    var minorCount = results.summary.warning + results.summary.info;
    lines.push('SUMMARY: ' + results.summary.total + ' issues (' + results.summary.critical + ' critical, ' + minorCount + ' minor)');

    // Patterns line
    lines.push('PATTERNS: ' +
               results.patterns.mobileOnly + ' mobile-only, ' +
               results.patterns.tabletOnly + ' tablet-only, ' +
               results.patterns.crossViewport + ' cross-viewport');

    return lines.join('\n');
  }

  /**
   * Format results as JSON matching design spec
   * Transforms internal structure to spec format
   */
  function formatJSON(results) {
    var viewportNames = Object.keys(results.viewports);

    // Build viewports object with only width and issues
    var viewportsObj = {};
    viewportNames.forEach(function(name) {
      var viewport = results.viewports[name];
      viewportsObj[name] = {
        width: viewport.width,
        issues: viewport.issues || []
      };
    });

    return {
      viewports: viewportsObj,
      summary: {
        total: results.summary.total,
        critical: results.summary.critical,
        minor: results.summary.warning + results.summary.info
      },
      patterns: {
        mobileOnly: results.patterns.mobileOnly,
        tabletOnly: results.patterns.tabletOnly,
        crossViewport: results.patterns.crossViewport
      }
    };
  }

  /**
   * Highlight the element matching a finding by its ID.
   * Injects a CSS outline on the element for 3 seconds, then removes it.
   * Checks both this module's local registry and the shared window.__devtool.audit.findingSelectors
   * so findings from other audit modules (a11y, quality, css, dom) are also highlightable.
   * @param {string} id - 8-char hex finding ID from an audit issue
   * @returns {boolean} true if the element was found and highlighted, false otherwise
   */
  function highlight(id) {
    var selector = findingSelectors[id] || getSharedRegistry()[id];
    if (!selector) {
      return false;
    }

    var el = document.querySelector(selector);
    if (!el) {
      return false;
    }

    var styleTag = document.getElementById('__devtool_highlight_style');
    if (!styleTag) {
      styleTag = document.createElement('style');
      styleTag.id = '__devtool_highlight_style';
      document.head.appendChild(styleTag);
    }

    var className = '__devtool_highlight_' + id;
    styleTag.textContent += '\n.' + className + ' { outline: 3px solid #f50 !important; outline-offset: 2px !important; }';
    el.classList.add(className);

    setTimeout(function() {
      el.classList.remove(className);
    }, 3000);

    return true;
  }

  // Export module
  window.__devtool_responsive = {
    audit: audit,
    detectLayoutIssues: detectLayoutIssues,
    detectOverflowIssues: detectOverflowIssues,
    detectViewportA11yIssues: detectViewportA11yIssues,
    filterViewportRelevant: filterViewportRelevant,
    runResponsiveA11yCheck: runResponsiveA11yCheck,
    DEFAULT_VIEWPORTS: DEFAULT_VIEWPORTS
  };

  // Register highlight under window.__devtool.audit namespace
  if (!window.__devtool) {
    window.__devtool = {};
  }
  if (!window.__devtool.audit) {
    window.__devtool.audit = {};
  }
  window.__devtool.audit.highlight = highlight;
})();

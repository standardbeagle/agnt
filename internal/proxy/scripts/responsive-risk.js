// Responsive risk analysis for DevTool
// Detects elements at risk of layout issues at different viewport sizes

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Standard breakpoints to test
  var BREAKPOINTS = [320, 375, 414, 768, 1024, 1280, 1440, 1920];

  /**
   * Check if an element has fixed dimensions that may cause issues
   */
  function checkFixedDimensions(element) {
    var computed = window.getComputedStyle(element);
    var issues = [];
    var rect = element.getBoundingClientRect();

    // Check for fixed width in pixels
    var width = computed.width;
    var hasFixedWidth = width && width.endsWith('px') && parseFloat(width) > 0;
    var widthValue = parseFloat(width) || 0;

    // Check for min-width that's too large
    var minWidth = parseFloat(computed.minWidth) || 0;

    // Check for elements wider than common mobile viewports
    if (hasFixedWidth && widthValue > 320) {
      issues.push({
        type: 'fixed-width',
        severity: widthValue > 768 ? 'error' : 'warning',
        message: 'Fixed width (' + Math.round(widthValue) + 'px) may cause horizontal scroll on mobile',
        details: {
          width: Math.round(widthValue) + 'px',
          breakpointsAffected: BREAKPOINTS.filter(function(bp) { return bp < widthValue; })
        }
      });
    }

    if (minWidth > 320) {
      issues.push({
        type: 'min-width-too-large',
        severity: minWidth > 768 ? 'error' : 'warning',
        message: 'min-width (' + Math.round(minWidth) + 'px) may cause horizontal scroll',
        details: {
          minWidth: Math.round(minWidth) + 'px',
          breakpointsAffected: BREAKPOINTS.filter(function(bp) { return bp < minWidth; })
        }
      });
    }

    // Check for elements that extend beyond viewport
    if (rect.width > window.innerWidth) {
      issues.push({
        type: 'exceeds-viewport',
        severity: 'error',
        message: 'Element exceeds current viewport width',
        details: {
          elementWidth: Math.round(rect.width) + 'px',
          viewportWidth: window.innerWidth + 'px',
          overflow: Math.round(rect.width - window.innerWidth) + 'px'
        }
      });
    }

    return issues;
  }

  /**
   * Check for small touch targets
   */
  function checkTouchTargets(element) {
    var issues = [];
    var tagName = element.tagName.toLowerCase();

    // Only check interactive elements
    var isInteractive = (
      tagName === 'a' ||
      tagName === 'button' ||
      tagName === 'input' ||
      tagName === 'select' ||
      tagName === 'textarea' ||
      element.onclick ||
      element.getAttribute('role') === 'button' ||
      element.getAttribute('tabindex') !== null
    );

    if (!isInteractive) return issues;

    var rect = element.getBoundingClientRect();
    var minSize = 44; // Apple HIG minimum touch target

    if (rect.width > 0 && rect.height > 0) {
      if (rect.width < minSize || rect.height < minSize) {
        issues.push({
          type: 'small-touch-target',
          severity: 'warning',
          message: 'Touch target smaller than 44x44px minimum',
          details: {
            width: Math.round(rect.width) + 'px',
            height: Math.round(rect.height) + 'px',
            recommended: '44x44px minimum'
          }
        });
      }
    }

    return issues;
  }

  /**
   * Check for horizontal scroll containers
   */
  function checkHorizontalScroll(element) {
    var issues = [];
    var computed = window.getComputedStyle(element);

    var hasHorizontalScroll = element.scrollWidth > element.clientWidth;
    var overflowX = computed.overflowX;

    // Intentional horizontal scroll (carousel, etc.) is ok
    var isIntentional = overflowX === 'scroll' || overflowX === 'auto';

    if (hasHorizontalScroll && !isIntentional) {
      issues.push({
        type: 'unintended-horizontal-scroll',
        severity: 'error',
        message: 'Element causes horizontal scroll without overflow-x setting',
        details: {
          scrollWidth: element.scrollWidth + 'px',
          clientWidth: element.clientWidth + 'px',
          overflow: (element.scrollWidth - element.clientWidth) + 'px'
        }
      });
    }

    return issues;
  }

  /**
   * Check for absolute/fixed positioning issues
   */
  function checkPositioning(element) {
    var issues = [];
    var computed = window.getComputedStyle(element);
    var position = computed.position;

    if (position === 'absolute' || position === 'fixed') {
      var left = parseFloat(computed.left);
      var right = parseFloat(computed.right);
      var rect = element.getBoundingClientRect();

      // Check if positioned element could go offscreen
      if (!isNaN(left) && left > 0 && rect.right > window.innerWidth) {
        issues.push({
          type: 'positioned-offscreen-right',
          severity: 'warning',
          message: position + ' positioned element extends past right edge',
          details: {
            position: position,
            left: computed.left,
            elementRight: Math.round(rect.right) + 'px',
            viewportWidth: window.innerWidth + 'px'
          }
        });
      }

      // Check for fixed elements that may overlap on small screens
      if (position === 'fixed') {
        var zIndex = parseInt(computed.zIndex) || 0;
        if (zIndex > 1000 && rect.height > 100) {
          issues.push({
            type: 'large-fixed-element',
            severity: 'warning',
            message: 'Large fixed element may obscure content on mobile',
            details: {
              height: Math.round(rect.height) + 'px',
              zIndex: zIndex,
              percentOfViewport: Math.round((rect.height / window.innerHeight) * 100) + '%'
            }
          });
        }
      }
    }

    return issues;
  }

  /**
   * Check for text sizing issues
   */
  function checkTextSizing(element) {
    var issues = [];
    var computed = window.getComputedStyle(element);
    var fontSize = parseFloat(computed.fontSize);

    // Check for very small font size
    if (fontSize < 12) {
      issues.push({
        type: 'small-font',
        severity: 'warning',
        message: 'Font size (' + fontSize + 'px) may be hard to read on mobile',
        details: {
          fontSize: fontSize + 'px',
          recommended: '14px minimum for body text'
        }
      });
    }

    // Check for viewport units on font-size without clamp
    var fontSizeValue = computed.fontSize;
    if (fontSizeValue && (fontSizeValue.includes('vw') || fontSizeValue.includes('vh'))) {
      // This would be set via CSS, harder to detect
      // For now, just note elements with very large or small font sizes
      if (fontSize > 48 || fontSize < 10) {
        issues.push({
          type: 'extreme-font-size',
          severity: 'warning',
          message: 'Extreme font size may cause issues at different viewports',
          details: {
            fontSize: fontSize + 'px'
          }
        });
      }
    }

    return issues;
  }

  /**
   * Check for table layout issues
   */
  function checkTableLayout(element) {
    var issues = [];

    if (element.tagName.toLowerCase() === 'table') {
      var rect = element.getBoundingClientRect();
      var computed = window.getComputedStyle(element);

      // Table wider than viewport
      if (rect.width > window.innerWidth) {
        issues.push({
          type: 'wide-table',
          severity: 'error',
          message: 'Table wider than viewport - consider responsive table pattern',
          details: {
            tableWidth: Math.round(rect.width) + 'px',
            viewportWidth: window.innerWidth + 'px',
            columns: element.rows && element.rows[0] ? element.rows[0].cells.length : 'unknown'
          }
        });
      }

      // Table without responsive wrapper
      var parent = element.parentElement;
      var parentOverflow = parent ? window.getComputedStyle(parent).overflowX : '';
      if (rect.width > 400 && parentOverflow !== 'auto' && parentOverflow !== 'scroll') {
        issues.push({
          type: 'table-not-scrollable',
          severity: 'warning',
          message: 'Wide table without horizontal scroll wrapper',
          details: {
            width: Math.round(rect.width) + 'px',
            suggestion: 'Wrap in container with overflow-x: auto'
          }
        });
      }
    }

    return issues;
  }

  /**
   * Main responsive risk check function
   */
  function checkResponsiveRisk() {
    try {
      var elements = document.body.querySelectorAll('*');
      var issues = [];
      var summary = {
        total: 0,
        errors: 0,
        warnings: 0,
        elementsAnalyzed: elements.length
      };

      for (var i = 0; i < elements.length; i++) {
        var element = elements[i];

        // Skip hidden elements
        if (element.offsetParent === null && element.tagName.toLowerCase() !== 'fixed') {
          continue;
        }

        var selector = utils.generateSelector(element);
        var elementIssues = [];

        // Run all checks
        elementIssues = elementIssues.concat(checkFixedDimensions(element));
        elementIssues = elementIssues.concat(checkTouchTargets(element));
        elementIssues = elementIssues.concat(checkHorizontalScroll(element));
        elementIssues = elementIssues.concat(checkPositioning(element));
        elementIssues = elementIssues.concat(checkTextSizing(element));
        elementIssues = elementIssues.concat(checkTableLayout(element));

        if (elementIssues.length > 0) {
          issues.push({
            selector: selector,
            tagName: element.tagName.toLowerCase(),
            issues: elementIssues
          });

          elementIssues.forEach(function(issue) {
            summary.total++;
            if (issue.severity === 'error') {
              summary.errors++;
            } else {
              summary.warnings++;
            }
          });
        }
      }

      // Sort by severity (errors first)
      issues.sort(function(a, b) {
        var aHasError = a.issues.some(function(i) { return i.severity === 'error'; });
        var bHasError = b.issues.some(function(i) { return i.severity === 'error'; });
        if (aHasError && !bHasError) return -1;
        if (!aHasError && bHasError) return 1;
        return b.issues.length - a.issues.length;
      });

      return {
        issues: issues,
        summary: summary,
        currentViewport: {
          width: window.innerWidth,
          height: window.innerHeight
        },
        breakpointsTested: BREAKPOINTS
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  // Export
  window.__devtool_responsive_risk = {
    checkResponsiveRisk: checkResponsiveRisk,
    checkFixedDimensions: checkFixedDimensions,
    checkTouchTargets: checkTouchTargets,
    checkHorizontalScroll: checkHorizontalScroll,
    checkPositioning: checkPositioning,
    checkTextSizing: checkTextSizing,
    checkTableLayout: checkTableLayout
  };
})();

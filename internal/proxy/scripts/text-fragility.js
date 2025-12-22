// Text fragility analysis for DevTool
// Detects text elements at risk of overflow, truncation, or layout shifts

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Standard breakpoints to test
  var BREAKPOINTS = [320, 375, 414, 768, 1024, 1280, 1440, 1920];

  /**
   * Get all text-containing elements on the page
   */
  function getTextElements() {
    var elements = [];
    var walker = document.createTreeWalker(
      document.body,
      NodeFilter.SHOW_ELEMENT,
      {
        acceptNode: function(node) {
          // Skip script, style, and hidden elements
          if (node.tagName === 'SCRIPT' || node.tagName === 'STYLE' ||
              node.tagName === 'NOSCRIPT' || node.tagName === 'SVG') {
            return NodeFilter.FILTER_REJECT;
          }
          // Check if element has direct text content
          var hasText = Array.from(node.childNodes).some(function(child) {
            return child.nodeType === Node.TEXT_NODE && child.textContent.trim().length > 0;
          });
          if (hasText) {
            return NodeFilter.FILTER_ACCEPT;
          }
          return NodeFilter.FILTER_SKIP;
        }
      }
    );

    var node;
    while ((node = walker.nextNode())) {
      elements.push(node);
    }
    return elements;
  }

  /**
   * Get the longest word in an element's text content
   */
  function getLongestWord(element) {
    var text = '';
    Array.from(element.childNodes).forEach(function(child) {
      if (child.nodeType === Node.TEXT_NODE) {
        text += child.textContent;
      }
    });
    var words = text.split(/\s+/).filter(function(w) { return w.length > 0; });
    if (words.length === 0) return { word: '', length: 0 };

    var longest = words.reduce(function(a, b) {
      return a.length > b.length ? a : b;
    });
    return { word: longest, length: longest.length };
  }

  /**
   * Measure the pixel width of text using a hidden span
   */
  function measureTextWidth(text, element) {
    var span = document.createElement('span');
    var computed = window.getComputedStyle(element);

    span.style.cssText = [
      'position: absolute',
      'visibility: hidden',
      'white-space: nowrap',
      'font-family: ' + computed.fontFamily,
      'font-size: ' + computed.fontSize,
      'font-weight: ' + computed.fontWeight,
      'font-style: ' + computed.fontStyle,
      'letter-spacing: ' + computed.letterSpacing,
      'text-transform: ' + computed.textTransform
    ].join(';');

    span.textContent = text;
    document.body.appendChild(span);
    var width = span.offsetWidth;
    document.body.removeChild(span);
    return width;
  }

  /**
   * Check if element has text overflow issues
   */
  function checkTextOverflow(element) {
    var computed = window.getComputedStyle(element);
    var issues = [];

    // Check for truncation settings
    var hasEllipsis = computed.textOverflow === 'ellipsis';
    var hasNowrap = computed.whiteSpace === 'nowrap';
    var hasHiddenOverflow = computed.overflow === 'hidden' ||
                           computed.overflowX === 'hidden';

    // Check actual overflow
    var isOverflowing = element.scrollWidth > element.clientWidth;
    var isVerticallyOverflowing = element.scrollHeight > element.clientHeight;

    // Check if text is actually truncated
    var isTruncated = hasEllipsis && hasHiddenOverflow && isOverflowing;

    if (isTruncated) {
      issues.push({
        type: 'truncated',
        severity: 'warning',
        message: 'Text is truncated with ellipsis',
        details: {
          scrollWidth: element.scrollWidth,
          clientWidth: element.clientWidth,
          overflow: Math.round(element.scrollWidth - element.clientWidth) + 'px'
        }
      });
    }

    if (isOverflowing && !hasHiddenOverflow) {
      issues.push({
        type: 'horizontal-overflow',
        severity: 'error',
        message: 'Text overflows container horizontally',
        details: {
          scrollWidth: element.scrollWidth,
          clientWidth: element.clientWidth,
          overflow: Math.round(element.scrollWidth - element.clientWidth) + 'px'
        }
      });
    }

    if (isVerticallyOverflowing && computed.overflowY !== 'scroll' && computed.overflowY !== 'auto') {
      issues.push({
        type: 'vertical-overflow',
        severity: 'error',
        message: 'Text overflows container vertically',
        details: {
          scrollHeight: element.scrollHeight,
          clientHeight: element.clientHeight,
          overflow: Math.round(element.scrollHeight - element.clientHeight) + 'px'
        }
      });
    }

    return issues;
  }

  /**
   * Calculate minimum width needed for longest word
   */
  function getMinWidthForLongestWord(element) {
    var longestWord = getLongestWord(element);
    if (longestWord.length === 0) return { width: 0, word: '' };

    var width = measureTextWidth(longestWord.word, element);
    var computed = window.getComputedStyle(element);

    // Add padding
    var paddingLeft = parseFloat(computed.paddingLeft) || 0;
    var paddingRight = parseFloat(computed.paddingRight) || 0;

    return {
      width: Math.ceil(width + paddingLeft + paddingRight),
      word: longestWord.word,
      wordLength: longestWord.length
    };
  }

  /**
   * Find breakpoints where text would cause issues
   */
  function findProblematicBreakpoints(element) {
    var minWidth = getMinWidthForLongestWord(element);
    if (minWidth.width === 0) return [];

    var problematic = [];
    var elementWidth = element.clientWidth;

    BREAKPOINTS.forEach(function(bp) {
      // Estimate element width at this breakpoint
      // This is a simplification - actual width depends on layout
      var ratio = bp / window.innerWidth;
      var estimatedWidth = elementWidth * ratio;

      if (estimatedWidth < minWidth.width) {
        problematic.push({
          breakpoint: bp,
          estimatedWidth: Math.round(estimatedWidth),
          requiredWidth: minWidth.width,
          deficit: Math.round(minWidth.width - estimatedWidth)
        });
      }
    });

    return problematic;
  }

  /**
   * Check for layout shift risk factors
   */
  function checkLayoutShiftRisk(element) {
    var computed = window.getComputedStyle(element);
    var risks = [];

    // Check for auto height with dynamic content potential
    var hasAutoHeight = computed.height === 'auto' || !computed.height;
    var hasMinHeight = computed.minHeight && computed.minHeight !== '0px';
    var hasMaxHeight = computed.maxHeight && computed.maxHeight !== 'none';

    // Elements with auto height and no constraints are shift risks
    if (hasAutoHeight && !hasMinHeight && !hasMaxHeight) {
      var lineHeight = parseFloat(computed.lineHeight) || parseFloat(computed.fontSize) * 1.2;
      var lines = Math.ceil(element.scrollHeight / lineHeight);

      if (lines > 1) {
        risks.push({
          type: 'multi-line-auto-height',
          severity: 'warning',
          message: 'Multi-line text with auto height - content changes may cause layout shift',
          details: {
            estimatedLines: lines,
            lineHeight: Math.round(lineHeight) + 'px'
          }
        });
      }
    }

    // Check for word-break or overflow-wrap settings
    var hasWordBreak = computed.wordBreak === 'break-all' || computed.wordBreak === 'break-word';
    var hasOverflowWrap = computed.overflowWrap === 'break-word' || computed.overflowWrap === 'anywhere';

    if (!hasWordBreak && !hasOverflowWrap) {
      var minWidth = getMinWidthForLongestWord(element);
      if (minWidth.wordLength > 15) {
        risks.push({
          type: 'long-word-no-break',
          severity: 'warning',
          message: 'Long word (' + minWidth.wordLength + ' chars) without word-break may overflow',
          details: {
            word: minWidth.word.substring(0, 20) + (minWidth.word.length > 20 ? '...' : ''),
            minWidthNeeded: minWidth.width + 'px'
          }
        });
      }
    }

    return risks;
  }

  /**
   * Main text fragility check function
   */
  function checkTextFragility() {
    try {
      var elements = getTextElements();
      var issues = [];
      var summary = {
        total: 0,
        errors: 0,
        warnings: 0,
        elementsAnalyzed: elements.length
      };

      elements.forEach(function(element) {
        var selector = utils.generateSelector(element);
        var elementIssues = [];

        // Check for overflow issues
        var overflowIssues = checkTextOverflow(element);
        elementIssues = elementIssues.concat(overflowIssues);

        // Check for layout shift risks
        var shiftRisks = checkLayoutShiftRisk(element);
        elementIssues = elementIssues.concat(shiftRisks);

        // Find problematic breakpoints
        var breakpointIssues = findProblematicBreakpoints(element);

        if (elementIssues.length > 0 || breakpointIssues.length > 0) {
          var longestWord = getLongestWord(element);
          var minWidth = getMinWidthForLongestWord(element);

          issues.push({
            selector: selector,
            text: element.textContent.substring(0, 50).trim() +
                  (element.textContent.length > 50 ? '...' : ''),
            longestWord: {
              word: longestWord.word.substring(0, 30) +
                    (longestWord.word.length > 30 ? '...' : ''),
              length: longestWord.length,
              minWidthPx: minWidth.width
            },
            issues: elementIssues,
            problematicBreakpoints: breakpointIssues
          });

          elementIssues.forEach(function(issue) {
            summary.total++;
            if (issue.severity === 'error') {
              summary.errors++;
            } else {
              summary.warnings++;
            }
          });

          // Count breakpoint issues
          if (breakpointIssues.length > 0) {
            summary.total++;
            summary.warnings++;
          }
        }
      });

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
        breakpointsTested: BREAKPOINTS
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  // Export
  window.__devtool_text_fragility = {
    checkTextFragility: checkTextFragility,
    getTextElements: getTextElements,
    getLongestWord: getLongestWord,
    measureTextWidth: measureTextWidth,
    getMinWidthForLongestWord: getMinWidthForLongestWord
  };
})();

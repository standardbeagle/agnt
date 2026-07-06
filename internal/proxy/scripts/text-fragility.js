// Text fragility analysis for DevTool
// Detects text elements at risk of overflow, truncation, or layout shifts

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Default detection thresholds; override per-call via options.thresholds.
  var DEFAULT_THRESHOLDS = {
    longWordChars: 15   // longest word above this length without word-break -> risk
  };

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
   * Measure the pixel width of text using a shared offscreen canvas.
   * canvas measureText avoids the old hidden-span DOM probe (append/measure/
   * remove per call forced style+layout work); results are memoized per
   * font-string + text.
   */
  var measureCtx = null;
  var measureCache = {};
  var measureCacheSize = 0;
  var MEASURE_CACHE_MAX = 2000;

  function getMeasureCtx() {
    if (!measureCtx) {
      var canvas = document.createElement('canvas');
      measureCtx = canvas.getContext('2d');
    }
    return measureCtx;
  }

  function applyTextTransform(text, transform) {
    if (transform === 'uppercase') return text.toUpperCase();
    if (transform === 'lowercase') return text.toLowerCase();
    if (transform === 'capitalize') {
      return text.replace(/\b\w/g, function(c) { return c.toUpperCase(); });
    }
    return text;
  }

  function measureTextWidth(text, element) {
    var computed = window.getComputedStyle(element);
    var font = [
      computed.fontStyle,
      computed.fontWeight,
      computed.fontSize,
      computed.fontFamily
    ].join(' ');

    var rendered = applyTextTransform(text, computed.textTransform);
    var letterSpacing = parseFloat(computed.letterSpacing) || 0;
    var cacheKey = font + '\x00' + letterSpacing + '\x00' + rendered;

    var cached = measureCache[cacheKey];
    if (cached !== undefined) return cached;

    var ctx = getMeasureCtx();
    if (!ctx) return 0;
    ctx.font = font;
    var width = ctx.measureText(rendered).width;
    // canvas measureText does not apply letter-spacing; add it per gap.
    if (letterSpacing && rendered.length > 1) {
      width += letterSpacing * (rendered.length - 1);
    }
    width = Math.ceil(width);

    if (measureCacheSize < MEASURE_CACHE_MAX) {
      measureCache[cacheKey] = width;
      measureCacheSize++;
    }
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

    // Text clipped by overflow:hidden without ellipsis - silent content loss
    var isClipped = isOverflowing && hasHiddenOverflow && !hasEllipsis;
    if (isClipped) {
      issues.push({
        type: 'clipped',
        severity: 'error',
        message: 'Text clipped by overflow:hidden without ellipsis - content is silently lost',
        details: {
          scrollWidth: element.scrollWidth,
          clientWidth: element.clientWidth,
          hiddenContent: Math.round(element.scrollWidth - element.clientWidth) + 'px',
          suggestion: 'Add text-overflow: ellipsis, or allow text to wrap, or increase container width'
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

    // Check for vertical clipping vs overflow
    var hasVerticalHiddenOverflow = computed.overflow === 'hidden' || computed.overflowY === 'hidden';
    var isVerticallyClipped = isVerticallyOverflowing && hasVerticalHiddenOverflow;

    if (isVerticallyClipped) {
      issues.push({
        type: 'vertical-clipped',
        severity: 'error',
        message: 'Text clipped vertically by overflow:hidden - content is silently lost',
        details: {
          scrollHeight: element.scrollHeight,
          clientHeight: element.clientHeight,
          hiddenContent: Math.round(element.scrollHeight - element.clientHeight) + 'px',
          suggestion: 'Increase container height, use max-height with overflow:auto, or use line-clamp'
        }
      });
    } else if (isVerticallyOverflowing && computed.overflowY !== 'scroll' && computed.overflowY !== 'auto') {
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
   * Calculate minimum width needed for longest word.
   * Memoized per element (WeakMap) — the main pass and the layout-shift check
   * both need it for the same element within one run.
   */
  var minWidthCache = typeof WeakMap !== 'undefined' ? new WeakMap() : null;

  function getMinWidthForLongestWord(element) {
    if (minWidthCache) {
      var hit = minWidthCache.get(element);
      if (hit) return hit;
    }

    var longestWord = getLongestWord(element);
    var result;
    if (longestWord.length === 0) {
      result = { width: 0, word: '' };
    } else {
      var width = measureTextWidth(longestWord.word, element);
      var computed = window.getComputedStyle(element);

      // Add padding
      var paddingLeft = parseFloat(computed.paddingLeft) || 0;
      var paddingRight = parseFloat(computed.paddingRight) || 0;

      result = {
        width: Math.ceil(width + paddingLeft + paddingRight),
        word: longestWord.word,
        wordLength: longestWord.length
      };
    }

    if (minWidthCache) minWidthCache.set(element, result);
    return result;
  }

  // NOTE: the old findProblematicBreakpoints() heuristic was removed. It
  // estimated element width at each breakpoint by linearly scaling the current
  // width with viewport ratio — real layouts (fixed sidebars, breakpoint media
  // queries, wrapping flex/grid) do not scale linearly, so its output was
  // fabricated data. Use the responsive audit for real per-viewport rendering.

  /**
   * Check for layout shift risk factors
   */
  function checkLayoutShiftRisk(element, th) {
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
      var longWordLimit = (th && th.longWordChars) || DEFAULT_THRESHOLDS.longWordChars;
      var minWidth = getMinWidthForLongestWord(element);
      if (minWidth.wordLength > longWordLimit) {
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
   * Options:
   *   thresholds: object - shallow-merged over DEFAULT_THRESHOLDS
   */
  function checkTextFragility(options) {
    try {
      options = options || {};
      var auditUtils = window.__devtool_audit_utils;
      var th = auditUtils && auditUtils.mergeThresholds
        ? auditUtils.mergeThresholds(DEFAULT_THRESHOLDS, options.thresholds)
        : DEFAULT_THRESHOLDS;

      // Reset the per-element min-width memo each run — text content may have
      // changed since the last audit.
      if (typeof WeakMap !== 'undefined') minWidthCache = new WeakMap();

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
        var shiftRisks = checkLayoutShiftRisk(element, th);
        elementIssues = elementIssues.concat(shiftRisks);

        if (elementIssues.length > 0) {
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
        note: 'per-breakpoint width estimation removed (linear viewport scaling was unsound) — use the responsive audit for real per-viewport checks'
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

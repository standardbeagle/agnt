// CSS audit

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Shared audit helpers (audit-utils.js): stable FNV-1a finding ids + the
  // shared window.__devtool.audit.findingSelectors highlight registry + the
  // canonical A-F grade scale.
  function computeFindingID(type, selector, message) {
    return window.__devtool_audit_utils.computeFindingID(type, selector, message);
  }

  function registerFinding(id, selector) {
    window.__devtool_audit_utils.registerFinding(id, selector);
  }

  // Default detection thresholds; override per-call via options.thresholds.
  var DEFAULT_THRESHOLDS = {
    zIndex: 100,      // computed z-index above this -> z-index-inflation
    patternMin: 3     // identical inline-style occurrences at/above this -> extract-to-class
  };

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  //   thresholds: object - shallow-merged over DEFAULT_THRESHOLDS
  function auditCSS(options) {
    options = options || {};
    var auditUtils = window.__devtool_audit_utils;
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var raw = options.raw === true; // Default: false (AI-optimized format)
    var TH = auditUtils.mergeThresholds(DEFAULT_THRESHOLDS, options.thresholds);

    var checksRun = [
      'inline-style-patterns',
      'important-declarations',
      'hardcoded-colors',
      'hardcoded-sizes',
      'z-index-inflation',
      'layout-issues',
      'css-variables',
      'vendor-prefixes'
    ];

    // Metrics tracking
    var metrics = {
      inlineStyleCount: 0,
      importantCount: 0,
      stylesheetCount: document.styleSheets.length,
      inaccessibleStylesheets: 0,
      cssVariableUsage: 0,
      hardcodedColors: 0,
      hardcodedSizes: 0
    };

    var fixable = [];
    var informational = [];
    var patterns = [];
    var categoryBreakdown = {
      layout: 0,
      visual: 0,
      typography: 0,
      animation: 0
    };

    // --- Helper functions ---

    // Normalize inline style string for pattern matching
    function normalizeStyle(styleStr) {
      return styleStr
        .replace(/\s+/g, ' ')
        .replace(/;\s*$/, '')
        .replace(/:\s+/g, ': ')
        .trim()
        .toLowerCase();
    }

    // Parse inline style into property map
    function parseInlineStyle(styleStr) {
      var props = {};
      var declarations = styleStr.split(';');
      for (var i = 0; i < declarations.length; i++) {
        var decl = declarations[i].trim();
        if (!decl) continue;
        var colonIndex = decl.indexOf(':');
        if (colonIndex === -1) continue;
        var prop = decl.substring(0, colonIndex).trim();
        var value = decl.substring(colonIndex + 1).trim();
        props[prop] = value;
      }
      return props;
    }

    // Categorize CSS property
    function categorizeProperty(prop) {
      var layoutProps = ['display', 'flex', 'grid', 'position', 'top', 'right', 'bottom', 'left',
                        'margin', 'padding', 'width', 'height', 'max-width', 'min-width',
                        'max-height', 'min-height', 'float', 'clear', 'overflow', 'z-index',
                        'align-items', 'justify-content', 'align-self', 'flex-direction',
                        'flex-wrap', 'gap', 'grid-template', 'grid-column', 'grid-row'];
      var visualProps = ['color', 'background', 'background-color', 'background-image',
                        'border', 'border-radius', 'box-shadow', 'opacity', 'visibility'];
      var typographyProps = ['font', 'font-size', 'font-family', 'font-weight', 'line-height',
                            'text-align', 'text-decoration', 'text-transform', 'letter-spacing'];
      var animationProps = ['transition', 'animation', 'transform'];

      if (layoutProps.indexOf(prop) !== -1) return 'layout';
      if (visualProps.indexOf(prop) !== -1) return 'visual';
      if (typographyProps.indexOf(prop) !== -1) return 'typography';
      if (animationProps.indexOf(prop) !== -1) return 'animation';
      return 'other';
    }

    // Check if value is a hardcoded color (hex, rgb, rgba, named colors)
    function isHardcodedColor(value) {
      return /^#[0-9a-f]{3,8}$/i.test(value) ||
             /^rgba?\(/.test(value) ||
             /^hsla?\(/.test(value) ||
             /^(red|blue|green|yellow|white|black|gray|grey|orange|purple|pink|brown)$/i.test(value);
    }

    // Check if value uses CSS variable
    function usesCSSVariable(value) {
      return /var\(--/.test(value);
    }

    // Check if value is hardcoded px size
    function isHardcodedPxSize(value) {
      return /^\d+px$/.test(value);
    }

    // Generate suggested class name from pattern
    function suggestClassName(styleStr) {
      var props = parseInlineStyle(styleStr);
      var keys = Object.keys(props);

      // Common patterns
      if (props.display === 'flex' && props['justify-content'] === 'center') {
        if (props['align-items'] === 'center') return 'flex-center';
        return 'flex-justify-center';
      }
      if (props.margin === '0 auto') return 'mx-auto';
      if (props.display === 'flex' && props['flex-direction'] === 'column') return 'flex-col';
      if (props.display === 'grid') return 'grid-container';
      if (props.position === 'absolute') return 'absolute';
      if (props.position === 'relative') return 'relative';

      // Generic based on primary property
      if (keys.length === 1) {
        return keys[0].replace(/[^a-z0-9]/gi, '-');
      }

      return 'utility-' + keys.length + 'props';
    }

    // Compact selector for a flagged element (first class or tag name).
    function shortSelector(elem) {
      var selector = elem.tagName.toLowerCase();
      if (elem.className && typeof elem.className === 'string') {
        var classes = elem.className.split(' ').filter(function(c) { return c; });
        if (classes.length > 0) {
          selector = '.' + classes[0];
        }
      }
      return selector;
    }

    // --- SINGLE ELEMENT PASS ---
    // One walk over the DOM covers what used to be four separate full scans:
    // inline-style pattern extraction, hardcoded color collection, fixed
    // dimension (layout) checks, and the computed z-index inflation scan.

    var stylePatterns = {};
    var elementsByPattern = {};
    var colorPatterns = {};
    var zIndexCount = 0;
    var layoutIssueCount = 0;

    var allElements = document.querySelectorAll('*');
    for (var i = 0; i < allElements.length; i++) {
      var elem = allElements[i];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(elem)) continue;

      // -- Inline style analysis (pattern / colors / sizes / layout) --
      var styleAttr = elem.getAttribute('style');
      if (styleAttr) {
        metrics.inlineStyleCount++;

        var normalized = normalizeStyle(styleAttr);
        if (normalized) {
          // Track pattern occurrences
          if (!stylePatterns[normalized]) {
            stylePatterns[normalized] = 0;
            elementsByPattern[normalized] = [];
          }
          stylePatterns[normalized]++;
          elementsByPattern[normalized].push(elem);

          // Categorize properties + collect color/size/variable metrics
          var props = parseInlineStyle(styleAttr);
          for (var prop in props) {
            if (!props.hasOwnProperty(prop)) continue;
            var value = props[prop];
            var category = categorizeProperty(prop);
            if (category !== 'other') {
              categoryBreakdown[category]++;
            }

            if (isHardcodedColor(value) && !usesCSSVariable(value)) {
              metrics.hardcodedColors++;
              var normColor = value.toLowerCase();
              colorPatterns[normColor] = (colorPatterns[normColor] || 0) + 1;
            }

            if (isHardcodedPxSize(value)) {
              metrics.hardcodedSizes++;
            }

            if (usesCSSVariable(value)) {
              metrics.cssVariableUsage++;
            }
          }

          // Fixed width/height layout issue (capped at 5 findings)
          if (layoutIssueCount < 5 &&
              ((props.width && /^\d+px$/.test(props.width)) ||
               (props.height && /^\d+px$/.test(props.height)))) {
            var fixedSelector = shortSelector(elem);
            var fixedMsg = 'fixed dimensions width:' + (props.width || 'auto') + ' height:' + (props.height || 'auto');
            var fixedID = computeFindingID('fixed-dimensions', fixedSelector, fixedMsg);
            registerFinding(fixedID, fixedSelector);
            fixable.push({
              id: fixedID,
              type: 'fixed-dimensions',
              severity: 'info',
              selector: fixedSelector,
              width: props.width,
              height: props.height,
              impact: 3,
              fix: 'Use relative units (%, rem, em) or max-width/max-height for responsiveness'
            });
            layoutIssueCount++;
          }
        }
      }

      // -- Z-index inflation (computed style; skipped once the finding cap is
      // reached so we stop paying for getComputedStyle) --
      if (zIndexCount < 10) {
        var zIndex = window.getComputedStyle(elem).zIndex;
        if (zIndex && zIndex !== 'auto') {
          var zValue = parseInt(zIndex, 10);
          if (zValue > TH.zIndex) {
            var zSelector = shortSelector(elem);
            var zMsg = 'z-index ' + zValue + ' exceeds ' + TH.zIndex;
            var zID = computeFindingID('z-index-inflation', zSelector, zMsg);
            registerFinding(zID, zSelector);
            fixable.push({
              id: zID,
              type: 'z-index-inflation',
              severity: zValue > TH.zIndex * 10 ? 'warning' : 'info',
              selector: zSelector,
              value: zValue,
              impact: Math.min(10, Math.floor(zValue / 100)),
              fix: 'Use layered z-index system (e.g., --z-modal: 100, --z-dropdown: 50)'
            });
            zIndexCount++;
          }
        }
      }
    }

    // Identify patterns that should be extracted to classes
    for (var pattern in stylePatterns) {
      if (!stylePatterns.hasOwnProperty(pattern)) continue;
      var count = stylePatterns[pattern];

      if (count >= TH.patternMin) {
        var elems = elementsByPattern[pattern];
        var selectors = [];
        for (var j = 0; j < Math.min(5, elems.length); j++) {
          var patternElemSel = elems[j].tagName.toLowerCase();
          if (elems[j].className && typeof elems[j].className === 'string' && elems[j].className.split(' ')[0]) {
            patternElemSel += '.' + elems[j].className.split(' ')[0];
          }
          selectors.push(patternElemSel);
        }
        if (elems.length > 5) {
          selectors.push('...');
        }

        var suggestedClass = suggestClassName(pattern);
        var patternSel = '[style*="' + pattern.substring(0, 30) + '"]';
        var patternMsg = 'Extract to .' + suggestedClass + ' utility class';
        var patternID = computeFindingID('inline-style-pattern', patternSel, patternMsg);
        registerFinding(patternID, patternSel);

        patterns.push({
          pattern: pattern,
          count: count,
          selectors: selectors,
          suggestedClass: suggestedClass
        });

        fixable.push({
          id: patternID,
          type: 'inline-style-pattern',
          severity: 'warning',
          selector: patternSel,
          count: count,
          pattern: pattern,
          impact: Math.min(10, Math.floor(count / 2)),
          fix: patternMsg
        });
      }
    }

    // Hardcoded color findings (from the single-pass collection)
    for (var color in colorPatterns) {
      if (!colorPatterns.hasOwnProperty(color)) continue;
      var colorCount = colorPatterns[color];

      if (colorCount >= 3) {
        var colorFix = 'Replace with CSS variable --color-' + (color.charAt(0) === '#' ? 'hex-' + color.substring(1, 4) : 'named');
        var colorID = computeFindingID('hardcoded-color', color, colorFix);
        fixable.push({
          id: colorID,
          type: 'hardcoded-color',
          severity: 'info',
          pattern: color,
          count: colorCount,
          impact: Math.min(5, Math.floor(colorCount / 3)),
          fix: colorFix
        });
      }
    }

    // --- Analysis: !important declarations ---

    for (var si = 0; si < document.styleSheets.length; si++) {
      try {
        var rules = document.styleSheets[si].cssRules || [];
        for (var ri = 0; ri < rules.length; ri++) {
          if (rules[ri].cssText && rules[ri].cssText.indexOf('!important') !== -1) {
            metrics.importantCount++;
          }
        }
      } catch (e) {
        // Cross-origin stylesheets can't be accessed — count instead of
        // silently swallowing so the report is honest about coverage.
        metrics.inaccessibleStylesheets++;
      }
    }

    var coverageNote = null;
    if (metrics.inaccessibleStylesheets > 0) {
      coverageNote = metrics.inaccessibleStylesheets + ' of ' + metrics.stylesheetCount +
        ' stylesheets are cross-origin and could not be inspected — !important and rule-level checks cover accessible sheets only';
    }

    if (metrics.importantCount > 0) {
      var importantMsg = metrics.importantCount + ' !important declarations found - review for necessity';
      informational.push({
        id: computeFindingID('important-declarations', 'stylesheets', importantMsg),
        type: 'important-declarations',
        severity: 'info',
        count: metrics.importantCount,
        message: importantMsg
      });
    }

    // --- Calculate score and grade ---

    var score = 100;

    // Deduct for inline style patterns
    score -= Math.min(30, patterns.length * 2);

    // Deduct for hardcoded colors
    score -= Math.min(20, Object.keys(colorPatterns).length * 1);

    // Deduct for excessive !important
    if (metrics.importantCount > 20) score -= 15;
    else if (metrics.importantCount > 10) score -= 10;
    else if (metrics.importantCount > 5) score -= 5;

    // Deduct for z-index issues
    score -= Math.min(10, zIndexCount * 2);

    // Deduct for hardcoded sizes
    score -= Math.min(10, Math.floor(metrics.hardcodedSizes / 5));

    // Ensure score doesn't go below 0
    score = Math.max(0, score);

    // Grade (canonical shared A-F scale)
    var grade = auditUtils.calculateGrade(score);

    // --- Generate actions ---

    var actions = [];

    // Top 3 patterns to extract
    var topPatterns = patterns.slice(0, 3);
    for (var ai = 0; ai < topPatterns.length; ai++) {
      actions.push('Create .' + topPatterns[ai].suggestedClass + ' utility class (used ' +
                  topPatterns[ai].count + ' times inline)');
    }

    // !important review
    if (metrics.importantCount > 0) {
      actions.push('Review ' + metrics.importantCount + ' !important declarations for necessity');
    }

    // Color variables
    var topColors = Object.keys(colorPatterns)
      .sort(function(a, b) { return colorPatterns[b] - colorPatterns[a]; })
      .slice(0, 1);
    if (topColors.length > 0) {
      actions.push('Replace ' + colorPatterns[topColors[0]] + ' hardcoded ' +
                  topColors[0] + ' colors with CSS variable');
    }

    // Z-index issues
    if (zIndexCount > 0) {
      actions.push('Address z-index inflation issues (' + zIndexCount + ' elements with z-index >' + TH.zIndex + ')');
    }

    // --- Stats ---

    var stats = {
      errors: 0,
      warnings: fixable.filter(function(f) { return f.severity === 'warning'; }).length,
      info: fixable.filter(function(f) { return f.severity === 'info'; }).length + informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    // --- Build response ---

    var patternsToExtract = patterns.length;
    var summary = metrics.inlineStyleCount + ' inline styles found';
    if (patternsToExtract > 0) {
      summary += ', ' + patternsToExtract + ' should be extracted to classes';
    }

    // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
    // Returns grouped data optimized for AI processing - AI generates class names using codebase context
    if (!raw) {
      // Collect all unique colors with usage context
      var colorData = [];
      for (var c in colorPatterns) {
        if (colorPatterns.hasOwnProperty(c)) {
          colorData.push({
            color: c,
            count: colorPatterns[c],
            // Help AI understand usage context
            isNeutral: /^#([0-9a-f])\1{2,5}$/i.test(c) || /^(gray|grey|white|black)$/i.test(c),
            isTransparent: c.indexOf('rgba') !== -1 && /,\s*0(\.\d+)?\)/.test(c)
          });
        }
      }
      colorData.sort(function(a, b) { return b.count - a.count; });

      // Collect z-index values for AI to design layer system
      var zIndexData = [];
      for (var zi = 0; zi < fixable.length; zi++) {
        if (fixable[zi].type === 'z-index-inflation' && fixable[zi].value !== undefined) {
          zIndexData.push({
            selector: fixable[zi].selector,
            value: fixable[zi].value
          });
        }
      }
      zIndexData.sort(function(a, b) { return b.value - a.value; });

      // Build pattern data with element samples for AI class naming
      var patternData = patterns.map(function(p) {
        return {
          pattern: p.pattern,
          count: p.count,
          selectors: p.selectors,
          // AI will use codebase context to pick better names
          suggestedClass: p.suggestedClass,
          // Parse pattern for AI to understand what it does
          properties: parseInlineStyle(p.pattern)
        };
      });

      var aiResponse = {
        audit: 'css',
        summary: summary,
        score: score,
        grade: grade,
        checkedAt: new Date().toISOString(),
        stats: stats,
        // Raw data for AI interpretation
        raw: {
          metrics: metrics,
          categoryBreakdown: categoryBreakdown,
          // Patterns for AI to name classes appropriately
          inlinePatterns: patternData,
          // Colors for AI to map to design tokens
          hardcodedColors: colorData,
          // Z-index values for AI to design layer system
          zIndexValues: zIndexData,
          // Fixed dimensions for AI to suggest responsive alternatives
          fixedDimensions: fixable.filter(function(f) {
            return f.type === 'fixed-dimensions';
          }).map(function(f) {
            return { selector: f.selector, width: f.width, height: f.height };
          })
        },
        // Hints for AI - what to look for in codebase
        automationHints: {
          lookFor: [
            'existing CSS variables (--color-*, --spacing-*, --z-*)',
            'utility class patterns (Tailwind, Bootstrap, custom)',
            'design token files or theme configuration',
            'CSS-in-JS theme objects'
          ],
          suggestionsNeeded: [
            patternData.length > 0 ? 'utility classes for ' + patternData.length + ' repeated patterns' : null,
            colorData.length > 0 ? 'CSS variable names for ' + colorData.length + ' colors' : null,
            zIndexData.length > 0 ? 'z-index layer system for ' + zIndexData.length + ' elevated elements' : null
          ].filter(Boolean)
        }
      };
      if (coverageNote) aiResponse.note = coverageNote;
      return aiResponse;
    }

    // === RAW RESPONSE (raw: true) ===
    // Returns verbose detailed format with all issues and context
    var response = {
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      metrics: metrics,
      fixable: fixable.slice(0, maxIssues),
      informational: informational,
      patterns: patterns.slice(0, 10),
      categoryBreakdown: categoryBreakdown,
      actions: actions,
      stats: stats
    };
    if (coverageNote) response.note = coverageNote;

    // Respect detailLevel for backward compatibility
    if (detailLevel === 'summary') {
      // Return compact summary
      var summaryResponse = {
        summary: summary,
        score: score,
        grade: grade,
        metrics: metrics,
        stats: stats
      };
      if (coverageNote) summaryResponse.note = coverageNote;
      return summaryResponse;
    }

    return response;
  }

  window.__devtool_audit_css = {
    auditCSS: auditCSS
  };
})();

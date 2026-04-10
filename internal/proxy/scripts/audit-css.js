// CSS audit

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  function auditCSS(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var raw = options.raw === true; // Default: false (AI-optimized format)

    var inlineStyles = document.querySelectorAll('[style]');
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
      inlineStyleCount: inlineStyles.length,
      importantCount: 0,
      stylesheetCount: document.styleSheets.length,
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

    // --- Analysis: Inline style patterns ---

    var stylePatterns = {};
    var elementsByPattern = {};

    for (var i = 0; i < inlineStyles.length; i++) {
      var elem = inlineStyles[i];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(elem)) continue;
      var styleAttr = elem.getAttribute('style');
      if (!styleAttr) continue;

      var normalized = normalizeStyle(styleAttr);
      if (!normalized) continue;

      // Track pattern occurrences
      if (!stylePatterns[normalized]) {
        stylePatterns[normalized] = 0;
        elementsByPattern[normalized] = [];
      }
      stylePatterns[normalized]++;
      elementsByPattern[normalized].push(elem);

      // Categorize properties
      var props = parseInlineStyle(styleAttr);
      for (var prop in props) {
        if (!props.hasOwnProperty(prop)) continue;
        var category = categorizeProperty(prop);
        if (category !== 'other') {
          categoryBreakdown[category]++;
        }

        // Check for hardcoded colors
        if (isHardcodedColor(props[prop]) && !usesCSSVariable(props[prop])) {
          metrics.hardcodedColors++;
        }

        // Check for hardcoded px sizes
        if (isHardcodedPxSize(props[prop])) {
          metrics.hardcodedSizes++;
        }

        // Check for CSS variable usage
        if (usesCSSVariable(props[prop])) {
          metrics.cssVariableUsage++;
        }
      }
    }

    // Identify patterns that should be extracted to classes (3+ occurrences)
    var patternId = 0;
    for (var pattern in stylePatterns) {
      if (!stylePatterns.hasOwnProperty(pattern)) continue;
      var count = stylePatterns[pattern];

      if (count >= 3) {
        var elems = elementsByPattern[pattern];
        var selectors = [];
        for (var j = 0; j < Math.min(5, elems.length); j++) {
          var selector = elems[j].tagName.toLowerCase();
          if (elems[j].className) {
            selector += '.' + elems[j].className.split(' ')[0];
          }
          selectors.push(selector);
        }
        if (elems.length > 5) {
          selectors.push('...');
        }

        var suggestedClass = suggestClassName(pattern);

        patterns.push({
          pattern: pattern,
          count: count,
          selectors: selectors,
          suggestedClass: suggestedClass
        });

        fixable.push({
          id: 'inline-pattern-' + (++patternId),
          type: 'inline-style-pattern',
          severity: 'warning',
          selector: '[style*="' + pattern.substring(0, 30) + '"]',
          count: count,
          pattern: pattern,
          impact: Math.min(10, Math.floor(count / 2)),
          fix: 'Extract to .' + suggestedClass + ' utility class'
        });
      }
    }

    // --- Analysis: !important declarations ---

    for (var i = 0; i < document.styleSheets.length; i++) {
      try {
        var rules = document.styleSheets[i].cssRules || [];
        for (var j = 0; j < rules.length; j++) {
          if (rules[j].cssText && rules[j].cssText.indexOf('!important') !== -1) {
            metrics.importantCount++;
          }
        }
      } catch (e) {
        // Cross-origin stylesheets can't be accessed
      }
    }

    if (metrics.importantCount > 0) {
      informational.push({
        id: 'important-count-1',
        type: 'important-declarations',
        severity: 'info',
        count: metrics.importantCount,
        message: metrics.importantCount + ' !important declarations found - review for necessity'
      });
    }

    // --- Analysis: Hardcoded colors ---

    var colorPatterns = {};
    for (var i = 0; i < inlineStyles.length; i++) {
      var styleAttr = inlineStyles[i].getAttribute('style');
      if (!styleAttr) continue;

      var props = parseInlineStyle(styleAttr);
      for (var prop in props) {
        if (!props.hasOwnProperty(prop)) continue;
        var value = props[prop];

        if (isHardcodedColor(value) && !usesCSSVariable(value)) {
          // Normalize hex colors to lowercase
          var normalized = value.toLowerCase();
          if (!colorPatterns[normalized]) {
            colorPatterns[normalized] = 0;
          }
          colorPatterns[normalized]++;
        }
      }
    }

    var colorId = 0;
    for (var color in colorPatterns) {
      if (!colorPatterns.hasOwnProperty(color)) continue;
      var count = colorPatterns[color];

      if (count >= 3) {
        fixable.push({
          id: 'hardcoded-color-' + (++colorId),
          type: 'hardcoded-color',
          severity: 'info',
          pattern: color,
          count: count,
          impact: Math.min(5, Math.floor(count / 3)),
          fix: 'Replace with CSS variable --color-' + (color.startsWith('#') ? 'hex-' + color.substring(1, 4) : 'named')
        });
      }
    }

    // --- Analysis: Z-index inflation ---

    var allElements = document.querySelectorAll('*');
    var zIndexId = 0;
    for (var i = 0; i < allElements.length; i++) {
      var elem = allElements[i];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(elem)) continue;
      var computed = window.getComputedStyle(elem);
      var zIndex = computed.zIndex;

      if (zIndex && zIndex !== 'auto') {
        var zValue = parseInt(zIndex, 10);
        if (zValue > 100) {
          var selector = elem.tagName.toLowerCase();
          if (elem.className && typeof elem.className === 'string') {
            var classes = elem.className.split(' ').filter(function(c) { return c; });
            if (classes.length > 0) {
              selector = '.' + classes[0];
            }
          }

          fixable.push({
            id: 'z-index-high-' + (++zIndexId),
            type: 'z-index-inflation',
            severity: zValue > 1000 ? 'warning' : 'info',
            selector: selector,
            value: zValue,
            impact: Math.min(10, Math.floor(zValue / 100)),
            fix: 'Use layered z-index system (e.g., --z-modal: 100, --z-dropdown: 50)'
          });

          // Limit to prevent overflow
          if (zIndexId >= 10) break;
        }
      }
    }

    // --- Analysis: Layout issues ---

    var layoutIssueId = 0;
    for (var i = 0; i < inlineStyles.length; i++) {
      var elem = inlineStyles[i];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(elem)) continue;
      var styleAttr = elem.getAttribute('style');
      if (!styleAttr) continue;

      var props = parseInlineStyle(styleAttr);

      // Check for fixed width/height
      if ((props.width && /^\d+px$/.test(props.width)) ||
          (props.height && /^\d+px$/.test(props.height))) {
        var selector = elem.tagName.toLowerCase();
        if (elem.className && typeof elem.className === 'string') {
          var classes = elem.className.split(' ').filter(function(c) { return c; });
          if (classes.length > 0) {
            selector = '.' + classes[0];
          }
        }

        fixable.push({
          id: 'fixed-size-' + (++layoutIssueId),
          type: 'fixed-dimensions',
          severity: 'info',
          selector: selector,
          width: props.width,
          height: props.height,
          impact: 3,
          fix: 'Use relative units (%, rem, em) or max-width/max-height for responsiveness'
        });

        if (layoutIssueId >= 5) break;
      }
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
    score -= Math.min(10, zIndexId * 2);

    // Deduct for hardcoded sizes
    score -= Math.min(10, Math.floor(metrics.hardcodedSizes / 5));

    // Ensure score doesn't go below 0
    score = Math.max(0, score);

    // Calculate grade
    var grade = 'F';
    if (score >= 90) grade = 'A';
    else if (score >= 80) grade = 'B';
    else if (score >= 70) grade = 'C';
    else if (score >= 60) grade = 'D';

    // --- Generate actions ---

    var actions = [];

    // Top 3 patterns to extract
    var topPatterns = patterns.slice(0, 3);
    for (var i = 0; i < topPatterns.length; i++) {
      actions.push('Create .' + topPatterns[i].suggestedClass + ' utility class (used ' +
                  topPatterns[i].count + ' times inline)');
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
    if (zIndexId > 0) {
      actions.push('Address z-index inflation issues (' + zIndexId + ' elements with z-index >100)');
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

    var patternsToExtract = patterns.filter(function(p) { return p.count >= 3; }).length;
    var summary = metrics.inlineStyleCount + ' inline styles found';
    if (patternsToExtract > 0) {
      summary += ', ' + patternsToExtract + ' should be extracted to classes';
    }

    // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
    // Returns grouped data optimized for AI processing - AI generates class names using codebase context
    if (!raw) {
      // Collect all unique colors with usage context
      var colorData = [];
      for (var color in colorPatterns) {
        if (colorPatterns.hasOwnProperty(color)) {
          colorData.push({
            color: color,
            count: colorPatterns[color],
            // Help AI understand usage context
            isNeutral: /^#([0-9a-f])\1{2,5}$/i.test(color) || /^(gray|grey|white|black)$/i.test(color),
            isTransparent: color.indexOf('rgba') !== -1 && /,\s*0(\.\d+)?\)/.test(color)
          });
        }
      }
      colorData.sort(function(a, b) { return b.count - a.count; });

      // Collect z-index values for AI to design layer system
      var zIndexData = [];
      for (var zi = 0; zi < fixable.length; zi++) {
        if (fixable[zi].type === 'z-index-inflation') {
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

      return {
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

    // Respect detailLevel for backward compatibility
    if (detailLevel === 'summary') {
      // Return compact summary
      return {
        summary: summary,
        score: score,
        grade: grade,
        metrics: metrics,
        stats: stats
      };
    }

    return response;
  }

  window.__devtool_audit_css = {
    auditCSS: auditCSS
  };
})();

// Quality audit primitives for DevTool
// DOM complexity, CSS, security, and page quality audits

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // --- Compact Response Helpers ---
  function truncateString(str, maxLength) {
    if (!str || typeof str !== 'string') return str;
    if (str.length <= maxLength) return str;
    return str.substring(0, maxLength) + '...';
  }

  function truncateUrl(url, maxLength) {
    if (!url || typeof url !== 'string') return url;
    if (url.length <= maxLength) return url;
    // Keep protocol + domain + last part of path
    try {
      var u = new URL(url);
      var base = u.protocol + '//' + u.host;
      var remaining = maxLength - base.length - 4; // 4 for "..."
      if (remaining > 10) {
        return base + '/...' + u.pathname.slice(-remaining);
      }
      return base + '/...';
    } catch (e) {
      return truncateString(url, maxLength);
    }
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  function auditDOMComplexity(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var elements = document.querySelectorAll('*');

    // Helper: Generate readable selector for an element
    function getSelector(el) {
      if (!el || !el.tagName) return '';
      var parts = [el.tagName.toLowerCase()];
      if (el.id) {
        parts.push('#' + el.id);
      } else if (el.className && typeof el.className === 'string') {
        var classes = el.className.trim().split(/\s+/).slice(0, 2);
        if (classes.length > 0 && classes[0]) {
          parts.push('.' + classes.join('.'));
        }
      }
      return parts.join('');
    }

    // Helper: Get full selector path (up to 5 levels)
    function getSelectorPath(el) {
      var path = [];
      var current = el;
      var depth = 0;
      while (current && current.tagName && depth < 5) {
        path.unshift(getSelector(current));
        current = current.parentElement;
        depth++;
      }
      return path.join(' > ');
    }

    // Helper: Calculate element depth
    function calculateDepth(el) {
      var d = 0;
      var current = el;
      while (current.parentElement) {
        d++;
        current = current.parentElement;
      }
      return d;
    }

    // Helper: Count descendants
    function countDescendants(el) {
      return el.querySelectorAll('*').length;
    }

    // === METRICS COLLECTION ===
    var maxDepth = 0;
    var totalDepth = 0;
    var totalChildren = 0;
    var elementData = [];

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      var depth = calculateDepth(el);
      var childCount = el.children.length;

      if (depth > maxDepth) maxDepth = depth;
      totalDepth += depth;
      totalChildren += childCount;

      elementData.push({
        element: el,
        depth: depth,
        childCount: childCount,
        attributeCount: el.attributes.length,
        descendants: -1 // Calculated on demand
      });
    }

    var averageChildren = elements.length > 0 ? totalChildren / elements.length : 0;

    // === ISSUE DETECTION ===
    var fixable = [];
    var informational = [];
    var hotspots = [];
    var issueId = 0;

    // 1. Duplicate IDs
    var ids = {};
    var duplicateIdMap = {};
    var elementsWithId = document.querySelectorAll('[id]');
    for (var j = 0; j < elementsWithId.length; j++) {
      var id = elementsWithId[j].id;
      if (!ids[id]) {
        ids[id] = [];
      }
      ids[id].push(elementsWithId[j]);
    }

    for (var dupId in ids) {
      if (ids[dupId].length > 1) {
        duplicateIdMap[dupId] = ids[dupId];
        var selectors = ids[dupId].map(function(el) {
          var parent = el.parentElement;
          var context = parent ? ' (' + getSelector(parent) + ')' : '';
          return getSelector(el) + context;
        });
        fixable.push({
          id: 'dup-id-' + (++issueId),
          type: 'duplicate-id',
          severity: 'error',
          duplicateId: dupId,
          count: ids[dupId].length,
          selectors: selectors,
          impact: 8,
          fix: 'Ensure all IDs are unique - rename duplicates'
        });
      }
    }

    // 2. Excessive children (>10 direct children)
    for (var k = 0; k < elementData.length; k++) {
      var data = elementData[k];
      if (data.childCount > 10) {
        fixable.push({
          id: 'large-children-' + (++issueId),
          type: 'excessive-children',
          severity: data.childCount > 50 ? 'error' : 'warning',
          selector: getSelectorPath(data.element),
          childCount: data.childCount,
          impact: Math.min(10, Math.floor(data.childCount / 10)),
          fix: data.childCount > 50
            ? 'Consider pagination or virtualization'
            : 'Consider componentization or grouping'
        });
      }
    }

    // 3. Deep nesting (>15 levels)
    for (var m = 0; m < elementData.length; m++) {
      var deepData = elementData[m];
      if (deepData.depth > 15) {
        fixable.push({
          id: 'deep-nest-' + (++issueId),
          type: 'excessive-depth',
          severity: deepData.depth > 20 ? 'error' : 'warning',
          selector: getSelectorPath(deepData.element),
          depth: deepData.depth,
          impact: Math.min(10, Math.floor(deepData.depth / 3)),
          fix: 'Flatten nesting or extract to component'
        });
      }
    }

    // 4. Excessive attributes (>10)
    for (var n = 0; n < elementData.length; n++) {
      var attrData = elementData[n];
      if (attrData.attributeCount > 10) {
        fixable.push({
          id: 'excess-attrs-' + (++issueId),
          type: 'excessive-attributes',
          severity: 'warning',
          selector: getSelectorPath(attrData.element),
          attributeCount: attrData.attributeCount,
          impact: Math.min(7, Math.floor(attrData.attributeCount / 2)),
          fix: 'Simplify element or use CSS classes instead of inline attributes'
        });
      }
    }

    // 5. Large lists without virtualization hints (>50 items)
    var lists = document.querySelectorAll('ul, ol');
    for (var p = 0; p < lists.length; p++) {
      var list = lists[p];
      var itemCount = list.querySelectorAll(':scope > li').length;
      if (itemCount > 50) {
        fixable.push({
          id: 'large-list-' + (++issueId),
          type: 'large-list',
          severity: itemCount > 200 ? 'error' : 'warning',
          selector: getSelectorPath(list),
          itemCount: itemCount,
          impact: Math.min(9, Math.floor(itemCount / 25)),
          fix: 'Consider virtualization (e.g., react-window) or pagination'
        });
      }
    }

    // 6. Large tables (>100 rows)
    var tables = document.querySelectorAll('table');
    for (var q = 0; q < tables.length; q++) {
      var table = tables[q];
      var rows = table.querySelectorAll('tr');
      var cells = table.querySelectorAll('td, th');
      if (rows.length > 100) {
        fixable.push({
          id: 'large-table-' + (++issueId),
          type: 'large-table',
          severity: rows.length > 500 ? 'error' : 'warning',
          selector: getSelectorPath(table),
          rows: rows.length,
          cells: cells.length,
          impact: Math.min(9, Math.floor(rows.length / 50)),
          fix: 'Consider pagination, virtual scrolling, or server-side filtering'
        });
      }
    }

    // 7. Large forms (>20 inputs)
    var forms = document.querySelectorAll('form');
    for (var r = 0; r < forms.length; r++) {
      var form = forms[r];
      var inputs = form.querySelectorAll('input, select, textarea');
      if (inputs.length > 20) {
        fixable.push({
          id: 'large-form-' + (++issueId),
          type: 'large-form',
          severity: 'warning',
          selector: getSelectorPath(form),
          inputCount: inputs.length,
          impact: Math.min(7, Math.floor(inputs.length / 5)),
          fix: 'Consider splitting into multi-step form or accordion sections'
        });
      }
    }

    // 8. Excessive inline event handlers
    var elementsWithHandlers = document.querySelectorAll('[onclick], [onload], [onerror], [onchange], [onsubmit]');
    for (var s = 0; s < elementsWithHandlers.length; s++) {
      var handlerEl = elementsWithHandlers[s];
      var handlerCount = 0;
      var handlerTypes = [];
      if (handlerEl.onclick) { handlerCount++; handlerTypes.push('onclick'); }
      if (handlerEl.onload) { handlerCount++; handlerTypes.push('onload'); }
      if (handlerEl.onerror) { handlerCount++; handlerTypes.push('onerror'); }
      if (handlerEl.onchange) { handlerCount++; handlerTypes.push('onchange'); }
      if (handlerEl.onsubmit) { handlerCount++; handlerTypes.push('onsubmit'); }

      if (handlerCount > 2) {
        fixable.push({
          id: 'excess-handlers-' + (++issueId),
          type: 'excessive-handlers',
          severity: 'warning',
          selector: getSelectorPath(handlerEl),
          handlerCount: handlerCount,
          handlers: handlerTypes,
          impact: 5,
          fix: 'Use addEventListener instead of inline event handlers'
        });
      }
    }

    // 9. Hotspots: Large subtrees (>100 descendants) - top 5
    var subtreeData = [];
    for (var t = 0; t < elementData.length; t++) {
      var desc = countDescendants(elementData[t].element);
      elementData[t].descendants = desc;
      if (desc > 100) {
        subtreeData.push({
          element: elementData[t].element,
          descendants: desc,
          depth: elementData[t].depth
        });
      }
    }
    subtreeData.sort(function(a, b) { return b.descendants - a.descendants; });

    for (var u = 0; u < Math.min(5, subtreeData.length); u++) {
      var subtree = subtreeData[u];
      var recommendation = 'Consider lazy loading or code splitting';
      if (subtree.descendants > 500) {
        recommendation = 'Critical: Consider virtualization or lazy loading';
      } else if (subtree.descendants > 200) {
        recommendation = 'Consider virtualization or lazy loading';
      }
      hotspots.push({
        selector: getSelectorPath(subtree.element),
        descendants: subtree.descendants,
        depth: subtree.depth,
        recommendation: recommendation
      });
    }

    // 10. Informational: Total element count
    if (elements.length > 1500) {
      informational.push({
        id: 'dom-count-' + (++issueId),
        type: 'element-count',
        severity: elements.length > 3000 ? 'warning' : 'info',
        message: elements.length + ' elements exceeds recommended 1500 for optimal performance',
        current: elements.length,
        recommended: 1500
      });
    }

    // 11. Informational: Max depth
    if (maxDepth > 15) {
      informational.push({
        id: 'max-depth-' + (++issueId),
        type: 'max-depth',
        severity: maxDepth > 20 ? 'warning' : 'info',
        message: 'Maximum nesting depth of ' + maxDepth + ' exceeds recommended 15',
        current: maxDepth,
        recommended: 15
      });
    }

    // === SCORING ===
    var score = 100;

    // Penalties
    score -= Math.min(20, Math.floor((elements.length - 1500) / 100)); // Element count penalty
    score -= Math.min(15, Math.floor((maxDepth - 15) / 2)); // Depth penalty
    score -= Math.min(10, Object.keys(duplicateIdMap).length * 5); // Duplicate ID penalty
    score -= Math.min(20, fixable.filter(function(f) { return f.severity === 'error'; }).length * 4); // Error penalty
    score -= Math.min(15, fixable.filter(function(f) { return f.severity === 'warning'; }).length * 2); // Warning penalty
    score = Math.max(0, Math.min(100, score));

    // Grade
    var grade = 'F';
    if (score >= 90) grade = 'A';
    else if (score >= 80) grade = 'B';
    else if (score >= 70) grade = 'C';
    else if (score >= 60) grade = 'D';

    // === ACTIONS ===
    var actions = [];

    // Sort fixable by impact (highest first)
    var sortedFixable = fixable.slice().sort(function(a, b) { return b.impact - a.impact; });

    // Top 5 actions
    for (var v = 0; v < Math.min(5, sortedFixable.length); v++) {
      var issue = sortedFixable[v];
      var action = '';
      switch (issue.type) {
        case 'duplicate-id':
          action = 'Fix ' + issue.count + ' duplicate IDs (' + issue.duplicateId + ')';
          break;
        case 'excessive-depth':
          action = 'Refactor ' + issue.selector + ' (' + issue.depth + ' levels deep)';
          break;
        case 'excessive-children':
          action = 'Refactor ' + issue.selector + ' (' + issue.childCount + ' children)';
          break;
        case 'large-list':
          action = 'Virtualize ' + issue.selector + ' (' + issue.itemCount + ' items)';
          break;
        case 'large-table':
          action = 'Paginate ' + issue.selector + ' (' + issue.rows + ' rows)';
          break;
        case 'large-form':
          action = 'Split ' + issue.selector + ' (' + issue.inputCount + ' inputs)';
          break;
        case 'excessive-attributes':
          action = 'Simplify ' + issue.selector + ' (' + issue.attributeCount + ' attributes)';
          break;
        case 'excessive-handlers':
          action = 'Refactor event handlers on ' + issue.selector;
          break;
      }
      if (action) actions.push(action);
    }

    // === SUMMARY ===
    var summaryParts = [];
    if (score >= 80) {
      summaryParts.push('DOM complexity is good');
    } else if (score >= 60) {
      summaryParts.push('DOM complexity is moderate');
    } else {
      summaryParts.push('DOM complexity is high');
    }
    summaryParts.push('(' + elements.length + ' elements)');

    if (fixable.length > 0) {
      summaryParts.push(fixable.length + ' area' + (fixable.length === 1 ? '' : 's') + ' need' + (fixable.length === 1 ? 's' : '') + ' attention');
    }

    var summary = summaryParts.join('. ');

    // === STATS ===
    var errorCount = fixable.filter(function(f) { return f.severity === 'error'; }).length;
    var warningCount = fixable.filter(function(f) { return f.severity === 'warning'; }).length;
    var infoCount = informational.filter(function(f) { return f.severity === 'info'; }).length;

    // === RESPONSE ===
    var response = {
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: [
        'duplicate-ids',
        'excessive-children',
        'excessive-depth',
        'excessive-attributes',
        'large-lists',
        'large-tables',
        'large-forms',
        'excessive-handlers',
        'subtree-size',
        'total-elements'
      ],

      metrics: {
        totalElements: elements.length,
        maxDepth: maxDepth,
        averageChildren: Math.round(averageChildren * 10) / 10,
        elementsWithId: elementsWithId.length,
        forms: document.forms.length,
        images: document.images.length,
        links: document.links.length,
        scripts: document.scripts.length,
        stylesheets: document.styleSheets.length,
        iframes: document.querySelectorAll('iframe').length
      },

      stats: {
        errors: errorCount,
        warnings: warningCount,
        info: infoCount,
        fixable: fixable.length,
        informational: informational.length
      }
    };

    // Include detailed data based on detailLevel
    if (detailLevel === 'summary') {
      // Summary: metrics and stats only
      response.duplicateIdCount = Object.keys(duplicateIdMap).length;
    } else {
      // Compact and full: include all arrays
      response.fixable = fixable;
      response.informational = informational;
      response.hotspots = hotspots;
      response.actions = actions;
    }

    return response;
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  function auditCSS(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;

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

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   maxUrlLength: number (default: 80)
  function auditSecurity(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var maxUrlLength = options.maxUrlLength || 80;

    var critical = [];
    var errors = [];
    var warnings = [];
    var informational = [];
    var checksRun = [];

    // Helper to generate unique IDs
    function generateId(type, index) {
      var hash = (type + index).split('').reduce(function(a, b) {
        a = ((a << 5) - a) + b.charCodeAt(0);
        return a & a;
      }, 0);
      return type + '-' + Math.abs(hash).toString(36);
    }

    // Helper to get CSS selector for element
    function getSelector(el) {
      if (!el || !el.tagName) return '';
      if (el.id) return '#' + el.id;
      if (el.className && typeof el.className === 'string') {
        var classes = el.className.trim().split(/\s+/).slice(0, 2).join('.');
        if (classes) return el.tagName.toLowerCase() + '.' + classes;
      }
      return el.tagName.toLowerCase();
    }

    // Helper to mask secrets
    function maskSecret(secret) {
      if (!secret || secret.length < 8) return '*****';
      return secret.substring(0, 6) + '*****';
    }

    // 1. Check for exposed API keys and secrets
    checksRun.push('exposed-secrets');
    var secretPatterns = [
      { pattern: /sk_live_[a-zA-Z0-9]{24,}|pk_live_[a-zA-Z0-9]{24,}/g, type: 'stripe-key' },
      { pattern: /api[_-]?key["\s:=]+["']?[a-zA-Z0-9_\-]{16,}["']?/gi, type: 'api-key' },
      { pattern: /bearer\s+[a-zA-Z0-9_\-\.]{20,}/gi, type: 'bearer-token' },
      { pattern: /token["\s:=]+["']?[a-zA-Z0-9_\-]{16,}["']?/gi, type: 'token' },
      { pattern: /secret["\s:=]+["']?[a-zA-Z0-9_\-]{16,}["']?/gi, type: 'secret' },
      { pattern: /password["\s:=]+["']?[a-zA-Z0-9_\-]{8,}["']?/gi, type: 'password' },
      { pattern: /AKIA[0-9A-Z]{16}/g, type: 'aws-key' },
      { pattern: /AIza[0-9A-Za-z\-_]{35}/g, type: 'google-api-key' }
    ];

    var allScripts = document.querySelectorAll('script');
    for (var i = 0; i < allScripts.length; i++) {
      var scriptContent = allScripts[i].textContent || '';
      for (var p = 0; p < secretPatterns.length; p++) {
        var matches = scriptContent.match(secretPatterns[p].pattern);
        if (matches) {
          for (var m = 0; m < matches.length; m++) {
            critical.push({
              id: generateId('exposed-secret', critical.length),
              type: 'exposed-secret',
              severity: 'critical',
              secretType: secretPatterns[p].type,
              pattern: maskSecret(matches[m]),
              selector: getSelector(allScripts[i]),
              impact: 10,
              message: 'Exposed ' + secretPatterns[p].type + ' in client-side code',
              fix: 'Move secret to server-side environment variable'
            });
          }
        }
      }
    }

    // Check HTML attributes for secrets
    var allElements = document.querySelectorAll('[data-api-key], [data-token], [data-secret]');
    for (var ae = 0; ae < allElements.length; ae++) {
      var el = allElements[ae];
      var attrValue = el.getAttribute('data-api-key') || el.getAttribute('data-token') || el.getAttribute('data-secret');
      if (attrValue && attrValue.length > 8) {
        critical.push({
          id: generateId('exposed-secret', critical.length),
          type: 'exposed-secret',
          severity: 'critical',
          secretType: 'html-attribute',
          pattern: maskSecret(attrValue),
          selector: getSelector(el),
          impact: 10,
          message: 'Secret exposed in HTML attribute',
          fix: 'Remove secret from HTML and use server-side authentication'
        });
      }
    }

    // 2. Check for XSS vectors
    checksRun.push('xss-vectors');

    // innerHTML usage detection
    var scriptTexts = Array.prototype.slice.call(document.querySelectorAll('script')).map(function(s) {
      return s.textContent || '';
    }).join('\n');

    var innerHTMLUsage = (scriptTexts.match(/\.innerHTML\s*=/g) || []).length;
    if (innerHTMLUsage > 0) {
      errors.push({
        id: generateId('innerHTML-usage', 0),
        type: 'xss-vector',
        severity: 'error',
        vector: 'innerHTML',
        count: innerHTMLUsage,
        impact: 8,
        message: 'Found ' + innerHTMLUsage + ' innerHTML assignments (XSS risk)',
        fix: 'Use textContent or sanitize HTML before assignment'
      });
    }

    var outerHTMLUsage = (scriptTexts.match(/\.outerHTML\s*=/g) || []).length;
    if (outerHTMLUsage > 0) {
      errors.push({
        id: generateId('outerHTML-usage', 0),
        type: 'xss-vector',
        severity: 'error',
        vector: 'outerHTML',
        count: outerHTMLUsage,
        impact: 8,
        message: 'Found ' + outerHTMLUsage + ' outerHTML assignments (XSS risk)',
        fix: 'Use safe DOM manipulation methods'
      });
    }

    var documentWriteUsage = (scriptTexts.match(/document\.write\(/g) || []).length;
    if (documentWriteUsage > 0) {
      errors.push({
        id: generateId('document-write', 0),
        type: 'xss-vector',
        severity: 'error',
        vector: 'document.write',
        count: documentWriteUsage,
        impact: 7,
        message: 'Found ' + documentWriteUsage + ' document.write calls (XSS risk)',
        fix: 'Use safe DOM manipulation methods'
      });
    }

    // 3. Check for eval usage
    checksRun.push('eval-usage');
    var evalUsage = (scriptTexts.match(/\beval\s*\(/g) || []).length;
    if (evalUsage > 0) {
      critical.push({
        id: generateId('eval-usage', 0),
        type: 'eval-usage',
        severity: 'critical',
        count: evalUsage,
        impact: 9,
        message: 'Found ' + evalUsage + ' eval() calls (arbitrary code execution risk)',
        fix: 'Replace eval() with safe alternatives like JSON.parse() or Function constructor'
      });
    }

    var functionConstructor = (scriptTexts.match(/new\s+Function\s*\(/g) || []).length;
    if (functionConstructor > 0) {
      errors.push({
        id: generateId('function-constructor', 0),
        type: 'eval-usage',
        severity: 'error',
        count: functionConstructor,
        impact: 8,
        message: 'Found ' + functionConstructor + ' Function constructor calls (code injection risk)',
        fix: 'Avoid dynamic code generation'
      });
    }

    // 4. Check for insecure storage of sensitive data
    checksRun.push('insecure-storage');
    var sensitiveKeys = ['password', 'token', 'secret', 'apikey', 'api_key', 'bearer', 'credential'];
    var storageIssues = [];

    try {
      for (var sk = 0; sk < sensitiveKeys.length; sk++) {
        if (localStorage.getItem(sensitiveKeys[sk]) || sessionStorage.getItem(sensitiveKeys[sk])) {
          storageIssues.push(sensitiveKeys[sk]);
        }
      }

      // Check all storage keys for sensitive patterns
      for (var lsi = 0; lsi < localStorage.length; lsi++) {
        var key = localStorage.key(lsi);
        for (var ski = 0; ski < sensitiveKeys.length; ski++) {
          if (key && key.toLowerCase().indexOf(sensitiveKeys[ski]) !== -1) {
            if (storageIssues.indexOf(key) === -1) {
              storageIssues.push(key);
            }
          }
        }
      }
    } catch (e) {
      // localStorage may not be available
    }

    if (storageIssues.length > 0) {
      errors.push({
        id: generateId('insecure-storage', 0),
        type: 'insecure-storage',
        severity: 'error',
        keys: storageIssues.slice(0, 5),
        count: storageIssues.length,
        impact: 8,
        message: 'Sensitive data stored in localStorage/sessionStorage',
        fix: 'Use secure httpOnly cookies or server-side sessions for sensitive data'
      });
    }

    // 5. Check for HTTP resources on HTTPS page (mixed content)
    checksRun.push('mixed-content');
    if (window.location.protocol === 'https:') {
      var mixedContent = [];

      var scripts = document.querySelectorAll('script[src^="http:"]');
      for (var mc1 = 0; mc1 < scripts.length; mc1++) {
        mixedContent.push({ type: 'script', url: scripts[mc1].src, element: scripts[mc1] });
      }

      var links = document.querySelectorAll('link[href^="http:"]');
      for (var mc2 = 0; mc2 < links.length; mc2++) {
        mixedContent.push({ type: 'stylesheet', url: links[mc2].href, element: links[mc2] });
      }

      var images = document.querySelectorAll('img[src^="http:"]');
      for (var mc3 = 0; mc3 < images.length; mc3++) {
        mixedContent.push({ type: 'image', url: images[mc3].src, element: images[mc3] });
      }

      if (mixedContent.length > 0) {
        errors.push({
          id: generateId('mixed-content', 0),
          type: 'mixed-content',
          severity: 'error',
          resourceCount: mixedContent.length,
          resources: detailLevel === 'summary' ? undefined : mixedContent.slice(0, 10).map(function(r) {
            return {
              type: r.type,
              url: detailLevel === 'full' ? r.url : truncateUrl(r.url, maxUrlLength),
              selector: getSelector(r.element)
            };
          }),
          impact: 7,
          message: 'Mixed content detected (' + mixedContent.length + ' HTTP resources)',
          fix: 'Change all resource URLs to HTTPS'
        });
      }
    }

    // 6. Check for insecure forms
    checksRun.push('insecure-forms');
    var insecureForms = document.querySelectorAll('form[action^="http:"]');
    if (insecureForms.length > 0) {
      errors.push({
        id: generateId('insecure-form', 0),
        type: 'insecure-form',
        severity: 'error',
        count: insecureForms.length,
        selector: 'form[action^="http:"]',
        impact: 9,
        message: 'Forms with insecure (HTTP) action URLs',
        fix: 'Change form action to HTTPS'
      });
    }

    // Check for password fields without proper autocomplete
    var passwordFieldsNoAutocomplete = document.querySelectorAll('input[type="password"]:not([autocomplete="new-password"]):not([autocomplete="current-password"])');
    if (passwordFieldsNoAutocomplete.length > 0) {
      warnings.push({
        id: generateId('password-autocomplete', 0),
        type: 'password-autocomplete',
        severity: 'warning',
        count: passwordFieldsNoAutocomplete.length,
        selector: 'input[type="password"]:not([autocomplete="new-password"]):not([autocomplete="current-password"])',
        impact: 5,
        message: 'Password fields without proper autocomplete attribute',
        fix: 'Add autocomplete="new-password" or autocomplete="current-password"'
      });
    }

    // Check for login forms over HTTP
    var loginForms = document.querySelectorAll('form');
    for (var lf = 0; lf < loginForms.length; lf++) {
      var form = loginForms[lf];
      var hasPassword = form.querySelector('input[type="password"]');
      if (hasPassword && window.location.protocol === 'http:') {
        critical.push({
          id: generateId('http-login', lf),
          type: 'http-login',
          severity: 'critical',
          selector: getSelector(form),
          impact: 10,
          message: 'Login form over unencrypted HTTP connection',
          fix: 'Use HTTPS for all pages with login forms'
        });
      }
    }

    // Check for CSRF token patterns
    var formsWithoutCSRF = [];
    for (var cf = 0; cf < loginForms.length; cf++) {
      var csrfForm = loginForms[cf];
      var method = (csrfForm.method || 'GET').toUpperCase();
      if (method === 'POST') {
        var hasCSRF = csrfForm.querySelector('input[name*="csrf"], input[name*="token"], input[name="_token"]');
        if (!hasCSRF) {
          formsWithoutCSRF.push(csrfForm);
        }
      }
    }
    if (formsWithoutCSRF.length > 0) {
      warnings.push({
        id: generateId('missing-csrf', 0),
        type: 'missing-csrf',
        severity: 'warning',
        count: formsWithoutCSRF.length,
        selector: 'form[method="post"]',
        impact: 6,
        message: 'POST forms without apparent CSRF token',
        fix: 'Add CSRF token to all state-changing forms'
      });
    }

    // Check for sensitive data in GET parameters
    var urlParams = new URLSearchParams(window.location.search);
    var sensitivParams = [];
    var paramKeys = ['password', 'token', 'secret', 'api_key', 'apikey'];
    for (var pk = 0; pk < paramKeys.length; pk++) {
      if (urlParams.has(paramKeys[pk])) {
        sensitivParams.push(paramKeys[pk]);
      }
    }
    if (sensitivParams.length > 0) {
      critical.push({
        id: generateId('sensitive-params', 0),
        type: 'sensitive-params',
        severity: 'critical',
        params: sensitivParams,
        impact: 9,
        message: 'Sensitive data in URL parameters: ' + sensitivParams.join(', '),
        fix: 'Use POST method or session storage for sensitive data'
      });
    }

    // 7. Check for clickjacking vulnerability
    checksRun.push('clickjacking');
    var hasXFrameOptions = false;
    var hasCSPFrameAncestors = false;
    var metaTags = document.querySelectorAll('meta[http-equiv]');
    for (var mt = 0; mt < metaTags.length; mt++) {
      var httpEquiv = metaTags[mt].getAttribute('http-equiv');
      if (httpEquiv && httpEquiv.toLowerCase() === 'x-frame-options') {
        hasXFrameOptions = true;
      }
      if (httpEquiv && httpEquiv.toLowerCase() === 'content-security-policy') {
        var content = metaTags[mt].getAttribute('content') || '';
        if (content.indexOf('frame-ancestors') !== -1) {
          hasCSPFrameAncestors = true;
        }
      }
    }
    if (!hasXFrameOptions && !hasCSPFrameAncestors) {
      warnings.push({
        id: generateId('clickjacking', 0),
        type: 'clickjacking',
        severity: 'warning',
        impact: 6,
        message: 'Page may be vulnerable to clickjacking (no X-Frame-Options or CSP frame-ancestors)',
        fix: 'Add X-Frame-Options: DENY or Content-Security-Policy: frame-ancestors \'none\''
      });
    }

    // 8. Check for open redirects
    checksRun.push('open-redirects');
    var redirectPatterns = (scriptTexts.match(/window\.location\s*=|window\.location\.href\s*=|window\.location\.replace\(/g) || []).length;
    if (redirectPatterns > 0) {
      informational.push({
        id: generateId('redirect-pattern', 0),
        type: 'redirect-pattern',
        severity: 'info',
        count: redirectPatterns,
        impact: 3,
        message: 'Found ' + redirectPatterns + ' redirect patterns (verify no user input)',
        fix: 'Validate redirect URLs against whitelist'
      });
    }

    // 9. Check for postMessage without origin check
    checksRun.push('postmessage-security');
    var postMessageListeners = (scriptTexts.match(/addEventListener\s*\(\s*["']message["']/g) || []).length;
    var originChecks = (scriptTexts.match(/event\.origin|e\.origin|message\.origin/g) || []).length;
    if (postMessageListeners > 0 && originChecks === 0) {
      errors.push({
        id: generateId('postmessage-no-origin', 0),
        type: 'postmessage-no-origin',
        severity: 'error',
        count: postMessageListeners,
        impact: 8,
        message: 'postMessage listeners without origin validation',
        fix: 'Always validate event.origin in message event listeners'
      });
    }

    // 10. Check for third-party scripts
    checksRun.push('third-party-scripts');
    var currentOrigin = window.location.origin;
    var thirdPartyScripts = [];
    var scriptSources = document.querySelectorAll('script[src]');
    for (var tps = 0; tps < scriptSources.length; tps++) {
      var src = scriptSources[tps].src;
      try {
        var srcUrl = new URL(src);
        if (srcUrl.origin !== currentOrigin) {
          thirdPartyScripts.push({
            url: src,
            origin: srcUrl.origin,
            element: scriptSources[tps]
          });
        }
      } catch (e) {
        // Invalid URL
      }
    }
    if (thirdPartyScripts.length > 0) {
      informational.push({
        id: generateId('third-party-scripts', 0),
        type: 'third-party-scripts',
        severity: 'info',
        count: thirdPartyScripts.length,
        origins: Array.from(new Set(thirdPartyScripts.map(function(s) { return s.origin; }))).slice(0, 5),
        impact: 4,
        message: 'Page loads ' + thirdPartyScripts.length + ' third-party scripts',
        fix: 'Review third-party scripts and use Subresource Integrity (SRI)'
      });
    }

    // 11. Check for inline scripts without nonce
    checksRun.push('inline-scripts');
    var inlineScripts = document.querySelectorAll('script:not([src])');
    var scriptsWithoutNonce = [];
    for (var isn = 0; isn < inlineScripts.length; isn++) {
      if (!inlineScripts[isn].nonce && !inlineScripts[isn].hasAttribute('nonce')) {
        scriptsWithoutNonce.push(inlineScripts[isn]);
      }
    }
    if (scriptsWithoutNonce.length > 0) {
      informational.push({
        id: generateId('inline-no-nonce', 0),
        type: 'inline-no-nonce',
        severity: 'info',
        count: scriptsWithoutNonce.length,
        selector: 'script:not([src]):not([nonce])',
        impact: 3,
        message: 'Inline scripts without CSP nonce',
        fix: 'Add nonce attribute or use external scripts with CSP'
      });
    }

    // 12. Check for external resources without SRI
    checksRun.push('sri');
    var externalResources = document.querySelectorAll('script[src], link[rel="stylesheet"][href]');
    var resourcesWithoutSRI = [];
    for (var sri = 0; sri < externalResources.length; sri++) {
      var resource = externalResources[sri];
      var resourceSrc = resource.src || resource.href;
      try {
        var resourceUrl = new URL(resourceSrc);
        if (resourceUrl.origin !== currentOrigin && !resource.integrity) {
          resourcesWithoutSRI.push(resource);
        }
      } catch (e) {
        // Invalid URL
      }
    }
    if (resourcesWithoutSRI.length > 0) {
      warnings.push({
        id: generateId('missing-sri', 0),
        type: 'missing-sri',
        severity: 'warning',
        count: resourcesWithoutSRI.length,
        selector: 'script[src]:not([integrity]), link[rel="stylesheet"][href]:not([integrity])',
        impact: 5,
        message: 'External resources without Subresource Integrity',
        fix: 'Add integrity attribute to all external resources'
      });
    }

    // 13. Check for missing noopener
    checksRun.push('noopener');
    var unsafeLinks = document.querySelectorAll('a[target="_blank"]:not([rel*="noopener"])');
    if (unsafeLinks.length > 0) {
      warnings.push({
        id: generateId('missing-noopener', 0),
        type: 'missing-noopener',
        severity: 'warning',
        count: unsafeLinks.length,
        selector: 'a[target="_blank"]:not([rel*="noopener"])',
        impact: 5,
        message: 'External links missing rel="noopener"',
        fix: 'Add rel="noopener noreferrer" to all external links'
      });
    }

    // Combine all issues
    var allIssues = critical.concat(errors).concat(warnings).concat(informational);

    // Separate fixable issues (those with selectors or clear remediation)
    var fixable = allIssues.filter(function(issue) {
      return issue.selector || issue.type === 'missing-noopener' ||
             issue.type === 'missing-sri' || issue.type === 'mixed-content' ||
             issue.type === 'insecure-form' || issue.type === 'password-autocomplete';
    });

    // Calculate stats
    var stats = {
      critical: critical.length,
      errors: errors.length,
      warnings: warnings.length,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    // Calculate score: 100 - (critical*20 + errors*10 + warnings*5 + info*1), min 0
    var score = Math.max(0, 100 - (stats.critical * 20 + stats.errors * 10 + stats.warnings * 5 + stats.info * 1));

    // Calculate grade
    var grade;
    if (score >= 90) grade = 'A';
    else if (score >= 80) grade = 'B';
    else if (score >= 70) grade = 'C';
    else if (score >= 60) grade = 'D';
    else grade = 'F';

    // Generate summary
    var summaryParts = [];
    if (stats.critical > 0) summaryParts.push(stats.critical + ' critical');
    if (stats.errors > 0) summaryParts.push(stats.errors + ' error' + (stats.errors > 1 ? 's' : ''));
    if (stats.warnings > 0) summaryParts.push(stats.warnings + ' warning' + (stats.warnings > 1 ? 's' : ''));
    if (stats.info > 0) summaryParts.push(stats.info + ' info');

    var summary = summaryParts.length > 0 ?
      summaryParts.join(', ') + ' found' :
      'No security issues detected';

    // Add top issues to summary
    if (critical.length > 0) {
      summary = critical.length + ' critical security issue' + (critical.length > 1 ? 's' : '') +
                ': ' + critical.slice(0, 2).map(function(i) { return i.type; }).join(', ');
    }

    // Generate prioritized actions
    var actions = [];
    if (critical.length > 0) {
      critical.slice(0, 3).forEach(function(issue) {
        actions.push('URGENT: ' + issue.fix);
      });
    }
    if (errors.length > 0 && actions.length < 5) {
      errors.slice(0, 5 - actions.length).forEach(function(issue) {
        actions.push(issue.fix);
      });
    }
    if (warnings.length > 0 && actions.length < 5) {
      warnings.slice(0, 5 - actions.length).forEach(function(issue) {
        if (issue.count) {
          actions.push(issue.fix + ' (' + issue.count + ' instances)');
        } else {
          actions.push(issue.fix);
        }
      });
    }

    // Build response based on detail level
    var response = {
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      stats: stats
    };

    if (detailLevel === 'summary') {
      // Summary: just overview
      return response;
    }

    // Compact and full modes: include categorized issues
    response.critical = critical.slice(0, maxIssues);
    response.fixable = fixable.slice(0, maxIssues);
    response.informational = informational.slice(0, maxIssues);
    response.actions = actions;

    if (detailLevel === 'full') {
      // Full mode: include all issues in their categories
      response.errors = errors;
      response.warnings = warnings;
      response.allIssues = allIssues;
    }

    return response;
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  function auditPageQuality(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;

    // Initialize tracking arrays
    var fixable = [];
    var informational = [];
    var checksRun = [];
    var actions = [];
    var score = 100;

    // Helper to get meta tag content
    function getMetaContent(name, property) {
      var selector = property ? 'meta[property="' + name + '"]' : 'meta[name="' + name + '"]';
      var meta = document.querySelector(selector);
      return meta ? meta.getAttribute('content') : null;
    }

    // Helper to calculate grade from score
    function calculateGrade(s) {
      if (s >= 97) return 'A+';
      if (s >= 93) return 'A';
      if (s >= 90) return 'A-';
      if (s >= 87) return 'B+';
      if (s >= 83) return 'B';
      if (s >= 80) return 'B-';
      if (s >= 77) return 'C+';
      if (s >= 73) return 'C';
      if (s >= 70) return 'C-';
      if (s >= 67) return 'D+';
      if (s >= 63) return 'D';
      if (s >= 60) return 'D-';
      return 'F';
    }

    // === META TAG ANALYSIS ===
    checksRun.push('meta-tags');
    var meta = {};

    // Title analysis
    var title = document.title || '';
    var titleLength = title.length;
    var titleOptimal = titleLength >= 50 && titleLength <= 60;
    meta.title = {
      value: title,
      length: titleLength,
      optimal: titleOptimal
    };

    if (!title) {
      score -= 10;
      fixable.push({
        id: 'title-missing-1',
        type: 'missing-title',
        severity: 'error',
        impact: 10,
        fix: 'Add a descriptive page title'
      });
      actions.push('Add a descriptive page title');
    } else if (titleLength < 30) {
      score -= 3;
      informational.push({
        id: 'title-short-1',
        type: 'title-length',
        severity: 'info',
        message: 'Title is ' + titleLength + ' chars (optimal: 50-60)',
        current: titleLength,
        optimal: '50-60'
      });
    } else if (titleLength > 60) {
      score -= 2;
      meta.title.issue = 'too long';
      informational.push({
        id: 'title-long-1',
        type: 'title-length',
        severity: 'info',
        message: 'Title is ' + titleLength + ' chars (optimal: 50-60)',
        current: titleLength,
        optimal: '50-60'
      });
      actions.push('Shorten title from ' + titleLength + ' to 50-60 characters');
    }

    // Description analysis
    var description = getMetaContent('description');
    if (description) {
      var descLength = description.length;
      var descOptimal = descLength >= 150 && descLength <= 160;
      meta.description = {
        value: description,
        length: descLength,
        optimal: descOptimal
      };

      if (descLength < 120) {
        score -= 2;
        meta.description.issue = 'too short';
        informational.push({
          id: 'desc-short-1',
          type: 'meta-description-length',
          severity: 'info',
          message: 'Meta description is ' + descLength + ' chars (optimal: 150-160)',
          current: descLength,
          optimal: '150-160'
        });
      } else if (descLength > 160) {
        score -= 2;
        meta.description.issue = 'too long';
        informational.push({
          id: 'desc-long-1',
          type: 'meta-description-length',
          severity: 'info',
          message: 'Meta description is ' + descLength + ' chars (optimal: 150-160)',
          current: descLength,
          optimal: '150-160'
        });
        actions.push('Shorten meta description from ' + descLength + ' to 150-160 characters');
      }
    } else {
      score -= 5;
      meta.description = { present: false };
      fixable.push({
        id: 'desc-missing-1',
        type: 'missing-description',
        severity: 'warning',
        impact: 5,
        fix: 'Add meta description (150-160 chars)'
      });
      actions.push('Add meta description (150-160 chars)');
    }

    // Canonical URL
    var canonical = document.querySelector('link[rel="canonical"]');
    if (canonical) {
      var canonicalUrl = canonical.href;
      var selfReferencing = canonicalUrl === window.location.href;
      meta.canonical = {
        present: true,
        value: canonicalUrl,
        selfReferencing: selfReferencing
      };
      if (!selfReferencing) {
        informational.push({
          id: 'canonical-external-1',
          type: 'canonical-external',
          severity: 'info',
          message: 'Canonical URL points to different page',
          canonical: canonicalUrl,
          current: window.location.href
        });
      }
    } else {
      score -= 3;
      meta.canonical = { present: false };
      fixable.push({
        id: 'canonical-missing-1',
        type: 'missing-canonical',
        severity: 'warning',
        impact: 3,
        fix: 'Add canonical link tag'
      });
      actions.push('Add canonical link tag');
    }

    // Robots meta
    var robots = getMetaContent('robots');
    if (robots) {
      meta.robots = { present: true, value: robots };
      if (robots.indexOf('noindex') !== -1) {
        informational.push({
          id: 'robots-noindex-1',
          type: 'robots-noindex',
          severity: 'info',
          message: 'Page is set to noindex'
        });
      }
    } else {
      meta.robots = { present: false };
    }

    // Viewport
    var viewport = getMetaContent('viewport');
    if (viewport) {
      meta.viewport = { present: true, value: viewport };
    } else {
      score -= 8;
      meta.viewport = { present: false };
      fixable.push({
        id: 'viewport-missing-1',
        type: 'missing-viewport',
        severity: 'error',
        impact: 8,
        fix: 'Add viewport meta tag: <meta name="viewport" content="width=device-width, initial-scale=1">'
      });
      actions.push('Add viewport meta tag for mobile optimization');
    }

    // Hreflang
    var hreflangLinks = document.querySelectorAll('link[rel="alternate"][hreflang]');
    if (hreflangLinks.length > 0) {
      var hreflangLangs = [];
      for (var i = 0; i < hreflangLinks.length; i++) {
        hreflangLangs.push(hreflangLinks[i].getAttribute('hreflang'));
      }
      meta.hreflang = { present: true, count: hreflangLinks.length, languages: hreflangLangs };
    } else {
      meta.hreflang = { present: false };
    }

    // === OPEN GRAPH TAGS ===
    checksRun.push('open-graph');
    var ogTags = ['og:title', 'og:description', 'og:image', 'og:url', 'og:type'];
    var ogPresent = [];
    var ogMissing = [];

    for (var j = 0; j < ogTags.length; j++) {
      if (getMetaContent(ogTags[j], true)) {
        ogPresent.push(ogTags[j]);
      } else {
        ogMissing.push(ogTags[j]);
      }
    }

    var openGraph = {
      complete: ogMissing.length === 0,
      present: ogPresent,
      missing: ogMissing
    };

    if (ogMissing.length > 0) {
      var ogImpact = Math.min(ogMissing.length * 2, 8);
      score -= ogImpact;
      fixable.push({
        id: 'og-missing-1',
        type: 'missing-og-tags',
        severity: 'warning',
        impact: ogImpact,
        missing: ogMissing,
        fix: 'Add Open Graph meta tags: ' + ogMissing.join(', ')
      });
      actions.push('Add Open Graph meta tags for social sharing (' + ogMissing.join(', ') + ')');
    }

    // === TWITTER CARD TAGS ===
    checksRun.push('twitter-card');
    var twitterTags = ['twitter:card', 'twitter:title', 'twitter:description', 'twitter:image'];
    var twitterPresent = [];
    var twitterMissing = [];

    for (var k = 0; k < twitterTags.length; k++) {
      if (getMetaContent(twitterTags[k])) {
        twitterPresent.push(twitterTags[k]);
      } else {
        twitterMissing.push(twitterTags[k]);
      }
    }

    var twitterCard = {
      complete: twitterMissing.length === 0,
      present: twitterPresent,
      missing: twitterMissing
    };

    if (twitterMissing.length > 0) {
      var twitterImpact = Math.min(twitterMissing.length * 2, 6);
      score -= twitterImpact;
      fixable.push({
        id: 'twitter-missing-1',
        type: 'missing-twitter-tags',
        severity: 'warning',
        impact: twitterImpact,
        missing: twitterMissing,
        fix: 'Add Twitter Card meta tags: ' + twitterMissing.join(', ')
      });
      actions.push('Add Twitter Card meta tags (' + twitterMissing.join(', ') + ')');
    }

    // === STRUCTURED DATA ===
    checksRun.push('structured-data');
    var jsonLdScripts = document.querySelectorAll('script[type="application/ld+json"]');
    var structuredData = {
      present: jsonLdScripts.length > 0,
      types: [],
      valid: true
    };

    if (jsonLdScripts.length > 0) {
      for (var l = 0; l < jsonLdScripts.length; l++) {
        try {
          var jsonLd = JSON.parse(jsonLdScripts[l].textContent);
          if (jsonLd['@type']) {
            structuredData.types.push(jsonLd['@type']);
          } else if (jsonLd['@graph']) {
            for (var m = 0; m < jsonLd['@graph'].length; m++) {
              if (jsonLd['@graph'][m]['@type']) {
                structuredData.types.push(jsonLd['@graph'][m]['@type']);
              }
            }
          }
        } catch (e) {
          structuredData.valid = false;
          fixable.push({
            id: 'structured-data-invalid-1',
            type: 'invalid-structured-data',
            severity: 'error',
            impact: 5,
            fix: 'Fix malformed JSON-LD structured data'
          });
          actions.push('Fix malformed JSON-LD structured data');
          score -= 5;
        }
      }
    } else {
      informational.push({
        id: 'structured-data-missing-1',
        type: 'missing-structured-data',
        severity: 'info',
        message: 'No JSON-LD structured data found (recommended for rich results)'
      });
    }

    // === CONTENT ANALYSIS ===
    checksRun.push('content-quality');

    // Heading hierarchy
    var headings = document.querySelectorAll('h1, h2, h3, h4, h5, h6');
    var headingLevels = [];
    var headingValid = true;
    var previousLevel = 0;

    for (var n = 0; n < headings.length; n++) {
      var level = parseInt(headings[n].tagName.substring(1));
      headingLevels.push('h' + level);

      if (previousLevel > 0 && level > previousLevel + 1) {
        headingValid = false;
      }
      previousLevel = level;
    }

    var h1Count = document.querySelectorAll('h1').length;
    if (h1Count === 0) {
      score -= 5;
      fixable.push({
        id: 'h1-missing-1',
        type: 'missing-h1',
        severity: 'warning',
        impact: 5,
        fix: 'Add H1 heading to page'
      });
      actions.push('Add H1 heading to page');
    } else if (h1Count > 1) {
      score -= 2;
      informational.push({
        id: 'h1-multiple-1',
        type: 'multiple-h1',
        severity: 'info',
        message: 'Multiple H1 headings found (' + h1Count + ')',
        count: h1Count
      });
    }

    if (!headingValid) {
      score -= 3;
      fixable.push({
        id: 'heading-hierarchy-1',
        type: 'heading-hierarchy',
        severity: 'warning',
        impact: 3,
        fix: 'Fix heading hierarchy (no skipped levels)'
      });
      actions.push('Fix heading hierarchy (no skipped levels)');
    }

    // Alt text coverage
    var images = document.querySelectorAll('img');
    var imagesWithAlt = document.querySelectorAll('img[alt]');
    var altCoverage = images.length > 0 ? Math.round((imagesWithAlt.length / images.length) * 100) : 100;
    var missingAlt = images.length - imagesWithAlt.length;

    if (missingAlt > 0) {
      var altImpact = Math.min(missingAlt * 2, 10);
      score -= altImpact;
      fixable.push({
        id: 'alt-missing-1',
        type: 'missing-alt',
        severity: 'warning',
        impact: altImpact,
        selector: 'img:not([alt])',
        count: missingAlt,
        fix: 'Add descriptive alt text to ' + missingAlt + ' image' + (missingAlt > 1 ? 's' : '')
      });
      actions.push('Add alt text to ' + missingAlt + ' image' + (missingAlt > 1 ? 's' : ''));
    }

    // Link text quality
    var links = document.querySelectorAll('a[href]');
    var genericTerms = ['click here', 'read more', 'learn more', 'more', 'here', 'link', 'click'];
    var genericLinks = [];

    for (var p = 0; p < links.length; p++) {
      var linkText = (links[p].textContent || '').trim().toLowerCase();
      for (var q = 0; q < genericTerms.length; q++) {
        if (linkText === genericTerms[q]) {
          genericLinks.push(linkText);
          break;
        }
      }
    }

    if (genericLinks.length > 0) {
      var linkImpact = Math.min(genericLinks.length, 5);
      score -= linkImpact;
      fixable.push({
        id: 'generic-links-1',
        type: 'generic-link-text',
        severity: 'warning',
        impact: linkImpact,
        count: genericLinks.length,
        fix: 'Improve generic link text (' + genericLinks.length + ' instance' + (genericLinks.length > 1 ? 's' : '') + ')'
      });
      actions.push('Improve generic link text (' + genericLinks.length + ' instance' + (genericLinks.length > 1 ? 's' : '') + ')');
    }

    // Content-to-code ratio (rough estimate)
    var bodyText = (document.body.textContent || '').trim();
    var textLength = bodyText.length;
    var htmlLength = document.documentElement.outerHTML.length;
    var contentRatio = htmlLength > 0 ? Math.round((textLength / htmlLength) * 100) : 0;

    if (contentRatio < 10 && textLength > 100) {
      score -= 3;
      informational.push({
        id: 'content-ratio-low-1',
        type: 'low-content-ratio',
        severity: 'info',
        message: 'Low content-to-code ratio (' + contentRatio + '%)',
        ratio: contentRatio
      });
    }

    var contentAnalysis = {
      headingStructure: {
        valid: headingValid,
        levels: headingLevels
      },
      altTextCoverage: {
        total: images.length,
        withAlt: imagesWithAlt.length,
        percentage: altCoverage
      },
      linkTextQuality: {
        total: links.length,
        generic: genericLinks.length,
        genericLinks: genericLinks.slice(0, 10)
      },
      contentToCodeRatio: contentRatio
    };

    // === TECHNICAL SEO ===
    checksRun.push('technical-seo');

    // Language attribute
    if (!document.documentElement.lang) {
      score -= 4;
      fixable.push({
        id: 'lang-missing-1',
        type: 'missing-lang',
        severity: 'warning',
        impact: 4,
        fix: 'Add lang attribute to <html> element'
      });
      actions.push('Add lang attribute to <html> element');
    }

    // Crawlable links
    var uncrawlableLinks = document.querySelectorAll('a[href^="javascript:"], a[href="#"]:not([href="#"])');
    var jsVoidLinks = document.querySelectorAll('a[href="javascript:void(0)"]');
    var totalUncrawlable = uncrawlableLinks.length;

    if (totalUncrawlable > 0) {
      var crawlImpact = Math.min(totalUncrawlable, 5);
      score -= crawlImpact;
      fixable.push({
        id: 'uncrawlable-links-1',
        type: 'uncrawlable-links',
        severity: 'warning',
        impact: crawlImpact,
        selector: 'a[href^="javascript:"], a[href="#"]',
        count: totalUncrawlable,
        fix: 'Replace ' + totalUncrawlable + ' non-crawlable link' + (totalUncrawlable > 1 ? 's' : '') + ' with proper URLs'
      });
      actions.push('Replace ' + totalUncrawlable + ' non-crawlable link' + (totalUncrawlable > 1 ? 's' : '') + ' with proper URLs');
    }

    // Image optimization hints
    var webpImages = document.querySelectorAll('img[src$=".webp"]');
    var lazyImages = document.querySelectorAll('img[loading="lazy"]');
    var lazyPercentage = images.length > 0 ? Math.round((lazyImages.length / images.length) * 100) : 0;

    if (images.length > 5 && lazyPercentage < 50) {
      informational.push({
        id: 'lazy-loading-low-1',
        type: 'low-lazy-loading',
        severity: 'info',
        message: 'Only ' + lazyPercentage + '% of images use lazy loading',
        percentage: lazyPercentage
      });
    }

    // === CALCULATE FINAL SCORE AND GRADE ===
    score = Math.max(0, Math.min(100, score));
    var grade = calculateGrade(score);

    // Build summary
    var summaryParts = ['SEO score ' + score + '/100'];
    if (ogMissing.length > 0) {
      summaryParts.push('Missing OG tags: ' + ogMissing.join(', '));
    }
    if (missingAlt > 0) {
      summaryParts.push(missingAlt + ' image' + (missingAlt > 1 ? 's' : '') + ' without alt');
    }
    if (genericLinks.length > 0) {
      summaryParts.push(genericLinks.length + ' generic link' + (genericLinks.length > 1 ? 's' : ''));
    }
    var summary = summaryParts.join('. ');

    // Build stats
    var stats = {
      errors: fixable.filter(function(f) { return f.severity === 'error'; }).length,
      warnings: fixable.filter(function(f) { return f.severity === 'warning'; }).length,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    // Build response
    var response = {
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      meta: meta,
      openGraph: openGraph,
      twitterCard: twitterCard,
      structuredData: structuredData,
      contentAnalysis: contentAnalysis,
      stats: stats
    };

    // Add fixable and informational based on detail level
    if (detailLevel === 'summary') {
      // Summary: just counts
      response.fixableCount = fixable.length;
      response.informationalCount = informational.length;
      response.actionCount = actions.length;
    } else {
      // Compact and full: include arrays
      response.fixable = fixable;
      response.informational = informational;
      response.actions = actions;
    }

    return response;
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxResources: number (default: 20) - limit resource entries
  //   maxUrlLength: number (default: 60) - truncate resource URLs
  function auditPerformance(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxResources = options.maxResources || 20;
    var maxUrlLength = options.maxUrlLength || 60;

    var perf = window.performance;
    if (!perf) {
      return { error: 'Performance API not available', detailLevel: detailLevel };
    }

    // === HELPER FUNCTIONS ===

    // Rate a metric based on thresholds
    function rateMetric(value, goodThreshold, poorThreshold) {
      if (value === null || value === undefined) return 'unknown';
      if (value <= goodThreshold) return 'good';
      if (value <= poorThreshold) return 'needs-improvement';
      return 'poor';
    }

    // Extract domain from URL
    function getDomain(url) {
      try {
        return new URL(url).hostname;
      } catch (e) {
        return 'unknown';
      }
    }

    // Format bytes to human-readable
    function formatBytes(bytes) {
      if (bytes === 0) return '0B';
      if (bytes < 1024) return bytes + 'B';
      if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + 'KB';
      return (bytes / 1024 / 1024).toFixed(1) + 'MB';
    }

    // Generate CSS selector for element
    function getSelector(el) {
      if (!el) return 'unknown';
      if (el.id) return '#' + el.id;
      if (el.className && typeof el.className === 'string') {
        var classes = el.className.trim().split(/\s+/);
        if (classes.length > 0 && classes[0]) {
          return el.tagName.toLowerCase() + '.' + classes[0];
        }
      }
      return el.tagName.toLowerCase();
    }

    // === COLLECT METRICS ===

    var timing = perf.timing || {};

    // Get paint timing
    var paintEntries = perf.getEntriesByType ? perf.getEntriesByType('paint') : [];
    var fcp = null;
    var fp = null;
    for (var i = 0; i < paintEntries.length; i++) {
      if (paintEntries[i].name === 'first-contentful-paint') fcp = Math.round(paintEntries[i].startTime);
      if (paintEntries[i].name === 'first-paint') fp = Math.round(paintEntries[i].startTime);
    }

    // Get LCP if available
    var lcp = null;
    try {
      var lcpEntries = perf.getEntriesByType ? perf.getEntriesByType('largest-contentful-paint') : [];
      if (lcpEntries.length > 0) {
        lcp = Math.round(lcpEntries[lcpEntries.length - 1].startTime);
      }
    } catch (e) {
      // LCP may not be available
    }

    // Try to get CLS via layout-shift entries
    var cls = null;
    try {
      var layoutShifts = perf.getEntriesByType('layout-shift') || [];
      if (layoutShifts.length > 0) {
        cls = layoutShifts.reduce(function(sum, entry) {
          if (!entry.hadRecentInput) {
            return sum + entry.value;
          }
          return sum;
        }, 0);
        cls = Math.round(cls * 1000) / 1000; // Round to 3 decimals
      }
    } catch (e) {
      // CLS may not be available
    }

    // INP is not widely available yet
    var inp = null;

    // === CORE WEB VITALS WITH RATINGS ===
    var coreWebVitals = {
      lcp: {
        value: lcp,
        rating: rateMetric(lcp, 2500, 4000),
        target: 2500
      },
      fcp: {
        value: fcp,
        rating: rateMetric(fcp, 1800, 3000),
        target: 1800
      },
      cls: {
        value: cls,
        rating: rateMetric(cls, 0.1, 0.25),
        target: 0.1
      },
      inp: {
        value: inp,
        rating: 'unknown',
        target: 200
      }
    };

    // === RESOURCE ANALYSIS ===

    var resources = perf.getEntriesByType ? perf.getEntriesByType('resource') : [];

    // Categorize resources by type
    var resourcesByType = {
      script: [],
      css: [],
      img: [],
      font: [],
      fetch: [],
      other: []
    };

    var thirdPartyMap = {};
    var currentDomain = window.location.hostname;

    for (var j = 0; j < resources.length; j++) {
      var r = resources[j];
      var type = r.initiatorType || 'other';

      // Normalize type
      if (type === 'link' && r.name.match(/\.css/i)) type = 'css';
      if (type === 'img' || r.name.match(/\.(jpg|jpeg|png|gif|webp|svg)/i)) type = 'img';
      if (type === 'xmlhttprequest' || type === 'fetch') type = 'fetch';
      if (r.name.match(/\.(woff2?|ttf|otf|eot)/i)) type = 'font';
      if (type === 'script' || r.name.match(/\.js/i)) type = 'script';
      if (type === 'link' || type === 'css' || r.name.match(/\.css/i)) type = 'css';

      var category = resourcesByType[type] ? type : 'other';

      var resourceData = {
        url: r.name,
        duration: Math.round(r.duration),
        size: r.transferSize || 0,
        type: category
      };

      resourcesByType[category].push(resourceData);

      // Track third-party resources
      var domain = getDomain(r.name);
      if (domain !== currentDomain && domain !== 'unknown') {
        if (!thirdPartyMap[domain]) {
          thirdPartyMap[domain] = {
            requests: 0,
            totalTime: 0,
            totalSize: 0
          };
        }
        thirdPartyMap[domain].requests++;
        thirdPartyMap[domain].totalTime += Math.round(r.duration);
        thirdPartyMap[domain].totalSize += r.transferSize || 0;
      }
    }

    // === ISSUE DETECTION ===

    var fixable = [];
    var informational = [];
    var actions = [];
    var checksRun = [];
    var fixableId = 0;

    // 1. Check for render-blocking scripts
    checksRun.push('render-blocking-resources');
    var blockingScripts = document.querySelectorAll('script[src]:not([async]):not([defer]):not([type="module"])');
    for (var k = 0; k < blockingScripts.length; k++) {
      var script = blockingScripts[k];
      var src = script.getAttribute('src');
      if (src && !src.match(/^\s*$/)) {
        fixable.push({
          id: 'render-block-' + (++fixableId),
          type: 'render-blocking',
          severity: 'error',
          selector: getSelector(script),
          impact: 8,
          fix: 'Add async or defer attribute',
          estimatedSavings: '~300-500ms'
        });
      }
    }

    // 2. Check for render-blocking stylesheets
    var blockingStyles = document.querySelectorAll('link[rel="stylesheet"]:not([media="print"])');
    for (var l = 0; l < blockingStyles.length; l++) {
      var link = blockingStyles[l];
      informational.push({
        id: 'css-block-' + l,
        type: 'render-blocking-css',
        severity: 'info',
        selector: getSelector(link),
        message: 'Stylesheet blocks rendering (consider critical CSS extraction)'
      });
    }

    // 3. Check for unoptimized images
    checksRun.push('unoptimized-images');
    var images = document.querySelectorAll('img[src]');
    var unoptimizedCount = 0;
    for (var m = 0; m < images.length; m++) {
      var img = images[m];
      var imgSrc = img.getAttribute('src');
      var naturalWidth = img.naturalWidth || 0;
      var naturalHeight = img.naturalHeight || 0;
      var displayWidth = img.offsetWidth || 0;
      var hasLazyLoading = img.getAttribute('loading') === 'lazy';

      // Find resource entry for this image
      var imgResource = null;
      for (var n = 0; n < resourcesByType.img.length; n++) {
        if (resourcesByType.img[n].url.indexOf(imgSrc) !== -1) {
          imgResource = resourcesByType.img[n];
          break;
        }
      }

      var imgSize = imgResource ? imgResource.size : 0;
      var isLarge = imgSize > 500 * 1024; // >500KB
      var isOversized = naturalWidth > displayWidth * 1.5 && displayWidth > 0;

      if (isLarge || (isOversized && !hasLazyLoading)) {
        unoptimizedCount++;
        if (unoptimizedCount <= 10) { // Limit to 10 entries
          fixable.push({
            id: 'img-unopt-' + unoptimizedCount,
            type: 'unoptimized-image',
            severity: isLarge ? 'error' : 'warning',
            selector: getSelector(img),
            size: imgSize > 0 ? formatBytes(imgSize) : 'unknown',
            dimensions: naturalWidth + 'x' + naturalHeight,
            impact: isLarge ? 7 : 5,
            fix: 'Resize to ' + displayWidth + 'px width, convert to WebP, add loading="lazy"'
          });
        }
      }
    }

    // 4. Check for font loading optimization
    checksRun.push('font-loading');
    var fontFaces = [];
    try {
      if (document.fonts && document.fonts.forEach) {
        document.fonts.forEach(function(font) {
          fontFaces.push(font);
        });
      }
    } catch (e) {
      // Font API may not be available
    }

    var hasSwap = false;
    for (var p = 0; p < document.styleSheets.length; p++) {
      try {
        var rules = document.styleSheets[p].cssRules || [];
        for (var q = 0; q < rules.length; q++) {
          if (rules[q].cssText && rules[q].cssText.indexOf('@font-face') !== -1) {
            if (rules[q].cssText.indexOf('font-display') !== -1) {
              hasSwap = true;
            }
          }
        }
      } catch (e) {
        // Cross-origin stylesheets can't be accessed
      }
    }

    if (fontFaces.length > 0 && !hasSwap) {
      informational.push({
        id: 'font-display',
        type: 'font-loading',
        severity: 'info',
        message: 'Consider adding font-display: swap to @font-face rules for better perceived performance'
      });
    }

    // 5. Analyze slowest resources
    var allResources = [];
    Object.keys(resourcesByType).forEach(function(type) {
      allResources = allResources.concat(resourcesByType[type]);
    });
    allResources.sort(function(a, b) { return b.duration - a.duration; });

    var slowestResources = allResources.slice(0, 10).map(function(r) {
      return {
        url: detailLevel === 'full' ? r.url : truncateUrl(r.url, maxUrlLength),
        duration: r.duration,
        type: r.type,
        size: r.size
      };
    });

    // 6. Detect large payloads
    checksRun.push('large-payloads');
    var largePayloads = allResources.filter(function(r) {
      return r.size > 100 * 1024; // >100KB
    });

    if (largePayloads.length > 0) {
      for (var s = 0; s < Math.min(5, largePayloads.length); s++) {
        var large = largePayloads[s];
        informational.push({
          id: 'large-payload-' + s,
          type: 'large-payload',
          severity: 'warning',
          url: detailLevel === 'full' ? large.url : truncateUrl(large.url, 80),
          size: formatBytes(large.size),
          message: 'Large resource: ' + formatBytes(large.size)
        });
      }
    }

    // 7. Third-party impact analysis
    checksRun.push('third-party-impact');
    var thirdPartyImpact = [];
    Object.keys(thirdPartyMap).forEach(function(domain) {
      thirdPartyImpact.push({
        domain: domain,
        requests: thirdPartyMap[domain].requests,
        totalTime: thirdPartyMap[domain].totalTime,
        totalSize: thirdPartyMap[domain].totalSize
      });
    });
    thirdPartyImpact.sort(function(a, b) { return b.totalTime - a.totalTime; });

    // === GENERATE ACTIONS ===

    if (lcp && lcp > 2500) {
      var lcpSeverity = lcp > 4000 ? 'poor' : 'needs-improvement';
      var lcpBlocking = blockingScripts.length > 0 ? ' (blocking scripts delay LCP by ~' + (blockingScripts.length * 300) + 'ms)' : '';
      actions.push('Improve LCP (' + (lcp / 1000).toFixed(1) + 's, ' + lcpSeverity + ')' + lcpBlocking);
    }

    if (blockingScripts.length > 0) {
      actions.push('Defer ' + blockingScripts.length + ' render-blocking script' + (blockingScripts.length > 1 ? 's' : '') + ' (estimated ~' + (blockingScripts.length * 300) + 'ms savings)');
    }

    if (unoptimizedCount > 0) {
      actions.push('Optimize ' + unoptimizedCount + ' image' + (unoptimizedCount > 1 ? 's' : '') + ': resize, compress, lazy load');
    }

    if (slowestResources.length > 0 && slowestResources[0].duration > 1000) {
      var slowest = slowestResources[0];
      var slowestDomain = getDomain(slowest.url);
      actions.push('Investigate slow resource: ' + slowestDomain + ' (' + (slowest.duration / 1000).toFixed(1) + 's)');
    }

    if (thirdPartyImpact.length > 0 && thirdPartyImpact[0].totalTime > 500) {
      actions.push('Review third-party impact from ' + thirdPartyImpact[0].domain + ' (' + thirdPartyImpact[0].requests + ' requests, ' + thirdPartyImpact[0].totalTime + 'ms)');
    }

    // === CALCULATE SCORE ===

    var score = 100;

    // Deduct for Core Web Vitals
    if (coreWebVitals.lcp.rating === 'poor') score -= 20;
    else if (coreWebVitals.lcp.rating === 'needs-improvement') score -= 10;

    if (coreWebVitals.fcp.rating === 'poor') score -= 15;
    else if (coreWebVitals.fcp.rating === 'needs-improvement') score -= 7;

    if (coreWebVitals.cls.rating === 'poor') score -= 15;
    else if (coreWebVitals.cls.rating === 'needs-improvement') score -= 7;

    // Deduct for issues
    var errorCount = fixable.filter(function(f) { return f.severity === 'error'; }).length;
    var warningCount = fixable.filter(function(f) { return f.severity === 'warning'; }).length;

    score -= errorCount * 5;
    score -= warningCount * 2;
    score = Math.max(0, Math.min(100, score));

    // Grade
    var grade = 'F';
    if (score >= 90) grade = 'A';
    else if (score >= 80) grade = 'B';
    else if (score >= 70) grade = 'C';
    else if (score >= 60) grade = 'D';
    else if (score >= 50) grade = 'E';

    // === GENERATE SUMMARY ===

    var summaryParts = [];
    if (lcp) {
      summaryParts.push('LCP ' + (lcp / 1000).toFixed(1) + 's (' + coreWebVitals.lcp.rating + ')');
    }
    if (blockingScripts.length > 0) {
      summaryParts.push(blockingScripts.length + ' render-blocking script' + (blockingScripts.length > 1 ? 's' : ''));
    }
    if (unoptimizedCount > 0) {
      summaryParts.push(unoptimizedCount + ' unoptimized image' + (unoptimizedCount > 1 ? 's' : ''));
    }
    var summary = summaryParts.join('. ') || 'Performance audit complete';

    // === STATS ===

    var stats = {
      errors: errorCount,
      warnings: warningCount,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    // === BUILD RESPONSE ===

    var response = {
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      coreWebVitals: coreWebVitals,
      fixable: fixable,
      informational: informational,
      slowestResources: slowestResources,
      thirdPartyImpact: thirdPartyImpact.slice(0, 10),
      actions: actions,
      stats: stats
    };

    // Legacy compatibility: include detailLevel
    response.detailLevel = detailLevel;

    // Memory info if available
    if (perf.memory) {
      response.memory = {
        usedJSHeapSize: Math.round(perf.memory.usedJSHeapSize / 1024 / 1024),
        totalJSHeapSize: Math.round(perf.memory.totalJSHeapSize / 1024 / 1024),
        jsHeapSizeLimit: Math.round(perf.memory.jsHeapSizeLimit / 1024 / 1024)
      };
    }

    return response;
  }

  // === UNIFIED AUDIT: auditAll ===
  // Runs all audits and provides a unified report with prioritized actions
  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   includeAccessibility: boolean (default: true) - requires async
  function auditAll(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var includeAccessibility = options.includeAccessibility !== false;

    // Run all synchronous audits
    var domResult = auditDOMComplexity({ detailLevel: detailLevel });
    var cssResult = auditCSS({ detailLevel: detailLevel });
    var securityResult = auditSecurity({ detailLevel: detailLevel });
    var seoResult = auditPageQuality({ detailLevel: detailLevel });
    var performanceResult = auditPerformance({ detailLevel: detailLevel });

    // Combine results
    function combineResults(accessibilityResult) {
      var audits = {
        dom: {
          score: domResult.score,
          grade: domResult.grade,
          errors: domResult.stats.errors,
          warnings: domResult.stats.warnings,
          hotspots: domResult.hotspots ? domResult.hotspots.length : 0
        },
        css: {
          score: cssResult.score,
          grade: cssResult.grade,
          errors: cssResult.stats.errors,
          warnings: cssResult.stats.warnings,
          inlineStyles: cssResult.metrics ? cssResult.metrics.inlineStyleCount : 0
        },
        security: {
          score: securityResult.score,
          grade: securityResult.grade,
          critical: securityResult.stats.critical || 0,
          errors: securityResult.stats.errors,
          warnings: securityResult.stats.warnings
        },
        seo: {
          score: seoResult.score,
          grade: seoResult.grade,
          errors: seoResult.stats.errors,
          warnings: seoResult.stats.warnings
        },
        performance: {
          score: performanceResult.score,
          grade: performanceResult.grade,
          coreWebVitals: performanceResult.coreWebVitals
        }
      };

      if (accessibilityResult) {
        audits.accessibility = {
          score: accessibilityResult.score,
          grade: accessibilityResult.grade,
          errors: accessibilityResult.stats ? accessibilityResult.stats.errors : 0,
          warnings: accessibilityResult.stats ? accessibilityResult.stats.warnings : 0
        };
      }

      // Calculate overall score (weighted average)
      var weights = {
        security: 1.5,    // Security is critical
        accessibility: 1.3,
        performance: 1.2,
        seo: 1.0,
        dom: 0.8,
        css: 0.7
      };

      var totalWeight = 0;
      var weightedSum = 0;

      for (var auditName in audits) {
        var weight = weights[auditName] || 1.0;
        weightedSum += audits[auditName].score * weight;
        totalWeight += weight;
      }

      var overallScore = Math.round(weightedSum / totalWeight);

      // Overall grade
      var grade = 'F';
      if (overallScore >= 90) grade = 'A';
      else if (overallScore >= 80) grade = 'B';
      else if (overallScore >= 70) grade = 'C';
      else if (overallScore >= 60) grade = 'D';

      // Collect all fixable issues with audit source
      var allFixable = [];

      function addIssues(issues, auditName) {
        if (!issues) return;
        for (var i = 0; i < issues.length; i++) {
          var issue = issues[i];
          allFixable.push({
            audit: auditName,
            id: issue.id,
            type: issue.type,
            severity: issue.severity,
            impact: issue.impact || 5,
            selector: issue.selector,
            message: issue.message,
            fix: issue.fix
          });
        }
      }

      addIssues(domResult.fixable, 'dom');
      addIssues(cssResult.fixable, 'css');
      addIssues(securityResult.fixable, 'security');
      addIssues(seoResult.fixable, 'seo');
      addIssues(performanceResult.fixable, 'performance');
      if (accessibilityResult && accessibilityResult.fixable) {
        addIssues(accessibilityResult.fixable, 'accessibility');
      }

      // Sort by impact (highest first), then by severity
      var severityOrder = { critical: 0, error: 1, warning: 2, info: 3 };
      allFixable.sort(function(a, b) {
        if (b.impact !== a.impact) return b.impact - a.impact;
        return (severityOrder[a.severity] || 4) - (severityOrder[b.severity] || 4);
      });

      // Generate prioritized actions (top 10)
      var prioritizedActions = [];
      for (var j = 0; j < Math.min(10, allFixable.length); j++) {
        var item = allFixable[j];
        prioritizedActions.push({
          priority: j + 1,
          audit: item.audit,
          action: item.fix || item.message,
          impact: item.impact,
          severity: item.severity
        });
      }

      // Critical issues (impact >= 8 or severity critical/error)
      var criticalIssues = allFixable.filter(function(item) {
        return item.impact >= 8 || item.severity === 'critical' || item.severity === 'error';
      }).slice(0, 5);

      // Quick wins (impact >= 5 and simple fixes)
      var quickWins = allFixable.filter(function(item) {
        return item.impact >= 5 && item.fix && item.fix.length < 100;
      }).slice(0, 5);

      // Generate summary
      var criticalCount = criticalIssues.length;
      var highPriorityCount = allFixable.filter(function(i) { return i.impact >= 7; }).length;
      var summaryParts = ['Overall score ' + overallScore + '/100'];
      if (criticalCount > 0) {
        summaryParts.push(criticalCount + ' critical issue' + (criticalCount > 1 ? 's' : ''));
      }
      if (highPriorityCount > 0) {
        summaryParts.push(highPriorityCount + ' high priority fix' + (highPriorityCount > 1 ? 'es' : ''));
      }
      var summary = summaryParts.join('. ');

      // Build response
      var response = {
        summary: summary,
        overallScore: overallScore,
        grade: grade,
        checkedAt: new Date().toISOString(),
        audits: audits,
        prioritizedActions: prioritizedActions,
        criticalIssues: criticalIssues,
        quickWins: quickWins,
        stats: {
          totalIssues: allFixable.length,
          critical: criticalIssues.length,
          highPriority: highPriorityCount
        }
      };

      // Include full audit results in full mode
      if (detailLevel === 'full') {
        response.fullResults = {
          dom: domResult,
          css: cssResult,
          security: securityResult,
          seo: seoResult,
          performance: performanceResult
        };
        if (accessibilityResult) {
          response.fullResults.accessibility = accessibilityResult;
        }
      }

      return response;
    }

    // If accessibility is included, we need to return a Promise
    if (includeAccessibility && window.__devtool_accessibility && window.__devtool_accessibility.auditAccessibility) {
      return window.__devtool_accessibility.auditAccessibility({ mode: 'standard' })
        .then(function(accessibilityResult) {
          return combineResults(accessibilityResult);
        })
        .catch(function(err) {
          console.warn('Accessibility audit failed:', err);
          return combineResults(null);
        });
    }

    // Synchronous path (no accessibility)
    return Promise.resolve(combineResults(null));
  }

  // Export audit functions
  window.__devtool_audit = {
    auditDOMComplexity: auditDOMComplexity,
    auditCSS: auditCSS,
    auditSecurity: auditSecurity,
    auditPageQuality: auditPageQuality,
    auditPerformance: auditPerformance,
    auditAll: auditAll
  };
})();

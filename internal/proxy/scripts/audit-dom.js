// DOM complexity audit

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  function auditDOMComplexity(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var raw = options.raw === true; // Default: false (AI-optimized format)
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

    // Helper: Truncate HTML for context
    function truncateHtml(el, maxLen) {
      if (!el) return '';
      maxLen = maxLen || 120;
      var html = el.outerHTML || '';
      if (html.length <= maxLen) return html;
      // Keep opening tag and truncate
      var tagEnd = html.indexOf('>');
      if (tagEnd > 0 && tagEnd < maxLen - 10) {
        return html.substring(0, tagEnd + 1) + '...</' + el.tagName.toLowerCase() + '>';
      }
      return html.substring(0, maxLen) + '...';
    }

    // Helper: Get tag hierarchy for context
    function getTagHierarchy(el, depth) {
      depth = depth || 3;
      var path = [];
      var current = el;
      while (current && current.tagName && path.length < depth) {
        path.unshift(current.tagName.toLowerCase());
        current = current.parentElement;
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
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(el)) continue;
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
      var idEl = elementsWithId[j];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(idEl)) continue;
      var id = idEl.id;
      if (!ids[id]) {
        ids[id] = [];
      }
      ids[id].push(idEl);
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
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(list)) continue;
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
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(table)) continue;
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
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(form)) continue;
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
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(handlerEl)) continue;
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

    // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
    // Returns grouped data optimized for AI processing - no pre-generated actions
    if (!raw) {
      // Build rich raw data for AI interpretation
      var rawDuplicateIds = [];
      for (var dupKey in duplicateIdMap) {
        var elems = duplicateIdMap[dupKey];
        rawDuplicateIds.push({
          id: dupKey,
          count: elems.length,
          instances: elems.slice(0, 5).map(function(el) {
            return {
              selector: getSelectorPath(el),
              element: truncateHtml(el, 100),
              context: getTagHierarchy(el, 4)
            };
          })
        });
      }

      // Build raw hotspot data with more context
      var rawHotspots = hotspots.map(function(h) {
        var el = document.querySelector(h.selector.split(' > ').pop()) || document.body;
        var childTags = {};
        var children = el.children;
        for (var ci = 0; ci < Math.min(children.length, 20); ci++) {
          var tag = children[ci].tagName.toLowerCase();
          childTags[tag] = (childTags[tag] || 0) + 1;
        }
        return {
          selector: h.selector,
          descendants: h.descendants,
          depth: h.depth,
          childTagDistribution: childTags,
          hasRepeatingPattern: Object.values(childTags).some(function(c) { return c > 5; })
        };
      });

      // Group fixable issues by type for AI processing
      var issuesByType = {};
      for (var fi = 0; fi < fixable.length; fi++) {
        var issue = fixable[fi];
        if (!issuesByType[issue.type]) {
          issuesByType[issue.type] = [];
        }
        issuesByType[issue.type].push({
          selector: issue.selector,
          severity: issue.severity,
          impact: issue.impact,
          // Type-specific data
          childCount: issue.childCount,
          depth: issue.depth,
          itemCount: issue.itemCount,
          rows: issue.rows,
          inputCount: issue.inputCount,
          attributeCount: issue.attributeCount
        });
      }

      return {
        audit: 'dom',
        summary: summary,
        score: score,
        grade: grade,
        checkedAt: new Date().toISOString(),
        stats: {
          errors: errorCount,
          warnings: warningCount,
          info: infoCount,
          totalIssues: fixable.length
        },
        // Raw data for AI interpretation - no pre-generated actions
        raw: {
          metrics: {
            totalElements: elements.length,
            maxDepth: maxDepth,
            averageChildren: Math.round(averageChildren * 10) / 10,
            forms: document.forms.length,
            tables: document.querySelectorAll('table').length,
            lists: document.querySelectorAll('ul, ol').length
          },
          duplicateIds: rawDuplicateIds,
          hotspots: rawHotspots,
          issuesByType: issuesByType
        },
        // Hints for AI - what to look for in codebase
        automationHints: {
          lookFor: [
            'component patterns for extracting large subtrees',
            'virtualization libraries (react-window, react-virtualized)',
            'existing ID naming conventions',
            'form wizard or multi-step patterns'
          ],
          suggestionsNeeded: [
            rawDuplicateIds.length > 0 ? 'rename strategy for ' + rawDuplicateIds.length + ' duplicate IDs' : null,
            hotspots.length > 0 ? 'component extraction for ' + hotspots.length + ' large subtrees' : null,
            issuesByType['large-list'] ? 'virtualization for large lists' : null,
            issuesByType['large-table'] ? 'pagination for large tables' : null
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

  window.__devtool_audit_dom = {
    auditDOMComplexity: auditDOMComplexity
  };
})();

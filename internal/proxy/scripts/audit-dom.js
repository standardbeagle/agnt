// DOM complexity audit

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
    children: 10,        // direct children above this -> excessive-children
    depth: 15,           // nesting depth above this -> excessive-depth
    attributes: 10,      // attributes above this -> excessive-attributes
    listItems: 50,       // list items above this -> large-list
    tableRows: 100,      // table rows above this -> large-table
    totalElements: 1500  // total elements above this -> element-count info
  };

  // Hard cap on the number of elements scanned in one pass. The exec bridge is
  // synchronous, so instead of chunk+yield we cap and flag.
  var MAX_SCAN_ELEMENTS = 5000;

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  //   thresholds: object - shallow-merged over DEFAULT_THRESHOLDS
  function auditDOMComplexity(options) {
    options = options || {};
    var auditUtils = window.__devtool_audit_utils;
    var detailLevel = options.detailLevel || 'compact';
    var raw = options.raw === true; // Default: false (AI-optimized format)
    var TH = auditUtils.mergeThresholds(DEFAULT_THRESHOLDS, options.thresholds);
    var allElements = document.querySelectorAll('*');
    var totalElementCount = allElements.length;
    var capped = totalElementCount > MAX_SCAN_ELEMENTS;
    var scanCount = capped ? MAX_SCAN_ELEMENTS : totalElementCount;

    var getSelector = auditUtils.getSelector;
    var getSelectorPath = auditUtils.getSelectorPath;

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

    // === DESCENDANT COUNTS: single post-order pass ===
    // querySelectorAll returns document (pre-)order, so iterating in reverse
    // guarantees every child is processed before its parent. Each element's
    // subtree size is 1 + sum of its children's subtree sizes — O(n) total
    // instead of the old per-element querySelectorAll('*') O(n^2).
    var descendantCounts = new Map();
    for (var dc = scanCount - 1; dc >= 0; dc--) {
      var dcEl = allElements[dc];
      var dcCount = 0;
      for (var dcChild = dcEl.firstElementChild; dcChild; dcChild = dcChild.nextElementSibling) {
        dcCount += 1 + (descendantCounts.get(dcChild) || 0);
      }
      descendantCounts.set(dcEl, dcCount);
    }

    function countDescendants(el) {
      return descendantCounts.get(el) || 0;
    }

    // === METRICS COLLECTION ===
    var maxDepth = 0;
    var totalDepth = 0;
    var totalChildren = 0;
    var elementData = [];

    for (var i = 0; i < scanCount; i++) {
      var el = allElements[i];
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

    var averageChildren = scanCount > 0 ? totalChildren / scanCount : 0;

    // === ISSUE DETECTION ===
    var fixable = [];
    var informational = [];
    var hotspots = [];

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
        var dupMsg = 'Duplicate ID "' + dupId + '" found ' + ids[dupId].length + ' times';
        var dupSel = '#' + dupId;
        var dupID = computeFindingID('duplicate-id', dupSel, dupMsg);
        registerFinding(dupID, dupSel);
        fixable.push({
          id: dupID,
          type: 'duplicate-id',
          severity: 'error',
          duplicateId: dupId,
          count: ids[dupId].length,
          selector: dupSel,
          selectors: selectors,
          impact: 8,
          fix: 'Ensure all IDs are unique - rename duplicates'
        });
      }
    }

    // 2. Excessive children
    for (var k = 0; k < elementData.length; k++) {
      var data = elementData[k];
      if (data.childCount > TH.children) {
        var childSel = getSelectorPath(data.element);
        var childFix = data.childCount > TH.children * 5
          ? 'Consider pagination or virtualization'
          : 'Consider componentization or grouping';
        var childMsg = data.childCount + ' direct children';
        var childID = computeFindingID('excessive-children', childSel, childMsg);
        registerFinding(childID, childSel);
        fixable.push({
          id: childID,
          type: 'excessive-children',
          severity: data.childCount > TH.children * 5 ? 'error' : 'warning',
          selector: childSel,
          childCount: data.childCount,
          impact: Math.min(10, Math.floor(data.childCount / TH.children)),
          fix: childFix
        });
      }
    }

    // 3. Deep nesting
    for (var m = 0; m < elementData.length; m++) {
      var deepData = elementData[m];
      if (deepData.depth > TH.depth) {
        var deepSel = getSelectorPath(deepData.element);
        var deepMsg = deepData.depth + ' levels deep';
        var deepID = computeFindingID('excessive-depth', deepSel, deepMsg);
        registerFinding(deepID, deepSel);
        fixable.push({
          id: deepID,
          type: 'excessive-depth',
          severity: deepData.depth > TH.depth + 5 ? 'error' : 'warning',
          selector: deepSel,
          depth: deepData.depth,
          impact: Math.min(10, Math.floor(deepData.depth / 3)),
          fix: 'Flatten nesting or extract to component'
        });
      }
    }

    // 4. Excessive attributes
    for (var n = 0; n < elementData.length; n++) {
      var attrData = elementData[n];
      if (attrData.attributeCount > TH.attributes) {
        var attrSel = getSelectorPath(attrData.element);
        var attrMsg = attrData.attributeCount + ' attributes';
        var attrID = computeFindingID('excessive-attributes', attrSel, attrMsg);
        registerFinding(attrID, attrSel);
        fixable.push({
          id: attrID,
          type: 'excessive-attributes',
          severity: 'warning',
          selector: attrSel,
          attributeCount: attrData.attributeCount,
          impact: Math.min(7, Math.floor(attrData.attributeCount / 2)),
          fix: 'Simplify element or use CSS classes instead of inline attributes'
        });
      }
    }

    // 5. Large lists without virtualization hints
    var lists = document.querySelectorAll('ul, ol');
    for (var p = 0; p < lists.length; p++) {
      var list = lists[p];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(list)) continue;
      var itemCount = list.querySelectorAll(':scope > li').length;
      if (itemCount > TH.listItems) {
        var listSel = getSelectorPath(list);
        var listMsg = itemCount + ' list items';
        var listID = computeFindingID('large-list', listSel, listMsg);
        registerFinding(listID, listSel);
        fixable.push({
          id: listID,
          type: 'large-list',
          severity: itemCount > TH.listItems * 4 ? 'error' : 'warning',
          selector: listSel,
          itemCount: itemCount,
          impact: Math.min(9, Math.floor(itemCount / 25)),
          fix: 'Consider virtualization (e.g., react-window) or pagination'
        });
      }
    }

    // 6. Large tables
    var tables = document.querySelectorAll('table');
    for (var q = 0; q < tables.length; q++) {
      var table = tables[q];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(table)) continue;
      var rows = table.querySelectorAll('tr');
      var cells = table.querySelectorAll('td, th');
      if (rows.length > TH.tableRows) {
        var tableSel = getSelectorPath(table);
        var tableMsg = rows.length + ' table rows';
        var tableID = computeFindingID('large-table', tableSel, tableMsg);
        registerFinding(tableID, tableSel);
        fixable.push({
          id: tableID,
          type: 'large-table',
          severity: rows.length > TH.tableRows * 5 ? 'error' : 'warning',
          selector: tableSel,
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
        var formSel = getSelectorPath(form);
        var formMsg = inputs.length + ' form inputs';
        var formID = computeFindingID('large-form', formSel, formMsg);
        registerFinding(formID, formSel);
        fixable.push({
          id: formID,
          type: 'large-form',
          severity: 'warning',
          selector: formSel,
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
        var handlerSel = getSelectorPath(handlerEl);
        var handlerMsg = handlerTypes.join(',') + ' inline handlers';
        var handlerID = computeFindingID('excessive-handlers', handlerSel, handlerMsg);
        registerFinding(handlerID, handlerSel);
        fixable.push({
          id: handlerID,
          type: 'excessive-handlers',
          severity: 'warning',
          selector: handlerSel,
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

    // hotspotElements is a parallel array of live element refs; kept out of
    // the returned hotspot objects so responses stay JSON-serializable.
    var hotspotElements = [];
    for (var u = 0; u < Math.min(5, subtreeData.length); u++) {
      var subtree = subtreeData[u];
      var recommendation = 'Consider lazy loading or code splitting';
      if (subtree.descendants > 500) {
        recommendation = 'Critical: Consider virtualization or lazy loading';
      } else if (subtree.descendants > 200) {
        recommendation = 'Consider virtualization or lazy loading';
      }
      hotspotElements.push(subtree.element);
      hotspots.push({
        selector: getSelectorPath(subtree.element),
        descendants: subtree.descendants,
        depth: subtree.depth,
        recommendation: recommendation
      });
    }

    // 10. Informational: Total element count
    if (totalElementCount > TH.totalElements) {
      var elemMsg = totalElementCount + ' elements exceeds recommended ' + TH.totalElements + ' for optimal performance';
      informational.push({
        id: computeFindingID('element-count', 'document', elemMsg),
        type: 'element-count',
        severity: totalElementCount > TH.totalElements * 2 ? 'warning' : 'info',
        message: elemMsg,
        current: totalElementCount,
        recommended: TH.totalElements
      });
    }

    // 11. Informational: Max depth
    if (maxDepth > TH.depth) {
      var depthMsg = 'Maximum nesting depth of ' + maxDepth + ' exceeds recommended ' + TH.depth;
      informational.push({
        id: computeFindingID('max-depth', 'document', depthMsg),
        type: 'max-depth',
        severity: maxDepth > TH.depth + 5 ? 'warning' : 'info',
        message: depthMsg,
        current: maxDepth,
        recommended: TH.depth
      });
    }

    // === SCORING ===
    var score = 100;

    // Penalties
    score -= Math.min(20, Math.floor((totalElementCount - TH.totalElements) / 100)); // Element count penalty
    score -= Math.min(15, Math.floor((maxDepth - TH.depth) / 2)); // Depth penalty
    score -= Math.min(10, Object.keys(duplicateIdMap).length * 5); // Duplicate ID penalty
    score -= Math.min(20, fixable.filter(function(f) { return f.severity === 'error'; }).length * 4); // Error penalty
    score -= Math.min(15, fixable.filter(function(f) { return f.severity === 'warning'; }).length * 2); // Warning penalty
    score = Math.max(0, Math.min(100, score));

    // Grade (canonical shared A-F scale)
    var grade = auditUtils.calculateGrade(score);

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
    summaryParts.push('(' + totalElementCount + ' elements)');

    if (capped) {
      summaryParts.push('scan capped at ' + MAX_SCAN_ELEMENTS + ' elements');
    }

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

      // Build raw hotspot data with more context using the element refs
      // captured at collection time (re-querying by the tail of the selector
      // path mis-targeted unrelated elements).
      var rawHotspots = hotspots.map(function(h, hi) {
        var el = hotspotElements[hi];
        if (!el || !el.isConnected) {
          try {
            el = document.querySelector(h.selector) || document.body;
          } catch (e) {
            el = document.body;
          }
        }
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
        capped: capped,
        stats: {
          errors: errorCount,
          warnings: warningCount,
          info: infoCount,
          totalIssues: fixable.length
        },
        // Raw data for AI interpretation - no pre-generated actions
        raw: {
          metrics: {
            totalElements: totalElementCount,
            scannedElements: scanCount,
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
      capped: capped,
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
        totalElements: totalElementCount,
        scannedElements: scanCount,
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

// Accessibility primitives for DevTool
// A11y information, contrast checking, tab order

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  function getA11yInfo(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var role = el.getAttribute('role') || getImplicitRole(el);

      return {
        role: role,
        ariaLabel: el.getAttribute('aria-label'),
        ariaLabelledBy: el.getAttribute('aria-labelledby'),
        ariaDescribedBy: el.getAttribute('aria-describedby'),
        ariaHidden: el.getAttribute('aria-hidden'),
        ariaExpanded: el.getAttribute('aria-expanded'),
        ariaDisabled: el.getAttribute('aria-disabled'),
        tabIndex: el.tabIndex,
        focusable: isFocusable(el),
        accessibleName: getAccessibleName(el)
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getImplicitRole(el) {
    var tag = el.tagName.toLowerCase();
    var roleMap = {
      'a': el.href ? 'link' : null,
      'article': 'article',
      'aside': 'complementary',
      'button': 'button',
      'footer': 'contentinfo',
      'form': 'form',
      'header': 'banner',
      'img': 'img',
      'input': getInputRole(el),
      'li': 'listitem',
      'main': 'main',
      'nav': 'navigation',
      'ol': 'list',
      'section': 'region',
      'select': 'combobox',
      'table': 'table',
      'textarea': 'textbox',
      'ul': 'list'
    };
    return roleMap[tag] || null;
  }

  function getInputRole(el) {
    var type = (el.type || 'text').toLowerCase();
    var inputRoles = {
      'button': 'button',
      'checkbox': 'checkbox',
      'email': 'textbox',
      'number': 'spinbutton',
      'radio': 'radio',
      'range': 'slider',
      'search': 'searchbox',
      'submit': 'button',
      'tel': 'textbox',
      'text': 'textbox',
      'url': 'textbox'
    };
    return inputRoles[type] || 'textbox';
  }

  function isFocusable(el) {
    if (el.disabled) return false;
    if (el.tabIndex < 0) return false;

    var tag = el.tagName.toLowerCase();
    var focusableTags = ['a', 'button', 'input', 'select', 'textarea'];

    if (focusableTags.indexOf(tag) !== -1) return true;
    if (el.tabIndex >= 0) return true;
    if (el.contentEditable === 'true') return true;

    return false;
  }

  function getAccessibleName(el) {
    // Try aria-label first
    var ariaLabel = el.getAttribute('aria-label');
    if (ariaLabel) return ariaLabel;

    // Try aria-labelledby
    var labelledBy = el.getAttribute('aria-labelledby');
    if (labelledBy) {
      var labelEl = document.getElementById(labelledBy);
      if (labelEl) return labelEl.textContent.trim();
    }

    // Try associated label
    if (el.id) {
      var label = document.querySelector('label[for="' + el.id + '"]');
      if (label) return label.textContent.trim();
    }

    // Try alt attribute (for images)
    var alt = el.getAttribute('alt');
    if (alt) return alt;

    // Try title attribute
    var title = el.getAttribute('title');
    if (title) return title;

    // Try text content (for buttons, links)
    if (['button', 'a'].indexOf(el.tagName.toLowerCase()) !== -1) {
      return el.textContent.trim();
    }

    return null;
  }

  function getContrast(foreground, background) {
    function getLuminance(color) {
      // Parse rgb/rgba color string
      var match = color.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
      if (!match) return 0;

      var rgb = [parseInt(match[1]), parseInt(match[2]), parseInt(match[3])];

      for (var i = 0; i < 3; i++) {
        var c = rgb[i] / 255;
        rgb[i] = c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
      }

      return 0.2126 * rgb[0] + 0.7152 * rgb[1] + 0.0722 * rgb[2];
    }

    var lum1 = getLuminance(foreground);
    var lum2 = getLuminance(background);

    var lighter = Math.max(lum1, lum2);
    var darker = Math.min(lum1, lum2);

    var ratio = (lighter + 0.05) / (darker + 0.05);

    return {
      ratio: Math.round(ratio * 100) / 100,
      passesAA: ratio >= 4.5,
      passesAALarge: ratio >= 3,
      passesAAA: ratio >= 7,
      passesAAALarge: ratio >= 4.5
    };
  }

  function getTabOrder() {
    var focusable = document.querySelectorAll(
      'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])'
    );

    var elements = [];
    for (var i = 0; i < focusable.length; i++) {
      var el = focusable[i];
      if (!el.disabled && el.offsetParent !== null) {
        elements.push({
          element: el,
          selector: utils.generateSelector(el),
          tabIndex: el.tabIndex,
          accessibleName: getAccessibleName(el)
        });
      }
    }

    // Sort by tabindex (0 comes last among positive values)
    elements.sort(function(a, b) {
      if (a.tabIndex === b.tabIndex) return 0;
      if (a.tabIndex === 0) return 1;
      if (b.tabIndex === 0) return -1;
      return a.tabIndex - b.tabIndex;
    });

    return { elements: elements, count: elements.length };
  }

  function getScreenReaderText(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var text = [];

      // Accessible name
      var name = getAccessibleName(el);
      if (name) text.push('Name: ' + name);

      // Role
      var role = el.getAttribute('role') || getImplicitRole(el);
      if (role) text.push('Role: ' + role);

      // State
      if (el.getAttribute('aria-expanded')) {
        text.push(el.getAttribute('aria-expanded') === 'true' ? 'expanded' : 'collapsed');
      }
      if (el.getAttribute('aria-checked')) {
        text.push(el.getAttribute('aria-checked') === 'true' ? 'checked' : 'not checked');
      }
      if (el.getAttribute('aria-selected')) {
        text.push(el.getAttribute('aria-selected') === 'true' ? 'selected' : 'not selected');
      }
      if (el.disabled) {
        text.push('disabled');
      }

      // Description
      var describedBy = el.getAttribute('aria-describedby');
      if (describedBy) {
        var descEl = document.getElementById(describedBy);
        if (descEl) text.push('Description: ' + descEl.textContent.trim());
      }

      return {
        text: text.join(', '),
        parts: text
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  // Basic accessibility audit (fallback when axe-core unavailable)
  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  function runBasicAudit(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var issues = [];

    // Check images without alt
    var images = document.querySelectorAll('img');
    for (var i = 0; i < images.length; i++) {
      var img = images[i];
      if (!img.alt && !img.getAttribute('role')) {
        issues.push({
          type: 'missing-alt',
          severity: 'error',
          element: img,
          selector: utils.generateSelector(img),
          message: 'Image missing alt attribute'
        });
      }
    }

    // Check form inputs without labels (including implicit labels)
    var inputs = document.querySelectorAll('input, select, textarea');
    for (var j = 0; j < inputs.length; j++) {
      var input = inputs[j];
      if (input.type === 'hidden') continue;

      var hasLabel = input.getAttribute('aria-label') ||
                     input.getAttribute('aria-labelledby') ||
                     (input.id && document.querySelector('label[for="' + input.id + '"]')) ||
                     input.closest('label'); // Check for implicit label

      if (!hasLabel) {
        issues.push({
          type: 'missing-label',
          severity: 'error',
          element: input,
          selector: utils.generateSelector(input),
          message: 'Form input missing label'
        });
      }
    }

    // Check buttons without accessible names
    var buttons = document.querySelectorAll('button, [role="button"]');
    for (var k = 0; k < buttons.length; k++) {
      var btn = buttons[k];
      var name = getAccessibleName(btn);
      if (!name) {
        issues.push({
          type: 'missing-button-label',
          severity: 'error',
          element: btn,
          selector: utils.generateSelector(btn),
          message: 'Button missing accessible name'
        });
      }
    }

    // Check links without href or with empty text
    var links = document.querySelectorAll('a');
    for (var l = 0; l < links.length; l++) {
      var link = links[l];
      if (!link.href) {
        issues.push({
          type: 'link-no-href',
          severity: 'warning',
          element: link,
          selector: utils.generateSelector(link),
          message: 'Link missing href attribute'
        });
      }
      if (link.textContent.trim() === '' && !link.getAttribute('aria-label')) {
        issues.push({
          type: 'empty-link',
          severity: 'error',
          element: link,
          selector: utils.generateSelector(link),
          message: 'Link has no text content or aria-label'
        });
      }
    }

    // Calculate counts before any filtering
    var totalCount = issues.length;
    var errorCount = issues.filter(function(i) { return i.severity === 'error'; }).length;
    var warningCount = issues.filter(function(i) { return i.severity === 'warning'; }).length;

    var response = {
      mode: 'basic',
      detailLevel: detailLevel,
      count: totalCount,
      errors: errorCount,
      warnings: warningCount
    };

    if (detailLevel === 'summary') {
      // Summary: counts only
      return response;
    } else if (detailLevel === 'compact') {
      // Compact: limited issues, truncated fields
      var limitedIssues = issues.slice(0, maxIssues);
      response.issues = limitedIssues.map(function(issue) {
        return compactIssue(issue, options);
      });
      if (totalCount > maxIssues) {
        response.truncated = true;
        response.shownIssues = maxIssues;
      }
    } else {
      // Full: all issues
      response.issues = issues;
    }

    return response;
  }

  // Load axe-core from CDN
  function loadAxeCore() {
    return new Promise(function(resolve, reject) {
      // Check if axe is already loaded
      if (window.axe) {
        resolve();
        return;
      }

      var script = document.createElement('script');
      script.src = 'https://cdnjs.cloudflare.com/ajax/libs/axe-core/4.8.3/axe.min.js';
      script.onload = function() {
        resolve();
      };
      script.onerror = function() {
        reject(new Error('Failed to load axe-core from CDN'));
      };
      document.head.appendChild(script);
    });
  }

  // --- Compact Response Helpers ---
  // Helper to truncate strings to reduce token usage
  function truncateString(str, maxLength) {
    if (!str || typeof str !== 'string') return str;
    if (str.length <= maxLength) return str;
    return str.substring(0, maxLength) + '...';
  }

  // Helper to shorten CSS selectors - keeps last 2-3 path elements
  function shortenSelector(selector, maxLength) {
    if (!selector || typeof selector !== 'string') return selector;
    if (selector.length <= maxLength) return selector;

    // Split by common CSS selector separators
    var parts = selector.split(/\s+>\s+|\s+/);
    if (parts.length <= 2) return truncateString(selector, maxLength);

    // Keep last 3 parts
    var shortened = parts.slice(-3).join(' > ');
    if (shortened.length <= maxLength) return '...' + shortened;

    return truncateString(shortened, maxLength);
  }

  // Helper to compact an issue object based on detail level
  function compactIssue(issue, options) {
    var maxHtml = options.maxHtmlLength || 100;
    var maxSelector = options.maxSelectorLength || 80;
    var detailLevel = options.detailLevel || 'compact';

    var compact = {
      type: issue.type,
      severity: issue.severity,
      message: truncateString(issue.message, 200)
    };

    if (issue.selector) {
      compact.selector = shortenSelector(issue.selector, maxSelector);
    }

    if (issue.html) {
      compact.html = truncateString(issue.html, maxHtml);
    }

    if (issue.impact) compact.impact = issue.impact;

    // Only include helpUrl and wcagTags in full mode
    if (detailLevel === 'full') {
      if (issue.helpUrl) compact.helpUrl = issue.helpUrl;
      if (issue.description) compact.description = issue.description;
      if (issue.wcagTags) compact.wcagTags = issue.wcagTags;
    }

    // Include category if present (for fast/comprehensive modes)
    if (issue.category) compact.category = issue.category;

    return compact;
  }

  // Sort issues by severity (critical > serious > moderate > minor)
  function sortIssuesBySeverity(issues) {
    var severityOrder = { critical: 0, serious: 1, moderate: 2, minor: 3 };
    return issues.slice().sort(function(a, b) {
      var aOrder = severityOrder[a.impact] !== undefined ? severityOrder[a.impact] : 4;
      var bOrder = severityOrder[b.impact] !== undefined ? severityOrder[b.impact] : 4;
      return aOrder - bOrder;
    });
  }

  // Run axe-core audit with configurable options
  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   maxHtmlLength: number (default: 100)
  //   maxSelectorLength: number (default: 80)
  //   level: 'a' | 'aa' (default) | 'aaa'
  //   selector: CSS selector to limit audit scope
  function runAxeAudit(options) {
    options = options || {};

    // Response optimization options (defaults favor token efficiency)
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;

    // Default to WCAG 2.1 Level AA
    var runOnly = options.level ?
      ['wcag2a', 'wcag2aa', 'wcag2aaa'].slice(0, options.level === 'aaa' ? 3 : options.level === 'a' ? 1 : 2) :
      ['wcag2a', 'wcag2aa'];

    var axeOptions = {
      runOnly: {
        type: 'tag',
        values: runOnly
      }
    };

    // Allow custom element selection
    if (options.selector) {
      axeOptions.selector = options.selector;
    }

    return window.axe.run(axeOptions).then(function(results) {
      var allIssues = [];

      // Process violations
      results.violations.forEach(function(violation) {
        violation.nodes.forEach(function(node) {
          allIssues.push({
            type: violation.id,
            severity: violation.impact === 'critical' || violation.impact === 'serious' ? 'error' : 'warning',
            impact: violation.impact,
            message: violation.help,
            description: violation.description,
            helpUrl: violation.helpUrl,
            selector: node.target.join(', '),
            html: node.html,
            wcagTags: violation.tags.filter(function(t) { return t.indexOf('wcag') === 0; })
          });
        });
      });

      // Calculate summary counts before filtering
      var totalCount = allIssues.length;
      var errorCount = allIssues.filter(function(i) { return i.severity === 'error'; }).length;
      var warningCount = allIssues.filter(function(i) { return i.severity === 'warning'; }).length;
      var summary = {
        critical: allIssues.filter(function(i) { return i.impact === 'critical'; }).length,
        serious: allIssues.filter(function(i) { return i.impact === 'serious'; }).length,
        moderate: allIssues.filter(function(i) { return i.impact === 'moderate'; }).length,
        minor: allIssues.filter(function(i) { return i.impact === 'minor'; }).length
      };

      // Build response based on detail level
      var response = {
        mode: 'axe-core',
        version: window.axe.version,
        level: options.level || 'aa',
        detailLevel: detailLevel,
        count: totalCount,
        errors: errorCount,
        warnings: warningCount,
        summary: summary
      };

      // Handle detail levels
      if (detailLevel === 'summary') {
        // Summary mode: counts only, no issues array
        response.passesCount = results.passes.length;
        response.incompleteCount = results.incomplete.length;
      } else if (detailLevel === 'compact') {
        // Compact mode: limited issues, truncated fields, no raw arrays
        var sortedIssues = sortIssuesBySeverity(allIssues);
        var limitedIssues = sortedIssues.slice(0, maxIssues);

        response.issues = limitedIssues.map(function(issue) {
          return compactIssue(issue, options);
        });
        response.passesCount = results.passes.length;
        response.incompleteCount = results.incomplete.length;

        if (totalCount > maxIssues) {
          response.truncated = true;
          response.shownIssues = maxIssues;
        }
      } else {
        // Full mode: all issues and raw arrays (backward compatible)
        response.violations = results.violations;
        response.passes = results.passes;
        response.incomplete = results.incomplete;
        response.inapplicable = results.inapplicable;
        response.issues = allIssues;
      }

      return response;
    });
  }

  // Fast improvements mode - quick wins beyond axe
  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  function runFastAudit(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var issues = [];

    // Get all stylesheets
    var cssRules = [];
    try {
      for (var i = 0; i < document.styleSheets.length; i++) {
        var sheet = document.styleSheets[i];
        try {
          if (sheet.cssRules) {
            for (var j = 0; j < sheet.cssRules.length; j++) {
              cssRules.push(sheet.cssRules[j]);
            }
          }
        } catch (e) {
          // Cross-origin stylesheet - skip
        }
      }
    } catch (e) {
      console.warn('Could not access stylesheets:', e);
    }

    // Check for focus indicators
    var focusable = document.querySelectorAll(
      'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])'
    );

    for (var i = 0; i < focusable.length; i++) {
      var el = focusable[i];
      if (el.disabled || el.offsetParent === null) continue;

      // Check if element has focus styles defined
      var hasFocusStyle = false;
      var hiddenOnFocus = false;

      for (var j = 0; j < cssRules.length; j++) {
        var rule = cssRules[j];
        if (rule.selectorText && (
          rule.selectorText.indexOf(':focus') !== -1 ||
          rule.selectorText.indexOf(':focus-visible') !== -1
        )) {
          hasFocusStyle = true;

          // Check if focus style hides element
          if (rule.style.display === 'none' ||
              rule.style.visibility === 'hidden' ||
              rule.style.opacity === '0') {
            hiddenOnFocus = true;
          }
        }
      }

      if (hiddenOnFocus) {
        issues.push({
          type: 'hidden-on-focus',
          severity: 'error',
          selector: utils.generateSelector(el),
          message: 'Element is hidden when focused (display:none, visibility:hidden, or opacity:0)',
          category: 'focus-management'
        });
      }

      // Check for visible focus indicator by comparing styles
      var baseOutline = window.getComputedStyle(el).outline;
      if (!hasFocusStyle && baseOutline === 'none' || baseOutline === '0px none rgb(0, 0, 0)') {
        // Element might not have visible focus indicator
        issues.push({
          type: 'no-focus-indicator',
          severity: 'warning',
          selector: utils.generateSelector(el),
          message: 'Element may lack visible focus indicator',
          category: 'focus-management'
        });
      }
    }

    // Check for color scheme support
    var hasLightMode = false;
    var hasDarkMode = false;

    for (var i = 0; i < cssRules.length; i++) {
      var rule = cssRules[i];
      if (rule instanceof CSSMediaRule) {
        var mediaText = rule.media.mediaText;
        if (mediaText.indexOf('prefers-color-scheme') !== -1) {
          if (mediaText.indexOf('light') !== -1) hasLightMode = true;
          if (mediaText.indexOf('dark') !== -1) hasDarkMode = true;
        }
      }
    }

    if (!hasLightMode && !hasDarkMode) {
      issues.push({
        type: 'no-color-scheme',
        severity: 'warning',
        message: 'No color scheme media queries detected (prefers-color-scheme)',
        category: 'color-scheme'
      });
    }

    // Calculate counts before any filtering
    var totalCount = issues.length;
    var errorCount = issues.filter(function(i) { return i.severity === 'error'; }).length;
    var warningCount = issues.filter(function(i) { return i.severity === 'warning'; }).length;
    var categories = {
      'focus-management': issues.filter(function(i) { return i.category === 'focus-management'; }).length,
      'color-scheme': issues.filter(function(i) { return i.category === 'color-scheme'; }).length
    };

    var response = {
      mode: 'fast',
      detailLevel: detailLevel,
      count: totalCount,
      errors: errorCount,
      warnings: warningCount,
      categories: categories
    };

    if (detailLevel === 'summary') {
      // Summary: counts only
      return response;
    } else if (detailLevel === 'compact') {
      // Compact: limited issues, truncated fields
      var limitedIssues = issues.slice(0, maxIssues);
      response.issues = limitedIssues.map(function(issue) {
        return compactIssue(issue, options);
      });
      if (totalCount > maxIssues) {
        response.truncated = true;
        response.shownIssues = maxIssues;
      }
    } else {
      // Full: all issues
      response.issues = issues;
    }

    return response;
  }

  // Build reverse index of CSS rules and media queries
  function buildMediaQueryIndex() {
    var index = {
      crossOriginSheets: [],
      mediaQueries: {},  // query string -> {rules: [], breakpoints: [], colorSchemes: []}
      classesToQueries: {},  // class name -> [query strings]
      selectorsToQueries: {},  // full selector -> [query strings]
      discoveredBreakpoints: [],
      discoveredColorSchemes: [],
      errors: []
    };

    try {
      for (var i = 0; i < document.styleSheets.length; i++) {
        var sheet = document.styleSheets[i];
        try {
          if (!sheet.cssRules) {
            index.crossOriginSheets.push({
              href: sheet.href || '(inline)',
              error: 'Cannot access cross-origin stylesheet'
            });
            continue;
          }
          parseRulesRecursive(sheet.cssRules, null, index);
        } catch (e) {
          index.errors.push({
            sheet: sheet.href || '(inline)',
            error: e.message
          });
        }
      }
    } catch (e) {
      index.errors.push({
        error: 'Failed to access stylesheets: ' + e.message
      });
    }

    // Deduplicate and sort breakpoints
    var bpSet = {};
    for (var i = 0; i < index.discoveredBreakpoints.length; i++) {
      bpSet[index.discoveredBreakpoints[i]] = true;
    }
    index.discoveredBreakpoints = Object.keys(bpSet).map(function(bp) { return parseInt(bp); }).sort(function(a, b) { return a - b; });

    // Deduplicate color schemes
    var csSet = {};
    for (var i = 0; i < index.discoveredColorSchemes.length; i++) {
      csSet[index.discoveredColorSchemes[i]] = true;
    }
    index.discoveredColorSchemes = Object.keys(csSet);

    return index;
  }

  function parseRulesRecursive(rules, parentMedia, index) {
    for (var i = 0; i < rules.length; i++) {
      var rule = rules[i];

      if (rule instanceof CSSMediaRule) {
        var mediaText = rule.media.mediaText;

        // Extract breakpoints (min-width, max-width)
        var minWidthMatch = mediaText.match(/min-width:\s*(\d+)px/);
        var maxWidthMatch = mediaText.match(/max-width:\s*(\d+)px/);
        if (minWidthMatch) index.discoveredBreakpoints.push(parseInt(minWidthMatch[1]));
        if (maxWidthMatch) index.discoveredBreakpoints.push(parseInt(maxWidthMatch[1]));

        // Extract color schemes
        if (mediaText.indexOf('prefers-color-scheme') !== -1) {
          if (mediaText.indexOf('dark') !== -1) index.discoveredColorSchemes.push('dark');
          if (mediaText.indexOf('light') !== -1) index.discoveredColorSchemes.push('light');
        }

        // Store media query info
        if (!index.mediaQueries[mediaText]) {
          index.mediaQueries[mediaText] = {
            rules: [],
            active: window.matchMedia(mediaText).matches
          };
        }

        // Recurse into media query rules
        parseRulesRecursive(rule.cssRules, mediaText, index);

      } else if (rule instanceof CSSStyleRule) {
        var selectorText = rule.selectorText;

        // Track selector to media query mapping
        if (parentMedia) {
          if (!index.selectorsToQueries[selectorText]) {
            index.selectorsToQueries[selectorText] = [];
          }
          if (index.selectorsToQueries[selectorText].indexOf(parentMedia) === -1) {
            index.selectorsToQueries[selectorText].push(parentMedia);
          }
        }

        // Extract classes from selector and map to media queries
        var classMatches = selectorText.match(/\.\w+/g);
        if (classMatches) {
          for (var j = 0; j < classMatches.length; j++) {
            var className = classMatches[j].substring(1); // Remove leading dot
            if (!index.classesToQueries[className]) {
              index.classesToQueries[className] = [];
            }
            if (parentMedia && index.classesToQueries[className].indexOf(parentMedia) === -1) {
              index.classesToQueries[className].push(parentMedia);
            }
          }
        }

        // Store rule in media query
        if (parentMedia) {
          index.mediaQueries[parentMedia].rules.push(selectorText);
        }
      }
    }
  }

  // Categorize element by media queries that affect it
  function categorizeElement(element, index) {
    var affectingQueries = {};
    var current = element;

    // Walk up the tree collecting media queries
    while (current && current.nodeType === 1) {
      // Check classes
      if (current.classList) {
        for (var i = 0; i < current.classList.length; i++) {
          var className = current.classList[i];
          var queries = index.classesToQueries[className];
          if (queries) {
            for (var j = 0; j < queries.length; j++) {
              affectingQueries[queries[j]] = true;
            }
          }
        }
      }

      // Check if any selectors match this element
      for (var selector in index.selectorsToQueries) {
        try {
          if (current.matches(selector)) {
            var queries = index.selectorsToQueries[selector];
            for (var j = 0; j < queries.length; j++) {
              affectingQueries[queries[j]] = true;
            }
          }
        } catch (e) {
          // Invalid selector, skip
        }
      }

      current = current.parentElement;
    }

    return Object.keys(affectingQueries);
  }

  // Comprehensive mode - CSS rule analysis and test enumeration
  // BETA: This is a premium feature that will require a license after beta
  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   level: 'a' | 'aa' (default) | 'aaa'
  function runComprehensiveAudit(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var issues = [];
    var level = options.level || 'aa';

    // Build media query index
    var index = buildMediaQueryIndex();

    // Flag cross-origin stylesheets
    for (var i = 0; i < index.crossOriginSheets.length; i++) {
      var sheet = index.crossOriginSheets[i];
      issues.push({
        type: 'cross-origin-stylesheet',
        severity: 'warning',
        message: 'Cannot access cross-origin stylesheet: ' + sheet.href,
        href: sheet.href,
        category: 'css-access'
      });
    }

    // Flag stylesheet access errors
    for (var i = 0; i < index.errors.length; i++) {
      var err = index.errors[i];
      issues.push({
        type: 'stylesheet-access-error',
        severity: 'warning',
        message: 'Error accessing stylesheet: ' + err.error,
        sheet: err.sheet,
        category: 'css-access'
      });
    }

    // Get current viewport and color scheme
    var currentWidth = window.innerWidth;
    var currentScheme = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';

    // Contrast thresholds based on WCAG level
    var normalThreshold = level === 'aaa' ? 7 : 4.5;
    var largeThreshold = level === 'aaa' ? 4.5 : 3;

    // Test interactive elements in current state
    var interactive = document.querySelectorAll(
      'a, button, input, select, textarea, [role="button"], [role="link"], [tabindex]:not([tabindex="-1"])'
    );

    var elementsByQueryCount = {};

    for (var i = 0; i < interactive.length; i++) {
      var el = interactive[i];
      if (el.offsetParent === null) continue;

      var selector = utils.generateSelector(el);
      var affectingQueries = categorizeElement(el, index);

      // Track elements by number of affecting queries
      var queryCount = affectingQueries.length;
      if (!elementsByQueryCount[queryCount]) {
        elementsByQueryCount[queryCount] = 0;
      }
      elementsByQueryCount[queryCount]++;

      // Get base styles
      var baseStyle = window.getComputedStyle(el);
      var baseColor = baseStyle.color;
      var baseBg = baseStyle.backgroundColor;

      // Check base state contrast
      var baseContrast = getContrast(baseColor, baseBg);
      var isLargeText = parseInt(baseStyle.fontSize) >= 18 ||
        (parseInt(baseStyle.fontSize) >= 14 && baseStyle.fontWeight >= 700);

      var requiredRatio = isLargeText ? largeThreshold : normalThreshold;

      if (baseContrast.ratio < requiredRatio) {
        issues.push({
          type: 'color-contrast-current',
          severity: 'error',
          selector: selector,
          state: 'default',
          message: 'Insufficient color contrast in current state',
          contrast: baseContrast.ratio,
          required: requiredRatio,
          foreground: baseColor,
          background: baseBg,
          affectedByQueries: affectingQueries.length,
          category: 'contrast'
        });
      }

      // Test focus state
      el.focus();
      var focusStyle = window.getComputedStyle(el);
      var focusColor = focusStyle.color;
      var focusBg = focusStyle.backgroundColor;
      var focusOutline = focusStyle.outlineColor;

      if (focusColor !== baseColor || focusBg !== baseBg) {
        var focusContrast = getContrast(focusColor, focusBg);
        if (focusContrast.ratio < requiredRatio) {
          issues.push({
            type: 'color-contrast-current',
            severity: 'error',
            selector: selector,
            state: 'focus',
            message: 'Insufficient color contrast in focus state',
            contrast: focusContrast.ratio,
            required: requiredRatio,
            foreground: focusColor,
            background: focusBg,
            affectedByQueries: affectingQueries.length,
            category: 'contrast'
          });
        }
      }

      if (focusOutline && focusBg) {
        var outlineContrast = getContrast(focusOutline, focusBg);
        if (outlineContrast.ratio < 3) {
          issues.push({
            type: 'focus-outline-contrast',
            severity: 'error',
            selector: selector,
            state: 'focus',
            message: 'Focus outline has insufficient contrast (min 3:1 required)',
            contrast: outlineContrast.ratio,
            required: 3,
            category: 'focus-indicator'
          });
        }
      }

      el.blur();

      // Warn about media queries affecting this element
      if (affectingQueries.length > 0) {
        var inactiveQueries = [];
        for (var j = 0; j < affectingQueries.length; j++) {
          var query = affectingQueries[j];
          if (!index.mediaQueries[query].active) {
            inactiveQueries.push(query);
          }
        }

        if (inactiveQueries.length > 0) {
          issues.push({
            type: 'untested-media-queries',
            severity: 'info',
            selector: selector,
            message: 'Element affected by ' + inactiveQueries.length + ' inactive media query(ies) - retest under different conditions',
            queries: inactiveQueries,
            category: 'untested-states'
          });
        }
      }
    }

    // Generate test recommendations
    var recommendations = [];

    if (index.discoveredBreakpoints.length > 0) {
      var untested = [];
      for (var i = 0; i < index.discoveredBreakpoints.length; i++) {
        var bp = index.discoveredBreakpoints[i];
        if (Math.abs(bp - currentWidth) > 50) {
          untested.push(bp);
        }
      }

      if (untested.length > 0) {
        recommendations.push({
          type: 'viewport-testing',
          message: 'To fully test responsive styles, run audits at these viewport widths: ' + untested.join(', ') + 'px',
          breakpoints: untested
        });
      }
    }

    if (index.discoveredColorSchemes.length > 0) {
      var untestedSchemes = [];
      for (var i = 0; i < index.discoveredColorSchemes.length; i++) {
        var scheme = index.discoveredColorSchemes[i];
        if (scheme !== currentScheme) {
          untestedSchemes.push(scheme);
        }
      }

      if (untestedSchemes.length > 0) {
        recommendations.push({
          type: 'color-scheme-testing',
          message: 'To fully test color scheme styles, enable: ' + untestedSchemes.join(', ') + ' mode and re-run audit',
          schemes: untestedSchemes
        });
      }
    }

    // Calculate counts before any filtering
    var totalCount = issues.length;
    var errorCount = issues.filter(function(i) { return i.severity === 'error'; }).length;
    var warningCount = issues.filter(function(i) { return i.severity === 'warning'; }).length;
    var infoCount = issues.filter(function(i) { return i.severity === 'info'; }).length;
    var categories = {
      'contrast': issues.filter(function(i) { return i.category === 'contrast'; }).length,
      'focus-indicator': issues.filter(function(i) { return i.category === 'focus-indicator'; }).length,
      'css-access': issues.filter(function(i) { return i.category === 'css-access'; }).length,
      'untested-states': issues.filter(function(i) { return i.category === 'untested-states'; }).length
    };

    var response = {
      mode: 'comprehensive',
      detailLevel: detailLevel,
      level: level,
      count: totalCount,
      errors: errorCount,
      warnings: warningCount,
      info: infoCount,
      categories: categories,
      summary: {
        testedStates: ['default', 'focus'],
        currentBreakpoint: currentWidth,
        currentColorScheme: currentScheme,
        totalInteractive: interactive.length
      }
    };

    if (detailLevel === 'summary') {
      // Summary: counts only, minimal cssAnalysis
      response.cssAnalysis = {
        totalStylesheets: document.styleSheets.length,
        crossOriginSheets: index.crossOriginSheets.length,
        totalMediaQueries: Object.keys(index.mediaQueries).length
      };
      response.recommendationCount = recommendations.length;
    } else if (detailLevel === 'compact') {
      // Compact: limited issues, truncated fields, essential cssAnalysis
      var limitedIssues = issues.slice(0, maxIssues);
      response.issues = limitedIssues.map(function(issue) {
        return compactIssue(issue, options);
      });
      if (totalCount > maxIssues) {
        response.truncated = true;
        response.shownIssues = maxIssues;
      }
      response.cssAnalysis = {
        totalStylesheets: document.styleSheets.length,
        crossOriginSheets: index.crossOriginSheets.length,
        discoveredBreakpoints: index.discoveredBreakpoints,
        discoveredColorSchemes: index.discoveredColorSchemes,
        totalMediaQueries: Object.keys(index.mediaQueries).length,
        activeMediaQueries: Object.keys(index.mediaQueries).filter(function(q) { return index.mediaQueries[q].active; }).length
      };
      response.testingRecommendations = recommendations;
    } else {
      // Full: all issues and detailed cssAnalysis
      response.issues = issues;
      response.cssAnalysis = {
        totalStylesheets: document.styleSheets.length,
        crossOriginSheets: index.crossOriginSheets.length,
        discoveredBreakpoints: index.discoveredBreakpoints,
        discoveredColorSchemes: index.discoveredColorSchemes,
        totalMediaQueries: Object.keys(index.mediaQueries).length,
        activeMediaQueries: Object.keys(index.mediaQueries).filter(function(q) { return index.mediaQueries[q].active; }).length,
        elementsByQueryCount: elementsByQueryCount
      };
      response.testingRecommendations = recommendations;
    }

    return response;
  }

  // Main audit function with mode support
  function auditAccessibility(options) {
    options = options || {};
    var mode = options.mode || 'standard';

    // If useBasic is explicitly set, skip axe-core
    if (options.useBasic === true) {
      return Promise.resolve(runBasicAudit(options));
    }

    // Fast mode - run fast improvements only
    if (mode === 'fast') {
      return Promise.resolve(runFastAudit(options));
    }

    // Comprehensive mode - run comprehensive checks
    if (mode === 'comprehensive') {
      return Promise.resolve(runComprehensiveAudit(options));
    }

    // Standard mode (default) - run axe-core
    return loadAxeCore()
      .then(function() {
        return runAxeAudit(options);
      })
      .catch(function(error) {
        console.warn('axe-core unavailable, falling back to basic audit:', error.message);
        var result = runBasicAudit(options);
        result.fallback = true;
        result.fallbackReason = error.message;
        return result;
      });
  }

  // Export accessibility functions
  window.__devtool_accessibility = {
    getA11yInfo: getA11yInfo,
    getContrast: getContrast,
    getTabOrder: getTabOrder,
    getScreenReaderText: getScreenReaderText,
    auditAccessibility: auditAccessibility
  };
})();

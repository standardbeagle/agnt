// Accessibility primitives for DevTool
// A11y information, contrast checking, tab order
// Overhauled for action-oriented output with CSS selectors

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  /**
   * Generate a stable 8-char hex finding ID from type, selector, and message.
   * FNV-1a 32-bit hash — same inputs always produce same output across runs.
   */
  function computeFindingID(type, selector, message) {
    var input = type + '\x00' + (selector || '') + '\x00' + (message || '');
    var h = 0x811c9dc5;
    for (var i = 0; i < input.length; i++) {
      h = h ^ input.charCodeAt(i);
      h = (h + (h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24)) >>> 0;
    }
    return ('00000000' + h.toString(16)).slice(-8);
  }

  function registerFinding(id, selector) {
    if (!window.__devtool) { window.__devtool = {}; }
    if (!window.__devtool.audit) { window.__devtool.audit = {}; }
    if (!window.__devtool.audit.findingSelectors) { window.__devtool.audit.findingSelectors = {}; }
    window.__devtool.audit.findingSelectors[id] = selector || '';
  }

  // Legacy: issue ID counter kept for backward-compat with any callers that don't use the id field.
  var issueIdCounter = 0;
  function generateIssueId(type) {
    issueIdCounter++;
    return type + '-' + issueIdCounter;
  }

  // Calculate score from issues (start at 100, subtract based on severity)
  function calculateScore(issues) {
    var score = 100;
    for (var i = 0; i < issues.length; i++) {
      var issue = issues[i];
      if (issue.severity === 'error' || issue.severity === 'critical') {
        score -= 10;
      } else if (issue.severity === 'warning') {
        score -= 5;
      } else if (issue.severity === 'info') {
        score -= 1;
      }
    }
    return Math.max(0, score);
  }

  // Get letter grade from score
  function getGrade(score) {
    if (score >= 90) return 'A';
    if (score >= 80) return 'B';
    if (score >= 70) return 'C';
    if (score >= 60) return 'D';
    return 'F';
  }

  // Truncate HTML to max length
  function truncateHtml(el, maxLength) {
    if (!el) return '';
    var html = el.outerHTML || '';
    maxLength = maxLength || 100;
    if (html.length <= maxLength) return html;
    return html.substring(0, maxLength) + '...';
  }

  // Group issues by type and count
  function groupIssuesByType(issues) {
    var groups = {};
    for (var i = 0; i < issues.length; i++) {
      var issue = issues[i];
      if (!groups[issue.type]) {
        groups[issue.type] = [];
      }
      groups[issue.type].push(issue);
    }
    return groups;
  }

  // Generate actionable summary from issues
  function generateSummary(fixable, informational, checkCount) {
    var errors = fixable.filter(function(i) { return i.severity === 'error'; }).length;
    var warnings = fixable.filter(function(i) { return i.severity === 'warning'; }).length;

    if (errors === 0 && warnings === 0) {
      return 'No accessibility issues found across ' + checkCount + ' checks.';
    }

    var groups = groupIssuesByType(fixable);
    var topTypes = Object.keys(groups).slice(0, 3);
    var typeDescriptions = topTypes.map(function(type) {
      return groups[type].length + ' ' + type.replace(/-/g, ' ');
    }).join(', ');

    var prefix = errors > 0 ?
      errors + ' critical accessibility error' + (errors > 1 ? 's' : '') :
      warnings + ' accessibility warning' + (warnings > 1 ? 's' : '');

    return prefix + ' found: ' + typeDescriptions + '.';
  }

  // Generate prioritized actions from fixable issues
  function generateActions(fixable) {
    var groups = groupIssuesByType(fixable);
    var actions = [];

    // Sort by count * average impact
    var sorted = Object.keys(groups).map(function(type) {
      var issues = groups[type];
      var avgImpact = issues.reduce(function(sum, i) { return sum + (i.impact || 5); }, 0) / issues.length;
      return { type: type, issues: issues, avgImpact: avgImpact, priority: issues.length * avgImpact };
    }).sort(function(a, b) { return b.priority - a.priority; });

    for (var i = 0; i < Math.min(sorted.length, 5); i++) {
      var group = sorted[i];
      var count = group.issues.length;
      var firstFix = group.issues[0].fix || 'Fix this issue';

      if (count === 1) {
        actions.push(firstFix + ' (' + group.issues[0].selector + ')');
      } else {
        var selectors = group.issues.slice(0, 3).map(function(i) { return i.selector; });
        actions.push(firstFix.replace(/this element|the element/gi, count + ' elements') +
          ' (e.g., ' + selectors.join(', ') + ')');
      }
    }

    return actions;
  }

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

  // Check heading hierarchy (no skipped levels)
  function checkHeadingHierarchy() {
    var issues = [];
    var headings = document.querySelectorAll('h1, h2, h3, h4, h5, h6');
    var prevLevel = 0;

    for (var i = 0; i < headings.length; i++) {
      var h = headings[i];
      var level = parseInt(h.tagName.charAt(1));

      if (prevLevel > 0 && level > prevLevel + 1) {
        var hSel = utils.generateSelector(h);
        var hMsg = 'Heading level skipped: h' + prevLevel + ' to h' + level;
        var hID = computeFindingID('heading-skip', hSel, hMsg);
        registerFinding(hID, hSel);
        issues.push({
          id: hID,
          type: 'heading-skip',
          severity: 'warning',
          impact: 5,
          selector: hSel,
          element: truncateHtml(h),
          message: hMsg,
          fix: 'Change to h' + (prevLevel + 1) + ' or add intermediate headings',
          wcag: '1.3.1'
        });
      }
      prevLevel = level;
    }

    // Check for missing h1
    var h1s = document.querySelectorAll('h1');
    if (h1s.length === 0) {
      issues.push({
        id: computeFindingID('missing-h1', 'body', 'Page has no h1 heading'),
        type: 'missing-h1',
        severity: 'error',
        impact: 7,
        selector: 'body',
        message: 'Page has no h1 heading',
        fix: 'Add an h1 heading that describes the page content',
        wcag: '1.3.1'
      });
    } else if (h1s.length > 1) {
      var h1MultiSel = utils.generateSelector(h1s[1]);
      var h1MultiMsg = 'Page has ' + h1s.length + ' h1 headings (should have one)';
      var h1MultiID = computeFindingID('multiple-h1', h1MultiSel, h1MultiMsg);
      registerFinding(h1MultiID, h1MultiSel);
      issues.push({
        id: h1MultiID,
        type: 'multiple-h1',
        severity: 'warning',
        impact: 3,
        selector: h1MultiSel,
        element: truncateHtml(h1s[1]),
        message: h1MultiMsg,
        fix: 'Keep only one h1 as the main page heading',
        wcag: '1.3.1'
      });
    }

    return issues;
  }

  // Check for poor link text
  function checkLinkText() {
    var issues = [];
    var badPhrases = ['click here', 'read more', 'learn more', 'here', 'more', 'link'];
    var links = document.querySelectorAll('a[href]');

    for (var i = 0; i < links.length; i++) {
      var link = links[i];
      var text = (link.textContent || '').trim().toLowerCase();

      for (var j = 0; j < badPhrases.length; j++) {
        if (text === badPhrases[j]) {
          var ltSel = utils.generateSelector(link);
          var ltMsg = 'Link text "' + text + '" is not descriptive';
          var ltID = computeFindingID('non-descriptive-link', ltSel, ltMsg);
          registerFinding(ltID, ltSel);
          issues.push({
            id: ltID,
            type: 'non-descriptive-link',
            severity: 'warning',
            impact: 4,
            selector: ltSel,
            element: truncateHtml(link),
            message: ltMsg,
            fix: 'Use descriptive text that indicates the link destination',
            wcag: '2.4.4'
          });
          break;
        }
      }
    }

    return issues;
  }

  // Check for missing skip link
  function checkSkipLink() {
    var issues = [];
    var skipLink = document.querySelector('a[href^="#main"], a[href="#content"], a[href="#maincontent"], .skip-link, .skip-to-content');

    if (!skipLink) {
      var firstLink = document.querySelector('a[href]');
      if (firstLink && !firstLink.href.includes('#')) {
        var skipMsg = 'No skip link to main content found';
        issues.push({
          id: computeFindingID('missing-skip-link', 'body', skipMsg),
          type: 'missing-skip-link',
          severity: 'warning',
          impact: 5,
          selector: 'body',
          message: skipMsg,
          fix: 'Add a skip link as the first focusable element: <a href="#main">Skip to main content</a>',
          wcag: '2.4.1'
        });
      }
    }

    return issues;
  }

  // Check for ARIA misuse
  function checkAriaMisuse() {
    var issues = [];

    // Check for required ARIA attributes
    var rolesWithRequired = {
      'checkbox': ['aria-checked'],
      'combobox': ['aria-expanded'],
      'slider': ['aria-valuenow', 'aria-valuemin', 'aria-valuemax'],
      'meter': ['aria-valuenow'],
      'progressbar': ['aria-valuenow'],
      'scrollbar': ['aria-controls', 'aria-valuenow'],
      'spinbutton': ['aria-valuenow'],
      'switch': ['aria-checked']
    };

    for (var role in rolesWithRequired) {
      var elements = document.querySelectorAll('[role="' + role + '"]');
      var required = rolesWithRequired[role];

      for (var i = 0; i < elements.length; i++) {
        var el = elements[i];
        for (var j = 0; j < required.length; j++) {
          var attr = required[j];
          if (!el.hasAttribute(attr)) {
            var ariaSel = utils.generateSelector(el);
            var ariaMsg = 'Element with role="' + role + '" missing required ' + attr;
            var ariaID = computeFindingID('aria-missing-required', ariaSel, ariaMsg);
            registerFinding(ariaID, ariaSel);
            issues.push({
              id: ariaID,
              type: 'aria-missing-required',
              severity: 'error',
              impact: 7,
              selector: ariaSel,
              element: truncateHtml(el),
              message: ariaMsg,
              fix: 'Add ' + attr + ' attribute with appropriate value',
              wcag: '4.1.2'
            });
          }
        }
      }
    }

    // Check for aria-hidden on focusable elements
    var hiddenFocusable = document.querySelectorAll('[aria-hidden="true"] a, [aria-hidden="true"] button, [aria-hidden="true"] input');
    for (var k = 0; k < hiddenFocusable.length; k++) {
      var el = hiddenFocusable[k];
      if (!el.disabled && el.tabIndex >= 0) {
        var ahfSel = utils.generateSelector(el);
        var ahfMsg = 'Focusable element inside aria-hidden container';
        var ahfID = computeFindingID('aria-hidden-focusable', ahfSel, ahfMsg);
        registerFinding(ahfID, ahfSel);
        issues.push({
          id: ahfID,
          type: 'aria-hidden-focusable',
          severity: 'error',
          impact: 8,
          selector: ahfSel,
          element: truncateHtml(el),
          message: ahfMsg,
          fix: 'Either remove aria-hidden from container or add tabindex="-1" to this element',
          wcag: '4.1.2'
        });
      }
    }

    return issues;
  }

  // Check for keyboard traps
  function checkKeyboardTraps() {
    var issues = [];
    var modals = document.querySelectorAll('[role="dialog"], [role="alertdialog"], .modal, .popup');

    for (var i = 0; i < modals.length; i++) {
      var modal = modals[i];
      var style = window.getComputedStyle(modal);

      // Only check visible modals
      if (style.display === 'none' || style.visibility === 'hidden') continue;

      var focusable = modal.querySelectorAll('a, button, input, select, textarea, [tabindex]:not([tabindex="-1"])');
      var hasClose = modal.querySelector('[aria-label*="close"], [aria-label*="dismiss"], .close-button, .btn-close');

      if (focusable.length > 0 && !hasClose) {
        var ktSel = utils.generateSelector(modal);
        var ktMsg = 'Dialog may trap keyboard focus (no close button found)';
        var ktID = computeFindingID('potential-keyboard-trap', ktSel, ktMsg);
        registerFinding(ktID, ktSel);
        issues.push({
          id: ktID,
          type: 'potential-keyboard-trap',
          severity: 'warning',
          impact: 8,
          selector: ktSel,
          message: ktMsg,
          fix: 'Ensure dialog has a close button and Escape key handler',
          wcag: '2.1.2'
        });
      }
    }

    return issues;
  }

  // Check color contrast (basic check for visible text)
  function checkColorContrast() {
    var issues = [];
    var textElements = document.querySelectorAll('p, span, a, button, label, h1, h2, h3, h4, h5, h6, li');
    var checked = 0;
    var maxCheck = 50; // Limit for performance

    for (var i = 0; i < textElements.length && checked < maxCheck; i++) {
      var el = textElements[i];
      if (el.offsetParent === null) continue; // Not visible

      var style = window.getComputedStyle(el);
      var color = style.color;
      var bgColor = style.backgroundColor;

      // Skip transparent backgrounds (would need to check parent)
      if (bgColor === 'rgba(0, 0, 0, 0)' || bgColor === 'transparent') continue;

      var contrast = getContrast(color, bgColor);
      var fontSize = parseFloat(style.fontSize);
      var fontWeight = parseInt(style.fontWeight);
      var isLargeText = fontSize >= 18 || (fontSize >= 14 && fontWeight >= 700);
      var required = isLargeText ? 3 : 4.5;

      if (contrast.ratio < required) {
        var ccSel = utils.generateSelector(el);
        var ccMsg = 'Insufficient color contrast: ' + contrast.ratio.toFixed(2) + ':1 (requires ' + required + ':1)';
        var ccID = computeFindingID('color-contrast', ccSel, ccMsg);
        registerFinding(ccID, ccSel);
        issues.push({
          id: ccID,
          type: 'color-contrast',
          severity: 'error',
          impact: 7,
          selector: ccSel,
          element: truncateHtml(el),
          message: ccMsg,
          fix: 'Increase contrast between text (' + color + ') and background (' + bgColor + ')',
          wcag: '1.4.3',
          contrast: contrast.ratio,
          required: required
        });
      }
      checked++;
    }

    return issues;
  }

  // Basic accessibility audit (fallback when axe-core unavailable)
  // Overhauled to match action-oriented output schema
  function runBasicAudit(options) {
    options = options || {};
    issueIdCounter = 0; // Reset ID counter

    var fixable = [];
    var informational = [];
    var checksRun = [];

    // Check 1: Images without alt
    checksRun.push('image-alt');
    var images = document.querySelectorAll('img');
    for (var i = 0; i < images.length; i++) {
      var img = images[i];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(img)) continue;
      if (!img.alt && !img.getAttribute('role')) {
        var imgSel = utils.generateSelector(img);
        var imgMsg = 'Image missing alt attribute';
        var imgID = computeFindingID('missing-alt', imgSel, imgMsg);
        registerFinding(imgID, imgSel);
        fixable.push({
          id: imgID,
          type: 'missing-alt',
          severity: 'error',
          impact: 9,
          selector: imgSel,
          element: truncateHtml(img),
          message: imgMsg,
          fix: 'Add alt="[description of image]" attribute',
          wcag: '1.1.1'
        });
      }
    }

    // Check 2: Form inputs without labels
    checksRun.push('input-label');
    var inputs = document.querySelectorAll('input, select, textarea');
    for (var j = 0; j < inputs.length; j++) {
      var input = inputs[j];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(input)) continue;
      if (input.type === 'hidden' || input.type === 'submit' || input.type === 'button') continue;

      var hasLabel = input.getAttribute('aria-label') ||
                     input.getAttribute('aria-labelledby') ||
                     input.getAttribute('placeholder') ||
                     (input.id && document.querySelector('label[for="' + input.id + '"]')) ||
                     input.closest('label');

      if (!hasLabel) {
        var inputSel = utils.generateSelector(input);
        var inputMsg = 'Form input missing label';
        var inputID = computeFindingID('missing-label', inputSel, inputMsg);
        registerFinding(inputID, inputSel);
        fixable.push({
          id: inputID,
          type: 'missing-label',
          severity: 'error',
          impact: 8,
          selector: inputSel,
          element: truncateHtml(input),
          message: inputMsg,
          fix: 'Add a <label> element or aria-label attribute',
          wcag: '1.3.1'
        });
      }
    }

    // Check 3: Buttons without accessible names
    checksRun.push('button-name');
    var buttons = document.querySelectorAll('button, [role="button"]');
    for (var k = 0; k < buttons.length; k++) {
      var btn = buttons[k];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(btn)) continue;
      var name = getAccessibleName(btn);
      if (!name) {
        var btnSel = utils.generateSelector(btn);
        var btnMsg = 'Button missing accessible name';
        var btnID = computeFindingID('missing-button-name', btnSel, btnMsg);
        registerFinding(btnID, btnSel);
        fixable.push({
          id: btnID,
          type: 'missing-button-name',
          severity: 'error',
          impact: 8,
          selector: btnSel,
          element: truncateHtml(btn),
          message: btnMsg,
          fix: 'Add text content, aria-label, or title attribute',
          wcag: '4.1.2'
        });
      }
    }

    // Check 4: Empty links
    checksRun.push('link-content');
    var links = document.querySelectorAll('a[href]');
    for (var l = 0; l < links.length; l++) {
      var link = links[l];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(link)) continue;
      var linkText = (link.textContent || '').trim();
      var linkName = getAccessibleName(link);
      if (!linkName && !linkText) {
        var elSel = utils.generateSelector(link);
        var elMsg = 'Link has no text content';
        var elID = computeFindingID('empty-link', elSel, elMsg);
        registerFinding(elID, elSel);
        fixable.push({
          id: elID,
          type: 'empty-link',
          severity: 'error',
          impact: 7,
          selector: elSel,
          element: truncateHtml(link),
          message: elMsg,
          fix: 'Add descriptive text or aria-label',
          wcag: '2.4.4'
        });
      }
    }

    // Check 5: Heading hierarchy
    checksRun.push('heading-order');
    fixable = fixable.concat(checkHeadingHierarchy());

    // Check 6: Link text quality
    checksRun.push('link-text');
    fixable = fixable.concat(checkLinkText());

    // Check 7: Skip link
    checksRun.push('skip-link');
    fixable = fixable.concat(checkSkipLink());

    // Check 8: ARIA misuse
    checksRun.push('aria-required');
    fixable = fixable.concat(checkAriaMisuse());

    // Check 9: Keyboard traps
    checksRun.push('keyboard-trap');
    fixable = fixable.concat(checkKeyboardTraps());

    // Check 10: Color contrast
    checksRun.push('color-contrast');
    fixable = fixable.concat(checkColorContrast());

    // Add informational items
    var focusableCount = document.querySelectorAll('a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])').length;
    var focusableMsg = focusableCount + ' focusable elements found';
    informational.push({
      id: computeFindingID('focusable-count', 'document', focusableMsg),
      type: 'focusable-count',
      severity: 'info',
      message: focusableMsg,
      context: { count: focusableCount }
    });

    var landmarkCount = document.querySelectorAll('header, nav, main, aside, footer, [role="banner"], [role="navigation"], [role="main"], [role="complementary"], [role="contentinfo"]').length;
    var landmarkMsg = landmarkCount + ' landmark regions found';
    informational.push({
      id: computeFindingID('landmark-count', 'document', landmarkMsg),
      type: 'landmark-count',
      severity: 'info',
      message: landmarkMsg,
      context: { count: landmarkCount }
    });

    // Calculate score and grade
    var allIssues = fixable.concat(informational);
    var score = calculateScore(fixable);
    var grade = getGrade(score);

    // Generate summary and actions
    var summary = generateSummary(fixable, informational, checksRun.length);
    var actions = generateActions(fixable);

    // Build stats
    var stats = {
      errors: fixable.filter(function(i) { return i.severity === 'error'; }).length,
      warnings: fixable.filter(function(i) { return i.severity === 'warning'; }).length,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    return {
      mode: 'basic',
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      fixable: fixable,
      informational: informational,
      actions: actions,
      stats: stats
    };
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
      script.src = '/__devtool_axe';
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

  // WCAG reference for common axe rule IDs
  var wcagReferences = {
    'image-alt': '1.1.1',
    'button-name': '4.1.2',
    'link-name': '2.4.4',
    'label': '1.3.1',
    'color-contrast': '1.4.3',
    'focus-order-semantics': '2.4.3',
    'heading-order': '1.3.1',
    'bypass': '2.4.1',
    'document-title': '2.4.2',
    'html-has-lang': '3.1.1',
    'landmark-one-main': '1.3.1',
    'region': '1.3.1',
    'aria-required-attr': '4.1.2',
    'aria-valid-attr': '4.1.2',
    'aria-hidden-focus': '4.1.2',
    'tabindex': '2.4.3',
    'input-button-name': '4.1.2',
    'form-field-multiple-labels': '1.3.1'
  };

  // Impact to score penalty mapping
  var impactPenalty = {
    'critical': 10,
    'serious': 8,
    'moderate': 5,
    'minor': 2
  };

  // Generate fix instructions for axe rule IDs
  function getFixInstruction(ruleId, node) {
    var fixes = {
      'image-alt': 'Add alt="[description]" attribute to this image',
      'button-name': 'Add text content, aria-label, or title to this button',
      'link-name': 'Add descriptive text or aria-label to this link',
      'label': 'Add a <label> element or aria-label to this input',
      'color-contrast': 'Increase text/background contrast ratio',
      'heading-order': 'Fix heading level order (no skipped levels)',
      'bypass': 'Add a skip link at the top of the page',
      'document-title': 'Add a descriptive <title> element',
      'html-has-lang': 'Add lang attribute to <html> element',
      'landmark-one-main': 'Add role="main" or <main> landmark',
      'region': 'Wrap content in appropriate landmark regions',
      'aria-required-attr': 'Add required ARIA attributes for this role',
      'aria-valid-attr': 'Fix invalid ARIA attribute values',
      'aria-hidden-focus': 'Remove focusable elements from aria-hidden containers',
      'tabindex': 'Use tabindex="0" or "-1" only, avoid positive values'
    };
    return fixes[ruleId] || 'Review and fix this accessibility issue';
  }

  // Run axe-core audit with configurable options
  // Overhauled to match action-oriented output schema
  // Options:
  //   level: 'a' | 'aa' (default) | 'aaa'
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  //   maxIssueTypes: number (default: 10) - max issue types to include in AI-optimized mode
  //   maxExamples: number (default: 3) - max examples per issue type in AI-optimized mode
  function runAxeAudit(options) {
    options = options || {};
    issueIdCounter = 0; // Reset ID counter

    // Default to WCAG 2.1 Level AA
    var level = options.level || 'aa';
    var raw = options.raw === true; // Default: false (AI-optimized format)
    var maxIssueTypes = options.maxIssueTypes || 10;
    var maxExamples = options.maxExamples || 3;
    var runOnly = level === 'aaa' ? ['wcag2a', 'wcag2aa', 'wcag2aaa'] :
                  level === 'a' ? ['wcag2a'] : ['wcag2a', 'wcag2aa'];

    var axeOptions = {
      runOnly: {
        type: 'tag',
        values: runOnly
      },
      // Exclude agnt/devtool UI elements from audit
      exclude: [
        ['#__devtool-indicator'],
        ['#__devtool-panel'],
        ['#__devtool-overlays'],
        ['[id^="__devtool"]'],
        ['[class*="__devtool"]']
      ]
    };

    // Allow custom element selection
    if (options.selector) {
      axeOptions.selector = options.selector;
    }

    return window.axe.run(axeOptions).then(function(results) {
      // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
      // Groups issues by type with limited examples for token efficiency
      if (!raw) {
        var issuesByType = {};
        var totalIssues = 0;
        var criticalCount = 0;
        var seriousCount = 0;
        var moderateCount = 0;
        var minorCount = 0;

        // Group violations by rule ID
        results.violations.forEach(function(violation) {
          var count = violation.nodes.length;
          totalIssues += count;

          // Count by impact
          if (violation.impact === 'critical') criticalCount += count;
          else if (violation.impact === 'serious') seriousCount += count;
          else if (violation.impact === 'moderate') moderateCount += count;
          else minorCount += count;

          issuesByType[violation.id] = {
            ruleId: violation.id,
            impact: violation.impact,
            message: violation.help,
            wcag: wcagReferences[violation.id] || '',
            fix: getFixInstruction(violation.id, null),
            count: count,
            // Limit examples for token efficiency
            examples: violation.nodes.slice(0, maxExamples).map(function(node) {
              return {
                selector: node.target.join(', '),
                html: (node.html || '').substring(0, 80)
              };
            })
          };
        });

        // Calculate score based on issue counts and impact
        var score = 100 - (criticalCount * 10) - (seriousCount * 8) - (moderateCount * 5) - (minorCount * 2);
        score = Math.max(0, Math.min(100, score));

        return {
          audit: 'accessibility',
          mode: 'axe-core',
          version: window.axe.version,
          level: level,
          checkedAt: new Date().toISOString(),
          score: score,
          grade: getGrade(score),
          stats: {
            totalIssues: totalIssues,
            critical: criticalCount,
            serious: seriousCount,
            moderate: moderateCount,
            minor: minorCount,
            passed: results.passes.length,
            incomplete: results.incomplete.length,
            rulesChecked: results.passes.length + results.violations.length
          },
          // Grouped issues for AI processing - no duplication per node
          raw: {
            issuesByType: issuesByType,
            incompleteRules: results.incomplete.map(function(i) {
              return { id: i.id, count: i.nodes.length, message: i.help };
            }),
            passedRuleCount: results.passes.length
          }
        };
      }

      // === RAW RESPONSE (raw: true) ===
      // Returns verbose detailed format with all issues and context
      var fixable = [];
      var informational = [];
      var checksRun = [];

      // Track rule IDs for checksRun
      results.violations.forEach(function(v) { checksRun.push(v.id); });
      results.passes.forEach(function(p) { checksRun.push(p.id); });

      // Process violations into fixable issues
      results.violations.forEach(function(violation) {
        violation.nodes.forEach(function(node) {
          var severity = violation.impact === 'critical' || violation.impact === 'serious' ? 'error' : 'warning';
          var impactScore = impactPenalty[violation.impact] || 5;
          var axeSel = node.target.join(', ');
          var axeMsg = violation.help;
          var axeID = computeFindingID(violation.id, axeSel, axeMsg);
          registerFinding(axeID, axeSel);
          fixable.push({
            id: axeID,
            type: violation.id,
            severity: severity,
            impact: impactScore,
            selector: axeSel,
            element: (node.html || '').substring(0, 100),
            message: axeMsg,
            fix: getFixInstruction(violation.id, node),
            wcag: wcagReferences[violation.id] || '',
            helpUrl: violation.helpUrl
          });
        });
      });

      // Add incomplete items as informational
      results.incomplete.forEach(function(incomplete) {
        var incompleteMsg = incomplete.help + ' (needs manual review)';
        informational.push({
          id: computeFindingID(incomplete.id + '-needs-review', 'document', incompleteMsg),
          type: incomplete.id + '-needs-review',
          severity: 'info',
          message: incompleteMsg,
          context: {
            nodeCount: incomplete.nodes.length,
            description: incomplete.description
          }
        });
      });

      // Add pass summary as informational
      if (results.passes.length > 0) {
        var passMsg = results.passes.length + ' accessibility checks passed';
        informational.push({
          id: computeFindingID('checks-passed', 'document', passMsg),
          type: 'checks-passed',
          severity: 'info',
          message: passMsg,
          context: { passedRules: results.passes.map(function(p) { return p.id; }) }
        });
      }

      // Calculate score - start at 100, subtract based on issues
      var score = 100;
      for (var i = 0; i < fixable.length; i++) {
        score -= (fixable[i].impact || 5);
      }
      score = Math.max(0, Math.min(100, score));
      var grade = getGrade(score);

      // Generate summary and actions
      var summary = generateSummary(fixable, informational, checksRun.length);
      var actions = generateActions(fixable);

      // Build stats
      var errorCount = fixable.filter(function(i) { return i.severity === 'error'; }).length;
      var warningCount = fixable.filter(function(i) { return i.severity === 'warning'; }).length;

      var stats = {
        errors: errorCount,
        warnings: warningCount,
        info: informational.length,
        fixable: fixable.length,
        informational: informational.length,
        passed: results.passes.length,
        incomplete: results.incomplete.length
      };

      return {
        mode: 'axe-core',
        version: window.axe.version,
        level: level,
        summary: summary,
        score: score,
        grade: grade,
        checkedAt: new Date().toISOString(),
        checksRun: checksRun.slice(0, 50), // Limit for token efficiency
        fixable: fixable,
        informational: informational,
        actions: actions,
        stats: stats
      };
    });
  }

  // Fast improvements mode - quick wins beyond axe
  // Overhauled to match action-oriented output schema
  function runFastAudit(options) {
    options = options || {};
    issueIdCounter = 0; // Reset ID counter

    var fixable = [];
    var informational = [];
    var checksRun = ['focus-indicators', 'focus-visibility', 'color-scheme'];

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

    var hiddenOnFocusCount = 0;
    var noFocusIndicatorCount = 0;

    for (var i = 0; i < focusable.length && fixable.length < 20; i++) {
      var el = focusable[i];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(el)) continue;
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
        hiddenOnFocusCount++;
        var fhSel = utils.generateSelector(el);
        var fhMsg = 'Element is hidden when focused';
        var fhID = computeFindingID('hidden-on-focus', fhSel, fhMsg);
        registerFinding(fhID, fhSel);
        fixable.push({
          id: fhID,
          type: 'hidden-on-focus',
          severity: 'error',
          impact: 9,
          selector: fhSel,
          element: truncateHtml(el),
          message: fhMsg,
          fix: 'Remove display:none, visibility:hidden, or opacity:0 from :focus styles',
          wcag: '2.4.7'
        });
      }

      // Check for visible focus indicator by comparing styles
      var baseOutline = window.getComputedStyle(el).outline;
      if (!hasFocusStyle && (baseOutline === 'none' || baseOutline === '0px none rgb(0, 0, 0)')) {
        noFocusIndicatorCount++;
        var fiSel = utils.generateSelector(el);
        var fiMsg = 'Element may lack visible focus indicator';
        var fiID = computeFindingID('no-focus-indicator', fiSel, fiMsg);
        registerFinding(fiID, fiSel);
        fixable.push({
          id: fiID,
          type: 'no-focus-indicator',
          severity: 'warning',
          impact: 6,
          selector: fiSel,
          element: truncateHtml(el),
          message: fiMsg,
          fix: 'Add :focus or :focus-visible styles with visible outline or border',
          wcag: '2.4.7'
        });
      }
    }

    // Check for color scheme support
    var hasLightMode = false;
    var hasDarkMode = false;

    for (var k = 0; k < cssRules.length; k++) {
      var rule = cssRules[k];
      if (rule instanceof CSSMediaRule) {
        var mediaText = rule.media.mediaText;
        if (mediaText.indexOf('prefers-color-scheme') !== -1) {
          if (mediaText.indexOf('light') !== -1) hasLightMode = true;
          if (mediaText.indexOf('dark') !== -1) hasDarkMode = true;
        }
      }
    }

    if (!hasLightMode && !hasDarkMode) {
      var noSchemeMsg = 'No color scheme media queries detected (prefers-color-scheme)';
      informational.push({
        id: computeFindingID('no-color-scheme', 'stylesheets', noSchemeMsg),
        type: 'no-color-scheme',
        severity: 'info',
        message: noSchemeMsg,
        context: { recommendation: 'Consider adding dark mode support for user preference' }
      });
    } else {
      var schemeMsg = 'Color scheme support: ' + (hasLightMode ? 'light ' : '') + (hasDarkMode ? 'dark' : '');
      informational.push({
        id: computeFindingID('color-scheme-support', 'stylesheets', schemeMsg),
        type: 'color-scheme-support',
        severity: 'info',
        message: schemeMsg,
        context: { light: hasLightMode, dark: hasDarkMode }
      });
    }

    // Calculate score and grade
    var score = calculateScore(fixable);
    var grade = getGrade(score);

    // Generate summary and actions
    var summary = generateSummary(fixable, informational, checksRun.length);
    var actions = generateActions(fixable);

    // Build stats
    var stats = {
      errors: fixable.filter(function(i) { return i.severity === 'error'; }).length,
      warnings: fixable.filter(function(i) { return i.severity === 'warning'; }).length,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    return {
      mode: 'fast',
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      fixable: fixable,
      informational: informational,
      actions: actions,
      stats: stats
    };
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
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  //   maxExamples: number (default: 5) - max examples per issue type in AI-optimized mode
  // Comprehensive mode - CSS rule analysis and state-specific testing
  // Overhauled to match action-oriented output schema
  function runComprehensiveAudit(options) {
    options = options || {};
    issueIdCounter = 0; // Reset ID counter
    var level = options.level || 'aa';
    var raw = options.raw === true; // Default: false (AI-optimized format)
    var maxExamples = options.maxExamples || 5;

    var fixable = [];
    var informational = [];
    var checksRun = ['contrast-states', 'focus-outline-contrast', 'css-access', 'media-query-coverage'];

    // Build media query index
    var index = buildMediaQueryIndex();

    // Flag cross-origin stylesheets as informational
    for (var i = 0; i < index.crossOriginSheets.length; i++) {
      var sheet = index.crossOriginSheets[i];
      var cssAccessMsg = 'Cannot analyze cross-origin stylesheet: ' + sheet.href;
      informational.push({
        id: computeFindingID('cross-origin-stylesheet', sheet.href || 'external', cssAccessMsg),
        type: 'cross-origin-stylesheet',
        severity: 'info',
        message: cssAccessMsg,
        context: { href: sheet.href }
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

    var elementsTested = 0;
    var maxElements = 30; // Limit for performance

    for (var j = 0; j < interactive.length && elementsTested < maxElements; j++) {
      var el = interactive[j];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(el)) continue;
      if (el.offsetParent === null) continue;
      elementsTested++;

      var selector = utils.generateSelector(el);
      var affectingQueries = categorizeElement(el, index);

      // Get base styles
      var baseStyle = window.getComputedStyle(el);
      var baseColor = baseStyle.color;
      var baseBg = baseStyle.backgroundColor;

      // Skip transparent backgrounds
      if (baseBg === 'rgba(0, 0, 0, 0)' || baseBg === 'transparent') continue;

      // Check base state contrast
      var baseContrast = getContrast(baseColor, baseBg);
      var isLargeText = parseInt(baseStyle.fontSize) >= 18 ||
        (parseInt(baseStyle.fontSize) >= 14 && parseInt(baseStyle.fontWeight) >= 700);

      var requiredRatio = isLargeText ? largeThreshold : normalThreshold;

      if (baseContrast.ratio < requiredRatio) {
        var baseMsg = 'Insufficient contrast in default state: ' + baseContrast.ratio.toFixed(2) + ':1';
        var baseID = computeFindingID('color-contrast', selector, baseMsg);
        registerFinding(baseID, selector);
        fixable.push({
          id: baseID,
          type: 'color-contrast',
          severity: 'error',
          impact: 8,
          selector: selector,
          element: truncateHtml(el),
          message: baseMsg,
          fix: 'Increase contrast between ' + baseColor + ' and ' + baseBg + ' to at least ' + requiredRatio + ':1',
          wcag: '1.4.3',
          contrast: baseContrast.ratio,
          required: requiredRatio
        });
      }

      // Test focus state
      try {
        el.focus();
        var focusStyle = window.getComputedStyle(el);
        var focusColor = focusStyle.color;
        var focusBg = focusStyle.backgroundColor;
        var focusOutline = focusStyle.outlineColor;

        if ((focusColor !== baseColor || focusBg !== baseBg) &&
            focusBg !== 'rgba(0, 0, 0, 0)' && focusBg !== 'transparent') {
          var focusContrast = getContrast(focusColor, focusBg);
          if (focusContrast.ratio < requiredRatio) {
            var focusMsg = 'Insufficient contrast in focus state: ' + focusContrast.ratio.toFixed(2) + ':1';
            var focusID = computeFindingID('color-contrast-focus', selector, focusMsg);
            registerFinding(focusID, selector);
            fixable.push({
              id: focusID,
              type: 'color-contrast-focus',
              severity: 'error',
              impact: 7,
              selector: selector,
              element: truncateHtml(el),
              message: focusMsg,
              fix: 'Increase focus state contrast to at least ' + requiredRatio + ':1',
              wcag: '1.4.3'
            });
          }
        }

        if (focusOutline && focusBg && focusBg !== 'rgba(0, 0, 0, 0)') {
          var outlineContrast = getContrast(focusOutline, focusBg);
          if (outlineContrast.ratio < 3) {
            var outlineMsg = 'Focus outline contrast too low: ' + outlineContrast.ratio.toFixed(2) + ':1 (min 3:1)';
            var outlineID = computeFindingID('focus-outline-contrast', selector, outlineMsg);
            registerFinding(outlineID, selector);
            fixable.push({
              id: outlineID,
              type: 'focus-outline-contrast',
              severity: 'error',
              impact: 7,
              selector: selector,
              message: outlineMsg,
              fix: 'Use a focus outline color with at least 3:1 contrast against background',
              wcag: '2.4.7'
            });
          }
        }

        el.blur();
      } catch (e) {
        // Some elements can't be focused, skip
      }

      // Track untested media queries as informational
      if (affectingQueries.length > 0) {
        var inactiveQueries = affectingQueries.filter(function(q) {
          return !index.mediaQueries[q].active;
        });

        if (inactiveQueries.length > 0 && fixable.length < 5) { // Only add a few
          var untestedMsg = 'Element has ' + inactiveQueries.length + ' untested responsive style(s)';
          informational.push({
            id: computeFindingID('untested-media-query', selector, untestedMsg),
            type: 'untested-media-query',
            severity: 'info',
            message: untestedMsg,
            context: {
              selector: selector,
              queries: inactiveQueries.slice(0, 3) // Limit for token efficiency
            }
          });
        }
      }
    }

    // Generate test recommendations as informational
    if (index.discoveredBreakpoints.length > 0) {
      var untested = index.discoveredBreakpoints.filter(function(bp) {
        return Math.abs(bp - currentWidth) > 50;
      });

      if (untested.length > 0) {
        var vpMsg = 'Re-run audit at these viewport widths for full coverage: ' + untested.slice(0, 5).join(', ') + 'px';
        informational.push({
          id: computeFindingID('viewport-testing-needed', 'document', vpMsg),
          type: 'viewport-testing-needed',
          severity: 'info',
          message: vpMsg,
          context: { breakpoints: untested }
        });
      }
    }

    if (index.discoveredColorSchemes.length > 0) {
      var untestedSchemes = index.discoveredColorSchemes.filter(function(s) { return s !== currentScheme; });
      if (untestedSchemes.length > 0) {
        var schemeTestMsg = 'Re-run audit in ' + untestedSchemes.join(', ') + ' mode for full coverage';
        informational.push({
          id: computeFindingID('color-scheme-testing-needed', 'document', schemeTestMsg),
          type: 'color-scheme-testing-needed',
          severity: 'info',
          message: schemeTestMsg,
          context: { currentScheme: currentScheme, untestedSchemes: untestedSchemes }
        });
      }
    }

    // Calculate score and grade
    var score = calculateScore(fixable);
    var grade = getGrade(score);

    // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
    // Groups issues by type with limited examples for token efficiency
    if (!raw) {
      var issuesByType = {};
      var errorCount = 0;
      var warningCount = 0;

      // Group issues by type
      fixable.forEach(function(issue) {
        if (!issuesByType[issue.type]) {
          issuesByType[issue.type] = {
            type: issue.type,
            severity: issue.severity,
            wcag: issue.wcag || '',
            fix: issue.fix || '',
            count: 0,
            examples: []
          };
        }
        issuesByType[issue.type].count++;
        if (issue.severity === 'error') errorCount++;
        else if (issue.severity === 'warning') warningCount++;

        // Limit examples per type
        if (issuesByType[issue.type].examples.length < maxExamples) {
          issuesByType[issue.type].examples.push({
            selector: issue.selector,
            message: issue.message
          });
        }
      });

      return {
        audit: 'accessibility',
        mode: 'comprehensive',
        level: level,
        checkedAt: new Date().toISOString(),
        score: score,
        grade: grade,
        stats: {
          totalIssues: fixable.length,
          errors: errorCount,
          warnings: warningCount,
          info: informational.length,
          elementsTested: elementsTested,
          totalInteractive: interactive.length
        },
        raw: {
          issuesByType: issuesByType,
          cssAnalysis: {
            currentViewport: currentWidth,
            currentColorScheme: currentScheme,
            breakpointsToTest: index.discoveredBreakpoints.filter(function(bp) {
              return Math.abs(bp - currentWidth) > 50;
            }).slice(0, 5),
            colorSchemesToTest: index.discoveredColorSchemes.filter(function(s) {
              return s !== currentScheme;
            })
          }
        }
      };
    }

    // === RAW RESPONSE (raw: true) ===
    // Returns verbose detailed format with all issues and context
    var summary = generateSummary(fixable, informational, checksRun.length);
    var actions = generateActions(fixable);

    // Build stats
    var stats = {
      errors: fixable.filter(function(i) { return i.severity === 'error'; }).length,
      warnings: fixable.filter(function(i) { return i.severity === 'warning'; }).length,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length,
      elementsTested: elementsTested,
      totalInteractive: interactive.length
    };

    return {
      mode: 'comprehensive',
      level: level,
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      fixable: fixable,
      informational: informational,
      actions: actions,
      stats: stats,
      cssAnalysis: {
        currentViewport: currentWidth,
        currentColorScheme: currentScheme,
        discoveredBreakpoints: index.discoveredBreakpoints,
        discoveredColorSchemes: index.discoveredColorSchemes,
        totalMediaQueries: Object.keys(index.mediaQueries).length
      }
    };
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

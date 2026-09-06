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

  // --- Modern-CSS opportunity scan ---------------------------------------
  // The hygiene checks above answer "what is wrong on this page". This scan
  // answers a different question: "what does this page hand-roll that CSS now
  // has a primitive for". Findings are ADVISORY and never move the score — a
  // page written before a primitive shipped is not defective.
  //
  // Every finding carries `baseline` and `fallback` because these primitives
  // landed at different times: some are interoperable today, some still drop
  // on the floor in one engine. Emitting the suggestion without the support
  // reality would push callers into shipping a value that silently does
  // nothing, which is the failure mode this project treats as worse than no
  // suggestion at all.
  var MODERN_CSS = {
    'alpha-shorthand': {
      feature: 'alpha() relative color',
      baseline: 'Chrome 151+, Safari 27+, Firefox nightly (as of 2026-09)',
      fallback: 'keep the rgba()/hsla() literal and layer alpha() behind @supports (color: alpha(red / 50%))'
    },
    'progress-function': {
      feature: 'progress()',
      baseline: 'Chrome, Edge, Safari; Firefox intent-to-ship (as of 2026-09)',
      fallback: 'keep the calc() form behind @supports (width: progress(1px, 0px, 2px))'
    },
    'typed-attr': {
      feature: 'attr() with a type — attr(data-x type(<length>))',
      baseline: 'Chrome, Edge, Safari; Firefox intent-to-ship (as of 2026-09)',
      fallback: 'keep the per-value rules, or pass the value in as a custom property (style="--size: 12px")'
    },
    'sibling-index': {
      feature: 'sibling-index() / sibling-count()',
      baseline: 'Chrome, Safari 26.2+, Firefox — interoperable as of 2026-09',
      fallback: 'none needed'
    },
    'text-box-trim': {
      feature: 'text-box-trim / text-box-edge',
      baseline: 'Chrome (since 2025-02), Safari; Firefox intent-to-prototype (as of 2026-09)',
      fallback: 'safe to add unconditionally — an engine without it renders exactly what the page renders today'
    },
    'shrink-to-fit': {
      feature: 'max-content-sizing: shrink-to-fit',
      baseline: 'newest of the set, not yet interoperable (as of 2026-09)',
      fallback: 'gate on @supports (max-content-sizing: shrink-to-fit) and keep the current wrapper sizing'
    }
  };

  // Ratio-of-differences: calc((v - a) / (b - a)) — the manual normalisation
  // progress() replaces. Matched structurally rather than with one regex,
  // because the real-world form nests var() inside both differences, which a
  // "no parens between the parens" pattern can never match. Requiring a
  // PARENTHESISED divisor keeps this off ordinary division such as
  // calc((100% - 2 * 10px) / 3).
  var PAREN_DIV_RE = /\)\s*\/\s*\(/;
  var SUBTRACT_RE = /\s-\s/g;
  function hasRatioShape(value) {
    if (value.indexOf('calc(') === -1) return false;
    if (!PAREN_DIV_RE.test(value)) return false;
    var minus = value.match(SUBTRACT_RE);
    return !!minus && minus.length >= 2;
  }
  var FIT_RE = /\b(fit|max|min)-content\b/;
  var HEX_RE = /#([0-9a-f]{3,8})\b/gi;
  var FUNC_COLOR_RE = /\b(rgba?|hsla?)\(([^()]*)\)/gi;
  var NTH_INT_RE = /:nth-child\((\d+)\)/g;
  var ATTR_SEL_SRC = '\\[\\s*([-\\w]+)\\s*([~^$*|]?=)\\s*("[^"]*"|\'[^\']*\'|[^\\]]*?)\\s*\\]';
  var ATTR_SEL_RE = new RegExp(ATTR_SEL_SRC, 'g');
  // Separate object: String.replace() with a /g regex resets that regex's
  // lastIndex, so stripping with the same matcher we are iterating would
  // restart the walk forever.
  var ATTR_SEL_STRIP_RE = new RegExp(ATTR_SEL_SRC, 'g');

  var MODERN_RULE_CAP = 3000;   // bounded walk: a huge sheet must not stall the page
  var MODERN_FINDING_CAP = 3;   // per feature

  function newModernState() {
    return {
      colors: {},        // colorKey -> { opaque, alphas, sample }
      attrGroups: {},    // base|attr -> { base, attr, values }
      nthGroups: {},     // normalized selector -> { selector, indexes }
      progress: [],      // { selector, snippet }
      textBox: [],       // { selector, top, bottom }
      fitContent: 0,
      rulesScanned: 0,
      truncated: false
    };
  }

  function alphaToken(token) {
    if (!token) return 1;
    var n = parseFloat(token);
    if (isNaN(n)) return 1;
    return token.charAt(token.length - 1) === '%' ? n / 100 : n;
  }

  // Normalize one color literal to { key, alpha }. Hex and rgb()/rgba() share
  // an 'rgb:' key space so "#0b5fff" and "rgba(11,95,255,.4)" collapse onto
  // the same base color; hsl() keeps its own space rather than guessing a
  // conversion the page never asked for.
  function colorEntry(space, channels, alpha) {
    return { key: space + ':' + channels.join(','), alpha: alpha };
  }

  function parseColorsIn(value, out) {
    var m;
    HEX_RE.lastIndex = 0;
    while ((m = HEX_RE.exec(value)) !== null) {
      var hex = m[1].toLowerCase();
      var r, g, b, a = 1;
      if (hex.length === 3 || hex.length === 4) {
        r = String(parseInt(hex[0] + hex[0], 16));
        g = String(parseInt(hex[1] + hex[1], 16));
        b = String(parseInt(hex[2] + hex[2], 16));
        if (hex.length === 4) a = parseInt(hex[3] + hex[3], 16) / 255;
      } else if (hex.length === 6 || hex.length === 8) {
        r = String(parseInt(hex.substring(0, 2), 16));
        g = String(parseInt(hex.substring(2, 4), 16));
        b = String(parseInt(hex.substring(4, 6), 16));
        if (hex.length === 8) a = parseInt(hex.substring(6, 8), 16) / 255;
      } else {
        continue; // 5- and 7-digit hex is not a color
      }
      out.push({ entry: colorEntry('rgb', [r, g, b], a), raw: m[0] });
    }
    FUNC_COLOR_RE.lastIndex = 0;
    while ((m = FUNC_COLOR_RE.exec(value)) !== null) {
      var fn = m[1].toLowerCase();
      var parts = m[2].split(/[\s,\/]+/).filter(function(p) { return p !== ''; });
      if (parts.length < 3) continue;
      var space = fn.charAt(0) === 'r' ? 'rgb' : 'hsl';
      var channels = [parts[0], parts[1], parts[2]].map(function(p) { return p.toLowerCase(); });
      out.push({ entry: colorEntry(space, channels, alphaToken(parts[3])), raw: m[0] });
    }
  }

  // Declaration-level scan, shared by the inline-style pass and the rule walk.
  function modernScanDeclaration(state, prop, value, selector) {
    if (!value) return;

    var colors = [];
    parseColorsIn(value, colors);
    for (var ci = 0; ci < colors.length; ci++) {
      var key = colors[ci].entry.key;
      var alpha = colors[ci].entry.alpha;
      var bucket = state.colors[key];
      if (!bucket) {
        bucket = state.colors[key] = { opaque: 0, alphas: {}, sample: colors[ci].raw };
      }
      if (alpha >= 1) bucket.opaque++;
      else bucket.alphas[String(alpha)] = (bucket.alphas[String(alpha)] || 0) + 1;
    }

    if (state.progress.length < MODERN_FINDING_CAP && hasRatioShape(value)) {
      state.progress.push({
        selector: selector,
        snippet: (prop + ': ' + value).substring(0, 80)
      });
    }

    if ((prop === 'width' || prop === 'inline-size' || prop === 'height' || prop === 'block-size') &&
        FIT_RE.test(value)) {
      state.fitContent++;
    }
  }

  // Selector-level scan: the two shapes that only exist in a stylesheet.
  function modernScanSelector(state, selectorText) {
    var m;

    NTH_INT_RE.lastIndex = 0;
    var indexes = [];
    while ((m = NTH_INT_RE.exec(selectorText)) !== null) indexes.push(m[1]);
    if (indexes.length > 0) {
      var nthKey = selectorText.replace(/:nth-child\(\d+\)/g, ':nth-child(N)');
      var nthGroup = state.nthGroups[nthKey];
      if (!nthGroup) {
        nthGroup = state.nthGroups[nthKey] = { selector: selectorText, indexes: {} };
      }
      for (var ni = 0; ni < indexes.length; ni++) nthGroup.indexes[indexes[ni]] = true;
    }

    ATTR_SEL_RE.lastIndex = 0;
    while ((m = ATTR_SEL_RE.exec(selectorText)) !== null) {
      var attr = m[1].toLowerCase();
      var val = m[3].replace(/^["']|["']$/g, '');
      if (!val) continue; // presence selectors carry no value to hand to attr()
      var base = selectorText.replace(ATTR_SEL_STRIP_RE, '').trim() || '*';
      var attrKey = base + '|' + attr;
      var attrGroup = state.attrGroups[attrKey];
      if (!attrGroup) {
        attrGroup = state.attrGroups[attrKey] = { base: base, attr: attr, sample: selectorText, values: {} };
      }
      attrGroup.values[val] = true;
    }
  }

  // Optical-centring tell: an explicit line-height paired with vertical padding
  // that differs top vs bottom is almost always compensation for the font's
  // half-leading, which text-box-trim removes at the source.
  function modernScanTextBox(state, style, selectorText) {
    if (state.textBox.length >= MODERN_FINDING_CAP) return;
    var lineHeight = style.getPropertyValue('line-height');
    if (!lineHeight || lineHeight === 'normal') return;
    var top = style.getPropertyValue('padding-top');
    var bottom = style.getPropertyValue('padding-bottom');
    if (!top || !bottom || top === bottom) return;
    state.textBox.push({ selector: selectorText, top: top, bottom: bottom });
  }

  function modernFinding(kind, selector, message, fix, extra) {
    var meta = MODERN_CSS[kind];
    var id = computeFindingID(kind, selector || meta.feature, message);
    if (selector) registerFinding(id, selector);
    var finding = {
      id: id,
      type: kind,
      severity: 'info',
      advisory: true,          // opportunity, not a defect — never scored
      feature: meta.feature,
      message: message,
      fix: fix,
      baseline: meta.baseline,
      fallback: meta.fallback
    };
    if (selector) finding.selector = selector;
    if (extra) {
      for (var k in extra) {
        if (extra.hasOwnProperty(k)) finding[k] = extra[k];
      }
    }
    return finding;
  }

  function buildModernFindings(state) {
    var out = [];
    var i;

    // alpha(): the same base color repeated once per opacity level.
    var colorKeys = Object.keys(state.colors).filter(function(k) {
      var b = state.colors[k];
      var alphaCount = Object.keys(b.alphas).length;
      return (b.opaque > 0 && alphaCount > 0) || alphaCount >= 2;
    }).sort(function(a, b) {
      return Object.keys(state.colors[b].alphas).length - Object.keys(state.colors[a].alphas).length;
    }).slice(0, MODERN_FINDING_CAP);
    for (i = 0; i < colorKeys.length; i++) {
      var bucket = state.colors[colorKeys[i]];
      var levels = Object.keys(bucket.alphas).length + (bucket.opaque > 0 ? 1 : 0);
      out.push(modernFinding('alpha-shorthand', null,
        bucket.sample + ' is declared at ' + levels + ' opacity levels as separate literals',
        'derive the translucent variants from the one token — alpha(var(--brand) / 60%) — so a color change lands in one place',
        { color: bucket.sample, opacityLevels: levels }));
    }

    // progress(): hand-rolled normalisation in calc().
    for (i = 0; i < state.progress.length; i++) {
      out.push(modernFinding('progress-function', state.progress[i].selector,
        'manual ratio math in calc(): ' + state.progress[i].snippet,
        'progress(<value>, <from>, <to>) returns the same unitless ratio and accepts mixed units on either end',
        { snippet: state.progress[i].snippet }));
    }

    // attr(): one rule per attribute value.
    var attrKeys = Object.keys(state.attrGroups).filter(function(k) {
      return Object.keys(state.attrGroups[k].values).length >= 3;
    }).sort(function(a, b) {
      return Object.keys(state.attrGroups[b].values).length - Object.keys(state.attrGroups[a].values).length;
    }).slice(0, MODERN_FINDING_CAP);
    for (i = 0; i < attrKeys.length; i++) {
      var g = state.attrGroups[attrKeys[i]];
      var valueCount = Object.keys(g.values).length;
      out.push(modernFinding('typed-attr', g.sample,
        valueCount + ' rules differ only by the [' + g.attr + '] value',
        'read the attribute as a typed value instead — e.g. padding: attr(' + g.attr + ' type(<length>), 1rem) — so a new value needs no new rule',
        { attribute: g.attr, valueCount: valueCount }));
    }

    // sibling-index()/sibling-count(): a rule per position.
    var nthKeys = Object.keys(state.nthGroups).filter(function(k) {
      return Object.keys(state.nthGroups[k].indexes).length >= 3;
    }).sort(function(a, b) {
      return Object.keys(state.nthGroups[b].indexes).length - Object.keys(state.nthGroups[a].indexes).length;
    }).slice(0, MODERN_FINDING_CAP);
    for (i = 0; i < nthKeys.length; i++) {
      var ng = state.nthGroups[nthKeys[i]];
      var idxCount = Object.keys(ng.indexes).length;
      out.push(modernFinding('sibling-index', ng.selector,
        idxCount + ' :nth-child() rules differ only by index — the set breaks when an item is added',
        'one rule covers any count: calc(360deg * sibling-index() / sibling-count()), no JS index pass and no per-position rule',
        { indexCount: idxCount }));
    }

    // text-box-trim: font-metric compensation done by hand.
    for (i = 0; i < state.textBox.length; i++) {
      var tb = state.textBox[i];
      out.push(modernFinding('text-box-trim', tb.selector,
        'line-height with asymmetric vertical padding (' + tb.top + ' / ' + tb.bottom + ') — the shape of half-leading compensation',
        'trim the leading at the source: text-box-trim: trim-both; text-box-edge: cap alphabetic — then equal padding centres the text',
        { paddingTop: tb.top, paddingBottom: tb.bottom }));
    }

    // shrink-to-fit: a fit-content child inside a full-width wrapper.
    if (state.fitContent > 0) {
      out.push(modernFinding('shrink-to-fit', null,
        state.fitContent + ' declaration(s) size an element to its content',
        'a wrapper that must hug such a child can do it directly with max-content-sizing: shrink-to-fit, instead of float/inline-block',
        { declarations: state.fitContent }));
    }

    return out;
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
      'vendor-prefixes',
      'modern-css-opportunities'
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

    var modern = newModernState();
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
          var inlineSel = shortSelector(elem);
          for (var prop in props) {
            if (!props.hasOwnProperty(prop)) continue;
            var value = props[prop];
            modernScanDeclaration(modern, prop, value, inlineSel);
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

    // One walk over the accessible rules feeds two checks. The !important
    // count stays top-level-only so its number keeps the meaning it always
    // had (a grouping rule's cssText already contains its children), while
    // the modern-CSS scan descends — the shapes it looks for routinely live
    // inside @media and nested rules.
    function walkRules(rules, depth) {
      for (var ri = 0; ri < rules.length; ri++) {
        if (modern.rulesScanned >= MODERN_RULE_CAP) {
          modern.truncated = true;
          return;
        }
        var rule = rules[ri];
        if (depth === 0 && rule.cssText && rule.cssText.indexOf('!important') !== -1) {
          metrics.importantCount++;
        }
        if (rule.selectorText && rule.style) {
          modern.rulesScanned++;
          modernScanSelector(modern, rule.selectorText);
          modernScanTextBox(modern, rule.style, rule.selectorText);
          for (var pi = 0; pi < rule.style.length; pi++) {
            var ruleProp = rule.style[pi];
            modernScanDeclaration(modern, ruleProp, rule.style.getPropertyValue(ruleProp), rule.selectorText);
          }
        }
        if (rule.cssRules) {
          walkRules(rule.cssRules, depth + 1);
        }
      }
    }

    for (var si = 0; si < document.styleSheets.length; si++) {
      try {
        walkRules(document.styleSheets[si].cssRules || [], 0);
      } catch (e) {
        // Cross-origin stylesheets can't be accessed — count instead of
        // silently swallowing so the report is honest about coverage.
        metrics.inaccessibleStylesheets++;
      }
    }

    var modernFindings = buildModernFindings(modern);
    for (var mi = 0; mi < modernFindings.length; mi++) {
      informational.push(modernFindings[mi]);
    }

    var coverageNote = null;
    if (metrics.inaccessibleStylesheets > 0) {
      coverageNote = metrics.inaccessibleStylesheets + ' of ' + metrics.stylesheetCount +
        ' stylesheets are cross-origin and could not be inspected — !important and rule-level checks cover accessible sheets only';
    }
    if (modern.truncated) {
      var truncNote = 'modern-CSS scan stopped after ' + MODERN_RULE_CAP +
        ' rules — opportunities in later rules are not reported';
      coverageNote = coverageNote ? coverageNote + '; ' + truncNote : truncNote;
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
        // Advisory: hand-rolled shapes a newer CSS primitive expresses
        // directly. Each carries its own support reality — read `baseline`
        // before adopting, and `fallback` for what to keep alongside it.
        modernCSS: {
          rulesScanned: modern.rulesScanned,
          opportunities: modernFindings
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
            zIndexData.length > 0 ? 'z-index layer system for ' + zIndexData.length + ' elevated elements' : null,
            modernFindings.length > 0 ? 'adopt ' + modernFindings.length + ' modern-CSS primitive(s), each gated on its own baseline field' : null
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
      modernCSS: modernFindings,
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

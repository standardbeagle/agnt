// Diagnostic CSS System for DevTool
// Visual debugging through CSS injection and overlay panels

(function() {
  'use strict';

  var utils = window.__devtool_utils;
  var core = window.__devtool_core;

  // Diagnostic state
  var state = {
    activeModes: {},
    styleElement: null,
    panels: {},
    nextPanelId: 1
  };

  // Color palette for visual distinction
  var COLORS = {
    red: '#ff0000',
    green: '#00ff00',
    blue: '#0000ff',
    yellow: '#ffff00',
    cyan: '#00ffff',
    magenta: '#ff00ff',
    orange: '#ff8800',
    purple: '#8800ff',
    lime: '#88ff00',
    pink: '#ff0088'
  };

  // ============================================================================
  // UTILITIES
  // ============================================================================

  function ensureStyleElement() {
    if (state.styleElement) return state.styleElement;

    var style = document.createElement('style');
    style.id = '__devtool-diagnostic-css';
    style.setAttribute('data-devtool', 'true');
    document.head.appendChild(style);
    state.styleElement = style;
    return style;
  }

  function injectCSS(css, mode) {
    var style = ensureStyleElement();
    var existing = style.textContent || '';

    // Add mode marker
    var marker = '/* MODE: ' + mode + ' */\n';
    style.textContent = existing + marker + css + '\n\n';

    state.activeModes[mode] = true;
  }

  function removeCSS(mode) {
    if (!state.styleElement) return;

    var content = state.styleElement.textContent || '';
    var lines = content.split('\n');
    var newLines = [];
    var skip = false;

    for (var i = 0; i < lines.length; i++) {
      if (lines[i].indexOf('/* MODE: ' + mode + ' */') !== -1) {
        skip = true;
        continue;
      }
      if (skip && lines[i].trim() === '') {
        skip = false;
        continue;
      }
      if (!skip) {
        newLines.push(lines[i]);
      }
    }

    state.styleElement.textContent = newLines.join('\n');
    delete state.activeModes[mode];
  }

  function createPanel(title, content, options) {
    options = options || {};
    var id = '__devtool-panel-' + state.nextPanelId++;

    var panel = document.createElement('div');
    panel.id = id;
    panel.setAttribute('data-devtool', 'true');
    panel.style.cssText = [
      'position: fixed',
      'top: ' + (options.top || '20px'),
      'right: ' + (options.right || '20px'),
      'max-width: ' + (options.maxWidth || '400px'),
      'max-height: ' + (options.maxHeight || '80vh'),
      'background: white',
      'border: 2px solid #333',
      'border-radius: 8px',
      'box-shadow: 0 4px 16px rgba(0,0,0,0.3)',
      'z-index: 2147483646',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
      'font-size: 14px',
      'overflow: auto'
    ].join(';');

    var header = document.createElement('div');
    header.style.cssText = [
      'padding: 12px 16px',
      'background: #333',
      'color: white',
      'font-weight: 600',
      'display: flex',
      'justify-content: space-between',
      'align-items: center',
      'position: sticky',
      'top: 0'
    ].join(';');
    header.textContent = title;

    var closeBtn = document.createElement('button');
    closeBtn.textContent = '×';
    closeBtn.setAttribute('aria-label', 'Close ' + title);
    closeBtn.title = 'Close ' + title;
    closeBtn.style.cssText = [
      'background: none',
      'border: none',
      'color: white',
      'font-size: 24px',
      'cursor: pointer',
      'padding: 0',
      'width: 24px',
      'height: 24px',
      'line-height: 1'
    ].join(';');
    closeBtn.onclick = function() {
      removePanel(id);
    };
    header.appendChild(closeBtn);

    var body = document.createElement('div');
    body.style.cssText = 'padding: 16px';

    if (typeof content === 'string') {
      body.innerHTML = content;
    } else {
      body.appendChild(content);
    }

    panel.appendChild(header);
    panel.appendChild(body);
    document.body.appendChild(panel);

    state.panels[id] = panel;
    return id;
  }

  function removePanel(panelId) {
    var panel = state.panels[panelId];
    if (panel && panel.parentNode) {
      panel.parentNode.removeChild(panel);
      delete state.panels[panelId];
    }
  }

  // ============================================================================
  // STRUCTURE & LAYOUT DIAGNOSTICS
  // ============================================================================

  function outlineAll() {
    var css = [
      '.__devtool-outline-all * {',
      '  outline: 1px solid ' + COLORS.red + ' !important;',
      '  outline-offset: -1px !important;',
      '}',
      '.__devtool-outline-all * * {',
      '  outline-color: ' + COLORS.green + ' !important;',
      '}',
      '.__devtool-outline-all * * * {',
      '  outline-color: ' + COLORS.blue + ' !important;',
      '}',
      '.__devtool-outline-all * * * * {',
      '  outline-color: ' + COLORS.yellow + ' !important;',
      '}',
      '.__devtool-outline-all * * * * * {',
      '  outline-color: ' + COLORS.cyan + ' !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'outline-all');
    document.body.classList.add('__devtool-outline-all');

    return { success: true, mode: 'outline-all' };
  }

  function showSemanticElements() {
    var css = [
      '.__devtool-semantic div { outline: 2px solid ' + COLORS.red + ' !important; }',
      '.__devtool-semantic span { outline: 2px solid ' + COLORS.blue + ' !important; }',
      '.__devtool-semantic section { outline: 3px solid ' + COLORS.green + ' !important; }',
      '.__devtool-semantic article { outline: 3px solid ' + COLORS.purple + ' !important; }',
      '.__devtool-semantic header { outline: 3px solid ' + COLORS.orange + ' !important; }',
      '.__devtool-semantic footer { outline: 3px solid ' + COLORS.cyan + ' !important; }',
      '.__devtool-semantic nav { outline: 3px solid ' + COLORS.magenta + ' !important; }',
      '.__devtool-semantic aside { outline: 3px solid ' + COLORS.lime + ' !important; }',
      '.__devtool-semantic main { outline: 3px solid ' + COLORS.pink + ' !important; }',
      '.__devtool-semantic p { outline: 1px dotted ' + COLORS.yellow + ' !important; }'
    ].join('\n');

    injectCSS(css, 'semantic');
    document.body.classList.add('__devtool-semantic');

    return {
      success: true,
      mode: 'semantic',
      legend: {
        div: COLORS.red,
        span: COLORS.blue,
        section: COLORS.green,
        article: COLORS.purple,
        header: COLORS.orange,
        footer: COLORS.cyan,
        nav: COLORS.magenta,
        aside: COLORS.lime,
        main: COLORS.pink,
        p: COLORS.yellow
      }
    };
  }

  function showContainers() {
    var css = [
      '.__devtool-containers .container,',
      '.__devtool-containers .wrapper,',
      '.__devtool-containers [class*="container"],',
      '.__devtool-containers [class*="wrapper"] {',
      '  outline: 3px dashed ' + COLORS.orange + ' !important;',
      '  background: rgba(255, 136, 0, 0.05) !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'containers');
    document.body.classList.add('__devtool-containers');

    return { success: true, mode: 'containers' };
  }

  function showGrid(options) {
    options = options || {};
    var color = options.color || 'rgba(255, 0, 0, 0.2)';
    var gapColor = options.gapColor || 'rgba(0, 255, 0, 0.3)';

    var css = [
      '.__devtool-grid [style*="display: grid"],',
      '.__devtool-grid [style*="display:grid"],',
      '.__devtool-grid .grid {',
      '  outline: 2px solid ' + COLORS.purple + ' !important;',
      '  background-image: repeating-linear-gradient(90deg, ' + color + ' 0px, transparent 1px, transparent 100%) !important;',
      '  position: relative !important;',
      '}',
      '.__devtool-grid [style*="display: grid"]::before,',
      '.__devtool-grid [style*="display:grid"]::before,',
      '.__devtool-grid .grid::before {',
      '  content: "GRID" !important;',
      '  position: absolute !important;',
      '  top: 0 !important;',
      '  left: 0 !important;',
      '  background: ' + COLORS.purple + ' !important;',
      '  color: white !important;',
      '  padding: 2px 6px !important;',
      '  font-size: 10px !important;',
      '  font-weight: bold !important;',
      '  z-index: 1 !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'grid');
    document.body.classList.add('__devtool-grid');

    return { success: true, mode: 'grid', options: options };
  }

  function showFlexbox(options) {
    options = options || {};

    var css = [
      '.__devtool-flexbox [style*="display: flex"],',
      '.__devtool-flexbox [style*="display:flex"],',
      '.__devtool-flexbox .flex {',
      '  outline: 2px solid ' + COLORS.cyan + ' !important;',
      '  background: rgba(0, 255, 255, 0.05) !important;',
      '  position: relative !important;',
      '}',
      '.__devtool-flexbox [style*="display: flex"]::before,',
      '.__devtool-flexbox [style*="display:flex"]::before,',
      '.__devtool-flexbox .flex::before {',
      '  content: "FLEX" !important;',
      '  position: absolute !important;',
      '  top: 0 !important;',
      '  right: 0 !important;',
      '  background: ' + COLORS.cyan + ' !important;',
      '  color: black !important;',
      '  padding: 2px 6px !important;',
      '  font-size: 10px !important;',
      '  font-weight: bold !important;',
      '  z-index: 1 !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'flexbox');
    document.body.classList.add('__devtool-flexbox');

    return { success: true, mode: 'flexbox', options: options };
  }

  function showGaps(options) {
    options = options || {};

    var css = [
      '.__devtool-gaps [style*="gap"],',
      '.__devtool-gaps [style*="grid-gap"],',
      '.__devtool-gaps [style*="column-gap"],',
      '.__devtool-gaps [style*="row-gap"] {',
      '  outline: 2px dashed ' + COLORS.lime + ' !important;',
      '}',
      '.__devtool-gaps [style*="gap"]::after,',
      '.__devtool-gaps [style*="grid-gap"]::after {',
      '  content: "HAS GAPS" !important;',
      '  position: absolute !important;',
      '  bottom: 0 !important;',
      '  left: 0 !important;',
      '  background: ' + COLORS.lime + ' !important;',
      '  color: black !important;',
      '  padding: 2px 6px !important;',
      '  font-size: 10px !important;',
      '  font-weight: bold !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'gaps');
    document.body.classList.add('__devtool-gaps');

    return { success: true, mode: 'gaps', options: options };
  }

  // ============================================================================
  // TYPOGRAPHY DIAGNOSTICS
  // ============================================================================

  function showTypographyPanel() {
    var elements = document.querySelectorAll('p, h1, h2, h3, h4, h5, h6, span, a, li, td, th, label, button');
    var styles = {};

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      var computed = window.getComputedStyle(el);

      var key = [
        computed.fontSize,
        computed.fontFamily.split(',')[0].replace(/['"]/g, ''),
        computed.fontWeight,
        computed.lineHeight,
        computed.color
      ].join('|');

      if (!styles[key]) {
        styles[key] = {
          fontSize: computed.fontSize,
          fontFamily: computed.fontFamily.split(',')[0].replace(/['"]/g, ''),
          fontWeight: computed.fontWeight,
          lineHeight: computed.lineHeight,
          color: computed.color,
          count: 0,
          elements: []
        };
      }

      styles[key].count++;
      if (styles[key].elements.length < 3) {
        styles[key].elements.push(utils.generateSelector(el));
      }
    }

    var styleArray = [];
    for (var styleKey in styles) {
      styleArray.push(styles[styleKey]);
    }

    styleArray.sort(function(a, b) { return b.count - a.count; });

    var html = '<div style="font-family: monospace; font-size: 12px;">';
    html += '<div style="margin-bottom: 12px; font-size: 14px; font-weight: bold;">Found ' + styleArray.length + ' unique text styles</div>';

    for (var j = 0; j < styleArray.length; j++) {
      var style = styleArray[j];
      var status = style.count > 10 ? '✓' : (style.count === 1 ? '✗' : '⚠');
      var statusColor = style.count > 10 ? '#22c55e' : (style.count === 1 ? '#ef4444' : '#f59e0b');

      html += '<div style="margin-bottom: 16px; padding: 12px; border: 1px solid #e5e7eb; border-radius: 4px;">';
      html += '<div style="display: flex; justify-content: space-between; margin-bottom: 8px;">';
      html += '<span style="font-weight: bold;">' + style.fontSize + ' / ' + style.fontFamily + ' / ' + style.fontWeight + '</span>';
      html += '<span style="color: ' + statusColor + ';">' + status + ' ' + style.count + '×</span>';
      html += '</div>';
      html += '<div style="font-size: ' + style.fontSize + '; font-family: ' + style.fontFamily + '; font-weight: ' + style.fontWeight + '; line-height: ' + style.lineHeight + '; color: ' + style.color + '; margin-bottom: 8px;">The quick brown fox jumps over the lazy dog</div>';
      html += '<div style="font-size: 10px; color: #666;">line-height: ' + style.lineHeight + ' | color: ' + style.color + '</div>';
      html += '</div>';
    }

    html += '</div>';

    var panelId = createPanel('Typography Audit', html, { maxHeight: '600px', maxWidth: '500px' });
    state.activeModes['typography-panel'] = panelId;

    return {
      success: true,
      mode: 'typography-panel',
      panelId: panelId,
      uniqueStyles: styleArray.length,
      styles: styleArray
    };
  }

  function highlightInconsistentText() {
    var elements = document.querySelectorAll('p, h1, h2, h3, h4, h5, h6, span, a, li');
    var fontSizes = {};

    for (var i = 0; i < elements.length; i++) {
      var size = window.getComputedStyle(elements[i]).fontSize;
      fontSizes[size] = (fontSizes[size] || 0) + 1;
    }

    var css = [];
    for (var size in fontSizes) {
      if (fontSizes[size] === 1) {
        css.push('.__devtool-inconsistent-text [style*="font-size: ' + size + '"] { outline: 2px solid ' + COLORS.red + ' !important; }');
      }
    }

    if (css.length > 0) {
      injectCSS(css.join('\n'), 'inconsistent-text');
      document.body.classList.add('__devtool-inconsistent-text');
    }

    return { success: true, mode: 'inconsistent-text', oneOffStyles: css.length };
  }

  function showTextBounds() {
    var css = [
      '.__devtool-text-bounds p,',
      '.__devtool-text-bounds h1,',
      '.__devtool-text-bounds h2,',
      '.__devtool-text-bounds h3,',
      '.__devtool-text-bounds h4,',
      '.__devtool-text-bounds h5,',
      '.__devtool-text-bounds h6,',
      '.__devtool-text-bounds span,',
      '.__devtool-text-bounds a {',
      '  outline: 1px dotted ' + COLORS.blue + ' !important;',
      '  background: rgba(0, 0, 255, 0.03) !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'text-bounds');
    document.body.classList.add('__devtool-text-bounds');

    return { success: true, mode: 'text-bounds' };
  }

  // ============================================================================
  // STACKING & LAYERING DIAGNOSTICS
  // ============================================================================

  function showStacking() {
    var elements = document.querySelectorAll('*');
    var zIndexElements = [];

    for (var i = 0; i < elements.length; i++) {
      var zIndex = window.getComputedStyle(elements[i]).zIndex;
      if (zIndex !== 'auto' && zIndex !== '0') {
        zIndexElements.push({
          element: elements[i],
          zIndex: parseInt(zIndex, 10),
          selector: utils.generateSelector(elements[i])
        });
      }
    }

    zIndexElements.sort(function(a, b) { return b.zIndex - a.zIndex; });

    var css = [
      '.__devtool-stacking [style*="z-index"] {',
      '  box-shadow: 0 0 0 3px ' + COLORS.orange + ' !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'stacking');
    document.body.classList.add('__devtool-stacking');

    return {
      success: true,
      mode: 'stacking',
      zIndexElements: zIndexElements
    };
  }

  function opacity(level) {
    level = level || 0.5;

    var css = [
      '.__devtool-opacity * {',
      '  opacity: ' + level + ' !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'opacity');
    document.body.classList.add('__devtool-opacity');

    return { success: true, mode: 'opacity', level: level };
  }

  function showPositioned() {
    var css = [
      '.__devtool-positioned [style*="position: absolute"],',
      '.__devtool-positioned [style*="position:absolute"] {',
      '  outline: 3px solid ' + COLORS.red + ' !important;',
      '}',
      '.__devtool-positioned [style*="position: fixed"],',
      '.__devtool-positioned [style*="position:fixed"] {',
      '  outline: 3px solid ' + COLORS.orange + ' !important;',
      '}',
      '.__devtool-positioned [style*="position: sticky"],',
      '.__devtool-positioned [style*="position:sticky"] {',
      '  outline: 3px solid ' + COLORS.purple + ' !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'positioned');
    document.body.classList.add('__devtool-positioned');

    return {
      success: true,
      mode: 'positioned',
      legend: {
        absolute: COLORS.red,
        fixed: COLORS.orange,
        sticky: COLORS.purple
      }
    };
  }

  // ============================================================================
  // INTERACTIVE ELEMENT DIAGNOSTICS
  // ============================================================================

  function showInteractive() {
    var css = [
      '.__devtool-interactive a,',
      '.__devtool-interactive button,',
      '.__devtool-interactive input,',
      '.__devtool-interactive select,',
      '.__devtool-interactive textarea,',
      '.__devtool-interactive [onclick],',
      '.__devtool-interactive [role="button"] {',
      '  outline: 2px solid ' + COLORS.lime + ' !important;',
      '  background: rgba(136, 255, 0, 0.2) !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'interactive');
    document.body.classList.add('__devtool-interactive');

    return { success: true, mode: 'interactive' };
  }

  function showFocusOrder() {
    var focusable = document.querySelectorAll('a, button, input, select, textarea, [tabindex]:not([tabindex="-1"])');
    var css = [];

    for (var i = 0; i < Math.min(focusable.length, 50); i++) {
      var el = focusable[i];
      el.setAttribute('data-focus-order', i + 1);

      css.push([
        '.__devtool-focus-order [data-focus-order="' + (i + 1) + '"]::before {',
        '  content: "' + (i + 1) + '" !important;',
        '  position: absolute !important;',
        '  top: -10px !important;',
        '  left: -10px !important;',
        '  background: ' + COLORS.red + ' !important;',
        '  color: white !important;',
        '  width: 20px !important;',
        '  height: 20px !important;',
        '  border-radius: 50% !important;',
        '  display: flex !important;',
        '  align-items: center !important;',
        '  justify-content: center !important;',
        '  font-size: 10px !important;',
        '  font-weight: bold !important;',
        '  z-index: 999999 !important;',
        '}'
      ].join('\n'));
    }

    injectCSS(css.join('\n'), 'focus-order');
    document.body.classList.add('__devtool-focus-order');

    return { success: true, mode: 'focus-order', count: focusable.length };
  }

  function showClickTargets() {
    var css = [
      '.__devtool-click-targets a,',
      '.__devtool-click-targets button,',
      '.__devtool-click-targets [onclick] {',
      '  min-width: 44px !important;',
      '  min-height: 44px !important;',
      '  outline: 1px dashed ' + COLORS.orange + ' !important;',
      '}'
    ].join('\n');

    injectCSS(css, 'click-targets');
    document.body.classList.add('__devtool-click-targets');

    return { success: true, mode: 'click-targets' };
  }

  // ============================================================================
  // RESPONSIVE DIAGNOSTICS
  // ============================================================================

  function showViewportInfo() {
    var info = [
      'Viewport: ' + window.innerWidth + ' × ' + window.innerHeight,
      'Screen: ' + window.screen.width + ' × ' + window.screen.height,
      'Device Pixel Ratio: ' + window.devicePixelRatio
    ].join('<br>');

    var panelId = createPanel('Viewport Info', info, {
      top: '80px',
      right: '20px',
      maxWidth: '300px'
    });

    state.activeModes['viewport-info'] = panelId;

    return {
      success: true,
      mode: 'viewport-info',
      panelId: panelId,
      width: window.innerWidth,
      height: window.innerHeight
    };
  }

  // ============================================================================
  // COLOR & SPACING DIAGNOSTICS
  // ============================================================================

  function showColorPalette() {
    var elements = document.querySelectorAll('*');
    var colors = {};

    for (var i = 0; i < elements.length; i++) {
      var computed = window.getComputedStyle(elements[i]);
      var color = computed.color;
      var bg = computed.backgroundColor;

      if (color && color !== 'rgba(0, 0, 0, 0)') {
        colors[color] = (colors[color] || 0) + 1;
      }
      if (bg && bg !== 'rgba(0, 0, 0, 0)' && bg !== 'transparent') {
        colors[bg] = (colors[bg] || 0) + 1;
      }
    }

    var colorArray = [];
    for (var c in colors) {
      colorArray.push({ color: c, count: colors[c] });
    }

    colorArray.sort(function(a, b) { return b.count - a.count; });

    var html = '<div style="font-family: monospace; font-size: 12px;">';
    html += '<div style="margin-bottom: 12px; font-weight: bold;">Found ' + colorArray.length + ' unique colors</div>';

    for (var j = 0; j < Math.min(colorArray.length, 30); j++) {
      var item = colorArray[j];
      html += '<div style="display: flex; align-items: center; margin-bottom: 8px;">';
      html += '<div style="width: 40px; height: 24px; background: ' + item.color + '; border: 1px solid #ccc; margin-right: 8px;"></div>';
      html += '<div style="flex: 1;">' + item.color + '</div>';
      html += '<div style="color: #666;">' + item.count + '×</div>';
      html += '</div>';
    }

    html += '</div>';

    var panelId = createPanel('Color Palette', html, {
      maxHeight: '600px',
      maxWidth: '400px'
    });

    state.activeModes['color-palette'] = panelId;

    return {
      success: true,
      mode: 'color-palette',
      panelId: panelId,
      uniqueColors: colorArray.length,
      colors: colorArray
    };
  }

  function showSpacingScale() {
    var elements = document.querySelectorAll('*');
    var spacing = {};

    for (var i = 0; i < Math.min(elements.length, 500); i++) {
      var computed = window.getComputedStyle(elements[i]);

      [computed.marginTop, computed.marginRight, computed.marginBottom, computed.marginLeft,
       computed.paddingTop, computed.paddingRight, computed.paddingBottom, computed.paddingLeft].forEach(function(val) {
        if (val && val !== '0px') {
          spacing[val] = (spacing[val] || 0) + 1;
        }
      });
    }

    var spacingArray = [];
    for (var s in spacing) {
      spacingArray.push({ value: s, count: spacing[s] });
    }

    spacingArray.sort(function(a, b) {
      return parseFloat(a.value) - parseFloat(b.value);
    });

    var html = '<div style="font-family: monospace; font-size: 12px;">';
    html += '<div style="margin-bottom: 12px; font-weight: bold;">Found ' + spacingArray.length + ' spacing values</div>';

    for (var j = 0; j < Math.min(spacingArray.length, 40); j++) {
      var item = spacingArray[j];
      var px = parseFloat(item.value);
      var rem = (px / 16).toFixed(2);

      html += '<div style="display: flex; justify-content: space-between; margin-bottom: 4px; padding: 4px; border-bottom: 1px solid #eee;">';
      html += '<div style="display: flex; gap: 12px;">';
      html += '<span style="width: 60px; font-weight: bold;">' + item.value + '</span>';
      html += '<span style="width: 60px; color: #666;">' + rem + 'rem</span>';
      html += '</div>';
      html += '<span style="color: #666;">' + item.count + '×</span>';
      html += '</div>';
    }

    html += '</div>';

    var panelId = createPanel('Spacing Scale', html, {
      maxHeight: '600px',
      maxWidth: '350px',
      top: '20px',
      right: '440px'
    });

    state.activeModes['spacing-scale'] = panelId;

    return {
      success: true,
      mode: 'spacing-scale',
      panelId: panelId,
      uniqueValues: spacingArray.length,
      values: spacingArray
    };
  }

  // ============================================================================
  // CONTROL FUNCTIONS
  // ============================================================================

  function clear(mode) {
    if (!mode) return clearAll();

    if (state.activeModes[mode]) {
      // If it's a panel, remove it
      if (typeof state.activeModes[mode] === 'string') {
        removePanel(state.activeModes[mode]);
      }

      // Remove CSS class
      var className = '__devtool-' + mode;
      document.body.classList.remove(className);

      // Remove CSS rules
      removeCSS(mode);

      delete state.activeModes[mode];
    }

    return { success: true, cleared: mode };
  }

  function clearAll() {
    // Remove all CSS classes
    for (var mode in state.activeModes) {
      var className = '__devtool-' + mode;
      document.body.classList.remove(className);
    }

    // Remove style element
    if (state.styleElement && state.styleElement.parentNode) {
      state.styleElement.parentNode.removeChild(state.styleElement);
      state.styleElement = null;
    }

    // Remove all panels
    for (var panelId in state.panels) {
      removePanel(panelId);
    }

    state.activeModes = {};

    return { success: true };
  }

  function list() {
    var modes = [];
    for (var mode in state.activeModes) {
      modes.push(mode);
    }
    return { activeModes: modes, count: modes.length };
  }

  // ============================================================================
  // DOM SNAPSHOT & DIFF
  // ============================================================================

  function serializeElement(el, includeStyles, captureAllStyles) {
    var obj = {
      tag: el.tagName.toLowerCase(),
      attrs: {},
      text: '',
      children: []
    };

    // Capture attributes
    for (var i = 0; i < el.attributes.length; i++) {
      var attr = el.attributes[i];
      obj.attrs[attr.name] = attr.value;
    }

    // Capture computed styles (if requested)
    if (includeStyles) {
      var computed = window.getComputedStyle(el);

      if (captureAllStyles) {
        // Capture all computed style properties
        obj.styles = {};
        for (var j = 0; j < computed.length; j++) {
          var prop = computed[j];
          obj.styles[prop] = computed.getPropertyValue(prop);
        }
      } else {
        // Capture only key layout/visual styles
        obj.styles = {
          display: computed.display,
          position: computed.position,
          width: computed.width,
          height: computed.height,
          margin: computed.margin,
          padding: computed.padding,
          background: computed.backgroundColor,
          color: computed.color,
          fontSize: computed.fontSize,
          fontWeight: computed.fontWeight,
          fontFamily: computed.fontFamily,
          lineHeight: computed.lineHeight,
          border: computed.border,
          borderRadius: computed.borderRadius,
          boxShadow: computed.boxShadow,
          opacity: computed.opacity,
          zIndex: computed.zIndex,
          transform: computed.transform
        };
      }
    }

    // Capture text nodes
    for (var j = 0; j < el.childNodes.length; j++) {
      var node = el.childNodes[j];
      if (node.nodeType === Node.TEXT_NODE) {
        var text = node.textContent.trim();
        if (text) {
          obj.text += text + ' ';
        }
      }
    }
    obj.text = obj.text.trim();

    // Recursively capture children
    for (var k = 0; k < el.children.length; k++) {
      obj.children.push(serializeElement(el.children[k], includeStyles, captureAllStyles));
    }

    return obj;
  }

  function captureDOMSnapshot(options) {
    options = options || {};
    var root = options.root || document.body;
    var includeStyles = options.includeStyles !== false; // Default true
    var captureAllStyles = options.captureAllStyles || false; // Default false (captures key styles only)

    var snapshot = {
      timestamp: Date.now(),
      url: window.location.href,
      viewport: {
        width: window.innerWidth,
        height: window.innerHeight
      },
      root: serializeElement(root, includeStyles, captureAllStyles),
      stats: {
        totalElements: 0,
        totalTextNodes: 0,
        maxDepth: 0
      },
      options: {
        includeStyles: includeStyles,
        captureAllStyles: captureAllStyles
      }
    };

    // Calculate stats
    function countElements(node, depth) {
      snapshot.stats.totalElements++;
      if (depth > snapshot.stats.maxDepth) {
        snapshot.stats.maxDepth = depth;
      }
      if (node.text) {
        snapshot.stats.totalTextNodes++;
      }
      for (var i = 0; i < node.children.length; i++) {
        countElements(node.children[i], depth + 1);
      }
    }
    countElements(snapshot.root, 0);

    return snapshot;
  }

  function generateNodePath(node, path) {
    path = path || [];
    if (!node.tag) return path;

    var selector = node.tag;
    if (node.attrs.id) {
      selector += '#' + node.attrs.id;
    } else if (node.attrs.class) {
      var classes = node.attrs.class.split(' ')[0];
      if (classes) selector += '.' + classes;
    }

    path.unshift(selector);
    return path;
  }

  function compareDOMSnapshots(baseline, current) {
    var added = [];
    var removed = [];
    var modified = [];

    function compareNodes(baseNode, currNode, path) {
      path = path || [];

      if (!baseNode && currNode) {
        // Node was added
        added.push({
          path: generateNodePath(currNode, path.slice()),
          node: currNode
        });
        return;
      }

      if (baseNode && !currNode) {
        // Node was removed
        removed.push({
          path: generateNodePath(baseNode, path.slice()),
          node: baseNode
        });
        return;
      }

      // Check for modifications
      var changes = [];

      // Compare tag
      if (baseNode.tag !== currNode.tag) {
        changes.push('tag changed from ' + baseNode.tag + ' to ' + currNode.tag);
      }

      // Compare text
      if (baseNode.text !== currNode.text) {
        changes.push('text changed');
      }

      // Compare attributes
      var baseAttrs = Object.keys(baseNode.attrs || {});
      var currAttrs = Object.keys(currNode.attrs || {});

      for (var i = 0; i < baseAttrs.length; i++) {
        var attr = baseAttrs[i];
        if (currNode.attrs[attr] !== baseNode.attrs[attr]) {
          changes.push('attribute "' + attr + '" changed');
        }
      }

      for (var j = 0; j < currAttrs.length; j++) {
        var cAttr = currAttrs[j];
        if (!baseNode.attrs[cAttr]) {
          changes.push('attribute "' + cAttr + '" added');
        }
      }

      if (changes.length > 0) {
        modified.push({
          path: generateNodePath(currNode, path.slice()),
          changes: changes,
          before: baseNode,
          after: currNode
        });
      }

      // Compare children
      var maxChildren = Math.max(
        baseNode.children ? baseNode.children.length : 0,
        currNode.children ? currNode.children.length : 0
      );

      for (var k = 0; k < maxChildren; k++) {
        var baseChild = baseNode.children ? baseNode.children[k] : null;
        var currChild = currNode.children ? currNode.children[k] : null;

        var childPath = path.slice();
        childPath.push(currNode.tag + '[' + k + ']');

        compareNodes(baseChild, currChild, childPath);
      }
    }

    compareNodes(baseline.root, current.root);

    return {
      added: added,
      removed: removed,
      modified: modified,
      summary: {
        totalChanges: added.length + removed.length + modified.length,
        added: added.length,
        removed: removed.length,
        modified: modified.length
      },
      baseline: {
        timestamp: baseline.timestamp,
        totalElements: baseline.stats.totalElements
      },
      current: {
        timestamp: current.timestamp,
        totalElements: current.stats.totalElements
      }
    };
  }

  function showDOMDiff(baseline, current) {
    var diff = compareDOMSnapshots(baseline, current);

    var html = '<div style="font-family: monospace; font-size: 12px;">';

    // Summary
    html += '<div style="margin-bottom: 16px; padding: 12px; background: #f0f0f0; border-radius: 4px;">';
    html += '<div style="font-weight: bold; margin-bottom: 8px;">DOM Diff Summary</div>';
    html += '<div>Total Changes: <strong>' + diff.summary.totalChanges + '</strong></div>';
    html += '<div style="color: #22c55e;">Added: ' + diff.summary.added + '</div>';
    html += '<div style="color: #ef4444;">Removed: ' + diff.summary.removed + '</div>';
    html += '<div style="color: #f59e0b;">Modified: ' + diff.summary.modified + '</div>';
    html += '</div>';

    // Added elements
    if (diff.added.length > 0) {
      html += '<div style="margin-bottom: 16px;">';
      html += '<div style="font-weight: bold; color: #22c55e; margin-bottom: 8px;">✓ Added Elements (' + diff.added.length + ')</div>';
      for (var i = 0; i < Math.min(diff.added.length, 20); i++) {
        var item = diff.added[i];
        html += '<div style="padding: 4px 8px; background: rgba(34, 197, 94, 0.1); margin-bottom: 4px; border-left: 3px solid #22c55e;">';
        html += '<code>' + item.path.join(' &gt; ') + '</code>';
        html += '</div>';
      }
      if (diff.added.length > 20) {
        html += '<div style="color: #666; font-size: 11px;">... and ' + (diff.added.length - 20) + ' more</div>';
      }
      html += '</div>';
    }

    // Removed elements
    if (diff.removed.length > 0) {
      html += '<div style="margin-bottom: 16px;">';
      html += '<div style="font-weight: bold; color: #ef4444; margin-bottom: 8px;">✗ Removed Elements (' + diff.removed.length + ')</div>';
      for (var j = 0; j < Math.min(diff.removed.length, 20); j++) {
        var rItem = diff.removed[j];
        html += '<div style="padding: 4px 8px; background: rgba(239, 68, 68, 0.1); margin-bottom: 4px; border-left: 3px solid #ef4444;">';
        html += '<code>' + rItem.path.join(' &gt; ') + '</code>';
        html += '</div>';
      }
      if (diff.removed.length > 20) {
        html += '<div style="color: #666; font-size: 11px;">... and ' + (diff.removed.length - 20) + ' more</div>';
      }
      html += '</div>';
    }

    // Modified elements
    if (diff.modified.length > 0) {
      html += '<div style="margin-bottom: 16px;">';
      html += '<div style="font-weight: bold; color: #f59e0b; margin-bottom: 8px;">⚠ Modified Elements (' + diff.modified.length + ')</div>';
      for (var k = 0; k < Math.min(diff.modified.length, 20); k++) {
        var mItem = diff.modified[k];
        html += '<div style="padding: 4px 8px; background: rgba(245, 158, 11, 0.1); margin-bottom: 4px; border-left: 3px solid #f59e0b;">';
        html += '<code>' + mItem.path.join(' &gt; ') + '</code><br>';
        html += '<div style="font-size: 10px; color: #666; margin-top: 4px;">' + mItem.changes.join(', ') + '</div>';
        html += '</div>';
      }
      if (diff.modified.length > 20) {
        html += '<div style="color: #666; font-size: 11px;">... and ' + (diff.modified.length - 20) + ' more</div>';
      }
      html += '</div>';
    }

    html += '</div>';

    var panelId = createPanel('DOM Diff', html, {
      maxHeight: '700px',
      maxWidth: '600px',
      top: '20px',
      right: '440px'
    });

    state.activeModes['dom-diff'] = panelId;

    return {
      success: true,
      mode: 'dom-diff',
      panelId: panelId,
      diff: diff
    };
  }

  function highlightDOMChanges(diff) {
    var css = [];

    // We can't directly highlight removed elements since they're gone
    // But we can highlight added and modified elements by their path

    // For now, just provide feedback that this would require element tracking
    return {
      success: false,
      message: 'Live DOM highlighting requires element tracking - use showDOMDiff() for diff panel instead',
      diff: diff
    };
  }

  // ============================================================================
  // EXPORT
  // ============================================================================

  window.__devtool_diagnostics = {
    // Structure & Layout
    outlineAll: outlineAll,
    showSemanticElements: showSemanticElements,
    showContainers: showContainers,
    showGrid: showGrid,
    showFlexbox: showFlexbox,
    showGaps: showGaps,

    // Typography
    showTypographyPanel: showTypographyPanel,
    highlightInconsistentText: highlightInconsistentText,
    showTextBounds: showTextBounds,

    // Stacking & Layering
    showStacking: showStacking,
    opacity: opacity,
    showPositioned: showPositioned,

    // Interactive
    showInteractive: showInteractive,
    showFocusOrder: showFocusOrder,
    showClickTargets: showClickTargets,

    // Responsive
    showViewportInfo: showViewportInfo,

    // Color & Spacing
    showColorPalette: showColorPalette,
    showSpacingScale: showSpacingScale,

    // DOM Snapshot & Diff
    captureDOMSnapshot: captureDOMSnapshot,
    compareDOMSnapshots: compareDOMSnapshots,
    showDOMDiff: showDOMDiff,
    highlightDOMChanges: highlightDOMChanges,

    // Control
    clear: clear,
    clearAll: clearAll,
    list: list,

    // State (for debugging)
    state: state
  };

  console.log('[DevTool] Diagnostics module loaded');
})();

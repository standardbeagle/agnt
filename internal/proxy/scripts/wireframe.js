// Wireframe generation module for DevTool
// Generates an SVG wireframe representation of the DOM structure
//
// Creates a simplified visual representation of the page layout,
// useful for AI agents to understand page structure without screenshots.

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // Default configuration
  var DEFAULT_CONFIG = {
    maxDepth: 10,              // Maximum DOM depth to traverse
    minWidth: 10,              // Minimum element width to include
    minHeight: 10,             // Minimum element height to include
    includeText: true,         // Include text labels for key elements
    includeClasses: false,     // Include class names in labels
    viewportOnly: false,       // Only include elements in viewport
    simplified: true,          // Use simplified wireframe style
    colorScheme: 'mono',       // 'mono', 'semantic', or 'depth'
    fontSize: 10,              // Font size for labels
    strokeWidth: 1,            // Stroke width for rectangles
    padding: 2,                // Padding inside rectangles for text
    excludeSelectors: [],      // CSS selectors to exclude
    includeSelectors: null,    // If set, only include these selectors
    maxElements: 500           // Maximum elements to include
  };

  // Semantic colors for different element types
  var SEMANTIC_COLORS = {
    header: '#4A90D9',
    nav: '#7B68EE',
    main: '#50C878',
    article: '#90EE90',
    section: '#98D8C8',
    aside: '#DDA0DD',
    footer: '#808080',
    form: '#FFB347',
    button: '#FF6B6B',
    input: '#FFD700',
    link: '#87CEEB',
    image: '#DEB887',
    heading: '#FF69B4',
    text: '#E0E0E0',
    list: '#B8B8B8',
    table: '#C0C0C0',
    default: '#A0A0A0'
  };

  // Depth-based colors (lighter to darker)
  var DEPTH_COLORS = [
    '#E8E8E8', '#D0D0D0', '#B8B8B8', '#A0A0A0',
    '#888888', '#707070', '#585858', '#404040',
    '#282828', '#101010'
  ];

  /**
   * Determine the semantic type of an element
   */
  function getElementType(el) {
    if (!el || !el.tagName) return 'default';

    var tag = el.tagName.toLowerCase();

    // Semantic HTML5 elements
    if (['header', 'nav', 'main', 'article', 'section', 'aside', 'footer'].indexOf(tag) !== -1) {
      return tag;
    }

    // Form elements
    if (tag === 'form') return 'form';
    if (tag === 'button' || (tag === 'input' && el.type === 'submit')) return 'button';
    if (['input', 'textarea', 'select'].indexOf(tag) !== -1) return 'input';

    // Links
    if (tag === 'a') return 'link';

    // Images
    if (['img', 'svg', 'picture', 'video', 'canvas'].indexOf(tag) !== -1) return 'image';

    // Headings
    if (/^h[1-6]$/.test(tag)) return 'heading';

    // Lists
    if (['ul', 'ol', 'dl', 'li', 'dt', 'dd'].indexOf(tag) !== -1) return 'list';

    // Tables
    if (['table', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td'].indexOf(tag) !== -1) return 'table';

    // Text containers
    if (['p', 'span', 'div', 'blockquote', 'pre', 'code'].indexOf(tag) !== -1) {
      // Check if it contains mostly text
      var text = (el.textContent || '').trim();
      if (text.length > 0 && el.children.length === 0) {
        return 'text';
      }
    }

    // Check role attribute for semantic meaning
    var role = el.getAttribute('role');
    if (role) {
      if (role === 'navigation') return 'nav';
      if (role === 'banner') return 'header';
      if (role === 'main') return 'main';
      if (role === 'contentinfo') return 'footer';
      if (role === 'complementary') return 'aside';
      if (role === 'button') return 'button';
      if (role === 'textbox') return 'input';
      if (role === 'link') return 'link';
    }

    return 'default';
  }

  /**
   * Get a short label for an element
   */
  function getElementLabel(el, config) {
    if (!el || !el.tagName) return '';

    var tag = el.tagName.toLowerCase();
    var label = tag;

    // Add ID if present
    if (el.id) {
      label += '#' + el.id.substring(0, 15);
    }

    // Add first class if requested
    if (config.includeClasses && el.classList && el.classList.length > 0) {
      label += '.' + el.classList[0].substring(0, 15);
    }

    // For headings, add the text
    if (/^h[1-6]$/.test(tag)) {
      var text = (el.textContent || '').trim().substring(0, 20);
      if (text) {
        label = tag + ': ' + text;
      }
    }

    // For buttons, add text
    if (tag === 'button' || (tag === 'input' && (el.type === 'submit' || el.type === 'button'))) {
      var btnText = (el.textContent || el.value || '').trim().substring(0, 15);
      if (btnText) {
        label = 'btn: ' + btnText;
      }
    }

    // For links, add href domain or text
    if (tag === 'a') {
      var linkText = (el.textContent || '').trim().substring(0, 15);
      if (linkText) {
        label = 'link: ' + linkText;
      }
    }

    // For images, add alt text
    if (tag === 'img') {
      var alt = (el.alt || '').trim().substring(0, 15);
      label = alt ? 'img: ' + alt : 'img';
    }

    // For inputs, add type and placeholder
    if (['input', 'textarea', 'select'].indexOf(tag) !== -1) {
      var type = el.type || 'text';
      var placeholder = (el.placeholder || '').substring(0, 10);
      label = type + (placeholder ? ': ' + placeholder : '');
    }

    return label;
  }

  /**
   * Check if element should be included based on config
   */
  function shouldIncludeElement(el, rect, config) {
    // Skip devtool elements
    if (utils.isDevtoolElement(el)) return false;

    // Skip invisible elements
    if (!rect || rect.width < config.minWidth || rect.height < config.minHeight) {
      return false;
    }

    // Skip zero-opacity or hidden elements
    try {
      var computed = window.getComputedStyle(el);
      if (computed.display === 'none' ||
          computed.visibility === 'hidden' ||
          parseFloat(computed.opacity) === 0) {
        return false;
      }
    } catch (e) {
      // Continue if style check fails
    }

    // Check viewport if configured
    if (config.viewportOnly) {
      var viewportHeight = window.innerHeight || document.documentElement.clientHeight;
      var viewportWidth = window.innerWidth || document.documentElement.clientWidth;

      if (rect.bottom < 0 || rect.top > viewportHeight ||
          rect.right < 0 || rect.left > viewportWidth) {
        return false;
      }
    }

    // Check exclude selectors
    if (config.excludeSelectors && config.excludeSelectors.length > 0) {
      for (var i = 0; i < config.excludeSelectors.length; i++) {
        try {
          if (el.matches(config.excludeSelectors[i])) {
            return false;
          }
        } catch (e) {
          // Invalid selector, skip
        }
      }
    }

    // Check include selectors
    if (config.includeSelectors && config.includeSelectors.length > 0) {
      var included = false;
      for (var j = 0; j < config.includeSelectors.length; j++) {
        try {
          if (el.matches(config.includeSelectors[j])) {
            included = true;
            break;
          }
        } catch (e) {
          // Invalid selector, skip
        }
      }
      if (!included) return false;
    }

    return true;
  }

  /**
   * Escape text for SVG
   */
  function escapeXml(text) {
    if (!text) return '';
    return text
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&apos;');
  }

  /**
   * Generate the SVG wireframe
   */
  function generateWireframe(options) {
    // Merge config with defaults
    var config = {};
    for (var key in DEFAULT_CONFIG) {
      config[key] = DEFAULT_CONFIG[key];
    }
    if (options) {
      for (var optKey in options) {
        if (DEFAULT_CONFIG.hasOwnProperty(optKey)) {
          config[optKey] = options[optKey];
        }
      }
    }

    // Calculate dimensions
    var pageWidth = document.documentElement.scrollWidth || window.innerWidth;
    var pageHeight = document.documentElement.scrollHeight || window.innerHeight;
    var viewportWidth = window.innerWidth || document.documentElement.clientWidth;
    var viewportHeight = window.innerHeight || document.documentElement.clientHeight;
    var scrollX = window.scrollX || window.pageXOffset || 0;
    var scrollY = window.scrollY || window.pageYOffset || 0;

    // Adjust dimensions for viewport-only mode
    var svgWidth = config.viewportOnly ? viewportWidth : pageWidth;
    var svgHeight = config.viewportOnly ? viewportHeight : pageHeight;
    var offsetX = config.viewportOnly ? scrollX : 0;
    var offsetY = config.viewportOnly ? scrollY : 0;

    // Collect elements to render
    var elements = [];
    var elementCount = 0;

    function walkDOM(el, depth) {
      if (depth > config.maxDepth || elementCount >= config.maxElements) return;
      if (!el || el.nodeType !== 1) return;

      var rect = utils.getRect(el);

      if (shouldIncludeElement(el, rect, config)) {
        var type = getElementType(el);
        var label = config.includeText ? getElementLabel(el, config) : '';

        elements.push({
          x: rect.left + scrollX - offsetX,
          y: rect.top + scrollY - offsetY,
          width: rect.width,
          height: rect.height,
          type: type,
          label: label,
          depth: depth,
          tag: el.tagName.toLowerCase(),
          id: el.id || null,
          selector: utils.generateSelector(el)
        });

        elementCount++;
      }

      // Walk children
      var children = el.children;
      if (children) {
        for (var i = 0; i < children.length && elementCount < config.maxElements; i++) {
          walkDOM(children[i], depth + 1);
        }
      }
    }

    // Start walking from body
    try {
      walkDOM(document.body, 0);
    } catch (e) {
      return { error: 'DOM traversal failed: ' + e.message };
    }

    // Generate SVG
    var svg = [];
    svg.push('<?xml version="1.0" encoding="UTF-8"?>');
    svg.push('<svg xmlns="http://www.w3.org/2000/svg" ');
    svg.push('width="' + svgWidth + '" height="' + svgHeight + '" ');
    svg.push('viewBox="0 0 ' + svgWidth + ' ' + svgHeight + '">');

    // Add metadata
    svg.push('<title>Wireframe - ' + escapeXml(document.title || 'Untitled') + '</title>');
    svg.push('<desc>Generated by agnt DevTool. Elements: ' + elements.length + '</desc>');

    // Add styles
    svg.push('<defs>');
    svg.push('<style type="text/css">');
    svg.push('.wireframe-rect { fill: none; stroke-width: ' + config.strokeWidth + '; }');
    svg.push('.wireframe-label { font-family: system-ui, sans-serif; font-size: ' + config.fontSize + 'px; fill: #333; }');
    if (config.simplified) {
      svg.push('.wireframe-rect { rx: 2; ry: 2; }');
    }
    svg.push('</style>');
    svg.push('</defs>');

    // Background
    svg.push('<rect x="0" y="0" width="100%" height="100%" fill="#FAFAFA"/>');

    // Render elements (back to front, so deeper elements render on top)
    elements.sort(function(a, b) {
      return a.depth - b.depth;
    });

    for (var i = 0; i < elements.length; i++) {
      var elem = elements[i];

      // Determine color
      var strokeColor;
      if (config.colorScheme === 'semantic') {
        strokeColor = SEMANTIC_COLORS[elem.type] || SEMANTIC_COLORS.default;
      } else if (config.colorScheme === 'depth') {
        var colorIndex = Math.min(elem.depth, DEPTH_COLORS.length - 1);
        strokeColor = DEPTH_COLORS[colorIndex];
      } else {
        strokeColor = '#666666';
      }

      // Render rectangle
      svg.push('<rect class="wireframe-rect" ');
      svg.push('x="' + elem.x + '" y="' + elem.y + '" ');
      svg.push('width="' + elem.width + '" height="' + elem.height + '" ');
      svg.push('stroke="' + strokeColor + '" ');
      if (config.colorScheme === 'semantic') {
        svg.push('fill="' + strokeColor + '" fill-opacity="0.1" ');
      }
      svg.push('data-selector="' + escapeXml(elem.selector) + '" ');
      svg.push('data-type="' + elem.type + '"/>');

      // Render label if there's room and text is enabled
      if (config.includeText && elem.label && elem.width > 30 && elem.height > 15) {
        var textX = elem.x + config.padding;
        var textY = elem.y + config.fontSize + config.padding;

        // Clip text to fit
        var maxChars = Math.floor((elem.width - config.padding * 2) / (config.fontSize * 0.6));
        var displayLabel = elem.label.substring(0, maxChars);
        if (displayLabel.length < elem.label.length) {
          displayLabel = displayLabel.substring(0, displayLabel.length - 1) + '\u2026';
        }

        svg.push('<text class="wireframe-label" ');
        svg.push('x="' + textX + '" y="' + textY + '">');
        svg.push(escapeXml(displayLabel));
        svg.push('</text>');
      }
    }

    // Add viewport indicator if showing full page
    if (!config.viewportOnly && (scrollX > 0 || scrollY > 0 || viewportHeight < pageHeight)) {
      svg.push('<rect x="' + scrollX + '" y="' + scrollY + '" ');
      svg.push('width="' + viewportWidth + '" height="' + viewportHeight + '" ');
      svg.push('fill="none" stroke="#FF4444" stroke-width="2" stroke-dasharray="8,4"/>');
      svg.push('<text x="' + (scrollX + 5) + '" y="' + (scrollY + 15) + '" ');
      svg.push('fill="#FF4444" font-size="12" font-family="system-ui, sans-serif">Viewport</text>');
    }

    svg.push('</svg>');

    return {
      svg: svg.join('\n'),
      width: svgWidth,
      height: svgHeight,
      elementCount: elements.length,
      viewportOnly: config.viewportOnly,
      truncated: elementCount >= config.maxElements,
      elements: elements.map(function(e) {
        return {
          selector: e.selector,
          type: e.type,
          label: e.label,
          bounds: { x: e.x, y: e.y, width: e.width, height: e.height }
        };
      })
    };
  }

  /**
   * Generate a minimal wireframe (fewer elements, no labels)
   */
  function generateMinimalWireframe(options) {
    var minimalDefaults = {
      maxDepth: 5,
      minWidth: 50,
      minHeight: 30,
      includeText: false,
      maxElements: 100,
      simplified: true,
      colorScheme: 'mono'
    };

    var config = {};
    for (var key in minimalDefaults) {
      config[key] = minimalDefaults[key];
    }
    if (options) {
      for (var optKey in options) {
        config[optKey] = options[optKey];
      }
    }

    return generateWireframe(config);
  }

  /**
   * Generate a semantic wireframe (color-coded by element type)
   */
  function generateSemanticWireframe(options) {
    var semanticDefaults = {
      colorScheme: 'semantic',
      includeText: true,
      maxDepth: 8
    };

    var config = {};
    for (var key in semanticDefaults) {
      config[key] = semanticDefaults[key];
    }
    if (options) {
      for (var optKey in options) {
        config[optKey] = options[optKey];
      }
    }

    return generateWireframe(config);
  }

  // Export wireframe functions
  window.__devtool_wireframe = {
    generate: generateWireframe,
    minimal: generateMinimalWireframe,
    semantic: generateSemanticWireframe,
    // Expose config for documentation
    defaultConfig: DEFAULT_CONFIG,
    semanticColors: SEMANTIC_COLORS
  };
})();

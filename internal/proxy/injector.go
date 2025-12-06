package proxy

import (
	"bytes"
	"strings"
	"sync"
)

var (
	// Cache the instrumentation script since it never changes
	cachedScript     string
	cachedScriptOnce sync.Once
)

// instrumentationScript returns JavaScript code for error and performance monitoring.
// The script is cached after first call for performance.
func instrumentationScript() string {
	cachedScriptOnce.Do(func() {
		cachedScript = generateInstrumentationScript()
	})
	return cachedScript
}

// generateInstrumentationScript creates the instrumentation JavaScript.
func generateInstrumentationScript() string {
	return `
<script src="https://cdn.jsdelivr.net/npm/html2canvas@1.4.1/dist/html2canvas.min.js"></script>
<script>
(function() {
  'use strict';

  // Configuration
  // Use relative WebSocket URL to automatically match the current connection
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const WS_URL = protocol + '//' + window.location.host + '/__devtool_metrics';
  let ws = null;
  let reconnectAttempts = 0;
  const MAX_RECONNECT_ATTEMPTS = 5;
  let pendingExecutions = new Map(); // Track pending JS executions

  // WebSocket connection
  function connect() {
    try {
      ws = new WebSocket(WS_URL);

      ws.onopen = function() {
        console.log('[DevTool] Metrics connection established');
        reconnectAttempts = 0;
        sendPageLoad();
      };

      ws.onmessage = function(event) {
        try {
          const message = JSON.parse(event.data);
          handleServerMessage(message);
        } catch (err) {
          console.error('[DevTool] Failed to parse server message:', err);
        }
      };

      ws.onclose = function() {
        console.log('[DevTool] Metrics connection closed');
        if (reconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
          reconnectAttempts++;
          setTimeout(connect, 1000 * reconnectAttempts);
        }
      };

      ws.onerror = function(err) {
        console.error('[DevTool] Metrics connection error:', err);
      };
    } catch (err) {
      console.error('[DevTool] Failed to create WebSocket:', err);
    }
  }

  // Handle messages from server
  function handleServerMessage(message) {
    if (message.type === 'execute') {
      executeJavaScript(message.id, message.code);
    }
  }

  // Execute JavaScript sent from server
  function executeJavaScript(execId, code) {
    const startTime = performance.now();
    let result, error;

    try {
      result = eval(code);
      // Convert result to string representation
      if (result === undefined) {
        result = 'undefined';
      } else if (result === null) {
        result = 'null';
      } else if (typeof result === 'function') {
        result = result.toString();
      } else if (typeof result === 'object') {
        try {
          result = JSON.stringify(result, null, 2);
        } catch (e) {
          result = String(result);
        }
      } else {
        result = String(result);
      }
    } catch (err) {
      error = err.toString();
      if (err.stack) {
        error += '\n' + err.stack;
      }
    }

    const duration = performance.now() - startTime;

    send('execution', {
      exec_id: execId,
      result: result || '',
      error: error || '',
      duration: duration,
      timestamp: Date.now()
    });
  }

  // Send metric to server
  function send(type, data) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      try {
        ws.send(JSON.stringify({ type: type, data: data, url: window.location.href }));
      } catch (err) {
        console.error('[DevTool] Failed to send metric:', err);
      }
    }
  }

  // Error tracking
  window.addEventListener('error', function(event) {
    send('error', {
      message: event.message,
      source: event.filename,
      lineno: event.lineno,
      colno: event.colno,
      error: event.error ? event.error.toString() : '',
      stack: event.error ? event.error.stack : '',
      timestamp: Date.now()
    });
  });

  // Promise rejection tracking
  window.addEventListener('unhandledrejection', function(event) {
    send('error', {
      message: 'Unhandled Promise Rejection: ' + event.reason,
      source: '',
      lineno: 0,
      colno: 0,
      error: event.reason ? event.reason.toString() : '',
      stack: event.reason && event.reason.stack ? event.reason.stack : '',
      timestamp: Date.now()
    });
  });

  // Performance tracking
  function sendPageLoad() {
    // Wait for load event
    if (document.readyState === 'complete') {
      capturePerformance();
    } else {
      window.addEventListener('load', capturePerformance);
    }
  }

  function capturePerformance() {
    // Use setTimeout to ensure all metrics are available
    setTimeout(function() {
      try {
        const perf = window.performance;
        if (!perf || !perf.timing) return;

        const timing = perf.timing;
        const navigation = perf.navigation;

        const metrics = {
          navigation_start: timing.navigationStart,
          dom_content_loaded: timing.domContentLoadedEventEnd - timing.navigationStart,
          load_event_end: timing.loadEventEnd - timing.navigationStart,
          dom_interactive: timing.domInteractive - timing.navigationStart,
          dom_complete: timing.domComplete - timing.navigationStart,
          timestamp: Date.now()
        };

        // Paint timing (if available)
        if (perf.getEntriesByType) {
          const paintEntries = perf.getEntriesByType('paint');
          paintEntries.forEach(function(entry) {
            if (entry.name === 'first-paint') {
              metrics.first_paint = Math.round(entry.startTime);
            } else if (entry.name === 'first-contentful-paint') {
              metrics.first_contentful_paint = Math.round(entry.startTime);
            }
          });

          // Resource timing (summary)
          const resources = perf.getEntriesByType('resource');
          if (resources && resources.length > 0) {
            metrics.resources = resources.slice(0, 50).map(function(r) {
              return {
                name: r.name,
                duration: Math.round(r.duration),
                size: r.transferSize || 0
              };
            });
          }
        }

        send('performance', metrics);
      } catch (err) {
        console.error('[DevTool] Failed to capture performance:', err);
      }
    }, 100);
  }

  // ============================================================================
  // PHASE 1: CORE INFRASTRUCTURE UTILITIES
  // ============================================================================

  // Resolve selector, element, or array to element
  function resolveElement(input) {
    if (!input) return null;
    if (input instanceof HTMLElement) return input;
    if (typeof input === 'string') {
      try {
        return document.querySelector(input);
      } catch (e) {
        return null;
      }
    }
    return null;
  }

  // Generate unique CSS selector for element
  function generateSelector(element) {
    if (!element || !(element instanceof HTMLElement)) return '';

    // Try ID first
    if (element.id) {
      return '#' + element.id;
    }

    // Build path from element to root
    var path = [];
    var current = element;

    while (current && current.nodeType === Node.ELEMENT_NODE) {
      var selector = current.nodeName.toLowerCase();

      // Add nth-of-type if needed
      if (current.parentNode) {
        var siblings = Array.prototype.filter.call(current.parentNode.children, function(el) {
          return el.nodeName === current.nodeName;
        });

        if (siblings.length > 1) {
          var index = siblings.indexOf(current) + 1;
          selector += ':nth-of-type(' + index + ')';
        }
      }

      path.unshift(selector);

      if (current.parentNode && current.parentNode.nodeType === Node.ELEMENT_NODE) {
        current = current.parentNode;
      } else {
        break;
      }
    }

    return path.join(' > ');
  }

  // Safe getComputedStyle wrapper
  function safeGetComputed(element, properties) {
    if (!element || !(element instanceof HTMLElement)) {
      return { error: 'Invalid element' };
    }

    try {
      var computed = window.getComputedStyle(element);
      var result = {};

      if (properties && Array.isArray(properties)) {
        // Get specific properties
        for (var i = 0; i < properties.length; i++) {
          var prop = properties[i];
          result[prop] = computed.getPropertyValue(prop) || computed[prop];
        }
      } else {
        // Get all common properties
        var commonProps = [
          'display', 'position', 'zIndex', 'opacity', 'visibility',
          'width', 'height', 'top', 'left', 'right', 'bottom',
          'margin', 'padding', 'border', 'backgroundColor', 'color'
        ];
        for (var j = 0; j < commonProps.length; j++) {
          var key = commonProps[j];
          result[key] = computed[key];
        }
      }

      return result;
    } catch (e) {
      return { error: e.message };
    }
  }

  // Parse CSS value to number (strips 'px', 'em', etc)
  function parseValue(value) {
    if (typeof value === 'number') return value;
    if (typeof value !== 'string') return 0;
    return parseFloat(value) || 0;
  }

  // Get element's bounding box with caching
  function getRect(element) {
    if (!element || !(element instanceof HTMLElement)) return null;
    try {
      return element.getBoundingClientRect();
    } catch (e) {
      return null;
    }
  }

  // Check if element is in viewport
  function isElementInViewport(element) {
    var rect = getRect(element);
    if (!rect) return false;

    return (
      rect.top >= 0 &&
      rect.left >= 0 &&
      rect.bottom <= (window.innerHeight || document.documentElement.clientHeight) &&
      rect.right <= (window.innerWidth || document.documentElement.clientWidth)
    );
  }

  // Find stacking context parent
  function getStackingContext(element) {
    if (!element || element === document.documentElement) return null;

    var parent = element.parentElement;
    while (parent && parent !== document.documentElement) {
      var computed = window.getComputedStyle(parent);

      // Check conditions that create stacking context
      if (
        computed.position !== 'static' && computed.zIndex !== 'auto' ||
        parseFloat(computed.opacity) < 1 ||
        computed.transform !== 'none' ||
        computed.filter !== 'none' ||
        computed.perspective !== 'none' ||
        computed.willChange === 'transform' || computed.willChange === 'opacity'
      ) {
        return parent;
      }

      parent = parent.parentElement;
    }

    return document.documentElement;
  }

  // Overlay management system
  var overlayState = {
    container: null,
    overlays: {},
    highlights: {},
    labels: {},
    nextId: 1
  };

  function initOverlayContainer() {
    if (overlayState.container) return overlayState.container;

    var container = document.createElement('div');
    container.id = '__devtool-overlays';
    container.style.cssText = [
      'position: fixed',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'pointer-events: none',
      'z-index: 2147483647',
      'overflow: hidden'
    ].join(';');

    document.documentElement.appendChild(container);
    overlayState.container = container;
    return container;
  }

  function removeOverlayContainer() {
    if (overlayState.container && overlayState.container.parentNode) {
      overlayState.container.parentNode.removeChild(overlayState.container);
      overlayState.container = null;
    }
  }

  function createOverlayElement(type, config) {
    var el = document.createElement('div');
    el.className = '__devtool-overlay-' + type;
    el.style.position = 'absolute';
    el.style.pointerEvents = 'none';
    return el;
  }

  // ============================================================================
  // PHASE 2: ELEMENT INSPECTION PRIMITIVES
  // ============================================================================

  function getElementInfo(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var attrs = {};
      for (var i = 0; i < el.attributes.length; i++) {
        var attr = el.attributes[i];
        attrs[attr.name] = attr.value;
      }

      return {
        element: el,
        selector: generateSelector(el),
        tag: el.tagName.toLowerCase(),
        id: el.id || null,
        classes: Array.prototype.slice.call(el.classList),
        attributes: attrs
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getPosition(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var rect = getRect(el);
      if (!rect) return { error: 'Failed to get bounding rect' };

      return {
        rect: {
          x: rect.x,
          y: rect.y,
          width: rect.width,
          height: rect.height,
          top: rect.top,
          right: rect.right,
          bottom: rect.bottom,
          left: rect.left
        },
        viewport: {
          x: rect.left,
          y: rect.top
        },
        document: {
          x: rect.left + window.scrollX,
          y: rect.top + window.scrollY
        },
        scroll: {
          x: window.scrollX,
          y: window.scrollY
        }
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getComputed(selector, properties) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    return safeGetComputed(el, properties);
  }

  function getBox(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);

      return {
        margin: {
          top: parseValue(computed.marginTop),
          right: parseValue(computed.marginRight),
          bottom: parseValue(computed.marginBottom),
          left: parseValue(computed.marginLeft)
        },
        border: {
          top: parseValue(computed.borderTopWidth),
          right: parseValue(computed.borderRightWidth),
          bottom: parseValue(computed.borderBottomWidth),
          left: parseValue(computed.borderLeftWidth)
        },
        padding: {
          top: parseValue(computed.paddingTop),
          right: parseValue(computed.paddingRight),
          bottom: parseValue(computed.paddingBottom),
          left: parseValue(computed.paddingLeft)
        },
        content: {
          width: el.clientWidth - parseValue(computed.paddingLeft) - parseValue(computed.paddingRight),
          height: el.clientHeight - parseValue(computed.paddingTop) - parseValue(computed.paddingBottom)
        }
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getLayout(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);
      var display = computed.display;

      var result = {
        display: display,
        position: computed.position,
        float: computed.float,
        clear: computed.clear
      };

      // Flexbox information
      if (display.indexOf('flex') !== -1) {
        result.flexbox = {
          container: true,
          direction: computed.flexDirection,
          wrap: computed.flexWrap,
          justifyContent: computed.justifyContent,
          alignItems: computed.alignItems,
          alignContent: computed.alignContent
        };
      } else if (el.parentElement && window.getComputedStyle(el.parentElement).display.indexOf('flex') !== -1) {
        result.flexbox = {
          container: false,
          flex: computed.flex,
          flexGrow: computed.flexGrow,
          flexShrink: computed.flexShrink,
          flexBasis: computed.flexBasis,
          alignSelf: computed.alignSelf,
          order: computed.order
        };
      }

      // Grid information
      if (display.indexOf('grid') !== -1) {
        result.grid = {
          container: true,
          templateColumns: computed.gridTemplateColumns,
          templateRows: computed.gridTemplateRows,
          gap: computed.gap,
          autoFlow: computed.gridAutoFlow
        };
      } else if (el.parentElement && window.getComputedStyle(el.parentElement).display.indexOf('grid') !== -1) {
        result.grid = {
          container: false,
          column: computed.gridColumn,
          row: computed.gridRow,
          area: computed.gridArea
        };
      }

      return result;
    } catch (e) {
      return { error: e.message };
    }
  }

  function getContainer(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);

      return {
        type: computed.containerType || 'normal',
        name: computed.containerName || null,
        contain: computed.contain || 'none'
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getStacking(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);
      var context = getStackingContext(el);

      return {
        zIndex: computed.zIndex,
        position: computed.position,
        context: context ? generateSelector(context) : null,
        opacity: parseFloat(computed.opacity),
        transform: computed.transform,
        filter: computed.filter
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getTransform(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);
      var transform = computed.transform;

      if (!transform || transform === 'none') {
        return {
          matrix: null,
          translate: { x: 0, y: 0 },
          rotate: 0,
          scale: { x: 1, y: 1 }
        };
      }

      return {
        matrix: transform,
        transform: transform,
        transformOrigin: computed.transformOrigin
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getOverflow(selector) {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);

      return {
        x: computed.overflowX,
        y: computed.overflowY,
        scrollWidth: el.scrollWidth,
        scrollHeight: el.scrollHeight,
        clientWidth: el.clientWidth,
        clientHeight: el.clientHeight,
        scrollTop: el.scrollTop,
        scrollLeft: el.scrollLeft,
        hasOverflow: el.scrollWidth > el.clientWidth || el.scrollHeight > el.clientHeight
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  // Global __devtool API
  window.__devtool = {
    // ========================================================================
    // LOGGING API
    // ========================================================================

    // Send custom log message
    log: function(message, level, data) {
      level = level || 'info';
      send('custom_log', {
        level: level,
        message: String(message),
        data: data || {},
        timestamp: Date.now()
      });
    },

    // Convenience methods
    debug: function(message, data) {
      this.log(message, 'debug', data);
    },

    info: function(message, data) {
      this.log(message, 'info', data);
    },

    warn: function(message, data) {
      this.log(message, 'warn', data);
    },

    error: function(message, data) {
      this.log(message, 'error', data);
    },

    // ========================================================================
    // ELEMENT INSPECTION PRIMITIVES
    // ========================================================================

    getElementInfo: getElementInfo,
    getPosition: getPosition,
    getComputed: getComputed,
    getBox: getBox,
    getLayout: getLayout,
    getContainer: getContainer,
    getStacking: getStacking,
    getTransform: getTransform,
    getOverflow: getOverflow,

    // ========================================================================
    // TREE WALKING PRIMITIVES
    // ========================================================================

    walkChildren: function(selector, depth, filter) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      depth = depth || 1;
      var results = [];

      function walk(element, currentDepth) {
        if (currentDepth > depth) return;

        var children = Array.prototype.slice.call(element.children);
        for (var i = 0; i < children.length; i++) {
          var child = children[i];

          if (!filter || filter(child)) {
            results.push({
              element: child,
              selector: generateSelector(child),
              depth: currentDepth
            });
          }

          if (currentDepth < depth) {
            walk(child, currentDepth + 1);
          }
        }
      }

      try {
        walk(el, 1);
        return { elements: results, count: results.length };
      } catch (e) {
        return { error: e.message };
      }
    },

    walkParents: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      var parents = [];
      var current = el.parentElement;

      while (current) {
        parents.push({
          element: current,
          selector: generateSelector(current),
          tag: current.tagName.toLowerCase()
        });
        current = current.parentElement;
      }

      return { parents: parents, count: parents.length };
    },

    findAncestor: function(selector, condition) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      if (typeof condition !== 'function') {
        return { error: 'Condition must be a function' };
      }

      var current = el.parentElement;
      while (current) {
        if (condition(current)) {
          return {
            element: current,
            selector: generateSelector(current)
          };
        }
        current = current.parentElement;
      }

      return { found: false };
    },

    // ========================================================================
    // VISUAL STATE PRIMITIVES
    // ========================================================================

    isVisible: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      try {
        var computed = window.getComputedStyle(el);
        var rect = getRect(el);

        if (!rect) {
          return { visible: false, reason: 'No bounding rect' };
        }

        if (computed.display === 'none') {
          return { visible: false, reason: 'display: none' };
        }

        if (computed.visibility === 'hidden') {
          return { visible: false, reason: 'visibility: hidden' };
        }

        if (parseFloat(computed.opacity) === 0) {
          return { visible: false, reason: 'opacity: 0' };
        }

        if (rect.width === 0 || rect.height === 0) {
          return { visible: false, reason: 'zero size' };
        }

        return { visible: true, area: rect.width * rect.height };
      } catch (e) {
        return { error: e.message };
      }
    },

    isInViewport: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      try {
        var rect = getRect(el);
        if (!rect) return { error: 'Failed to get bounding rect' };

        var viewportHeight = window.innerHeight || document.documentElement.clientHeight;
        var viewportWidth = window.innerWidth || document.documentElement.clientWidth;

        var intersecting = !(
          rect.bottom < 0 ||
          rect.top > viewportHeight ||
          rect.right < 0 ||
          rect.left > viewportWidth
        );

        var visibleWidth = Math.min(rect.right, viewportWidth) - Math.max(rect.left, 0);
        var visibleHeight = Math.min(rect.bottom, viewportHeight) - Math.max(rect.top, 0);
        var visibleArea = Math.max(0, visibleWidth) * Math.max(0, visibleHeight);
        var totalArea = rect.width * rect.height;
        var ratio = totalArea > 0 ? visibleArea / totalArea : 0;

        return {
          intersecting: intersecting,
          ratio: ratio,
          rect: rect,
          fullyVisible: ratio === 1
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    checkOverlap: function(selector1, selector2) {
      var el1 = resolveElement(selector1);
      var el2 = resolveElement(selector2);

      if (!el1 || !el2) return { error: 'Element not found' };

      try {
        var rect1 = getRect(el1);
        var rect2 = getRect(el2);

        if (!rect1 || !rect2) return { error: 'Failed to get bounding rects' };

        var overlaps = !(
          rect1.right < rect2.left ||
          rect1.left > rect2.right ||
          rect1.bottom < rect2.top ||
          rect1.top > rect2.bottom
        );

        if (!overlaps) {
          return { overlaps: false };
        }

        var overlapLeft = Math.max(rect1.left, rect2.left);
        var overlapRight = Math.min(rect1.right, rect2.right);
        var overlapTop = Math.max(rect1.top, rect2.top);
        var overlapBottom = Math.min(rect1.bottom, rect2.bottom);

        var overlapArea = (overlapRight - overlapLeft) * (overlapBottom - overlapTop);

        return {
          overlaps: true,
          area: overlapArea,
          rect: {
            left: overlapLeft,
            right: overlapRight,
            top: overlapTop,
            bottom: overlapBottom
          }
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    // ========================================================================
    // LAYOUT DIAGNOSTIC PRIMITIVES
    // ========================================================================

    findOverflows: function() {
      var elements = document.querySelectorAll('*');
      var results = [];

      for (var i = 0; i < elements.length; i++) {
        var el = elements[i];
        var overflow = getOverflow(el);

        if (overflow && overflow.hasOverflow) {
          results.push({
            element: el,
            selector: generateSelector(el),
            type: overflow.x === 'hidden' || overflow.y === 'hidden' ? 'hidden' : 'scrollable',
            scrollWidth: overflow.scrollWidth,
            scrollHeight: overflow.scrollHeight,
            clientWidth: overflow.clientWidth,
            clientHeight: overflow.clientHeight
          });
        }
      }

      return { overflows: results, count: results.length };
    },

    findStackingContexts: function() {
      var elements = document.querySelectorAll('*');
      var contexts = [];

      for (var i = 0; i < elements.length; i++) {
        var el = elements[i];
        var computed = window.getComputedStyle(el);

        var isContext = (
          (computed.position !== 'static' && computed.zIndex !== 'auto') ||
          parseFloat(computed.opacity) < 1 ||
          computed.transform !== 'none' ||
          computed.filter !== 'none' ||
          computed.perspective !== 'none'
        );

        if (isContext) {
          contexts.push({
            element: el,
            selector: generateSelector(el),
            zIndex: computed.zIndex,
            reason: []
          });

          var last = contexts[contexts.length - 1];
          if (computed.position !== 'static' && computed.zIndex !== 'auto') {
            last.reason.push('positioned');
          }
          if (parseFloat(computed.opacity) < 1) {
            last.reason.push('opacity');
          }
          if (computed.transform !== 'none') {
            last.reason.push('transform');
          }
          if (computed.filter !== 'none') {
            last.reason.push('filter');
          }
        }
      }

      return { contexts: contexts, count: contexts.length };
    },

    findOffscreen: function() {
      var elements = document.querySelectorAll('*');
      var results = [];

      for (var i = 0; i < elements.length; i++) {
        var el = elements[i];
        var viewport = this.isInViewport(el);

        if (viewport && !viewport.intersecting) {
          var rect = viewport.rect;
          var direction = [];

          if (rect.bottom < 0) direction.push('above');
          if (rect.top > window.innerHeight) direction.push('below');
          if (rect.right < 0) direction.push('left');
          if (rect.left > window.innerWidth) direction.push('right');

          results.push({
            element: el,
            selector: generateSelector(el),
            direction: direction,
            rect: rect
          });
        }
      }

      return { offscreen: results, count: results.length };
    },

    // ========================================================================
    // VISUAL OVERLAY SYSTEM
    // ========================================================================

    highlight: function(selector, config) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      config = config || {};
      var color = config.color || 'rgba(0, 123, 255, 0.3)';
      var duration = config.duration;
      var id = 'highlight-' + overlayState.nextId++;

      try {
        initOverlayContainer();
        var rect = getRect(el);

        var highlight = createOverlayElement('highlight', config);
        highlight.id = id;
        highlight.style.cssText += [
          'top: ' + rect.top + 'px',
          'left: ' + rect.left + 'px',
          'width: ' + rect.width + 'px',
          'height: ' + rect.height + 'px',
          'background-color: ' + color,
          'border: 2px solid ' + (config.borderColor || '#007bff'),
          'box-sizing: border-box'
        ].join(';');

        overlayState.container.appendChild(highlight);
        overlayState.highlights[id] = highlight;

        if (duration) {
          setTimeout(function() {
            window.__devtool.removeHighlight(id);
          }, duration);
        }

        return { highlightId: id };
      } catch (e) {
        return { error: e.message };
      }
    },

    removeHighlight: function(highlightId) {
      var highlight = overlayState.highlights[highlightId];
      if (highlight && highlight.parentNode) {
        highlight.parentNode.removeChild(highlight);
        delete overlayState.highlights[highlightId];
      }
    },

    clearAllOverlays: function() {
      overlayState.overlays = {};
      overlayState.highlights = {};
      overlayState.labels = {};

      if (overlayState.container) {
        removeOverlayContainer();
      }
    },

    // ========================================================================
    // INTERACTIVE PRIMITIVES
    // ========================================================================

    measureBetween: function(selector1, selector2) {
      var el1 = resolveElement(selector1);
      var el2 = resolveElement(selector2);

      if (!el1 || !el2) return { error: 'Element not found' };

      try {
        var rect1 = getRect(el1);
        var rect2 = getRect(el2);

        if (!rect1 || !rect2) return { error: 'Failed to get bounding rects' };

        var center1 = {
          x: rect1.left + rect1.width / 2,
          y: rect1.top + rect1.height / 2
        };

        var center2 = {
          x: rect2.left + rect2.width / 2,
          y: rect2.top + rect2.height / 2
        };

        var dx = center2.x - center1.x;
        var dy = center2.y - center1.y;
        var diagonal = Math.sqrt(dx * dx + dy * dy);

        return {
          distance: {
            x: Math.abs(dx),
            y: Math.abs(dy),
            diagonal: diagonal
          },
          direction: {
            horizontal: dx > 0 ? 'right' : 'left',
            vertical: dy > 0 ? 'down' : 'up'
          }
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    // ========================================================================
    // COMPOSITE CONVENIENCE FUNCTIONS
    // ========================================================================

    inspect: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      var info = getElementInfo(selector);
      var position = getPosition(selector);
      var box = getBox(selector);
      var layout = getLayout(selector);
      var stacking = getStacking(selector);
      var container = getContainer(selector);
      var visibility = this.isVisible(selector);
      var viewport = this.isInViewport(selector);

      return {
        info: info,
        position: position,
        box: box,
        layout: layout,
        stacking: stacking,
        container: container,
        visibility: visibility,
        viewport: viewport
      };
    },

    diagnoseLayout: function(selector) {
      var el = resolveElement(selector);
      var overflows = this.findOverflows();
      var contexts = this.findStackingContexts();
      var offscreen = this.findOffscreen();

      var result = {
        overflows: overflows,
        stackingContexts: contexts,
        offscreen: offscreen
      };

      if (selector) {
        var stacking = getStacking(selector);
        result.element = {
          selector: generateSelector(el),
          stacking: stacking
        };
      }

      return result;
    },

    showLayout: function(config) {
      config = config || {};
      console.log('[DevTool] Layout visualization not yet fully implemented');
      console.log('[DevTool] Use highlight() to mark specific elements');
      return { message: 'Use highlight() for now' };
    },

    // ========================================================================
    // PHASE 7: INTERACTIVE PRIMITIVES (ADVANCED)
    // ========================================================================

    selectElement: function() {
      return new Promise(function(resolve, reject) {
        var overlay = document.createElement('div');
        overlay.style.cssText = [
          'position: fixed',
          'top: 0',
          'left: 0',
          'right: 0',
          'bottom: 0',
          'z-index: 2147483646',
          'cursor: crosshair',
          'background: rgba(0, 0, 0, 0.1)'
        ].join(';');

        var highlightBox = document.createElement('div');
        highlightBox.style.cssText = [
          'position: absolute',
          'border: 2px solid #007bff',
          'background: rgba(0, 123, 255, 0.1)',
          'pointer-events: none',
          'display: none'
        ].join(';');
        overlay.appendChild(highlightBox);

        var labelBox = document.createElement('div');
        labelBox.style.cssText = [
          'position: absolute',
          'background: #007bff',
          'color: white',
          'padding: 4px 8px',
          'font-size: 12px',
          'font-family: monospace',
          'border-radius: 3px',
          'pointer-events: none',
          'display: none',
          'white-space: nowrap'
        ].join(';');
        overlay.appendChild(labelBox);

        function cleanup() {
          if (overlay.parentNode) {
            overlay.parentNode.removeChild(overlay);
          }
        }

        overlay.addEventListener('mousemove', function(e) {
          var target = document.elementFromPoint(e.clientX, e.clientY);
          if (!target || target === overlay || target === highlightBox || target === labelBox) {
            highlightBox.style.display = 'none';
            labelBox.style.display = 'none';
            return;
          }

          var rect = target.getBoundingClientRect();
          highlightBox.style.cssText += [
            'display: block',
            'top: ' + rect.top + 'px',
            'left: ' + rect.left + 'px',
            'width: ' + rect.width + 'px',
            'height: ' + rect.height + 'px'
          ].join(';');

          var selector = generateSelector(target);
          labelBox.textContent = selector;
          labelBox.style.cssText += [
            'display: block',
            'top: ' + (rect.top - 25) + 'px',
            'left: ' + rect.left + 'px'
          ].join(';');
        });

        overlay.addEventListener('click', function(e) {
          e.preventDefault();
          e.stopPropagation();

          var target = document.elementFromPoint(e.clientX, e.clientY);
          if (target && target !== overlay && target !== highlightBox && target !== labelBox) {
            var selector = generateSelector(target);
            cleanup();
            resolve(selector);
          }
        });

        overlay.addEventListener('keydown', function(e) {
          if (e.key === 'Escape') {
            cleanup();
            reject(new Error('Selection cancelled'));
          }
        });

        document.body.appendChild(overlay);
        overlay.focus();
      });
    },

    waitForElement: function(selector, timeout) {
      timeout = timeout || 5000;
      var startTime = Date.now();

      return new Promise(function(resolve, reject) {
        var el = resolveElement(selector);
        if (el) {
          resolve(el);
          return;
        }

        var observer = new MutationObserver(function(mutations) {
          var el = resolveElement(selector);
          if (el) {
            observer.disconnect();
            resolve(el);
          } else if (Date.now() - startTime > timeout) {
            observer.disconnect();
            reject(new Error('Timeout waiting for element: ' + selector));
          }
        });

        observer.observe(document.body, {
          childList: true,
          subtree: true
        });

        setTimeout(function() {
          observer.disconnect();
          reject(new Error('Timeout waiting for element: ' + selector));
        }, timeout);
      });
    },

    ask: function(question, options) {
      return new Promise(function(resolve, reject) {
        var modal = document.createElement('div');
        modal.style.cssText = [
          'position: fixed',
          'top: 50%',
          'left: 50%',
          'transform: translate(-50%, -50%)',
          'background: white',
          'padding: 20px',
          'border-radius: 8px',
          'box-shadow: 0 4px 20px rgba(0,0,0,0.3)',
          'z-index: 2147483647',
          'min-width: 300px',
          'max-width: 500px'
        ].join(';');

        var overlay = document.createElement('div');
        overlay.style.cssText = [
          'position: fixed',
          'top: 0',
          'left: 0',
          'right: 0',
          'bottom: 0',
          'background: rgba(0,0,0,0.5)',
          'z-index: 2147483646'
        ].join(';');

        var title = document.createElement('h3');
        title.style.cssText = 'margin: 0 0 15px 0; color: #333;';
        title.textContent = question;
        modal.appendChild(title);

        var buttonContainer = document.createElement('div');
        buttonContainer.style.cssText = 'display: flex; gap: 10px; flex-wrap: wrap;';

        options = options || ['Yes', 'No'];
        for (var i = 0; i < options.length; i++) {
          (function(option) {
            var btn = document.createElement('button');
            btn.textContent = option;
            btn.style.cssText = [
              'padding: 10px 20px',
              'border: none',
              'border-radius: 4px',
              'background: #007bff',
              'color: white',
              'cursor: pointer',
              'font-size: 14px'
            ].join(';');

            btn.addEventListener('mouseover', function() {
              this.style.background = '#0056b3';
            });

            btn.addEventListener('mouseout', function() {
              this.style.background = '#007bff';
            });

            btn.addEventListener('click', function() {
              cleanup();
              resolve(option);
            });

            buttonContainer.appendChild(btn);
          })(options[i]);
        }

        modal.appendChild(buttonContainer);

        function cleanup() {
          if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
          if (modal.parentNode) modal.parentNode.removeChild(modal);
        }

        overlay.addEventListener('click', function() {
          cleanup();
          reject(new Error('Question cancelled'));
        });

        document.body.appendChild(overlay);
        document.body.appendChild(modal);
      });
    },

    // ========================================================================
    // PHASE 8: STATE CAPTURE PRIMITIVES
    // ========================================================================

    captureDOM: function() {
      try {
        var html = document.documentElement.outerHTML;
        var hash = 0;
        for (var i = 0; i < html.length; i++) {
          var char = html.charCodeAt(i);
          hash = ((hash << 5) - hash) + char;
          hash = hash & hash;
        }

        return {
          snapshot: html,
          hash: hash.toString(16),
          timestamp: Date.now(),
          url: window.location.href,
          size: html.length
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    captureStyles: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      try {
        var computed = window.getComputedStyle(el);
        var inline = el.style.cssText;

        var computedObj = {};
        for (var i = 0; i < computed.length; i++) {
          var prop = computed[i];
          computedObj[prop] = computed.getPropertyValue(prop);
        }

        return {
          selector: generateSelector(el),
          computed: computedObj,
          inline: inline,
          timestamp: Date.now()
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    captureState: function(keys) {
      try {
        var state = {
          timestamp: Date.now(),
          url: window.location.href
        };

        if (!keys || keys.indexOf('localStorage') !== -1) {
          state.localStorage = {};
          try {
            for (var i = 0; i < localStorage.length; i++) {
              var key = localStorage.key(i);
              state.localStorage[key] = localStorage.getItem(key);
            }
          } catch (e) {
            state.localStorage = { error: 'Access denied' };
          }
        }

        if (!keys || keys.indexOf('sessionStorage') !== -1) {
          state.sessionStorage = {};
          try {
            for (var j = 0; j < sessionStorage.length; j++) {
              var skey = sessionStorage.key(j);
              state.sessionStorage[skey] = sessionStorage.getItem(skey);
            }
          } catch (e) {
            state.sessionStorage = { error: 'Access denied' };
          }
        }

        if (!keys || keys.indexOf('cookies') !== -1) {
          state.cookies = document.cookie;
        }

        return state;
      } catch (e) {
        return { error: e.message };
      }
    },

    captureNetwork: function() {
      try {
        var resources = [];
        if (window.performance && window.performance.getEntriesByType) {
          var entries = window.performance.getEntriesByType('resource');
          for (var i = 0; i < entries.length; i++) {
            var entry = entries[i];
            resources.push({
              name: entry.name,
              type: entry.initiatorType,
              duration: entry.duration,
              size: entry.transferSize || 0,
              startTime: entry.startTime
            });
          }
        }

        return {
          resources: resources,
          count: resources.length,
          timestamp: Date.now()
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    // ========================================================================
    // PHASE 9: ACCESSIBILITY PRIMITIVES
    // ========================================================================

    getA11yInfo: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      try {
        var computed = window.getComputedStyle(el);

        var ariaAttrs = {};
        for (var i = 0; i < el.attributes.length; i++) {
          var attr = el.attributes[i];
          if (attr.name.startsWith('aria-')) {
            ariaAttrs[attr.name] = attr.value;
          }
        }

        return {
          role: el.getAttribute('role') || el.tagName.toLowerCase(),
          aria: ariaAttrs,
          tabindex: el.tabIndex,
          focusable: el.tabIndex >= 0 || ['A', 'BUTTON', 'INPUT', 'SELECT', 'TEXTAREA'].indexOf(el.tagName) !== -1,
          label: el.getAttribute('aria-label') || el.getAttribute('aria-labelledby') || el.textContent.trim().substring(0, 50),
          hidden: computed.display === 'none' || computed.visibility === 'hidden' || el.getAttribute('aria-hidden') === 'true'
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    getContrast: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      try {
        var computed = window.getComputedStyle(el);

        function parseColor(color) {
          var match = color.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
          if (match) {
            return {
              r: parseInt(match[1]),
              g: parseInt(match[2]),
              b: parseInt(match[3])
            };
          }
          return null;
        }

        function getLuminance(rgb) {
          var rsRGB = rgb.r / 255;
          var gsRGB = rgb.g / 255;
          var bsRGB = rgb.b / 255;

          var r = rsRGB <= 0.03928 ? rsRGB / 12.92 : Math.pow((rsRGB + 0.055) / 1.055, 2.4);
          var g = gsRGB <= 0.03928 ? gsRGB / 12.92 : Math.pow((gsRGB + 0.055) / 1.055, 2.4);
          var b = bsRGB <= 0.03928 ? bsRGB / 12.92 : Math.pow((bsRGB + 0.055) / 1.055, 2.4);

          return 0.2126 * r + 0.7152 * g + 0.0722 * b;
        }

        function getContrastRatio(fg, bg) {
          var l1 = getLuminance(fg);
          var l2 = getLuminance(bg);
          var lighter = Math.max(l1, l2);
          var darker = Math.min(l1, l2);
          return (lighter + 0.05) / (darker + 0.05);
        }

        var fgColor = parseColor(computed.color);
        var bgColor = parseColor(computed.backgroundColor);

        if (!fgColor || !bgColor) {
          return { error: 'Could not parse colors' };
        }

        var ratio = getContrastRatio(fgColor, bgColor);

        return {
          foreground: computed.color,
          background: computed.backgroundColor,
          ratio: ratio.toFixed(2),
          passes: {
            AA: ratio >= 4.5,
            AALarge: ratio >= 3,
            AAA: ratio >= 7,
            AAALarge: ratio >= 4.5
          }
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    getTabOrder: function(container) {
      var root = container ? resolveElement(container) : document.body;
      if (!root) return { error: 'Container not found' };

      try {
        var focusable = root.querySelectorAll(
          'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])'
        );

        var elements = [];
        for (var i = 0; i < focusable.length; i++) {
          var el = focusable[i];
          elements.push({
            element: el,
            selector: generateSelector(el),
            tabindex: el.tabIndex,
            tag: el.tagName.toLowerCase()
          });
        }

        elements.sort(function(a, b) {
          if (a.tabindex === 0 && b.tabindex === 0) return 0;
          if (a.tabindex === 0) return 1;
          if (b.tabindex === 0) return -1;
          return a.tabindex - b.tabindex;
        });

        return { elements: elements, count: elements.length };
      } catch (e) {
        return { error: e.message };
      }
    },

    getScreenReaderText: function(selector) {
      var el = resolveElement(selector);
      if (!el) return { error: 'Element not found' };

      try {
        var ariaLabel = el.getAttribute('aria-label');
        if (ariaLabel) return ariaLabel;

        var ariaLabelledBy = el.getAttribute('aria-labelledby');
        if (ariaLabelledBy) {
          var labelEl = document.getElementById(ariaLabelledBy);
          if (labelEl) return labelEl.textContent.trim();
        }

        if (el.tagName === 'IMG') {
          return el.alt || '(No alt text)';
        }

        if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') {
          var label = document.querySelector('label[for="' + el.id + '"]');
          if (label) return label.textContent.trim();
        }

        return el.textContent.trim();
      } catch (e) {
        return { error: e.message };
      }
    },

    auditAccessibility: function() {
      try {
        var errors = [];
        var warnings = [];

        var imgs = document.querySelectorAll('img');
        for (var i = 0; i < imgs.length; i++) {
          var img = imgs[i];
          if (!img.alt) {
            errors.push({
              rule: 'img-alt',
              element: generateSelector(img),
              message: 'Image missing alt text',
              fix: 'Add alt attribute to image'
            });
          }
        }

        var buttons = document.querySelectorAll('button');
        for (var j = 0; j < buttons.length; j++) {
          var btn = buttons[j];
          if (!btn.textContent.trim() && !btn.getAttribute('aria-label')) {
            errors.push({
              rule: 'button-name',
              element: generateSelector(btn),
              message: 'Button has no accessible name',
              fix: 'Add text content or aria-label'
            });
          }
        }

        var inputs = document.querySelectorAll('input, textarea, select');
        for (var k = 0; k < inputs.length; k++) {
          var input = inputs[k];
          if (input.id) {
            var label = document.querySelector('label[for="' + input.id + '"]');
            if (!label && !input.getAttribute('aria-label')) {
              warnings.push({
                rule: 'label-missing',
                element: generateSelector(input),
                message: 'Form control missing label',
                fix: 'Add <label> or aria-label'
              });
            }
          }
        }

        var score = Math.max(0, 100 - (errors.length * 10) - (warnings.length * 5));

        return {
          errors: errors,
          warnings: warnings,
          score: score,
          summary: {
            errors: errors.length,
            warnings: warnings.length
          }
        };
      } catch (e) {
        return { error: e.message };
      }
    },

    // Take screenshot and save to server
    // Usage:
    //   screenshot() - captures entire page
    //   screenshot('my-name') - captures entire page with custom name
    //   screenshot('my-name', '#selector') - captures specific element
    //   screenshot(null, '.class') - captures element with auto-generated name
    screenshot: function(name, selector) {
      return new Promise(function(resolve, reject) {
        if (typeof html2canvas === 'undefined') {
          reject(new Error('html2canvas not loaded'));
          return;
        }

        // Handle different parameter combinations
        // screenshot(selector) where selector is a string starting with . or #
        if (typeof name === 'string' && !selector && (name.startsWith('.') || name.startsWith('#') || name.startsWith('['))) {
          selector = name;
          name = null;
        }

        name = name || 'screenshot_' + Date.now();

        // Determine target element
        var targetElement = document.body;
        if (selector) {
          try {
            targetElement = document.querySelector(selector);
            if (!targetElement) {
              reject(new Error('Element not found: ' + selector));
              return;
            }
          } catch (err) {
            reject(new Error('Invalid selector: ' + selector + ' - ' + err.message));
            return;
          }
        }

        html2canvas(targetElement, {
          allowTaint: true,
          useCORS: true,
          logging: false,
          scrollY: -window.scrollY,
          scrollX: -window.scrollX,
          windowWidth: targetElement === document.body ? document.documentElement.scrollWidth : undefined,
          windowHeight: targetElement === document.body ? document.documentElement.scrollHeight : undefined
        }).then(function(canvas) {
          const dataUrl = canvas.toDataURL('image/png');
          const width = canvas.width;
          const height = canvas.height;

          send('screenshot', {
            name: name,
            data: dataUrl,
            width: width,
            height: height,
            format: 'png',
            selector: selector || 'body',
            timestamp: Date.now()
          });

          resolve({
            name: name,
            width: width,
            height: height,
            selector: selector || 'body'
          });
        }).catch(function(err) {
          reject(err);
        });
      });
    },

    // Check if connected
    isConnected: function() {
      return ws && ws.readyState === WebSocket.OPEN;
    },

    // Get connection status
    getStatus: function() {
      if (!ws) return 'not_initialized';
      switch (ws.readyState) {
        case WebSocket.CONNECTING: return 'connecting';
        case WebSocket.OPEN: return 'connected';
        case WebSocket.CLOSING: return 'closing';
        case WebSocket.CLOSED: return 'closed';
        default: return 'unknown';
      }
    }
  };

  // Initialize connection
  connect();

  console.log('[DevTool] API available at window.__devtool');
  console.log('[DevTool] Usage:');
  console.log('  __devtool.log("message", "info", {key: "value"})');
  console.log('  __devtool.screenshot("my-screenshot")');
})();
</script>
`
}

// InjectInstrumentation adds monitoring JavaScript to HTML responses.
// The wsPort parameter is deprecated and unused (kept for backward compatibility).
// The script now uses relative URLs via window.location.host.
func InjectInstrumentation(body []byte, wsPort int) []byte {
	script := instrumentationScript()

	// Try to inject before </head>
	if idx := bytes.Index(body, []byte("</head>")); idx != -1 {
		result := make([]byte, 0, len(body)+len(script))
		result = append(result, body[:idx]...)
		result = append(result, []byte(script)...)
		result = append(result, body[idx:]...)
		return result
	}

	// Try to inject after <head>
	if idx := bytes.Index(body, []byte("<head>")); idx != -1 {
		insertAt := idx + 6
		result := make([]byte, 0, len(body)+len(script))
		result = append(result, body[:insertAt]...)
		result = append(result, []byte(script)...)
		result = append(result, body[insertAt:]...)
		return result
	}

	// Try to inject after <body>
	if idx := bytes.Index(body, []byte("<body")); idx != -1 {
		// Find the end of the body tag
		endIdx := bytes.Index(body[idx:], []byte(">"))
		if endIdx != -1 {
			insertAt := idx + endIdx + 1
			result := make([]byte, 0, len(body)+len(script))
			result = append(result, body[:insertAt]...)
			result = append(result, []byte(script)...)
			result = append(result, body[insertAt:]...)
			return result
		}
	}

	// Try to inject after <html>
	if idx := bytes.Index(body, []byte("<html")); idx != -1 {
		endIdx := bytes.Index(body[idx:], []byte(">"))
		if endIdx != -1 {
			insertAt := idx + endIdx + 1
			result := make([]byte, 0, len(body)+len(script))
			result = append(result, body[:insertAt]...)
			result = append(result, []byte(script)...)
			result = append(result, body[insertAt:]...)
			return result
		}
	}

	// Last resort: prepend to body
	result := make([]byte, 0, len(body)+len(script))
	result = append(result, []byte(script)...)
	result = append(result, body...)
	return result
}

// ShouldInject determines if JavaScript should be injected based on content type.
func ShouldInject(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "text/html")
}

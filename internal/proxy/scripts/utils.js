// Utility functions for DevTool instrumentation
// Shared helpers used by multiple modules
//
// INDUSTRIAL-STRENGTH ERROR HANDLING:
// - Top-level error boundary
// - All DOM operations wrapped
// - Feature detection throughout
// - Input validation on all functions

(function() {
  'use strict';

  // Top-level error boundary
  try {
    // Feature detection
    var hasQuerySelector = typeof document.querySelector === 'function';
    var hasGetComputedStyle = typeof window.getComputedStyle === 'function';
    var hasGetBoundingClientRect = true; // Test on first use
    var ELEMENT_NODE = (typeof Node !== 'undefined' && Node.ELEMENT_NODE) || 1;

    // Error logging
    function logError(context, error) {
      try {
        console.error('[DevTool][Utils]', context, error);
      } catch (e) {
        // Logging failed - nothing we can do
      }
    }

    // Resolve selector, element, or array to element
    function resolveElement(input) {
      if (!input) return null;

      try {
        if (input instanceof HTMLElement) return input;

        if (typeof input === 'string') {
          if (!hasQuerySelector) return null;

          try {
            return document.querySelector(input);
          } catch (e) {
            logError('querySelector_failed', e);
            return null;
          }
        }
      } catch (e) {
        logError('resolveElement_failed', e);
      }

      return null;
    }

    // Generate unique CSS selector for element
    function generateSelector(element) {
      if (!element || !(element instanceof HTMLElement)) return '';

      try {
        // Try ID first
        if (element.id && typeof element.id === 'string') {
          try {
            // Validate ID is safe
            if (/^[a-zA-Z][\w-]*$/.test(element.id)) {
              return '#' + element.id;
            }
          } catch (e) {
            // ID validation failed - continue to path generation
          }
        }

        // Build path from element to root
        var path = [];
        var current = element;
        var depth = 0;
        var MAX_DEPTH = 50; // Prevent infinite loops

        while (current && current.nodeType === ELEMENT_NODE && depth < MAX_DEPTH) {
          try {
            var selector = '';

            try {
              selector = current.nodeName ? current.nodeName.toLowerCase() : 'unknown';
            } catch (e) {
              selector = 'unknown';
            }

            // Add nth-of-type if needed
            try {
              if (current.parentNode && current.parentNode.children) {
                var siblings = [];

                // Safe children iteration
                try {
                  for (var i = 0; i < current.parentNode.children.length; i++) {
                    var child = current.parentNode.children[i];
                    if (child && child.nodeName === current.nodeName) {
                      siblings.push(child);
                    }
                  }
                } catch (e) {
                  // Sibling iteration failed
                }

                if (siblings.length > 1) {
                  var index = -1;
                  for (var j = 0; j < siblings.length; j++) {
                    if (siblings[j] === current) {
                      index = j + 1;
                      break;
                    }
                  }
                  if (index > 0) {
                    selector += ':nth-of-type(' + index + ')';
                  }
                }
              }
            } catch (e) {
              // nth-of-type calculation failed - use selector without it
            }

            path.unshift(selector);

            // Move to parent
            try {
              if (current.parentNode && current.parentNode.nodeType === ELEMENT_NODE) {
                current = current.parentNode;
              } else {
                break;
              }
            } catch (e) {
              break;
            }

            depth++;
          } catch (e) {
            logError('selector_path_iteration_failed', e);
            break;
          }
        }

        return path.join(' > ');

      } catch (e) {
        logError('generateSelector_failed', e);
        return '';
      }
    }

    // Safe getComputedStyle wrapper
    function safeGetComputed(element, properties) {
      if (!element || !(element instanceof HTMLElement)) {
        return { error: 'Invalid element' };
      }

      if (!hasGetComputedStyle) {
        return { error: 'getComputedStyle not supported' };
      }

      try {
        var computed = window.getComputedStyle(element);
        if (!computed) {
          return { error: 'getComputedStyle returned null' };
        }

        var result = {};

        if (properties && Array.isArray(properties)) {
          // Get specific properties
          for (var i = 0; i < properties.length; i++) {
            try {
              var prop = properties[i];
              if (typeof prop === 'string') {
                var value = null;
                try {
                  value = computed.getPropertyValue(prop);
                } catch (e) {
                  // getPropertyValue failed, try direct access
                  try {
                    value = computed[prop];
                  } catch (e2) {
                    value = null;
                  }
                }
                result[prop] = value || '';
              }
            } catch (e) {
              // Property access failed - skip
            }
          }
        } else {
          // Get all common properties
          var commonProps = [
            'display', 'position', 'zIndex', 'opacity', 'visibility',
            'width', 'height', 'top', 'left', 'right', 'bottom',
            'margin', 'padding', 'border', 'backgroundColor', 'color'
          ];

          for (var j = 0; j < commonProps.length; j++) {
            try {
              var key = commonProps[j];
              result[key] = computed[key] || '';
            } catch (e) {
              result[key] = '';
            }
          }
        }

        return result;

      } catch (e) {
        logError('safeGetComputed_failed', e);
        return { error: e.message || 'getComputedStyle failed' };
      }
    }

    // Parse CSS value to number (strips 'px', 'em', etc)
    function parseValue(value) {
      try {
        if (typeof value === 'number') return value;
        if (typeof value !== 'string') return 0;

        var parsed = parseFloat(value);
        return isNaN(parsed) ? 0 : parsed;
      } catch (e) {
        return 0;
      }
    }

    // Get element's bounding box
    function getRect(element) {
      if (!element || !(element instanceof HTMLElement)) return null;

      try {
        if (typeof element.getBoundingClientRect !== 'function') {
          return null;
        }

        var rect = element.getBoundingClientRect();

        // Validate rect has expected properties
        if (rect && typeof rect.top === 'number' && typeof rect.left === 'number') {
          return rect;
        }

        return null;
      } catch (e) {
        logError('getRect_failed', e);
        return null;
      }
    }

    // Check if element is in viewport
    function isElementInViewport(element) {
      try {
        var rect = getRect(element);
        if (!rect) return false;

        var windowHeight = 0;
        var windowWidth = 0;

        try {
          windowHeight = window.innerHeight || document.documentElement.clientHeight || 0;
          windowWidth = window.innerWidth || document.documentElement.clientWidth || 0;
        } catch (e) {
          return false;
        }

        return (
          rect.top >= 0 &&
          rect.left >= 0 &&
          rect.bottom <= windowHeight &&
          rect.right <= windowWidth
        );

      } catch (e) {
        logError('isElementInViewport_failed', e);
        return false;
      }
    }

    // Find stacking context parent
    function getStackingContext(element) {
      if (!element || element === document.documentElement) return null;
      if (!hasGetComputedStyle) return null;

      try {
        var parent = element.parentElement;
        var depth = 0;
        var MAX_DEPTH = 50;

        while (parent && parent !== document.documentElement && depth < MAX_DEPTH) {
          try {
            var computed = window.getComputedStyle(parent);
            if (!computed) break;

            // Check conditions that create stacking context
            var createsContext = false;

            try {
              createsContext = (
                (computed.position !== 'static' && computed.zIndex !== 'auto') ||
                parseFloat(computed.opacity || '1') < 1 ||
                (computed.transform && computed.transform !== 'none') ||
                (computed.filter && computed.filter !== 'none') ||
                (computed.perspective && computed.perspective !== 'none') ||
                computed.willChange === 'transform' ||
                computed.willChange === 'opacity'
              );
            } catch (e) {
              // Condition check failed
            }

            if (createsContext) {
              return parent;
            }

            parent = parent.parentElement;
            depth++;

          } catch (e) {
            logError('stacking_context_iteration_failed', e);
            break;
          }
        }

        return document.documentElement;

      } catch (e) {
        logError('getStackingContext_failed', e);
        return null;
      }
    }

    // Check if element is part of agnt/devtool UI (should be excluded from audits/tracking)
    // Matches: #__devtool-*, .__devtool*, [id^="__devtool"], or any element inside these
    function isDevtoolElement(element) {
      if (!element) return false;

      try {
        // Check if element or any ancestor matches devtool patterns
        var current = element;
        var depth = 0;
        var MAX_DEPTH = 50;

        while (current && current.nodeType === ELEMENT_NODE && depth < MAX_DEPTH) {
          try {
            // Check ID prefix
            if (current.id && typeof current.id === 'string') {
              if (current.id.indexOf('__devtool') === 0) return true;
            }

            // Check class list for __devtool prefix
            if (current.classList) {
              for (var i = 0; i < current.classList.length; i++) {
                if (current.classList[i].indexOf('__devtool') === 0) return true;
              }
            }

            // Check for data attribute marker
            if (current.hasAttribute && current.hasAttribute('data-devtool-ui')) {
              return true;
            }
          } catch (e) {
            // Skip this element on error
          }

          current = current.parentElement;
          depth++;
        }
      } catch (e) {
        logError('isDevtoolElement_failed', e);
      }

      return false;
    }

    // Export utilities with existence check
    try {
      if (!window.__devtool_utils) {
        window.__devtool_utils = {
          resolveElement: resolveElement,
          generateSelector: generateSelector,
          safeGetComputed: safeGetComputed,
          parseValue: parseValue,
          getRect: getRect,
          isElementInViewport: isElementInViewport,
          getStackingContext: getStackingContext,
          isDevtoolElement: isDevtoolElement
        };
      }
    } catch (e) {
      console.error('[DevTool][Utils] Failed to export:', e);
    }

  } catch (e) {
    // Top-level failure
    console.error('[DevTool][Utils] Module initialization failed:', e);
  }
})();

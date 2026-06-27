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
    // isFlexOrGridItem reports whether the element's parent is a flex/grid
    // container — those children honor z-index without position:relative, a
    // case the naive "position !== static" check silently misses.
    function isFlexOrGridItem(el) {
      try {
        var p = el && el.parentElement;
        if (!p) return false;
        var d = window.getComputedStyle(p).display || '';
        return d.indexOf('flex') !== -1 || d.indexOf('grid') !== -1;
      } catch (e) {
        return false;
      }
    }

    // stackingContextTriggers returns every CSS condition on `computed` that
    // creates a NEW stacking context, each as {property, value}. Empty array
    // means the element creates no context. This is the canonical, complete
    // list (the spec's set) — reused by getStacking, findStackingContexts,
    // and getStackingChain so all three agree. The decisive datum for the
    // "z-index does nothing" bug is WHICH property created the offending
    // context, so we return the property+value, never a bare boolean.
    function stackingContextTriggers(computed, flexOrGridChild) {
      var t = [];
      if (!computed) return t;
      try {
        var pos = computed.position;
        var z = computed.zIndex;
        if ((pos === 'absolute' || pos === 'relative') && z !== 'auto') {
          t.push({ property: 'z-index', value: z + ' (position:' + pos + ')' });
        }
        if (pos === 'fixed' || pos === 'sticky') {
          t.push({ property: 'position', value: pos });
        }
        if (flexOrGridChild && z !== 'auto') {
          t.push({ property: 'z-index', value: z + ' (flex/grid child)' });
        }
        if (parseFloat(computed.opacity || '1') < 1) {
          t.push({ property: 'opacity', value: computed.opacity });
        }
        if (computed.transform && computed.transform !== 'none') {
          t.push({ property: 'transform', value: computed.transform });
        }
        if (computed.filter && computed.filter !== 'none') {
          t.push({ property: 'filter', value: computed.filter });
        }
        if (computed.backdropFilter && computed.backdropFilter !== 'none') {
          t.push({ property: 'backdrop-filter', value: computed.backdropFilter });
        }
        if (computed.perspective && computed.perspective !== 'none') {
          t.push({ property: 'perspective', value: computed.perspective });
        }
        if (computed.clipPath && computed.clipPath !== 'none') {
          t.push({ property: 'clip-path', value: computed.clipPath });
        }
        if (computed.mask && computed.mask !== 'none' && computed.mask !== '') {
          t.push({ property: 'mask', value: computed.mask });
        }
        if (computed.mixBlendMode && computed.mixBlendMode !== 'normal') {
          t.push({ property: 'mix-blend-mode', value: computed.mixBlendMode });
        }
        if (computed.isolation === 'isolate') {
          t.push({ property: 'isolation', value: 'isolate' });
        }
        var wc = computed.willChange;
        if (wc && wc !== 'auto' &&
            /transform|opacity|filter|perspective|clip-path|mask|isolation|mix-blend-mode|z-index/.test(wc)) {
          t.push({ property: 'will-change', value: wc });
        }
        var contain = computed.contain;
        if (contain && /(^|\s)(layout|paint|strict|content)(\s|$)/.test(contain)) {
          t.push({ property: 'contain', value: contain });
        }
      } catch (e) {
        // defensive — partial trigger list better than none
      }
      return t;
    }

    // containingBlockTrap returns the CSS property on `computed` that makes an
    // ancestor the containing block for position:fixed descendants (so the
    // "fixed" element scrolls/positions relative to that ancestor instead of
    // the viewport), or null. This is the invisible-in-source cause behind
    // "my fixed header scrolls away" — a distant ancestor's transform.
    function containingBlockTrap(computed) {
      if (!computed) return null;
      try {
        if (computed.transform && computed.transform !== 'none') {
          return { property: 'transform', value: computed.transform };
        }
        if (computed.perspective && computed.perspective !== 'none') {
          return { property: 'perspective', value: computed.perspective };
        }
        if (computed.filter && computed.filter !== 'none') {
          return { property: 'filter', value: computed.filter };
        }
        if (computed.backdropFilter && computed.backdropFilter !== 'none') {
          return { property: 'backdrop-filter', value: computed.backdropFilter };
        }
        var wc = computed.willChange;
        if (wc && /transform|perspective|filter/.test(wc)) {
          return { property: 'will-change', value: wc };
        }
        var contain = computed.contain;
        if (contain && /(^|\s)(layout|paint|strict|content)(\s|$)/.test(contain)) {
          return { property: 'contain', value: contain };
        }
      } catch (e) {
        // defensive
      }
      return null;
    }

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

            if (stackingContextTriggers(computed, isFlexOrGridItem(parent)).length > 0) {
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

    // getStackingChain walks ancestors and returns the full ladder of stacking
    // contexts above `element`, each {selector, triggers:[{property,value}]}.
    // The first entry is the nearest stacking root (the one z-index is
    // resolved against); the last is the viewport root. This is what lets an
    // agent see that a child's z-index:9999 is meaningless because its root is
    // a sibling-of-the-thing-it-wants-to-cover.
    function getStackingChain(element) {
      var chain = [];
      if (!element) return chain;
      try {
        var parent = element.parentElement;
        var depth = 0;
        var MAX_DEPTH = 50;
        while (parent && parent !== document.documentElement && depth < MAX_DEPTH) {
          try {
            var computed = window.getComputedStyle(parent);
            var triggers = stackingContextTriggers(computed, isFlexOrGridItem(parent));
            if (triggers.length > 0) {
              chain.push({ selector: generateSelector(parent), triggers: triggers });
            }
          } catch (e) {
            // skip this ancestor
          }
          parent = parent.parentElement;
          depth++;
        }
        chain.push({ selector: ':root', triggers: [{ property: 'root', value: 'initial containing block' }] });
      } catch (e) {
        logError('getStackingChain_failed', e);
      }
      return chain;
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
          getStackingChain: getStackingChain,
          stackingContextTriggers: stackingContextTriggers,
          containingBlockTrap: containingBlockTrap,
          isFlexOrGridItem: isFlexOrGridItem,
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

// Element inspection primitives for DevTool
// Provides detailed information about DOM elements

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  function getElementInfo(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var attrs = {};
      for (var i = 0; i < el.attributes.length; i++) {
        var attr = el.attributes[i];
        attrs[attr.name] = attr.value;
      }

      return {
        element: el,
        selector: utils.generateSelector(el),
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
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var rect = utils.getRect(el);
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
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    return utils.safeGetComputed(el, properties);
  }

  function getBox(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);

      return {
        margin: {
          top: utils.parseValue(computed.marginTop),
          right: utils.parseValue(computed.marginRight),
          bottom: utils.parseValue(computed.marginBottom),
          left: utils.parseValue(computed.marginLeft)
        },
        border: {
          top: utils.parseValue(computed.borderTopWidth),
          right: utils.parseValue(computed.borderRightWidth),
          bottom: utils.parseValue(computed.borderBottomWidth),
          left: utils.parseValue(computed.borderLeftWidth)
        },
        padding: {
          top: utils.parseValue(computed.paddingTop),
          right: utils.parseValue(computed.paddingRight),
          bottom: utils.parseValue(computed.paddingBottom),
          left: utils.parseValue(computed.paddingLeft)
        },
        content: {
          width: el.clientWidth - utils.parseValue(computed.paddingLeft) - utils.parseValue(computed.paddingRight),
          height: el.clientHeight - utils.parseValue(computed.paddingTop) - utils.parseValue(computed.paddingBottom)
        }
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getLayout(selector) {
    var el = utils.resolveElement(selector);
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
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);
      var pos = computed.position;

      var result = {
        type: computed.containerType || 'normal',
        name: computed.containerName || null,
        contain: computed.contain || 'none',
        position: pos
      };

      // For positioned elements, resolve the ACTUAL containing block and
      // surface the distant-ancestor property that established it. This is
      // the invisible-in-source cause behind "my position:fixed element
      // scrolls away / is mispositioned" — an ancestor transform/filter/
      // will-change/contain traps a fixed element so it positions relative
      // to that ancestor instead of the viewport.
      if (pos === 'fixed' || pos === 'absolute') {
        result.expectedContainingBlock = pos === 'fixed'
          ? 'viewport'
          : 'nearest positioned ancestor';

        var trap = null;
        var actual = null;
        var parent = el.parentElement;
        var depth = 0;
        while (parent && parent !== document.documentElement && depth < 50) {
          var pc = window.getComputedStyle(parent);
          // A transform/filter/etc. ancestor establishes the containing block
          // for BOTH fixed and absolute descendants.
          var t = utils.containingBlockTrap(pc);
          if (t) {
            trap = { selector: utils.generateSelector(parent), property: t.property, value: t.value };
            actual = trap.selector;
            break;
          }
          // For absolute, a positioned ancestor is the normal containing block
          // (not a "trap" — expected behavior), so stop there.
          if (pos === 'absolute' && pc.position !== 'static') {
            actual = utils.generateSelector(parent);
            break;
          }
          parent = parent.parentElement;
          depth++;
        }

        result.actualContainingBlock = actual ||
          (pos === 'fixed' ? 'viewport' : 'initial containing block');
        // trappedBy set ONLY when a fixed element is unexpectedly captured —
        // the actionable finding. null means the fixed element is correctly
        // viewport-relative.
        result.trappedBy = (pos === 'fixed') ? trap : null;
        result.escaped = (pos === 'fixed') ? (trap === null) : undefined;
      }

      return result;
    } catch (e) {
      return { error: e.message };
    }
  }

  function getStacking(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var computed = window.getComputedStyle(el);
      var selfTriggers = utils.stackingContextTriggers(computed, utils.isFlexOrGridItem(el));
      var chain = utils.getStackingChain(el);
      var root = chain.length > 0 ? chain[0] : null;

      return {
        zIndex: computed.zIndex,
        position: computed.position,
        // Does THIS element create its own stacking context, and via what?
        createsContext: selfTriggers.length > 0,
        selfTriggers: selfTriggers,
        // The nearest ancestor stacking context. z-index is only resolved
        // against siblings inside this same root — comparing z-index across
        // different roots is meaningless (the classic "z-index does nothing"
        // bug).
        stackingRoot: root ? root.selector : null,
        // WHY that root is a stacking context — the property to remove/move
        // to actually fix layering, instead of bumping z-index forever.
        rootTrigger: root ? (root.triggers[0] || null) : null,
        chain: chain,
        opacity: parseFloat(computed.opacity),
        transform: computed.transform,
        filter: computed.filter
      };
    } catch (e) {
      return { error: e.message };
    }
  }

  function getTransform(selector) {
    var el = utils.resolveElement(selector);
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
    var el = utils.resolveElement(selector);
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

  // Export inspection functions
  window.__devtool_inspection = {
    getElementInfo: getElementInfo,
    getPosition: getPosition,
    getComputed: getComputed,
    getBox: getBox,
    getLayout: getLayout,
    getContainer: getContainer,
    getStacking: getStacking,
    getTransform: getTransform,
    getOverflow: getOverflow
  };
})();

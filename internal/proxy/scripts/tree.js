// Tree walking primitives for DevTool
// Navigate DOM tree relationships
//
// Return values never include live DOM nodes — exec results are
// JSON.stringify'd and an element serializes to {}. Selectors are the
// element handles.

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // walkChildren(selector, depth?, filter?)
  // walkChildren(selector, {maxDepth?, filter?})
  // Both call forms are supported; the options-object form matches the
  // documented API, the positional form is kept for back-compat.
  function walkChildren(selector, depthOrOptions, filter) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    var depth = 1;
    if (depthOrOptions && typeof depthOrOptions === 'object') {
      depth = depthOrOptions.maxDepth || depthOrOptions.depth || 1;
      filter = depthOrOptions.filter || filter;
    } else if (typeof depthOrOptions === 'number') {
      depth = depthOrOptions;
    }
    if (filter && typeof filter !== 'function') {
      return { error: 'filter must be a function' };
    }

    var results = [];

    function walk(element, currentDepth) {
      if (currentDepth > depth) return;

      var children = Array.prototype.slice.call(element.children);
      for (var i = 0; i < children.length; i++) {
        var child = children[i];

        if (!filter || filter(child)) {
          results.push({
            selector: utils.generateSelector(child),
            tag: child.tagName.toLowerCase(),
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
  }

  function walkParents(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    var parents = [];
    var current = el.parentElement;

    while (current) {
      parents.push({
        selector: utils.generateSelector(current),
        tag: current.tagName.toLowerCase()
      });
      current = current.parentElement;
    }

    return { parents: parents, count: parents.length };
  }

  // findAncestor(selector, condition)
  // condition may be a predicate function OR a CSS selector string
  // (matched via el.matches), per the documented API.
  function findAncestor(selector, condition) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    var predicate;
    if (typeof condition === 'function') {
      predicate = condition;
    } else if (typeof condition === 'string') {
      predicate = function(node) {
        try {
          return node.matches(condition);
        } catch (e) {
          return false;
        }
      };
    } else {
      return { error: 'Condition must be a function or CSS selector string' };
    }

    var current = el.parentElement;
    while (current) {
      if (predicate(current)) {
        return {
          found: true,
          selector: utils.generateSelector(current),
          tag: current.tagName.toLowerCase()
        };
      }
      current = current.parentElement;
    }

    return { found: false };
  }

  // Export tree functions
  window.__devtool_tree = {
    walkChildren: walkChildren,
    walkParents: walkParents,
    findAncestor: findAncestor
  };
})();

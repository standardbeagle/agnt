// Interactive primitives for DevTool
// Element selection, waiting, and user prompts

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  function selectElement() {
    return new Promise(function(resolve, reject) {
      var overlay = document.createElement('div');
      overlay.style.cssText = [
        'position: fixed',
        'top: 0',
        'left: 0',
        'right: 0',
        'bottom: 0',
        'z-index: ' + (window.__devtoolTokens ? window.__devtoolTokens.z.overlay : 2147483642),
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

        var selector = utils.generateSelector(target);
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
          var selector = utils.generateSelector(target);
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
  }

  // JSON-safe description of an element (never the live node — exec results
  // are JSON.stringify'd and a DOM node serializes to {}).
  function elementResult(el, extra) {
    var result = {
      found: true,
      selector: utils.generateSelector(el),
      tag: el.tagName ? el.tagName.toLowerCase() : '',
      id: el.id || null,
      classes: Array.prototype.slice.call(el.classList || [])
    };
    if (extra) {
      for (var k in extra) {
        if (extra.hasOwnProperty(k)) result[k] = extra[k];
      }
    }
    return result;
  }

  function isElementVisible(el) {
    try {
      var computed = window.getComputedStyle(el);
      if (computed.display === 'none' || computed.visibility === 'hidden' ||
          parseFloat(computed.opacity) === 0) {
        return false;
      }
      var rect = el.getBoundingClientRect();
      return rect.width > 0 && rect.height > 0;
    } catch (e) {
      return false;
    }
  }

  // Shared wait loop: polls `check` on every mutation until it returns a
  // truthy result or the timeout fires. Single timeout path — the timer and
  // the observer are both always cleaned up on settle (no leaked timers).
  function waitFor(check, timeoutMs, timeoutMessage) {
    return new Promise(function(resolve, reject) {
      var settled = false;
      var observer = null;
      var timer = null;

      function settle(fn, value) {
        if (settled) return;
        settled = true;
        if (timer) clearTimeout(timer);
        if (observer) observer.disconnect();
        fn(value);
      }

      var initial = check();
      if (initial) {
        resolve(initial);
        return;
      }

      observer = new MutationObserver(function() {
        var result = check();
        if (result) settle(resolve, result);
      });
      observer.observe(document.documentElement || document.body, {
        childList: true,
        subtree: true,
        attributes: true
      });

      timer = setTimeout(function() {
        settle(reject, new Error(timeoutMessage));
      }, timeoutMs);
    });
  }

  function waitForElement(selector, timeout) {
    timeout = timeout || 5000;
    return waitFor(function() {
      var el = utils.resolveElement(selector);
      return el ? elementResult(el) : null;
    }, timeout, 'Timeout waiting for element: ' + selector);
  }

  function waitForRemoved(selector, timeout) {
    timeout = timeout || 5000;
    return waitFor(function() {
      return utils.resolveElement(selector) ? null : { removed: true, selector: selector };
    }, timeout, 'Timeout waiting for element to be removed: ' + selector);
  }

  function waitForVisible(selector, timeout) {
    timeout = timeout || 5000;
    return waitFor(function() {
      var el = utils.resolveElement(selector);
      return (el && isElementVisible(el)) ? elementResult(el, { visible: true }) : null;
    }, timeout, 'Timeout waiting for element to be visible: ' + selector);
  }

  // React-safe form fill: set the value through the native value setter so
  // controlled components see the change, then dispatch input + change.
  function fill(selector, value) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var tag = el.tagName ? el.tagName.toLowerCase() : '';
      if (tag !== 'input' && tag !== 'textarea' && tag !== 'select' && !el.isContentEditable) {
        return { error: 'Element is not fillable: ' + tag };
      }

      if (el.isContentEditable && tag !== 'input' && tag !== 'textarea' && tag !== 'select') {
        el.textContent = String(value);
        el.dispatchEvent(new Event('input', { bubbles: true }));
        return elementResult(el, { filled: true, value: String(value) });
      }

      if (tag === 'select') {
        el.value = String(value);
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
        return elementResult(el, { filled: true, value: el.value });
      }

      if (el.type === 'checkbox' || el.type === 'radio') {
        var checked = value === true || value === 'true' || value === 'checked';
        var proto = Object.getPrototypeOf(el);
        var checkedDesc = Object.getOwnPropertyDescriptor(proto, 'checked') ||
          Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'checked');
        if (checkedDesc && checkedDesc.set) {
          checkedDesc.set.call(el, checked);
        } else {
          el.checked = checked;
        }
        el.dispatchEvent(new Event('click', { bubbles: true }));
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
        return elementResult(el, { filled: true, checked: checked });
      }

      // Text-like input / textarea: use the native prototype setter so React's
      // value tracker (which patches the instance setter) observes the change.
      var protoForValue = tag === 'textarea' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      var desc = Object.getOwnPropertyDescriptor(protoForValue, 'value');
      if (desc && desc.set) {
        desc.set.call(el, String(value));
      } else {
        el.value = String(value);
      }
      el.dispatchEvent(new Event('input', { bubbles: true }));
      el.dispatchEvent(new Event('change', { bubbles: true }));

      return elementResult(el, { filled: true, value: el.value });
    } catch (e) {
      return { error: e.message };
    }
  }

  // Dispatch a realistic pointer/mouse event sequence at the element center.
  // Frameworks that gate on pointerdown/mousedown (menus, drag handles,
  // synthetic event systems) see the full sequence, unlike el.click().
  function clickElement(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      var rect = el.getBoundingClientRect();
      var cx = rect.left + rect.width / 2;
      var cy = rect.top + rect.height / 2;
      var eventInit = {
        bubbles: true,
        cancelable: true,
        composed: true,
        view: window,
        clientX: cx,
        clientY: cy,
        button: 0
      };

      var sequence = ['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click'];
      for (var i = 0; i < sequence.length; i++) {
        var type = sequence[i];
        var ev;
        if (type.indexOf('pointer') === 0 && typeof PointerEvent === 'function') {
          ev = new PointerEvent(type, Object.assign({ pointerId: 1, isPrimary: true, pointerType: 'mouse' }, eventInit));
        } else if (type.indexOf('pointer') === 0) {
          continue; // no PointerEvent support — mouse events suffice
        } else {
          ev = new MouseEvent(type, eventInit);
        }
        el.dispatchEvent(ev);
      }

      if (typeof el.focus === 'function') {
        try { el.focus(); } catch (e) { /* non-focusable */ }
      }

      return elementResult(el, { clicked: true, x: cx, y: cy });
    } catch (e) {
      return { error: e.message };
    }
  }

  function scrollIntoView(selector) {
    var el = utils.resolveElement(selector);
    if (!el) return { error: 'Element not found' };

    try {
      el.scrollIntoView({ block: 'center', inline: 'nearest' });
      var rect = el.getBoundingClientRect();
      return elementResult(el, {
        scrolled: true,
        rect: {
          x: rect.x, y: rect.y, width: rect.width, height: rect.height,
          top: rect.top, right: rect.right, bottom: rect.bottom, left: rect.left
        }
      });
    } catch (e) {
      return { error: e.message };
    }
  }

  function ask(question, options) {
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
        'z-index: ' + (window.__devtoolTokens ? window.__devtoolTokens.z.panel : 2147483644),
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
        'z-index: ' + (window.__devtoolTokens ? window.__devtoolTokens.z.overlay : 2147483642)
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
  }

  function measureBetween(selector1, selector2) {
    var el1 = utils.resolveElement(selector1);
    var el2 = utils.resolveElement(selector2);

    if (!el1 || !el2) return { error: 'Element not found' };

    try {
      var rect1 = utils.getRect(el1);
      var rect2 = utils.getRect(el2);

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
  }

  // Export interactive functions
  window.__devtool_interactive = {
    selectElement: selectElement,
    waitForElement: waitForElement,
    waitForRemoved: waitForRemoved,
    waitForVisible: waitForVisible,
    fill: fill,
    clickElement: clickElement,
    scrollIntoView: scrollIntoView,
    ask: ask,
    measureBetween: measureBetween
  };
})();

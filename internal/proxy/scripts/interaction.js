// User interaction tracking module for DevTool
// Tracks mouse, keyboard, and form interactions
//
// INDUSTRIAL-STRENGTH ERROR HANDLING:
// - Top-level error boundary
// - All event handlers wrapped in try-catch
// - Safe DOM operations throughout
// - Input validation on all functions
// - Graceful degradation on failures

(function() {
  'use strict';

  // Top-level error boundary
  try {
    // Dependency validation
    var utils = window.__devtool_utils;
    var core = window.__devtool_core;

    if (!utils || typeof utils.generateSelector !== 'function') {
      console.error('[DevTool][Interaction] Missing or invalid utils dependency');
      return;
    }

    if (!core || typeof core.send !== 'function') {
      console.error('[DevTool][Interaction] Missing or invalid core dependency');
      return;
    }

    // Feature detection
    var hasAddEventListener = typeof document.addEventListener === 'function';
    var hasElementFromPoint = typeof document.elementFromPoint === 'function';

    if (!hasAddEventListener) {
      console.error('[DevTool][Interaction] addEventListener not supported - tracking disabled');
      return;
    }

    // Error reporting
    function reportError(context, error) {
      try {
        if (core && typeof core.reportError === 'function') {
          core.reportError('[Interaction] ' + context, error);
        } else {
          console.error('[DevTool][Interaction]', context, error);
        }
      } catch (e) {
        console.error('[DevTool][Interaction] Error reporting failed:', e);
      }
    }

    // Configuration with safe defaults
    var config = {
      maxHistorySize: 500,
      debounceScroll: 100,
      debounceInput: 300,
      truncateText: 100,
      mouseMoveWindow: 60000,
      mouseMoveInterval: 100,
      sendBatchSize: 10,
      sendInterval: 1000
    };

    // State
    var mouseMoveBuffer = [];
    var lastInteractionTime = 0;
    var lastMouseSampleTime = 0;
    var interactionTimeBase = 0;
    var interactions = [];
    var interactionIndex = 0;
    var pendingBatch = [];
    var MAX_PENDING_BATCH = 300; // hard cap; oldest entries dropped on overflow
    var batchDropped = 0;        // total records dropped from pendingBatch
    var lastScroll = 0;
    var inputDebounce = {};
    var batchTimer = null;

    // Safe array conversion for classList
    function classListToArray(classList) {
      if (!classList) return [];

      try {
        var arr = [];
        for (var i = 0; i < Math.min(classList.length, 5); i++) {
          arr.push(classList[i]);
        }
        return arr;
      } catch (e) {
        reportError('classList_conversion_failed', e);
        return [];
      }
    }

    // Safe attribute getter
    function safeGetAttribute(element, attrName) {
      if (!element || !attrName) return null;

      try {
        if (typeof element.hasAttribute === 'function' &&
            typeof element.getAttribute === 'function') {
          if (element.hasAttribute(attrName)) {
            return element.getAttribute(attrName);
          }
        }
      } catch (e) {
        // getAttribute failed - return null
      }
      return null;
    }

    // Bounded enqueue for the send batch (same contract as mutation.js):
    // drop-oldest at MAX_PENDING_BATCH so a burst of events, or a transport
    // that drains slower than the page produces, cannot grow the queue
    // without bound.
    function queueBatchRecord(record) {
      pendingBatch.push(record);
      if (pendingBatch.length > MAX_PENDING_BATCH) {
        var overflow = pendingBatch.length - MAX_PENDING_BATCH;
        pendingBatch.splice(0, overflow);
        batchDropped += overflow;
      }
    }

    // Most text nodes boundedText will visit for one element.
    var MAX_TEXT_NODES = 200;

    // boundedText returns up to max characters of el's text by walking text
    // nodes and stopping as soon as it has enough. It never reads innerText:
    // that forces layout and serialises the whole subtree, which for a click
    // on <body> is the entire page.
    function boundedText(el, max) {
      try {
        if (!el || typeof document.createTreeWalker !== 'function') return '';
        var walker = document.createTreeWalker(el, 4 /* NodeFilter.SHOW_TEXT */, null);
        var out = '';
        var visited = 0;
        var node;
        while (out.length < max && visited < MAX_TEXT_NODES && (node = walker.nextNode())) {
          visited++;
          var parentTag = node.parentNode && node.parentNode.nodeName;
          if (parentTag === 'SCRIPT' || parentTag === 'STYLE' || parentTag === 'NOSCRIPT') continue;
          var piece = String(node.nodeValue || '').replace(/\s+/g, ' ');
          if (piece === ' ' && (out === '' || out.charAt(out.length - 1) === ' ')) continue;
          out += piece;
        }
        return out.replace(/^\s+|\s+$/g, '').substring(0, max);
      } catch (e) {
        return '';
      }
    }

    // resolveTarget maps an event target to the element worth recording.
    // Anything that is not an Element (document, window, text node) yields
    // null. An element outside the HTML namespace — the <svg>/<path> an icon
    // button hands back as event.target — resolves to its nearest HTML
    // ancestor, which is the control the user meant to press; a standalone
    // SVG document keeps the element itself.
    function resolveTarget(el) {
      try {
        if (!el || el.nodeType !== 1) return null;
        if (el instanceof HTMLElement) return { el: el, via: undefined };
        var cur = el.parentNode;
        var hops = 0;
        while (cur && hops++ < 50) {
          if (cur.nodeType === 1 && cur instanceof HTMLElement) {
            return { el: cur, via: el.tagName ? String(el.tagName).toLowerCase() : 'unknown' };
          }
          cur = cur.parentNode;
        }
        return { el: el, via: undefined };
      } catch (e) {
        return null;
      }
    }

    // Generate target info with comprehensive error handling. opts.skipText
    // leaves the text out entirely — scroll and mousemove sample many times a
    // second and their target is often the document root.
    function getTargetInfo(rawEl, opts) {
      var resolved = resolveTarget(rawEl);
      if (!resolved) return null;
      var el = resolved.el;

      try {
        var text = '';
        if (!(opts && opts.skipText)) {
          text = boundedText(el, config.truncateText);
        }

        var attrs = {};
        var relevantAttrs = ['href', 'src', 'type', 'name', 'placeholder', 'role', 'aria-label'];

        for (var i = 0; i < relevantAttrs.length; i++) {
          try {
            var value = safeGetAttribute(el, relevantAttrs[i]);
            if (value !== null) {
              attrs[relevantAttrs[i]] = value;
            }
          } catch (e) {
            // Attribute access failed - skip
          }
        }

        var selector = null;
        try {
          if (utils && typeof utils.generateSelector === 'function') {
            selector = utils.generateSelector(el);
          }
        } catch (e) {
          reportError('selector_generation_failed', e);
        }

        return {
          selector: selector,
          tag: el.tagName ? el.tagName.toLowerCase() : 'unknown',
          id: el.id || undefined,
          classes: classListToArray(el.classList),
          text: text || undefined,
          attributes: Object.keys(attrs).length > 0 ? attrs : undefined,
          // Set when the event landed on a non-HTML descendant (e.g. "svg",
          // "path") and was resolved up to this element.
          resolved_from: resolved.via
        };

      } catch (e) {
        reportError('getTargetInfo_failed', e);
        return null;
      }
    }

    // Input types that take no typed text; a keydown on them is not typing.
    var NON_TEXT_INPUT_TYPES = {
      button: 1, submit: 1, reset: 1, checkbox: 1, radio: 1, range: 1,
      color: 1, file: 1, image: 1
    };

    // keyTargetKind classifies a keydown target for keystroke privacy:
    //   'password' — a password field: nothing about the key is recorded.
    //   'editable' — any other place the user types (inputs, textareas,
    //                selects, contenteditable): typed characters are masked.
    //   'none'     — everything else: the key is a shortcut, recorded as is.
    function keyTargetKind(el) {
      try {
        if (!el || el.nodeType !== 1) return 'none';
        var tag = el.tagName ? String(el.tagName).toLowerCase() : '';
        if (tag === 'input') {
          var type = String(el.type || el.getAttribute('type') || 'text').toLowerCase();
          if (type === 'password') return 'password';
          return NON_TEXT_INPUT_TYPES[type] ? 'none' : 'editable';
        }
        if (tag === 'textarea' || tag === 'select') return 'editable';
        if (el.isContentEditable === true) return 'editable';
        // isContentEditable is layout-free but not implemented everywhere;
        // the attribute walk is the fallback.
        if (typeof el.closest === 'function' &&
            el.closest('[contenteditable=""], [contenteditable="true"], [contenteditable="plaintext-only"]')) {
          return 'editable';
        }
        return 'none';
      } catch (e) {
        // Unknown target: mask rather than risk recording typed text.
        return 'editable';
      }
    }

    // keyInfoFor builds the recorded key record. What shortcut debugging needs
    // survives — modifier combos and non-printing keys (Enter, Escape, Tab,
    // arrows). What a keylogger needs does not: a character typed into an
    // editable target is replaced by a masked marker, and a password field
    // records neither the key nor its modifiers.
    function keyInfoFor(event, target) {
      var kind = keyTargetKind(target);
      if (kind === 'password') {
        return { key: '', code: '', masked: true };
      }
      var key = event.key || '';
      var isCombo = !!(event.ctrlKey || event.metaKey || event.altKey);
      // A single code point is a typed character; named keys are longer.
      var isPrintable = key.length === 1 || (key.length === 2 && key.charCodeAt(0) >= 0xD800 && key.charCodeAt(0) <= 0xDBFF);
      if (kind === 'editable' && isPrintable && !isCombo) {
        return { key: '', code: '', masked: true };
      }
      return {
        key: key,
        code: event.code || '',
        ctrl: event.ctrlKey || undefined,
        alt: event.altKey || undefined,
        shift: event.shiftKey || undefined,
        meta: event.metaKey || undefined
      };
    }

    // Safe interaction recording. opts.skipText is passed through to
    // getTargetInfo for high-frequency events.
    function recordInteraction(eventType, event, extra, opts) {
      if (!event || typeof eventType !== 'string') return;

      try {
        var target = event.target || event.srcElement;

        // Skip agnt/devtool UI elements
        if (utils.isDevtoolElement && utils.isDevtoolElement(target)) return;

        var targetInfo = getTargetInfo(target, opts);
        if (!targetInfo) return;

        var interaction = {
          event_type: eventType,
          target: targetInfo,
          timestamp: Date.now()
        };

        // Add position for mouse events
        try {
          if (event.clientX !== undefined) {
            interaction.position = {
              client_x: event.clientX || 0,
              client_y: event.clientY || 0,
              page_x: event.pageX || 0,
              page_y: event.pageY || 0
            };
          }
        } catch (e) {
          // Position extraction failed
        }

        // Add key info for keyboard events
        try {
          if (event.key !== undefined) {
            interaction.key = keyInfoFor(event, target);
          }
        } catch (e) {
          // Key info extraction failed
        }

        // Add extra data
        if (extra && typeof extra === 'object') {
          try {
            for (var key in extra) {
              if (extra.hasOwnProperty(key)) {
                interaction[key] = extra[key];
              }
            }
          } catch (e) {
            // Extra data merge failed
          }
        }

        // Store locally (circular buffer)
        try {
          if (interactions.length < config.maxHistorySize) {
            interactions.push(interaction);
          } else {
            interactions[interactionIndex % config.maxHistorySize] = interaction;
          }
          interactionIndex++;
        } catch (e) {
          reportError('interaction_storage_failed', e);
        }

        // Queue for server
        try {
          queueBatchRecord(interaction);
        } catch (e) {
          reportError('batch_queue_failed', e);
        }

      } catch (e) {
        reportError('recordInteraction_failed', e);
      }
    }

    // Reset interaction time base
    function resetInteractionTime() {
      try {
        var now = Date.now();
        lastInteractionTime = now;
        interactionTimeBase = now;
        lastMouseSampleTime = 0;
      } catch (e) {
        reportError('resetInteractionTime_failed', e);
      }
    }

    // Safe event handlers - each wrapped in try-catch
    function handleClick(e) {
      try {
        resetInteractionTime();
        recordInteraction('click', e);
      } catch (err) {
        reportError('handleClick_failed', err);
      }
    }

    function handleDblClick(e) {
      try {
        recordInteraction('dblclick', e);
      } catch (err) {
        reportError('handleDblClick_failed', err);
      }
    }

    function handleKeyDown(e) {
      try {
        // Only track meaningful keys
        if (e && e.key && ['Control', 'Alt', 'Shift', 'Meta'].indexOf(e.key) !== -1) {
          return;
        }
        resetInteractionTime();
        recordInteraction('keydown', e);
      } catch (err) {
        reportError('handleKeyDown_failed', err);
      }
    }

    function handleInput(e) {
      try {
        if (!e || !e.target) return;

        var target = e.target;
        var key = null;

        try {
          if (utils && typeof utils.generateSelector === 'function') {
            key = utils.generateSelector(target);
          }
        } catch (err) {
          // Selector generation failed - use fallback
          key = 'input-' + Date.now();
        }

        if (!key) return;

        // Clear existing debounce
        try {
          if (inputDebounce[key]) {
            clearTimeout(inputDebounce[key]);
          }
        } catch (err) {
          // clearTimeout failed
        }

        // Set new debounce
        try {
          inputDebounce[key] = setTimeout(function() {
            try {
              var value = '';

              // Don't send password values
              try {
                if (target.type !== 'password' && target.value) {
                  value = String(target.value).substring(0, config.truncateText);
                }
              } catch (err) {
                // Value extraction failed
              }

              recordInteraction('input', e, { value: value });
            } catch (err) {
              reportError('input_debounce_callback_failed', err);
            }
          }, config.debounceInput);
        } catch (err) {
          // setTimeout failed - record immediately
          recordInteraction('input', e, { value: '' });
        }

      } catch (err) {
        reportError('handleInput_failed', err);
      }
    }

    function handleFocus(e) {
      try {
        recordInteraction('focus', e);
      } catch (err) {
        reportError('handleFocus_failed', err);
      }
    }

    function handleBlur(e) {
      try {
        recordInteraction('blur', e);
      } catch (err) {
        reportError('handleBlur_failed', err);
      }
    }

    function handleScroll(e) {
      try {
        var now = Date.now();
        if (now - lastScroll < config.debounceScroll) return;
        lastScroll = now;

        var scrollTarget = e.target;
        try {
          if (e.target === document) {
            scrollTarget = document.documentElement;
          }
        } catch (err) {
          scrollTarget = document.documentElement || e.target;
        }

        var scrollX = 0;
        var scrollY = 0;

        try {
          scrollX = window.scrollX || document.documentElement.scrollLeft || 0;
          scrollY = window.scrollY || document.documentElement.scrollTop || 0;
        } catch (err) {
          // Scroll position access failed
        }

        recordInteraction('scroll', {
          target: scrollTarget,
          clientX: undefined,
          clientY: undefined
        }, {
          scroll_position: {
            x: scrollX,
            y: scrollY
          }
        }, { skipText: true });

      } catch (err) {
        reportError('handleScroll_failed', err);
      }
    }

    function handleSubmit(e) {
      try {
        recordInteraction('submit', e);
      } catch (err) {
        reportError('handleSubmit_failed', err);
      }
    }

    function handleContextMenu(e) {
      try {
        recordInteraction('contextmenu', e);
      } catch (err) {
        reportError('handleContextMenu_failed', err);
      }
    }

    function handleMouseMove(e) {
      try {
        if (!e) return;

        var now = Date.now();

        // Only sample if within window
        if (now - lastInteractionTime > config.mouseMoveWindow) {
          return;
        }

        var interactionTime = now - interactionTimeBase;

        // Sample at interval
        if (interactionTime - lastMouseSampleTime < config.mouseMoveInterval) {
          return;
        }
        lastMouseSampleTime = interactionTime;

        var sample = {
          event_type: 'mousemove',
          position: {
            client_x: e.clientX || 0,
            client_y: e.clientY || 0,
            page_x: e.pageX || 0,
            page_y: e.pageY || 0
          },
          wall_time: now,
          interaction_time: interactionTime,
          timestamp: now
        };

        // Get element under cursor
        try {
          if (hasElementFromPoint && typeof e.clientX === 'number' && typeof e.clientY === 'number') {
            var target = document.elementFromPoint(e.clientX, e.clientY);
            if (target) {
              sample.target = getTargetInfo(target, { skipText: true });
            }
          }
        } catch (err) {
          // elementFromPoint failed
        }

        try {
          mouseMoveBuffer.push(sample);

          // Trim buffer
          var cutoff = now - config.mouseMoveWindow;
          while (mouseMoveBuffer.length > 0 && mouseMoveBuffer[0] && mouseMoveBuffer[0].wall_time < cutoff) {
            mouseMoveBuffer.shift();
          }
        } catch (err) {
          reportError('mousemove_buffer_management_failed', err);
        }

      } catch (err) {
        reportError('handleMouseMove_failed', err);
      }
    }

    // Safe listener attachment
    function attachListeners() {
      try {
        if (!hasAddEventListener) {
          console.warn('[DevTool][Interaction] addEventListener not available');
          return;
        }

        var listeners = [
          ['click', handleClick],
          ['dblclick', handleDblClick],
          ['keydown', handleKeyDown],
          ['input', handleInput],
          ['focus', handleFocus],
          ['blur', handleBlur],
          ['scroll', handleScroll],
          ['submit', handleSubmit],
          ['contextmenu', handleContextMenu],
          ['mousemove', handleMouseMove]
        ];

        for (var i = 0; i < listeners.length; i++) {
          try {
            document.addEventListener(listeners[i][0], listeners[i][1], true);
          } catch (e) {
            reportError('addEventListener_' + listeners[i][0] + '_failed', e);
          }
        }

      } catch (e) {
        reportError('attachListeners_failed', e);
      }
    }

    // Safe batch send
    function sendBatch() {
      try {
        if (pendingBatch.length === 0) return;

        var batch = pendingBatch.splice(0, config.sendBatchSize);

        if (core && typeof core.send === 'function') {
          core.send('interactions', { events: batch });
        }
      } catch (e) {
        reportError('sendBatch_failed', e);
      }
    }

    // Start batch sender
    try {
      batchTimer = setInterval(function() {
        try {
          sendBatch();
        } catch (e) {
          reportError('batch_timer_callback_failed', e);
        }
      }, config.sendInterval);
    } catch (e) {
      reportError('setInterval_failed', e);
    }

    // Initialize — attach interaction listeners only in the canonical content
    // frame. Under the always-wrap model the chrome shell and passive foreign
    // frames must not capture interactions (the user interacts with the content
    // frame); this keeps exactly one interaction-listener set per page. Role is
    // resolved by the frames.js context adapter; standalone defaults to
    // attaching so non-wrapped pages keep full tracking. See
    // docs/responsive-canonical-target.md §5.
    try {
      var __frameRole = window.__devtool_context.role;
      if (!__frameRole || __frameRole === 'content') {
        attachListeners();
      }
    } catch (e) {
      reportError('initialization_failed', e);
    }

    // The interaction store is a circular buffer: once full, new entries
    // overwrite slot (interactionIndex % maxHistorySize), so raw array order
    // is NOT chronological after wrap. orderedInteractions() rebuilds
    // oldest→newest order from interactionIndex; every read goes through it.
    function orderedInteractions() {
      if (interactions.length < config.maxHistorySize) {
        return interactions.slice();
      }
      var head = interactionIndex % config.maxHistorySize;
      return interactions.slice(head).concat(interactions.slice(0, head));
    }

    // Export interactions API with input validation
    try {
      if (!window.__devtool_interactions) {
        window.__devtool_interactions = {
          getHistory: function(count) {
            try {
              count = typeof count === 'number' ? count : 50;
              if (count < 0) count = 50;

              var ordered = orderedInteractions();
              var start = Math.max(0, ordered.length - count);
              return ordered.slice(start);
            } catch (e) {
              reportError('getHistory_failed', e);
              return [];
            }
          },

          getLastClick: function() {
            try {
              var ordered = orderedInteractions();
              for (var i = ordered.length - 1; i >= 0; i--) {
                if (ordered[i] && ordered[i].event_type === 'click') {
                  return ordered[i];
                }
              }
              return null;
            } catch (e) {
              reportError('getLastClick_failed', e);
              return null;
            }
          },

          getClicksOn: function(selector) {
            try {
              if (typeof selector !== 'string') return [];

              return orderedInteractions().filter(function(i) {
                try {
                  return i &&
                         i.event_type === 'click' &&
                         i.target &&
                         i.target.selector &&
                         i.target.selector.indexOf(selector) !== -1;
                } catch (e) {
                  return false;
                }
              });
            } catch (e) {
              reportError('getClicksOn_failed', e);
              return [];
            }
          },

          getMouseTrail: function(interactionTimestamp, windowMs) {
            try {
              windowMs = typeof windowMs === 'number' ? windowMs : 5000;
              interactionTimestamp = typeof interactionTimestamp === 'number' ? interactionTimestamp : Date.now();

              var start = interactionTimestamp - windowMs;
              var end = interactionTimestamp + windowMs;

              return mouseMoveBuffer.filter(function(m) {
                try {
                  return m && m.wall_time >= start && m.wall_time <= end;
                } catch (e) {
                  return false;
                }
              });
            } catch (e) {
              reportError('getMouseTrail_failed', e);
              return [];
            }
          },

          getMouseBuffer: function() {
            try {
              return mouseMoveBuffer.slice();
            } catch (e) {
              reportError('getMouseBuffer_failed', e);
              return [];
            }
          },

          getLastClickContext: function(trailMs) {
            try {
              var click = this.getLastClick();
              if (!click) return null;

              trailMs = typeof trailMs === 'number' ? trailMs : 2000;

              return {
                click: click,
                mouseTrail: this.getMouseTrail(click.timestamp, trailMs)
              };
            } catch (e) {
              reportError('getLastClickContext_failed', e);
              return null;
            }
          },

          clear: function() {
            try {
              interactions = [];
              interactionIndex = 0;
              mouseMoveBuffer = [];
              pendingBatch = [];
            } catch (e) {
              reportError('clear_failed', e);
            }
          },

          // Queue health: how many events are waiting for the next send and
          // how many were dropped because the queue hit its cap.
          getQueueStats: function() {
            return { pending: pendingBatch.length, dropped: batchDropped, max: MAX_PENDING_BATCH };
          },

          config: config
        };
      }
    } catch (e) {
      console.error('[DevTool][Interaction] Failed to export API:', e);
    }

  } catch (e) {
    // Top-level failure
    console.error('[DevTool][Interaction] Module initialization failed:', e);
  }
})();

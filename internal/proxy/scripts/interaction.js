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

    // Generate target info with comprehensive error handling
    function getTargetInfo(el) {
      if (!el || !(el instanceof HTMLElement)) return null;

      try {
        var text = '';
        try {
          if (el.innerText && typeof el.innerText === 'string') {
            text = el.innerText.substring(0, config.truncateText);
          }
        } catch (e) {
          // innerText access failed
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
          attributes: Object.keys(attrs).length > 0 ? attrs : undefined
        };

      } catch (e) {
        reportError('getTargetInfo_failed', e);
        return null;
      }
    }

    // Safe interaction recording
    function recordInteraction(eventType, event, extra) {
      if (!event || typeof eventType !== 'string') return;

      try {
        var target = event.target || event.srcElement;

        // Skip agnt/devtool UI elements
        if (utils.isDevtoolElement && utils.isDevtoolElement(target)) return;

        var targetInfo = getTargetInfo(target);
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
            interaction.key = {
              key: event.key || '',
              code: event.code || '',
              ctrl: event.ctrlKey || undefined,
              alt: event.altKey || undefined,
              shift: event.shiftKey || undefined,
              meta: event.metaKey || undefined
            };
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
          pendingBatch.push(interaction);
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
        });

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
              sample.target = getTargetInfo(target);
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

    // Initialize
    try {
      attachListeners();
    } catch (e) {
      reportError('initialization_failed', e);
    }

    // Export interactions API with input validation
    try {
      if (!window.__devtool_interactions) {
        window.__devtool_interactions = {
          getHistory: function(count) {
            try {
              count = typeof count === 'number' ? count : 50;
              if (count < 0) count = 50;

              var start = Math.max(0, interactions.length - count);
              return interactions.slice(start);
            } catch (e) {
              reportError('getHistory_failed', e);
              return [];
            }
          },

          getLastClick: function() {
            try {
              for (var i = interactions.length - 1; i >= 0; i--) {
                if (interactions[i] && interactions[i].event_type === 'click') {
                  return interactions[i];
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

              return interactions.filter(function(i) {
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

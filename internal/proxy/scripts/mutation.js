// DOM mutation tracking module for DevTool
// Tracks added, removed, and modified elements with visual highlighting
//
// INDUSTRIAL-STRENGTH ERROR HANDLING:
// - Top-level error boundary
// - Dependency validation
// - Safe MutationObserver with feature detection
// - All DOM operations wrapped in try-catch
// - Graceful degradation on failures

(function() {
  'use strict';

  // Top-level error boundary
  try {
    // Dependency validation
    var utils = window.__devtool_utils;
    var core = window.__devtool_core;

    if (!utils || typeof utils.generateSelector !== 'function') {
      console.error('[DevTool][Mutation] Missing or invalid utils dependency');
      return;
    }

    if (!core || typeof core.send !== 'function') {
      console.error('[DevTool][Mutation] Missing or invalid core dependency');
      return;
    }

    // Feature detection
    var hasMutationObserver = typeof MutationObserver !== 'undefined';
    var hasMap = typeof Map !== 'undefined';

    if (!hasMutationObserver) {
      console.warn('[DevTool][Mutation] MutationObserver not supported - tracking disabled');
      return;
    }

    // Error reporting
    function reportError(context, error) {
      try {
        if (core && typeof core.reportError === 'function') {
          core.reportError('[Mutation] ' + context, error);
        } else {
          console.error('[DevTool][Mutation]', context, error);
        }
      } catch (e) {
        console.error('[DevTool][Mutation] Error reporting failed:', e);
      }
    }

    // Configuration with safe defaults
    var config = {
      maxHistorySize: 200,
      highlightDuration: 2000,
      highlightAddedColor: 'rgba(0, 255, 0, 0.2)',
      highlightRemovedColor: 'rgba(255, 0, 0, 0.2)',
      highlightModifiedColor: 'rgba(255, 255, 0, 0.2)',
      trackAttributes: true,
      trackCharacterData: false,
      enableHighlighting: false,  // OFF by default - enable via API (React-friendly)
      ignoreSelectors: ['.__devtool', '#__devtool-overlays', 'script', 'style', 'link'],
      sendBatchSize: 10,
      sendInterval: 1000
    };

    var mutations = [];
    var pendingBatch = [];
    var highlightElements = hasMap ? new Map() : null;
    var highlightFallback = []; // Fallback if Map not available
    var observer = null;
    var batchTimer = null;

    // Max mutation rate tracking (starts after page load settles)
    var maxRateTracking = false;
    var maxRate = 0;
    var maxRateTimestamp = 0;
    var maxRateWindow = 1000; // Track max over 1s windows
    var rateCheckInterval = null;

    // Safe element ignore check
    function shouldIgnore(node) {
      if (!node) return true;
      if (!(node instanceof HTMLElement)) return true;

      try {
        for (var i = 0; i < config.ignoreSelectors.length; i++) {
          try {
            if (node.matches && typeof node.matches === 'function') {
              if (node.matches(config.ignoreSelectors[i])) return true;
            }
            if (node.closest && typeof node.closest === 'function') {
              if (node.closest(config.ignoreSelectors[i])) return true;
            }
          } catch (e) {
            // Invalid selector - skip
          }
        }
      } catch (e) {
        reportError('shouldIgnore_failed', e);
      }

      return false;
    }

    // Safe highlight with full error handling
    function highlightMutation(element, color) {
      // Check if highlighting is enabled
      if (!config.enableHighlighting) return;

      if (!element || !(element instanceof HTMLElement)) return;
      if (shouldIgnore(element)) return;
      if (typeof color !== 'string') return;

      try {
        if (!element.style) return;

        var originalBg = element.style.backgroundColor || '';
        var originalOutline = element.style.outline || '';
        var originalTransition = element.style.transition || '';

        // Apply highlight styles
        try {
          element.style.transition = 'background-color 0.3s, outline 0.3s';
          element.style.backgroundColor = color;

          try {
            var outlineColor = color.replace('0.2', '0.8');
            element.style.outline = '2px solid ' + outlineColor;
          } catch (e) {
            // Color replace failed - use original
            element.style.outline = '2px solid ' + color;
          }
        } catch (e) {
          reportError('highlight_style_apply_failed', e);
          return;
        }

        // Store for cleanup
        var id = 'mutation-' + Date.now() + '-' + Math.random();
        var highlightInfo = {
          element: element,
          originalBg: originalBg,
          originalOutline: originalOutline,
          originalTransition: originalTransition
        };

        if (highlightElements) {
          highlightElements.set(id, highlightInfo);
        } else {
          highlightFallback.push({ id: id, info: highlightInfo });
        }

        // Schedule cleanup
        setTimeout(function() {
          try {
            var info = null;

            if (highlightElements) {
              info = highlightElements.get(id);
              highlightElements.delete(id);
            } else {
              for (var i = 0; i < highlightFallback.length; i++) {
                if (highlightFallback[i].id === id) {
                  info = highlightFallback[i].info;
                  highlightFallback.splice(i, 1);
                  break;
                }
              }
            }

            if (info && info.element && info.element.style) {
              try {
                info.element.style.backgroundColor = info.originalBg;
                info.element.style.outline = info.originalOutline;

                setTimeout(function() {
                  try {
                    if (info.element && info.element.style) {
                      info.element.style.transition = info.originalTransition;
                    }
                  } catch (e) {
                    // Element may be removed - ignore
                  }
                }, 300);
              } catch (e) {
                // Style restoration failed - ignore
              }
            }
          } catch (e) {
            reportError('highlight_cleanup_failed', e);
          }
        }, config.highlightDuration);

      } catch (e) {
        reportError('highlightMutation_failed', e);
      }
    }

    // Safe selector generation
    function safeGenerateSelector(element) {
      try {
        if (!element || typeof utils.generateSelector !== 'function') {
          return null;
        }
        return utils.generateSelector(element);
      } catch (e) {
        reportError('selector_generation_failed', e);
        return null;
      }
    }

    // Safe NodeList to Array conversion
    function nodeListToArray(nodeList) {
      if (!nodeList) return [];

      try {
        if (Array.isArray(nodeList)) return nodeList;

        var arr = [];
        for (var i = 0; i < nodeList.length; i++) {
          arr.push(nodeList[i]);
        }
        return arr;
      } catch (e) {
        reportError('nodeList_conversion_failed', e);
        return [];
      }
    }

    // Find triggering interaction for a mutation
    // Returns the most recent interaction within the correlation window (500ms)
    function findTriggeringInteraction(timestamp) {
      try {
        // Check if interactions module is available
        if (!window.__devtool_interactions || typeof window.__devtool_interactions.getHistory !== 'function') {
          return null;
        }

        var correlationWindow = 500; // 500ms window
        var windowStart = timestamp - correlationWindow;

        // Get recent interactions
        var interactions = window.__devtool_interactions.getHistory(20);
        if (!interactions || interactions.length === 0) return null;

        // Find most recent interaction before this mutation
        for (var i = interactions.length - 1; i >= 0; i--) {
          var interaction = interactions[i];
          if (interaction && interaction.timestamp <= timestamp && interaction.timestamp >= windowStart) {
            return {
              type: interaction.event_type,
              timestamp: interaction.timestamp,
              latency: timestamp - interaction.timestamp,
              target: interaction.target ? interaction.target.selector : null
            };
          }
        }

        return null;
      } catch (e) {
        // Silently ignore - correlation is optional
        return null;
      }
    }

    // Mutation observer callback with comprehensive error handling
    function handleMutations(mutationsList) {
      if (!mutationsList || mutationsList.length === 0) return;

      try {
        for (var i = 0; i < mutationsList.length; i++) {
          try {
            var mutation = mutationsList[i];
            if (!mutation) continue;

            // Handle added/removed nodes
            if (mutation.type === 'childList') {
              try {
                // Added nodes
                var addedNodes = nodeListToArray(mutation.addedNodes);
                for (var j = 0; j < addedNodes.length; j++) {
                  try {
                    var node = addedNodes[j];
                    if (shouldIgnore(node)) continue;

                    var now = Date.now();
                    var triggeredBy = findTriggeringInteraction(now);

                    var record = {
                      mutation_type: 'added',
                      target: {
                        selector: safeGenerateSelector(mutation.target),
                        tag: mutation.target.tagName ? mutation.target.tagName.toLowerCase() : 'unknown',
                        id: mutation.target.id || undefined
                      },
                      added: [{
                        selector: node.nodeType === 1 ? safeGenerateSelector(node) : null,
                        tag: node.nodeName ? node.nodeName.toLowerCase() : 'unknown',
                        id: node.id || undefined,
                        html: node.outerHTML ? node.outerHTML.substring(0, 500) : undefined
                      }],
                      timestamp: now,
                      triggered_by: triggeredBy
                    };

                    mutations.push(record);
                    pendingBatch.push(record);

                    if (node instanceof HTMLElement) {
                      highlightMutation(node, config.highlightAddedColor);
                    }
                  } catch (e) {
                    reportError('added_node_processing_failed', e);
                  }
                }

                // Removed nodes
                var removedNodes = nodeListToArray(mutation.removedNodes);
                for (var k = 0; k < removedNodes.length; k++) {
                  try {
                    var rnode = removedNodes[k];
                    if (shouldIgnore(rnode)) continue;

                    var rNow = Date.now();
                    var rTriggeredBy = findTriggeringInteraction(rNow);

                    var rrecord = {
                      mutation_type: 'removed',
                      target: {
                        selector: safeGenerateSelector(mutation.target),
                        tag: mutation.target.tagName ? mutation.target.tagName.toLowerCase() : 'unknown',
                        id: mutation.target.id || undefined
                      },
                      removed: [{
                        tag: rnode.nodeName ? rnode.nodeName.toLowerCase() : 'unknown',
                        id: rnode.id || undefined,
                        html: rnode.outerHTML ? rnode.outerHTML.substring(0, 200) : undefined
                      }],
                      timestamp: rNow,
                      triggered_by: rTriggeredBy
                    };

                    mutations.push(rrecord);
                    pendingBatch.push(rrecord);
                  } catch (e) {
                    reportError('removed_node_processing_failed', e);
                  }
                }
              } catch (e) {
                reportError('childList_mutation_failed', e);
              }
            }

            // Handle attribute changes
            if (mutation.type === 'attributes' && config.trackAttributes) {
              try {
                var target = mutation.target;
                if (shouldIgnore(target)) continue;

                var attrValue = null;
                try {
                  if (target && typeof target.getAttribute === 'function' && mutation.attributeName) {
                    attrValue = target.getAttribute(mutation.attributeName);
                  }
                } catch (e) {
                  // getAttribute failed - use null
                }

                var aNow = Date.now();
                var aTriggeredBy = findTriggeringInteraction(aNow);

                var arecord = {
                  mutation_type: 'attributes',
                  target: {
                    selector: safeGenerateSelector(target),
                    tag: target.tagName ? target.tagName.toLowerCase() : 'unknown',
                    id: target.id || undefined
                  },
                  attribute: {
                    name: mutation.attributeName || 'unknown',
                    old_value: mutation.oldValue || null,
                    new_value: attrValue
                  },
                  timestamp: aNow,
                  triggered_by: aTriggeredBy
                };

                mutations.push(arecord);
                pendingBatch.push(arecord);
                highlightMutation(target, config.highlightModifiedColor);
              } catch (e) {
                reportError('attribute_mutation_failed', e);
              }
            }
          } catch (e) {
            reportError('mutation_processing_failed', e);
          }
        }

        // Trim history
        try {
          if (mutations.length > config.maxHistorySize) {
            mutations = mutations.slice(-config.maxHistorySize);
          }
        } catch (e) {
          reportError('history_trim_failed', e);
        }

      } catch (e) {
        reportError('handleMutations_failed', e);
      }
    }

    // Safe batch send
    function sendBatch() {
      try {
        if (pendingBatch.length === 0) return;

        var batch = pendingBatch.splice(0, config.sendBatchSize);

        if (core && typeof core.send === 'function') {
          core.send('mutations', { events: batch });
        }
      } catch (e) {
        reportError('sendBatch_failed', e);
      }
    }

    // Safe observer start
    function startObserver() {
      try {
        if (observer) return;

        if (!hasMutationObserver) {
          console.warn('[DevTool][Mutation] MutationObserver not available');
          return;
        }

        if (!document.body) {
          console.warn('[DevTool][Mutation] document.body not available');
          return;
        }

        // Create observer with wrapped callback
        try {
          observer = new MutationObserver(function(mutations) {
            try {
              handleMutations(mutations);
            } catch (e) {
              reportError('observer_callback_failed', e);
            }
          });
        } catch (e) {
          reportError('observer_creation_failed', e);
          return;
        }

        // Configure options with validation
        var options = {
          childList: true,
          subtree: true,
          attributes: config.trackAttributes,
          attributeOldValue: config.trackAttributes, // Fixed: match attributes setting
          characterData: config.trackCharacterData,
          characterDataOldValue: config.trackCharacterData // Fixed: match characterData setting
        };

        // Start observing with validation
        try {
          observer.observe(document.body, options);
        } catch (e) {
          reportError('observer_observe_failed', e);
          observer = null;
        }

      } catch (e) {
        reportError('startObserver_failed', e);
      }
    }

    // Safe observer stop
    function stopObserver() {
      try {
        if (observer && typeof observer.disconnect === 'function') {
          observer.disconnect();
          observer = null;
        }
      } catch (e) {
        reportError('stopObserver_failed', e);
      }
    }

    // Start batch sender with error handling
    try {
      batchTimer = setInterval(function() {
        try {
          sendBatch();
        } catch (e) {
          reportError('batch_timer_failed', e);
        }
      }, config.sendInterval);
    } catch (e) {
      reportError('setInterval_failed', e);
    }

    // Start max rate tracking (checks current rate and updates max)
    function startMaxRateTracking() {
      try {
        maxRateTracking = true;

        rateCheckInterval = setInterval(function() {
          try {
            if (!maxRateTracking) return;

            var now = Date.now();
            var windowStart = now - maxRateWindow;
            var count = 0;

            // Count mutations in last window (from end for efficiency)
            for (var i = mutations.length - 1; i >= 0; i--) {
              if (mutations[i].timestamp < windowStart) break;
              count++;
            }

            var rate = count / (maxRateWindow / 1000); // mutations per second

            if (rate > maxRate) {
              maxRate = rate;
              maxRateTimestamp = now;
            }
          } catch (e) {
            reportError('rate_check_failed', e);
          }
        }, 1000); // Check every second
      } catch (e) {
        reportError('startMaxRateTracking_failed', e);
      }
    }

    // Initialize when DOM is ready
    try {
      if (document.body) {
        startObserver();

        // Start max rate tracking after 3 seconds (let initial render settle)
        setTimeout(function() {
          try {
            startMaxRateTracking();
          } catch (e) {
            reportError('delayed_max_tracking_start_failed', e);
          }
        }, 3000);
      } else {
        if (typeof document.addEventListener === 'function') {
          document.addEventListener('DOMContentLoaded', function() {
            try {
              startObserver();

              // Start max rate tracking after 3 seconds
              setTimeout(function() {
                try {
                  startMaxRateTracking();
                } catch (e) {
                  reportError('delayed_max_tracking_start_failed', e);
                }
              }, 3000);
            } catch (e) {
              reportError('DOMContentLoaded_handler_failed', e);
            }
          });
        }
      }
    } catch (e) {
      reportError('initialization_failed', e);
    }

    // Export mutations API with input validation
    try {
      if (!window.__devtool_mutations) {
        window.__devtool_mutations = {
          getHistory: function(count) {
            try {
              count = typeof count === 'number' ? count : 50;
              if (count < 0) count = 50;

              var start = Math.max(0, mutations.length - count);
              return mutations.slice(start);
            } catch (e) {
              reportError('getHistory_failed', e);
              return [];
            }
          },

          getAdded: function(since) {
            try {
              since = typeof since === 'number' ? since : 0;

              return mutations.filter(function(m) {
                try {
                  return m && m.mutation_type === 'added' && m.timestamp > since;
                } catch (e) {
                  return false;
                }
              });
            } catch (e) {
              reportError('getAdded_failed', e);
              return [];
            }
          },

          getRemoved: function(since) {
            try {
              since = typeof since === 'number' ? since : 0;

              return mutations.filter(function(m) {
                try {
                  return m && m.mutation_type === 'removed' && m.timestamp > since;
                } catch (e) {
                  return false;
                }
              });
            } catch (e) {
              reportError('getRemoved_failed', e);
              return [];
            }
          },

          getModified: function(since) {
            try {
              since = typeof since === 'number' ? since : 0;

              return mutations.filter(function(m) {
                try {
                  return m && m.mutation_type === 'attributes' && m.timestamp > since;
                } catch (e) {
                  return false;
                }
              });
            } catch (e) {
              reportError('getModified_failed', e);
              return [];
            }
          },

          highlightRecent: function(duration) {
            try {
              duration = typeof duration === 'number' ? duration : 5000;
              if (duration < 0) duration = 5000;

              var since = Date.now() - duration;

              for (var i = 0; i < mutations.length; i++) {
                try {
                  var m = mutations[i];
                  if (m && m.timestamp > since && m.mutation_type === 'added' && m.added) {
                    for (var j = 0; j < m.added.length; j++) {
                      try {
                        var node = m.added[j];
                        if (node && node.selector) {
                          var el = document.querySelector(node.selector);
                          if (el) {
                            highlightMutation(el, config.highlightAddedColor);
                          }
                        }
                      } catch (e) {
                        // querySelector failed or highlight failed - skip
                      }
                    }
                  }
                } catch (e) {
                  // Mutation processing failed - skip
                }
              }
            } catch (e) {
              reportError('highlightRecent_failed', e);
            }
          },

          clear: function() {
            try {
              mutations = [];
              pendingBatch = [];
            } catch (e) {
              reportError('clear_failed', e);
            }
          },

          pause: function() {
            try {
              stopObserver();
            } catch (e) {
              reportError('pause_failed', e);
            }
          },

          resume: function() {
            try {
              startObserver();
            } catch (e) {
              reportError('resume_failed', e);
            }
          },

          enableHighlighting: function() {
            try {
              config.enableHighlighting = true;
            } catch (e) {
              reportError('enableHighlighting_failed', e);
            }
          },

          disableHighlighting: function() {
            try {
              config.enableHighlighting = false;
            } catch (e) {
              reportError('disableHighlighting_failed', e);
            }
          },

          getRateStats: function(windows) {
            try {
              // Default windows: 1s, 5s, 30s
              windows = windows || [1000, 5000, 30000];

              var now = Date.now();
              var rates = {};
              var counts = {};

              // Count mutations in each window
              for (var i = 0; i < windows.length; i++) {
                var windowMs = windows[i];
                var windowStart = now - windowMs;
                var count = 0;

                // Count from end (most recent) for efficiency
                for (var j = mutations.length - 1; j >= 0; j--) {
                  if (mutations[j].timestamp < windowStart) break;
                  count++;
                }

                var label = windowMs < 1000 ? windowMs + 'ms' : (windowMs / 1000) + 's';
                counts[label] = count;
                rates[label] = count / (windowMs / 1000); // mutations per second
              }

              // Calculate acceleration ratios
              var acceleration = {};
              var labels = Object.keys(rates);

              for (var k = 0; k < labels.length - 1; k++) {
                var shortLabel = labels[k];
                var longLabel = labels[k + 1];
                var ratio = rates[shortLabel] / (rates[longLabel] || 0.001); // avoid divide by zero
                acceleration[shortLabel + '/' + longLabel] = Math.round(ratio * 100) / 100;
              }

              // Determine status
              var primaryRatio = acceleration[labels[0] + '/' + labels[1]] || 1;
              var status = primaryRatio > 1.5 ? 'accelerating' :
                           primaryRatio < 0.7 ? 'decelerating' : 'steady';

              // Health check
              var currentRate = rates[labels[0]] || 0;
              var health = currentRate > 50 ? 'critical' :
                           currentRate > 20 ? 'warning' : 'ok';

              return {
                windows: rates,
                counts: counts,
                acceleration: acceleration,
                status: status,
                health: health,
                max: {
                  rate: Math.round(maxRate * 100) / 100,
                  timestamp: maxRateTimestamp,
                  ago: maxRateTimestamp ? Math.round((now - maxRateTimestamp) / 1000) + 's' : 'n/a'
                }
              };

            } catch (e) {
              reportError('getRateStats_failed', e);
              return { error: e.message || 'getRateStats failed' };
            }
          },

          // Get mutations triggered by specific interaction type
          getTriggeredBy: function(interactionType) {
            try {
              if (typeof interactionType !== 'string') {
                return { error: 'Invalid interaction type' };
              }

              var results = [];
              for (var i = 0; i < mutations.length; i++) {
                try {
                  var m = mutations[i];
                  if (m && m.triggered_by && m.triggered_by.type === interactionType) {
                    results.push(m);
                  }
                } catch (e) {
                  // Skip invalid mutation
                }
              }

              return results;
            } catch (e) {
              reportError('getTriggeredBy_failed', e);
              return { error: e.message || 'getTriggeredBy failed' };
            }
          },

          // Get mutations with no triggering interaction (spontaneous updates)
          getUntriggered: function() {
            try {
              var results = [];
              for (var i = 0; i < mutations.length; i++) {
                try {
                  var m = mutations[i];
                  if (m && !m.triggered_by) {
                    results.push(m);
                  }
                } catch (e) {
                  // Skip invalid mutation
                }
              }

              return results;
            } catch (e) {
              reportError('getUntriggered_failed', e);
              return { error: e.message || 'getUntriggered failed' };
            }
          },

          // Get correlation statistics
          getCorrelationStats: function() {
            try {
              var stats = {
                total: mutations.length,
                triggered: 0,
                untriggered: 0,
                by_type: {},
                avg_latency: {},
                max_latency: {}
              };

              var latencySum = {};
              var latencyCount = {};
              var latencyMax = {};

              for (var i = 0; i < mutations.length; i++) {
                try {
                  var m = mutations[i];
                  if (!m) continue;

                  if (m.triggered_by) {
                    stats.triggered++;
                    var type = m.triggered_by.type;

                    // Count by type
                    stats.by_type[type] = (stats.by_type[type] || 0) + 1;

                    // Track latency
                    if (typeof m.triggered_by.latency === 'number') {
                      latencySum[type] = (latencySum[type] || 0) + m.triggered_by.latency;
                      latencyCount[type] = (latencyCount[type] || 0) + 1;
                      latencyMax[type] = Math.max(latencyMax[type] || 0, m.triggered_by.latency);
                    }
                  } else {
                    stats.untriggered++;
                  }
                } catch (e) {
                  // Skip invalid mutation
                }
              }

              // Calculate averages
              for (var type in latencySum) {
                if (latencyCount[type] > 0) {
                  stats.avg_latency[type] = Math.round(latencySum[type] / latencyCount[type]);
                  stats.max_latency[type] = latencyMax[type];
                }
              }

              return stats;
            } catch (e) {
              reportError('getCorrelationStats_failed', e);
              return { error: e.message || 'getCorrelationStats failed' };
            }
          },

          config: config
        };
      }
    } catch (e) {
      console.error('[DevTool][Mutation] Failed to export API:', e);
    }

  } catch (e) {
    // Top-level failure - log and abort
    console.error('[DevTool][Mutation] Module initialization failed:', e);
  }
})();

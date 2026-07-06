// Floating Indicator — VanJS tab components (Overview/Errors/Network/
// Performance/Interactions/History/Chaos + attachment chips).
// Split from indicator.js; shares symbols with the other indicator-*
// modules via the window.__devtool_indicator_internal namespace.
// Chrome-shell functions (I.showMicroToast, I.showAttachmentPreview, ...)
// are assigned by indicator.js and only invoked at runtime, after every
// indicator-* module has evaluated.

(function() {
  'use strict';

  var I = window.__devtool_indicator_internal;

  // Shared symbols from indicator-styles.js / indicator-data.js (load earlier).
  var TOKENS = I.TOKENS;
  var STYLES = I.STYLES;
  var ICONS = I.ICONS;
  var IND_MOTION = I.IND_MOTION;
  var tags = I.tags;
  var state = I.state;
  var store = I.store;
  var actions = I.actions;
  var chaosRequest = I.chaosRequest;
  var dvErrors = I.dvErrors;
  var dvApi = I.dvApi;
  var dvMutations = I.dvMutations;
  var dvInteractions = I.dvInteractions;

  function formatTimeAgo(timestamp) {
    var seconds = Math.floor((Date.now() - timestamp) / 1000);
    if (seconds < 60) return seconds + 's ago';
    var minutes = Math.floor(seconds / 60);
    if (minutes < 60) return minutes + 'm ago';
    var hours = Math.floor(minutes / 60);
    return hours + 'h ago';
  }

  function truncate(str, maxLen) {
    if (str.length <= maxLen) return str;
    return str.substring(0, maxLen - 3) + '...';
  }

  // ============================================
  // VanJS Components
  // ============================================

  // Chip component - renders a single attachment chip
  function ChipComponent(attachment) {
    var iconSvg = ICONS.element;
    if (attachment.type === 'screenshot') iconSvg = ICONS.screenshot;
    else if (attachment.type === 'sketch') iconSvg = ICONS.sketch;
    else if (attachment.type === 'audit') iconSvg = ICONS.audit;
    else if (attachment.type === 'style-edit') iconSvg = ICONS.styleEdit;

    var chip = tags.div({
      style: STYLES.chip + '; cursor: pointer;',
      'data-id': attachment.id
    },
      tags.span({style: STYLES.chipIcon}),
      tags.span({style: STYLES.chipLabel}, attachment.label),
      tags.button({
        style: STYLES.chipRemove,
        onclick: function(e) {
          e.stopPropagation();
          actions.removeAttachment(attachment.id);
          I.hideAttachmentPreview();
        },
        onmouseenter: function(e) { e.target.style.color = TOKENS.colors.error; },
        onmouseleave: function(e) { e.target.style.color = TOKENS.colors.textMuted; }
      })
    );

    // Set innerHTML for SVG icons (VanJS doesn't parse HTML strings)
    chip.children[0].innerHTML = iconSvg;
    chip.children[2].innerHTML = ICONS.close;

    // Hover preview handlers
    chip.onmouseenter = function() {
      var rect = chip.getBoundingClientRect();
      I.showAttachmentPreview(attachment, rect);
    };
    chip.onmouseleave = function() {
      I.hideAttachmentPreview();
    };

    return chip;
  }

  // AttachmentArea component - reactive list of chips
  // This re-renders automatically when store.attachments changes
  function AttachmentAreaComponent() {
    return function() {
      var list = store.attachments.val;
      if (list.length === 0) {
        return tags.div({style: STYLES.attachmentArea + '; display: none;', id: '__devtool-attachments'});
      }

      return tags.div({
        style: STYLES.attachmentArea + '; display: flex;',
        id: '__devtool-attachments'
      }, list.map(function(att) { return ChipComponent(att); }));
    };
  }

  // ============================================
  // VanJS Tab Components
  // ============================================

  // Helper for status colors
  function getStatusColor(value, warningThreshold, criticalThreshold, invert) {
    if (invert) {
      // Lower is better (e.g., errors, failed calls)
      if (value === 0) return TOKENS.colors.success;
      return value > criticalThreshold ? TOKENS.colors.error : TOKENS.colors.active;
    }
    // Higher is worse (e.g., DOM rate)
    if (value > criticalThreshold) return TOKENS.colors.error;
    if (value > warningThreshold) return TOKENS.colors.active;
    return TOKENS.colors.success;
  }

  // Overview Tab Component
  function OverviewTabComponent() {
    return function() {
      var data = store.overview.val;
      var isReact = data.framework && data.framework.name === 'React';

      var children = [];

      // Version badge
      children.push(tags.div({
        style: 'text-align: right; font-size: 11px; color: ' + TOKENS.colors.textMuted + '; margin-bottom: ' + TOKENS.spacing.sm + '; font-family: ui-monospace, monospace;',
        title: 'Press Ctrl+Y to toggle this panel'
      }, 'agnt v' + data.version + (data.proxyId ? ' \u00b7 proxy: ' + data.proxyId : '')));

      // Framework badge
      if (data.framework) {
        var versionText = data.framework.version && data.framework.version !== 'unknown' ? ' v' + data.framework.version : '';
        children.push(tags.div({style: STYLES.healthCard},
          tags.div({style: STYLES.healthLabel}, 'Framework'),
          tags.div({style: STYLES.healthValue + '; font-size: 16px;'}, data.framework.name + versionText)
        ));
      }

      // Health cards grid
      var errorColor = data.errorCount > 0 ? TOKENS.colors.error : TOKENS.colors.success;
      var apiColor = data.failedApiCount > 0 ? TOKENS.colors.error : TOKENS.colors.success;
      var domStatus = data.domRate > 50 ? 'Critical' : (data.domRate > 20 ? 'Warning' : 'OK');
      var domColor = getStatusColor(data.domRate, 20, 50, false);
      var perfColor = data.avgApiTime > 2000 ? TOKENS.colors.active : TOKENS.colors.success;

      var gridChildren = [
        tags.div({style: STYLES.healthCard},
          tags.div({style: STYLES.healthLabel}, 'Errors'),
          tags.div({style: STYLES.healthValue + '; color: ' + errorColor + ';'}, String(data.errorCount))
        ),
        tags.div({style: STYLES.healthCard},
          tags.div({style: STYLES.healthLabel}, 'Failed API'),
          tags.div({style: STYLES.healthValue + '; color: ' + apiColor + ';'}, String(data.failedApiCount))
        ),
        tags.div({style: STYLES.healthCard},
          tags.div({style: STYLES.healthLabel}, 'DOM Updates'),
          tags.div({style: STYLES.healthValue + '; color: ' + domColor + ';'}, domStatus)
        ),
        tags.div({style: STYLES.healthCard},
          tags.div({style: STYLES.healthLabel}, 'Avg API Time'),
          tags.div({style: STYLES.healthValue + '; font-size: 16px; color: ' + perfColor + ';'}, data.avgApiTime + 'ms')
        )
      ];

      // React-specific metrics
      if (isReact) {
        var rerenderStatus = data.rerenderRate > 5 ? 'High' : (data.rerenderRate > 2 ? 'Moderate' : 'Low');
        var rerenderColor = getStatusColor(data.rerenderRate, 2, 5, false);
        gridChildren.push(tags.div({style: STYLES.healthCard},
          tags.div({style: STYLES.healthLabel}, 'React Rerenders'),
          tags.div({style: STYLES.healthValue + '; color: ' + rerenderColor + ';'}, data.rerenderRate + '/s')
        ));

        if (data.inputLag > 0) {
          var lagStatus = data.inputLag > 100 ? 'Slow' : (data.inputLag > 50 ? 'OK' : 'Fast');
          var lagColor = getStatusColor(data.inputLag, 50, 100, false);
          gridChildren.push(tags.div({style: STYLES.healthCard},
            tags.div({style: STYLES.healthLabel}, 'Input Lag'),
            tags.div({style: STYLES.healthValue + '; color: ' + lagColor + ';'}, data.inputLag + 'ms')
          ));
        }
      }

      children.push(tags.div({style: 'display: grid; grid-template-columns: 1fr 1fr; gap: ' + TOKENS.spacing.sm + ';'}, gridChildren));

      return tags.div({}, children);
    };
  }

  // Errors Tab Component
  function ErrorsTabComponent() {
    return function() {
      var errors = store.errors.val;

      if (!dvErrors()) {
        return tags.div({style: STYLES.emptyState}, 'Error tracking not available');
      }

      if (errors.length === 0) {
        return tags.div({style: STYLES.emptyState}, '\u2713 No errors detected');
      }

      return tags.div({}, errors.map(function(error) {
        var timeAgo = formatTimeAgo(error.lastSeen);
        var countPrefix = error.count > 1 ? '\u00d7' + error.count + ' ' : '';

        var summary = tags.div({style: STYLES.errorMessage + '; cursor: pointer;'},
          countPrefix + error.message.substring(0, 100)
        );
        var meta = tags.div({style: STYLES.errorMeta},
          error.source + (error.lineno ? ':' + error.lineno : '') +
          (error.colno ? ':' + error.colno : '') + ' \u2022 ' + timeAgo
        );

        // Detail panel with full error + stack trace
        var detail = document.createElement('div');
        detail.style.cssText = 'display: none; margin-top: 6px; padding: 8px; background: ' + TOKENS.colors.surfaceAlt + '; border-radius: 4px; font-size: 11px; font-family: monospace; white-space: pre-wrap; word-break: break-all; max-height: 150px; overflow-y: auto;';

        var fullText = error.message + '\n';
        if (error.source) fullText += 'source: ' + error.source + (error.lineno ? ':' + error.lineno : '') + (error.colno ? ':' + error.colno : '') + '\n';
        if (error.stack) fullText += '\n' + error.stack;
        if (error.count > 1) fullText += '\noccurrences: ' + error.count;
        detail.textContent = fullText;

        var btnBar = document.createElement('div');
        btnBar.style.cssText = 'margin-top: 6px; display: flex; gap: 6px;';

        var copyBtn = document.createElement('button');
        copyBtn.style.cssText = 'padding: 2px 8px; font-size: 10px; background: ' + TOKENS.colors.surface + '; border: 1px solid ' + TOKENS.colors.border + '; border-radius: 3px; color: ' + TOKENS.colors.textMuted + '; cursor: pointer;';
        copyBtn.textContent = 'copy';
        copyBtn.onclick = function(e) {
          e.stopPropagation();
          navigator.clipboard.writeText(fullText).then(function() {
            copyBtn.textContent = 'copied';
            setTimeout(function() { copyBtn.textContent = 'copy'; }, 1500);
          });
        };

        var sendBtn = document.createElement('button');
        sendBtn.style.cssText = 'padding: 2px 8px; font-size: 10px; background: ' + TOKENS.colors.active + '; border: none; border-radius: 3px; color: white; cursor: pointer;';
        sendBtn.textContent = '\u2709 send to agent';
        sendBtn.onclick = function(e) {
          e.stopPropagation();
          actions.sendToAgent('error', error.message.substring(0, 80), fullText);
        };

        btnBar.appendChild(copyBtn);
        btnBar.appendChild(sendBtn);
        detail.appendChild(btnBar);

        var item = tags.div({style: STYLES.errorItem});
        item.appendChild(summary);
        item.appendChild(meta);
        item.appendChild(detail);

        var errorKey = (error.message || '') + '|' + (error.source || '') + '|' + (error.lineno || 0) + '|' + (error.colno || 0);
        var expanded = !!state.expandedErrorKeys[errorKey];
        detail.style.display = expanded ? 'block' : 'none';

        item.onclick = function() {
          expanded = !expanded;
          state.expandedErrorKeys[errorKey] = expanded;
          detail.style.display = expanded ? 'block' : 'none';
        };
        item.onmouseenter = function() { item.style.background = detail.style.display === 'block' ? 'transparent' : TOKENS.colors.surfaceAlt; };
        item.onmouseleave = function() { item.style.background = 'transparent'; };

        return item;
      }));
    };
  }

  // Network Tab Component
  function NetworkTabComponent() {
    return function() {
      var calls = store.network.val;

      if (!dvApi()) {
        return tags.div({style: STYLES.emptyState}, 'Network tracking not available');
      }

      if (calls.length === 0) {
        return tags.div({style: STYLES.emptyState}, 'No API calls tracked');
      }

      return tags.div({}, calls.map(function(call) {
        var statusColor = call.ok ? TOKENS.colors.success : TOKENS.colors.error;
        var timeAgo = formatTimeAgo(call.timestamp);
        var isError = !call.ok;

        // Summary row
        var summary = tags.div({style: STYLES.errorMessage + '; cursor: pointer;'},
          tags.span({style: 'color: ' + statusColor + '; font-weight: 600;'}, String(call.status || 0)),
          ' ' + call.method + ' ' + truncate(call.url, 40)
        );

        var meta = tags.div({style: STYLES.errorMeta},
          (call.duration || 0) + 'ms \u2022 ' + timeAgo +
          (call.statusText && isError ? ' \u2022 ' + call.statusText : '')
        );

        // Detail panel (hidden by default)
        var detail = document.createElement('div');
        detail.style.cssText = 'display: none; margin-top: 6px; padding: 8px; background: ' + TOKENS.colors.surfaceAlt + '; border-radius: 4px; font-size: 11px; font-family: monospace; white-space: pre-wrap; word-break: break-all; max-height: 120px; overflow-y: auto;';

        var detailText = call.method + ' ' + call.url + '\n';
        detailText += 'status: ' + (call.status || 0) + (call.statusText ? ' ' + call.statusText : '') + '\n';
        detailText += 'duration: ' + (call.duration || 0) + 'ms\n';
        if (call.errorMessage) detailText += 'error: ' + call.errorMessage + '\n';
        if (call.responseBody) detailText += '\n' + call.responseBody;
        if (call.error) detailText += '\nerror: ' + call.error;
        detail.textContent = detailText;

        var btnBar = document.createElement('div');
        btnBar.style.cssText = 'margin-top: 6px; display: flex; gap: 6px;';

        var copyBtn = document.createElement('button');
        copyBtn.style.cssText = 'padding: 2px 8px; font-size: 10px; background: ' + TOKENS.colors.surface + '; border: 1px solid ' + TOKENS.colors.border + '; border-radius: 3px; color: ' + TOKENS.colors.textMuted + '; cursor: pointer;';
        copyBtn.textContent = 'copy';
        copyBtn.onclick = function(e) {
          e.stopPropagation();
          navigator.clipboard.writeText(detailText).then(function() {
            copyBtn.textContent = 'copied';
            setTimeout(function() { copyBtn.textContent = 'copy'; }, 1500);
          });
        };

        // Send to agent — only for error responses
        if (isError) {
          var sendBtn = document.createElement('button');
          sendBtn.style.cssText = 'padding: 2px 8px; font-size: 10px; background: ' + TOKENS.colors.active + '; border: none; border-radius: 3px; color: white; cursor: pointer;';
          sendBtn.textContent = '\u2709 send to agent';
          sendBtn.onclick = function(e) {
            e.stopPropagation();
            var summary = call.status + ' ' + call.method + ' ' + call.url.substring(0, 60);
            actions.sendToAgent('network', summary, detailText);
          };
          btnBar.appendChild(sendBtn);
        }

        btnBar.appendChild(copyBtn);
        detail.appendChild(btnBar);

        var item = tags.div({style: STYLES.errorItem});
        item.appendChild(summary);
        item.appendChild(meta);
        item.appendChild(detail);

        var networkKey = (call.method || '') + '|' + (call.url || '') + '|' + (call.timestamp || 0);
        var expanded = !!state.expandedNetworkKeys[networkKey];
        detail.style.display = expanded ? 'block' : 'none';

        item.onclick = function() {
          expanded = !expanded;
          state.expandedNetworkKeys[networkKey] = expanded;
          detail.style.display = expanded ? 'block' : 'none';
        };
        item.onmouseenter = function() { item.style.background = detail.style.display === 'block' ? 'transparent' : TOKENS.colors.surfaceAlt; };
        item.onmouseleave = function() { item.style.background = 'transparent'; };

        return item;
      }));
    };
  }

  // Performance Tab Component
  function PerformanceTabComponent() {
    return function() {
      var data = store.performance.val;

      if (!dvMutations()) {
        return tags.div({style: STYLES.emptyState}, 'Performance tracking not available');
      }

      var children = [];

      // Mutation rate stats
      var gridChildren = [];
      [1000, 5000, 30000].forEach(function(windowMs) {
        if (data.rateStats[windowMs]) {
          var rate = data.rateStats[windowMs].rate;
          var status = rate > 50 ? 'Critical' : (rate > 20 ? 'Warning' : 'OK');
          var color = getStatusColor(rate, 20, 50, false);
          gridChildren.push(tags.div({style: STYLES.healthCard},
            tags.div({style: STYLES.healthLabel}, 'Mutations (' + (windowMs / 1000) + 's window)'),
            tags.div({style: STYLES.healthValue + '; color: ' + color + ';'},
              rate.toFixed(1) + '/s ',
              tags.span({style: 'font-size: 12px; color: ' + TOKENS.colors.textMuted + ';'}, '\u2022 ' + status)
            )
          ));
        }
      });

      children.push(tags.div({style: 'display: flex; flex-direction: column; gap: ' + TOKENS.spacing.sm + ';'}, gridChildren));

      // React-specific metrics
      if (data.isReact) {
        children.push(tags.div({
          style: 'margin-top: ' + TOKENS.spacing.lg + '; padding-bottom: ' + TOKENS.spacing.sm + '; border-bottom: 1px solid ' + TOKENS.colors.border + '; font-size: 11px; font-weight: 600; color: ' + TOKENS.colors.textMuted + '; text-transform: uppercase; letter-spacing: 0.5px;'
        }, 'React Performance'));

        var reactGridChildren = [];

        // Rerender Rate
        var rerenderStatus = data.rerenderRate > 5 ? 'High' : (data.rerenderRate > 2 ? 'Moderate' : 'Low');
        var rerenderColor = getStatusColor(data.rerenderRate, 2, 5, false);
        reactGridChildren.push(tags.div({style: STYLES.healthCard},
          tags.div({style: STYLES.healthLabel}, 'Rerender Rate (30s)'),
          tags.div({style: STYLES.healthValue + '; color: ' + rerenderColor + ';'},
            data.rerenderRate + '/s ',
            tags.span({style: 'font-size: 12px; color: ' + TOKENS.colors.textMuted + ';'}, '\u2022 ' + rerenderStatus)
          ),
          tags.div({style: 'font-size: 11px; color: ' + TOKENS.colors.textMuted + '; margin-top: 4px;'}, 'Spontaneous updates: ' + data.rerenderCount)
        ));

        // Input Lag
        if (data.inputLag > 0 || data.inputCount > 0) {
          var lagStatus = data.inputLag > 100 ? 'Slow' : (data.inputLag > 50 ? 'OK' : 'Fast');
          var lagColor = getStatusColor(data.inputLag, 50, 100, false);
          reactGridChildren.push(tags.div({style: STYLES.healthCard},
            tags.div({style: STYLES.healthLabel}, 'Input Lag'),
            tags.div({style: STYLES.healthValue + '; color: ' + lagColor + ';'},
              data.inputLag + 'ms ',
              tags.span({style: 'font-size: 12px; color: ' + TOKENS.colors.textMuted + ';'}, '\u2022 ' + lagStatus)
            ),
            tags.div({style: 'font-size: 11px; color: ' + TOKENS.colors.textMuted + '; margin-top: 4px;'}, 'Max: ' + data.maxInputLag + 'ms \u2022 Samples: ' + data.inputCount)
          ));
        }

        // Hotspots
        if (data.hotspots.length > 0) {
          var hotspotChildren = [tags.div({style: STYLES.healthLabel}, 'Rerender Hotspots')];
          data.hotspots.forEach(function(hotspot) {
            hotspotChildren.push(tags.div({style: 'font-size: 11px; margin-top: 6px; padding: 4px 6px; background: ' + TOKENS.colors.surfaceAlt + '; border-radius: 4px;'},
              tags.div({style: 'font-weight: 500; color: ' + TOKENS.colors.text + ';'}, truncate(hotspot.selector, 35)),
              tags.div({style: 'color: ' + TOKENS.colors.textMuted + '; margin-top: 2px;'}, '\u00d7' + hotspot.count + ' rerenders')
            ));
          });
          reactGridChildren.push(tags.div({style: STYLES.healthCard}, hotspotChildren));
        }

        children.push(tags.div({style: 'display: flex; flex-direction: column; gap: ' + TOKENS.spacing.sm + '; margin-top: ' + TOKENS.spacing.sm + ';'}, reactGridChildren));
      }

      return tags.div({}, children);
    };
  }


  // Helper to format interaction target
  function formatInteractionTarget(target) {
    if (!target) return 'unknown';
    if (typeof target === 'string') return target;
    if (target.selector) return target.selector;
    if (target.tag) {
      var str = target.tag;
      if (target.id) str += '#' + target.id;
      return str;
    }
    return 'element';
  }

  // Interactions Tab Component
  function InteractionsTabComponent() {
    return function() {
      var history = store.interactions.val;

      if (!dvInteractions()) {
        return tags.div({style: STYLES.emptyState}, 'Interaction tracking not available');
      }

      if (history.length === 0) {
        return tags.div({style: STYLES.emptyState}, 'No interactions tracked');
      }

      return tags.div({}, history.map(function(interaction) {
        var eventType = interaction.event_type || interaction.type || 'unknown';
        var targetStr = formatInteractionTarget(interaction.target);
        var timeAgo = formatTimeAgo(interaction.timestamp);

        return tags.div({style: STYLES.errorItem},
          tags.div({style: STYLES.errorMessage}, eventType + ' on ' + targetStr),
          tags.div({style: STYLES.errorMeta}, timeAgo)
        );
      }));
    };
  }

  // History Tab Component
  function HistoryTabComponent() {
    var HISTORY_COLORS = {
      tool: '#6366f1',
      message: '#3b82f6',
      error: '#ef4444',
      network: '#8b5cf6',
      screenshot: '#22c55e',
      system: '#94a3b8'
    };
    var HISTORY_ICONS = {
      tool: '\u2699',
      message: '\u2709',
      error: '\u2718',
      network: '\u21c4',
      screenshot: '\ud83d\udcf7',
      system: '\u25cb'
    };

    return function() {
      var events = store.history.val;

      if (events.length === 0) {
        return tags.div({style: STYLES.emptyState}, 'No events recorded yet');
      }

      var clearBtn = tags.button({
        style: 'border: none; background: none; font-size: 11px; color: ' + TOKENS.colors.textMuted + '; cursor: pointer; padding: 2px 6px; margin-bottom: 8px; float: right;',
        onclick: function() { store.history.val = []; }
      }, 'Clear');

      var list = tags.div({style: 'clear: both;'},
        events.map(function(evt) {
          var color = HISTORY_COLORS[evt.type] || HISTORY_COLORS.system;
          var icon = HISTORY_ICONS[evt.type] || HISTORY_ICONS.system;
          var timeAgo = formatTimeAgo(evt.timestamp);

          var item = tags.div({style: STYLES.historyItem},
            tags.div({style: STYLES.historyDot + '; background: ' + color + ';'}),
            tags.div({style: STYLES.historyBody},
              tags.div({style: STYLES.historyText}, icon + ' ' + evt.text),
              evt.detail ? tags.div({style: STYLES.historyMeta}, truncate(evt.detail, 60)) : ''
            ),
            tags.div({style: STYLES.historyTime}, timeAgo)
          );

          item.onmouseenter = function() { item.style.background = TOKENS.colors.surfaceAlt; };
          item.onmouseleave = function() { item.style.background = 'transparent'; };

          return item;
        })
      );

      return tags.div({}, clearBtn, list);
    };
  }

  // Chaos Tab Component
  function ChaosTabComponent() {
    var chaosTypeIcons = {
      latency: '⏱', bandwidth: '🐌', packet_loss: '📉',
      disconnect: '🔌', slow_close: '🚶', slow_drip: '💧',
      timeout: '⏳', stale: '🥶', out_of_order: '🔀',
      http_error: '💥', rate_limit: '🛑', bit_flip: '🎲',
      truncate: '✂', corrupt_json: '🦠', chunked_abort: '⛔',
      partial_body: '🪦', header_bomb: '💣'
    };

    function ruleDetail(rule) {
      var parts = [];
      if (rule.probability && rule.probability < 1) parts.push(Math.round(rule.probability * 100) + '% odds');
      if (rule.min_latency_ms || rule.max_latency_ms) parts.push((rule.min_latency_ms || 0) + '–' + (rule.max_latency_ms || 0) + 'ms');
      if (rule.error_codes && rule.error_codes.length) parts.push('HTTP ' + rule.error_codes.join('/'));
      if (rule.bytes_per_ms) parts.push(rule.bytes_per_ms + 'B/ms');
      if (rule.truncate_percent) parts.push('keep ' + Math.round(rule.truncate_percent * 100) + '%');
      if (rule.url_pattern) parts.push(rule.url_pattern);
      return parts.join(' · ');
    }

    return function() {
      var chaos = store.chaos.val;
      var presets = store.chaosPresets.val;

      if (!chaos.loaded) {
        return tags.div({style: STYLES.emptyState}, 'Connecting to chaos engine…');
      }

      var children = [];

      // Master toggle card
      var statusColor = chaos.enabled ? TOKENS.colors.chaos : TOKENS.colors.textMuted;
      var toggleTrack = tags.div({
        style: [
          'width: 44px', 'height: 24px', 'border-radius: 12px', 'position: relative',
          'cursor: pointer', 'transition: ' + IND_MOTION.transition('background 0.25s ease'), 'flex-shrink: 0',
          'background: ' + (chaos.enabled ? TOKENS.colors.chaos : TOKENS.colors.border)
        ].join(';'),
        onclick: function() { chaosRequest(chaos.enabled ? 'disable' : 'enable', {}); }
      }, tags.div({
        style: [
          'position: absolute', 'top: 2px', 'width: 20px', 'height: 20px',
          'border-radius: 50%', 'background: #fff', 'transition: ' + IND_MOTION.transition('left 0.25s ease'),
          'box-shadow: 0 1px 3px rgba(0,0,0,0.3)',
          'left: ' + (chaos.enabled ? '22px' : '2px')
        ].join(';')
      }));

      children.push(tags.div({
        style: STYLES.healthCard + '; display: flex; align-items: center; gap: ' + TOKENS.spacing.md + ';' +
          (chaos.enabled ? ' border: 1px solid ' + TOKENS.colors.chaos + '; box-shadow: 0 0 12px rgba(168,85,247,0.25);' : '')
      },
        tags.div({style: 'font-size: 22px; flex-shrink: 0;'}, chaos.enabled ? '⛈' : '☀'),
        tags.div({style: 'flex: 1; min-width: 0;'},
          tags.div({style: 'font-weight: 600; font-size: 14px; color: ' + TOKENS.colors.text + ';'}, 'Chaos Engineering'),
          tags.div({style: 'font-size: 11px; color: ' + statusColor + ';'},
            chaos.enabled
              ? 'Injecting failures into proxied traffic'
              : 'Off — traffic passes through untouched')
        ),
        toggleTrack
      ));

      // Presets
      children.push(tags.div({style: STYLES.healthLabel + '; margin-top: ' + TOKENS.spacing.md + ';'}, 'Presets'));
      if (presets.length === 0) {
        children.push(tags.div({style: 'font-size: 12px; color: ' + TOKENS.colors.textMuted + ';'}, 'Loading presets…'));
      } else {
        children.push(tags.div({style: 'display: flex; flex-wrap: wrap; gap: ' + TOKENS.spacing.xs + ';'},
          presets.map(function(preset) {
            return tags.button({
              style: [
                'padding: 4px 10px', 'border-radius: ' + TOKENS.radius.full,
                'border: 1px solid ' + TOKENS.colors.border, 'background: ' + TOKENS.colors.surfaceAlt,
                'color: ' + TOKENS.colors.text, 'font-size: 11px', 'cursor: pointer',
                'transition: ' + IND_MOTION.transition('all 0.15s ease')
              ].join(';'),
              title: 'Apply preset: ' + (preset.rules || []).join(', '),
              onclick: function() {
                chaosRequest('preset', {name: preset.name}, function(result, err) {
                  if (err) I.showMicroToast('✘ ' + err, TOKENS.colors.error);
                  else I.showMicroToast('⛈ ' + preset.name, TOKENS.colors.chaos);
                });
              },
              onmouseenter: function(e) {
                e.target.style.borderColor = TOKENS.colors.chaos;
                e.target.style.color = TOKENS.colors.chaos;
              },
              onmouseleave: function(e) {
                e.target.style.borderColor = TOKENS.colors.border;
                e.target.style.color = TOKENS.colors.text;
              }
            }, preset.name);
          })
        ));
      }

      // Active rules
      children.push(tags.div({
        style: 'display: flex; align-items: center; justify-content: space-between; margin-top: ' + TOKENS.spacing.md + ';'
      },
        tags.div({style: STYLES.healthLabel}, 'Rules (' + chaos.rules.length + ')'),
        chaos.rules.length > 0 ? tags.button({
          style: 'background: none; border: none; color: ' + TOKENS.colors.error + '; font-size: 11px; cursor: pointer;',
          onclick: function() { chaosRequest('clear', {}); }
        }, 'Clear all') : null
      ));

      if (chaos.rules.length === 0) {
        children.push(tags.div({style: 'font-size: 12px; color: ' + TOKENS.colors.textMuted + '; padding: ' + TOKENS.spacing.sm + ' 0;'},
          'No rules configured. Pick a preset above to get started.'));
      } else {
        children.push(tags.div({style: 'display: flex; flex-direction: column; gap: ' + TOKENS.spacing.xs + ';'},
          chaos.rules.map(function(rule) {
            var icon = chaosTypeIcons[rule.type] || '⚙';
            var detail = ruleDetail(rule);
            return tags.div({
              style: STYLES.healthCard + '; display: flex; align-items: center; gap: ' + TOKENS.spacing.sm + ';' +
                (rule.enabled ? '' : ' opacity: 0.5;')
            },
              tags.span({style: 'font-size: 16px; flex-shrink: 0;'}, icon),
              tags.div({style: 'flex: 1; min-width: 0;'},
                tags.div({style: 'font-size: 12px; font-weight: 600; color: ' + TOKENS.colors.text + '; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;'},
                  rule.name || rule.id),
                tags.div({style: 'font-size: 10px; color: ' + TOKENS.colors.textMuted + '; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;'},
                  rule.type + (detail ? ' · ' + detail : '') + (rule.applied ? ' · hit ' + rule.applied + '×' : ''))
              ),
              tags.input({
                type: 'checkbox',
                checked: rule.enabled,
                title: rule.enabled ? 'Disable rule' : 'Enable rule',
                style: 'cursor: pointer; accent-color: ' + TOKENS.colors.chaos + ';',
                onchange: function(e) {
                  chaosRequest('toggle_rule', {rule_id: rule.id, enabled: e.target.checked});
                }
              }),
              tags.button({
                style: 'background: none; border: none; color: ' + TOKENS.colors.textMuted + '; cursor: pointer; font-size: 14px; padding: 2px;',
                title: 'Remove rule',
                onclick: function() { chaosRequest('remove_rule', {rule_id: rule.id}); }
              }, '×')
            );
          })
        ));
      }

      // Swallowed-error detection toggle. When on, an injected error fault
      // that produces no app-side error within the window is raised as an
      // agent incident — surfacing apps that silently eat failures.
      children.push(tags.label({
        style: 'display: flex; align-items: center; gap: ' + TOKENS.spacing.sm + '; margin-top: ' + TOKENS.spacing.md + '; cursor: pointer;',
        title: 'Raise an incident when the app swallows an injected fault (no error shown)'
      },
        tags.div({style: 'flex: 1; min-width: 0;'},
          tags.div({style: 'font-size: 12px; font-weight: 600; color: ' + TOKENS.colors.text + ';'}, 'Detect swallowed errors'),
          tags.div({style: 'font-size: 10px; color: ' + TOKENS.colors.textMuted + ';'}, 'Flag faults the app silently eats')
        ),
        tags.input({
          type: 'checkbox',
          checked: chaos.swallowDetect === true,
          style: 'cursor: pointer; accent-color: ' + TOKENS.colors.chaos + ';',
          onchange: function(e) {
            chaosRequest('set_swallow_detect', {enabled: e.target.checked});
          }
        })
      ));

      // Stats
      if (chaos.stats) {
        var s = chaos.stats;
        children.push(tags.div({
          style: 'display: flex; align-items: center; justify-content: space-between; margin-top: ' + TOKENS.spacing.md + ';'
        },
          tags.div({style: STYLES.healthLabel}, 'Impact'),
          tags.button({
            style: 'background: none; border: none; color: ' + TOKENS.colors.textMuted + '; font-size: 11px; cursor: pointer;',
            onclick: function() { chaosRequest('reset_stats', {}); }
          }, 'Reset')
        ));
        var affectedPct = s.total_requests > 0 ? Math.round((s.affected_count / s.total_requests) * 100) : 0;
        var statCells = [
          ['Requests', String(s.total_requests || 0), TOKENS.colors.text],
          ['Affected', (s.affected_count || 0) + ' (' + affectedPct + '%)', s.affected_count > 0 ? TOKENS.colors.chaos : TOKENS.colors.text],
          ['Errors injected', String(s.errors_injected || 0), s.errors_injected > 0 ? TOKENS.colors.error : TOKENS.colors.text],
          ['Latency added', (s.latency_injected_ms || 0) + 'ms', TOKENS.colors.text],
          ['Drops', String(s.drops_injected || 0), TOKENS.colors.text],
          ['Truncated', String(s.truncated_count || 0), TOKENS.colors.text]
        ];
        children.push(tags.div({style: 'display: grid; grid-template-columns: 1fr 1fr 1fr; gap: ' + TOKENS.spacing.xs + ';'},
          statCells.map(function(cell) {
            return tags.div({style: STYLES.healthCard + '; padding: 6px 8px;'},
              tags.div({style: 'font-size: 9px; text-transform: uppercase; letter-spacing: 0.5px; color: ' + TOKENS.colors.textMuted + ';'}, cell[0]),
              tags.div({style: 'font-size: 13px; font-weight: 600; color: ' + cell[2] + ';'}, cell[1])
            );
          })
        ));
      }

      return tags.div({}, children);
    };
  }


  // Shared with indicator.js (tab bar + panel construction).
  I.AttachmentAreaComponent = AttachmentAreaComponent;
  I.OverviewTabComponent = OverviewTabComponent;
  I.ErrorsTabComponent = ErrorsTabComponent;
  I.NetworkTabComponent = NetworkTabComponent;
  I.PerformanceTabComponent = PerformanceTabComponent;
  I.InteractionsTabComponent = InteractionsTabComponent;
  I.HistoryTabComponent = HistoryTabComponent;
  I.ChaosTabComponent = ChaosTabComponent;
})();

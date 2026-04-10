// Floating Indicator for DevTool
// Redesigned with visual hierarchy and Gestalt principles
// Attachments are logged first, then referenced in messages
// Uses VanJS for reactive state management

(function() {
  'use strict';

  // VanJS 1.6.0 minified (~1.5KB) - Reactive UI framework
  // https://vanjs.org - MIT License
  // prettier-ignore
  {let e,t,r,o,n,s,l,i,f,h,w,a,u,d,c,_,S,g,y,b,m,v,j,x,O;l=Object.getPrototypeOf,f={},h=l(i={isConnected:1}),w=l(l),a=(e,t,r,o)=>(e??(o?setTimeout(r,o):queueMicrotask(r),new Set)).add(t),u=(e,t,o)=>{let n=r;r=t;try{return e(o)}catch(e){return console.error(e),o}finally{r=n}},d=e=>e.filter(e=>e.t?.isConnected),c=e=>n=a(n,e,()=>{for(let e of n)e.o=d(e.o),e.l=d(e.l);n=s},1e3),_={get val(){return r?.i?.add(this),this.rawVal},get oldVal(){return r?.i?.add(this),this.h},set val(o){r?.u?.add(this),o!==this.rawVal&&(this.rawVal=o,this.o.length+this.l.length?(t?.add(this),e=a(e,this,x)):this.h=o)}},S=e=>({__proto__:_,rawVal:e,h:e,o:[],l:[]}),g=(e,t)=>{let r={i:new Set,u:new Set},n={f:e},s=o;o=[];let l=u(e,r,t);l=(l??document).nodeType?l:new Text(l);for(let e of r.i)r.u.has(e)||(c(e),e.o.push(n));for(let e of o)e.t=l;return o=s,n.t=l},y=(e,t=S(),r)=>{let n={i:new Set,u:new Set},s={f:e,s:t};s.t=r??o?.push(s)??i,t.val=u(e,n,t.rawVal);for(let e of n.i)n.u.has(e)||(c(e),e.l.push(s));return t},b=(e,...t)=>{for(let r of t.flat(1/0)){let t=l(r??0),o=t===_?g(()=>r.val):t===w?g(r):r;o!=s&&e.append(o)}return e},m=(e,t,...r)=>{let[{is:o,...n},...i]=l(r[0]??0)===h?r:[{},...r],a=e?document.createElementNS(e,t,{is:o}):document.createElement(t,{is:o});for(let[e,r]of Object.entries(n)){let o=t=>t?Object.getOwnPropertyDescriptor(t,e)??o(l(t)):s,n=t+","+e,i=f[n]??=o(l(a))?.set??0,h=e.startsWith("on")?(t,r)=>{let o=e.slice(2);a.removeEventListener(o,r),a.addEventListener(o,t)}:i?i.bind(a):a.setAttribute.bind(a,e),u=l(r??0);e.startsWith("on")||u===w&&(r=y(r),u=_),u===_?g(()=>(h(r.val,r.h),a)):h(r)}return b(a,i)},v=e=>({get:(t,r)=>m.bind(s,e,r)}),j=(e,t)=>t?t!==e&&e.replaceWith(t):e.remove(),x=()=>{let r=0,o=[...e].filter(e=>e.rawVal!==e.h);do{t=new Set;for(let e of new Set(o.flatMap(e=>e.l=d(e.l))))y(e.f,e.s,e.t),e.t=s}while(++r<100&&(o=[...t]).length);let n=[...e].filter(e=>e.rawVal!==e.h);e=s;for(let e of new Set(n.flatMap(e=>e.o=d(e.o))))j(e.t,g(e.f,e.t)),e.t=s;for(let e of n)e.h=e.rawVal},O={tags:new Proxy(e=>new Proxy(m,v(e)),v()),hydrate:(e,t)=>j(e,g(t,e)),add:b,state:S,derive:y},window.van=O;}

  var van = window.van;
  var tags = van.tags;

  var core = window.__devtool_core;
  var utils = window.__devtool_utils;

  // Generate unique IDs for attachments
  function generateId() {
    return 'ctx_' + Date.now().toString(36) + Math.random().toString(36).substr(2, 5);
  }

  // State
  var state = {
    container: null,
    bug: null,
    panel: null,
    outputPreview: null, // Floating output preview element
    sparkline: null, // API activity sparkline element
    sparklineInterval: null, // Update interval for sparkline
    isExpanded: false,
    isDragging: false,
    dragOffset: { x: 0, y: 0 },
    position: { x: 20, y: 20 },
    isVisible: true,
    isActive: false, // AI tool activity state
    activityTimeout: null,
    outputPreviewTimeout: null, // Auto-hide timeout for output preview
    requestNotification: true, // Always request notification when task completes
    // Attachments are now logged items with references
    attachments: [], // { id, type, label, summary, timestamp }
    // Tab management
    activeTab: 'compose', // compose|overview|errors|network|performance|interactions
    tabUpdateInterval: null, // Update interval for active tab
    lastAuditResults: null, // Cache audit results
    inspectBtn: null, // Inspect toolbar button (for active state tracking)
    microToast: null, // Micro toast element (compact pill near bug)
    microToastTimeout: null // Auto-hide timer for micro toast
  };

  // ============================================
  // Reactive Store (VanJS)
  // Fully reactive state management for all tabs
  // ============================================
  var store = {
    // Compose tab
    attachments: van.state([]),
    message: van.state(''),

    // Overview tab data
    overview: van.state({
      framework: null,
      errorCount: 0,
      failedApiCount: 0,
      domRate: 0,
      avgApiTime: 0,
      rerenderRate: 0,
      inputLag: 0,
      version: 'unknown'
    }),

    // Errors tab data
    errors: van.state([]),

    // Network tab data
    network: van.state([]),

    // Performance tab data
    performance: van.state({
      rateStats: {},
      isReact: false,
      rerenderRate: 0,
      rerenderCount: 0,
      inputLag: 0,
      maxInputLag: 0,
      inputCount: 0,
      hotspots: []
    }),

    // Interactions tab data
    interactions: van.state([]),

    // History tab data (chronological event log)
    history: van.state([])
  };

  // ============================================
  // Data Refresh Functions
  // These update the reactive store, UI follows automatically
  // ============================================
  function refreshOverviewData() {
    var framework = window.__devtool_framework ? window.__devtool_framework.detect() : null;
    var errorStats = window.__devtool_errors ? window.__devtool_errors.getStats() : null;
    var apiStats = window.__devtool_api ? window.__devtool_api.getStats() : null;
    var mutationStats = window.__devtool_mutations ? window.__devtool_mutations.getRateStats([5000]) : null;
    var isReact = framework && framework.name === 'React';

    var data = {
      framework: framework,
      errorCount: errorStats ? errorStats.totalCount : 0,
      failedApiCount: apiStats ? apiStats.failed : 0,
      domRate: mutationStats && mutationStats[5000] ? mutationStats[5000].rate : 0,
      avgApiTime: apiStats ? apiStats.avgDuration : 0,
      rerenderRate: 0,
      inputLag: 0,
      version: window.__devtool_version || 'unknown',
      proxyId: window.__devtool_proxy_id || ''
    };

    // React-specific metrics
    if (isReact && window.__devtool_mutations) {
      try {
        var untriggered = window.__devtool_mutations.getUntriggered ? window.__devtool_mutations.getUntriggered() : [];
        var recentUntriggered = untriggered.filter(function(m) {
          return m.timestamp && (Date.now() - m.timestamp) < 30000;
        });
        data.rerenderRate = parseFloat((recentUntriggered.length / 30).toFixed(1));
      } catch (e) { /* ignore */ }

      try {
        var correlationStats = window.__devtool_mutations.getCorrelationStats ? window.__devtool_mutations.getCorrelationStats() : null;
        if (correlationStats && correlationStats.avg_latency) {
          data.inputLag = correlationStats.avg_latency.input || 0;
        }
      } catch (e) { /* ignore */ }
    }

    store.overview.val = data;
  }

  function refreshErrorsData() {
    if (!window.__devtool_errors) {
      store.errors.val = [];
      return;
    }
    var deduplicated = window.__devtool_errors.getDeduplicatedErrors();
    var allErrors = [].concat(deduplicated.jsErrors || [], deduplicated.consoleErrors || [], deduplicated.consoleWarnings || []);
    store.errors.val = allErrors;
  }

  function refreshNetworkData() {
    if (!window.__devtool_api) {
      store.network.val = [];
      return;
    }
    var calls = window.__devtool_api.getCalls();
    store.network.val = calls.slice(-20).reverse();
  }

  function refreshPerformanceData() {
    if (!window.__devtool_mutations) {
      store.performance.val = { rateStats: {}, isReact: false, rerenderRate: 0, rerenderCount: 0, inputLag: 0, maxInputLag: 0, inputCount: 0, hotspots: [] };
      return;
    }

    var rateStats = window.__devtool_mutations.getRateStats([1000, 5000, 30000]) || {};
    var framework = window.__devtool_framework ? window.__devtool_framework.detect() : null;
    var isReact = framework && framework.name === 'React';

    var data = {
      rateStats: rateStats,
      isReact: isReact,
      rerenderRate: 0,
      rerenderCount: 0,
      inputLag: 0,
      maxInputLag: 0,
      inputCount: 0,
      hotspots: []
    };

    if (isReact) {
      try {
        var untriggered = window.__devtool_mutations.getUntriggered ? window.__devtool_mutations.getUntriggered() : [];
        var recentUntriggered = untriggered.filter(function(m) {
          return m.timestamp && (Date.now() - m.timestamp) < 30000;
        });
        data.rerenderRate = parseFloat((recentUntriggered.length / 30).toFixed(1));
        data.rerenderCount = recentUntriggered.length;
      } catch (e) { /* ignore */ }

      try {
        var correlationStats = window.__devtool_mutations.getCorrelationStats ? window.__devtool_mutations.getCorrelationStats() : null;
        if (correlationStats && correlationStats.avg_latency) {
          data.inputLag = correlationStats.avg_latency.input || 0;
          data.maxInputLag = correlationStats.max_latency.input || 0;
          data.inputCount = correlationStats.by_type ? (correlationStats.by_type.input || 0) : 0;
        }
      } catch (e) { /* ignore */ }

      try {
        var untriggeredMutations = window.__devtool_mutations.getUntriggered ? window.__devtool_mutations.getUntriggered() : [];
        var elementCounts = {};
        untriggeredMutations.forEach(function(m) {
          if (m.target_selector) {
            elementCounts[m.target_selector] = (elementCounts[m.target_selector] || 0) + 1;
          }
        });
        var hotspots = [];
        for (var selector in elementCounts) {
          hotspots.push({ selector: selector, count: elementCounts[selector] });
        }
        hotspots.sort(function(a, b) { return b.count - a.count; });
        data.hotspots = hotspots.slice(0, 3);
      } catch (e) { /* ignore */ }
    }

    store.performance.val = data;
  }

  function refreshInteractionsData() {
    if (!window.__devtool_interactions) {
      store.interactions.val = [];
      return;
    }
    var history = window.__devtool_interactions.getHistory ? window.__devtool_interactions.getHistory() : [];
    store.interactions.val = history.slice(-10).reverse();
  }

  // Refresh all tab data
  function refreshAllTabData() {
    refreshOverviewData();
    refreshErrorsData();
    refreshNetworkData();
    refreshPerformanceData();
    refreshInteractionsData();
  }

  // Reactive actions - update store, DOM follows automatically
  var actions = {
    addAttachment: function(type, data) {
      var id = generateId();

      if (type === 'screenshot' && data.imageBuffer) {
        // Stream raw PNG bytes directly — no base64 encoding, no JSON overhead
        sendCaptureBinary(id, data.imageBuffer);
        // Store only metadata; drop the ArrayBuffer from in-memory state
        var attachment = {
          id: id,
          type: type,
          label: data.label,
          summary: data.summary,
          data: { area: data.area },
          timestamp: Date.now()
        };
        store.attachments.val = store.attachments.val.concat([attachment]);
      } else {
        // Other types (sketch, element, audit): send JSON capture event
        var attachment = {
          id: id,
          type: type,
          label: data.label,
          summary: data.summary,
          data: data,
          timestamp: Date.now()
        };
        core.send(type + '_capture', {
          id: id,
          timestamp: attachment.timestamp,
          data: data
        });
        store.attachments.val = store.attachments.val.concat([attachment]);
      }

      // Sync to legacy state for backward compatibility
      state.attachments = store.attachments.val;

      return id;
    },

    removeAttachment: function(id) {
      store.attachments.val = store.attachments.val.filter(function(a) { return a.id !== id; });
      state.attachments = store.attachments.val;
    },

    clearAttachments: function() {
      store.attachments.val = [];
      state.attachments = [];
    },

    setMessage: function(text) {
      store.message.val = text;
    },

    // Send context (error or network issue) to the AI agent.
    // Creates an attachment, pre-fills the message, switches to compose tab.
    sendToAgent: function(type, summary, detail) {
      var id = generateId();
      var attachment = {
        id: id,
        type: type,
        label: summary.substring(0, 40),
        summary: summary,
        data: { detail: detail, tag: type, text: summary },
        timestamp: Date.now()
      };
      store.attachments.val = store.attachments.val.concat([attachment]);
      state.attachments = store.attachments.val;
      store.message.val = 'Fix this ' + type + ': ' + summary;
      switchTab('compose');
      togglePanel(true);
    }
  };

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
          hideAttachmentPreview();
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
      showAttachmentPreview(attachment, rect);
    };
    chip.onmouseleave = function() {
      hideAttachmentPreview();
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

      if (!window.__devtool_errors) {
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

        var expanded = false;
        item.onclick = function() {
          expanded = !expanded;
          detail.style.display = expanded ? 'block' : 'none';
        };
        item.onmouseenter = function() { item.style.background = expanded ? 'transparent' : TOKENS.colors.surfaceAlt; };
        item.onmouseleave = function() { item.style.background = 'transparent'; };

        return item;
      }));
    };
  }

  // Network Tab Component
  function NetworkTabComponent() {
    return function() {
      var calls = store.network.val;

      if (!window.__devtool_api) {
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

        var expanded = false;
        item.onclick = function() {
          expanded = !expanded;
          detail.style.display = expanded ? 'block' : 'none';
        };
        item.onmouseenter = function() { item.style.background = expanded ? 'transparent' : TOKENS.colors.surfaceAlt; };
        item.onmouseleave = function() { item.style.background = 'transparent'; };

        return item;
      }));
    };
  }

  // Performance Tab Component
  function PerformanceTabComponent() {
    return function() {
      var data = store.performance.val;

      if (!window.__devtool_mutations) {
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

      if (!window.__devtool_interactions) {
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

  // Design tokens - consistent visual language
  var TOKENS = {
    colors: {
      primary: '#6366f1',      // Indigo
      primaryDark: '#4f46e5',
      secondary: '#64748b',    // Slate
      success: '#22c55e',
      error: '#ef4444',
      active: '#f59e0b',       // Amber - for activity state
      surface: '#ffffff',
      surfaceAlt: '#f8fafc',
      border: '#e2e8f0',
      text: '#1e293b',
      textMuted: '#64748b',
      textInverse: '#ffffff'
    },
    radius: {
      sm: '6px',
      md: '10px',
      lg: '14px',
      full: '9999px'
    },
    shadow: {
      sm: '0 1px 2px rgba(0,0,0,0.05)',
      md: '0 4px 12px rgba(0,0,0,0.1)',
      lg: '0 10px 40px rgba(0,0,0,0.15)',
      glow: '0 0 20px rgba(99,102,241,0.3)'
    },
    spacing: {
      xs: '4px',
      sm: '8px',
      md: '12px',
      lg: '16px',
      xl: '20px'
    }
  };

  // Styles
  var STYLES = {
    // The floating bug - entry point
    bug: [
      'position: fixed',
      'width: 52px',
      'height: 52px',
      'border-radius: ' + TOKENS.radius.full,
      'background: ' + TOKENS.colors.primary,
      'box-shadow: ' + TOKENS.shadow.lg + ', ' + TOKENS.shadow.glow,
      'cursor: pointer',
      'z-index: 2147483646',
      'display: flex',
      'align-items: center',
      'justify-content: center',
      'transition: transform 0.2s ease, box-shadow 0.2s ease',
      'user-select: none'
    ].join(';'),

    statusDot: [
      'position: absolute',
      'top: 0',
      'right: 0',
      'width: 14px',
      'height: 14px',
      'border-radius: ' + TOKENS.radius.full,
      'border: 2.5px solid ' + TOKENS.colors.surface,
      'transition: background-color 0.3s ease'
    ].join(';'),

    // Activity ring - pulses when AI is working
    activityRing: [
      'position: absolute',
      'top: -4px',
      'left: -4px',
      'right: -4px',
      'bottom: -4px',
      'border-radius: ' + TOKENS.radius.full,
      'border: 2px solid ' + TOKENS.colors.active,
      'opacity: 0',
      'pointer-events: none'
    ].join(';'),

    // Output preview - floating next to the bug when AI is outputting
    outputPreview: [
      'position: fixed',
      'max-width: 400px',
      'min-width: 200px',
      'background: rgba(30, 41, 59, 0.95)',
      'color: #e2e8f0',
      'border-radius: ' + TOKENS.radius.md,
      'padding: 10px 14px',
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 12px',
      'line-height: 1.5',
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483645',
      'pointer-events: none',
      'opacity: 0',
      'transform: translateX(10px)',
      'transition: opacity 0.2s ease, transform 0.2s ease',
      'overflow: hidden',
      'white-space: pre-wrap',
      'word-break: break-word',
      'backdrop-filter: blur(8px)'
    ].join(';'),

    outputPreviewVisible: [
      'opacity: 1',
      'transform: translateX(0)'
    ].join(';'),

    // API activity sparkline - thin strip below the bug
    sparkline: [
      'position: fixed',
      'width: 52px',
      'height: 12px',
      'border-radius: 6px',
      'background: rgba(99,102,241,0.15)',
      'overflow: hidden',
      'pointer-events: none',
      'z-index: 2147483645',
      'transition: opacity 0.3s ease'
    ].join(';'),

    // Panel - the main interface
    panel: [
      'position: fixed',
      'width: 480px',
      'background: ' + TOKENS.colors.surface,
      'border-radius: ' + TOKENS.radius.lg,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483645',
      'overflow: visible',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      'font-size: 14px',
      'color: ' + TOKENS.colors.text,
      'transition: opacity 0.2s ease, transform 0.2s ease'
    ].join(';'),

    // Header - minimal, functional
    header: [
      'display: flex',
      'align-items: center',
      'justify-content: space-between',
      'padding: ' + TOKENS.spacing.md + ' ' + TOKENS.spacing.lg,
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-bottom: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    headerTitle: [
      'font-weight: 600',
      'font-size: 13px',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px'
    ].join(';'),

    closeBtn: [
      'background: none',
      'border: none',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'padding: 4px',
      'border-radius: ' + TOKENS.radius.sm,
      'display: flex',
      'transition: background 0.15s ease'
    ].join(';'),

    // Compose area - the main content
    compose: [
      'padding: ' + TOKENS.spacing.lg
    ].join(';'),

    // Message card - groups message + attachments (Gestalt: Common Region)
    messageCard: [
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.md,
      'background: ' + TOKENS.colors.surface,
      'overflow: hidden',
      'transition: border-color 0.2s ease, box-shadow 0.2s ease'
    ].join(';'),

    messageCardFocused: [
      'border-color: ' + TOKENS.colors.primary,
      'box-shadow: 0 0 0 3px rgba(99,102,241,0.1)'
    ].join(';'),

    // Text input within card
    textarea: [
      'width: 100%',
      'min-height: 80px',
      'padding: ' + TOKENS.spacing.md,
      'border: none',
      'outline: none',
      'resize: none',
      'font-size: 14px',
      'font-family: inherit',
      'line-height: 1.5',
      'color: ' + TOKENS.colors.text,
      'background: transparent',
      'box-sizing: border-box'
    ].join(';'),

    // Attachment chips area (Gestalt: Proximity - grouped with message)
    attachmentArea: [
      'padding: 0 ' + TOKENS.spacing.md + ' ' + TOKENS.spacing.md,
      'display: flex',
      'flex-wrap: wrap',
      'gap: ' + TOKENS.spacing.sm
    ].join(';'),

    // Individual attachment chip
    chip: [
      'display: inline-flex',
      'align-items: center',
      'gap: 6px',
      'padding: 5px 10px',
      'background: ' + TOKENS.colors.surfaceAlt,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.full,
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'max-width: 200px',
      'overflow: hidden'
    ].join(';'),

    chipIcon: [
      'flex-shrink: 0',
      'width: 14px',
      'height: 14px'
    ].join(';'),

    chipLabel: [
      'white-space: nowrap',
      'overflow: hidden',
      'text-overflow: ellipsis'
    ].join(';'),

    chipRemove: [
      'flex-shrink: 0',
      'background: none',
      'border: none',
      'padding: 0',
      'cursor: pointer',
      'color: ' + TOKENS.colors.textMuted,
      'display: flex',
      'transition: color 0.15s ease'
    ].join(';'),

    // Attachment preview popup (shown on chip hover)
    attachmentPreview: [
      'position: fixed',
      'z-index: 2147483647',
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.lg,
      'box-shadow: 0 8px 24px rgba(0,0,0,0.4)',
      'padding: ' + TOKENS.spacing.sm,
      'max-width: 320px',
      'max-height: 240px',
      'overflow: hidden',
      'pointer-events: none',
      'opacity: 0',
      'transition: opacity 0.15s ease'
    ].join(';'),

    attachmentPreviewImage: [
      'width: 100%',
      'height: auto',
      'max-height: 200px',
      'object-fit: contain',
      'border-radius: ' + TOKENS.radius.sm
    ].join(';'),

    attachmentPreviewElement: [
      'font-family: monospace',
      'font-size: 11px',
      'color: ' + TOKENS.colors.text,
      'white-space: pre-wrap',
      'word-break: break-word'
    ].join(';'),

    // Element highlight overlay for element preview
    elementPreviewHighlight: [
      'position: fixed',
      'z-index: 2147483645',
      'background: rgba(99, 102, 241, 0.2)',
      'border: 2px solid ' + TOKENS.colors.primary,
      'border-radius: 2px',
      'pointer-events: none',
      'transition: all 0.15s ease'
    ].join(';'),

    // Toolbar - secondary actions (Gestalt: Similarity)
    // Flexbox with wrap for responsive fit
    toolbar: [
      'display: flex',
      'flex-wrap: wrap',
      'align-items: center',
      'gap: 6px',
      'padding: 10px ' + TOKENS.spacing.md,
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-top: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    // Container for action buttons (left side)
    toolbarActions: [
      'display: flex',
      'flex-wrap: wrap',
      'align-items: center',
      'gap: 6px',
      'flex: 1 1 auto',
      'min-width: 0'
    ].join(';'),

    toolBtn: [
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'gap: 4px',
      'padding: 6px 10px',
      'background: transparent',
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 12px',
      'font-weight: 500',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'transition: all 0.15s ease',
      'white-space: nowrap'
    ].join(';'),

    // Primary send button - visual hierarchy (most prominent)
    sendBtn: [
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'gap: 5px',
      'padding: 8px 14px',
      'background: ' + TOKENS.colors.primary,
      'border: none',
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 13px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textInverse,
      'cursor: pointer',
      'transition: background 0.15s ease, transform 0.1s ease',
      'white-space: nowrap',
      'margin-left: auto'
    ].join(';'),

    // Selection overlays
    overlay: [
      'position: fixed',
      'top: 0',
      'left: 0',
      'right: 0',
      'bottom: 0',
      'z-index: 2147483647',
      'background: transparent',
      'pointer-events: auto',
      'user-select: none',
      '-webkit-user-select: none',
      'cursor: crosshair'
    ].join(';'),

    overlayDimmed: [
      'background: rgba(0, 0, 0, 0.4)'
    ].join(';'),

    selectionBox: [
      'position: absolute',
      'border: 2px solid ' + TOKENS.colors.primary,
      'background: rgba(99, 102, 241, 0.15)',
      'border-radius: 4px',
      'pointer-events: none'
    ].join(';'),

    elementHighlight: [
      'position: absolute',
      'border: 2px solid ' + TOKENS.colors.primary,
      'background: rgba(99, 102, 241, 0.1)',
      'pointer-events: none',
      'border-radius: 4px',
      'z-index: 2147483647'
    ].join(';'),

    tooltip: [
      'position: absolute',
      'background: ' + TOKENS.colors.text,
      'color: ' + TOKENS.colors.textInverse,
      'padding: 4px 8px',
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 11px',
      'font-family: ui-monospace, monospace',
      'white-space: nowrap',
      'pointer-events: none'
    ].join(';'),

    // Instructions bar during selection
    instructionBar: [
      'position: fixed',
      'bottom: 20px',
      'left: 50%',
      'transform: translateX(-50%)',
      'background: ' + TOKENS.colors.text,
      'color: ' + TOKENS.colors.textInverse,
      'padding: 10px 20px',
      'border-radius: ' + TOKENS.radius.full,
      'font-size: 13px',
      'font-weight: 500',
      'z-index: 2147483647',
      'box-shadow: ' + TOKENS.shadow.lg
    ].join(';'),

    // Dropdown container
    dropdownContainer: [
      'position: relative',
      'display: inline-block'
    ].join(';'),

    // Dropdown button with chevron
    dropdownBtn: [
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'gap: 4px',
      'padding: 6px 10px',
      'background: transparent',
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'font-size: 12px',
      'font-weight: 500',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'transition: all 0.15s ease',
      'white-space: nowrap'
    ].join(';'),

    // Mega menu (audit dropdown)
    megaMenu: [
      'position: absolute',
      'bottom: calc(100% + 4px)',
      'left: 0',
      'width: 480px',
      'max-width: calc(100vw - 16px)',
      'background: ' + TOKENS.colors.surface,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.md,
      'box-shadow: ' + TOKENS.shadow.lg,
      'z-index: 2147483648',
      'opacity: 0',
      'transform: translateY(4px)',
      'pointer-events: none',
      'transition: opacity 0.15s ease, transform 0.15s ease'
    ].join(';'),

    megaMenuVisible: [
      'opacity: 1',
      'transform: translateY(0)',
      'pointer-events: auto'
    ].join(';'),

    // Top row of mega menu: 4 columns for the first 4 sections
    megaMenuTopRow: [
      'display: grid',
      'grid-template-columns: 1fr 1fr 1fr 1fr',
      'gap: 0'
    ].join(';'),

    // Bottom row of mega menu: Technical section spanning full width
    megaMenuBottomRow: [
      'border-top: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    // Column within mega menu
    megaMenuColumn: [
      'padding: ' + TOKENS.spacing.sm + ' 0',
      'border-right: 1px solid ' + TOKENS.colors.border
    ].join(';'),

    megaMenuColumnLast: [
      'border-right: none'
    ].join(';'),

    // Column header in mega menu
    megaMenuColumnHeader: [
      'padding: 4px ' + TOKENS.spacing.sm,
      'font-size: 10px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px',
      'margin-bottom: 2px'
    ].join(';'),

    // Menu item with label + description blurb
    megaMenuItem: [
      'display: block',
      'padding: 5px ' + TOKENS.spacing.sm,
      'font-size: 12px',
      'color: ' + TOKENS.colors.text,
      'cursor: pointer',
      'transition: background 0.1s ease',
      'border: none',
      'background: none',
      'width: 100%',
      'text-align: left',
      'line-height: 1.3'
    ].join(';'),

    megaMenuItemHover: [
      'background: ' + TOKENS.colors.surfaceAlt
    ].join(';'),

    megaMenuItemLabel: [
      'font-weight: 600',
      'font-size: 12px',
      'color: ' + TOKENS.colors.text
    ].join(';'),

    megaMenuItemDesc: [
      'font-weight: 400',
      'font-size: 10px',
      'color: ' + TOKENS.colors.textMuted,
      'display: block',
      'margin-top: 1px'
    ].join(';'),

    // Technical section: 3-column grid spanning full width
    megaMenuTechnicalGrid: [
      'display: grid',
      'grid-template-columns: 1fr 1fr 1fr',
      'padding: ' + TOKENS.spacing.sm + ' 0'
    ].join(';'),

    megaMenuTechnicalHeader: [
      'padding: 4px ' + TOKENS.spacing.sm,
      'font-size: 10px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.textMuted,
      'text-transform: uppercase',
      'letter-spacing: 0.5px',
      'border-top: none'
    ].join(';'),

    // Tab styles
    tabBar: [
      'display: flex',
      'align-items: center',
      'background: ' + TOKENS.colors.surfaceAlt,
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'overflow-x: auto',
      'overflow-y: hidden',
      'padding: 0 ' + TOKENS.spacing.sm,
      'gap: ' + TOKENS.spacing.xs
    ].join(';'),

    tab: [
      'padding: 8px 12px',
      'font-size: 12px',
      'font-weight: 500',
      'border: none',
      'background: transparent',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'border-bottom: 2px solid transparent',
      'transition: color 0.15s ease, border-color 0.15s ease',
      'white-space: nowrap',
      'position: relative',
      'display: flex',
      'align-items: center',
      'gap: 4px'
    ].join(';'),

    tabActive: [
      'color: ' + TOKENS.colors.primary,
      'border-bottom-color: ' + TOKENS.colors.primary
    ].join(';'),

    tabBadge: [
      'min-width: 16px',
      'height: 16px',
      'padding: 0 4px',
      'font-size: 10px',
      'font-weight: 600',
      'border-radius: ' + TOKENS.radius.full,
      'display: inline-flex',
      'align-items: center',
      'justify-content: center',
      'line-height: 1'
    ].join(';'),

    tabBadgeRed: [
      'background: ' + TOKENS.colors.error,
      'color: white'
    ].join(';'),

    tabBadgeYellow: [
      'background: ' + TOKENS.colors.active,
      'color: white'
    ].join(';'),

    tabBadgeGreen: [
      'background: ' + TOKENS.colors.success,
      'color: white'
    ].join(';'),

    tabContent: [
      'padding: ' + TOKENS.spacing.lg,
      'max-height: 400px',
      'overflow-y: auto',
      'overflow-x: hidden'
    ].join(';'),

    tabCloseBtn: [
      'margin-left: auto',
      'background: none',
      'border: none',
      'color: ' + TOKENS.colors.textMuted,
      'cursor: pointer',
      'padding: 4px',
      'display: flex',
      'flex-shrink: 0'
    ].join(';'),

    // Tab content specific styles
    healthCard: [
      'background: ' + TOKENS.colors.surfaceAlt,
      'border: 1px solid ' + TOKENS.colors.border,
      'border-radius: ' + TOKENS.radius.sm,
      'padding: ' + TOKENS.spacing.md,
      'margin-bottom: ' + TOKENS.spacing.sm
    ].join(';'),

    healthLabel: [
      'font-size: 11px',
      'color: ' + TOKENS.colors.textMuted,
      'margin-bottom: 4px',
      'text-transform: uppercase',
      'letter-spacing: 0.5px'
    ].join(';'),

    healthValue: [
      'font-size: 20px',
      'font-weight: 600',
      'color: ' + TOKENS.colors.text
    ].join(';'),

    errorItem: [
      'padding: ' + TOKENS.spacing.sm,
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'font-size: 12px',
      'cursor: pointer',
      'transition: background 0.15s ease'
    ].join(';'),

    errorMessage: [
      'color: ' + TOKENS.colors.text,
      'margin-bottom: 4px',
      'font-weight: 500'
    ].join(';'),

    errorMeta: [
      'color: ' + TOKENS.colors.textMuted,
      'font-size: 11px'
    ].join(';'),

    emptyState: [
      'text-align: center',
      'padding: ' + TOKENS.spacing.xl,
      'color: ' + TOKENS.colors.textMuted,
      'font-size: 13px'
    ].join(';'),

    // Micro toast - compact pill near the bug
    microToast: [
      'position: fixed',
      'background: rgba(15, 23, 42, 0.92)',
      'color: #e2e8f0',
      'font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
      'font-size: 11px',
      'font-weight: 500',
      'padding: 5px 12px',
      'border-radius: ' + TOKENS.radius.full,
      'white-space: nowrap',
      'backdrop-filter: blur(12px)',
      'pointer-events: none',
      'z-index: 2147483646',
      'opacity: 0',
      'transform: translateY(6px) scale(0.92)',
      'transition: opacity 0.25s cubic-bezier(0.16,1,0.3,1), transform 0.25s cubic-bezier(0.16,1,0.3,1)',
      'box-shadow: 0 4px 20px rgba(0,0,0,0.25), inset 0 0.5px 0 rgba(255,255,255,0.06)',
      'letter-spacing: 0.3px',
      'max-width: 260px',
      'overflow: hidden',
      'text-overflow: ellipsis'
    ].join(';'),

    microToastVisible: [
      'opacity: 1',
      'transform: translateY(0) scale(1)'
    ].join(';'),

    // History tab item
    historyItem: [
      'display: flex',
      'align-items: flex-start',
      'gap: 10px',
      'padding: 8px 0',
      'border-bottom: 1px solid ' + TOKENS.colors.border,
      'font-size: 12px',
      'line-height: 1.4',
      'transition: background 0.12s ease'
    ].join(';'),

    historyDot: [
      'flex-shrink: 0',
      'width: 8px',
      'height: 8px',
      'border-radius: ' + TOKENS.radius.full,
      'margin-top: 4px'
    ].join(';'),

    historyBody: [
      'flex: 1',
      'min-width: 0'
    ].join(';'),

    historyText: [
      'color: ' + TOKENS.colors.text,
      'white-space: nowrap',
      'overflow: hidden',
      'text-overflow: ellipsis'
    ].join(';'),

    historyMeta: [
      'font-size: 10px',
      'color: ' + TOKENS.colors.textMuted,
      'margin-top: 1px'
    ].join(';'),

    historyTime: [
      'flex-shrink: 0',
      'font-size: 10px',
      'color: ' + TOKENS.colors.textMuted,
      'margin-top: 3px',
      'white-space: nowrap'
    ].join(';')
  };

  // Icons (compact SVGs)
  var ICONS = {
    logo: '<svg width="24" height="24" viewBox="0 0 24 24" fill="white"><path d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/></svg>',
    close: '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6L6 18M6 6l12 12"/></svg>',
    send: '<svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor"><path d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z"/></svg>',
    screenshot: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="M21 15l-5-5L5 21"/></svg>',
    element: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>',
    sketch: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 19l7-7 3 3-7 7-3-3z"/><path d="M18 13l-1.5-7.5L2 2l3.5 14.5L13 18l5-5z"/><path d="M2 2l7.586 7.586"/><circle cx="11" cy="11" r="2"/></svg>',
    design: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 20h9"/><path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z"/></svg>',
    x: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M18 6L6 18M6 6l12 12"/></svg>',
    actions: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>',
    chevronDown: '<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="6 9 12 15 18 9"/></svg>',
    check: '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="20 6 9 17 4 12"/></svg>',
    audit: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 11l3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/></svg>',
    styleEdit: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2C6.5 2 2 6.5 2 12s4.5 10 10 10c.9 0 1.7-.1 2.5-.3"/><path d="M12 2c2.2 0 4 4.5 4 10"/><path d="M12 2c-2.2 0-4 4.5-4 10s1.8 10 4 10"/><path d="M2 12h10"/><path d="M20 14l-4 4 1.5 1.5a2.12 2.12 0 0 0 3 0 2.12 2.12 0 0 0 0-3L20 14z"/></svg>',
    inspect: '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M2 22l1-6.5 5.5 5.5L2 22z"/><path d="M8.5 15.5L18 6a2.83 2.83 0 1 0-4-4L4.5 11.5"/><circle cx="18" cy="4" r="1" fill="currentColor" stroke="none"/></svg>'
  };

  // Initialize
  function init() {
    if (state.container) return;
    loadPrefs();
    createUI();
    setupStatusPolling();
    setupGlobalShortcuts();
  }

  // Global keyboard shortcuts
  function setupGlobalShortcuts() {
    document.addEventListener('keydown', function(e) {
      // Ctrl+Y (or Cmd+Y on Mac) - toggle indicator panel
      if (e.key === 'y' && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey) {
        e.preventDefault();
        togglePanel();
      }
    });
  }

  function createUI() {
    state.container = document.createElement('div');
    state.container.id = '__devtool-indicator';
    if (!state.isVisible) state.container.style.display = 'none';

    // Isolate indicator clicks from page handlers (e.g., dropdown "click outside" detectors)
    // Use bubbling phase (not capture) so events still reach child elements inside the container
    // Only stop click/mousedown/pointerdown — NOT mouseup/pointerup, which must reach
    // document for drag release detection in handleDragStart
    state.container.addEventListener('click', function(e) { e.stopPropagation(); });
    state.container.addEventListener('mousedown', function(e) { e.stopPropagation(); });
    state.container.addEventListener('pointerdown', function(e) { e.stopPropagation(); });

    createBug();
    createPanel();
    createOutputPreview();
    createMicroToast();

    document.documentElement.appendChild(state.container);
    createSparkline();
  }

  // Create floating output preview element
  function createOutputPreview() {
    var preview = document.createElement('div');
    preview.id = '__devtool-output-preview';
    preview.style.cssText = STYLES.outputPreview;
    state.outputPreview = preview;
    state.container.appendChild(preview);
  }

  // Show output preview with lines floating next to the bug
  function showOutputPreview(lines) {
    if (!state.outputPreview || !state.bug || !lines || lines.length === 0) return;

    // Format lines with subtle styling
    var html = lines.map(function(line) {
      // Limit each line to prevent overflow
      if (line.length > 80) {
        line = line.substring(0, 77) + '...';
      }
      return escapeHtml(line);
    }).join('\n');

    state.outputPreview.innerHTML = html;

    // Position next to the bug (to the right)
    var bugRect = state.bug.getBoundingClientRect();
    var previewWidth = Math.min(400, window.innerWidth - bugRect.right - 30);

    state.outputPreview.style.left = (bugRect.right + 12) + 'px';
    state.outputPreview.style.bottom = state.position.y + 'px';
    state.outputPreview.style.maxWidth = previewWidth + 'px';

    // If not enough space on right, position on left
    if (bugRect.right + 220 > window.innerWidth) {
      state.outputPreview.style.left = 'auto';
      state.outputPreview.style.right = (window.innerWidth - bugRect.left + 12) + 'px';
    }

    // Show with animation
    state.outputPreview.style.cssText = STYLES.outputPreview + ';' + STYLES.outputPreviewVisible;
    state.outputPreview.style.left = (bugRect.right + 12) + 'px';
    state.outputPreview.style.bottom = state.position.y + 'px';

    // Auto-hide after 3 seconds of no updates
    clearTimeout(state.outputPreviewTimeout);
    state.outputPreviewTimeout = setTimeout(function() {
      hideOutputPreview();
    }, 3000);
  }

  // Hide output preview
  function hideOutputPreview() {
    if (!state.outputPreview) return;
    state.outputPreview.style.cssText = STYLES.outputPreview;
    clearTimeout(state.outputPreviewTimeout);
  }

  // Create micro toast element
  function createMicroToast() {
    var toast = document.createElement('div');
    toast.id = '__devtool-micro-toast';
    toast.style.cssText = STYLES.microToast;
    state.microToast = toast;
    state.container.appendChild(toast);
  }

  // Show a compact micro toast near the bug
  function showMicroToast(text, color) {
    if (!state.microToast || !state.bug) return;

    // Set content
    state.microToast.textContent = text;

    // Subtle left accent line
    var accentColor = color || TOKENS.colors.primary;
    state.microToast.style.cssText = STYLES.microToast;
    state.microToast.style.borderLeft = '2px solid ' + accentColor;

    // Position above the bug, centered
    var bugRect = state.bug.getBoundingClientRect();
    var toastW = state.microToast.offsetWidth || 120;
    var left = bugRect.left + (bugRect.width / 2) - (toastW / 2);
    // Keep on screen
    left = Math.max(8, Math.min(left, window.innerWidth - toastW - 8));

    state.microToast.style.left = left + 'px';
    state.microToast.style.bottom = (window.innerHeight - bugRect.top + 8) + 'px';

    // Animate in
    requestAnimationFrame(function() {
      state.microToast.style.cssText = STYLES.microToast + ';' + STYLES.microToastVisible;
      state.microToast.style.borderLeft = '2px solid ' + accentColor;
      state.microToast.style.left = left + 'px';
      state.microToast.style.bottom = (window.innerHeight - bugRect.top + 8) + 'px';
    });

    // Auto-hide after 2.5 seconds
    clearTimeout(state.microToastTimeout);
    state.microToastTimeout = setTimeout(function() {
      hideMicroToast();
    }, 2500);
  }

  // Hide the micro toast
  function hideMicroToast() {
    if (!state.microToast) return;
    state.microToast.style.cssText = STYLES.microToast;
    clearTimeout(state.microToastTimeout);
  }

  // Create the API activity sparkline element
  function createSparkline() {
    var el = document.createElement('div');
    el.id = '__devtool-sparkline';
    el.style.cssText = STYLES.sparkline;
    el.innerHTML = '<svg width="52" height="12" viewBox="0 0 52 12" xmlns="http://www.w3.org/2000/svg"></svg>';
    state.sparkline = el;
    state.container.appendChild(el);
    positionSparkline();
    updateSparkline();
    // Refresh every 2 seconds
    state.sparklineInterval = setInterval(updateSparkline, 2000);
  }

  // Position sparkline below the bug
  function positionSparkline() {
    if (!state.sparkline || !state.bug) return;
    var bugRect = state.bug.getBoundingClientRect();
    state.sparkline.style.left = bugRect.left + 'px';
    state.sparkline.style.bottom = (window.innerHeight - bugRect.bottom - 18) + 'px';
  }

  // Update sparkline SVG from API tracker data
  function updateSparkline() {
    if (!state.sparkline) return;
    if (!window.__devtool_api || !window.__devtool_api.getSparklineData) return;

    var data = window.__devtool_api.getSparklineData(60);
    var buckets = data.buckets;
    var maxBucket = data.maxBucket || 1;
    var svg = state.sparkline.querySelector('svg');
    if (!svg) return;

    // Downsample 60 buckets to 26 bars (fit in 52px with 2px per bar)
    var barCount = 26;
    var barWidth = 2;
    var maxH = 10; // leave 1px padding top+bottom
    var step = Math.max(1, Math.floor(buckets.length / barCount));
    var points = '';
    for (var i = 0; i < barCount; i++) {
      var sum = 0;
      var count = 0;
      for (var j = 0; j < step && (i * step + j) < buckets.length; j++) {
        sum += buckets[i * step + j];
        count++;
      }
      var avg = count > 0 ? sum / count : 0;
      var h = Math.round((avg / maxBucket) * maxH);
      if (h < 1 && avg > 0) h = 1; // minimum visible bar for activity
      var x = i * barWidth;
      var y = 12 - h;
      points += '<rect x="' + x + '" y="' + y + '" width="' + (barWidth - 0.5) + '" height="' + h + '" fill="rgba(99,102,241,0.7)" rx="0.5"/>';
    }

    svg.innerHTML = points;
    positionSparkline();

    // Fade out if no recent activity
    var recentTotal = 0;
    for (var k = buckets.length - 5; k < buckets.length; k++) {
      if (k >= 0) recentTotal += buckets[k];
    }
    state.sparkline.style.opacity = recentTotal > 0 ? '1' : '0.3';
  }

  // Log an event to the history store
  function logHistoryEvent(type, text, detail) {
    var maxHistory = 200;
    var entry = {
      id: Date.now() + '_' + Math.random().toString(36).substr(2, 4),
      type: type,       // 'tool', 'message', 'error', 'network', 'screenshot', 'system'
      text: text,
      detail: detail || '',
      timestamp: Date.now()
    };
    var current = store.history.val.slice();
    current.unshift(entry);
    if (current.length > maxHistory) current = current.slice(0, maxHistory);
    store.history.val = current;
  }

  // Escape HTML to prevent XSS
  function escapeHtml(text) {
    var div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  function createBug() {
    var bug = document.createElement('div');
    bug.style.cssText = STYLES.bug;
    bug.style.left = state.position.x + 'px';
    bug.style.bottom = state.position.y + 'px';
    bug.innerHTML = ICONS.logo;

    // Activity ring (pulses when AI is working)
    var ring = document.createElement('div');
    ring.id = '__devtool-activity-ring';
    ring.style.cssText = STYLES.activityRing;
    bug.appendChild(ring);

    // Inject CSS animation for pulse effect
    injectActivityAnimation();

    // Status indicator
    var dot = document.createElement('div');
    dot.id = '__devtool-status';
    dot.style.cssText = STYLES.statusDot;
    dot.style.backgroundColor = core.isConnected() ? TOKENS.colors.success : TOKENS.colors.error;
    bug.appendChild(dot);

    // Entrance animation, then idle breathing
    bug.classList.add('__devtool-entrance');
    bug.addEventListener('animationend', function handler() {
      bug.classList.remove('__devtool-entrance');
      if (!state.isActive) {
        bug.classList.add('__devtool-breathe');
      }
      bug.removeEventListener('animationend', handler);
    });

    // Drag and click handling
    bug.addEventListener('mousedown', handleDragStart);
    bug.addEventListener('mouseenter', function() {
      if (!state.isDragging) {
        bug.style.transform = 'scale(1.08)';
      }
    });
    bug.addEventListener('mouseleave', function() {
      if (!state.isDragging) {
        bug.style.transform = 'scale(1)';
      }
    });

    state.bug = bug;
    state.container.appendChild(bug);
  }

  // Inject CSS keyframes for activity animation
  function injectActivityAnimation() {
    if (document.getElementById('__devtool-activity-style')) return;

    var style = document.createElement('style');
    style.id = '__devtool-activity-style';
    style.textContent = [
      // Entrance animation - spring bounce with rotation
      '@keyframes __devtool-entrance {',
      '  0% { transform: scale(0) rotate(-180deg); opacity: 0; }',
      '  50% { transform: scale(1.12) rotate(8deg); opacity: 1; }',
      '  70% { transform: scale(0.96) rotate(-3deg); }',
      '  100% { transform: scale(1) rotate(0deg); }',
      '}',
      '.__devtool-entrance {',
      '  animation: __devtool-entrance 0.6s cubic-bezier(0.34, 1.56, 0.64, 1) both;',
      '}',
      // Idle breathing glow
      '@keyframes __devtool-breathe {',
      '  0%, 100% { box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 20px rgba(99,102,241,0.2); }',
      '  50% { box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 28px rgba(99,102,241,0.4); }',
      '}',
      '.__devtool-breathe {',
      '  animation: __devtool-breathe 3s ease-in-out infinite;',
      '}',
      // Orbital ring - rotating glow
      '@keyframes __devtool-orbit {',
      '  0% { transform: rotate(0deg) scale(1); box-shadow: 4px 0 10px rgba(245,158,11,0.5), -4px 0 10px rgba(99,102,241,0.3); }',
      '  25% { transform: rotate(90deg) scale(1.06); box-shadow: 0 4px 12px rgba(245,158,11,0.6), 0 -4px 10px rgba(99,102,241,0.4); }',
      '  50% { transform: rotate(180deg) scale(1.1); box-shadow: -4px 0 14px rgba(245,158,11,0.7), 4px 0 10px rgba(99,102,241,0.5); }',
      '  75% { transform: rotate(270deg) scale(1.06); box-shadow: 0 -4px 12px rgba(245,158,11,0.6), 0 4px 10px rgba(99,102,241,0.4); }',
      '  100% { transform: rotate(360deg) scale(1); box-shadow: 4px 0 10px rgba(245,158,11,0.5), -4px 0 10px rgba(99,102,241,0.3); }',
      '}',
      '.__devtool-active {',
      '  animation: __devtool-orbit 2.5s linear infinite;',
      '}',
      // Bug active pulse - scale + shadow throb
      '@keyframes __devtool-active-pulse {',
      '  0%, 100% {',
      '    transform: scale(1);',
      '    box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 20px rgba(99,102,241,0.3), 0 0 0 0 rgba(245,158,11,0);',
      '  }',
      '  50% {',
      '    transform: scale(1.06);',
      '    box-shadow: 0 10px 40px rgba(0,0,0,0.15), 0 0 25px rgba(99,102,241,0.5), 0 0 12px rgba(245,158,11,0.35);',
      '  }',
      '}',
      '.__devtool-bug-active {',
      '  animation: __devtool-active-pulse 2s ease-in-out infinite !important;',
      '}'
    ].join('\\n');
    document.head.appendChild(style);
  }

  // Set activity state (called when AI tool becomes active/idle)
  function setActivityState(isActive) {
    state.isActive = isActive;
    var ring = document.getElementById('__devtool-activity-ring');
    var bug = state.bug;

    if (isActive) {
      if (bug) {
        bug.classList.remove('__devtool-breathe');
        bug.classList.add('__devtool-bug-active');
      }
      if (ring) {
        ring.classList.add('__devtool-active');
        ring.style.opacity = '1';
      }
    } else {
      if (bug) {
        bug.classList.remove('__devtool-bug-active');
        bug.classList.add('__devtool-breathe');
      }
      if (ring) {
        ring.classList.remove('__devtool-active');
        ring.style.opacity = '0';
      }
      hideOutputPreview();
    }
  }

  // Inject container query styles for responsive tabs
  function injectContainerQueryStyles() {
    if (document.getElementById('__devtool-container-style')) return;

    var style = document.createElement('style');
    style.id = '__devtool-container-style';
    style.textContent = [
      // Make panel a container for container queries
      '#__devtool-panel {',
      '  container-type: inline-size;',
      '  container-name: devtool-panel;',
      '}',
      // Tab bar uses CSS grid - horizontal row with auto-sizing columns
      '.__devtool-tab-bar {',
      '  display: grid !important;',
      '  grid-template-columns: repeat(8, 1fr) auto;',
      '  gap: 0;',
      '  align-items: center;',
      '}',
      // Tab buttons - centered text, no overflow
      '.__devtool-tab {',
      '  justify-content: center;',
      '  text-align: center;',
      '  min-width: 0;',
      '  white-space: nowrap !important;',
      '}',
      // Short label hidden by default (wide panel shows full labels)
      '.__devtool-tab-short { display: none; }',
      '.__devtool-tab-full { display: inline; }',
      // Container query: narrow panel (< 420px) - show short labels
      '@container devtool-panel (max-width: 420px) {',
      '  .__devtool-tab { padding: 6px 4px !important; font-size: 11px !important; }',
      '  .__devtool-tab-short { display: inline; }',
      '  .__devtool-tab-full { display: none; }',
      '}',
      // Container query: very narrow panel (< 300px) - minimal padding
      '@container devtool-panel (max-width: 300px) {',
      '  .__devtool-tab { padding: 4px 2px !important; font-size: 10px !important; }',
      '}',
      // Container query: wide panel (> 520px) - comfortable spacing
      '@container devtool-panel (min-width: 520px) {',
      '  .__devtool-tab { padding: 10px 14px !important; }',
      '}'
    ].join('\n');
    document.head.appendChild(style);
  }

  function createPanel() {
    // Inject container query styles
    injectContainerQueryStyles();

    var panel = document.createElement('div');
    panel.id = '__devtool-panel';
    panel.style.cssText = STYLES.panel + '; display: flex; flex-direction: column;';
    panel.style.display = 'none';
    panel.style.opacity = '0';
    panel.style.transform = 'translateY(8px)';

    // Tab bar (replaces header)
    var tabBar = createTabBar();
    panel.appendChild(tabBar);

    // Tab content area
    var tabContent = document.createElement('div');
    tabContent.id = '__devtool-tab-content';
    tabContent.style.cssText = STYLES.tabContent;
    panel.appendChild(tabContent);

    state.panel = panel;
    state.container.appendChild(panel);

    // Load active tab from localStorage
    try {
      var savedTab = localStorage.getItem('__devtool_active_tab');
      if (savedTab) {
        state.activeTab = savedTab;
      }
    } catch (e) {
      // Ignore localStorage errors
    }

    // Initial render
    switchTab(state.activeTab);
  }

  function createTabBar() {
    var tabBar = document.createElement('div');
    tabBar.className = '__devtool-tab-bar';
    tabBar.style.cssText = STYLES.tabBar;

    var tabs = [
      { id: 'compose', label: 'Message', short: 'Msg', title: 'Send message to agent' },
      { id: 'overview', label: 'Overview', short: 'Info', title: 'Page overview' },
      { id: 'errors', label: 'Errors', short: 'Err', title: 'JavaScript errors' },
      { id: 'network', label: 'Network', short: 'Net', title: 'Network requests' },
      { id: 'performance', label: 'Perf', short: 'Perf', title: 'Performance metrics' },
      { id: 'interactions', label: 'Interact', short: 'Intx', title: 'User interactions' },
      { id: 'history', label: 'History', short: 'Hist', title: 'Event history' }
    ];

    tabs.forEach(function(tabInfo) {
      var tab = document.createElement('button');
      tab.id = '__devtool-tab-' + tabInfo.id;
      tab.className = '__devtool-tab';
      tab.style.cssText = STYLES.tab;
      // Add both full and short labels as spans for container query switching
      tab.innerHTML = '<span class="__devtool-tab-full">' + tabInfo.label + '</span>' +
                      '<span class="__devtool-tab-short">' + tabInfo.short + '</span>';
      if (tabInfo.title) tab.title = tabInfo.title;
      tab.onclick = function() { switchTab(tabInfo.id); };

      // Highlight active tab
      if (state.activeTab === tabInfo.id) {
        tab.style.cssText = STYLES.tab + ';' + STYLES.tabActive;
      }

      tabBar.appendChild(tab);
    });

    // Close button at the end
    var closeBtn = document.createElement('button');
    closeBtn.style.cssText = STYLES.tabCloseBtn;
    closeBtn.innerHTML = ICONS.close;
    closeBtn.setAttribute('aria-label', 'Close panel');
    closeBtn.title = 'Close panel';
    closeBtn.onclick = function(e) { e.stopPropagation(); togglePanel(false); };
    closeBtn.onmouseenter = function() { closeBtn.style.color = TOKENS.colors.text; };
    closeBtn.onmouseleave = function() { closeBtn.style.color = TOKENS.colors.textMuted; };
    tabBar.appendChild(closeBtn);

    return tabBar;
  }

  function switchTab(tabId) {
    state.activeTab = tabId;

    // Save to localStorage
    try {
      localStorage.setItem('__devtool_active_tab', tabId);
    } catch (e) {
      // Ignore localStorage errors
    }

    // Update tab bar highlighting
    var tabs = ['compose', 'overview', 'errors', 'network', 'performance', 'interactions', 'history'];
    tabs.forEach(function(id) {
      var tab = document.getElementById('__devtool-tab-' + id);
      if (tab) {
        if (id === tabId) {
          tab.style.cssText = STYLES.tab + ';' + STYLES.tabActive;
        } else {
          tab.style.cssText = STYLES.tab;
        }
      }
    });

    // Render tab content
    var content = document.getElementById('__devtool-tab-content');
    if (!content) return;

    content.innerHTML = '';

    // Refresh data for the tab before rendering
    switch (tabId) {
      case 'overview':
        refreshOverviewData();
        van.add(content, OverviewTabComponent());
        break;
      case 'errors':
        refreshErrorsData();
        van.add(content, ErrorsTabComponent());
        break;
      case 'network':
        refreshNetworkData();
        van.add(content, NetworkTabComponent());
        break;
      case 'performance':
        refreshPerformanceData();
        van.add(content, PerformanceTabComponent());
        break;
      case 'interactions':
        refreshInteractionsData();
        van.add(content, InteractionsTabComponent());
        break;
      case 'history':
        van.add(content, HistoryTabComponent());
        break;
      case 'compose':
        renderComposeTab(content);
        break;
    }

    // Adjust panel width for history tab (needs more room for timestamps)
    if (state.panel) {
      state.panel.style.width = (tabId === 'history') ? '540px' : '480px';
    }

    // Start update interval for active tab
    updateTabBadges();
    startTabUpdates();
  }

  function startTabUpdates() {
    // Clear existing interval
    if (state.tabUpdateInterval) {
      clearInterval(state.tabUpdateInterval);
    }

    // Only update if panel is expanded
    if (!state.isExpanded) return;

    // Update every second
    state.tabUpdateInterval = setInterval(function() {
      if (!state.isExpanded) {
        clearInterval(state.tabUpdateInterval);
        state.tabUpdateInterval = null;
        return;
      }

      updateTabBadges();
      updateActiveTabContent();
    }, 1000);
  }

  function updateTabBadges() {
    // Update error tab badge
    var errorTab = document.getElementById('__devtool-tab-errors');
    if (errorTab && window.__devtool_errors) {
      var stats = window.__devtool_errors.getStats();
      var totalErrors = stats.totalCount;
      updateTabBadge(errorTab, totalErrors, totalErrors > 0 ? 'red' : null);
    }

    // Update network tab badge
    var networkTab = document.getElementById('__devtool-tab-network');
    if (networkTab && window.__devtool_api) {
      var failedCalls = window.__devtool_api.getFailedCalls().length;
      updateTabBadge(networkTab, failedCalls, failedCalls > 0 ? 'red' : null);
    }

    // Update history tab badge (show count of recent events)
    var historyTab = document.getElementById('__devtool-tab-history');
    if (historyTab) {
      var recentEvents = store.history.val.filter(function(e) {
        return (Date.now() - e.timestamp) < 30000; // Last 30 seconds
      }).length;
      updateTabBadge(historyTab, recentEvents > 0 ? recentEvents : null, recentEvents > 0 ? null : null);
    }

    // Update performance tab badge
    var perfTab = document.getElementById('__devtool-tab-performance');
    if (perfTab && window.__devtool_mutations) {
      var rateStats = window.__devtool_mutations.getRateStats([5000]);
      if (rateStats && rateStats[5000]) {
        var rate = rateStats[5000].rate;
        var color = rate > 50 ? 'red' : (rate > 20 ? 'yellow' : 'green');
        updateTabBadge(perfTab, '●', color);
      }
    }
  }

  function updateTabBadge(tabElement, content, color) {
    // Remove existing badge
    var existing = tabElement.querySelector('[data-badge]');
    if (existing) {
      existing.remove();
    }

    if (!content) return;

    var badge = document.createElement('span');
    badge.setAttribute('data-badge', 'true');
    badge.style.cssText = STYLES.tabBadge;

    if (color === 'red') {
      badge.style.cssText += ';' + STYLES.tabBadgeRed;
    } else if (color === 'yellow') {
      badge.style.cssText += ';' + STYLES.tabBadgeYellow;
    } else if (color === 'green') {
      badge.style.cssText += ';' + STYLES.tabBadgeGreen;
    }

    badge.textContent = content;
    tabElement.appendChild(badge);
  }

  function updateActiveTabContent() {
    // Only update non-compose tabs (compose is static)
    if (state.activeTab === 'compose') return;

    // Just refresh the store data - VanJS will automatically update the DOM
    switch (state.activeTab) {
      case 'overview':
        refreshOverviewData();
        break;
      case 'errors':
        refreshErrorsData();
        break;
      case 'network':
        refreshNetworkData();
        break;
      case 'performance':
        refreshPerformanceData();
        break;
      case 'interactions':
        refreshInteractionsData();
        break;
      case 'history':
        // Force re-render to update relative timestamps
        store.history.val = store.history.val.slice();
        break;
      // other tabs have no data to refresh
    }
  }

  function renderComposeTab(container) {
    // Compose area (exact copy of original)
    var compose = document.createElement('div');
    compose.style.cssText = 'padding: 0;'; // Remove extra padding since tab content already has padding

    // Message card (groups message + attachments - Gestalt: Common Region)
    var card = document.createElement('div');
    card.id = '__devtool-card';
    card.style.cssText = STYLES.messageCard;

    var textarea = document.createElement('textarea');
    textarea.id = '__devtool-message';
    textarea.style.cssText = STYLES.textarea;
    textarea.placeholder = 'Describe what you need help with... (Ctrl+Enter to send)';
    textarea.value = store.message.val;
    textarea.onfocus = function() {
      card.style.cssText = STYLES.messageCard + ';' + STYLES.messageCardFocused;
    };
    textarea.onblur = function() {
      card.style.cssText = STYLES.messageCard;
    };
    // Auto-expand textarea based on content
    textarea.oninput = function() {
      store.message.val = textarea.value;
      var previousHeight = state.panel ? state.panel.offsetHeight : 0;
      textarea.style.height = 'auto';
      textarea.style.height = Math.min(Math.max(textarea.scrollHeight, 80), 200) + 'px';
      // Reposition panel to expand upward
      if (state.panel && state.isExpanded) {
        requestAnimationFrame(function() {
          var newHeight = state.panel.offsetHeight;
          var heightDiff = newHeight - previousHeight;
          if (heightDiff !== 0) {
            var currentTop = parseInt(state.panel.style.top) || 0;
            state.panel.style.top = (currentTop - heightDiff) + 'px';
          }
        });
      }
    };
    // Ctrl+Enter to send
    textarea.onkeydown = function(e) {
      if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
        e.preventDefault();
        handleSend();
      }
    };
    card.appendChild(textarea);

    // Attachment chips container - REACTIVE with VanJS
    // This automatically updates when store.attachments changes
    van.add(card, AttachmentAreaComponent());

    compose.appendChild(card);
    container.appendChild(compose);

    // Toolbar with actions
    var toolbar = document.createElement('div');
    toolbar.style.cssText = STYLES.toolbar;

    // Action buttons container (wraps for responsive layout)
    var actionsContainer = document.createElement('div');
    actionsContainer.style.cssText = STYLES.toolbarActions;

    // Tool buttons (Gestalt: Similarity - all secondary actions look alike)
    var screenshotBtn = createToolBtn('Screenshot', ICONS.screenshot, startScreenshotMode);
    var elementBtn = createToolBtn('Element', ICONS.element, startElementMode);
    var sketchBtn = createToolBtn('Sketch', ICONS.sketch, openSketch);
    var designBtn = createToolBtn('Design', ICONS.design, startDesignMode);
    state.inspectBtn = createToolBtn('Inspect', ICONS.inspect, startInspectMode);
    state.inspectBtn.title = 'Live style editor \u2014 inspect and edit CSS';
    // Override hover handlers to preserve active state
    state.inspectBtn.onmouseleave = function() {
      var active = window.__devtool_style_editor && window.__devtool_style_editor.isOpen();
      if (!active) {
        state.inspectBtn.style.background = 'transparent';
        state.inspectBtn.style.borderColor = TOKENS.colors.border;
        state.inspectBtn.style.color = TOKENS.colors.textMuted;
      }
    };
    var auditDropdown = createActionsDropdown();

    actionsContainer.appendChild(screenshotBtn);
    actionsContainer.appendChild(elementBtn);
    actionsContainer.appendChild(sketchBtn);
    actionsContainer.appendChild(designBtn);
    actionsContainer.appendChild(state.inspectBtn);
    actionsContainer.appendChild(auditDropdown);
    toolbar.appendChild(actionsContainer);

    // Send button (visual hierarchy - primary action, auto-pushed right via margin-left: auto)
    var sendBtn = document.createElement('button');
    sendBtn.style.cssText = STYLES.sendBtn;
    sendBtn.innerHTML = ICONS.send + ' Send';
    sendBtn.title = 'Send message (Ctrl+Enter)';
    sendBtn.onclick = handleSend;
    sendBtn.onmouseenter = function() { sendBtn.style.background = TOKENS.colors.primaryDark; };
    sendBtn.onmouseleave = function() { sendBtn.style.background = TOKENS.colors.primary; };
    toolbar.appendChild(sendBtn);

    container.appendChild(toolbar);
  }

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

  function createToolBtn(label, icon, onClick) {
    var btn = document.createElement('button');
    btn.style.cssText = STYLES.toolBtn;
    btn.innerHTML = icon + ' ' + label;
    btn.onclick = onClick;
    btn.onmouseenter = function() {
      btn.style.background = TOKENS.colors.surface;
      btn.style.borderColor = TOKENS.colors.primary;
      btn.style.color = TOKENS.colors.primary;
    };
    btn.onmouseleave = function() {
      btn.style.background = 'transparent';
      btn.style.borderColor = TOKENS.colors.border;
      btn.style.color = TOKENS.colors.textMuted;
    };
    return btn;
  }

  // Audit actions configuration
  var AUDIT_ACTIONS = [
    // Quality Audits
    {
      id: 'fullAudit',
      label: 'Full Page Audit',
      description: 'Comprehensive quality audit with grade (A-F)',
      async: true,
      fn: function() {
        if (window.__devtool && window.__devtool.auditPageQuality) {
          return window.__devtool.auditPageQuality();
        }
        return Promise.resolve({ error: 'Page quality audit not available' });
      }
    },
    {
      id: 'accessibility',
      label: 'Accessibility',
      description: 'Check for a11y issues (WCAG)',
      fn: function() {
        if (window.__devtool_accessibility) {
          return window.__devtool_accessibility.auditAccessibility();
        }
        return { error: 'Accessibility module not loaded' };
      }
    },
    {
      id: 'security',
      label: 'Security',
      description: 'Mixed content, XSS risks, noopener',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditSecurity();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'seo',
      label: 'SEO / Meta',
      description: 'Meta tags, headings, structure',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditPageQuality();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    // Layout & Visual
    {
      id: 'layoutIssues',
      label: 'Layout Issues',
      description: 'Overflows, z-index, offscreen elements',
      fn: function() {
        if (window.__devtool && window.__devtool.diagnoseLayout) {
          return window.__devtool.diagnoseLayout();
        }
        return { error: 'Layout diagnostics not available' };
      }
    },
    {
      id: 'textFragility',
      label: 'Text Fragility',
      description: 'Truncation, overflow, font issues',
      fn: function() {
        if (window.__devtool && window.__devtool.checkTextFragility) {
          return window.__devtool.checkTextFragility();
        }
        return { error: 'Text fragility check not available' };
      }
    },
    {
      id: 'responsiveRisk',
      label: 'Responsive Risk',
      description: 'Elements that may break at different sizes',
      fn: function() {
        if (window.__devtool && window.__devtool.checkResponsiveRisk) {
          return window.__devtool.checkResponsiveRisk();
        }
        return { error: 'Responsive risk check not available' };
      }
    },
    // Debug Context
    {
      id: 'lastClick',
      label: 'Last Click Context',
      description: 'What the user just clicked + mouse trail',
      fn: function() {
        if (window.__devtool_interactions) {
          return window.__devtool_interactions.getLastClickContext();
        }
        return { error: 'Interaction tracking not available' };
      }
    },
    {
      id: 'recentMutations',
      label: 'Recent DOM Changes',
      description: 'What changed in the DOM recently',
      fn: function() {
        if (window.__devtool_mutations) {
          return {
            added: window.__devtool_mutations.getAdded(Date.now() - 30000),
            removed: window.__devtool_mutations.getRemoved(Date.now() - 30000),
            modified: window.__devtool_mutations.getModified(Date.now() - 30000)
          };
        }
        return { error: 'Mutation tracking not available' };
      }
    },
    // State Capture
    {
      id: 'captureState',
      label: 'Browser State',
      description: 'localStorage, sessionStorage, cookies',
      fn: function() {
        if (window.__devtool_capture) {
          return window.__devtool_capture.captureState(['localStorage', 'sessionStorage', 'cookies']);
        }
        return { error: 'State capture not available' };
      }
    },
    {
      id: 'networkSummary',
      label: 'Network/Resources',
      description: 'Resource timing and loading data',
      fn: function() {
        if (window.__devtool_capture) {
          return window.__devtool_capture.captureNetwork();
        }
        return { error: 'Network capture not available' };
      }
    },
    // Technical
    {
      id: 'domComplexity',
      label: 'DOM Complexity',
      description: 'Node count, depth, performance impact',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditDOMComplexity();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'css',
      label: 'CSS Quality',
      description: 'Inline styles, !important usage',
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditCSS();
        }
        return { error: 'Audit module not loaded' };
      }
    },
    {
      id: 'performance',
      label: 'Performance',
      description: 'Resource timing, render-blocking, layout thrash',
      async: true,
      fn: function() {
        if (window.__devtool_audit) {
          return window.__devtool_audit.auditPerformance();
        }
        return { error: 'Audit module not loaded' };
      }
    }
  ];

  // Create the Actions dropdown
  function createActionsDropdown() {
    var container = document.createElement('div');
    container.style.cssText = STYLES.dropdownContainer;

    var btn = document.createElement('button');
    btn.style.cssText = STYLES.dropdownBtn;
    btn.innerHTML = ICONS.actions + ' Audit ' + ICONS.chevronDown;
    container.appendChild(btn);

    var menu = document.createElement('div');
    menu.style.cssText = STYLES.megaMenu;
    menu.id = '__devtool-audit-menu';

    // Group actions by category
    var sections = [
      { label: 'Quality Audits', ids: ['fullAudit', 'accessibility', 'security', 'seo'] },
      { label: 'Layout & Visual', ids: ['layoutIssues', 'textFragility', 'responsiveRisk'] },
      { label: 'Debug Context', ids: ['lastClick', 'recentMutations'] },
      { label: 'State & Network', ids: ['captureState', 'networkSummary'] },
      { label: 'Technical', ids: ['domComplexity', 'css', 'performance'] }
    ];

    // Helper: build a menu item with label + description blurb
    function buildMenuItem(action) {
      var item = document.createElement('button');
      item.style.cssText = STYLES.megaMenuItem;
      var labelSpan = document.createElement('span');
      labelSpan.style.cssText = STYLES.megaMenuItemLabel;
      labelSpan.textContent = action.label;
      var descSpan = document.createElement('span');
      descSpan.style.cssText = STYLES.megaMenuItemDesc;
      descSpan.textContent = action.description;
      item.appendChild(labelSpan);
      item.appendChild(descSpan);

      item.onmouseenter = function() {
        item.style.cssText = STYLES.megaMenuItem + ';' + STYLES.megaMenuItemHover;
      };
      item.onmouseleave = function() {
        item.style.cssText = STYLES.megaMenuItem;
      };

      item.onclick = function(e) {
        e.stopPropagation();
        closeDropdown();
        runAuditAction(action);
      };

      return item;
    }

    // Top row: first 4 sections as columns
    var topSections = sections.slice(0, 4);
    var topRow = document.createElement('div');
    topRow.style.cssText = STYLES.megaMenuTopRow;

    topSections.forEach(function(section, colIndex) {
      var col = document.createElement('div');
      col.style.cssText = STYLES.megaMenuColumn;
      if (colIndex === topSections.length - 1) {
        col.style.cssText = STYLES.megaMenuColumn + ';' + STYLES.megaMenuColumnLast;
      }

      var header = document.createElement('div');
      header.style.cssText = STYLES.megaMenuColumnHeader;
      header.textContent = section.label;
      col.appendChild(header);

      section.ids.forEach(function(actionId) {
        var action = AUDIT_ACTIONS.find(function(a) { return a.id === actionId; });
        if (!action) return;
        col.appendChild(buildMenuItem(action));
      });

      topRow.appendChild(col);
    });

    menu.appendChild(topRow);

    // Bottom row: Technical section spanning full width
    var techSection = sections[4];
    var bottomRow = document.createElement('div');
    bottomRow.style.cssText = STYLES.megaMenuBottomRow;

    var techHeader = document.createElement('div');
    techHeader.style.cssText = STYLES.megaMenuTechnicalHeader;
    techHeader.textContent = techSection.label;
    bottomRow.appendChild(techHeader);

    var techGrid = document.createElement('div');
    techGrid.style.cssText = STYLES.megaMenuTechnicalGrid;

    techSection.ids.forEach(function(actionId) {
      var action = AUDIT_ACTIONS.find(function(a) { return a.id === actionId; });
      if (!action) return;
      techGrid.appendChild(buildMenuItem(action));
    });

    bottomRow.appendChild(techGrid);
    menu.appendChild(bottomRow);

    container.appendChild(menu);

    // Toggle dropdown
    var isOpen = false;

    function openDropdown() {
      isOpen = true;
      menu.style.cssText = STYLES.megaMenu + ';' + STYLES.megaMenuVisible;
      // Responsive: reduce columns on narrow viewports
      var vw = window.innerWidth;
      if (vw < 360) {
        topRow.style.gridTemplateColumns = '1fr 1fr';
        techGrid.style.gridTemplateColumns = '1fr 1fr';
      } else if (vw < 480) {
        topRow.style.gridTemplateColumns = '1fr 1fr';
      } else {
        topRow.style.gridTemplateColumns = '1fr 1fr 1fr 1fr';
        techGrid.style.gridTemplateColumns = '1fr 1fr 1fr';
      }
      btn.style.background = TOKENS.colors.surface;
      btn.style.borderColor = TOKENS.colors.primary;
      btn.style.color = TOKENS.colors.primary;
      document.addEventListener('click', handleOutsideClick);
    }

    function closeDropdown() {
      isOpen = false;
      menu.style.cssText = STYLES.megaMenu;
      btn.style.background = 'transparent';
      btn.style.borderColor = TOKENS.colors.border;
      btn.style.color = TOKENS.colors.textMuted;
      document.removeEventListener('click', handleOutsideClick);
    }

    function handleOutsideClick(e) {
      if (!container.contains(e.target)) {
        closeDropdown();
      }
    }

    btn.onclick = function(e) {
      e.stopPropagation();
      isOpen ? closeDropdown() : openDropdown();
    };

    btn.onmouseenter = function() {
      if (!isOpen) {
        btn.style.background = TOKENS.colors.surface;
        btn.style.borderColor = TOKENS.colors.primary;
        btn.style.color = TOKENS.colors.primary;
      }
    };
    btn.onmouseleave = function() {
      if (!isOpen) {
        btn.style.background = 'transparent';
        btn.style.borderColor = TOKENS.colors.border;
        btn.style.color = TOKENS.colors.textMuted;
      }
    };

    return container;
  }

  // Run an audit action and add result as attachment
  function runAuditAction(action) {
    function handleResult(result) {
      // Format summary based on result
      var summary = formatAuditSummary(action.id, result);

      // Add as attachment
      addAttachment('audit', {
        label: action.label,
        summary: summary,
        auditType: action.id,
        result: result
      });

      togglePanel(true);
    }

    try {
      var result = action.fn();

      // Handle async functions (like fullAudit)
      if (result && typeof result.then === 'function') {
        result.then(handleResult).catch(function(e) {
          handleResult({ error: e.message || 'Async audit failed' });
        });
      } else {
        handleResult(result);
      }
    } catch (e) {
      console.error('Audit failed:', e);
      handleResult({ error: e.message || 'Audit failed' });
    }
  }

  // Format a human-readable summary for audit results
  // Updated to use new action-oriented audit schema with summary, score, grade
  function formatAuditSummary(auditId, result) {
    if (!result) {
      return 'No data captured';
    }
    if (result.error) {
      return 'Error: ' + result.error;
    }

    // New schema: if result has summary field, use it directly
    if (result.summary && typeof result.summary === 'string') {
      var prefix = '';
      if (result.grade) {
        prefix = '[' + result.grade + '] ';
      } else if (result.score !== undefined) {
        prefix = '[' + result.score + '/100] ';
      }
      return prefix + result.summary;
    }

    // Legacy support for older audit formats
    switch (auditId) {
      // Quality Audits
      case 'fullAudit':
        return 'Grade: ' + (result.grade || '?') + ' (' + (result.overallScore || 0) + '/100) - ' +
               (result.criticalIssues ? result.criticalIssues.length : 0) + ' critical issues';

      case 'accessibility':
        if (result.stats) {
          // AI-optimized format uses critical/serious/moderate/minor
          if (result.stats.totalIssues !== undefined) {
            var criticalCount = (result.stats.critical || 0) + (result.stats.serious || 0);
            var otherCount = (result.stats.moderate || 0) + (result.stats.minor || 0);
            if (result.stats.totalIssues === 0) {
              return '[' + (result.grade || 'A') + '] No accessibility issues found across ' + (result.stats.rulesChecked || result.stats.passed || 0) + ' checks.';
            }
            return '[' + (result.grade || '?') + '] ' + criticalCount + ' critical accessibility error' + (criticalCount !== 1 ? 's' : '') + ' found' + (otherCount > 0 ? ': ' + otherCount + ' ' + Object.keys(result.raw?.issuesByType || {}).slice(0, 3).join(', ') : '') + '.';
          }
          // Legacy format
          return '[' + (result.grade || '?') + '] ' + result.stats.errors + ' errors, ' + result.stats.warnings + ' warnings';
        }
        return result.count + ' issue(s): ' + result.errors + ' errors, ' + result.warnings + ' warnings';

      case 'security':
        if (result.stats) {
          // AI-optimized format
          if (result.stats.totalIssues !== undefined) {
            var criticalCount = (result.stats.critical || 0);
            var errorCount = (result.stats.errors || 0);
            if (result.stats.totalIssues === 0) {
              return '[' + (result.grade || 'A') + '] No security issues found.';
            }
            return '[' + (result.grade || '?') + '] ' + criticalCount + ' critical, ' + errorCount + ' errors';
          }
          // Legacy format
          return '[' + (result.grade || '?') + '] ' + result.stats.errors + ' errors, ' + result.stats.warnings + ' warnings';
        }
        return result.count + ' issue(s): ' + result.errors + ' errors, ' + result.warnings + ' warnings';

      case 'seo':
        if (result.meta && result.meta.title) {
          return '[' + (result.grade || '?') + '] Title: "' + result.meta.title.value.substring(0, 30) + '"';
        }
        return result.count + ' issue(s) - Title: "' + (result.title || 'missing').substring(0, 30) + '"';

      // Layout & Visual
      case 'layoutIssues':
        var overflowCount = result.overflows ? result.overflows.length : 0;
        var stackingCount = result.stackingContexts ? result.stackingContexts.length : 0;
        var offscreenCount = result.offscreen ? result.offscreen.length : 0;
        return overflowCount + ' overflows, ' + stackingCount + ' z-index contexts, ' + offscreenCount + ' offscreen';

      case 'textFragility':
        if (result.summary) {
          return result.summary.total + ' issue(s): ' + result.summary.errors + ' errors, ' + result.summary.warnings + ' warnings';
        }
        return (result.issues ? result.issues.length : 0) + ' text issues found';

      case 'responsiveRisk':
        if (result.summary) {
          return result.summary.total + ' risk(s): ' + result.summary.errors + ' errors, ' + result.summary.warnings + ' warnings';
        }
        return (result.issues ? result.issues.length : 0) + ' responsive risks found';

      // Debug Context
      case 'lastClick':
        if (!result || !result.click) {
          return 'No recent click recorded';
        }
        var click = result.click;
        var target = click.target ? (click.target.selector || click.target.tag) : 'unknown';
        return 'Clicked: ' + target.substring(0, 40);

      case 'recentMutations':
        var addedCount = result.added ? result.added.length : 0;
        var removedCount = result.removed ? result.removed.length : 0;
        var modifiedCount = result.modified ? result.modified.length : 0;
        return addedCount + ' added, ' + removedCount + ' removed, ' + modifiedCount + ' modified (last 30s)';

      // State Capture
      case 'captureState':
        var localCount = result.localStorage ? Object.keys(result.localStorage).length : 0;
        var sessionCount = result.sessionStorage ? Object.keys(result.sessionStorage).length : 0;
        var cookieCount = result.cookies ? Object.keys(result.cookies).length : 0;
        return localCount + ' localStorage, ' + sessionCount + ' sessionStorage, ' + cookieCount + ' cookies';

      case 'networkSummary':
        var entries = result.entries || [];
        var totalSize = entries.reduce(function(sum, e) { return sum + (e.size || 0); }, 0);
        var totalSizeKB = Math.round(totalSize / 1024);
        return entries.length + ' resources, ' + totalSizeKB + 'KB total';

      // Technical
      case 'domComplexity':
        if (result.metrics) {
          return '[' + (result.grade || '?') + '] ' + result.metrics.totalElements + ' elements, depth ' + result.metrics.maxDepth;
        }
        var rating = result.rating || 'unknown';
        return result.totalElements + ' nodes, depth ' + result.maxDepth + ' (' + rating + ')';

      case 'css':
        if (result.metrics) {
          return '[' + (result.grade || '?') + '] ' + result.metrics.inlineStyleCount + ' inline styles, ' + result.stats.fixable + ' issues';
        }
        return result.issues.length + ' issue(s), ' + result.inlineStyleCount + ' inline styles';

      default:
        // Try to create a generic summary
        if (typeof result === 'object') {
          var keys = Object.keys(result).slice(0, 3);
          return keys.map(function(k) { return k + ': ' + JSON.stringify(result[k]).substring(0, 20); }).join(', ');
        }
        return String(result).substring(0, 100);
    }
  }

  // Format audit result as actionable markdown for AI agent
  // Prioritizes issues by severity and provides fix instructions
  function formatAuditForAgent(auditType, label, result) {
    if (!result) {
      return '**' + label + '**: No data';
    }
    if (result.error) {
      return '**' + label + '**: Error - ' + result.error;
    }

    var lines = [];
    var grade = result.grade || '?';
    var score = result.score !== undefined ? result.score + '/100' : '';

    lines.push('**' + label + '** [' + grade + (score ? ', ' + score : '') + ']');

    // Handle accessibility audit (AI-optimized format)
    if (auditType === 'accessibility' && result.raw && result.raw.issuesByType) {
      var issues = result.raw.issuesByType;
      var issueKeys = Object.keys(issues);

      if (issueKeys.length === 0) {
        lines.push('No issues found.');
        return lines.join('\n');
      }

      // Sort by impact: critical > serious > moderate > minor
      var impactOrder = { critical: 0, serious: 1, moderate: 2, minor: 3 };
      issueKeys.sort(function(a, b) {
        return (impactOrder[issues[a].impact] || 4) - (impactOrder[issues[b].impact] || 4);
      });

      lines.push('');
      lines.push('**Fix these issues:**');

      // Limit to top 5 most important
      issueKeys.slice(0, 5).forEach(function(key, idx) {
        var issue = issues[key];
        var severity = issue.impact === 'critical' || issue.impact === 'serious' ? 'ERROR' : 'WARN';
        lines.push((idx + 1) + '. **' + issue.ruleId + '** [' + severity + '] - ' + issue.count + ' instance(s)');
        lines.push('   ' + issue.message);
        if (issue.fix) {
          lines.push('   Fix: ' + issue.fix);
        }
        if (issue.examples && issue.examples.length > 0) {
          lines.push('   Target: `' + issue.examples[0].selector + '`');
        }
      });

      if (issueKeys.length > 5) {
        lines.push('');
        lines.push('_+ ' + (issueKeys.length - 5) + ' more issues_');
      }

      return lines.join('\n');
    }

    // Handle security audit
    if (auditType === 'security' && result.raw && result.raw.issuesByType) {
      var issues = result.raw.issuesByType;
      var issueKeys = Object.keys(issues);

      if (issueKeys.length === 0) {
        lines.push('No security issues found.');
        return lines.join('\n');
      }

      lines.push('');
      lines.push('**Security issues:**');

      issueKeys.slice(0, 5).forEach(function(key, idx) {
        var issueList = issues[key];
        var count = issueList.length;
        var first = issueList[0] || {};
        lines.push((idx + 1) + '. **' + key + '** - ' + count + ' instance(s)');
        if (first.message) lines.push('   ' + first.message);
        if (first.selector) lines.push('   Target: `' + first.selector + '`');
      });

      return lines.join('\n');
    }

    // Handle layout issues audit
    if (auditType === 'layoutIssues') {
      var overflows = result.overflows || [];
      var stacking = result.stackingContexts || [];
      var offscreen = result.offscreen || [];
      var totalIssues = overflows.length + stacking.length + offscreen.length;

      if (totalIssues === 0) {
        lines.push('No layout issues found.');
        return lines.join('\n');
      }

      lines.push('');

      if (overflows.length > 0) {
        lines.push('**Overflow Issues** (' + overflows.length + '):');
        overflows.slice(0, 3).forEach(function(item, idx) {
          lines.push((idx + 1) + '. `' + item.selector + '` - ' + item.type + ' overflow');
          lines.push('   Content: ' + item.scrollWidth + 'x' + item.scrollHeight + ', Container: ' + item.clientWidth + 'x' + item.clientHeight);
          lines.push('   Fix: Add overflow handling or resize container');
        });
        if (overflows.length > 3) {
          lines.push('   _+ ' + (overflows.length - 3) + ' more_');
        }
        lines.push('');
      }

      if (stacking.length > 0) {
        lines.push('**Stacking Contexts** (' + stacking.length + '):');
        stacking.slice(0, 3).forEach(function(item, idx) {
          var reasons = item.reason ? item.reason.join(', ') : 'unknown';
          lines.push((idx + 1) + '. `' + item.selector + '` - z-index: ' + item.zIndex);
          lines.push('   Reason: ' + reasons);
        });
        if (stacking.length > 3) {
          lines.push('   _+ ' + (stacking.length - 3) + ' more_');
        }
        lines.push('');
      }

      if (offscreen.length > 0) {
        lines.push('**Offscreen Elements** (' + offscreen.length + '):');
        offscreen.slice(0, 3).forEach(function(item, idx) {
          var dir = item.direction ? item.direction.join(', ') : 'unknown';
          lines.push((idx + 1) + '. `' + item.selector + '` - positioned ' + dir);
          lines.push('   Fix: Check positioning or remove hidden element');
        });
        if (offscreen.length > 3) {
          lines.push('   _+ ' + (offscreen.length - 3) + ' more_');
        }
      }

      return lines.join('\n');
    }

    // Handle other audits with fixable array
    if (result.fixable && result.fixable.length > 0) {
      lines.push('');
      lines.push('**Issues to fix:**');

      result.fixable.slice(0, 5).forEach(function(issue, idx) {
        lines.push((idx + 1) + '. **' + issue.type + '** [' + (issue.severity || 'info').toUpperCase() + ']');
        if (issue.message) lines.push('   ' + issue.message);
        if (issue.fix) lines.push('   Fix: ' + issue.fix);
        if (issue.selector) lines.push('   Target: `' + issue.selector + '`');
      });

      if (result.fixable.length > 5) {
        lines.push('');
        lines.push('_+ ' + (result.fixable.length - 5) + ' more issues_');
      }

      return lines.join('\n');
    }

    // Fallback: use summary if available
    if (result.summary) {
      lines.push(result.summary);
    } else {
      lines.push('Audit complete. Score: ' + (score || 'N/A'));
    }

    return lines.join('\n');
  }

  // Attachment preview state
  var previewState = {
    popup: null,
    highlight: null
  };

  // Show attachment preview on hover
  function showAttachmentPreview(attachment, chipRect) {
    hideAttachmentPreview();

    var popup = document.createElement('div');
    popup.id = '__devtool-attachment-preview';
    popup.style.cssText = STYLES.attachmentPreview;

    if (attachment.type === 'screenshot') {
      var info = document.createElement('div');
      info.style.cssText = STYLES.attachmentPreviewElement;
      var area = attachment.data && attachment.data.area;
      var dims = area ? (area.width + '\u00d7' + area.height) : '';
      if (attachment.filePath) {
        info.textContent = (dims ? dims + '\n' : '') + attachment.filePath;
      } else {
        info.textContent = (dims || 'Screenshot') + '\nSaving\u2026';
      }
      popup.appendChild(info);
    } else if (attachment.type === 'element' && attachment.data) {
      // Element preview - show selector and highlight the element
      var info = document.createElement('div');
      info.style.cssText = STYLES.attachmentPreviewElement;
      info.innerHTML = '<strong>' + (attachment.data.tag || 'element') + '</strong>\n' +
        (attachment.data.selector || '') + '\n\n' +
        (attachment.data.text ? '"' + attachment.data.text.substring(0, 100) + '"' : '');
      popup.appendChild(info);

      // Highlight the element on the page
      if (attachment.data.selector) {
        try {
          var el = document.querySelector(attachment.data.selector);
          if (el) {
            var rect = el.getBoundingClientRect();
            var highlight = document.createElement('div');
            highlight.id = '__devtool-preview-highlight';
            highlight.style.cssText = STYLES.elementPreviewHighlight;
            highlight.style.left = rect.left + 'px';
            highlight.style.top = rect.top + 'px';
            highlight.style.width = rect.width + 'px';
            highlight.style.height = rect.height + 'px';
            document.body.appendChild(highlight);
            previewState.highlight = highlight;
          }
        } catch (e) {
          // Invalid selector, ignore
        }
      }
    } else if (attachment.type === 'sketch' && attachment.data) {
      // Sketch preview - show thumbnail or info
      var sketchInfo = document.createElement('div');
      sketchInfo.style.cssText = STYLES.attachmentPreviewElement;
      var elements = attachment.data.elements || [];
      sketchInfo.innerHTML = '<strong>Sketch</strong>\n' +
        elements.length + ' element' + (elements.length !== 1 ? 's' : '') + '\n' +
        (attachment.summary || '');
      popup.appendChild(sketchInfo);
    } else if (attachment.type === 'style-edit' && attachment.data) {
      // Style-edit preview - show diff of original -> current values
      var styleInfo = document.createElement('div');
      styleInfo.style.cssText = STYLES.attachmentPreviewElement;
      var changes = attachment.data.changes || [];
      var lines = ['<strong>' + (attachment.data.selector || 'style-edit') + '</strong>'];
      for (var ci = 0; ci < changes.length; ci++) {
        var c = changes[ci];
        lines.push(c.property + ': ' + c.original + ' \u2192 ' + c.current);
      }
      styleInfo.innerHTML = lines.join('\n');
      popup.appendChild(styleInfo);
    } else if (attachment.type === 'audit' && attachment.data) {
      // Audit preview - show summary
      var auditInfo = document.createElement('div');
      auditInfo.style.cssText = STYLES.attachmentPreviewElement;
      auditInfo.innerHTML = '<strong>' + (attachment.data.auditType || 'Audit') + '</strong>\n' +
        (attachment.summary || '');
      popup.appendChild(auditInfo);
    } else {
      // Default: show summary
      var defaultInfo = document.createElement('div');
      defaultInfo.style.cssText = STYLES.attachmentPreviewElement;
      defaultInfo.textContent = attachment.summary || attachment.label;
      popup.appendChild(defaultInfo);
    }

    // Position the popup above the chip
    document.body.appendChild(popup);
    previewState.popup = popup;

    // Calculate position - show above chip, centered
    var popupRect = popup.getBoundingClientRect();
    var left = chipRect.left + (chipRect.width / 2) - (popupRect.width / 2);
    var top = chipRect.top - popupRect.height - 8;

    // Keep within viewport
    left = Math.max(8, Math.min(left, window.innerWidth - popupRect.width - 8));
    if (top < 8) {
      top = chipRect.bottom + 8; // Show below if not enough space above
    }

    popup.style.left = left + 'px';
    popup.style.top = top + 'px';

    // Fade in
    requestAnimationFrame(function() {
      popup.style.opacity = '1';
    });
  }

  // Hide attachment preview
  function hideAttachmentPreview() {
    if (previewState.popup) {
      previewState.popup.parentNode.removeChild(previewState.popup);
      previewState.popup = null;
    }
    if (previewState.highlight) {
      previewState.highlight.parentNode.removeChild(previewState.highlight);
      previewState.highlight = null;
    }
  }

  // Attachment chip creation
  function createChip(attachment) {
    var chip = document.createElement('div');
    chip.style.cssText = STYLES.chip;
    chip.dataset.id = attachment.id;
    chip.style.cursor = 'pointer';

    var icon = document.createElement('span');
    icon.style.cssText = STYLES.chipIcon;
    var iconSvg = ICONS.element;
    if (attachment.type === 'screenshot') iconSvg = ICONS.screenshot;
    else if (attachment.type === 'sketch') iconSvg = ICONS.sketch;
    else if (attachment.type === 'audit') iconSvg = ICONS.audit;
    else if (attachment.type === 'style-edit') iconSvg = ICONS.styleEdit;
    icon.innerHTML = iconSvg;
    chip.appendChild(icon);

    var label = document.createElement('span');
    label.style.cssText = STYLES.chipLabel;
    label.textContent = attachment.label;
    chip.appendChild(label);

    var removeBtn = document.createElement('button');
    removeBtn.style.cssText = STYLES.chipRemove;
    removeBtn.innerHTML = ICONS.x;
    removeBtn.setAttribute('aria-label', 'Remove ' + attachment.label);
    removeBtn.title = 'Remove ' + attachment.label;
    removeBtn.onclick = function(e) {
      e.stopPropagation();
      removeAttachment(attachment.id);
      hideAttachmentPreview();
    };
    removeBtn.onmouseenter = function() { removeBtn.style.color = TOKENS.colors.error; };
    removeBtn.onmouseleave = function() { removeBtn.style.color = TOKENS.colors.textMuted; };
    chip.appendChild(removeBtn);

    // Hover preview handlers
    chip.onmouseenter = function() {
      var rect = chip.getBoundingClientRect();
      showAttachmentPreview(attachment, rect);
    };
    chip.onmouseleave = function() {
      hideAttachmentPreview();
    };

    return chip;
  }

  // addAttachment - now uses reactive store (VanJS)
  // DOM updates automatically via AttachmentAreaComponent
  function addAttachment(type, data) {
    return actions.addAttachment(type, data);

    return attachment.id;
  }

  // removeAttachment - now uses reactive store (VanJS)
  // DOM updates automatically via AttachmentAreaComponent
  function removeAttachment(id) {
    hideAttachmentPreview();
    actions.removeAttachment(id);
  }

  // clearAttachments - now uses reactive store (VanJS)
  function clearAttachments() {
    hideAttachmentPreview();
    actions.clearAttachments();
  }

  // Send message - assembles everything into a structured message
  function handleSend() {
    var textarea = document.getElementById('__devtool-message');
    var userMessage = textarea ? textarea.value.trim() : '';

    if (!userMessage && state.attachments.length === 0) return;

    // Build the structured message
    var parts = [];

    // User's message first
    if (userMessage) {
      parts.push(userMessage);
    }

    // Add context section if there are attachments
    if (state.attachments.length > 0) {
      parts.push('');
      parts.push('---');
      parts.push('**Context from page:** ' + window.location.href);
      parts.push('');

      state.attachments.forEach(function(att) {
        if (att.type === 'screenshot') {
          var desc = '- Screenshot `' + att.id + '`';
          if (att.filePath) {
            desc += ' (`' + att.filePath + '`)';
          }
          desc += ': ' + att.summary;
          parts.push(desc);
        } else if (att.type === 'element') {
          parts.push('- Element `' + att.id + '`: `' + att.data.selector + '` (' + att.data.tag + ')');
        } else if (att.type === 'sketch') {
          parts.push('- Sketch `' + att.id + '`: ' + att.summary);
        } else if (att.type === 'style-edit') {
          var seChanges = att.data && att.data.changes || [];
          var seSelector = att.data && att.data.selector || '';
          var seHeader = '- Style edit `' + seSelector + '`: ' + seChanges.length + ' CSS change' + (seChanges.length !== 1 ? 's' : '');
          if (att.data && att.data.reactProps && att.data.reactProps.component) {
            seHeader += ', React ' + att.data.reactProps.component + ' props attached';
          }
          parts.push(seHeader);
          for (var sci = 0; sci < seChanges.length; sci++) {
            var sc = seChanges[sci];
            parts.push('  ' + sc.property + ': ' + sc.original + ' \u2192 ' + sc.current + ' (' + sc.scope + ')');
          }
          if (att.data && att.data.screenshots) {
            var seBefore = att.data.screenshots.before;
            var seAfter = att.data.screenshots.after;
            if (seBefore || seAfter) {
              var ssIds = [];
              if (seBefore) ssIds.push('`' + seBefore + '`');
              if (seAfter) ssIds.push('`' + seAfter + '`');
              parts.push('  Before/after screenshots: ' + ssIds.join(', '));
            }
          }
        } else if (att.type === 'audit') {
          // Format audit result as actionable markdown summary
          parts.push('');
          parts.push(formatAuditForAgent(att.data.auditType, att.data.label, att.data.result));
        }
      });

      parts.push('');
      parts.push('*Use `proxy exec` to inspect or interact with the page.*');
    }

    var fullMessage = parts.join('\n');

    // Send via panel_message
    core.send('panel_message', {
      timestamp: Date.now(),
      payload: {
        message: fullMessage,
        attachments: state.attachments.map(function(a) {
          // For screenshots, omit binary area data — already sent via screenshot_capture event.
          // For other types (audit, element, sketch), include data payload used by the overlay.
          var area = a.data && a.data.area;
          var att = {
            id: a.id,
            type: a.type,
            selector: a.data && a.data.selector,
            tag: a.data && a.data.tag,
            text: a.data && a.data.text,
            summary: a.summary,
            filePath: a.filePath || null
          };
          if (a.type === 'screenshot') {
            att.area = area ? { x: area.x, y: area.y, width: area.width, height: area.height } : null;
          } else {
            att.area = area || null;
            att.data = a.data;
          }
          return att;
        }),
        url: window.location.href,
        request_notification: state.requestNotification
      }
    });

    // Clear
    if (textarea) textarea.value = '';
    store.message.val = '';
    clearAttachments();
    togglePanel(false);
  }

  // Capture screenshot area using html2canvas.
  // Calls back with an ArrayBuffer of raw PNG bytes (no base64 encoding).
  function captureArea(x, y, w, h, callback) {
    if (typeof html2canvas === 'undefined') {
      console.error('[DevTool] html2canvas not loaded for screenshot capture');
      callback(null);
      return;
    }

    html2canvas(document.body, {
      allowTaint: true,
      useCORS: true,
      logging: false,
      x: x,
      y: y,
      width: w,
      height: h,
      scrollX: 0,
      scrollY: 0,
      windowWidth: document.documentElement.scrollWidth,
      windowHeight: document.documentElement.scrollHeight
    }).then(function(canvas) {
      canvas.toBlob(function(blob) {
        if (!blob) { callback(null); return; }
        blob.arrayBuffer().then(function(buf) {
          callback(buf);
        }).catch(function() { callback(null); });
      }, 'image/png');
    }).catch(function(err) {
      console.error('[DevTool] Screenshot capture failed:', err);
      callback(null);
    });
  }

  // Send raw PNG bytes as a binary WebSocket frame.
  // Frame format: [1 byte: idLen][idLen bytes: id][PNG bytes]
  function sendCaptureBinary(id, arrayBuffer) {
    var ws = core.ws && core.ws();
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    var idBytes = new TextEncoder().encode(id);
    var frame = new Uint8Array(1 + idBytes.length + arrayBuffer.byteLength);
    frame[0] = idBytes.length;
    frame.set(idBytes, 1);
    frame.set(new Uint8Array(arrayBuffer), 1 + idBytes.length);
    ws.send(frame.buffer);
  }

  // Screenshot mode
  function startScreenshotMode() {
    // Hide panel immediately without animation so overlay is the topmost interactive layer
    state.panel.style.display = 'none';
    state.isExpanded = false;
    if (state.tabUpdateInterval) {
      clearInterval(state.tabUpdateInterval);
      state.tabUpdateInterval = null;
    }

    // Detect responsive mode or touch device
    var isResponsive = window.innerWidth < 768; // Common tablet breakpoint
    var isTouchDevice = 'ontouchstart' in window || navigator.maxTouchPoints > 0;
    var isDragSelectUnavailable = isResponsive || isTouchDevice;

    // Hide indicator during capture to avoid including it in screenshots
    function hideIndicatorForCapture() {
      if (state.container) state.container.style.display = 'none';
    }
    function restoreIndicator() {
      if (state.container && state.isVisible) state.container.style.display = 'block';
    }

    // If drag select is not available, capture full screen immediately
    if (isDragSelectUnavailable) {
      var w = window.innerWidth;
      var h = window.innerHeight;
      hideIndicatorForCapture();
      captureArea(0, 0, w, h, function(imageBuffer) {
        restoreIndicator();
        addAttachment('screenshot', {
          label: 'Full screen (' + w + '\u00d7' + h + ')',
          summary: 'Full screen screenshot ' + w + 'x' + h,
          area: { x: 0, y: 0, width: w, height: h },
          imageBuffer: imageBuffer
        });
        togglePanel(true);
      });
      return;
    }

    // Desktop mode: show drag selection overlay
    var overlay = document.createElement('div');
    overlay.style.cssText = STYLES.overlay;

    var box = document.createElement('div');
    box.style.cssText = STYLES.selectionBox;
    box.style.display = 'none';
    overlay.appendChild(box);

    var instructions = document.createElement('div');
    instructions.style.cssText = STYLES.instructionBar;
    instructions.textContent = 'Click and drag to select area \u2022 ESC to cancel';
    overlay.appendChild(instructions);

    var start = null;

    // Block all events from reaching the page underneath (capture phase)
    function block(e) {
      e.preventDefault();
      e.stopPropagation();
      e.stopImmediatePropagation();
    }

    var blockTypes = ['mousedown', 'mouseup', 'mousemove', 'click', 'pointerdown', 'pointerup', 'pointermove', 'contextmenu'];
    blockTypes.forEach(function(type) {
      overlay.addEventListener(type, block, true);
    });

    overlay.addEventListener('mousedown', function(e) {
      start = { x: e.clientX, y: e.clientY };
      box.style.display = 'block';
      box.style.left = start.x + 'px';
      box.style.top = start.y + 'px';
      box.style.width = '0';
      box.style.height = '0';
    }, true);

    overlay.addEventListener('mousemove', function(e) {
      if (!start) return;
      var x = Math.min(start.x, e.clientX);
      var y = Math.min(start.y, e.clientY);
      var w = Math.abs(e.clientX - start.x);
      var h = Math.abs(e.clientY - start.y);
      box.style.left = x + 'px';
      box.style.top = y + 'px';
      box.style.width = w + 'px';
      box.style.height = h + 'px';
    }, true);

    overlay.addEventListener('mouseup', function(e) {
      if (!start) return;
      var x = Math.min(start.x, e.clientX);
      var y = Math.min(start.y, e.clientY);
      var w = Math.abs(e.clientX - start.x);
      var h = Math.abs(e.clientY - start.y);

      cleanup();

      if (w > 20 && h > 20) {
        // Hide indicator before capture to avoid including it in screenshot
        hideIndicatorForCapture();
        var absX = x + window.scrollX;
        var absY = y + window.scrollY;
        captureArea(absX, absY, w, h, function(imageBuffer) {
          restoreIndicator();
          addAttachment('screenshot', {
            label: w + '\u00d7' + h + ' area',
            summary: 'Screenshot area at (' + x + ',' + y + ') size ' + w + 'x' + h,
            area: { x: absX, y: absY, width: w, height: h },
            imageBuffer: imageBuffer
          });
          togglePanel(true);
        });
      } else {
        togglePanel(true);
      }
    }, true);

    // Prevent clicks on the instruction bar from starting a selection
    instructions.addEventListener('mousedown', function(e) { e.stopPropagation(); }, true);

    function cleanup() {
      document.removeEventListener('keydown', onKey);
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
    }

    function onKey(e) {
      if (e.key === 'Escape') {
        cleanup();
        togglePanel(true);
      }
    }
    document.addEventListener('keydown', onKey);

    document.body.appendChild(overlay);
  }

  // Element selection mode
  function startElementMode() {
    togglePanel(false);

    var overlay = document.createElement('div');
    overlay.style.cssText = STYLES.overlay;

    var highlight = document.createElement('div');
    highlight.style.cssText = STYLES.elementHighlight;
    highlight.style.display = 'none';
    overlay.appendChild(highlight);

    var tooltip = document.createElement('div');
    tooltip.style.cssText = STYLES.tooltip;
    tooltip.style.display = 'none';
    overlay.appendChild(tooltip);

    var instructions = document.createElement('div');
    instructions.style.cssText = STYLES.instructionBar;
    instructions.textContent = 'Click an element to select \u2022 ESC to cancel';
    overlay.appendChild(instructions);

    var hovered = null;

    overlay.onmousemove = function(e) {
      overlay.style.pointerEvents = 'none';
      var el = document.elementFromPoint(e.clientX, e.clientY);
      overlay.style.pointerEvents = 'auto';

      if (!el || el === state.container || state.container.contains(el)) {
        highlight.style.display = 'none';
        tooltip.style.display = 'none';
        hovered = null;
        return;
      }

      hovered = el;
      var rect = el.getBoundingClientRect();

      highlight.style.display = 'block';
      highlight.style.left = rect.left + 'px';
      highlight.style.top = rect.top + 'px';
      highlight.style.width = rect.width + 'px';
      highlight.style.height = rect.height + 'px';

      var selector = utils.generateSelector(el);
      tooltip.textContent = selector;
      tooltip.style.display = 'block';
      tooltip.style.left = Math.min(rect.left, window.innerWidth - 200) + 'px';
      tooltip.style.top = Math.max(rect.top - 28, 5) + 'px';
    };

    overlay.onclick = function(e) {
      e.preventDefault();
      e.stopPropagation();
      cleanup();

      if (hovered) {
        var selector = utils.generateSelector(hovered);
        var tag = hovered.tagName.toLowerCase();
        var text = (hovered.textContent || '').trim().substring(0, 100);
        var computed = window.getComputedStyle(hovered);

        var meta = {
          label: selector.length > 30 ? tag + (hovered.id ? '#' + hovered.id : '') : selector,
          summary: selector + ' - "' + text.substring(0, 50) + '"',
          selector: selector,
          tag: tag,
          id: hovered.id || null,
          classes: Array.from(hovered.classList),
          text: text,
          rect: hovered.getBoundingClientRect()
        };

        // Semantic attributes
        var attrs = ['role', 'aria-label', 'name', 'type', 'href', 'data-testid', 'data-test-id', 'placeholder', 'title', 'alt'];
        attrs.forEach(function(a) { var v = hovered.getAttribute(a); if (v) meta[a.replace(/-/g, '_')] = v; });

        // Framework detection
        var reactKey = Object.keys(hovered).find(function(k) { return k.startsWith('__reactFiber$') || k.startsWith('__reactInternalInstance$'); });
        if (reactKey) {
          var fiber = hovered[reactKey];
          if (fiber && fiber.type) {
            meta.framework = 'react';
            meta.component = typeof fiber.type === 'function' ? (fiber.type.displayName || fiber.type.name || 'Anonymous') : String(fiber.type);
          }
        }
        if (hovered.__vue__) { meta.framework = 'vue'; meta.component = hovered.__vue__.$options.name || 'Anonymous'; }
        if (hovered.__svelte_meta) { meta.framework = 'svelte'; }

        // Layout-relevant computed styles
        if (computed.display !== 'block' && computed.display !== 'inline') meta.display = computed.display;
        if (computed.position !== 'static') meta.position = computed.position;
        if (computed.overflow !== 'visible') meta.overflow = computed.overflow;

        // Event listeners (Chrome DevTools API only)
        if (typeof getEventListeners === 'function') {
          try { var ls = getEventListeners(hovered); var lt = Object.keys(ls); if (lt.length) meta.listeners = lt; } catch (e) {}
        }

        addAttachment('element', meta);
      }

      togglePanel(true);
    };

    function cleanup() {
      document.removeEventListener('keydown', onKey);
      if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
    }

    function onKey(e) {
      if (e.key === 'Escape') {
        cleanup();
        togglePanel(true);
      }
    }
    document.addEventListener('keydown', onKey);

    document.body.appendChild(overlay);
  }

  // Sketch mode - opens sketch, on save adds as attachment
  function openSketch() {
    togglePanel(false);
    if (window.__devtool_sketch) {
      // Set callback for when sketch is saved
      window.__devtool_sketch.onSave = function(sketchData) {
        // Use reactive addAttachment - DOM updates automatically
        addAttachment('sketch', {
          label: sketchData.elementCount + ' elements',
          summary: 'Sketch with ' + sketchData.elementCount + ' elements',
          elements: sketchData.elements,
          elementCount: sketchData.elementCount
        });

        togglePanel(true);
      };

      window.__devtool_sketch.toggle();
    }
  }

  // Design mode - start design iteration for an element
  function startDesignMode() {
    togglePanel(false);
    if (window.__devtool_design) {
      window.__devtool_design.start();
    } else {
      console.error('[Indicator] Design module not loaded');
      togglePanel(true);
    }
  }

  // Inspect mode - open style editor for hover-to-select
  function startInspectMode() {
    if (window.__devtool_style_editor) {
      window.__devtool_style_editor.open();
    } else {
      console.error('[Indicator] Style editor module not loaded');
    }
  }

  // Panel toggle
  function togglePanel(show) {
    var shouldShow = show !== undefined ? show : !state.isExpanded;
    state.isExpanded = shouldShow;

    if (shouldShow) {
      updatePanelPosition();
      state.panel.style.display = 'flex'; // Changed to flex for column layout
      // Re-render active tab now that panel is visible
      switchTab(state.activeTab);
      requestAnimationFrame(function() {
        state.panel.style.opacity = '1';
        state.panel.style.transform = 'translateY(0)';
      });
    } else {
      state.panel.style.opacity = '0';
      state.panel.style.transform = 'translateY(8px)';
      setTimeout(function() { state.panel.style.display = 'none'; }, 200);
      // Stop tab updates
      if (state.tabUpdateInterval) {
        clearInterval(state.tabUpdateInterval);
        state.tabUpdateInterval = null;
      }
    }
  }

  function updatePanelPosition() {
    if (!state.panel || !state.bug) return;
    var rect = state.bug.getBoundingClientRect();
    var panelH = state.panel.offsetHeight || 300;

    var x = rect.left;
    var y = rect.top - panelH - 12;

    if (x + 380 > window.innerWidth) x = window.innerWidth - 390;
    if (x < 10) x = 10;
    if (y < 10) y = rect.bottom + 12;

    state.panel.style.left = x + 'px';
    state.panel.style.top = y + 'px';
  }

  // Drag handling
  function handleDragStart(e) {
    if (e.button !== 0) return;

    var startX = e.clientX;
    var startY = e.clientY;
    var startPos = { x: state.position.x, y: state.position.y };
    var dragged = false;

    function onMove(e) {
      var dx = e.clientX - startX;
      var dy = e.clientY - startY;

      if (Math.abs(dx) > 5 || Math.abs(dy) > 5) dragged = true;

      if (dragged) {
        state.isDragging = true;
        var x = startPos.x + dx;
        var y = startPos.y - dy;

        x = Math.max(0, Math.min(x, window.innerWidth - 52));
        y = Math.max(0, Math.min(y, window.innerHeight - 52));

        state.position = { x: x, y: y };
        state.bug.style.left = x + 'px';
        state.bug.style.bottom = y + 'px';
        updatePanelPosition();
        positionSparkline();
      }
    }

    function onUp() {
      document.removeEventListener('mousemove', onMove);
      document.removeEventListener('mouseup', onUp);

      if (dragged) {
        savePrefs();
        setTimeout(function() { state.isDragging = false; }, 0);
      } else {
        togglePanel();
      }
    }

    document.addEventListener('mousemove', onMove);
    document.addEventListener('mouseup', onUp);
  }

  // Status polling and message handling
  function setupStatusPolling() {
    setInterval(function() {
      var dot = document.getElementById('__devtool-status');
      if (dot) {
        dot.style.backgroundColor = core.isConnected() ? TOKENS.colors.success : TOKENS.colors.error;
      }
      // Update inspect button active state
      if (state.inspectBtn) {
        var active = window.__devtool_style_editor && window.__devtool_style_editor.isOpen();
        if (active) {
          state.inspectBtn.style.background = TOKENS.colors.surface;
          state.inspectBtn.style.borderColor = TOKENS.colors.primary;
          state.inspectBtn.style.color = TOKENS.colors.primary;
        } else {
          state.inspectBtn.style.background = 'transparent';
          state.inspectBtn.style.borderColor = TOKENS.colors.border;
          state.inspectBtn.style.color = TOKENS.colors.textMuted;
        }
      }
    }, 1000);

    // Register message handler for activity state
    if (core && core.onMessage) {
      core.onMessage(handleMessage);
    }
  }

  // Handle incoming WebSocket messages
  function handleMessage(message) {
    if (message.type === 'activity') {
      var payload = message.payload || message;
      var active = payload.active === true;
      setActivityState(active);
      if (active) {
        showMicroToast('\u26a1 Working...', TOKENS.colors.active);
        logHistoryEvent('system', 'Agent active', '');
      } else {
        showMicroToast('\u2713 Idle', TOKENS.colors.success);
        logHistoryEvent('system', 'Agent idle', '');
      }
    } else if (message.type === 'output_preview') {
      var payload = message.payload || message;
      if (payload.lines && Array.isArray(payload.lines)) {
        showOutputPreview(payload.lines);
      }
    } else if (message.type === 'tool_event') {
      // Tool call from the AI agent
      var payload = message.payload || message;
      var toolName = payload.name || 'unknown';
      var toolAction = payload.action || 'call';
      if (toolAction === 'error') {
        showMicroToast('\u2718 ' + toolName, TOKENS.colors.error);
        logHistoryEvent('error', 'Tool error: ' + toolName, payload.detail || '');
      } else {
        showMicroToast('\u2699 ' + toolName, TOKENS.colors.primary);
        logHistoryEvent('tool', toolName, payload.detail || '');
      }
    } else if (message.type === 'execute') {
      showMicroToast('\u25b6 exec', TOKENS.colors.secondary);
      logHistoryEvent('tool', 'Proxy exec', (message.code || '').substring(0, 80));
    } else if (message.type === 'proxy_diagnostic') {
      var payload = message.payload || message;
      var level = payload.level || 'info';
      var diagMsg = payload.message || 'diagnostic';
      var color = level === 'error' ? TOKENS.colors.error : TOKENS.colors.active;
      showMicroToast((level === 'error' ? '\u2718 ' : '\u26a0 ') + diagMsg.substring(0, 30), color);
      logHistoryEvent(level === 'error' ? 'error' : 'system', diagMsg, '');
    } else if (message.type === 'capture_ack' && message.id && message.file_path) {
      // Server confirmed screenshot saved
      store.attachments.val = store.attachments.val.map(function(a) {
        if (a.id !== message.id) return a;
        return Object.assign({}, a, { filePath: message.file_path });
      });
      state.attachments = store.attachments.val;
      showMicroToast('\ud83d\udcf7 Screenshot saved', TOKENS.colors.success);
      logHistoryEvent('screenshot', 'Screenshot captured', message.file_path);
    }
  }

  // Preferences
  function savePrefs() {
    try {
      localStorage.setItem('__devtool_prefs', JSON.stringify({
        position: state.position,
        isVisible: state.isVisible
      }));
    } catch (e) {}
  }

  function loadPrefs() {
    try {
      var saved = localStorage.getItem('__devtool_prefs');
      if (saved) {
        var prefs = JSON.parse(saved);
        if (prefs.position) state.position = prefs.position;
        if (typeof prefs.isVisible === 'boolean') state.isVisible = prefs.isVisible;
      }
    } catch (e) {}
  }

  // Public API
  function show() {
    if (state.container) {
      state.container.style.display = 'block';
      state.isVisible = true;
      savePrefs();
    }
  }

  function hide() {
    if (state.container) {
      state.container.style.display = 'none';
      state.isVisible = false;
      savePrefs();
    }
  }

  function toggle() {
    state.isVisible ? hide() : show();
  }

  function destroy() {
    if (state.container && state.container.parentNode) {
      state.container.parentNode.removeChild(state.container);
    }
    state.container = null;
    state.bug = null;
    state.panel = null;
  }

  // Init on ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  // Export
  window.__devtool_indicator = {
    init: init,
    show: show,
    hide: hide,
    toggle: toggle,
    destroy: destroy,
    togglePanel: togglePanel,
    switchTab: switchTab,
    setMessage: actions.setMessage,
    setActivityState: setActivityState,
    addAttachment: addAttachment,
    showMicroToast: showMicroToast,
    logHistoryEvent: logHistoryEvent,
    state: state
  };
})();

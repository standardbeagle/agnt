// Floating Indicator — reactive store, data refresh, and chaos client.
// Split from indicator.js; shares symbols with the other indicator-*
// modules via the window.__devtool_indicator_internal namespace.
// Chrome-shell functions (I.showMicroToast, I.switchTab, ...) are
// assigned by indicator.js and only invoked at runtime, after every
// indicator-* module has evaluated.

(function() {
  'use strict';

  var I = window.__devtool_indicator_internal;

  // Shared design tokens from indicator-styles.js (loads earlier).
  var TOKENS = I.TOKENS;

  // VanJS 1.6.0 minified (~1.5KB) - Reactive UI framework
  // https://vanjs.org - MIT License
  // prettier-ignore
  {let e,t,r,o,n,s,l,i,f,h,w,a,u,d,c,_,S,g,y,b,m,v,j,x,O;l=Object.getPrototypeOf,f={},h=l(i={isConnected:1}),w=l(l),a=(e,t,r,o)=>(e??(o?setTimeout(r,o):queueMicrotask(r),new Set)).add(t),u=(e,t,o)=>{let n=r;r=t;try{return e(o)}catch(e){return console.error(e),o}finally{r=n}},d=e=>e.filter(e=>e.t?.isConnected),c=e=>n=a(n,e,()=>{for(let e of n)e.o=d(e.o),e.l=d(e.l);n=s},1e3),_={get val(){return r?.i?.add(this),this.rawVal},get oldVal(){return r?.i?.add(this),this.h},set val(o){r?.u?.add(this),o!==this.rawVal&&(this.rawVal=o,this.o.length+this.l.length?(t?.add(this),e=a(e,this,x)):this.h=o)}},S=e=>({__proto__:_,rawVal:e,h:e,o:[],l:[]}),g=(e,t)=>{let r={i:new Set,u:new Set},n={f:e},s=o;o=[];let l=u(e,r,t);l=(l??document).nodeType?l:new Text(l);for(let e of r.i)r.u.has(e)||(c(e),e.o.push(n));for(let e of o)e.t=l;return o=s,n.t=l},y=(e,t=S(),r)=>{let n={i:new Set,u:new Set},s={f:e,s:t};s.t=r??o?.push(s)??i,t.val=u(e,n,t.rawVal);for(let e of n.i)n.u.has(e)||(c(e),e.l.push(s));return t},b=(e,...t)=>{for(let r of t.flat(1/0)){let t=l(r??0),o=t===_?g(()=>r.val):t===w?g(r):r;o!=s&&e.append(o)}return e},m=(e,t,...r)=>{let[{is:o,...n},...i]=l(r[0]??0)===h?r:[{},...r],a=e?document.createElementNS(e,t,{is:o}):document.createElement(t,{is:o});for(let[e,r]of Object.entries(n)){let o=t=>t?Object.getOwnPropertyDescriptor(t,e)??o(l(t)):s,n=t+","+e,i=f[n]??=o(l(a))?.set??0,h=e.startsWith("on")?(t,r)=>{let o=e.slice(2);a.removeEventListener(o,r),a.addEventListener(o,t)}:i?i.bind(a):a.setAttribute.bind(a,e),u=l(r??0);e.startsWith("on")||u===w&&(r=y(r),u=_),u===_?g(()=>(h(r.val,r.h),a)):h(r)}return b(a,i)},v=e=>({get:(t,r)=>m.bind(s,e,r)}),j=(e,t)=>t?t!==e&&e.replaceWith(t):e.remove(),x=()=>{let r=0,o=[...e].filter(e=>e.rawVal!==e.h);do{t=new Set;for(let e of new Set(o.flatMap(e=>e.l=d(e.l))))y(e.f,e.s,e.t),e.t=s}while(++r<100&&(o=[...t]).length);let n=[...e].filter(e=>e.rawVal!==e.h);e=s;for(let e of new Set(n.flatMap(e=>e.o=d(e.o))))j(e.t,g(e.f,e.t)),e.t=s;for(let e of n)e.h=e.rawVal},O={tags:new Proxy(e=>new Proxy(m,v(e)),v()),hydrate:(e,t)=>j(e,g(t,e)),add:b,state:S,derive:y},window.van=O;}

  var van = window.van;
  var tags = van.tags;

  var core = window.__devtool_core;
  var utils = window.__devtool_utils;

  // Shadow DOM mount helpers
  //
  // All indicator UI is mounted into a shadow root (via shadow-root.js) for
  // style isolation from the host page. When the shadow root is unavailable
  // (legacy browser, CSP failure), mountRoot() falls back to document.body
  // and the helpers transparently use document-level lookups.
  //
  // Helpers:
  //   mountRoot()                   -> ShadowRoot or document.body
  //   isShadowMount()               -> true if mountRoot is a ShadowRoot
  //   getInMount(id)                -> element lookup by id scoped to mount
  //   styleTarget()                 -> where to append a <style> so its rules
  //                                    affect the indicator UI (shadow root
  //                                    when shadow, document.head when fallback)
  function mountRoot() {
    return typeof window.__devtoolGetMountRoot === 'function'
      ? window.__devtoolGetMountRoot()
      : document.body;
  }
  function isShadowMount() {
    return typeof window.__devtoolIsShadowMount === 'function' && window.__devtoolIsShadowMount();
  }
  // Get element by id from the active mount root. ShadowRoot inherits
  // getElementById from DocumentFragment; document.body does not, so we
  // branch and use document.getElementById in the fallback path.
  function getInMount(id) {
    if (isShadowMount()) {
      var root = mountRoot();
      // ShadowRoot.getElementById exists per DOM spec.
      if (root && typeof root.getElementById === 'function') {
        return root.getElementById(id);
      }
    }
    return document.getElementById(id);
  }
  // When injecting a <style> that targets UI inside the mount root, append
  // it to the mount root itself (shadow) or document.head (fallback). Using
  // document.head when the UI is inside a shadow root would not work — host
  // stylesheets do not cascade into shadow roots.
  function styleTarget() {
    if (isShadowMount()) {
      return mountRoot();
    }
    return document.head;
  }

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
    outputPreviewLines: null, // Committed-lines child of the output preview
    outputPreviewThrobber: null, // In-place-updating throbber line child
    outputPreviewHTML: null, // Last rendered lines HTML (skip rebuild when unchanged)
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
    microToastTimeout: null, // Auto-hide timer for micro toast
    expandedErrorKeys: {},   // Stable keys of expanded error items
    expandedNetworkKeys: {}, // Stable keys of expanded network items
    prevFocus: null          // Element to restore focus to on panel close
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
    history: van.state([]),

    // Chaos tab data (live engine state pushed from the proxy)
    chaos: van.state({
      loaded: false,
      enabled: false,
      swallowDetect: false,
      globalOdds: 0,
      rules: [],
      stats: null
    }),
    chaosPresets: van.state([])
  };

  // ============================================
  // Chaos control client
  // Request/response over the metrics WebSocket: chaos_request out,
  // chaos_response back (keyed by request_id), chaos_state pushed on any
  // engine mutation regardless of which surface (panel, MCP, hub) made it.
  // ============================================
  var chaosPending = {}; // request_id -> callback(result, error)

  function chaosRequest(action, params, callback) {
    if (!core || !core.send) {
      if (callback) callback(null, 'not connected');
      return;
    }
    var requestID = generateId();
    if (callback) {
      chaosPending[requestID] = callback;
      // Drop stale callbacks so the map cannot grow unbounded
      setTimeout(function() { delete chaosPending[requestID]; }, 10000);
    }
    var sent = core.send('chaos_request', {
      request_id: requestID,
      action: action,
      params: params || {}
    });
    if (!sent && callback) {
      delete chaosPending[requestID];
      callback(null, 'not connected');
    }
  }

  function applyChaosSnapshot(snap) {
    if (!snap) return;
    var prev = store.chaos.val;
    var next = {
      loaded: true,
      enabled: snap.enabled === true,
      swallowDetect: snap.swallow_detect === true,
      globalOdds: snap.global_odds || 0,
      rules: snap.rules || [],
      stats: snap.stats || null
    };
    store.chaos.val = next;
    I.setChaosIndicator(next.enabled);
    if (prev.loaded && prev.enabled !== next.enabled) {
      if (next.enabled) {
        I.showMicroToast('⛈ Chaos ON', TOKENS.colors.chaos);
        I.logHistoryEvent('system', 'Chaos mode enabled', '');
      } else {
        I.showMicroToast('☀ Chaos off', TOKENS.colors.success);
        I.logHistoryEvent('system', 'Chaos mode disabled', '');
      }
    }
  }

  function refreshChaosData() {
    chaosRequest('status', {}, function(result) {
      if (result) applyChaosSnapshot(result);
    });
    if (store.chaosPresets.val.length === 0) {
      chaosRequest('list_presets', {}, function(result) {
        if (result && result.presets) store.chaosPresets.val = result.presets;
      });
    }
  }

  // ============================================
  // Data Refresh Functions
  // These update the reactive store, UI follows automatically
  // ============================================

  // Capture (errors, fetch/XHR, DOM mutations, interactions) and framework
  // detection all run in the CONTENT frame, but this indicator runs in the chrome
  // shell — a separate window whose own __devtool_* globals are empty. Read them
  // from the content frame via targetWindow() (defined below; returns the active
  // content frame's window when we are the shell, else self). Without this the
  // Overview / Errors / Network / Performance / Interactions tabs all read empty
  // shell-local globals. targetWindow() is hoisted, so these are safe to call.
  function dvErrors() { var w = targetWindow(); return w ? w.__devtool_errors : null; }
  function dvApi() { var w = targetWindow(); return w ? w.__devtool_api : null; }
  function dvMutations() { var w = targetWindow(); return w ? w.__devtool_mutations : null; }
  function dvFramework() { var w = targetWindow(); return w ? w.__devtool_framework : null; }
  function dvInteractions() { var w = targetWindow(); return w ? w.__devtool_interactions : null; }

  // mutationRateStats adapts mutation.js getRateStats() output to the shape this
  // panel consumes. getRateStats returns { windows: { '5s': <rate number> }, ... }
  // (label-keyed, value is the rate), but the Overview/Performance reads and the
  // perf badge expect a numeric-ms key whose value is an object with .rate
  // (e.g. stats[5000].rate). Without this adapter every mutation rate reads 0 and
  // the Performance "Mutations (Ns window)" cards never render. Returns {} when
  // unavailable.
  function mutationRateStats(windows) {
    var m = dvMutations();
    if (!m || typeof m.getRateStats !== 'function') { return {}; }
    var raw;
    try { raw = m.getRateStats(windows); } catch (e) { return {}; }
    if (!raw || !raw.windows) { return {}; }
    var out = {};
    for (var i = 0; i < windows.length; i++) {
      var ms = windows[i];
      var label = ms < 1000 ? (ms + 'ms') : ((ms / 1000) + 's');
      if (raw.windows[label] != null) { out[ms] = { rate: raw.windows[label] }; }
    }
    return out;
  }

  function refreshOverviewData() {
    var framework = dvFramework() ? dvFramework().detect() : null;
    var errorStats = dvErrors() ? dvErrors().getStats() : null;
    var apiStats = dvApi() ? dvApi().getStats() : null;
    var mutationStats = mutationRateStats([5000]);
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
    if (isReact && dvMutations()) {
      try {
        var untriggered = dvMutations().getUntriggered ? dvMutations().getUntriggered() : [];
        var recentUntriggered = untriggered.filter(function(m) {
          return m.timestamp && (Date.now() - m.timestamp) < 30000;
        });
        data.rerenderRate = parseFloat((recentUntriggered.length / 30).toFixed(1));
      } catch (e) { /* ignore */ }

      try {
        var correlationStats = dvMutations().getCorrelationStats ? dvMutations().getCorrelationStats() : null;
        if (correlationStats && correlationStats.avg_latency) {
          data.inputLag = correlationStats.avg_latency.input || 0;
        }
      } catch (e) { /* ignore */ }
    }

    store.overview.val = data;
  }

  function refreshErrorsData() {
    if (!dvErrors()) {
      store.errors.val = [];
      return;
    }
    var deduplicated = dvErrors().getDeduplicatedErrors();
    var allErrors = [].concat(deduplicated.jsErrors || [], deduplicated.consoleErrors || [], deduplicated.consoleWarnings || []);
    store.errors.val = allErrors;
  }

  function refreshNetworkData() {
    if (!dvApi()) {
      store.network.val = [];
      return;
    }
    var calls = dvApi().getCalls();
    store.network.val = calls.slice(-20).reverse();
  }

  function refreshPerformanceData() {
    if (!dvMutations()) {
      store.performance.val = { rateStats: {}, isReact: false, rerenderRate: 0, rerenderCount: 0, inputLag: 0, maxInputLag: 0, inputCount: 0, hotspots: [] };
      return;
    }

    var rateStats = mutationRateStats([1000, 5000, 30000]);
    var framework = dvFramework() ? dvFramework().detect() : null;
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
        var untriggered = dvMutations().getUntriggered ? dvMutations().getUntriggered() : [];
        var recentUntriggered = untriggered.filter(function(m) {
          return m.timestamp && (Date.now() - m.timestamp) < 30000;
        });
        data.rerenderRate = parseFloat((recentUntriggered.length / 30).toFixed(1));
        data.rerenderCount = recentUntriggered.length;
      } catch (e) { /* ignore */ }

      try {
        var correlationStats = dvMutations().getCorrelationStats ? dvMutations().getCorrelationStats() : null;
        if (correlationStats && correlationStats.avg_latency) {
          data.inputLag = correlationStats.avg_latency.input || 0;
          data.maxInputLag = correlationStats.max_latency.input || 0;
          data.inputCount = correlationStats.by_type ? (correlationStats.by_type.input || 0) : 0;
        }
      } catch (e) { /* ignore */ }

      try {
        var untriggeredMutations = dvMutations().getUntriggered ? dvMutations().getUntriggered() : [];
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
    if (!dvInteractions()) {
      store.interactions.val = [];
      return;
    }
    var history = dvInteractions().getHistory ? dvInteractions().getHistory() : [];
    store.interactions.val = history.slice(-10).reverse();
  }

  // Reactive actions - update store, DOM follows automatically
  var actions = {
    addAttachment: function(type, data) {
      var id = generateId();

      if (type === 'screenshot' && data.imageBuffer) {
        // Stream raw PNG bytes directly — no base64 encoding, no JSON overhead
        I.sendCaptureBinary(id, data.imageBuffer);
        // Store only metadata; drop the ArrayBuffer from in-memory state
        var attachment = {
          id: id,
          type: type,
          label: data.label,
          summary: data.summary,
          data: { area: data.area, thumbnail: data.thumbnail },
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
      I.switchTab('compose');
      I.togglePanel(true);
    }
  };

  // Page-operating surfaces (audits, element picker, sketch/design overlays)
  // must run against the real page DOM. Under the always-wrap frame model the
  // page lives in the content <iframe> while this indicator runs in the chrome
  // shell — so `document` here is the shell, and elementFromPoint/querySelector
  // would only ever see the iframe element. targetWindow() returns the window
  // whose `document` IS the page: the active content frame when we are the
  // shell, else our own window (unwrapped page / legacy standalone). Same-origin
  // (the proxy serves both frames) so reaching the content frame's globals and
  // calling them synchronously is safe; on any cross-origin / not-ready failure
  // we fall back to self.
  function targetWindow() {
    try {
      if (window.__devtool_frame_role === 'chrome' &&
          window.__devtool_frames && typeof window.__devtool_frames.active === 'function') {
        var a = window.__devtool_frames.active();
        if (a && a.win) { return a.win; }
      }
    } catch (e) { /* cross-origin / shell registry not ready — fall back to self */ }
    return window;
  }

  // Shared with indicator-tabs.js / indicator.js.
  I.van = van;
  I.tags = tags;
  I.core = core;
  I.utils = utils;
  I.mountRoot = mountRoot;
  I.isShadowMount = isShadowMount;
  I.getInMount = getInMount;
  I.styleTarget = styleTarget;
  I.state = state;
  I.store = store;
  I.chaosPending = chaosPending;
  I.chaosRequest = chaosRequest;
  I.applyChaosSnapshot = applyChaosSnapshot;
  I.refreshChaosData = refreshChaosData;
  I.dvErrors = dvErrors;
  I.dvApi = dvApi;
  I.dvMutations = dvMutations;
  I.dvInteractions = dvInteractions;
  I.mutationRateStats = mutationRateStats;
  I.refreshOverviewData = refreshOverviewData;
  I.refreshErrorsData = refreshErrorsData;
  I.refreshNetworkData = refreshNetworkData;
  I.refreshPerformanceData = refreshPerformanceData;
  I.refreshInteractionsData = refreshInteractionsData;
  I.actions = actions;
  I.targetWindow = targetWindow;
})();

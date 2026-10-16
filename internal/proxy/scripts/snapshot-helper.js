// Snapshot helpers for visual regression testing
// Integrates with existing screenshot API and MCP snapshot tool

(function() {
  'use strict';

  var core = window.__devtool_core;

  // Pre-render same-origin iframe content for html2canvas capture.
  function html2canvasWithIframes(element, canvasOpts) {
    var root = element === document.body ? document.documentElement : element;
    var iframes = Array.prototype.slice.call(root.querySelectorAll('iframe'));
    if (element.tagName === 'IFRAME') iframes.unshift(element);
    if (iframes.length === 0) return html2canvas(element, canvasOpts);

    function waitForPaint(win) {
      return new Promise(function(resolve) {
        var raf = (win && win.requestAnimationFrame) || requestAnimationFrame;
        raf(function() { raf(function() { resolve(); }); });
      });
    }

    var renderPromises = iframes.map(function(iframe) {
      try {
        var doc = iframe.contentDocument || iframe.contentWindow.document;
        if (!doc || !doc.body || doc.body.scrollHeight <= 0) {
          return Promise.resolve(null);
        }
        return waitForPaint(iframe.contentWindow).then(function() {
          return html2canvas(doc.body, {
            allowTaint: true, useCORS: true, logging: false,
            width: iframe.clientWidth || iframe.offsetWidth,
            height: iframe.clientHeight || iframe.offsetHeight
          });
        }).then(function(c) { return c.toDataURL('image/png'); })
          .catch(function() { return null; });
      } catch (e) { return Promise.resolve(null); }
    });

    return Promise.all(renderPromises).then(function(dataUrls) {
      var origOnclone = canvasOpts.onclone;
      canvasOpts.onclone = function(clonedDoc, clonedEl) {
        var clonedRoot = clonedEl || clonedDoc.documentElement;
        var clonedIframes = Array.prototype.slice.call(clonedRoot.querySelectorAll('iframe'));
        clonedIframes.forEach(function(cf, i) {
          if (!dataUrls[i] || !cf.parentNode) return;
          var img = clonedDoc.createElement('img');
          img.src = dataUrls[i];
          var computed = window.getComputedStyle(iframes[i]);
          img.style.width = computed.width;
          img.style.height = computed.height;
          img.style.display = computed.display === 'none' ? 'none' : 'block';
          img.style.border = computed.border;
          img.style.margin = computed.margin;
          if (computed.transform && computed.transform !== 'none') {
            img.style.transform = computed.transform;
            img.style.transformOrigin = computed.transformOrigin;
          }
          cf.parentNode.replaceChild(img, cf);
        });
        if (origOnclone) origOnclone(clonedDoc, clonedEl);
      };
      return html2canvas(element, canvasOpts);
    });
  }

  // Helper to capture current page as PageCapture format
  function captureCurrentPage() {
    return new Promise(function(resolve, reject) {
      // html2canvas is loaded on demand; ensure it, then re-enter.
      if (typeof html2canvas === 'undefined') {
        if (typeof window.__devtool_ensureHtml2canvas === 'function') {
          window.__devtool_ensureHtml2canvas().then(function() {
            captureCurrentPage().then(resolve, reject);
          }, reject);
        } else {
          reject(new Error('html2canvas not loaded'));
        }
        return;
      }

      html2canvasWithIframes(document.body, {
        allowTaint: true,
        useCORS: true,
        logging: false,
        scrollY: -window.scrollY,
        scrollX: -window.scrollX,
        windowWidth: document.documentElement.scrollWidth,
        windowHeight: document.documentElement.scrollHeight
      }).then(function(canvas) {
        // Get base64 data (remove data:image/png;base64, prefix)
        var dataUrl = canvas.toDataURL('image/png');
        var base64Data = dataUrl.split(',')[1];

        resolve({
          url: window.location.pathname,
          viewport: {
            width: window.innerWidth,
            height: window.innerHeight
          },
          screenshot_data: base64Data
        });
      }).catch(reject);
    });
  }

  // Baseline/compare requests go to the proxy as snapshot_request and are
  // answered by snapshot_response keyed by request_id (the store.js pattern).
  // The proxy saves and compares through the same baseline store the
  // `snapshot` MCP tool uses (~/.agnt/baselines), and the promise settles only
  // on its reply: resolved with the saved baseline or the CompareResult,
  // rejected with the proxy's error, on timeout, or when nothing was sent.

  // The proxy refuses a larger capture (maxSnapshotScreenshotBase64 in
  // ws_snapshot.go); over the 16 MiB WebSocket frame cap the connection itself
  // would close. Checked here so an oversize page fails with a clear message.
  var MAX_SCREENSHOT_BASE64 = 15 * 1024 * 1024;
  var SNAPSHOT_TIMEOUT_MS = 60000;
  var pendingRequests = {}; // request_id -> {resolve, reject, timeout}
  var requestIdCounter = 0;

  function connected() {
    return !!(core && typeof core.isConnected === 'function' && core.isConnected());
  }

  function notConnected(what) {
    return new Error(what + ' failed: DevTool socket not connected');
  }

  function sendSnapshotRequest(action, name, page) {
    var what = action === 'baseline' ? 'Baseline "' + name + '"' : 'Comparison to "' + name + '"';
    return new Promise(function(resolve, reject) {
      if (!page || !page.screenshot_data) {
        reject(new Error(what + ' failed: the capture produced no image'));
        return;
      }
      if (page.screenshot_data.length > MAX_SCREENSHOT_BASE64) {
        reject(new Error(what + ' failed: the capture is ' + page.screenshot_data.length +
          ' bytes of base64, over the ' + MAX_SCREENSHOT_BASE64 + ' byte limit; capture a smaller page or viewport'));
        return;
      }
      if (!connected()) {
        reject(notConnected(what));
        return;
      }

      var requestId = 'snap_' + Date.now().toString(36) + '_' + (++requestIdCounter);
      var timeout = setTimeout(function() {
        delete pendingRequests[requestId];
        reject(new Error(what + ' failed: no reply from the proxy within ' + (SNAPSHOT_TIMEOUT_MS / 1000) + 's'));
      }, SNAPSHOT_TIMEOUT_MS);
      pendingRequests[requestId] = { resolve: resolve, reject: reject, timeout: timeout };

      var sent = core.send('snapshot_request', {
        request_id: requestId,
        action: action,
        name: name,
        page: page
      });
      if (sent === false) {
        clearTimeout(timeout);
        delete pendingRequests[requestId];
        reject(notConnected(what));
      }
    });
  }

  function handleSnapshotResponse(message) {
    if (!message || message.type !== 'snapshot_response') return;
    var pending = pendingRequests[message.request_id];
    if (!pending) return;
    clearTimeout(pending.timeout);
    delete pendingRequests[message.request_id];
    if (message.error) {
      pending.reject(new Error(message.error));
    } else {
      pending.resolve(message.result);
    }
  }

  if (core && core.onMessage) {
    core.onMessage(handleSnapshotResponse);
  }

  // Capture the page and send it; fails fast (before the capture) on a
  // missing name or a closed socket.
  function captureAndSend(action, name) {
    if (typeof name !== 'string' || name === '') {
      return Promise.reject(new Error('A baseline name is required'));
    }
    if (!connected()) {
      return Promise.reject(notConnected(action === 'baseline' ? 'Baseline "' + name + '"' : 'Comparison to "' + name + '"'));
    }
    return captureCurrentPage().then(function(page) {
      return sendSnapshotRequest(action, name, page);
    });
  }

  // Create a baseline from the current page. Resolves with the saved baseline
  // ({name, timestamp, git_commit, git_branch, pages, config}).
  function createBaseline(name) {
    return captureAndSend('baseline', name);
  }

  // Compare the current page to a saved baseline. Resolves with the
  // CompareResult ({baseline_name, timestamp, pages, summary}); a visual
  // change is a resolved result with summary.has_regressions, not a rejection.
  function compareToBaseline(baselineName) {
    return captureAndSend('compare', baselineName);
  }

  // Capture multiple pages (for multi-page baselines)
  function capturePages(urls) {
    var currentUrl = window.location.pathname;
    var pages = [];

    function captureNext(index) {
      if (index >= urls.length) {
        return Promise.resolve(pages);
      }

      var url = urls[index];

      // If URL is different from current, we can't auto-navigate
      // User needs to navigate manually or we need playwright
      if (url !== currentUrl && index === 0) {
        return captureCurrentPage().then(function(page) {
          pages.push(page);
          return pages;
        });
      }

      return captureCurrentPage().then(function(page) {
        pages.push(page);
        return captureNext(index + 1);
      });
    }

    return captureNext(0);
  }

  // Quick baseline with auto-generated name
  function quickBaseline() {
    var timestamp = new Date().toISOString().replace(/[:.]/g, '-').substring(0, 19);
    var name = 'quick-' + timestamp;
    return createBaseline(name);
  }

  // Export snapshot helpers
  window.__devtool_snapshot = {
    // Core functions
    captureCurrentPage: captureCurrentPage,
    createBaseline: createBaseline,
    compareToBaseline: compareToBaseline,
    capturePages: capturePages,
    quickBaseline: quickBaseline,

    // Convenience aliases
    baseline: createBaseline,
    compare: compareToBaseline,
    quick: quickBaseline,

    // Manual PageCapture creation (advanced)
    createPageCapture: function(url, screenshotData, width, height) {
      return {
        url: url || window.location.pathname,
        viewport: {
          width: width || window.innerWidth,
          height: height || window.innerHeight
        },
        screenshot_data: screenshotData
      };
    }
  };
})();

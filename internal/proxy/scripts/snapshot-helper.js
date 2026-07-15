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

  // Create a baseline from current page
  function createBaseline(name) {
    return captureCurrentPage().then(function(page) {
      core.send('snapshot_baseline', {
        name: name,
        pages: [page],
        timestamp: Date.now()
      });

      return {
        success: true,
        message: 'Baseline "' + name + '" captured',
        page: page
      };
    });
  }

  // Compare current page to baseline
  function compareToBaseline(baselineName) {
    return captureCurrentPage().then(function(page) {
      core.send('snapshot_compare', {
        baseline: baselineName,
        pages: [page],
        timestamp: Date.now()
      });

      return {
        success: true,
        message: 'Comparison to "' + baselineName + '" requested',
        page: page
      };
    });
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

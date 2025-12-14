// Snapshot helpers for visual regression testing
// Integrates with existing screenshot API and MCP snapshot tool

(function() {
  'use strict';

  var core = window.__devtool_core;

  // Helper to capture current page as PageCapture format
  function captureCurrentPage() {
    return new Promise(function(resolve, reject) {
      if (typeof html2canvas === 'undefined') {
        reject(new Error('html2canvas not loaded'));
        return;
      }

      html2canvas(document.body, {
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

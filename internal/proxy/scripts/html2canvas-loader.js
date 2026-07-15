(function() {
  'use strict';

  // On-demand loader for html2canvas-pro. The library is ~211KB and is only
  // needed when a screenshot is actually captured (MCP snapshot tool, design
  // capture, indicator capture mode, sketch background). Shipping it in the
  // always-injected head bundle blocked first paint of every proxied page and
  // parsed 211KB in every frame that never captures. Instead it is served
  // lazily from /__devtool_html2canvas and injected only on first use — the
  // same pattern accessibility.js already uses for axe-core.
  //
  // Exposes window.__devtool_ensureHtml2canvas(): Promise<void> that resolves
  // once window.html2canvas is a callable function. Idempotent and safe to call
  // concurrently — a single in-flight load is shared by all callers.

  if (typeof window.__devtool_ensureHtml2canvas === 'function') {
    return; // already installed in this frame
  }

  var inflight = null; // shared Promise while the script tag is loading

  window.__devtool_ensureHtml2canvas = function() {
    if (typeof window.html2canvas === 'function') {
      return Promise.resolve();
    }
    if (inflight) {
      return inflight;
    }
    inflight = new Promise(function(resolve, reject) {
      // A concurrent frame/script may have injected it between the check above
      // and here; re-check before appending a duplicate tag.
      if (typeof window.html2canvas === 'function') {
        resolve();
        return;
      }
      var existing = document.querySelector('script[data-devtool-html2canvas]');
      if (existing) {
        existing.addEventListener('load', function() { resolve(); });
        existing.addEventListener('error', function() { reject(new Error('html2canvas failed to load')); });
        return;
      }
      var script = document.createElement('script');
      script.src = '/__devtool_html2canvas';
      script.setAttribute('data-devtool-html2canvas', '1');
      script.onload = function() {
        if (typeof window.html2canvas === 'function') {
          resolve();
        } else {
          reject(new Error('html2canvas loaded but window.html2canvas is undefined'));
        }
      };
      script.onerror = function() {
        inflight = null; // allow a retry on the next capture attempt
        reject(new Error('html2canvas failed to load from /__devtool_html2canvas'));
      };
      (document.head || document.documentElement).appendChild(script);
    });
    return inflight;
  };
})();

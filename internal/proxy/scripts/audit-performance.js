// Performance audit

(function() {
  'use strict';

  var utils = window.__devtool_utils;
  var auditUtils = window.__devtool_audit_utils;

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxResources: number (default: 20) - limit resource entries
  //   maxUrlLength: number (default: 60) - truncate resource URLs
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  function auditPerformance(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxResources = options.maxResources || 20;
    var maxUrlLength = options.maxUrlLength || 60;
    var raw = options.raw === true; // Default: false (AI-optimized format)

    var perf = window.performance;
    if (!perf) {
      return { error: 'Performance API not available', detailLevel: detailLevel };
    }

    // === HELPER FUNCTIONS ===

    // Rate a metric based on thresholds
    function rateMetric(value, goodThreshold, poorThreshold) {
      if (value === null || value === undefined) return 'unknown';
      if (value <= goodThreshold) return 'good';
      if (value <= poorThreshold) return 'needs-improvement';
      return 'poor';
    }

    // Extract domain from URL
    function getDomain(url) {
      try {
        return new URL(url).hostname;
      } catch (e) {
        return 'unknown';
      }
    }

    // Format bytes to human-readable
    function formatBytes(bytes) {
      if (bytes === 0) return '0B';
      if (bytes < 1024) return bytes + 'B';
      if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + 'KB';
      return (bytes / 1024 / 1024).toFixed(1) + 'MB';
    }

    // Generate CSS selector for element — shared implementation (audit-utils.js)
    function getSelector(el) {
      var sel = auditUtils.getSelector(el);
      return sel || 'unknown';
    }

    // === COLLECT METRICS ===

    // Get paint timing
    var paintEntries = perf.getEntriesByType ? perf.getEntriesByType('paint') : [];
    var fcp = null;
    var fp = null;
    for (var i = 0; i < paintEntries.length; i++) {
      if (paintEntries[i].name === 'first-contentful-paint') fcp = Math.round(paintEntries[i].startTime);
      if (paintEntries[i].name === 'first-paint') fp = Math.round(paintEntries[i].startTime);
    }

    // === LCP / CLS / INP ===
    // Preferred source: window.__devtool_vitals, the buffered accumulator that
    // observes largest-contentful-paint / layout-shift / event (INP) / longtask
    // entries from page start. The synchronous getEntriesByType reads below are
    // fallback only — they see an empty buffer for entry types that require a
    // PerformanceObserver, so a missing accumulator is reported honestly.
    function vitalNumber(v) {
      if (typeof v === 'number' && isFinite(v)) return v;
      if (v && typeof v.value === 'number' && isFinite(v.value)) return v.value;
      return null;
    }

    var vitalsData = null;
    var vitalsNote = null;
    try {
      var vitalsAcc = window.__devtool_vitals;
      if (vitalsAcc) {
        if (typeof vitalsAcc.getVitals === 'function') vitalsData = vitalsAcc.getVitals();
        else if (typeof vitalsAcc.get === 'function') vitalsData = vitalsAcc.get();
        else vitalsData = vitalsAcc;
      }
    } catch (e) {
      vitalsData = null;
    }

    var lcp = null;
    var cls = null;
    var inp = null;

    if (vitalsData) {
      var lcpV = vitalNumber(vitalsData.lcp);
      var clsV = vitalNumber(vitalsData.cls);
      var inpV = vitalNumber(vitalsData.inp);
      if (lcpV !== null) lcp = Math.round(lcpV);
      if (clsV !== null) cls = Math.round(clsV * 1000) / 1000;
      if (inpV !== null) inp = Math.round(inpV);
    } else {
      vitalsNote = 'vitals accumulator unavailable — LCP/CLS/INP read from the synchronous performance buffer, which is usually empty for these entry types';
    }

    // Fallback: synchronous entry reads (only fill values still missing)
    if (lcp === null) {
      try {
        var lcpEntries = perf.getEntriesByType ? perf.getEntriesByType('largest-contentful-paint') : [];
        if (lcpEntries.length > 0) {
          lcp = Math.round(lcpEntries[lcpEntries.length - 1].startTime);
        }
      } catch (e) {
        // LCP may not be available
      }
    }

    if (cls === null) {
      try {
        var layoutShifts = perf.getEntriesByType('layout-shift') || [];
        if (layoutShifts.length > 0) {
          cls = layoutShifts.reduce(function(sum, entry) {
            if (!entry.hadRecentInput) {
              return sum + entry.value;
            }
            return sum;
          }, 0);
          cls = Math.round(cls * 1000) / 1000; // Round to 3 decimals
        }
      } catch (e) {
        // CLS may not be available
      }
    }

    // === CORE WEB VITALS WITH RATINGS ===
    var coreWebVitals = {
      lcp: {
        value: lcp,
        rating: rateMetric(lcp, 2500, 4000),
        target: 2500
      },
      fcp: {
        value: fcp,
        rating: rateMetric(fcp, 1800, 3000),
        target: 1800
      },
      cls: {
        value: cls,
        rating: rateMetric(cls, 0.1, 0.25),
        target: 0.1
      },
      inp: {
        value: inp,
        rating: inp === null ? 'unknown' : rateMetric(inp, 200, 500),
        target: 200
      }
    };
    if (vitalsNote) coreWebVitals.note = vitalsNote;

    // === RESOURCE ANALYSIS ===

    var resources = perf.getEntriesByType ? perf.getEntriesByType('resource') : [];

    // Categorize resources by type
    var resourcesByType = {
      script: [],
      css: [],
      img: [],
      font: [],
      fetch: [],
      other: []
    };

    var thirdPartyMap = {};
    var currentDomain = window.location.hostname;

    for (var j = 0; j < resources.length; j++) {
      var r = resources[j];
      var type = r.initiatorType || 'other';

      // Normalize type
      if (type === 'link' && r.name.match(/\.css/i)) type = 'css';
      if (type === 'img' || r.name.match(/\.(jpg|jpeg|png|gif|webp|svg)/i)) type = 'img';
      if (type === 'xmlhttprequest' || type === 'fetch') type = 'fetch';
      if (r.name.match(/\.(woff2?|ttf|otf|eot)/i)) type = 'font';
      if (type === 'script' || r.name.match(/\.js/i)) type = 'script';
      if (type === 'link' || type === 'css' || r.name.match(/\.css/i)) type = 'css';

      var category = resourcesByType[type] ? type : 'other';

      var resourceData = {
        url: r.name,
        duration: Math.round(r.duration),
        size: r.transferSize || 0,
        type: category
      };

      resourcesByType[category].push(resourceData);

      // Track third-party resources
      var domain = getDomain(r.name);
      if (domain !== currentDomain && domain !== 'unknown') {
        if (!thirdPartyMap[domain]) {
          thirdPartyMap[domain] = {
            requests: 0,
            totalTime: 0,
            totalSize: 0
          };
        }
        thirdPartyMap[domain].requests++;
        thirdPartyMap[domain].totalTime += Math.round(r.duration);
        thirdPartyMap[domain].totalSize += r.transferSize || 0;
      }
    }

    // === ISSUE DETECTION ===

    var fixable = [];
    var informational = [];
    var actions = [];
    var checksRun = [];
    var fixableId = 0;

    // 1. Check for render-blocking scripts
    checksRun.push('render-blocking-resources');
    var blockingScripts = document.querySelectorAll('script[src]:not([async]):not([defer]):not([type="module"])');
    for (var k = 0; k < blockingScripts.length; k++) {
      var script = blockingScripts[k];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(script)) continue;
      var src = script.getAttribute('src');
      if (src && !src.match(/^\s*$/)) {
        fixable.push({
          id: 'render-block-' + (++fixableId),
          type: 'render-blocking',
          severity: 'error',
          selector: getSelector(script),
          impact: 8,
          fix: 'Add async or defer attribute',
          estimatedSavings: '~300-500ms'
        });
      }
    }

    // 2. Check for render-blocking stylesheets
    var blockingStyles = document.querySelectorAll('link[rel="stylesheet"]:not([media="print"])');
    for (var l = 0; l < blockingStyles.length; l++) {
      var link = blockingStyles[l];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(link)) continue;
      informational.push({
        id: 'css-block-' + l,
        type: 'render-blocking-css',
        severity: 'info',
        selector: getSelector(link),
        message: 'Stylesheet blocks rendering (consider critical CSS extraction)'
      });
    }

    // 3. Check for unoptimized images
    checksRun.push('unoptimized-images');
    var images = document.querySelectorAll('img[src]');
    var unoptimizedCount = 0;
    for (var m = 0; m < images.length; m++) {
      var img = images[m];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(img)) continue;
      var imgSrc = img.getAttribute('src');
      var naturalWidth = img.naturalWidth || 0;
      var naturalHeight = img.naturalHeight || 0;
      var displayWidth = img.offsetWidth || 0;
      var hasLazyLoading = img.getAttribute('loading') === 'lazy';

      // Find resource entry for this image
      var imgResource = null;
      for (var n = 0; n < resourcesByType.img.length; n++) {
        if (resourcesByType.img[n].url.indexOf(imgSrc) !== -1) {
          imgResource = resourcesByType.img[n];
          break;
        }
      }

      var imgSize = imgResource ? imgResource.size : 0;
      var isLarge = imgSize > 500 * 1024; // >500KB
      var isOversized = naturalWidth > displayWidth * 1.5 && displayWidth > 0;

      if (isLarge || (isOversized && !hasLazyLoading)) {
        unoptimizedCount++;
        if (unoptimizedCount <= 10) { // Limit to 10 entries
          fixable.push({
            id: 'img-unopt-' + unoptimizedCount,
            type: 'unoptimized-image',
            severity: isLarge ? 'error' : 'warning',
            selector: getSelector(img),
            size: imgSize > 0 ? formatBytes(imgSize) : 'unknown',
            dimensions: naturalWidth + 'x' + naturalHeight,
            impact: isLarge ? 7 : 5,
            fix: 'Resize to ' + displayWidth + 'px width, convert to WebP, add loading="lazy"'
          });
        }
      }
    }

    // 4. Check for font loading optimization
    checksRun.push('font-loading');
    var fontFaces = [];
    try {
      if (document.fonts && document.fonts.forEach) {
        document.fonts.forEach(function(font) {
          fontFaces.push(font);
        });
      }
    } catch (e) {
      // Font API may not be available
    }

    var hasSwap = false;
    var inaccessibleSheets = 0;
    for (var p = 0; p < document.styleSheets.length; p++) {
      try {
        var rules = document.styleSheets[p].cssRules || [];
        for (var q = 0; q < rules.length; q++) {
          if (rules[q].cssText && rules[q].cssText.indexOf('@font-face') !== -1) {
            if (rules[q].cssText.indexOf('font-display') !== -1) {
              hasSwap = true;
            }
          }
        }
      } catch (e) {
        // Cross-origin stylesheets can't be accessed — count instead of
        // silently swallowing so the report is honest about coverage.
        inaccessibleSheets++;
      }
    }

    if (fontFaces.length > 0 && !hasSwap) {
      var fontDisplayMsg = 'Consider adding font-display: swap to @font-face rules for better perceived performance';
      if (inaccessibleSheets > 0) {
        fontDisplayMsg += ' (' + inaccessibleSheets + ' cross-origin stylesheets could not be inspected)';
      }
      informational.push({
        id: 'font-display',
        type: 'font-loading',
        severity: 'info',
        message: fontDisplayMsg
      });
    }
    if (inaccessibleSheets > 0) {
      informational.push({
        id: 'stylesheet-coverage',
        type: 'stylesheet-coverage',
        severity: 'info',
        message: inaccessibleSheets + ' of ' + document.styleSheets.length +
          ' stylesheets are cross-origin and could not be inspected for @font-face rules'
      });
    }

    // 5. Analyze slowest resources
    var allResources = [];
    Object.keys(resourcesByType).forEach(function(type) {
      allResources = allResources.concat(resourcesByType[type]);
    });
    allResources.sort(function(a, b) { return b.duration - a.duration; });

    var slowestResources = allResources.slice(0, 10).map(function(r) {
      return {
        url: detailLevel === 'full' ? r.url : auditUtils.truncateUrl(r.url, maxUrlLength),
        duration: r.duration,
        type: r.type,
        size: r.size
      };
    });

    // 6. Detect large payloads
    checksRun.push('large-payloads');
    var largePayloads = allResources.filter(function(r) {
      return r.size > 100 * 1024; // >100KB
    });

    if (largePayloads.length > 0) {
      for (var s = 0; s < Math.min(5, largePayloads.length); s++) {
        var large = largePayloads[s];
        informational.push({
          id: 'large-payload-' + s,
          type: 'large-payload',
          severity: 'warning',
          url: detailLevel === 'full' ? large.url : auditUtils.truncateUrl(large.url, 80),
          size: formatBytes(large.size),
          message: 'Large resource: ' + formatBytes(large.size)
        });
      }
    }

    // 7. Third-party impact analysis
    checksRun.push('third-party-impact');
    var thirdPartyImpact = [];
    Object.keys(thirdPartyMap).forEach(function(domain) {
      thirdPartyImpact.push({
        domain: domain,
        requests: thirdPartyMap[domain].requests,
        totalTime: thirdPartyMap[domain].totalTime,
        totalSize: thirdPartyMap[domain].totalSize
      });
    });
    thirdPartyImpact.sort(function(a, b) { return b.totalTime - a.totalTime; });

    // === GENERATE ACTIONS ===

    if (lcp && lcp > 2500) {
      var lcpSeverity = lcp > 4000 ? 'poor' : 'needs-improvement';
      var lcpBlocking = blockingScripts.length > 0 ? ' (blocking scripts delay LCP by ~' + (blockingScripts.length * 300) + 'ms)' : '';
      actions.push('Improve LCP (' + (lcp / 1000).toFixed(1) + 's, ' + lcpSeverity + ')' + lcpBlocking);
    }

    if (blockingScripts.length > 0) {
      actions.push('Defer ' + blockingScripts.length + ' render-blocking script' + (blockingScripts.length > 1 ? 's' : '') + ' (estimated ~' + (blockingScripts.length * 300) + 'ms savings)');
    }

    if (unoptimizedCount > 0) {
      actions.push('Optimize ' + unoptimizedCount + ' image' + (unoptimizedCount > 1 ? 's' : '') + ': resize, compress, lazy load');
    }

    if (slowestResources.length > 0 && slowestResources[0].duration > 1000) {
      var slowest = slowestResources[0];
      var slowestDomain = getDomain(slowest.url);
      actions.push('Investigate slow resource: ' + slowestDomain + ' (' + (slowest.duration / 1000).toFixed(1) + 's)');
    }

    if (thirdPartyImpact.length > 0 && thirdPartyImpact[0].totalTime > 500) {
      actions.push('Review third-party impact from ' + thirdPartyImpact[0].domain + ' (' + thirdPartyImpact[0].requests + ' requests, ' + thirdPartyImpact[0].totalTime + 'ms)');
    }

    // === CALCULATE SCORE ===

    var score = 100;

    // Deduct for Core Web Vitals
    if (coreWebVitals.lcp.rating === 'poor') score -= 20;
    else if (coreWebVitals.lcp.rating === 'needs-improvement') score -= 10;

    if (coreWebVitals.fcp.rating === 'poor') score -= 15;
    else if (coreWebVitals.fcp.rating === 'needs-improvement') score -= 7;

    if (coreWebVitals.cls.rating === 'poor') score -= 15;
    else if (coreWebVitals.cls.rating === 'needs-improvement') score -= 7;

    // Deduct for issues
    var errorCount = fixable.filter(function(f) { return f.severity === 'error'; }).length;
    var warningCount = fixable.filter(function(f) { return f.severity === 'warning'; }).length;

    score -= errorCount * 5;
    score -= warningCount * 2;
    score = Math.max(0, Math.min(100, score));

    // Grade (canonical shared A-F scale — the old E band is gone)
    var grade = auditUtils.calculateGrade(score);

    // === GENERATE SUMMARY ===

    var summaryParts = [];
    if (lcp) {
      summaryParts.push('LCP ' + (lcp / 1000).toFixed(1) + 's (' + coreWebVitals.lcp.rating + ')');
    }
    if (blockingScripts.length > 0) {
      summaryParts.push(blockingScripts.length + ' render-blocking script' + (blockingScripts.length > 1 ? 's' : ''));
    }
    if (unoptimizedCount > 0) {
      summaryParts.push(unoptimizedCount + ' unoptimized image' + (unoptimizedCount > 1 ? 's' : ''));
    }
    var summary = summaryParts.join('. ') || 'Performance audit complete';

    // === STATS ===

    var stats = {
      errors: errorCount,
      warnings: warningCount,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
    // Returns grouped data optimized for AI processing
    if (!raw) {
      // Build blocking script details for AI decision-making
      var blockingScriptDetails = [];
      for (var bs = 0; bs < blockingScripts.length; bs++) {
        var bsEl = blockingScripts[bs];
        var bsSrc = bsEl.getAttribute('src') || '';
        blockingScriptDetails.push({
          src: bsSrc,
          selector: getSelector(bsEl),
          isExternal: bsSrc.indexOf('//') !== -1 || bsSrc.indexOf('http') === 0,
          domain: getDomain(bsSrc.indexOf('//') === 0 ? 'https:' + bsSrc : bsSrc)
        });
      }

      // Build unoptimized image details
      var unoptimizedImages = fixable.filter(function(f) {
        return f.type === 'unoptimized-image';
      }).map(function(img) {
        return {
          selector: img.selector,
          size: img.size,
          dimensions: img.dimensions,
          severity: img.severity
        };
      });

      // Resource summary by type
      var resourceSummary = {
        script: { count: resourcesByType.script.length, totalSize: 0 },
        css: { count: resourcesByType.css.length, totalSize: 0 },
        img: { count: resourcesByType.img.length, totalSize: 0 },
        font: { count: resourcesByType.font.length, totalSize: 0 }
      };
      ['script', 'css', 'img', 'font'].forEach(function(type) {
        resourcesByType[type].forEach(function(r) {
          resourceSummary[type].totalSize += r.size || 0;
        });
        resourceSummary[type].totalSizeFormatted = formatBytes(resourceSummary[type].totalSize);
      });

      return {
        audit: 'performance',
        summary: summary,
        score: score,
        grade: grade,
        raw: {
          coreWebVitals: coreWebVitals,
          blockingScripts: blockingScriptDetails,
          unoptimizedImages: unoptimizedImages,
          thirdPartyImpact: thirdPartyImpact.slice(0, 10),
          slowestResources: slowestResources.slice(0, 5),
          resourceSummary: resourceSummary,
          fontCount: fontFaces.length,
          hasFontDisplaySwap: hasSwap,
          inaccessibleStylesheets: inaccessibleSheets
        },
        automationHints: {
          lookFor: [
            'bundler config (webpack, vite, rollup) for script optimization',
            'image processing/CDN setup (imagemin, sharp, cloudinary)',
            'existing async/defer patterns in HTML templates',
            'critical CSS extraction configuration',
            'font loading strategy (preload, font-display)'
          ],
          suggestionsNeeded: [
            'which scripts can safely use async vs defer',
            'image optimization pipeline recommendations',
            'third-party script evaluation (keep/defer/remove)',
            'resource preloading priorities'
          ]
        }
      };
    }

    // === RAW RESPONSE (raw: true) ===
    // Returns verbose detailed format with all issues and context
    var response = {
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      coreWebVitals: coreWebVitals,
      fixable: fixable,
      informational: informational,
      slowestResources: slowestResources,
      thirdPartyImpact: thirdPartyImpact.slice(0, 10),
      actions: actions,
      stats: stats
    };

    // Legacy compatibility: include detailLevel
    response.detailLevel = detailLevel;

    // Memory info if available
    if (perf.memory) {
      response.memory = {
        usedJSHeapSize: Math.round(perf.memory.usedJSHeapSize / 1024 / 1024),
        totalJSHeapSize: Math.round(perf.memory.totalJSHeapSize / 1024 / 1024),
        jsHeapSizeLimit: Math.round(perf.memory.jsHeapSizeLimit / 1024 / 1024)
      };
    }

    return response;
  }

  window.__devtool_audit_performance = {
    auditPerformance: auditPerformance
  };
})();

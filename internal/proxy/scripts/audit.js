// Quality audit primitives for DevTool
// DOM complexity, CSS, security, and page quality audits

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  // --- Compact Response Helpers ---
  function truncateString(str, maxLength) {
    if (!str || typeof str !== 'string') return str;
    if (str.length <= maxLength) return str;
    return str.substring(0, maxLength) + '...';
  }

  function truncateUrl(url, maxLength) {
    if (!url || typeof url !== 'string') return url;
    if (url.length <= maxLength) return url;
    // Keep protocol + domain + last part of path
    try {
      var u = new URL(url);
      var base = u.protocol + '//' + u.host;
      var remaining = maxLength - base.length - 4; // 4 for "..."
      if (remaining > 10) {
        return base + '/...' + u.pathname.slice(-remaining);
      }
      return base + '/...';
    } catch (e) {
      return truncateString(url, maxLength);
    }
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  function auditDOMComplexity(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var elements = document.querySelectorAll('*');
    var depth = 0;
    var maxDepth = 0;

    function calculateDepth(el) {
      var d = 0;
      var current = el;
      while (current.parentElement) {
        d++;
        current = current.parentElement;
      }
      return d;
    }

    for (var i = 0; i < elements.length; i++) {
      var d = calculateDepth(elements[i]);
      if (d > maxDepth) maxDepth = d;
    }

    var duplicateIds = [];
    var ids = {};
    var elementsWithId = document.querySelectorAll('[id]');
    for (var j = 0; j < elementsWithId.length; j++) {
      var id = elementsWithId[j].id;
      if (ids[id]) {
        duplicateIds.push(id);
      }
      ids[id] = true;
    }

    var response = {
      totalElements: elements.length,
      maxDepth: maxDepth,
      elementsWithId: elementsWithId.length,
      forms: document.forms.length,
      images: document.images.length,
      links: document.links.length,
      scripts: document.scripts.length,
      stylesheets: document.styleSheets.length,
      iframes: document.querySelectorAll('iframe').length
    };

    // Only include duplicateIds array in compact/full mode
    if (detailLevel !== 'summary') {
      response.duplicateIds = duplicateIds;
    } else {
      response.duplicateIdCount = duplicateIds.length;
    }

    return response;
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  function auditCSS(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var issues = [];
    var inlineStyles = document.querySelectorAll('[style]');

    if (inlineStyles.length > 10) {
      issues.push({
        type: 'excessive-inline-styles',
        severity: 'warning',
        count: inlineStyles.length,
        message: 'Many elements with inline styles (' + inlineStyles.length + ')'
      });
    }

    // Check for !important usage
    var importantCount = 0;
    for (var i = 0; i < document.styleSheets.length; i++) {
      try {
        var rules = document.styleSheets[i].cssRules || [];
        for (var j = 0; j < rules.length; j++) {
          if (rules[j].cssText && rules[j].cssText.indexOf('!important') !== -1) {
            importantCount++;
          }
        }
      } catch (e) {
        // Cross-origin stylesheets can't be accessed
      }
    }

    if (importantCount > 5) {
      issues.push({
        type: 'excessive-important',
        severity: 'warning',
        count: importantCount,
        message: 'Many !important declarations (' + importantCount + ')'
      });
    }

    var response = {
      detailLevel: detailLevel,
      count: issues.length,
      inlineStyleCount: inlineStyles.length,
      importantCount: importantCount,
      stylesheetCount: document.styleSheets.length
    };

    if (detailLevel === 'summary') {
      // Summary: counts only
      return response;
    } else {
      // Compact and full: include issues (already small)
      response.issues = issues.slice(0, maxIssues);
      if (issues.length > maxIssues) {
        response.truncated = true;
      }
    }

    return response;
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   maxUrlLength: number (default: 80)
  function auditSecurity(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var maxUrlLength = options.maxUrlLength || 80;
    var issues = [];

    // Check for HTTP resources on HTTPS page
    if (window.location.protocol === 'https:') {
      var mixedContent = [];

      var scripts = document.querySelectorAll('script[src^="http:"]');
      for (var i = 0; i < scripts.length; i++) {
        mixedContent.push({
          type: 'script',
          url: scripts[i].src
        });
      }

      var links = document.querySelectorAll('link[href^="http:"]');
      for (var j = 0; j < links.length; j++) {
        mixedContent.push({
          type: 'stylesheet',
          url: links[j].href
        });
      }

      var images = document.querySelectorAll('img[src^="http:"]');
      for (var k = 0; k < images.length; k++) {
        mixedContent.push({
          type: 'image',
          url: images[k].src
        });
      }

      if (mixedContent.length > 0) {
        var compactResources = mixedContent.map(function(r) {
          return {
            type: r.type,
            url: detailLevel === 'full' ? r.url : truncateUrl(r.url, maxUrlLength)
          };
        });
        issues.push({
          type: 'mixed-content',
          severity: 'error',
          resourceCount: mixedContent.length,
          resources: detailLevel === 'summary' ? undefined : compactResources.slice(0, 10),
          message: 'Mixed content detected (' + mixedContent.length + ' HTTP resources)'
        });
      }
    }

    // Check for forms without HTTPS action
    var forms = document.querySelectorAll('form[action^="http:"]');
    if (forms.length > 0) {
      issues.push({
        type: 'insecure-form',
        severity: 'error',
        count: forms.length,
        message: 'Forms with insecure (HTTP) action URLs'
      });
    }

    // Check for target="_blank" without rel="noopener"
    var unsafeLinks = document.querySelectorAll('a[target="_blank"]:not([rel*="noopener"])');
    if (unsafeLinks.length > 0) {
      issues.push({
        type: 'missing-noopener',
        severity: 'warning',
        count: unsafeLinks.length,
        message: 'Links with target="_blank" missing rel="noopener"'
      });
    }

    // Check for autocomplete on password fields
    var passwordFields = document.querySelectorAll('input[type="password"][autocomplete="on"]');
    if (passwordFields.length > 0) {
      issues.push({
        type: 'password-autocomplete',
        severity: 'warning',
        count: passwordFields.length,
        message: 'Password fields with autocomplete enabled'
      });
    }

    var totalCount = issues.length;
    var errorCount = issues.filter(function(i) { return i.severity === 'error'; }).length;
    var warningCount = issues.filter(function(i) { return i.severity === 'warning'; }).length;

    var response = {
      detailLevel: detailLevel,
      count: totalCount,
      errors: errorCount,
      warnings: warningCount
    };

    if (detailLevel === 'summary') {
      // Summary: counts only
      return response;
    } else {
      // Compact and full: include issues
      response.issues = issues.slice(0, maxIssues);
      if (totalCount > maxIssues) {
        response.truncated = true;
        response.shownIssues = maxIssues;
      }
    }

    return response;
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  function auditPageQuality(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var issues = [];

    // Check for missing meta tags
    if (!document.querySelector('meta[name="viewport"]')) {
      issues.push({
        type: 'missing-viewport',
        severity: 'warning',
        message: 'Missing viewport meta tag'
      });
    }

    if (!document.querySelector('meta[name="description"]')) {
      issues.push({
        type: 'missing-description',
        severity: 'info',
        message: 'Missing meta description'
      });
    }

    // Check document structure
    if (!document.querySelector('h1')) {
      issues.push({
        type: 'missing-h1',
        severity: 'warning',
        message: 'Page missing H1 heading'
      });
    }

    var h1s = document.querySelectorAll('h1');
    if (h1s.length > 1) {
      issues.push({
        type: 'multiple-h1',
        severity: 'info',
        count: h1s.length,
        message: 'Multiple H1 headings found'
      });
    }

    // Check language attribute
    if (!document.documentElement.lang) {
      issues.push({
        type: 'missing-lang',
        severity: 'warning',
        message: 'HTML element missing lang attribute'
      });
    }

    // Check title
    if (!document.title || document.title.trim() === '') {
      issues.push({
        type: 'missing-title',
        severity: 'error',
        message: 'Page missing or empty title'
      });
    }

    var totalCount = issues.length;
    var errorCount = issues.filter(function(i) { return i.severity === 'error'; }).length;
    var warningCount = issues.filter(function(i) { return i.severity === 'warning'; }).length;
    var infoCount = issues.filter(function(i) { return i.severity === 'info'; }).length;

    var response = {
      detailLevel: detailLevel,
      count: totalCount,
      errors: errorCount,
      warnings: warningCount,
      info: infoCount,
      title: document.title ? truncateString(document.title, 100) : null,
      lang: document.documentElement.lang,
      viewport: document.querySelector('meta[name="viewport"]')?.content
    };

    if (detailLevel === 'summary') {
      // Summary: counts only, no issues array
      return response;
    } else {
      // Compact and full: include issues
      response.issues = issues.slice(0, maxIssues);
      if (totalCount > maxIssues) {
        response.truncated = true;
        response.shownIssues = maxIssues;
      }
    }

    return response;
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxResources: number (default: 20) - limit resource entries
  //   maxUrlLength: number (default: 60) - truncate resource URLs
  function auditPerformance(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxResources = options.maxResources || 20;
    var maxUrlLength = options.maxUrlLength || 60;

    var perf = window.performance;
    if (!perf) {
      return { error: 'Performance API not available', detailLevel: detailLevel };
    }

    var timing = perf.timing || {};
    var navigation = perf.getEntriesByType ? perf.getEntriesByType('navigation')[0] : null;

    // Calculate key timings
    var pageLoad = timing.loadEventEnd - timing.navigationStart;
    var domReady = timing.domContentLoadedEventEnd - timing.navigationStart;
    var firstByte = timing.responseStart - timing.navigationStart;
    var dnsLookup = timing.domainLookupEnd - timing.domainLookupStart;
    var tcpConnect = timing.connectEnd - timing.connectStart;
    var serverResponse = timing.responseEnd - timing.requestStart;
    var domParsing = timing.domComplete - timing.domLoading;

    // Get paint timing
    var paintEntries = perf.getEntriesByType ? perf.getEntriesByType('paint') : [];
    var fcp = null;
    var fp = null;
    for (var i = 0; i < paintEntries.length; i++) {
      if (paintEntries[i].name === 'first-contentful-paint') fcp = Math.round(paintEntries[i].startTime);
      if (paintEntries[i].name === 'first-paint') fp = Math.round(paintEntries[i].startTime);
    }

    // Get LCP if available
    var lcp = null;
    try {
      var lcpEntries = perf.getEntriesByType ? perf.getEntriesByType('largest-contentful-paint') : [];
      if (lcpEntries.length > 0) {
        lcp = Math.round(lcpEntries[lcpEntries.length - 1].startTime);
      }
    } catch (e) {
      // LCP may not be available
    }

    var response = {
      detailLevel: detailLevel,
      metrics: {
        pageLoad: pageLoad > 0 ? pageLoad : null,
        domContentLoaded: domReady > 0 ? domReady : null,
        firstByte: firstByte > 0 ? firstByte : null,
        firstPaint: fp,
        firstContentfulPaint: fcp,
        largestContentfulPaint: lcp,
        dnsLookup: dnsLookup > 0 ? dnsLookup : null,
        tcpConnect: tcpConnect > 0 ? tcpConnect : null,
        serverResponse: serverResponse > 0 ? serverResponse : null,
        domParsing: domParsing > 0 ? domParsing : null
      }
    };

    // Performance thresholds (Core Web Vitals and best practices)
    var issues = [];
    if (fcp && fcp > 2500) {
      issues.push({
        type: 'slow-fcp',
        severity: fcp > 4000 ? 'error' : 'warning',
        value: fcp,
        threshold: 2500,
        message: 'First Contentful Paint is slow (' + fcp + 'ms, target <2500ms)'
      });
    }
    if (lcp && lcp > 2500) {
      issues.push({
        type: 'slow-lcp',
        severity: lcp > 4000 ? 'error' : 'warning',
        value: lcp,
        threshold: 2500,
        message: 'Largest Contentful Paint is slow (' + lcp + 'ms, target <2500ms)'
      });
    }
    if (firstByte > 600) {
      issues.push({
        type: 'slow-ttfb',
        severity: firstByte > 1800 ? 'error' : 'warning',
        value: firstByte,
        threshold: 600,
        message: 'Time to First Byte is slow (' + firstByte + 'ms, target <600ms)'
      });
    }
    if (pageLoad > 3000) {
      issues.push({
        type: 'slow-page-load',
        severity: pageLoad > 10000 ? 'error' : 'warning',
        value: pageLoad,
        threshold: 3000,
        message: 'Page load time is slow (' + pageLoad + 'ms, target <3000ms)'
      });
    }

    response.issues = issues;
    response.issueCount = issues.length;
    response.errors = issues.filter(function(i) { return i.severity === 'error'; }).length;
    response.warnings = issues.filter(function(i) { return i.severity === 'warning'; }).length;

    // Summary mode: just metrics and issue counts
    if (detailLevel === 'summary') {
      delete response.issues; // Keep only issueCount
      return response;
    }

    // Get resource timing for compact/full modes
    var resources = perf.getEntriesByType ? perf.getEntriesByType('resource') : [];
    var resourceStats = {
      total: resources.length,
      byType: {}
    };

    // Categorize resources
    var resourceList = [];
    for (var j = 0; j < resources.length; j++) {
      var r = resources[j];
      var type = r.initiatorType || 'other';
      resourceStats.byType[type] = (resourceStats.byType[type] || 0) + 1;

      resourceList.push({
        type: type,
        url: detailLevel === 'full' ? r.name : truncateUrl(r.name, maxUrlLength),
        duration: Math.round(r.duration),
        size: r.transferSize || 0
      });
    }

    // Sort by duration (slowest first)
    resourceList.sort(function(a, b) { return b.duration - a.duration; });

    response.resources = {
      total: resources.length,
      byType: resourceStats.byType
    };

    // Include resource list in compact/full mode
    if (detailLevel === 'compact') {
      response.resources.slowest = resourceList.slice(0, maxResources);
      if (resources.length > maxResources) {
        response.resources.truncated = true;
      }
    } else {
      // Full mode: all resources
      response.resources.all = resourceList;
    }

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

  // Export audit functions
  window.__devtool_audit = {
    auditDOMComplexity: auditDOMComplexity,
    auditCSS: auditCSS,
    auditSecurity: auditSecurity,
    auditPageQuality: auditPageQuality,
    auditPerformance: auditPerformance
  };
})();

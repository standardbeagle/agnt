// Page quality audit and unified audit runner

(function() {
  'use strict';

  var utils = window.__devtool_utils;

  /**
   * Generate a stable 8-char hex finding ID from type, selector, and message.
   * FNV-1a 32-bit hash — same inputs always produce same output across runs.
   */
  function computeFindingID(type, selector, message) {
    var input = type + '\x00' + (selector || '') + '\x00' + (message || '');
    var h = 0x811c9dc5;
    for (var i = 0; i < input.length; i++) {
      h = h ^ input.charCodeAt(i);
      h = (h + (h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24)) >>> 0;
    }
    return ('00000000' + h.toString(16)).slice(-8);
  }

  function registerFinding(id, selector) {
    if (!window.__devtool) { window.__devtool = {}; }
    if (!window.__devtool.audit) { window.__devtool.audit = {}; }
    if (!window.__devtool.audit.findingSelectors) { window.__devtool.audit.findingSelectors = {}; }
    window.__devtool.audit.findingSelectors[id] = selector || '';
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  function auditPageQuality(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var raw = options.raw === true; // Default: false (AI-optimized format)

    // Initialize tracking arrays
    var fixable = [];
    var informational = [];
    var checksRun = [];
    var actions = [];
    var score = 100;

    // Helper to get meta tag content
    function getMetaContent(name, property) {
      var selector = property ? 'meta[property="' + name + '"]' : 'meta[name="' + name + '"]';
      var meta = document.querySelector(selector);
      return meta ? meta.getAttribute('content') : null;
    }

    // Helper to calculate grade from score
    function calculateGrade(s) {
      if (s >= 97) return 'A+';
      if (s >= 93) return 'A';
      if (s >= 90) return 'A-';
      if (s >= 87) return 'B+';
      if (s >= 83) return 'B';
      if (s >= 80) return 'B-';
      if (s >= 77) return 'C+';
      if (s >= 73) return 'C';
      if (s >= 70) return 'C-';
      if (s >= 67) return 'D+';
      if (s >= 63) return 'D';
      if (s >= 60) return 'D-';
      return 'F';
    }

    // === META TAG ANALYSIS ===
    checksRun.push('meta-tags');
    var meta = {};

    // Title analysis
    var title = document.title || '';
    var titleLength = title.length;
    var titleOptimal = titleLength >= 50 && titleLength <= 60;
    meta.title = {
      value: title,
      length: titleLength,
      optimal: titleOptimal
    };

    if (!title) {
      score -= 10;
      var titleMissingMsg = 'Add a descriptive page title';
      fixable.push({
        id: computeFindingID('missing-title', 'head > title', titleMissingMsg),
        type: 'missing-title',
        severity: 'error',
        impact: 10,
        fix: titleMissingMsg
      });
      actions.push(titleMissingMsg);
    } else if (titleLength < 30) {
      score -= 3;
      var titleShortMsg = 'Title is ' + titleLength + ' chars (optimal: 50-60)';
      informational.push({
        id: computeFindingID('title-length', 'head > title', titleShortMsg),
        type: 'title-length',
        severity: 'info',
        message: titleShortMsg,
        current: titleLength,
        optimal: '50-60'
      });
    } else if (titleLength > 60) {
      score -= 2;
      meta.title.issue = 'too long';
      var titleLongMsg = 'Title is ' + titleLength + ' chars (optimal: 50-60)';
      informational.push({
        id: computeFindingID('title-length', 'head > title', titleLongMsg),
        type: 'title-length',
        severity: 'info',
        message: titleLongMsg,
        current: titleLength,
        optimal: '50-60'
      });
      actions.push('Shorten title from ' + titleLength + ' to 50-60 characters');
    }

    // Description analysis
    var description = getMetaContent('description');
    if (description) {
      var descLength = description.length;
      var descOptimal = descLength >= 150 && descLength <= 160;
      meta.description = {
        value: description,
        length: descLength,
        optimal: descOptimal
      };

      if (descLength < 120) {
        score -= 2;
        meta.description.issue = 'too short';
        var descShortMsg = 'Meta description is ' + descLength + ' chars (optimal: 150-160)';
        informational.push({
          id: computeFindingID('meta-description-length', 'meta[name="description"]', descShortMsg),
          type: 'meta-description-length',
          severity: 'info',
          message: descShortMsg,
          current: descLength,
          optimal: '150-160'
        });
      } else if (descLength > 160) {
        score -= 2;
        meta.description.issue = 'too long';
        var descLongMsg = 'Meta description is ' + descLength + ' chars (optimal: 150-160)';
        informational.push({
          id: computeFindingID('meta-description-length', 'meta[name="description"]', descLongMsg),
          type: 'meta-description-length',
          severity: 'info',
          message: descLongMsg,
          current: descLength,
          optimal: '150-160'
        });
        actions.push('Shorten meta description from ' + descLength + ' to 150-160 characters');
      }
    } else {
      score -= 5;
      meta.description = { present: false };
      var descMissingMsg = 'Add meta description (150-160 chars)';
      fixable.push({
        id: computeFindingID('missing-description', 'meta[name="description"]', descMissingMsg),
        type: 'missing-description',
        severity: 'warning',
        impact: 5,
        fix: descMissingMsg
      });
      actions.push(descMissingMsg);
    }

    // Canonical URL
    var canonical = document.querySelector('link[rel="canonical"]');
    if (canonical) {
      var canonicalUrl = canonical.href;
      var selfReferencing = canonicalUrl === window.location.href;
      meta.canonical = {
        present: true,
        value: canonicalUrl,
        selfReferencing: selfReferencing
      };
      if (!selfReferencing) {
        informational.push({
          id: computeFindingID('canonical-external', 'link[rel="canonical"]', 'Canonical URL points to different page'),
          type: 'canonical-external',
          severity: 'info',
          message: 'Canonical URL points to different page',
          canonical: canonicalUrl,
          current: window.location.href
        });
      }
    } else {
      score -= 3;
      meta.canonical = { present: false };
      var canonicalMissingMsg = 'Add canonical link tag';
      fixable.push({
        id: computeFindingID('missing-canonical', 'link[rel="canonical"]', canonicalMissingMsg),
        type: 'missing-canonical',
        severity: 'warning',
        impact: 3,
        fix: canonicalMissingMsg
      });
      actions.push(canonicalMissingMsg);
    }

    // Robots meta
    var robots = getMetaContent('robots');
    if (robots) {
      meta.robots = { present: true, value: robots };
      if (robots.indexOf('noindex') !== -1) {
        informational.push({
          id: computeFindingID('robots-noindex', 'meta[name="robots"]', 'Page is set to noindex'),
          type: 'robots-noindex',
          severity: 'info',
          message: 'Page is set to noindex'
        });
      }
    } else {
      meta.robots = { present: false };
    }

    // Viewport
    var viewport = getMetaContent('viewport');
    if (viewport) {
      meta.viewport = { present: true, value: viewport };
    } else {
      score -= 8;
      meta.viewport = { present: false };
      var viewportMsg = 'Add viewport meta tag: <meta name="viewport" content="width=device-width, initial-scale=1">';
      fixable.push({
        id: computeFindingID('missing-viewport', 'meta[name="viewport"]', viewportMsg),
        type: 'missing-viewport',
        severity: 'error',
        impact: 8,
        fix: viewportMsg
      });
      actions.push('Add viewport meta tag for mobile optimization');
    }

    // Hreflang
    var hreflangLinks = document.querySelectorAll('link[rel="alternate"][hreflang]');
    if (hreflangLinks.length > 0) {
      var hreflangLangs = [];
      for (var i = 0; i < hreflangLinks.length; i++) {
        hreflangLangs.push(hreflangLinks[i].getAttribute('hreflang'));
      }
      meta.hreflang = { present: true, count: hreflangLinks.length, languages: hreflangLangs };
    } else {
      meta.hreflang = { present: false };
    }

    // === OPEN GRAPH TAGS ===
    checksRun.push('open-graph');
    var ogTags = ['og:title', 'og:description', 'og:image', 'og:url', 'og:type'];
    var ogPresent = [];
    var ogMissing = [];

    for (var j = 0; j < ogTags.length; j++) {
      if (getMetaContent(ogTags[j], true)) {
        ogPresent.push(ogTags[j]);
      } else {
        ogMissing.push(ogTags[j]);
      }
    }

    var openGraph = {
      complete: ogMissing.length === 0,
      present: ogPresent,
      missing: ogMissing
    };

    if (ogMissing.length > 0) {
      var ogImpact = Math.min(ogMissing.length * 2, 8);
      score -= ogImpact;
      var ogMsg = 'Add Open Graph meta tags: ' + ogMissing.join(', ');
      fixable.push({
        id: computeFindingID('missing-og-tags', 'meta[property]', ogMsg),
        type: 'missing-og-tags',
        severity: 'warning',
        impact: ogImpact,
        missing: ogMissing,
        fix: ogMsg
      });
      actions.push('Add Open Graph meta tags for social sharing (' + ogMissing.join(', ') + ')');
    }

    // === TWITTER CARD TAGS ===
    checksRun.push('twitter-card');
    var twitterTags = ['twitter:card', 'twitter:title', 'twitter:description', 'twitter:image'];
    var twitterPresent = [];
    var twitterMissing = [];

    for (var k = 0; k < twitterTags.length; k++) {
      if (getMetaContent(twitterTags[k])) {
        twitterPresent.push(twitterTags[k]);
      } else {
        twitterMissing.push(twitterTags[k]);
      }
    }

    var twitterCard = {
      complete: twitterMissing.length === 0,
      present: twitterPresent,
      missing: twitterMissing
    };

    if (twitterMissing.length > 0) {
      var twitterImpact = Math.min(twitterMissing.length * 2, 6);
      score -= twitterImpact;
      var twitterMsg = 'Add Twitter Card meta tags: ' + twitterMissing.join(', ');
      fixable.push({
        id: computeFindingID('missing-twitter-tags', 'meta[name^="twitter:"]', twitterMsg),
        type: 'missing-twitter-tags',
        severity: 'warning',
        impact: twitterImpact,
        missing: twitterMissing,
        fix: twitterMsg
      });
      actions.push('Add Twitter Card meta tags (' + twitterMissing.join(', ') + ')');
    }

    // === STRUCTURED DATA ===
    checksRun.push('structured-data');
    var jsonLdScripts = document.querySelectorAll('script[type="application/ld+json"]');
    var structuredData = {
      present: jsonLdScripts.length > 0,
      types: [],
      valid: true
    };

    if (jsonLdScripts.length > 0) {
      for (var l = 0; l < jsonLdScripts.length; l++) {
        try {
          var jsonLd = JSON.parse(jsonLdScripts[l].textContent);
          if (jsonLd['@type']) {
            structuredData.types.push(jsonLd['@type']);
          } else if (jsonLd['@graph']) {
            for (var m = 0; m < jsonLd['@graph'].length; m++) {
              if (jsonLd['@graph'][m]['@type']) {
                structuredData.types.push(jsonLd['@graph'][m]['@type']);
              }
            }
          }
        } catch (e) {
          structuredData.valid = false;
          var sdInvalidMsg = 'Fix malformed JSON-LD structured data';
          fixable.push({
            id: computeFindingID('invalid-structured-data', 'script[type="application/ld+json"]', sdInvalidMsg),
            type: 'invalid-structured-data',
            severity: 'error',
            impact: 5,
            fix: sdInvalidMsg
          });
          actions.push(sdInvalidMsg);
          score -= 5;
        }
      }
    } else {
      var sdMissingMsg = 'No JSON-LD structured data found (recommended for rich results)';
      informational.push({
        id: computeFindingID('missing-structured-data', 'script[type="application/ld+json"]', sdMissingMsg),
        type: 'missing-structured-data',
        severity: 'info',
        message: sdMissingMsg
      });
    }

    // === CONTENT ANALYSIS ===
    checksRun.push('content-quality');

    // Heading hierarchy
    var headings = document.querySelectorAll('h1, h2, h3, h4, h5, h6');
    var headingLevels = [];
    var headingValid = true;
    var previousLevel = 0;

    for (var n = 0; n < headings.length; n++) {
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(headings[n])) continue;
      var level = parseInt(headings[n].tagName.substring(1));
      headingLevels.push('h' + level);

      if (previousLevel > 0 && level > previousLevel + 1) {
        headingValid = false;
      }
      previousLevel = level;
    }

    var h1Count = document.querySelectorAll('h1').length;
    if (h1Count === 0) {
      score -= 5;
      var h1MissingMsg = 'Add H1 heading to page';
      fixable.push({
        id: computeFindingID('missing-h1', 'h1', h1MissingMsg),
        type: 'missing-h1',
        severity: 'warning',
        impact: 5,
        fix: h1MissingMsg
      });
      actions.push(h1MissingMsg);
    } else if (h1Count > 1) {
      score -= 2;
      var h1MultiMsg = 'Multiple H1 headings found (' + h1Count + ')';
      informational.push({
        id: computeFindingID('multiple-h1', 'h1', h1MultiMsg),
        type: 'multiple-h1',
        severity: 'info',
        message: h1MultiMsg,
        count: h1Count
      });
    }

    if (!headingValid) {
      score -= 3;
      var headingMsg = 'Fix heading hierarchy (no skipped levels)';
      fixable.push({
        id: computeFindingID('heading-hierarchy', 'h1,h2,h3,h4,h5,h6', headingMsg),
        type: 'heading-hierarchy',
        severity: 'warning',
        impact: 3,
        fix: headingMsg
      });
      actions.push(headingMsg);
    }

    // Alt text coverage
    var images = document.querySelectorAll('img');
    var imagesWithAlt = document.querySelectorAll('img[alt]');
    var altCoverage = images.length > 0 ? Math.round((imagesWithAlt.length / images.length) * 100) : 100;
    var missingAlt = images.length - imagesWithAlt.length;

    if (missingAlt > 0) {
      var altImpact = Math.min(missingAlt * 2, 10);
      score -= altImpact;
      var altSel = 'img:not([alt])';
      var altMsg = 'Add descriptive alt text to ' + missingAlt + ' image' + (missingAlt > 1 ? 's' : '');
      var altID = computeFindingID('missing-alt', altSel, altMsg);
      registerFinding(altID, altSel);
      fixable.push({
        id: altID,
        type: 'missing-alt',
        severity: 'warning',
        impact: altImpact,
        selector: altSel,
        count: missingAlt,
        fix: altMsg
      });
      actions.push('Add alt text to ' + missingAlt + ' image' + (missingAlt > 1 ? 's' : ''));
    }

    // Link text quality
    var links = document.querySelectorAll('a[href]');
    var genericTerms = ['click here', 'read more', 'learn more', 'more', 'here', 'link', 'click'];
    var genericLinks = [];

    for (var p = 0; p < links.length; p++) {
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(links[p])) continue;
      var linkText = (links[p].textContent || '').trim().toLowerCase();
      for (var q = 0; q < genericTerms.length; q++) {
        if (linkText === genericTerms[q]) {
          genericLinks.push(linkText);
          break;
        }
      }
    }

    if (genericLinks.length > 0) {
      var linkImpact = Math.min(genericLinks.length, 5);
      score -= linkImpact;
      var linkMsg = 'Improve generic link text (' + genericLinks.length + ' instance' + (genericLinks.length > 1 ? 's' : '') + ')';
      var linkSel = 'a[href]';
      var linkID = computeFindingID('generic-link-text', linkSel, linkMsg);
      registerFinding(linkID, linkSel);
      fixable.push({
        id: linkID,
        type: 'generic-link-text',
        severity: 'warning',
        impact: linkImpact,
        selector: linkSel,
        count: genericLinks.length,
        fix: linkMsg
      });
      actions.push(linkMsg);
    }

    // Content-to-code ratio (rough estimate)
    var bodyText = (document.body.textContent || '').trim();
    var textLength = bodyText.length;
    var htmlLength = document.documentElement.outerHTML.length;
    var contentRatio = htmlLength > 0 ? Math.round((textLength / htmlLength) * 100) : 0;

    if (contentRatio < 10 && textLength > 100) {
      score -= 3;
      var ratioMsg = 'Low content-to-code ratio (' + contentRatio + '%)';
      informational.push({
        id: computeFindingID('low-content-ratio', 'body', ratioMsg),
        type: 'low-content-ratio',
        severity: 'info',
        message: ratioMsg,
        ratio: contentRatio
      });
    }

    var contentAnalysis = {
      headingStructure: {
        valid: headingValid,
        levels: headingLevels
      },
      altTextCoverage: {
        total: images.length,
        withAlt: imagesWithAlt.length,
        percentage: altCoverage
      },
      linkTextQuality: {
        total: links.length,
        generic: genericLinks.length,
        genericLinks: genericLinks.slice(0, 10)
      },
      contentToCodeRatio: contentRatio
    };

    // === TECHNICAL SEO ===
    checksRun.push('technical-seo');

    // Language attribute
    if (!document.documentElement.lang) {
      score -= 4;
      var langMsg = 'Add lang attribute to <html> element';
      fixable.push({
        id: computeFindingID('missing-lang', 'html', langMsg),
        type: 'missing-lang',
        severity: 'warning',
        impact: 4,
        fix: langMsg
      });
      actions.push(langMsg);
    }

    // Crawlable links
    var uncrawlableLinks = document.querySelectorAll('a[href^="javascript:"], a[href="#"]:not([href="#"])');
    var jsVoidLinks = document.querySelectorAll('a[href="javascript:void(0)"]');
    var totalUncrawlable = uncrawlableLinks.length;

    if (totalUncrawlable > 0) {
      var crawlImpact = Math.min(totalUncrawlable, 5);
      score -= crawlImpact;
      var crawlSel = 'a[href^="javascript:"], a[href="#"]';
      var crawlMsg = 'Replace ' + totalUncrawlable + ' non-crawlable link' + (totalUncrawlable > 1 ? 's' : '') + ' with proper URLs';
      var crawlID = computeFindingID('uncrawlable-links', crawlSel, crawlMsg);
      registerFinding(crawlID, crawlSel);
      fixable.push({
        id: crawlID,
        type: 'uncrawlable-links',
        severity: 'warning',
        impact: crawlImpact,
        selector: crawlSel,
        count: totalUncrawlable,
        fix: crawlMsg
      });
      actions.push(crawlMsg);
    }

    // Image optimization hints
    var webpImages = document.querySelectorAll('img[src$=".webp"]');
    var lazyImages = document.querySelectorAll('img[loading="lazy"]');
    var lazyPercentage = images.length > 0 ? Math.round((lazyImages.length / images.length) * 100) : 0;

    if (images.length > 5 && lazyPercentage < 50) {
      var lazyMsg = 'Only ' + lazyPercentage + '% of images use lazy loading';
      informational.push({
        id: computeFindingID('low-lazy-loading', 'img', lazyMsg),
        type: 'low-lazy-loading',
        severity: 'info',
        message: lazyMsg,
        percentage: lazyPercentage
      });
    }

    // === CALCULATE FINAL SCORE AND GRADE ===
    score = Math.max(0, Math.min(100, score));
    var grade = calculateGrade(score);

    // Build summary
    var summaryParts = ['SEO score ' + score + '/100'];
    if (ogMissing.length > 0) {
      summaryParts.push('Missing OG tags: ' + ogMissing.join(', '));
    }
    if (missingAlt > 0) {
      summaryParts.push(missingAlt + ' image' + (missingAlt > 1 ? 's' : '') + ' without alt');
    }
    if (genericLinks.length > 0) {
      summaryParts.push(genericLinks.length + ' generic link' + (genericLinks.length > 1 ? 's' : ''));
    }
    var summary = summaryParts.join('. ');

    // Build stats
    var stats = {
      errors: fixable.filter(function(f) { return f.severity === 'error'; }).length,
      warnings: fixable.filter(function(f) { return f.severity === 'warning'; }).length,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
    // Returns grouped data for AI to generate context-aware SEO recommendations
    if (!raw) {
      // Collect missing elements for AI to generate content
      var missingElements = [];
      if (!meta.title.value) missingElements.push('title');
      if (!meta.description.value) missingElements.push('meta description');
      if (!meta.canonical) missingElements.push('canonical URL');
      missingElements = missingElements.concat(ogMissing.map(function(t) { return 'og:' + t; }));
      if (!twitterCard.present) missingElements.push('Twitter card tags');

      // Images needing alt text
      var imagesNeedingAlt = [];
      var imgElements = document.querySelectorAll('img:not([alt])');
      for (var ia = 0; ia < Math.min(imgElements.length, 10); ia++) {
        var img = imgElements[ia];
        // Skip agnt/devtool UI elements
        if (utils.isDevtoolElement && utils.isDevtoolElement(img)) continue;
        imagesNeedingAlt.push({
          src: (img.src || '').split('/').pop().split('?')[0] || 'unknown',
          context: img.parentElement ? img.parentElement.tagName.toLowerCase() : 'body'
        });
      }

      return {
        audit: 'seo',
        summary: summary,
        score: score,
        grade: grade,
        checkedAt: new Date().toISOString(),
        stats: stats,
        // Raw data for AI interpretation
        raw: {
          // Current meta values for AI to improve
          currentMeta: {
            title: meta.title.value || null,
            titleLength: meta.title.length,
            description: meta.description ? meta.description.value : null,
            descriptionLength: meta.description ? meta.description.length : 0
          },
          // What's missing for AI to generate
          missingElements: missingElements,
          // Open Graph status
          openGraph: {
            present: ogPresent,
            missing: ogMissing
          },
          // Content for AI to understand page context
          pageContent: {
            headingStructure: contentAnalysis.headingStructure,
            firstH1: document.querySelector('h1') ? document.querySelector('h1').textContent.trim().substring(0, 100) : null,
            bodyTextSample: (document.body.textContent || '').trim().substring(0, 500)
          },
          // Images needing descriptions
          imagesNeedingAlt: imagesNeedingAlt,
          // Links that need fixing
          genericLinkCount: genericLinks.length,
          uncrawlableLinkCount: totalUncrawlable,
          // Structured data status
          hasStructuredData: structuredData.present
        },
        // Hints for AI - what to look for in codebase
        automationHints: {
          lookFor: [
            'page templates or layouts with meta tag placeholders',
            'SEO configuration files or CMS settings',
            'image alt text patterns in existing code',
            'structured data templates (JSON-LD)'
          ],
          suggestionsNeeded: [
            missingElements.length > 0 ? 'content for ' + missingElements.length + ' missing meta elements' : null,
            imagesNeedingAlt.length > 0 ? 'alt text for ' + imagesNeedingAlt.length + ' images' : null,
            genericLinks.length > 0 ? 'descriptive text for ' + genericLinks.length + ' generic links' : null,
            !structuredData.present ? 'JSON-LD structured data for page' : null
          ].filter(Boolean)
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
      meta: meta,
      openGraph: openGraph,
      twitterCard: twitterCard,
      structuredData: structuredData,
      contentAnalysis: contentAnalysis,
      stats: stats
    };

    // Add fixable and informational based on detail level
    if (detailLevel === 'summary') {
      // Summary: just counts
      response.fixableCount = fixable.length;
      response.informationalCount = informational.length;
      response.actionCount = actions.length;
    } else {
      // Compact and full: include arrays
      response.fixable = fixable;
      response.informational = informational;
      response.actions = actions;
    }

    return response;
  }

  // === UNIFIED AUDIT: auditAll ===
  // Runs all audits and provides a unified report with prioritized actions
  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   includeAccessibility: boolean (default: true) - requires async
  //   raw: boolean - if true, returns verbose detailed format from all audits (default: false, returns AI-optimized format)
  function auditAll(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var includeAccessibility = options.includeAccessibility !== false;
    var raw = options.raw === true; // Default: false (AI-optimized format)

    // Run all synchronous audits (with raw option if requested)
    var auditOpts = raw
      ? { raw: true }
      : { detailLevel: detailLevel };

    var domResult = window.__devtool_audit_dom.auditDOMComplexity(auditOpts);
    var cssResult = window.__devtool_audit_css.auditCSS(auditOpts);
    var securityResult = window.__devtool_audit_security.auditSecurity(auditOpts);
    var seoResult = auditPageQuality(auditOpts);
    var performanceResult = window.__devtool_audit_performance.auditPerformance(auditOpts);
    // API efficiency audit (7th). Synchronous — reads the api-tracker buffer.
    // Guarded: degrades to null if the module isn't loaded.
    var apiResult = (window.__devtool_audit_api && window.__devtool_audit_api.auditAPIEfficiency)
      ? window.__devtool_audit_api.auditAPIEfficiency(auditOpts)
      : null;
    // Loading (spinner) audit (8th). Synchronous — reads the spinner timeline.
    // Guarded: degrades to null if the module isn't loaded.
    var loadingResult = (window.__devtool_audit_loading && window.__devtool_audit_loading.auditLoading)
      ? window.__devtool_audit_loading.auditLoading(auditOpts)
      : null;

    // === AI-OPTIMIZED AGGREGATION (DEFAULT) ===
    // Returns combined grouped data from all audits for AI to generate contextual summaries
    if (!raw) {
      // Run accessibility audit if available (for automation we want all data)
      var accessibilityPromise;
      if (includeAccessibility && window.__devtool_accessibility && window.__devtool_accessibility.auditAccessibility) {
        accessibilityPromise = window.__devtool_accessibility.auditAccessibility({ mode: 'standard' })
          .catch(function() { return null; });
      } else {
        accessibilityPromise = Promise.resolve(null);
      }

      return accessibilityPromise.then(function(accessibilityResult) {
        // Calculate overall scores for prioritization
        var scores = {
          dom: domResult.score,
          css: cssResult.score,
          security: securityResult.score,
          seo: seoResult.score,
          performance: performanceResult.score
        };

        if (apiResult) {
          scores.api = apiResult.score;
        }

        if (loadingResult) {
          scores.loading = loadingResult.score;
        }

        if (accessibilityResult) {
          scores.accessibility = accessibilityResult.score;
        }

        // Find lowest scoring audits (areas needing most attention)
        var priorityOrder = Object.keys(scores).sort(function(a, b) {
          return scores[a] - scores[b];
        });

        // Calculate overall weighted score
        var weights = { security: 1.5, accessibility: 1.3, performance: 1.2, api: 1.1, loading: 1.1, seo: 1.0, dom: 0.8, css: 0.7 };
        var totalWeight = 0;
        var weightedSum = 0;
        for (var auditName in scores) {
          var weight = weights[auditName] || 1.0;
          weightedSum += scores[auditName] * weight;
          totalWeight += weight;
        }
        var overallScore = Math.round(weightedSum / totalWeight);

        // Grade
        var grade = 'F';
        if (overallScore >= 90) grade = 'A';
        else if (overallScore >= 80) grade = 'B';
        else if (overallScore >= 70) grade = 'C';
        else if (overallScore >= 60) grade = 'D';

        // Build summary for AI
        var lowestAudit = priorityOrder[0];
        var summary = 'Overall ' + overallScore + '/100 (' + grade + '). Priority: ' +
          lowestAudit + ' (' + scores[lowestAudit] + ')';

        // Collect all automation hints
        var allLookFor = [];
        var allSuggestionsNeeded = [];
        [domResult, cssResult, securityResult, seoResult, performanceResult, apiResult, loadingResult, accessibilityResult]
          .filter(function(r) { return r && r.automationHints; })
          .forEach(function(r) {
            if (r.automationHints.lookFor) {
              allLookFor = allLookFor.concat(r.automationHints.lookFor);
            }
            if (r.automationHints.suggestionsNeeded) {
              allSuggestionsNeeded = allSuggestionsNeeded.concat(r.automationHints.suggestionsNeeded);
            }
          });

        return {
          audit: 'all',
          summary: summary,
          overallScore: overallScore,
          grade: grade,
          priorityOrder: priorityOrder,
          scores: scores,
          audits: {
            dom: domResult,
            css: cssResult,
            security: securityResult,
            seo: seoResult,
            performance: performanceResult,
            api: apiResult,
            loading: loadingResult,
            accessibility: accessibilityResult
          },
          automationHints: {
            priorityAreas: priorityOrder.slice(0, 3),
            lookFor: allLookFor,
            suggestionsNeeded: allSuggestionsNeeded,
            context: {
              pageUrl: window.location.href,
              pageTitle: document.title,
              doctype: document.doctype ? document.doctype.name : 'unknown'
            }
          }
        };
      });
    }

    // === RAW RESPONSE (raw: true) ===
    // Returns verbose detailed format from all audits
    function combineResults(accessibilityResult) {
      var audits = {
        dom: {
          score: domResult.score,
          grade: domResult.grade,
          errors: domResult.stats.errors,
          warnings: domResult.stats.warnings,
          hotspots: domResult.hotspots ? domResult.hotspots.length : 0
        },
        css: {
          score: cssResult.score,
          grade: cssResult.grade,
          errors: cssResult.stats.errors,
          warnings: cssResult.stats.warnings,
          inlineStyles: cssResult.metrics ? cssResult.metrics.inlineStyleCount : 0
        },
        security: {
          score: securityResult.score,
          grade: securityResult.grade,
          critical: securityResult.stats.critical || 0,
          errors: securityResult.stats.errors,
          warnings: securityResult.stats.warnings
        },
        seo: {
          score: seoResult.score,
          grade: seoResult.grade,
          errors: seoResult.stats.errors,
          warnings: seoResult.stats.warnings
        },
        performance: {
          score: performanceResult.score,
          grade: performanceResult.grade,
          coreWebVitals: performanceResult.coreWebVitals
        }
      };

      if (apiResult) {
        audits.api = {
          score: apiResult.score,
          grade: apiResult.grade,
          findings: apiResult.findings ? apiResult.findings.length : 0
        };
      }

      if (loadingResult) {
        audits.loading = {
          score: loadingResult.score,
          grade: loadingResult.grade,
          findings: loadingResult.findings ? loadingResult.findings.length : 0
        };
      }

      if (accessibilityResult) {
        audits.accessibility = {
          score: accessibilityResult.score,
          grade: accessibilityResult.grade,
          errors: accessibilityResult.stats ? accessibilityResult.stats.errors : 0,
          warnings: accessibilityResult.stats ? accessibilityResult.stats.warnings : 0
        };
      }

      // Calculate overall score (weighted average)
      var weights = {
        security: 1.5,    // Security is critical
        accessibility: 1.3,
        performance: 1.2,
        api: 1.1,
        loading: 1.1,
        seo: 1.0,
        dom: 0.8,
        css: 0.7
      };

      var totalWeight = 0;
      var weightedSum = 0;

      for (var auditName in audits) {
        var weight = weights[auditName] || 1.0;
        weightedSum += audits[auditName].score * weight;
        totalWeight += weight;
      }

      var overallScore = Math.round(weightedSum / totalWeight);

      // Overall grade
      var grade = 'F';
      if (overallScore >= 90) grade = 'A';
      else if (overallScore >= 80) grade = 'B';
      else if (overallScore >= 70) grade = 'C';
      else if (overallScore >= 60) grade = 'D';

      // Collect all fixable issues with audit source
      var allFixable = [];

      function addIssues(issues, auditName) {
        if (!issues) return;
        for (var i = 0; i < issues.length; i++) {
          var issue = issues[i];
          allFixable.push({
            audit: auditName,
            id: issue.id,
            type: issue.type,
            severity: issue.severity,
            impact: issue.impact || 5,
            selector: issue.selector,
            message: issue.message,
            fix: issue.fix
          });
        }
      }

      addIssues(domResult.fixable, 'dom');
      addIssues(cssResult.fixable, 'css');
      addIssues(securityResult.fixable, 'security');
      addIssues(seoResult.fixable, 'seo');
      addIssues(performanceResult.fixable, 'performance');
      if (apiResult) {
        addIssues(apiResult.fixable || apiResult.findings, 'api');
      }
      if (loadingResult) {
        addIssues(loadingResult.fixable || loadingResult.findings, 'loading');
      }
      if (accessibilityResult && accessibilityResult.fixable) {
        addIssues(accessibilityResult.fixable, 'accessibility');
      }

      // Sort by impact (highest first), then by severity
      var severityOrder = { critical: 0, error: 1, warning: 2, info: 3 };
      allFixable.sort(function(a, b) {
        if (b.impact !== a.impact) return b.impact - a.impact;
        return (severityOrder[a.severity] || 4) - (severityOrder[b.severity] || 4);
      });

      // Generate prioritized actions (top 10)
      var prioritizedActions = [];
      for (var j = 0; j < Math.min(10, allFixable.length); j++) {
        var item = allFixable[j];
        // Generate action text from fix, message, or type-based fallback
        var actionText = item.fix || item.message;
        if (!actionText) {
          // Generate clear direction from type
          var typeToAction = {
            'duplicate-id': 'Fix duplicate ID conflicts',
            'excessive-children': 'Reduce child elements or componentize',
            'excessive-depth': 'Flatten DOM nesting structure',
            'excessive-attributes': 'Simplify element attributes',
            'large-list': 'Implement virtualization or pagination',
            'large-table': 'Add pagination or virtual scrolling',
            'large-form': 'Split into multi-step form',
            'excessive-handlers': 'Refactor inline event handlers',
            'inline-style-pattern': 'Extract to CSS utility class',
            'hardcoded-color': 'Replace with CSS variable',
            'z-index-inflation': 'Implement layered z-index system',
            'fixed-dimensions': 'Use responsive units',
            'missing-title': 'Add descriptive page title',
            'missing-description': 'Add meta description',
            'missing-canonical': 'Add canonical link tag',
            'missing-viewport': 'Add viewport meta tag',
            'missing-og-tags': 'Add Open Graph meta tags',
            'missing-twitter-tags': 'Add Twitter Card meta tags',
            'missing-h1': 'Add H1 heading',
            'heading-hierarchy': 'Fix heading hierarchy order',
            'missing-structured-data': 'Add JSON-LD structured data',
            'invalid-structured-data': 'Fix malformed structured data',
            'exposed-secret': 'Remove secret from client-side code',
            'xss-vector': 'Sanitize DOM manipulation',
            'eval-usage': 'Replace eval with safe alternatives',
            'insecure-storage': 'Use secure session storage',
            'insecure-form': 'Change form action to HTTPS',
            'http-login': 'Enable HTTPS for login forms',
            'missing-csrf': 'Add CSRF token to forms',
            'sensitive-params': 'Remove sensitive data from URL',
            'clickjacking': 'Add X-Frame-Options header',
            'postmessage-no-origin': 'Validate postMessage origin',
            'missing-sri': 'Add Subresource Integrity',
            'missing-noopener': 'Add rel="noopener" to links',
            'render-blocking': 'Defer or async load resources',
            'large-resource': 'Optimize or compress resource'
          };
          actionText = typeToAction[item.type] || ('Address ' + item.type.replace(/-/g, ' ') + ' issue');
        }
        prioritizedActions.push({
          priority: j + 1,
          audit: item.audit,
          action: actionText,
          impact: item.impact,
          severity: item.severity
        });
      }

      // Critical issues (impact >= 8 or severity critical/error)
      var criticalIssues = allFixable.filter(function(item) {
        return item.impact >= 8 || item.severity === 'critical' || item.severity === 'error';
      }).slice(0, 5);

      // Quick wins (impact >= 5 and simple fixes)
      var quickWins = allFixable.filter(function(item) {
        return item.impact >= 5 && item.fix && item.fix.length < 100;
      }).slice(0, 5);

      // Generate summary
      var criticalCount = criticalIssues.length;
      var highPriorityCount = allFixable.filter(function(i) { return i.impact >= 7; }).length;
      var summaryParts = ['Overall score ' + overallScore + '/100'];
      if (criticalCount > 0) {
        summaryParts.push(criticalCount + ' critical issue' + (criticalCount > 1 ? 's' : ''));
      }
      if (highPriorityCount > 0) {
        summaryParts.push(highPriorityCount + ' high priority fix' + (highPriorityCount > 1 ? 'es' : ''));
      }
      var summary = summaryParts.join('. ');

      // Build response
      var response = {
        summary: summary,
        overallScore: overallScore,
        grade: grade,
        checkedAt: new Date().toISOString(),
        audits: audits,
        prioritizedActions: prioritizedActions,
        criticalIssues: criticalIssues,
        quickWins: quickWins,
        stats: {
          totalIssues: allFixable.length,
          critical: criticalIssues.length,
          highPriority: highPriorityCount
        }
      };

      // Include full audit results in full mode
      if (detailLevel === 'full') {
        response.fullResults = {
          dom: domResult,
          css: cssResult,
          security: securityResult,
          seo: seoResult,
          performance: performanceResult
        };
        if (apiResult) {
          response.fullResults.api = apiResult;
        }
        if (loadingResult) {
          response.fullResults.loading = loadingResult;
        }
        if (accessibilityResult) {
          response.fullResults.accessibility = accessibilityResult;
        }
      }

      return response;
    }

    // If accessibility is included, we need to return a Promise
    // For raw mode, we need to request raw format from accessibility audit too
    if (includeAccessibility && window.__devtool_accessibility && window.__devtool_accessibility.auditAccessibility) {
      return window.__devtool_accessibility.auditAccessibility({ mode: 'standard', raw: true })
        .then(function(accessibilityResult) {
          return combineResults(accessibilityResult);
        })
        .catch(function(err) {
          console.warn('Accessibility audit failed:', err);
          return combineResults(null);
        });
    }

    // Synchronous path (no accessibility)
    return Promise.resolve(combineResults(null));
  }

  window.__devtool_audit_quality = {
    auditPageQuality: auditPageQuality,
    auditAll: auditAll
  };

  // Re-export all audit functions under the original namespace for api.js compatibility
  window.__devtool_audit = {
    auditDOMComplexity: window.__devtool_audit_dom.auditDOMComplexity,
    auditCSS: window.__devtool_audit_css.auditCSS,
    auditSecurity: window.__devtool_audit_security.auditSecurity,
    auditPageQuality: auditPageQuality,
    auditPerformance: window.__devtool_audit_performance.auditPerformance,
    auditAll: auditAll
  };
})();

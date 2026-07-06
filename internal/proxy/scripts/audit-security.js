// Security audit

(function() {
  'use strict';

  var utils = window.__devtool_utils;
  var auditUtils = window.__devtool_audit_utils;

  // Helper to check if script is a devtool script (filter from all audits)
  function isDevtoolScript(script) {
    // Check src attribute for devtool paths
    var src = script.getAttribute('src') || '';
    if (src.indexOf('__devtool') !== -1 || src.indexOf('devtool-mcp') !== -1) return true;

    // Check content for devtool markers
    var content = script.textContent || '';
    if (content.indexOf('__devtool') !== -1 &&
        (content.indexOf('window.__devtool') !== -1 ||
         content.indexOf('__devtool_core') !== -1 ||
         content.indexOf('__devtool_utils') !== -1)) return true;

    return false;
  }

  // === EXTERNAL SAME-ORIGIN SCRIPT PREFETCH ===
  // auditSecurity() is synchronous (the exec bridge and auditAll() consume its
  // return value directly), so external script bodies are fetched into a
  // module-level cache in the background. The first audit call kicks the
  // prefetch off and reports honestly that external coverage is pending;
  // subsequent calls scan the cached contents. Same-origin only — cross-origin
  // script bodies are unreadable from the page and are reported as a note.
  var EXT_MAX_FILES = 20;          // cap number of external files fetched
  var EXT_MAX_BYTES = 500 * 1024;  // cap bytes scanned per file

  var externalScan = {
    state: 'idle',   // idle | loading | done
    fetched: [],     // { source, selector, content }
    total: 0,        // same-origin candidates found
    skipped: 0,      // candidates beyond EXT_MAX_FILES
    truncated: 0,    // files cut at EXT_MAX_BYTES
    errors: 0,       // fetch failures
    crossOrigin: 0   // cross-origin scripts (never fetched)
  };

  function startExternalScriptPrefetch() {
    if (externalScan.state !== 'idle') return;
    externalScan.state = 'loading';

    var candidates = [];
    try {
      var origin = window.location.origin;
      var scriptEls = document.querySelectorAll('script[src]');
      for (var i = 0; i < scriptEls.length; i++) {
        var s = scriptEls[i];
        if (isDevtoolScript(s)) continue;
        try {
          var u = new URL(s.src, origin);
          if (u.origin === origin) {
            candidates.push({ url: s.src, selector: auditUtils.getSelector(s) });
          } else {
            externalScan.crossOrigin++;
          }
        } catch (e) {
          // Invalid URL — skip
        }
      }
    } catch (e) {
      externalScan.state = 'done';
      return;
    }

    externalScan.total = candidates.length;
    if (candidates.length > EXT_MAX_FILES) {
      externalScan.skipped = candidates.length - EXT_MAX_FILES;
      candidates = candidates.slice(0, EXT_MAX_FILES);
    }

    if (candidates.length === 0 || typeof window.fetch !== 'function') {
      externalScan.state = 'done';
      return;
    }

    var remaining = candidates.length;
    candidates.forEach(function(c) {
      window.fetch(c.url, { credentials: 'same-origin' })
        .then(function(resp) {
          if (!resp.ok) throw new Error('http ' + resp.status);
          return resp.text();
        })
        .then(function(text) {
          if (text.length > EXT_MAX_BYTES) {
            text = text.substring(0, EXT_MAX_BYTES);
            externalScan.truncated++;
          }
          externalScan.fetched.push({ source: c.url, selector: c.selector, content: text });
        })
        .catch(function() {
          externalScan.errors++;
        })
        .then(function() {
          remaining--;
          if (remaining === 0) externalScan.state = 'done';
        });
    });
  }

  // Options:
  //   detailLevel: 'summary' | 'compact' (default) | 'full'
  //   maxIssues: number (default: 20)
  //   maxUrlLength: number (default: 80)
  //   raw: boolean - if true, returns verbose detailed format (default: false, returns AI-optimized format)
  function auditSecurity(options) {
    options = options || {};
    var detailLevel = options.detailLevel || 'compact';
    var maxIssues = options.maxIssues || 20;
    var maxUrlLength = options.maxUrlLength || 80;
    var raw = options.raw === true; // Default: false (AI-optimized format)

    var critical = [];
    var errors = [];
    var warnings = [];
    var informational = [];
    var checksRun = [];

    // Kick off (or reuse) the external same-origin script cache.
    startExternalScriptPrefetch();

    // Helper to generate unique IDs — shared FNV-1a hash (audit-utils.js).
    function generateId(type, index) {
      return type + '-' + auditUtils.computeFindingID(type, '', String(index));
    }

    // Compact readable selector — shared implementation.
    var getSelector = auditUtils.getSelector;

    // Helper to mask secrets
    function maskSecret(secret) {
      if (!secret || secret.length < 8) return '*****';
      return secret.substring(0, 6) + '*****';
    }

    // === SCAN UNITS: inline user scripts + cached same-origin externals ===
    var userScripts = Array.prototype.slice.call(document.querySelectorAll('script')).filter(function(s) {
      return !isDevtoolScript(s);
    });

    var scanUnits = [];
    for (var su = 0; su < userScripts.length; su++) {
      var suEl = userScripts[su];
      var suContent = suEl.textContent || '';
      if (!suContent) continue; // src-only scripts covered by the prefetch cache
      scanUnits.push({
        content: suContent,
        source: suEl.getAttribute('src') || 'inline script',
        selector: getSelector(suEl)
      });
    }
    for (var xf = 0; xf < externalScan.fetched.length; xf++) {
      scanUnits.push(externalScan.fetched[xf]);
    }

    // Coverage note for external scripts (honest about what was NOT scanned).
    var coverageParts = [];
    if (externalScan.state === 'loading') {
      coverageParts.push('same-origin external script fetch in progress (' +
        externalScan.fetched.length + '/' + Math.min(externalScan.total, EXT_MAX_FILES) +
        ' cached) — re-run audit for full coverage');
    }
    if (externalScan.crossOrigin > 0) {
      coverageParts.push(externalScan.crossOrigin + ' cross-origin script' +
        (externalScan.crossOrigin > 1 ? 's' : '') + ' not scanned (content unreadable from the page)');
    }
    if (externalScan.skipped > 0) {
      coverageParts.push(externalScan.skipped + ' same-origin scripts skipped (cap ' + EXT_MAX_FILES + ' files)');
    }
    if (externalScan.truncated > 0) {
      coverageParts.push(externalScan.truncated + ' files truncated at ' + (EXT_MAX_BYTES / 1024) + 'KB');
    }
    if (externalScan.errors > 0) {
      coverageParts.push(externalScan.errors + ' script fetches failed');
    }
    // Reported via the top-level `note` field on every response shape (NOT as
    // an informational finding — audit-side coverage limits must not deduct
    // from the page's score).
    var externalScanNote = coverageParts.join('; ');

    // 1. Check for exposed API keys and secrets
    checksRun.push('exposed-secrets');
    // nameBased patterns match on a variable NAME ("token", "password"), which
    // is a much weaker signal than a structural key format — those downgrade
    // to warning instead of critical.
    var secretPatterns = [
      { pattern: /sk_live_[a-zA-Z0-9]{24,}|pk_live_[a-zA-Z0-9]{24,}/g, type: 'stripe-key' },
      { pattern: /api[_-]?key["\s:=]+["']?[a-zA-Z0-9_\-]{16,}["']?/gi, type: 'api-key' },
      { pattern: /bearer\s+[a-zA-Z0-9_\-\.]{20,}/gi, type: 'bearer-token' },
      { pattern: /token["\s:=]+["']?[a-zA-Z0-9_\-]{16,}["']?/gi, type: 'token', nameBased: true },
      { pattern: /secret["\s:=]+["']?[a-zA-Z0-9_\-]{16,}["']?/gi, type: 'secret' },
      { pattern: /password["\s:=]+["']?[a-zA-Z0-9_\-]{8,}["']?/gi, type: 'password', nameBased: true },
      { pattern: /AKIA[0-9A-Z]{16}/g, type: 'aws-key' },
      { pattern: /AIza[0-9A-Za-z\-_]{35}/g, type: 'google-api-key' }
    ];

    for (var i = 0; i < scanUnits.length; i++) {
      var unit = scanUnits[i];
      for (var p = 0; p < secretPatterns.length; p++) {
        var matches = unit.content.match(secretPatterns[p].pattern);
        if (matches) {
          for (var m = 0; m < matches.length; m++) {
            var nameBased = secretPatterns[p].nameBased === true;
            var bucket = nameBased ? warnings : critical;
            bucket.push({
              id: generateId('exposed-secret', critical.length + warnings.length),
              type: 'exposed-secret',
              severity: nameBased ? 'warning' : 'critical',
              secretType: secretPatterns[p].type,
              pattern: maskSecret(matches[m]),
              selector: unit.selector,
              source: unit.source,
              impact: nameBased ? 6 : 10,
              message: (nameBased ? 'Possible ' : 'Exposed ') + secretPatterns[p].type + ' in client-side code',
              fix: 'Move secret to server-side environment variable'
            });
          }
        }
      }
    }

    // Check HTML attributes for secrets
    var allElements = document.querySelectorAll('[data-api-key], [data-token], [data-secret]');
    for (var ae = 0; ae < allElements.length; ae++) {
      var el = allElements[ae];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(el)) continue;
      var attrValue = el.getAttribute('data-api-key') || el.getAttribute('data-token') || el.getAttribute('data-secret');
      if (attrValue && attrValue.length > 8) {
        critical.push({
          id: generateId('exposed-secret', critical.length),
          type: 'exposed-secret',
          severity: 'critical',
          secretType: 'html-attribute',
          pattern: maskSecret(attrValue),
          selector: getSelector(el),
          impact: 10,
          message: 'Secret exposed in HTML attribute',
          fix: 'Remove secret from HTML and use server-side authentication'
        });
      }
    }

    // 2. Check for XSS vectors
    checksRun.push('xss-vectors');

    var scriptTexts = scanUnits.map(function(u) {
      return u.content;
    }).join('\n');

    // Helper to find pattern matches with source file info across all scan
    // units (inline + cached same-origin externals).
    function findPatternInScripts(pattern, units) {
      var results = [];
      for (var i = 0; i < units.length; i++) {
        var unit = units[i];
        var matches = unit.content.match(pattern);
        if (matches && matches.length > 0) {
          results.push({
            source: unit.source,
            selector: unit.selector,
            count: matches.length
          });
        }
      }
      return results;
    }

    var innerHTMLResults = findPatternInScripts(/\.innerHTML\s*=/g, scanUnits);
    var innerHTMLUsage = innerHTMLResults.reduce(function(sum, r) { return sum + r.count; }, 0);
    if (innerHTMLUsage > 0) {
      // innerHTML assignment is only exploitable with unsanitized input, which
      // a static pattern match cannot confirm — warning, not error.
      warnings.push({
        id: generateId('innerHTML-usage', 0),
        type: 'xss-vector',
        severity: 'warning',
        vector: 'innerHTML',
        count: innerHTMLUsage,
        sources: innerHTMLResults.slice(0, 5),
        impact: 6,
        message: 'Found ' + innerHTMLUsage + ' innerHTML assignments (XSS risk if input is unsanitized)',
        fix: 'Use textContent or sanitize HTML before assignment'
      });
    }

    var outerHTMLResults = findPatternInScripts(/\.outerHTML\s*=/g, scanUnits);
    var outerHTMLUsage = outerHTMLResults.reduce(function(sum, r) { return sum + r.count; }, 0);
    if (outerHTMLUsage > 0) {
      errors.push({
        id: generateId('outerHTML-usage', 0),
        type: 'xss-vector',
        severity: 'error',
        vector: 'outerHTML',
        count: outerHTMLUsage,
        sources: outerHTMLResults.slice(0, 5),
        impact: 8,
        message: 'Found ' + outerHTMLUsage + ' outerHTML assignments (XSS risk)',
        fix: 'Use safe DOM manipulation methods'
      });
    }

    var documentWriteResults = findPatternInScripts(/document\.write\(/g, scanUnits);
    var documentWriteUsage = documentWriteResults.reduce(function(sum, r) { return sum + r.count; }, 0);
    if (documentWriteUsage > 0) {
      errors.push({
        id: generateId('document-write', 0),
        type: 'xss-vector',
        severity: 'error',
        vector: 'document.write',
        count: documentWriteUsage,
        sources: documentWriteResults.slice(0, 5),
        impact: 7,
        message: 'Found ' + documentWriteUsage + ' document.write calls (XSS risk)',
        fix: 'Use safe DOM manipulation methods'
      });
    }

    // 3. Check for eval usage
    checksRun.push('eval-usage');
    var evalResults = findPatternInScripts(/\beval\s*\(/g, scanUnits);
    var evalUsage = evalResults.reduce(function(sum, r) { return sum + r.count; }, 0);
    if (evalUsage > 0) {
      critical.push({
        id: generateId('eval-usage', 0),
        type: 'eval-usage',
        severity: 'critical',
        count: evalUsage,
        sources: evalResults.slice(0, 5),
        impact: 9,
        message: 'Found ' + evalUsage + ' eval() calls (arbitrary code execution risk)',
        fix: 'Replace eval() with safe alternatives like JSON.parse() or Function constructor'
      });
    }

    var funcConstructorResults = findPatternInScripts(/new\s+Function\s*\(/g, scanUnits);
    var functionConstructor = funcConstructorResults.reduce(function(sum, r) { return sum + r.count; }, 0);
    if (functionConstructor > 0) {
      errors.push({
        id: generateId('function-constructor', 0),
        type: 'eval-usage',
        severity: 'error',
        count: functionConstructor,
        sources: funcConstructorResults.slice(0, 5),
        impact: 8,
        message: 'Found ' + functionConstructor + ' Function constructor calls (code injection risk)',
        fix: 'Avoid dynamic code generation'
      });
    }

    // 4. Check for insecure storage of sensitive data
    checksRun.push('insecure-storage');
    var sensitiveKeys = ['password', 'token', 'secret', 'apikey', 'api_key', 'bearer', 'credential'];
    var storageIssues = [];

    try {
      for (var sk = 0; sk < sensitiveKeys.length; sk++) {
        if (localStorage.getItem(sensitiveKeys[sk]) || sessionStorage.getItem(sensitiveKeys[sk])) {
          storageIssues.push(sensitiveKeys[sk]);
        }
      }

      // Check all storage keys for sensitive patterns
      for (var lsi = 0; lsi < localStorage.length; lsi++) {
        var key = localStorage.key(lsi);
        for (var ski = 0; ski < sensitiveKeys.length; ski++) {
          if (key && key.toLowerCase().indexOf(sensitiveKeys[ski]) !== -1) {
            if (storageIssues.indexOf(key) === -1) {
              storageIssues.push(key);
            }
          }
        }
      }
    } catch (e) {
      // localStorage may not be available
    }

    if (storageIssues.length > 0) {
      errors.push({
        id: generateId('insecure-storage', 0),
        type: 'insecure-storage',
        severity: 'error',
        keys: storageIssues.slice(0, 5),
        count: storageIssues.length,
        impact: 8,
        message: 'Sensitive data stored in localStorage/sessionStorage',
        fix: 'Use secure httpOnly cookies or server-side sessions for sensitive data'
      });
    }

    // 5. Check for HTTP resources on HTTPS page (mixed content)
    checksRun.push('mixed-content');
    if (window.location.protocol === 'https:') {
      var mixedContent = [];

      var scripts = document.querySelectorAll('script[src^="http:"]');
      for (var mc1 = 0; mc1 < scripts.length; mc1++) {
        mixedContent.push({ type: 'script', url: scripts[mc1].src, element: scripts[mc1] });
      }

      var links = document.querySelectorAll('link[href^="http:"]');
      for (var mc2 = 0; mc2 < links.length; mc2++) {
        mixedContent.push({ type: 'stylesheet', url: links[mc2].href, element: links[mc2] });
      }

      var images = document.querySelectorAll('img[src^="http:"]');
      for (var mc3 = 0; mc3 < images.length; mc3++) {
        mixedContent.push({ type: 'image', url: images[mc3].src, element: images[mc3] });
      }

      if (mixedContent.length > 0) {
        errors.push({
          id: generateId('mixed-content', 0),
          type: 'mixed-content',
          severity: 'error',
          resourceCount: mixedContent.length,
          resources: detailLevel === 'summary' ? undefined : mixedContent.slice(0, 10).map(function(r) {
            return {
              type: r.type,
              url: detailLevel === 'full' ? r.url : auditUtils.truncateUrl(r.url, maxUrlLength),
              selector: getSelector(r.element)
            };
          }),
          impact: 7,
          message: 'Mixed content detected (' + mixedContent.length + ' HTTP resources)',
          fix: 'Change all resource URLs to HTTPS'
        });
      }
    }

    // 6. Check for insecure forms
    checksRun.push('insecure-forms');
    var insecureForms = document.querySelectorAll('form[action^="http:"]');
    if (insecureForms.length > 0) {
      errors.push({
        id: generateId('insecure-form', 0),
        type: 'insecure-form',
        severity: 'error',
        count: insecureForms.length,
        selector: 'form[action^="http:"]',
        impact: 9,
        message: 'Forms with insecure (HTTP) action URLs',
        fix: 'Change form action to HTTPS'
      });
    }

    // Check for password fields without proper autocomplete
    var passwordFieldsNoAutocomplete = document.querySelectorAll('input[type="password"]:not([autocomplete="new-password"]):not([autocomplete="current-password"])');
    if (passwordFieldsNoAutocomplete.length > 0) {
      warnings.push({
        id: generateId('password-autocomplete', 0),
        type: 'password-autocomplete',
        severity: 'warning',
        count: passwordFieldsNoAutocomplete.length,
        selector: 'input[type="password"]:not([autocomplete="new-password"]):not([autocomplete="current-password"])',
        impact: 5,
        message: 'Password fields without proper autocomplete attribute',
        fix: 'Add autocomplete="new-password" or autocomplete="current-password"'
      });
    }

    // Check for login forms over HTTP
    var loginForms = document.querySelectorAll('form');
    for (var lf = 0; lf < loginForms.length; lf++) {
      var form = loginForms[lf];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(form)) continue;
      var hasPassword = form.querySelector('input[type="password"]');
      if (hasPassword && window.location.protocol === 'http:') {
        critical.push({
          id: generateId('http-login', lf),
          type: 'http-login',
          severity: 'critical',
          selector: getSelector(form),
          impact: 10,
          message: 'Login form over unencrypted HTTP connection',
          fix: 'Use HTTPS for all pages with login forms'
        });
      }
    }

    // Check for CSRF token patterns
    var formsWithoutCSRF = [];
    for (var cf = 0; cf < loginForms.length; cf++) {
      var csrfForm = loginForms[cf];
      // Skip agnt/devtool UI elements
      if (utils.isDevtoolElement && utils.isDevtoolElement(csrfForm)) continue;
      var method = (csrfForm.method || 'GET').toUpperCase();
      if (method === 'POST') {
        var hasCSRF = csrfForm.querySelector('input[name*="csrf"], input[name*="token"], input[name="_token"]');
        if (!hasCSRF) {
          formsWithoutCSRF.push(csrfForm);
        }
      }
    }
    if (formsWithoutCSRF.length > 0) {
      warnings.push({
        id: generateId('missing-csrf', 0),
        type: 'missing-csrf',
        severity: 'warning',
        count: formsWithoutCSRF.length,
        selector: 'form[method="post"]',
        impact: 6,
        message: 'POST forms without apparent CSRF token',
        fix: 'Add CSRF token to all state-changing forms'
      });
    }

    // Check for sensitive data in GET parameters
    var urlParams = new URLSearchParams(window.location.search);
    var sensitivParams = [];
    var paramKeys = ['password', 'token', 'secret', 'api_key', 'apikey'];
    for (var pk = 0; pk < paramKeys.length; pk++) {
      if (urlParams.has(paramKeys[pk])) {
        sensitivParams.push(paramKeys[pk]);
      }
    }
    if (sensitivParams.length > 0) {
      critical.push({
        id: generateId('sensitive-params', 0),
        type: 'sensitive-params',
        severity: 'critical',
        params: sensitivParams,
        impact: 9,
        message: 'Sensitive data in URL parameters: ' + sensitivParams.join(', '),
        fix: 'Use POST method or session storage for sensitive data'
      });
    }

    // 7. Check for clickjacking vulnerability
    checksRun.push('clickjacking');
    var hasXFrameOptions = false;
    var hasCSPFrameAncestors = false;
    var metaTags = document.querySelectorAll('meta[http-equiv]');
    for (var mt = 0; mt < metaTags.length; mt++) {
      var httpEquiv = metaTags[mt].getAttribute('http-equiv');
      if (httpEquiv && httpEquiv.toLowerCase() === 'x-frame-options') {
        hasXFrameOptions = true;
      }
      if (httpEquiv && httpEquiv.toLowerCase() === 'content-security-policy') {
        var content = metaTags[mt].getAttribute('content') || '';
        if (content.indexOf('frame-ancestors') !== -1) {
          hasCSPFrameAncestors = true;
        }
      }
    }
    if (!hasXFrameOptions && !hasCSPFrameAncestors) {
      warnings.push({
        id: generateId('clickjacking', 0),
        type: 'clickjacking',
        severity: 'warning',
        impact: 6,
        message: 'Page may be vulnerable to clickjacking (no X-Frame-Options or CSP frame-ancestors)',
        fix: 'Add X-Frame-Options: DENY or Content-Security-Policy: frame-ancestors \'none\''
      });
    }

    // 8. Check for open redirects
    checksRun.push('open-redirects');
    var redirectPatterns = (scriptTexts.match(/window\.location\s*=|window\.location\.href\s*=|window\.location\.replace\(/g) || []).length;
    if (redirectPatterns > 0) {
      informational.push({
        id: generateId('redirect-pattern', 0),
        type: 'redirect-pattern',
        severity: 'info',
        count: redirectPatterns,
        impact: 3,
        message: 'Found ' + redirectPatterns + ' redirect patterns (verify no user input)',
        fix: 'Validate redirect URLs against whitelist'
      });
    }

    // 9. Check for postMessage without origin check
    checksRun.push('postmessage-security');
    var postMessageListeners = (scriptTexts.match(/addEventListener\s*\(\s*["']message["']/g) || []).length;
    var originChecks = (scriptTexts.match(/event\.origin|e\.origin|message\.origin/g) || []).length;
    if (postMessageListeners > 0 && originChecks === 0) {
      errors.push({
        id: generateId('postmessage-no-origin', 0),
        type: 'postmessage-no-origin',
        severity: 'error',
        count: postMessageListeners,
        impact: 8,
        message: 'postMessage listeners without origin validation',
        fix: 'Always validate event.origin in message event listeners'
      });
    }

    // 10. Check for third-party scripts
    checksRun.push('third-party-scripts');
    var currentOrigin = window.location.origin;
    var thirdPartyScripts = [];
    var scriptSources = document.querySelectorAll('script[src]');
    for (var tps = 0; tps < scriptSources.length; tps++) {
      var scriptEl = scriptSources[tps];
      // Skip devtool scripts
      if (isDevtoolScript(scriptEl)) continue;
      var src = scriptEl.src;
      try {
        var srcUrl = new URL(src);
        if (srcUrl.origin !== currentOrigin) {
          thirdPartyScripts.push({
            url: src,
            origin: srcUrl.origin,
            element: scriptEl
          });
        }
      } catch (e) {
        // Invalid URL
      }
    }
    if (thirdPartyScripts.length > 0) {
      informational.push({
        id: generateId('third-party-scripts', 0),
        type: 'third-party-scripts',
        severity: 'info',
        count: thirdPartyScripts.length,
        origins: Array.from(new Set(thirdPartyScripts.map(function(s) { return s.origin; }))).slice(0, 5),
        impact: 4,
        message: 'Page loads ' + thirdPartyScripts.length + ' third-party scripts',
        fix: 'Review third-party scripts and use Subresource Integrity (SRI)'
      });
    }

    // 11. Check for inline scripts without nonce
    checksRun.push('inline-scripts');
    var inlineScripts = document.querySelectorAll('script:not([src])');
    var scriptsWithoutNonce = [];
    for (var isn = 0; isn < inlineScripts.length; isn++) {
      var inlineScript = inlineScripts[isn];
      // Skip devtool scripts
      if (isDevtoolScript(inlineScript)) continue;
      if (!inlineScript.nonce && !inlineScript.hasAttribute('nonce')) {
        scriptsWithoutNonce.push(inlineScript);
      }
    }
    if (scriptsWithoutNonce.length > 0) {
      informational.push({
        id: generateId('inline-no-nonce', 0),
        type: 'inline-no-nonce',
        severity: 'info',
        count: scriptsWithoutNonce.length,
        selector: 'script:not([src]):not([nonce])',
        impact: 3,
        message: 'Inline scripts without CSP nonce',
        fix: 'Add nonce attribute or use external scripts with CSP'
      });
    }

    // 12. Check for external resources without SRI
    checksRun.push('sri');
    var externalResources = document.querySelectorAll('script[src], link[rel="stylesheet"][href]');
    var resourcesWithoutSRI = [];
    for (var sri = 0; sri < externalResources.length; sri++) {
      var resource = externalResources[sri];
      // Skip devtool scripts
      if (resource.tagName === 'SCRIPT' && isDevtoolScript(resource)) continue;
      var resourceSrc = resource.src || resource.href;
      try {
        var resourceUrl = new URL(resourceSrc);
        if (resourceUrl.origin !== currentOrigin && !resource.integrity) {
          resourcesWithoutSRI.push(resource);
        }
      } catch (e) {
        // Invalid URL
      }
    }
    if (resourcesWithoutSRI.length > 0) {
      warnings.push({
        id: generateId('missing-sri', 0),
        type: 'missing-sri',
        severity: 'warning',
        count: resourcesWithoutSRI.length,
        selector: 'script[src]:not([integrity]), link[rel="stylesheet"][href]:not([integrity])',
        impact: 5,
        message: 'External resources without Subresource Integrity',
        fix: 'Add integrity attribute to all external resources'
      });
    }

    // 13. Check for missing noopener
    checksRun.push('noopener');
    var unsafeLinks = document.querySelectorAll('a[target="_blank"]:not([rel*="noopener"])');
    if (unsafeLinks.length > 0) {
      warnings.push({
        id: generateId('missing-noopener', 0),
        type: 'missing-noopener',
        severity: 'warning',
        count: unsafeLinks.length,
        selector: 'a[target="_blank"]:not([rel*="noopener"])',
        impact: 5,
        message: 'External links missing rel="noopener"',
        fix: 'Add rel="noopener noreferrer" to all external links'
      });
    }

    // Combine all issues
    var allIssues = critical.concat(errors).concat(warnings).concat(informational);

    // Separate fixable issues (those with selectors or clear remediation)
    var fixable = allIssues.filter(function(issue) {
      return issue.selector || issue.type === 'missing-noopener' ||
             issue.type === 'missing-sri' || issue.type === 'mixed-content' ||
             issue.type === 'insecure-form' || issue.type === 'password-autocomplete';
    });

    // Calculate stats
    var stats = {
      critical: critical.length,
      errors: errors.length,
      warnings: warnings.length,
      info: informational.length,
      fixable: fixable.length,
      informational: informational.length
    };

    // Calculate score: 100 - (critical*20 + errors*10 + warnings*5 + info*1), min 0
    var score = Math.max(0, 100 - (stats.critical * 20 + stats.errors * 10 + stats.warnings * 5 + stats.info * 1));

    // Grade (canonical shared A-F scale)
    var grade = auditUtils.calculateGrade(score);

    // Generate summary
    var summaryParts = [];
    if (stats.critical > 0) summaryParts.push(stats.critical + ' critical');
    if (stats.errors > 0) summaryParts.push(stats.errors + ' error' + (stats.errors > 1 ? 's' : ''));
    if (stats.warnings > 0) summaryParts.push(stats.warnings + ' warning' + (stats.warnings > 1 ? 's' : ''));
    if (stats.info > 0) summaryParts.push(stats.info + ' info');

    var summary = summaryParts.length > 0 ?
      summaryParts.join(', ') + ' found' :
      'No security issues detected';

    // Add top issues to summary
    if (critical.length > 0) {
      summary = critical.length + ' critical security issue' + (critical.length > 1 ? 's' : '') +
                ': ' + critical.slice(0, 2).map(function(i) { return i.type; }).join(', ');
    }

    // Generate prioritized actions
    var actions = [];
    if (critical.length > 0) {
      critical.slice(0, 3).forEach(function(issue) {
        actions.push('URGENT: ' + issue.fix);
      });
    }
    if (errors.length > 0 && actions.length < 5) {
      errors.slice(0, 5 - actions.length).forEach(function(issue) {
        actions.push(issue.fix);
      });
    }
    if (warnings.length > 0 && actions.length < 5) {
      warnings.slice(0, 5 - actions.length).forEach(function(issue) {
        if (issue.count) {
          actions.push(issue.fix + ' (' + issue.count + ' instances)');
        } else {
          actions.push(issue.fix);
        }
      });
    }

    // === AI-OPTIMIZED RESPONSE (DEFAULT) ===
    // Returns grouped data for AI to generate context-aware security recommendations
    if (!raw) {
      // Group issues by type for AI processing
      var issuesByType = {};
      var allIssuesForRaw = [].concat(critical, errors, warnings);
      for (var ri = 0; ri < allIssuesForRaw.length; ri++) {
        var rIssue = allIssuesForRaw[ri];
        if (!issuesByType[rIssue.type]) {
          issuesByType[rIssue.type] = [];
        }
        issuesByType[rIssue.type].push({
          severity: rIssue.severity,
          selector: rIssue.selector,
          vector: rIssue.vector,
          secretType: rIssue.secretType,
          count: rIssue.count,
          message: rIssue.message
        });
      }

      // Categorize issues by fix complexity for AI prioritization
      var fixComplexity = {
        domFixable: [], // Can be fixed by modifying DOM/attributes
        codeChanges: [], // Requires JavaScript code changes
        backendChanges: [], // Requires server-side changes
        configChanges: [] // Requires infrastructure/config changes
      };

      for (var fc = 0; fc < allIssuesForRaw.length; fc++) {
        var fcIssue = allIssuesForRaw[fc];
        var t = fcIssue.type;
        if (t === 'missing-noopener' || t === 'password-autocomplete' || t === 'insecure-form') {
          fixComplexity.domFixable.push(fcIssue);
        } else if (t === 'xss-vector' || t === 'eval-usage' || t === 'postmessage-no-origin') {
          fixComplexity.codeChanges.push(fcIssue);
        } else if (t === 'exposed-secret' || t === 'insecure-storage' || t === 'missing-csrf') {
          fixComplexity.backendChanges.push(fcIssue);
        } else {
          fixComplexity.configChanges.push(fcIssue);
        }
      }

      var aiResponse = {
        audit: 'security',
        summary: summary,
        score: score,
        grade: grade,
        checkedAt: new Date().toISOString(),
        stats: stats,
        // Raw data for AI interpretation
        raw: {
          issuesByType: issuesByType,
          fixComplexity: {
            domFixable: fixComplexity.domFixable.length,
            codeChanges: fixComplexity.codeChanges.length,
            backendChanges: fixComplexity.backendChanges.length,
            configChanges: fixComplexity.configChanges.length
          },
          // Detailed issues by complexity for AI to prioritize
          domFixableIssues: fixComplexity.domFixable.map(function(i) {
            return { type: i.type, selector: i.selector, count: i.count };
          }),
          codeChangeIssues: fixComplexity.codeChanges.map(function(i) {
            return { type: i.type, vector: i.vector, count: i.count };
          }),
          backendIssues: fixComplexity.backendChanges.map(function(i) {
            return { type: i.type, secretType: i.secretType };
          })
        },
        // Hints for AI - what to look for in codebase
        automationHints: {
          lookFor: [
            'environment variable configuration (.env files)',
            'sanitization utilities (DOMPurify, sanitize-html)',
            'authentication/session handling patterns',
            'CSP configuration files',
            'framework-specific security middleware'
          ],
          suggestionsNeeded: [
            fixComplexity.domFixable.length > 0 ? 'DOM attribute fixes for ' + fixComplexity.domFixable.length + ' issues' : null,
            fixComplexity.codeChanges.length > 0 ? 'code refactoring for ' + fixComplexity.codeChanges.length + ' XSS/injection risks' : null,
            fixComplexity.backendChanges.length > 0 ? 'backend migration for ' + fixComplexity.backendChanges.length + ' exposed secrets' : null,
            fixComplexity.configChanges.length > 0 ? 'security headers/config for ' + fixComplexity.configChanges.length + ' issues' : null
          ].filter(Boolean)
        }
      };
      if (externalScanNote) aiResponse.note = externalScanNote;
      return aiResponse;
    }

    // === RAW RESPONSE (raw: true) ===
    // Returns verbose detailed format with all issues and context
    var response = {
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      checksRun: checksRun,
      stats: stats
    };
    if (externalScanNote) response.note = externalScanNote;

    if (detailLevel === 'summary') {
      // Summary: just overview
      return response;
    }

    // Compact and full modes: include categorized issues
    response.critical = critical.slice(0, maxIssues);
    response.fixable = fixable.slice(0, maxIssues);
    response.informational = informational.slice(0, maxIssues);
    response.actions = actions;

    if (detailLevel === 'full') {
      // Full mode: include all issues in their categories
      response.errors = errors;
      response.warnings = warnings;
      response.allIssues = allIssues;
    }

    return response;
  }

  window.__devtool_audit_security = {
    auditSecurity: auditSecurity
  };
})();

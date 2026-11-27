---
sidebar_position: 14
---

# Security & Validation Auditing

One call returns a structured client-side security report — no manual DOM crawling or header parsing. `auditSecurity()` rolls up forms, headers, XSS-risk patterns, external resources, framework quality, and prototype-pollution checks into a single graded result.

## auditSecurity

Run the comprehensive security audit. Returns structured findings across every client-visible security surface.

```javascript
window.__devtool.auditSecurity()
```

**What it covers:**
| Area | Findings |
|------|----------|
| Security headers | CSP, frame protection, HTTPS, mixed content, referrer policy |
| DOM security | inline scripts, inline event handlers, `javascript:` URLs, dangerous attributes |
| Form security | CSRF tokens, insecure form actions, autocomplete on sensitive fields |
| Framework quality | dev builds in production, vulnerable library versions, `v-html`/`ng-bind-html` usage |
| External resources | missing Subresource Integrity (SRI), unsandboxed iframes |
| Prototype pollution | best-practice checks for prototype hardening |

**Returns:**
```javascript
{
  headers: {
    csp: {
      present: true,
      value: "default-src 'self'; script-src 'self' 'unsafe-inline'",
      source: "meta-tag"
    },
    frameProtection: { present: true },
    https: { present: true, protocol: "https:" },
    mixedContent: {
      present: false,
      count: 0,
      details: { scripts: [], stylesheets: [], images: [], iframes: [] }
    },
    referrerPolicy: { present: true, value: "strict-origin-when-cross-origin" }
  },
  domSecurity: {
    dangerousElements: {
      inlineScripts: [
        { selector: "script:nth-of-type(3)", length: 1250, preview: "var config = {...}" }
      ],
      inlineEventHandlers: [
        { selector: "button.submit", attribute: "onclick", value: "submitForm()" }
      ],
      javascriptURLs: [
        { selector: "a.legacy-link", attribute: "href", value: "javascript:void(0)" }
      ],
      dangerousAttributes: [
        { selector: "div.content", attribute: "srcdoc", length: 500 }
      ]
    },
    summary: { inlineScripts: 5, inlineEventHandlers: 12, javascriptURLs: 2, dangerousAttributes: 1, total: 20 }
  },
  framework: {
    primary: "React",
    frameworks: [
      { name: "React", detected: true, version: "18.2.0", devToolsInstalled: true }
    ],
    issues: [
      {
        type: "react-dev-build",
        severity: "error",
        message: "React development build detected in production",
        fix: "Use production build for better performance and security"
      }
    ]
  },
  forms: {
    forms: [
      {
        selector: "form#login",
        action: "https://example.com/auth",
        method: "post",
        hasValidation: true,
        hasCSRF: true,
        fields: [
          { type: "email", name: "email", hasValidation: true },
          { type: "password", name: "password", hasValidation: true, isPassword: true }
        ]
      }
    ],
    summary: { total: 3, withValidation: 2, withCSRF: 2, passwordFields: 1, sensitiveFields: 1 }
  },
  resources: {
    summary: { totalScripts: 8, externalScripts: 3, withIntegrity: 2, iframes: 2, sandboxedIframes: 1 }
  },
  prototype: {
    vulnerable: false,
    recommendations: [
      "Use Object.create(null) for dictionary objects",
      "Validate and sanitize all user input before using as object keys"
    ]
  },
  summary: {
    frameworkDetected: "React",
    totalIssues: 12,
    criticalIssues: 2,
    httpsEnabled: true,
    cspEnabled: true
  },
  issues: [ /* all issues from all areas */ ],
  criticalIssues: [ /* only error severity issues */ ],
  overallScore: 75,
  grade: "C",  // A (90+), B (80-89), C (70-79), D (60-69), F (<60)
  timestamp: 1699999999999
}
```

**Score Weights:**
| Area | Weight |
|------|--------|
| Security Headers | 25% |
| DOM Security | 25% |
| Form Security | 20% |
| Framework Quality | 15% |
| External Resources | 15% |

**Issues Detected (across all areas):**
| Type | Severity | Description |
|------|----------|-------------|
| `missing-csp` | warning | No Content-Security-Policy meta tag |
| `missing-frame-protection` | info | No frame-ancestors in CSP |
| `no-https` | error | Page served over HTTP (non-localhost) |
| `mixed-content-active` | error | Scripts/stylesheets loaded over HTTP |
| `mixed-content-passive` | warning | Images/iframes loaded over HTTP |
| `inline-scripts` | warning | Inline `<script>` tags (XSS vectors) |
| `inline-event-handlers` | warning | onclick, onload, etc. attributes |
| `javascript-urls` | error | `javascript:` URLs in href/src/action |
| `dangerous-attributes` | info | srcdoc, data-html attributes |
| `missing-csrf` | warning | POST form without CSRF token |
| `insecure-form-action` | error | Form submits to HTTP URL |
| `sensitive-autocomplete` | warning | Sensitive field without autocomplete=off |
| `react-dev-build` | error | React development build in production |
| `vue-v-html` | warning | Using v-html (potential XSS) |
| `jquery-vulnerable` | warning | jQuery < 3.5.0 vulnerabilities |
| `missing-sri` | warning | External script without integrity hash |
| `unsandboxed-iframe` | warning | External iframe without sandbox |
| `prototype-not-frozen` | info | Object.prototype is not frozen |

---

## Common Patterns

### Pre-Deploy Security Check

```javascript
function securityCheck() {
  const audit = window.__devtool.auditSecurity()

  const blockers = []

  // Critical issues block deployment
  if (audit.criticalIssues.length > 0) {
    blockers.push(`${audit.criticalIssues.length} critical security issues`)
  }

  // No HTTPS in production
  if (!audit.headers.https.present) {
    blockers.push('HTTPS not enabled')
  }

  // Dev build in production
  if (audit.framework.issues.some(i => i.type === 'react-dev-build')) {
    blockers.push('React development build detected')
  }

  return {
    pass: blockers.length === 0,
    grade: audit.grade,
    score: audit.overallScore,
    blockers: blockers
  }
}
```

### Triage by Severity

```javascript
const audit = window.__devtool.auditSecurity()

console.log(`Security grade: ${audit.grade} (${audit.overallScore}/100)`)
console.log(`Primary framework: ${audit.summary.frameworkDetected}`)

// Critical issues first
audit.criticalIssues.forEach(i => {
  console.error(`[${i.severity}] ${i.type}: ${i.message}`)
  if (i.fix) console.log(`  Fix: ${i.fix}`)
})
```

---

## Performance Notes

- `auditSecurity` runs all checks in one pass; the DOM-security and external-resource scans walk every element/script/iframe, so it may be slow on large DOMs.
- Run it after page load completes for stable results.

## Security Considerations

- This audit detects client-side patterns only.
- Server-side headers (HSTS, X-Frame-Options) aren't visible to client JS.
- Some issues can only be detected through HTTP response headers — use the browser DevTools Network tab for complete header inspection.
- Consider server-side security scanning for comprehensive coverage.

## See Also

- [OWASP XSS Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cross_Site_Scripting_Prevention_Cheat_Sheet.html)
- [Content Security Policy - MDN](https://developer.mozilla.org/en-US/docs/Web/HTTP/CSP)
- [CSS Evaluation](/api/frontend/css-evaluation) - CSS architecture auditing
- [Quality Auditing](/api/frontend/quality-auditing) - Performance metrics

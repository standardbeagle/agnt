---
description: "Audit page for security vulnerabilities and best practices"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog"]
---

Audit the current page for security vulnerabilities using agnt's diagnostic tools.

## Steps

1. Run the security audit:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditSecurity()"}
   ```

2. Check for JavaScript errors (may indicate security issues):
   ```
   proxylog {proxy_id: "dev", types: ["error"], limit: 20}
   ```

3. Capture the page state to review cookies and storage:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.captureState()"}
   ```

4. Take a screenshot for documentation:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.screenshot('security-audit')"}
   ```

## What the Audit Checks

### Critical Security Issues (Errors)

| Issue | Description |
|-------|-------------|
| `mixed-content` | HTTP resources loaded on HTTPS page (blocks secure content) |
| `insecure-form` | Form submitting to HTTP URL (credentials exposed) |

### Security Warnings

| Issue | Description |
|-------|-------------|
| `missing-noopener` | `target="_blank"` links without `rel="noopener"` (tabnabbing risk) |
| `password-autocomplete` | Password fields with autocomplete enabled |

## Interpreting Results

The audit returns:
- `issues`: Array of security vulnerabilities found
- `count`: Total number of issues
- `errors`: Critical security issues
- `warnings`: Non-critical security concerns

For mixed content issues, the `resources` array shows:
- `type`: Resource type (script, stylesheet, image)
- `url`: The insecure HTTP URL

## State Capture Review

The `captureState()` function reveals:

### Cookies
- Check for `HttpOnly` flag on sensitive cookies
- Verify `Secure` flag on HTTPS sites
- Look for session tokens or sensitive data

### Local/Session Storage
- Identify what data is stored client-side
- Look for tokens, credentials, or PII
- Check for sensitive data that should be server-side

## Additional Security Checks

```
// Check Content Security Policy
proxy {action: "exec", id: "dev", code: "document.querySelector('meta[http-equiv=\"Content-Security-Policy\"]')?.content"}

// Find all forms and their actions
proxy {action: "exec", id: "dev", code: "Array.from(document.forms).map(f => ({action: f.action, method: f.method}))"}

// Find all scripts (check for untrusted sources)
proxy {action: "exec", id: "dev", code: "Array.from(document.scripts).map(s => s.src).filter(s => s)"}

// Check for inline event handlers (XSS risk)
proxy {action: "exec", id: "dev", code: "document.querySelectorAll('[onclick], [onerror], [onload]').length"}
```

## Security Best Practices Checklist

- [ ] All resources loaded over HTTPS
- [ ] Forms submit to HTTPS endpoints
- [ ] External links have `rel="noopener noreferrer"`
- [ ] Sensitive cookies have `HttpOnly` and `Secure` flags
- [ ] No credentials stored in localStorage
- [ ] Content Security Policy header configured
- [ ] X-Frame-Options or CSP frame-ancestors set
- [ ] Input validation on all user inputs
- [ ] No inline event handlers (onclick, etc.)
- [ ] Third-party scripts from trusted sources only

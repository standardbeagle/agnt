---
description: "Audit page for SEO best practices and issues"
allowed-tools: ["mcp__devtool__proxy", "mcp__devtool__proxylog"]
---

Audit the current page for SEO (Search Engine Optimization) best practices using agnt's diagnostic tools.

## Steps

1. Run the page quality audit (includes SEO checks):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditPageQuality()"}
   ```

2. Analyze DOM structure for SEO-relevant elements:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditDOMComplexity()"}
   ```

3. Check accessibility (affects SEO):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility()"}
   ```

4. Take a screenshot for visual reference:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.screenshot('seo-audit')"}
   ```

## What the Audit Checks

### Page Quality Issues

| Issue | Severity | Description |
|-------|----------|-------------|
| `missing-viewport` | warning | No viewport meta tag (mobile SEO) |
| `missing-description` | info | No meta description (search snippets) |
| `missing-h1` | warning | No H1 heading (content hierarchy) |
| `multiple-h1` | info | Multiple H1 headings (confusing hierarchy) |
| `missing-lang` | warning | No lang attribute (language detection) |
| `missing-title` | error | No page title (critical for SEO) |

### DOM Structure Relevant to SEO
- `links`: Number of links on page
- `images`: Number of images (should have alt text)
- `forms`: Number of forms

## Manual SEO Checklist

After running the automated audit, also verify:

### Title & Meta
- [ ] Title is 50-60 characters
- [ ] Meta description is 150-160 characters
- [ ] Keywords appear naturally in content

### Content Structure
- [ ] Single H1 containing primary keyword
- [ ] Logical heading hierarchy (H1 > H2 > H3)
- [ ] Content is unique and valuable

### Technical SEO
- [ ] Page loads in under 3 seconds
- [ ] Mobile-friendly design
- [ ] HTTPS enabled
- [ ] Canonical URL set
- [ ] Structured data (JSON-LD) present

### Images
- [ ] All images have descriptive alt text
- [ ] Images are optimized for size
- [ ] Images have descriptive filenames

### Links
- [ ] Internal links use descriptive anchor text
- [ ] External links open in new tab with rel="noopener"
- [ ] No broken links (404s)

## Additional Diagnostic Commands

```
// Check all images for alt text
proxy {action: "exec", id: "dev", code: "Array.from(document.images).map(i => ({src: i.src, alt: i.alt}))"}

// Get all headings in order
proxy {action: "exec", id: "dev", code: "Array.from(document.querySelectorAll('h1,h2,h3,h4,h5,h6')).map(h => ({tag: h.tagName, text: h.textContent.trim()}))"}

// Check for meta tags
proxy {action: "exec", id: "dev", code: "Array.from(document.querySelectorAll('meta')).map(m => ({name: m.name || m.property, content: m.content}))"}
```

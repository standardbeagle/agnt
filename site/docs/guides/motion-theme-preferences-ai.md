---
title: "Testing prefers-reduced-motion and Dark Mode with AI Coding Agents"
description: "Verify your site actually honors prefers-reduced-motion and prefers-color-scheme. Audit animations, scan CSS coverage, and compare themes on real devices with AI."
keywords: [prefers-reduced-motion testing, dark mode testing, prefers-color-scheme debug, reduced motion accessibility, dark mode contrast audit, respect user preferences web, test dark mode real device]
sidebar_label: "Motion & Theme Prefs"
---

# Testing prefers-reduced-motion and Dark Mode with AI Coding Agents

`prefers-reduced-motion` and `prefers-color-scheme` are the two user preferences a site most commonly *claims* to support and least commonly *actually* honors. The claim is one media query in a stylesheet; the reality is every animation added since that stylesheet was written, every component library shipping its own keyframes, and every hardcoded `#ffffff` that arrived in a Friday hotfix. Nobody re-tests the preference paths, because testing them means flipping an OS setting and manually eyeballing the whole app — twice.

The failure modes are not cosmetic. Reduced motion is a vestibular-safety setting: a user who enables it and still gets a parallax hero or an infinite pulse can experience real nausea. A broken dark mode is milder — white flashes, unreadable gray-on-gray, a light-themed iframe glowing in a dark page — but it reads as neglect, and the contrast failures it introduces are WCAG violations that never show up in a light-mode-only audit.

This is a verification problem, and an **AI coding agent** with runtime instruments can do the tedious part: enumerate what's animating, prove whether the preference changes it, and diff the two themes for what didn't change but should have.

## The Traditional Approach

**DevTools emulation** (Rendering panel → emulate `prefers-reduced-motion` / `prefers-color-scheme`) is the standard manual tool. It changes the media query result, a human looks at the page, and nothing is recorded. It also runs on desktop Chrome only — the iOS "Reduce Motion" setting and Android's dark theme interact with real pages through paths emulation doesn't reproduce.

**Grep the CSS for the media query**: proves the query exists, proves nothing about coverage. The `@media (prefers-reduced-motion: reduce)` block written in 2024 does not know about the spinner added last sprint.

**Visual QA twice**: correct and unsustainable, which is why it happens before launch and never again.

## The agnt Approach

The page itself knows the answers, and `proxy exec` can ask it. Three checks, each a one-call instrument the agent runs with the preference off and again with it on.

### 1. Does reduced motion actually stop the motion?

The decisive test is a conjunction: the preference is active *and* animations are still running. `document.getAnimations()` reports every live CSS animation, transition, and Web Animation regardless of where it came from — your code, a component library, a third-party widget:

```json
proxy {action: "exec", id: "app", code: `
  ({
    reducedMotionActive: matchMedia('(prefers-reduced-motion: reduce)').matches,
    stillRunning: document.getAnimations()
      .filter(a => a.playState === 'running')
      .map(a => ({
        name: a.animationName || a.constructor.name,
        infinite: a.effect && a.effect.getTiming().iterations === Infinity
      }))
  })
`}
```

`reducedMotionActive: true` with a non-empty `stillRunning` list is the violation, itemized. Not every running animation is a failure — an opacity fade under 200ms is generally considered acceptable under reduced motion — but an `infinite: true` entry with the preference active is exactly the thing the setting exists to prevent. The [auditAnimations](/api/frontend/quality-auditing) audit flags those infinite animations with fix guidance (`prefers-reduced-motion` is literally in its remediation text), so the combined call is: run the audit, then check whether the preference is active and the `infinite-animation` findings survived it.

### 2. Does the CSS even attempt coverage?

Before toggling anything, the agent can measure how much preference-handling CSS exists at all by walking the CSSOM:

```json
proxy {action: "exec", id: "app", code: `
  (function() {
    const found = { reducedMotion: [], colorScheme: [], blockedSheets: 0 };
    for (const sheet of document.styleSheets) {
      let rules;
      try { rules = sheet.cssRules; } catch (e) { found.blockedSheets++; continue; }
      for (const rule of rules) {
        if (rule.media && rule.media.mediaText) {
          if (rule.media.mediaText.includes('prefers-reduced-motion'))
            found.reducedMotion.push(rule.media.mediaText);
          if (rule.media.mediaText.includes('prefers-color-scheme'))
            found.colorScheme.push(rule.media.mediaText);
        }
      }
    }
    found.declaredColorScheme = getComputedStyle(document.documentElement).colorScheme;
    return found;
  })()
`}
```

Empty arrays end the investigation early: the site doesn't handle the preference, full stop, and the fix starts at the stylesheet rather than the audit. One honest limit: cross-origin stylesheets without CORS headers throw on `cssRules` access, so the scan reports `blockedSheets` instead of silently under-counting — a component library served from a CDN may be exactly where the unhandled animations live. `declaredColorScheme` matters separately: a page that styles dark mode but never declares `color-scheme: light dark` gets light-mode form controls and scrollbars inside its dark theme, which is the "almost right" look users notice immediately.

### 3. Did dark mode change everything it should have?

Theme verification is a diff problem: capture the same page under both schemes and compare. Toggle the scheme (OS setting, browser emulation, or the site's own switch), then capture a [snapshot](/api/snapshot) baseline per theme:

```json
snapshot {action: "baseline", id: "app", name: "theme-light"}
// flip the scheme, reload
snapshot {action: "baseline", id: "app", name: "theme-dark"}
```

For the elements that *didn't* respond, sample computed colors per scheme and let the agent diff the two runs:

```json
proxy {action: "exec", id: "app", code: `
  ['body', 'header', 'main', '.card', '.sidebar', 'input', 'button'].map(sel => {
    const el = document.querySelector(sel);
    if (!el) return null;
    const s = getComputedStyle(el);
    return { sel, background: s.backgroundColor, color: s.color };
  }).filter(Boolean)
`}
```

A selector whose `background`/`color` pair is byte-identical across the two runs is either intentionally theme-neutral or a hardcoded color that escaped the theme — the agent flags it, a human adjudicates which. Then run the [accessibility audit](/guides/accessibility-auditing-ai) *under the dark scheme*: contrast failures introduced by dark themes (muted gray text on dark gray cards is the classic) are invisible to the light-mode audit run everyone already does.

## The On-Device Half

Both preferences are fundamentally *device* settings, and this is where proxy-injected instrumentation earns its keep the same way it does in the [GPU guide](/guides/gpu-compositor-debugging-ai): the checks above run inside whatever browser loads the page. Point a phone at the proxy URL — or a [tunnel](/api/tunnel) when it's off-network — flip Reduce Motion or dark theme in the device's own settings, reload, and re-run the exact same calls. The `matchMedia` result now reflects iOS or Android's real preference plumbing, the animation registry reflects what that engine actually kept running, and the color samples reflect the real rendered theme. No desktop emulation stands in for the device; the device answers for itself.

That closes the loop the traditional approach leaves open. Emulation checks what your CSS *says*; the on-device run checks what a user with the preference *gets* — and the delta between those two answers is precisely the list of things to fix.

## See Also

- [GPU & Compositor Debugging](/guides/gpu-compositor-debugging-ai) — the performance case for honoring reduced motion, and `auditAnimations` in depth
- [Accessibility Auditing](/guides/accessibility-auditing-ai) — contrast checking to run under each color scheme
- [Mobile Testing on Real Devices](/guides/mobile-testing-real-devices-ai) — tunnels and device setup for the on-device runs
- [Visual Regression Testing](/guides/visual-regression-testing-ai) — per-theme baselines with `snapshot`

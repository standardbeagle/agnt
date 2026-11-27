---
title: "Visual Regression Testing with AI Coding Agents"
description: "Catch visual regressions before they ship. Screenshot comparison, baseline management, and AI-assisted diff analysis for frontend development."
keywords: [visual regression testing, screenshot comparison, UI testing, AI coding agent, MCP server, baseline management]
sidebar_label: "Visual Regression Testing"
---

# Visual Regression Testing with AI Coding Agents

**Visual regression testing** catches the bugs that your test suite cannot see. You change a padding value on a card component. The unit tests pass. The integration tests pass. The linter is happy. But the nav bar is now 4 pixels taller and the hero section sits slightly off-center. Nobody notices until a designer opens the staging URL two days later.

CSS changes are unpredictable by nature. A `font-size` bump in a base typography class cascades through every heading on every page. A `flex-wrap` change on a container reshapes the layout only when the content exceeds a certain width. A `z-index` fix on a modal uncovers a tooltip that was hiding behind it. These are real regressions that affect real users, and they are invisible to every automated check that does not actually look at the rendered page.

The cost compounds. Each unnoticed regression degrades the UI incrementally. By the time someone flags the problem, the offending commit is buried under weeks of changes. Preventing this requires comparing what the page looked like before and after every change -- something tedious enough that most teams skip it.

## The Traditional Approach

The standard options fall into three categories, each with significant friction.

**Manual inspection.** Open the page, squint, scroll, compare to your memory of what it looked like before. This catches obvious breakages but misses subtle shifts. It does not scale past a handful of pages, and it is entirely dependent on the developer remembering to look.

**Cloud services (Chromatic, Percy, Applitools).** These capture screenshots in CI and compare them against baselines stored in their platform. They work, but they are expensive, CI-only, and introduce a feedback delay -- you push a commit, wait for CI, review diffs in a web dashboard, then go back to fix things. The loop is measured in minutes, not seconds.

**Local screenshot diffing tools.** Tools like `reg-suit` or `backstop.js` run locally but require config files, reference directories, headless browser management, and a workflow for updating baselines. Most developers set them up once and stop maintaining them.

All three share a common problem: the developer is alone when interpreting the diff. A red overlay shows that pixels changed, but not why or whether the change is a problem.

## The agnt Approach

agnt provides visual regression testing as a conversation between you and your AI coding agent. The `snapshot` MCP tool captures screenshots, stores baselines, and compares them with pixel-level diffing. The AI agent -- being multimodal -- can look at both the baseline and the current screenshot, describe exactly what changed, and determine whether the change is intentional or a regression. The entire workflow happens inside a single coding session: take a baseline, make changes, capture new screenshots, compare. No context switching.

## Taking Baselines

A baseline is a named set of screenshots captured at a known-good state. Before starting any visual change, have your AI agent capture one.

First, the proxy must be running so screenshots can be captured from the browser:

```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
```

Then capture the baseline. The AI takes screenshots via the browser and passes them to the snapshot tool:

```json
proxy {action: "exec", id: "app", code: "__devtool.screenshot('homepage')"}

snapshot {
  action: "baseline",
  name: "before-button-restyle",
  pages: [
    {url: "/", viewport: {width: 1920, height: 1080}, screenshot_data: "<base64>"},
    {url: "/dashboard", viewport: {width: 1920, height: 1080}, screenshot_data: "<base64>"}
  ]
}
```

The baseline is stored locally in your project directory, tagged with the current git commit and branch. Name baselines descriptively -- `before-button-restyle`, `pre-typography-refactor`, `header-layout-v2` -- so you can find them later.

You can capture multiple viewports for the same page to cover responsive breakpoints:

```json
snapshot {
  action: "baseline",
  name: "responsive-checkout",
  pages: [
    {url: "/checkout", viewport: {width: 1920, height: 1080}, screenshot_data: "<base64>"},
    {url: "/checkout", viewport: {width: 768, height: 1024}, screenshot_data: "<base64>"},
    {url: "/checkout", viewport: {width: 375, height: 812}, screenshot_data: "<base64>"}
  ]
}
```

List existing baselines at any time:

```json
snapshot {action: "list"}
```

## Comparing After Changes

Once you have made your CSS or layout changes, capture new screenshots and compare them against the baseline:

```json
snapshot {
  action: "compare",
  baseline: "before-button-restyle",
  pages: [
    {url: "/", viewport: {width: 1920, height: 1080}, screenshot_data: "<base64>"},
    {url: "/dashboard", viewport: {width: 1920, height: 1080}, screenshot_data: "<base64>"}
  ]
}
```

The tool performs pixel-level comparison and returns a structured report:

```
Visual Regression Report: before-button-restyle -> current
===================================================

  2 of 2 pages changed (3.2% avg diff)

  /
   Layout shift detected in header region
   Diff: .agnt/snapshots/before-button-restyle/diff-homepage.png

  /dashboard
   Minor color changes in navigation
   Diff: .agnt/snapshots/before-button-restyle/diff-dashboard.png

===================================================
Summary: 2 unexpected changes found
```

The diff threshold defaults to 1% -- changes below that are treated as noise (anti-aliasing differences, sub-pixel rendering). Adjust it for stricter or looser matching:

```json
snapshot {
  action: "compare",
  baseline: "before-button-restyle",
  diff_threshold: 0.005,
  pages: [...]
}
```

## AI-Assisted Diff Analysis

The comparison report tells you that something changed. The AI tells you what changed and whether it matters.

When the snapshot tool returns a regression report, the AI agent has access to all three images: the baseline screenshot, the current screenshot, and the generated diff image (changed pixels highlighted in red). Because the AI is multimodal, it can visually inspect all three and provide analysis that no pixel-diffing algorithm can:

- "The button padding increased by approximately 8px, which pushed the search bar down. The search bar now overlaps the first row of results on the dashboard page."
- "The color change in the navigation is intentional -- it matches the new primary color you just defined in `theme.ts`. No regression here."
- "The header height increased because the logo image is now rendered at its intrinsic size instead of being constrained. Adding `max-height: 40px` to `.header-logo` would fix this."

This is the advantage over traditional screenshot diffing. A red overlay shows where pixels changed. The AI tells you why, whether it was intended, and what to do about it.

## Workflow Integration

The most effective pattern is to make snapshot comparison a habit around every visual change.

**Before/after for every CSS change:**

```
You: "Change the primary button style to use rounded corners and the new brand color."

AI: I will capture a baseline first, then make the changes.
    [takes baseline "pre-button-update"]
    [modifies button styles]
    [captures new screenshots]
    [compares against baseline]

    The button changes look correct. However, I found an unintended side effect:
    the .btn-primary class is also used on the "Save" button in the settings
    modal, and the increased border-radius makes it overflow its container
    on mobile viewports. I will fix the modal container padding.
```

**Page-level vs. component-level snapshots.** Full-page screenshots catch layout shifts between components. But if you are editing a single component, you can also capture just that element using `__devtool.screenshot('component', '#my-component')` to get a tighter comparison without noise from unrelated page sections.

**Managing baselines.** Delete baselines you no longer need to keep the snapshot directory clean:

```json
snapshot {action: "delete", name: "before-button-restyle"}
```

## Real-World Example

You are restyling the primary action buttons across your application. The change is straightforward: increase `padding` from `8px 16px` to `12px 24px` and switch from `border-radius: 4px` to `border-radius: 8px`.

The AI captures baselines for the homepage, dashboard, and settings pages. It makes the CSS changes, captures new screenshots, and runs the comparison. The report shows changes on all three pages, as expected -- the buttons are larger. But the dashboard page shows a 7.2% diff, higher than the other two pages at around 2%.

The AI examines the dashboard diff image and identifies the problem: the primary action button in the top navigation bar grew larger due to the padding increase. Because the nav bar has a fixed height of `48px`, the button now overflows vertically, pushing the nav bar taller by 8 pixels. This shifts the entire page content down, which is why the diff percentage is so high -- every pixel below the nav bar has moved.

The fix is a scoped override: `.nav-bar .btn-primary { padding: 8px 16px; }` to keep the nav button at its original size. The AI applies the fix, re-runs the comparison, and the dashboard diff drops to 0.3% -- just the border-radius change on other buttons, which is intentional.

Without the snapshot comparison, this nav bar overflow would have shipped. The unit tests do not check layout. The button component tests pass because the component renders correctly in isolation -- the problem is only visible in the context of the fixed-height nav bar.

## See Also

- [Proxy API Reference](/api/proxy) -- screenshots via `proxy exec` and browser JavaScript execution
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- catch runtime errors alongside visual regressions
- [proxylog API Reference](/api/proxylog) -- query captured screenshots and traffic logs

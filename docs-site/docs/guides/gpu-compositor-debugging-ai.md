---
title: "Debugging GPU and Compositor Performance with AI Coding Agents"
description: "Reproduce the T3 Code GPU bug from Theo's 'Fable Broke My App' video and diagnose it in minutes: infinite animate-pulse animations, backdrop-filter, and noise overlays, caught by agnt's compositor-load audit on any device including mobile."
keywords: [GPU process high CPU, Chrome GPU process, compositor performance, CSS animations GPU, Tailwind animate-pulse performance, backdrop-filter performance, T3 Code, Theo T3 GPU bug, Fable broke my app, infinite animation battery drain, mobile web performance, on-device debugging, AI coding agent, MCP server]
sidebar_label: "GPU & Compositor Debugging"
---

# Debugging GPU and Compositor Performance with AI Coding Agents

There is a class of performance bug that every JavaScript-oriented profiling tool is blind to. The page is snappy. The main thread is idle. Lighthouse is happy. And yet the browser's GPU process is burning 20-50% CPU, the laptop fan is spinning, and the battery is draining -- from a page that is doing *nothing*.

This guide simulates one of these bugs end to end and walks through diagnosing and fixing it with an **AI coding agent** driving agnt. It is modeled on a real incident Theo (t3.gg) documented in ["Fable Broke My App and Couldn't Fix It"](https://www.youtube.com/watch?v=TKlOCjLMNtw): **T3 Code**, his open-source agentic coding UI, was pegging Chrome's GPU process at up to 50% on a high-refresh 5K display. Multiple frontier models -- Fable, GPT-5.6, Codex -- produced huge confident PRs that changed nothing (one rewrote 10,000+ lines of network-layer code). The eventual culprit, found by hand over a day and a half, was Tailwind's `animate-pulse` on two tiny sidebar terminal icons -- amplified by a full-page noise overlay and `backdrop-filter` blur. The punchline: after shipping the fix his GPU was *still* hot, because each idle claude.ai tab was independently burning ~10% GPU from the same bug class.

The models could not find it because they had no way to *observe* the compositor. Given the right instruments, the diagnosis takes minutes -- and as of this guide, agnt ships those instruments as a first-class audit. The walkthrough below first reproduces the investigation the hard way (the same experiment-driven path that closed the real bug, worth understanding because it is how you handle the *next* bug class that has no audit yet), then shows the one-call version.

## Why Agents Fail at This Class of Bug

Once rendering work is offloaded to CSS, the usual evidence trails go cold:

- **The JS profiler shows nothing.** An infinite opacity animation runs entirely on the compositor thread. Zero scripting, zero layout, zero style recalculation in the flame chart.
- **DevTools changes the patient.** Opening the Performance panel inserts instrumentation that alters the page's rendering behavior, so the numbers you capture are not the numbers users experience.
- **The cost is invisible per-element.** Each infinitely-animating element is "cheap" -- but it promotes itself to its own GPU layer and forces the compositor to commit a frame at the display's refresh rate, forever. On a 120 Hz 5K display, a handful of pulsing icons means the page *never goes idle*.
- **Multipliers hide elsewhere in the tree.** A low-opacity noise/grain overlay or a `backdrop-filter` on an ancestor makes every one of those recomposites dramatically more expensive. The offending element and the amplifying element are usually far apart in the codebase.

An agent asked "why is my GPU pegged?" with no instruments will pattern-match on whatever animation-adjacent code it finds and confidently fix the wrong thing. The workflow that works is different: **the agent builds and drives diagnostic tooling, runs controlled experiments on the live page, and lets measurements -- not vibes -- pick the culprit.** agnt's `proxy exec` is the seam that makes this possible: the agent can inject, measure, toggle, and re-measure on the real running app without touching source code.

## The Simulated Bug

Drop this into any dev project as `gpu-demo.html` (or fold the classes into a React/Tailwind app -- `animate-pulse` is the stock Tailwind utility). It reproduces all three ingredients:

```html
<!doctype html>
<html>
<head>
<style>
  body { margin: 0; background: #1a1a1e; color: #ddd; font-family: system-ui; }

  /* Ingredient 1: infinite compositor animations (Tailwind's animate-pulse) */
  @keyframes pulse { 50% { opacity: .5; } }
  .animate-pulse { animation: pulse 2s cubic-bezier(.4,0,.6,1) infinite; }

  /* Ingredient 2: full-page noise overlay ("make the gray less monolithic") */
  .noise {
    position: fixed; inset: 0; pointer-events: none; opacity: .04; z-index: 50;
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='128' height='128'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.9'/%3E%3C/filter%3E%3Crect width='128' height='128' filter='url(%23n)'/%3E%3C/svg%3E");
  }

  /* Ingredient 3: backdrop blur on the composer bar */
  .composer {
    position: fixed; bottom: 0; left: 0; right: 0; padding: 16px;
    background: rgba(30,30,34,.6); backdrop-filter: blur(12px);
  }

  .sidebar { width: 240px; padding: 12px; }
  .row { display: flex; gap: 8px; align-items: center; padding: 6px; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: #4ade80; }
</style>
</head>
<body>
  <div class="noise"></div>
  <div class="sidebar">
    <div class="row"><span class="dot animate-pulse"></span> thread: fix-auth-flow</div>
    <div class="row"><span class="dot animate-pulse"></span> thread: perf-cleanup</div>
    <div class="row"><span class="dot"></span> thread: idle-branch</div>
  </div>
  <div class="composer"><input placeholder="Ask the agent..." style="width:100%"></div>
</body>
</html>
```

Serve it and open Chrome's own task manager (`Window > Task Manager` rather than DevTools -- the task manager observes without perturbing). On a high-refresh HiDPI display, the **GPU Process** row sits at 10-40% CPU for a page that is visually static apart from two pulsing 8-pixel dots.

## Setup

```json
run {script_name: "dev"}
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
```

Open the proxy URL in the browser you will measure in. Everything below runs through `proxy exec` against the live page -- no source edits, no rebuild, no DevTools open.

## Step 1: Inventory Every Animation on the Page

Before theorizing, enumerate. `document.getAnimations()` returns every running CSS animation, transition, and Web Animation -- including the ones buried in component libraries the agent has never read:

```json
proxy {action: "exec", id: "app", code: `
  document.getAnimations().map(a => {
    const el = a.effect && a.effect.target;
    return {
      name: a.animationName || a.constructor.name,
      state: a.playState,
      // Infinite animations are the compositor-pinning suspects
      infinite: a.effect && a.effect.getTiming().iterations === Infinity,
      element: el ? el.tagName + (el.className ? '.' + String(el.className).split(' ').join('.') : '') : null,
      visible: el ? (r => r.width > 0 && r.height > 0 && r.bottom > 0 && r.top < innerHeight)(el.getBoundingClientRect()) : false
    };
  })
`}
```

```json
[
  {"name": "pulse", "state": "running", "infinite": true, "element": "SPAN.dot.animate-pulse", "visible": true},
  {"name": "pulse", "state": "running", "infinite": true, "element": "SPAN.dot.animate-pulse", "visible": true}
]
```

Two entries. Both infinite, both running, both visible. In a real app this list is longer -- skeletons, spinners, status dots, gradient shimmer effects -- and the `infinite: true` + `visible: true` subset is the shortlist. Anything finite or offscreen is noise.

At the same time, sweep for the known amplifiers:

```json
proxy {action: "exec", id: "app", code: `
  const hits = [];
  document.querySelectorAll('*').forEach(el => {
    const s = getComputedStyle(el);
    if (s.backdropFilter !== 'none') hits.push({sel: el.className, prop: 'backdrop-filter', val: s.backdropFilter});
    if (s.filter !== 'none') hits.push({sel: el.className, prop: 'filter', val: s.filter});
    const r = el.getBoundingClientRect();
    if (s.position === 'fixed' && r.width >= innerWidth && r.height >= innerHeight && s.backgroundImage !== 'none')
      hits.push({sel: el.className, prop: 'full-viewport overlay', val: s.backgroundImage.slice(0, 60)});
  });
  hits
`}
```

```json
[
  {"sel": "noise", "prop": "full-viewport overlay", "val": "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org..."},
  {"sel": "composer", "prop": "backdrop-filter", "val": "blur(12px)"}
]
```

The agent now has a map: two infinite animations, one full-viewport overlay, one backdrop blur. What it does *not* have is proof of which combination is guilty. That requires experiments.

## Step 2: Measure Whether the Page Ever Goes Idle

The GPU-process CPU number lives in the browser's task manager, outside any page API -- the human reads that number, and that division of labor is fine. But the page-observable proxy for it is: **does the compositor keep producing frames when nothing should be changing?** A steady `requestAnimationFrame` cadence on a visually static page means something is forcing commits every vsync:

```json
proxy {action: "exec", id: "app", code: `
  new Promise(resolve => {
    let frames = 0;
    const start = performance.now();
    function tick(t) {
      frames++;
      if (t - start < 2000) requestAnimationFrame(tick);
      else resolve({
        framesIn2s: frames,
        effectiveFps: Math.round(frames / ((t - start) / 1000)),
        runningAnimations: document.getAnimations().filter(a => a.playState === 'running').length
      });
    }
    requestAnimationFrame(tick);
  })
`}
```

```json
{"framesIn2s": 240, "effectiveFps": 120, "runningAnimations": 2}
```

120 fps, indefinitely, on a page where the only motion is two dots fading. This is the smoking gun the JS profiler cannot show: the page never idles. (Caveat: rAF proves frames are being *scheduled*; a purely compositor-driven animation can sometimes commit without firing page rAF. Treat this as one signal, corroborated by the task-manager reading and the experiments below -- not as the sole oracle.)

## Step 3: Build a Kill-Switch and Bisect

This is the step that separates measurement-driven debugging from confident guessing. Have the agent install a toggle harness on the live page -- each switch disables one suspect category via injected CSS, so the human (or a screenshot/task-manager loop) can watch the GPU-process number respond:

```json
proxy {action: "exec", id: "app", code: `
  window.__gpuBisect = (() => {
    const style = document.createElement('style');
    style.id = '__gpu_bisect';
    document.head.appendChild(style);
    const rules = {
      animations: '*, *::before, *::after { animation-play-state: paused !important; }',
      transitions: '* { transition: none !important; }',
      blur: '* { backdrop-filter: none !important; filter: none !important; }',
      noise: '.noise { display: none !important; }'
    };
    const active = new Set();
    const render = () => { style.textContent = [...active].map(k => rules[k]).join('\\n'); };
    return {
      off: k => { active.add(k); render(); return [...active]; },
      on: k => { active.delete(k); render(); return [...active]; },
      all: () => { Object.keys(rules).forEach(k => active.add(k)); render(); return [...active]; },
      reset: () => { active.clear(); render(); return 'reset'; }
    };
  })();
  'installed: __gpuBisect.off(name) / .on(name) / .all() / .reset()'
`}
```

Note the choice of `animation-play-state: paused` rather than `animation: none` -- pausing freezes the compositor work without unmounting animation-dependent layout, so the UI does not visually collapse mid-experiment.

Now run the bisection. After each toggle, re-run the frame counter from Step 2 (and glance at the task manager):

```json
proxy {action: "exec", id: "app", code: "window.__gpuBisect.off('noise')"}
// re-measure -> {"framesIn2s": 240, "effectiveFps": 120}  — still pumping. Not the trigger.

proxy {action: "exec", id: "app", code: "window.__gpuBisect.off('blur')"}
// re-measure -> {"framesIn2s": 239, "effectiveFps": 120}  — still pumping. Not the trigger.

proxy {action: "exec", id: "app", code: "window.__gpuBisect.reset()"}
proxy {action: "exec", id: "app", code: "window.__gpuBisect.off('animations')"}
// re-measure -> {"framesIn2s": 3, "effectiveFps": 2}      — page goes idle. Found it.
```

The structure of the result matters as much as the verdict. The noise overlay and the blur are not what *starts* the frame pump -- the infinite animations are. But with animations running, toggling `noise` and `blur` visibly drops the task-manager number, because each forced commit is repainting through a full-viewport texture and a blur pass. **Trigger: `animate-pulse`. Amplifiers: noise overlay + `backdrop-filter`.** That two-part diagnosis is exactly what single-suspect guessing misses -- an agent that "fixes the blur" or "removes the noise" leaves the frame pump running, and one that only de-pulses the icons leaves the page one future animation away from regressing.

## Step 4: Fix It in Source, Not in the Console

The kill-switch proved causality on the live page; the fix goes in the codebase. The measured-and-verified changes:

**1. Make the pulse pause when nothing needs attention.** The honest fix for an infinite attention-getter is to scope *when it runs* -- making it uglier is a concession, and scoping is a fix. Gate it on actual state, and respect users who asked for less motion:

```css
/* Pulse only while the thread is actively working */
.dot { /* static by default */ }
[data-status="working"] .dot { animation: pulse 2s cubic-bezier(.4,0,.6,1) infinite; }

@media (prefers-reduced-motion: reduce) {
  [data-status="working"] .dot { animation: none; }
}
```

If a design genuinely wants an always-on pulse, drop the refresh-rate coupling instead: a `steps(2)` blink or a longer period cuts commits proportionally. But "pause when idle" is the fix that lets the page reach zero.

**2. Remove the full-page noise overlay** (or bake the grain into the background asset itself so it costs a single paint instead of one per commit).

**3. Keep `backdrop-filter` only where content actually scrolls behind it.** A blur over a static background is a per-commit cost purchasing nothing.

## Step 5: Verify With the Same Instruments, and Pin the Result

Re-run the measurements that convicted the bug -- never different, friendlier ones:

```json
proxy {action: "exec", id: "app", code: `
  new Promise(resolve => {
    let frames = 0; const start = performance.now();
    function tick(t) { frames++; t - start < 2000 ? requestAnimationFrame(tick) : resolve({framesIn2s: frames}); }
    requestAnimationFrame(tick);
  })
`}
// {"framesIn2s": 2}  — idle page, idle compositor
```

```json
proxy {action: "exec", id: "app", code: `
  document.getAnimations().filter(a => a.effect.getTiming().iterations === Infinity && a.playState === 'running').length
`}
// 0
```

And because the fix touched visual styling (the noise removal shifts perceived background color -- in the real T3 Code incident this produced mismatched grays across panels that survived several model-authored "fixes"), pin the look with a visual baseline:

```json
snapshot {action: "baseline", id: "app", name: "post-gpu-fix"}
```

Future changes compare against it with `snapshot {action: "compare", ...}`, so the perf fix and the visual regression check travel together.

## The One-Call Version: `auditAnimations`

Everything Steps 1-3 did by hand is now a built-in audit in agnt's injected instrumentation, sitting alongside `auditPageQuality` and friends:

```json
proxy {action: "exec", id: "app", code: "__devtool.audit.auditAnimations({sampleMs: 2000})"}
```

```json
{
  "audit": "animations",
  "score": 69,
  "grade": "D",
  "summary": "2 infinite animations keep the compositor committing every frame. sampled 120 fps over 2004ms (page never idles). 3 issues to address",
  "frameSample": {"frames": 240, "sampleMs": 2004, "effectiveFps": 120},
  "stats": {"animationsRunning": 2, "errors": 1, "warnings": 2, "info": 0, "totalIssues": 3},
  "findingsByType": {
    "infinite-animation": [
      {"severity": "warning", "selector": "span.dot.animate-pulse", "message": "Infinite animation \"pulse\" on a visible element keeps the compositor committing at display refresh rate — the page can never go idle. Gate it on state (run only while active), honor prefers-reduced-motion, or make it finite."}
    ],
    "viewport-overlay-amplifier": [
      {"severity": "error", "selector": "div.noise", "message": "Full-viewport overlay (noise/grain layer?) — repainted on every compositor commit, multiplied by an active infinite animation. Bake the texture into the background asset so it costs one paint, not one per frame."}
    ]
  }
}
```

The audit reads `document.getAnimations()` -- the declarative animation registry, the one signal that reports an animation *exists and is running* regardless of whether anything observable changes -- and cross-references it against an amplifier sweep. It encodes the trigger/amplifier distinction from Step 3 directly: amplifier findings escalate from `info` to `error` only when a frame pump is actually live, because a noise overlay above a genuinely idle page costs a single paint.

Note what it does *not* flag on this page: the composer's `backdrop-filter`. That bar covers a sliver of the viewport, and the audit only reports **large-area** filters (≥25% of the viewport) because a small blur behind a text box is a legitimate design choice with proportionally small cost. Precision matters here -- the fastest way to teach a developer to ignore an audit is to flag their perfectly fine composer bar.

Beyond the exact bug from the video, it catches the neighboring defects in the same class:

- **`layout-property-animation`** (error) -- an animation or transition touching `width`, `top`, `margin`, or any layout-inducing property. Strictly worse than a compositor pump: it forces main-thread style + layout every frame. The fix is always the same -- animate `transform`/`opacity` instead.
- **`will-change-overuse`** (warning) -- `will-change` scattered across many elements as a cargo-cult "performance" hint. Each one is a standing layer promotion costing compositing memory on every commit; apply it just-in-time and remove it when the interaction ends.
- **Honest non-answers** -- on a browser without `document.getAnimations()`, the audit returns `notApplicable` rather than a passing grade it could not have measured. The frame sample is time-bounded and self-reporting: in a backgrounded or occluded tab where the browser throttles rAF, it resolves with `rafStarved: true` and the summary says "inconclusive" instead of a fake near-zero fps reading as "page idles." And the rAF sample is documented as one signal rather than an oracle -- a purely compositor-driven animation can commit without firing page rAF, and the GPU-process CPU number itself lives outside every page API.

Passing `sampleMs` adds the Step 2 idle-frame measurement to the same report: `effectiveFps` at the display refresh rate on a visually static page is the frame-pump conviction; ≤5 fps means the page idles and any remaining heat is coming from somewhere else -- like a forgotten claude.ai tab.

The regression tripwire from the real incident becomes one line in any future session or CI check: run the audit, assert `findingsByType["infinite-animation"]` is empty (or exactly the set the design intentionally allows). That catches the next `animate-pulse` before it ships to someone's battery.

## Why On-Device Matters: The Mobile Blind Spot

Here is the part no desktop-profiler workflow covers. The compositor cost this guide diagnoses is *per device*: it scales with refresh rate, DPI, and GPU class. The exact page that idles politely on a dev machine can pin the GPU on a 120 Hz phone -- and mobile is where the bill actually lands, as thermal throttling and battery drain rather than a fan you can hear.

The traditional options for measuring this on a real phone are all bad:

- **Chrome remote debugging** needs a USB cable, a desktop Chrome, `chrome://inspect`, and an Android device -- and then hands you the same JS-oriented DevTools that were blind to this bug on desktop.
- **Safari/iOS remote inspection** needs a Mac, a cable, and developer mode -- same blindness, fewer knobs.
- **Perf overlays and lab tools** (Lighthouse, WebPageTest) run synthetic desktop-class traces that do not reproduce a specific device's refresh rate or thermal behavior.

agnt sidesteps all of it because the instrumentation travels with the page. The audit is injected by the reverse proxy into the HTML itself, so it runs **inside whatever browser loads the page** -- an iPhone on the couch, a mid-range Android over the office wifi, a tablet in a test rack. Point the device at the proxy URL (or a [tunnel](/api/tunnel) when it is not on your network) and the same `proxy exec` call your agent just ran on desktop executes on the device's own engine, against the device's own animation registry, at the device's own refresh rate:

```json
// Same call, now answering for the phone that just loaded the page
proxy {action: "exec", id: "app", code: "__devtool.audit.auditAnimations({sampleMs: 2000})"}
// frameSample.effectiveFps: 120 on a ProMotion iPhone, 60 on the budget Android — per-device truth
```

No cable, no desktop attach, no per-platform inspector. The agent gets the same structured findings from a real device that it gets from the dev machine, which means the diagnosis loop in this guide -- inventory, sample, bisect, fix, verify -- works on the hardware your users actually hold. That is the difference between "Lighthouse says fine" and knowing the sidebar pulse is why your app drains phones. See [Mobile Testing on Real Devices](/guides/mobile-testing-real-devices-ai) for the device-setup side of this workflow.

## The Division of Labor That Works

The T3 Code incident [in the video](https://www.youtube.com/watch?v=TKlOCjLMNtw) cost an experienced engineer a day and a half, two 5 a.m. nights, and several thousand lines of model-generated code that changed nothing. His own conclusion was not "agents are useless at performance" -- it was that agents guess when they cannot observe, and that their real value was building the diagnostic tools he steered. The workflow that closed the bug, and the one this guide encodes (now with the tool-building step pre-built as `auditAnimations`):

| Who | Does what |
|-----|-----------|
| Human | Notices the symptom (fan, heat, task-manager GPU %); reads the one number pages cannot see |
| Agent via agnt | Inventories animations and amplifiers, installs the kill-switch harness, runs the bisection, re-measures after each toggle, applies and verifies the source fix |
| Measurements | Pick the culprit |

No step above asked the model to *know* the answer. Every step gave it an instrument and asked it to run an experiment. That is the difference between an agent that fixates on the first animation it greps and an agent that hands you a two-line diagnosis with the evidence attached -- on every device your users own.

## See Also

- ["Fable Broke My App and Couldn't Fix It"](https://www.youtube.com/watch?v=TKlOCjLMNtw) -- Theo's original T3 Code investigation this guide is modeled on
- [Frontend Performance Monitoring](/guides/frontend-performance-monitoring-ai) -- Core Web Vitals, resource timing, and network bottlenecks
- [Mobile Testing on Real Devices](/guides/mobile-testing-real-devices-ai) -- pointing phones and tablets at the proxy
- [CSS Layout Debugging](/guides/css-layout-debugging-ai) -- diagnosing layout-level issues with `__devtool` helpers
- [Visual Regression Testing](/guides/visual-regression-testing-ai) -- baseline/compare workflows with `snapshot`
- [Quality & Performance Auditing](/api/frontend/quality-auditing) -- the full audit family `auditAnimations` joins
- [proxy API Reference](/api/proxy) -- full `exec` parameter docs

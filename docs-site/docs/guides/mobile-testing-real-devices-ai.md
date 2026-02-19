---
title: "How to Test on Real Mobile Devices with AI Coding Agents"
description: "Test your web app on real phones with full debugging instrumentation. Cloudflare and ngrok tunnels with automatic error capture and performance monitoring."
keywords: [mobile testing, real device testing, tunnel debugging, AI coding agent, MCP server, Cloudflare tunnel, ngrok]
sidebar_label: "Mobile Testing"
---

# How to Test on Real Mobile Devices with AI Coding Agents

**Mobile testing** on real devices is where the bugs are. Chrome DevTools device emulation gets you close -- it simulates viewport sizes and touch events -- but it runs the same rendering engine as your desktop browser. It does not catch the iOS Safari bug where `position: fixed` elements jump during scroll momentum. It does not catch the Android Chrome issue where `100vh` includes the address bar, pushing your footer off-screen. It does not catch the WebKit bug where `gap` in flexbox is silently ignored on older Safari versions.

These are not edge cases. They are the bugs your users actually report, and they only reproduce on real hardware. A phone in your hand, pointing at `localhost`, is the most reliable way to find them. But the moment you open your app on a phone, you lose all debugging instrumentation. There is no console. There is no network tab. There is no way to see what went wrong unless you wire up remote USB debugging -- and even then you only get Safari-on-Mac or Chrome-on-desktop, not both at once.

The result is a frustrating workflow: something breaks on your phone, you squint at the screen trying to identify the symptom, switch to your laptop, describe the problem to your AI coding agent in words, and hope the description is specific enough to lead to the right fix. Half the time it is not, and you are back to squinting.

## The Traditional Approach

There are a few ways teams deal with this:

**USB debugging** requires a cable, platform-specific setup (Xcode for Safari, `chrome://inspect` for Android), and only works with one browser at a time. Most developers set it up once, get frustrated by the connection dropping, and never use it again.

**Cloud device farms** like BrowserStack and Sauce Labs give you real devices in a browser tab. They are excellent for CI and cross-device matrix testing, but they cost money and add latency to the feedback loop. For the "let me just check this on my phone" moment during development, they are overkill.

**Testing without instrumentation** is what most developers actually do. Open the tunnel URL on your phone. Tap around. If something looks broken, take a screenshot and paste it into Slack. No stack traces, no error context, no HTTP traffic. Just "it looks wrong on iPhone."

## The agnt Approach

agnt solves this by routing your phone's traffic through an instrumented reverse proxy. Start a proxy in front of your dev server, expose it via a Cloudflare or ngrok tunnel, and open the tunnel URL on your phone. The proxy injects the same JavaScript instrumentation into every HTML page -- error capture, the `window.__devtool` diagnostic API, and the floating indicator -- regardless of whether the request comes from your desktop or a phone on the other side of the world.

Your AI coding agent queries the captured errors and traffic with `get_errors` and `proxylog`, just like it would for desktop debugging. The phone is a first-class debugging target.

Here is the setup:

```json
// 1. Start your dev server
run {script_name: "dev"}

// 2. Start the proxy (bind to all interfaces for tunnel access)
proxy {action: "start", id: "dev", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
// Response: {listen_addr: "0.0.0.0:45849"}

// 3. Start a tunnel
tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 45849, proxy_id: "dev"}
// Response: {public_url: "https://random-words.trycloudflare.com"}
```

Open `https://random-words.trycloudflare.com` on your phone. Every JavaScript error, every failed HTTP request, every unhandled promise rejection is now captured and queryable by your AI agent.

## Setting Up Tunnels

agnt supports two tunnel providers natively.

### Cloudflare (Free, No Account)

Cloudflare's quick tunnels require no account and no configuration. Install the `cloudflared` binary and you are done.

```bash
# macOS
brew install cloudflare/cloudflare/cloudflared

# Linux
curl -L --output cloudflared.deb https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
sudo dpkg -i cloudflared.deb
```

```json
tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 45849, proxy_id: "dev"}
```

The URL changes every time you start a tunnel. For quick "check it on my phone" testing, this is the fastest option.

### ngrok (Stable URLs, More Features)

ngrok requires a free account and auth token, but gives you a stable inspection dashboard at `http://localhost:4040` and optionally persistent URLs on paid plans.

```bash
brew install ngrok/ngrok/ngrok
ngrok config add-authtoken <your-token>
```

```json
tunnel {action: "start", id: "dev", provider: "ngrok", local_port: 45849, proxy_id: "dev"}
```

### Checking Tunnel Status

```json
// Get the public URL
tunnel {action: "status", id: "dev"}

// Stop when done
tunnel {action: "stop", id: "dev"}
```

## LAN Testing Without Tunnels

If your phone is on the same Wi-Fi network as your development machine, you can skip the tunnel entirely. Start the proxy with `bind_address: "0.0.0.0"` and access it via your machine's local IP address.

```json
proxy {action: "start", id: "dev", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
// Response: {listen_addr: "0.0.0.0:45849"}
```

Find your machine's IP (`ifconfig` or `ip addr`) and open `http://192.168.1.xxx:45849` on your phone. All instrumentation works the same way -- the proxy does not care where the request comes from. This avoids the tunnel overhead and works even without internet access, but only covers devices on your local network.

## What Works on Mobile

The proxy injects its JavaScript into every HTML response, so all instrumentation works identically on mobile:

- **Error capture** -- JavaScript runtime errors, unhandled promise rejections, and HTTP failures are logged automatically. Query them with `get_errors {}` or `proxylog {proxy_id: "dev", types: ["error"]}`.
- **Floating indicator** -- The draggable indicator appears on the phone screen. It is touch-friendly. You can tap it to send a message directly to your AI coding agent describing what you see, without switching to your laptop.
- **Screenshots** -- `proxy {action: "exec", id: "dev", code: "window.__devtool.screenshot('mobile-checkout')"}` captures the page as rendered on the phone.
- **Diagnostics** -- The full `window.__devtool` API is available: element inspection, layout diagnostics, accessibility auditing, DOM complexity analysis. Your AI agent runs these remotely via `proxy exec`.

## Mobile-Specific Error Patterns

Certain categories of bugs only surface on real devices. Here is what to watch for:

**Touch event errors** -- Code that relies on `mouseover`, `mouseenter`, or `hover` states breaks on touch devices. A dropdown menu that opens on hover but has no tap handler will silently fail. The proxy captures any resulting JavaScript errors (like accessing properties on elements that were never revealed).

**Viewport and layout issues** -- `100vh` on iOS Safari includes the browser chrome, causing content to overflow. `position: fixed` elements bounce during scroll momentum on WebKit. `window.innerHeight` changes as the address bar hides and shows. These often trigger ResizeObserver errors or layout recalculations that cascade into JavaScript failures.

**iOS Safari quirks** -- Safari on iOS has incomplete support for certain APIs: `Intl.Segmenter`, `CSS.registerProperty`, the `scrollend` event. Code that calls these without feature detection throws errors that only appear on real iPhones.

**Missing API support** -- `SharedArrayBuffer`, `Notification API`, and `Web Bluetooth` behave differently or are unavailable on mobile browsers. The proxy captures the resulting errors when your code calls these APIs and they fail.

## Real-World Example: iOS Safari Rendering Bug

You are building a checkout page. It looks perfect on desktop and in Chrome DevTools mobile emulation. You start a tunnel, open it on your iPhone, and the "Place Order" button is invisible -- pushed below the viewport by the iOS Safari address bar.

Your AI agent runs `get_errors {}` and sees:

```
=== Warnings (1) ===

[browser:js] ResizeObserver loop completed with undelivered notifications (12x, latest 1s ago)
  → src/components/CheckoutLayout.tsx:89:12
  page: https://random-words.trycloudflare.com/checkout
```

Twelve ResizeObserver warnings in one second. The layout is thrashing -- iOS Safari's dynamic viewport is continuously resizing the page, and the checkout container's `height: 100vh` is fighting with the browser chrome. The AI sees the exact component and line number, diagnoses the `100vh` problem, and suggests replacing it with `height: 100dvh` (dynamic viewport height) or a JavaScript-based calculation using `window.visualViewport.height`.

Without the tunnel and proxy instrumentation, this debugging session would have gone: "The button is missing on my phone. I think it might be a CSS issue. Can you check the checkout page?" -- and the AI would have spent five minutes guessing before asking you to scroll down.

```json
// AI can also run diagnostics directly on the mobile device
proxy {action: "exec", id: "dev", code: "window.__devtool.findOverflows()"}
// Returns elements that overflow their containers -- visible on the phone but invisible in emulation

proxy {action: "exec", id: "dev", code: "window.__devtool.captureState()"}
// Returns viewport dimensions, user agent, and device pixel ratio from the actual phone
```

## See Also

- [tunnel API Reference](/api/tunnel) -- full parameter docs, provider comparison, and installation instructions
- [Mobile Testing Use Case](/use-cases/mobile-testing) -- complete workflow including BrowserStack integration and device-specific diagnostics
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- the `get_errors` workflow in depth, including deduplication and severity filtering

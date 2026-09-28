---
id: dev-oidc-personas
title: "Sign in as anyone. No IdP."
description: The story app on a remote build box, browsed over the tailnet, signs in through agnt's dev OIDC issuer; the dev switches from a standard user to an admin from the indicator's persona chip.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Sign in as anyone. No IdP.

Testing the admin view shouldn't need a second real account. The personas live
in `.agnt.kdl`, the dev proxy serves the OIDC issuer, and one click switches
who you are.

<DemoVideo
  src="/video/dev-oidc-personas.webm"
  poster="/img/dev-oidc-personas-poster.webp"
  captions="/video/dev-oidc-personas.vtt"
  label="Demo: the story app on a build box, browsed over the tailnet. It signs in through agnt's dev issuer, which shows a persona picker; the dev picks Dev Standard, then switches to Dev Admin from the persona chip in the agnt indicator, and the app signs in again as admin." />

*Narrated · 0:58. Recorded live against a real Next.js app on a build box,
over the tailnet. Nothing in the flow is scripted on the app side.*

## What happens

**The config (0:04).** A `dev-oidc` block declares a client for the app and two
personas. The proxy is bound to the tailnet, and `allow` grants both personas
to one Tailscale login.

**The picker (0:19).** The app's "Continue as dev persona" button starts a
normal OIDC login against agnt's issuer. The browser is on another tailnet
device, so agnt asks `tailscale whois` who is on the other end and offers only
the personas `allow` grants that login. Off loopback the picker always shows.

**Standard (0:31).** Picking *Dev Standard* completes the code flow and the
app greets "Dev Standard".

**The switch (0:36).** The indicator panel's persona chip reads
`as: Dev Standard`. Choosing *Dev Admin* posts the issuer's switch form: the
persona changes, the app's session cookies expire, and the app lands on its
login path. Its sign-in button now completes silently as the admin.

**Agents (0:46).** The same switch is `:as admin` in the `agnt run` overlay and
`devauth {action: "as", persona: "admin"}` for an agent. `devauth` can also
mint an access token for API checks as each role.

## Set it up

See [Dev Sign-in & Protected Sharing](../features/dev-auth.md) for the config,
the trust rules for loopback, tailnet and Cloudflare Access, and how to point
your app at the issuer.

---
id: cloudflare-access
title: "Share the dev build. Not with everyone."
description: A named Cloudflare tunnel declared in .agnt.kdl puts a dev proxy at a stable hostname behind Cloudflare Access; a stranger gets the login, a forged token sent around Cloudflare gets 403 from agnt's origin check, an allowed caller gets through.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Share the dev build. Not with everyone.

A quick tunnel is public to anyone who finds the URL. A named tunnel puts your
dev proxy at a hostname you own, with Cloudflare Access in front, and agnt
checks the Access token again at the origin.

<DemoVideo
  src="/video/cloudflare-access.webm"
  poster="/img/cloudflare-access-poster.webp"
  captions="/video/cloudflare-access.vtt"
  label="Demo: a proxy declared with a cloudflare-tunnel block at agnt-demo.sbdev.io. curl without login gets a 302 to the Cloudflare Access login; a forged Access header sent straight to agnt's ingress gets 403; an Access service token gets through. In the browser, the Access login page, then the app showing it was reached over https at the public hostname." />

*Narrated · 1:10. Recorded live: a real named tunnel, a real Access
application, and the proxy started from the demo's own `.agnt.kdl`.*

## What happens

**The config (0:04).** One `cloudflare-tunnel` block on the proxy: the tunnel
UUID, the hostname, the credential file, and the Access application's team
domain and audience tag. agnt starts cloudflared when the proxy starts.

**A stranger (0:18).** `curl` to the public hostname gets a `302` to the
Access login. Cloudflare's edge stops it before it reaches your machine.

**Around the edge (0:32).** Skip Cloudflare entirely: send a forged
`Cf-Access-Jwt-Assertion` straight to the loopback ingress agnt opened for
cloudflared. agnt verifies the token itself (RS256, the team's keys, issuer,
audience, validity window) and answers `403`.

**An allowed caller (0:44).** With an Access service token, the request passes
the edge, passes agnt's check, and reaches the app.

**In the browser (0:48).** The Access login page for a stranger; for an
allowed caller, the app, reporting that it was reached at
`https://agnt-demo.sbdev.io` with `X-Forwarded-Proto: https`, so OAuth
redirect URIs built from forwarded headers come out right.

## Set it up

See [Dev Sign-in & Protected Sharing](../features/dev-auth.md#named-cloudflare-tunnel-with-access)
for the block, the fail-closed rules, and the one-time provisioning agnt leaves
to you (tunnel, DNS route, Access application).

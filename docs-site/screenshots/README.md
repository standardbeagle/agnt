# Mode screenshot harness

Generates the screenshots/videos of the browser-overlay modes (indicator,
sketch, design, responsive) and the audit/feature demos (element inspection,
layout diagnostics, accessibility, quality/API/loading audits, color palette,
responsive audit) used in `docs/api/frontend/*` and `docs/api/*`, plus the
`debug-to-e2e` workflow scene (audit tools diagnose a CSS containing-block
trap, the diagnosis becomes a site-wide regression check that goes red → fix →
green, then every failure condition of the Add-customer form is driven as
generated e2e cases against the mock `POST /api/customer` endpoint). It embeds the
**exact** proxy-injected `__devtool` bundle into a static demo page and drives
each mode with Playwright's bundled Chromium — no separately-installed browser
required. The static server also mirrors the proxy's `/__devtool_axe` endpoint
and mocks `/api/*` JSON endpoints (with latency) so the accessibility, API, and
loading audits run against real traffic.

Poster stills for `<ModeVideo>` come from each scene's final-frame PNG:

```bash
ffmpeg -y -i ../static/img/<scene>.png -vf scale=960:-1 ../static/img/<scene>-poster.webp
```

## Regenerate

```bash
# 1. Emit the injected bundle straight from the Go scripts package (authoritative)
go run ./docs-site/screenshots/genbundle > docs-site/screenshots/bundle.html   # agnt-allow

# 2. Capture PNGs (+ per-mode webm videos)
node docs-site/screenshots/capture.mjs

# 3. (optional) Convert the dynamic ones to GIF
cd docs-site/screenshots
ffmpeg -y -i videos/sketch-mode.webm -vf "fps=8,scale=720:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=128[p];[s1][p]paletteuse=dither=bayer" ../static/img/sketch-mode.gif
ffmpeg -y -i videos/responsive-mode.webm -vf "fps=8,scale=720:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=128[p];[s1][p]paletteuse=dither=bayer" ../static/img/responsive-mode.gif
```

## Narrated live-stack demo (debug-to-e2e-narrated.webm)

A second pipeline records the same workflow against the REAL agnt stack —
`serve-live.mjs` upstream, a real `agnt` daemon proxy injecting the bundle,
the floating panel's compose box sending a real `panel_message`, and a live
agent replying over `PROXY TOAST` — then adds section title cards, edge-tts
voice-over, and burned + WebVTT captions:

```bash
# 1. upstream + proxy
node docs-site/screenshots/serve-live.mjs &            # 127.0.0.1:8021
#    agnt MCP: proxy {action:"start", id:"demo", target_url:"http://127.0.0.1:8021"}

# 2. record one take (an agent must answer the sync markers in SYNC_DIR:
#    touch reply-1 / reply-2 after replying via proxy {action:"toast"})
PROXY_URL=http://127.0.0.1:<proxy-port>/ SYNC_DIR=/tmp/demo-sync \
  node docs-site/screenshots/record-live.mjs

# 3. titles + narration + captions (needs `pip install edge-tts`, ffmpeg)
node docs-site/screenshots/narrate-assemble.mjs
```

The waits on the live agent are spliced out at assembly using the marks in
`videos/debug-to-e2e-live.json`; narration text/voice lives in
`narration.json`. Output: `videos/debug-to-e2e-narrated.webm` + `.vtt`.

## Files

| File | Role | Committed |
|------|------|-----------|
| `genbundle/main.go` | Prints `scripts.GetCombinedScript()` — the real injected bundle | yes |
| `page.html` | Sample dashboard markup with an `<!--AGNT_BUNDLE-->` marker | yes |
| `capture.mjs` | Builds the page, serves it over http, drives each mode, captures PNG + webm | yes |
| `bundle.html`, `.serve/`, `videos/` | Generated artifacts | no (gitignored) |

Output PNGs/GIFs land in `docs-site/static/img/` and are committed.

Prereqs: `npm i -D playwright && npx playwright install chromium`, plus `ffmpeg`
for the GIF step.

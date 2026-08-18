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

## Scripted demo engine (`engine/` + `demos/`)

The engine turns a demo into a declarative, regenerable artifact: one
`demo.json` composes **cli** segments (VHS tapes), **browser** segments
(Playwright against the real agnt stack), and **card** segments (title cards)
into a single assembled webm — demos-as-code, re-runnable on every release.

```bash
make demo NAME=vhs-spiral                          # record + assemble
make demo NAME=vhs-spiral DEMOFLAGS=--only=fix     # re-record one segment
make demo NAME=vhs-spiral DEMOFLAGS=--assemble-only # re-cut from existing takes
```

- **cli segment**: `tape` lines are raw VHS commands; the engine adds
  Output/Set lines and runs `vhs`. `Set Framerate 10` is the default because
  VHS synthesizes frame timestamps at the nominal framerate — under load the
  capture loop drops frames and wall-clock time compresses (~3.5x speedup
  observed at 25fps on a loaded 6-core box). `trimSeconds` cuts idle tail.
- **browser segment**: an `.mjs` module exporting `run(driver, args)`. The
  driver wraps Playwright plus a **scripted agent**: `d.agentToast(...)` fires
  a real `PROXY TOAST` over the daemon socket, replacing record-live's live
  agent + SYNC_DIR marker dance with deterministic playback. Marks
  (`d.mark(name)`) + per-segment `keep` ranges splice out waits at assembly
  (endpoints: `"start"`, `"end"`, `"mark:<name>±<sec>"`).
- **setup**: `demo.json` can spawn the upstream (`serve-live.mjs`) and
  start/stop the demo proxy over the daemon socket automatically — the daemon
  must already be running.
- **narration** (optional): edge-tts VO + burned/WebVTT captions, anchors
  `"<seg-id>"`, `"<seg-id>+<sec>"`, `"<seg-id>+end"`. If edge-tts is not on
  PATH the engine assembles silent instead of failing. VO is loudness-normalized
  to the EBU R128 web target (`I=-16 LUFS, TP=-1.5 dBTP, LRA=11`) in the final
  mux — a single `loudnorm` pass on the mixed narration, so volume is even across
  lines and demos with no per-clip tuning.
- **brand** (optional): `"brand": {"image": "...", "position": "...", "opacity":
  0.85, "width": 220}` overlays a logo natively in the **single** final mux — no
  second encode. `image` resolves relative to the demo dir and must exist (a
  missing file is a hard error, never a silent unbranded output). `position` is
  one of `top-right` (default — bottom is captions + the card brand), `top-left`,
  `bottom-right`, `bottom-left`. The narrated path already re-encodes to burn
  subtitles, so the overlay is free there; the silent path (normally a
  stream-copy) takes its one necessary encode only when a brand is present.

  This retires step 4 of the `create-demo-video` skill (a second full vp9 encode
  applied after assembly to stamp the logo — slower and a generation loss). Once
  a demo declares `brand`, drop that post-encode from the skill recipe; the skill
  edit itself lives in the skill repo, not here.

Output lands in `demos/<name>/out/<name>.webm` (gitignored).

Verify the final-mux graph construction and the real ffmpeg output:

```bash
make demo-engine-test   # unit: constructed ffmpeg argv (overlay, loudnorm, one-encode)
make demo-mux-check     # integration: brand pixels + EBU R128 loudness on a fixture
```

`demo-mux-check` runs real ffmpeg on a synthetic fixture and loud-skips when
ffmpeg is absent; its loudness leg uses real edge-tts VO when available (the
-16 LUFS target is calibrated for speech).

`demos/vhs-spiral/` is the reference demo: a submit button rendered half
off-screen, three rounds of blind terminal automation (`agent-stub.mjs`
plays canned sessions so the failing attempts stay deterministic — point the
tape at the real `claude` for a live take), then agnt fixes it in one pass
through the real proxy.

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

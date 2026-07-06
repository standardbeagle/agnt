# Mode screenshot harness

Generates the screenshots/videos of the browser-overlay modes (indicator,
sketch, design, responsive) and the audit/feature demos (element inspection,
layout diagnostics, accessibility, quality/API/loading audits, color palette,
responsive audit) used in `docs/api/frontend/*` and `docs/api/*`. It embeds the
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

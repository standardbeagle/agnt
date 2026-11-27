#!/usr/bin/env bash
#
# demo-publish.sh — publish a rendered demo into the site as committed assets.
#
#   make demo-publish NAME=<x>
#   scripts/demo-publish.sh <x>
#
# Reads the assembled demo at site/screenshots/demos/<x>/out/<x>.webm (the
# output of `make demo NAME=<x>`, which is gitignored) and produces three assets
# at fixed site paths that ARE committed and referenced by <ModeVideo/>:
#
#   site/static/video/<x>.webm        — the clip itself
#   site/static/video/<x>.vtt         — captions, only when a sidecar exists
#   site/static/img/<x>-poster.webp   — 960-wide still from t=2s (the poster)
#   site/static/img/<x>-demo.webp     — 8fps 720-wide looping animation (README/GitHub)
#
# The work is done by the demo engine's --publish path
# (site/screenshots/engine/lib/publish.mjs) so this target, the engine CLI
# and the MCP `demo {action:"publish"}` tool share one implementation: a missing
# source or a missing ffmpeg exits nonzero having written nothing, and every
# artifact is staged in a temp dir and moved into place only after all renders
# succeed, so a mid-run ffmpeg failure never leaves a partial poster/webp behind.
set -euo pipefail

NAME="${1:-${NAME:-}}"
if [[ -z "$NAME" ]]; then
  echo "demo-publish: NAME is required (usage: make demo-publish NAME=<x>)" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ ! -f "$ROOT/site/screenshots/demos/$NAME/demo.json" ]]; then
  echo "demo-publish: no demo named '$NAME' under $ROOT/site/screenshots/demos" >&2
  exit 1
fi

exec node "$ROOT/site/screenshots/engine/demo.mjs" "demos/$NAME" --publish

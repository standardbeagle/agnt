#!/usr/bin/env bash
#
# demo-publish.sh — publish a rendered demo into the docs-site as committed assets.
#
#   make demo-publish NAME=<x>
#   scripts/demo-publish.sh <x>
#
# Reads the assembled demo at docs-site/screenshots/demos/<x>/out/<x>.webm (the
# output of `make demo NAME=<x>`, which is gitignored) and produces three assets
# at fixed docs-site paths that ARE committed and referenced by <ModeVideo/>:
#
#   docs-site/static/video/<x>.webm        — the clip itself
#   docs-site/static/video/<x>.vtt         — captions, only when a sidecar exists
#   docs-site/static/img/<x>-poster.webp   — 960-wide still from t=2s (the poster)
#   docs-site/static/img/<x>-demo.webp     — 8fps 720-wide looping animation (README/GitHub)
#
# The source webm is checked FIRST: a missing source exits nonzero with a
# run-first hint and writes nothing. Every artifact is rendered to a temp file
# and moved into place only after all renders succeed, so a mid-run ffmpeg
# failure never leaves a partial poster/webp behind, and a re-run overwrites
# deterministically.
set -euo pipefail

NAME="${1:-${NAME:-}}"
if [[ -z "$NAME" ]]; then
  echo "demo-publish: NAME is required (usage: make demo-publish NAME=<x>)" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

SRC_DIR="$ROOT/docs-site/screenshots/demos/$NAME/out"
SRC_WEBM="$SRC_DIR/$NAME.webm"
SRC_VTT="$SRC_DIR/$NAME.vtt"

VIDEO_DIR="$ROOT/docs-site/static/video"
IMG_DIR="$ROOT/docs-site/static/img"

DEST_WEBM="$VIDEO_DIR/$NAME.webm"
DEST_VTT="$VIDEO_DIR/$NAME.vtt"
DEST_POSTER="$IMG_DIR/$NAME-poster.webp"
DEST_ANIM="$IMG_DIR/$NAME-demo.webp"

# Source check FIRST — fail loud, write nothing.
if [[ ! -f "$SRC_WEBM" ]]; then
  echo "demo-publish: source not found: $SRC_WEBM" >&2
  echo "              run 'make demo NAME=$NAME' first" >&2
  exit 1
fi

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "demo-publish: ffmpeg not found on PATH (required for poster + animated webp)" >&2
  exit 1
fi

mkdir -p "$VIDEO_DIR" "$IMG_DIR"

# Stage every artifact in a temp dir; move into place only once all renders pass.
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/demo-publish.XXXXXX")"
trap 'rm -rf "$TMP_DIR"' EXIT

TMP_WEBM="$TMP_DIR/$NAME.webm"
TMP_POSTER="$TMP_DIR/$NAME-poster.webp"
TMP_ANIM="$TMP_DIR/$NAME-demo.webp"

cp "$SRC_WEBM" "$TMP_WEBM"

# Poster: single frame at t=2s, scaled to 960 wide (height auto, even).
ffmpeg -nostdin -y -ss 2 -i "$SRC_WEBM" -frames:v 1 -vf "scale=960:-2" "$TMP_POSTER" >/dev/null 2>&1

# Animated webp for README/GitHub: 8fps, 720 wide, infinite loop.
ffmpeg -nostdin -y -i "$SRC_WEBM" \
  -vf "fps=8,scale=720:-2:flags=lanczos" \
  -loop 0 -an -c:v libwebp -q:v 70 "$TMP_ANIM" >/dev/null 2>&1

# All renders succeeded — publish atomically.
mv -f "$TMP_WEBM" "$DEST_WEBM"
mv -f "$TMP_POSTER" "$DEST_POSTER"
mv -f "$TMP_ANIM" "$DEST_ANIM"

# .vtt sidecar: copied when present, skipped silently when the demo is silent.
if [[ -f "$SRC_VTT" ]]; then
  cp -f "$SRC_VTT" "$DEST_VTT"
  VTT_NOTE=" + $DEST_VTT"
else
  VTT_NOTE=""
fi

echo "demo-publish: $NAME"
echo "  video   $DEST_WEBM$VTT_NOTE"
echo "  poster  $DEST_POSTER"
echo "  demo    $DEST_ANIM"
echo
echo "Embed in docs with:"
echo "  <ModeVideo src=\"/video/$NAME.webm\" poster=\"/img/$NAME-poster.webp\" label=\"...\" />"

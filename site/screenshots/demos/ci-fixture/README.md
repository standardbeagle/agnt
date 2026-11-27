# ci-fixture — the demo-engine assembly smoke fixture

This is **not** a published demo. It exists so CI can prove the assembly
pipeline (splice → normalize → concat → mux) still turns takes into a playable
video, without Chrome, VHS, edge-tts, or the daemon.

- It is **card-less on purpose** — cards are the only assembly step that needs
  chromium, so leaving them out keeps `--assemble-only` to ffmpeg + node.
- The takes in `takes/` are **committed** (a few KB each). The two segment
  scripts under `segments/` are inert stubs: the fixture is assemble-only and
  is never recorded (it declares no `setup`), so nothing ever runs them.
- `clip-b` carries a `mark:mid` at 1500 ms (`takes/clip-b.json`) so the run
  exercises real keep-range splicing, not just a straight concat.

Regenerate the takes (only if the pipeline's encode settings change):

```sh
ffmpeg -y -f lavfi -i "color=c=#1e293b:s=640x400:r=25" -t 3 \
  -vf "drawtext=text='ci-fixture clip A':fontcolor=white:fontsize=28:x=(w-tw)/2:y=(h-th)/2" \
  -c:v libvpx-vp9 -crf 40 -b:v 0 -deadline realtime -cpu-used 8 -pix_fmt yuv420p takes/clip-a.webm
ffmpeg -y -f lavfi -i "color=c=#0ea5e9:s=640x400:r=25" -t 3 \
  -vf "drawtext=text='ci-fixture clip B':fontcolor=black:fontsize=28:x=(w-tw)/2:y=(h-th)/2" \
  -c:v libvpx-vp9 -crf 40 -b:v 0 -deadline realtime -cpu-used 8 -pix_fmt yuv420p takes/clip-b.webm
```

Run the smoke locally: `make demo-assemble-check`.

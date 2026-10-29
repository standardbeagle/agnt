# vt10x

[![Build Status](https://travis-ci.org/hinshun/vt10x.svg?branch=master)](https://travis-ci.org/hinshun/vt10x)
[![GoDoc](https://godoc.org/github.com/hinshun/vt10x?status.svg)](https://godoc.org/github.com/hinshun/vt10x)

Package vt10x is a vt10x terminal emulation backend, influenced
largely by st, rxvt, xterm, and iTerm as reference. Use it for terminal
muxing, a terminal emulation frontend, or wherever else you need
terminal emulation.

## standardbeagle fork

`github.com/standardbeagle/vt10x` is a fork of `hinshun/vt10x` maintained for
[agnt](https://github.com/standardbeagle/agnt), which feeds every byte a child
process writes through this emulator and repaints from `Row`/`Cell`. Patches on
top of upstream:

- **Direct colour, no per-rune garbage, `Row` accessor** (`v0.1.0-agnt.1`).
- **CSI dispatch on prefix, intermediate and final byte.** `CSI > 1 u` (kitty
  keyboard push), `CSI > 4;2 m` (modifyOtherKeys), `CSI > c` / `CSI = c`
  (DA2/DA3), `CSI > 0 q` (XTVERSION), `CSI ? 2026 h/l`, `CSI SP q` and friends
  were run as the unprefixed sequence with the same final byte (`CSI u` is
  DECRC, `CSI m` is SGR). They are now ignored. Malformed sequences are dropped
  whole; parameters are capped at 65535; a zero count means one.
- **Wide characters and grapheme clusters.** East Asian wide characters, emoji
  and two-column clusters occupy a head cell plus a continuation cell; zero-width
  runes (combining marks, ZWJ, variation selectors, skin tones) join the previous
  cell, including across `Write` calls; overwriting, erasing, inserting or
  deleting half a wide character blanks the other half; a wide character that
  does not fit in the last column wraps first. Segmentation and width come from
  [rivo/uniseg](https://github.com/rivo/uniseg) v0.4.7; ASCII and a generated
  table of one-column and CJK runes skip it.
- **`Write` keeps a UTF-8 sequence split by a read boundary** and always reports
  the whole buffer consumed (it used to drop the leading bytes of a split
  character and return a short count).
- **CUU/CUD/CNL/CPL honour the scroll region** the way xterm does.
- Smaller fixes: no pending wrap at the margin while autowrap is off, `ED 1` on
  row 1, parser states no longer allocate a closure per escape sequence.

### Reading cells

```go
g := term.Cell(x, y)
g.Char          // first rune of the cluster (the whole cluster for one rune)
g.Text()        // the whole cluster: Char followed by g.Extra; "" for a continuation cell
g.IsWide()      // first cell of a two-column pair; the next cell is its continuation
g.IsWideContinuation() // second cell: no text, skip it when rendering
```

A row walked left to right is: render `Text()` for every cell that is not a
continuation, and advance one column per cell (a wide cell and its continuation
together advance two). Colours and attributes are on both cells.

### Differential tests

`oracle/` is a separate Go module (so `charmbracelet/x/vt` never enters this
module's graph) that feeds the same byte streams to vt10x and to
`charmbracelet/x/vt` and compares the cursor and every cell's text:
`go -C oracle test ./...`. Where x/vt itself departs from xterm the case is
listed in `oracle/exempt_test.go` with the reason.

package vt10x

// ANSI color values
const (
	Black Color = iota
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	LightGrey
	DarkGrey
	LightRed
	LightGreen
	LightYellow
	LightBlue
	LightMagenta
	LightCyan
	White
)

// Default colors are potentially distinct to allow for special behavior.
// For example, a transparent background. Otherwise, the simple case is to
// map default colors to another color.
const (
	DefaultFG Color = 1<<24 + iota
	DefaultBG
	DefaultCursor
)

// Color is either a palette index or a tagged 24-bit RGB value.
//
// Palette indices occupy [0, 256): the ANSI colours in [0, 16) and the xterm
// cube/greyscale in [16, 256). A direct-colour value carries colorRGBFlag and
// packs the channels as r<<16 | g<<8 | b.
//
// The flag is load-bearing. Upstream stored RGB untagged in the same uint32
// space, so any colour with zero red and green — every rgb(0, 0, b) — was
// indistinguishable from the palette index of the same number: a saturated
// blue and xterm 255 (near-white grey) were both Color(255). A renderer
// reading the state back had no way to tell them apart and painted one as the
// other. Tagging removes the ambiguity instead of guessing at it.
type Color uint32

// colorRGBFlag marks a Color as direct 24-bit colour. It sits above both the
// packed RGB range (max 0xFFFFFF) and the Default* sentinels at 1<<24, so the
// three spaces stay disjoint.
const colorRGBFlag Color = 1 << 25

// RGB returns a direct-colour Color for the given channels.
func RGB(r, g, b uint8) Color {
	return colorRGBFlag | Color(r)<<16 | Color(g)<<8 | Color(b)
}

// IsRGB reports whether c carries direct 24-bit colour rather than a palette
// index. Callers rendering a Color must check this before treating the value
// as an index.
func (c Color) IsRGB() bool {
	return c&colorRGBFlag != 0
}

// RGB unpacks the channels of a direct-colour Color. The result is only
// meaningful when IsRGB reports true.
func (c Color) RGB() (r, g, b uint8) {
	return uint8(c >> 16), uint8(c >> 8), uint8(c)
}

// ANSI returns true if Color is within [0, 16).
func (c Color) ANSI() bool {
	return (c < 16)
}

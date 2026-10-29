package vt10x

// fastWidth classifies a rune without consulting the grapheme segmenter.
//
// It returns 1 or 2 when r is known to be a complete character of that many
// columns on its own: it never joins the cluster to its left (unless that
// cluster ends in a Prepend character, which the caller tracks), and nothing
// about it depends on what follows. It returns 0 when r needs the segmenter:
// combining marks, joiners, variation selectors, emoji, regional indicators,
// Hangul jamo, controls and everything above the BMP.
//
// The table is generated from github.com/rivo/uniseg; see width_table_test.go,
// which fails when the table drifts from the segmenter.
func fastWidth(r rune) int {
	if r < 0 || r >= 0x10000 {
		return 0
	}
	return int(widthPages[widthPageIndex[r>>8]][r&0xFF])
}

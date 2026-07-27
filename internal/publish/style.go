package publish

import "strings"

// ValidateStyleProps enforces the guards that SURVIVE on an applyStyle payload
// (spec §6): a non-empty property name, and the §5 style-patch size cap.
//
// RETIRED 2026-07-27 with INV-6, and deliberately NOT reinstated here:
//
//   - the §5b property allowlist (color/background/border*/font*/…). §5b is now
//     non-enforced guidance for what a variant typically needs; "any CSS
//     property" is legal.
//   - the §5 forbidden-token scan (`url(`, `expression(`, `@import`,
//     `image-set(`, `cross-fade(`, `javascript:`) and its companion backslash /
//     `;{}<>` rejections, which existed only to make that scan unbypassable.
//
// Both constrained a *trusted* author — the publisher holding the dev session
// (§0 Actors) — while the exfil they nominally blocked is actually contained by
// the wholesale-replaced CSP (§4, INV-11/INV-12): `connect-src 'self'` and an
// `img-src` without `https:` bound where a `url()` can reach far better than a
// substring match on a CSS value can. §6a is explicit that downstream code MUST
// NOT reintroduce the scan as defense-in-depth: it produces false rejections of
// legitimate variants and adds nothing CSP does not already guarantee.
//
// The size cap is untouched — it is an abuse control, not an injection control.
func ValidateStyleProps(props map[string]string) error {
	if len(props) == 0 {
		return errf("applyStyle: no properties")
	}
	total := 0
	for prop, val := range props {
		if strings.TrimSpace(prop) == "" {
			return errf("applyStyle: empty property name")
		}
		total += len(prop) + len(val)
	}
	if total > MaxStylePatchBytes {
		return errf("applyStyle: patch size %d exceeds max %d", total, MaxStylePatchBytes)
	}
	return nil
}

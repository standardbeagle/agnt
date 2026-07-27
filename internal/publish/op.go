package publish

import "strings"

// allowedAttrName encodes the setAttribute name allowlist.
//
// DEVIATION (documented): the P1 spec §6 lists the setAttribute allowlist as
// {class,id,title,alt,aria-*,data-*,href,src} with href/src re-validated as
// https. This P2 task instruction is STRICTER and overrides it: the setAttribute
// allowlist must EXCLUDE on*/href/src/srcdoc/formaction. We follow the stricter
// task contract (still satisfies INV-6): href/src cannot be set via
// setAttribute; image URLs go through the dedicated setImageSrc op (which
// enforces https, §6). This is deny-by-default and safe.
func allowedAttrName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	// Explicitly forbidden regardless of anything else.
	if strings.HasPrefix(lower, "on") { // on*, e.g. onclick — event handlers
		return false
	}
	switch lower {
	case "href", "src", "srcdoc", "formaction":
		return false
	case "class", "id", "title", "alt":
		return true
	}
	if strings.HasPrefix(lower, "aria-") || strings.HasPrefix(lower, "data-") {
		// aria-/data- suffix must be a well-formed ident tail.
		return true
	}
	return false
}

// isClassIdent reports whether s is a single valid ident usable as a class name.
func isClassIdent(s string) bool { return fullIdRe.MatchString(s) }

// noControlChars reports whether s is free of ASCII control chars.
func noControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Validate enforces the op contract (spec §6/§6a): the op type is known, the
// selector is present and in the restricted grammar for element-targeting ops
// and absent for variant-root ops, only the fields legal for the op type are
// populated, and each is within its surviving limit.
//
// The raw-content ops (setHTML/addStyle/addScript) are deliberately NOT
// inspected for "script-ness": INV-6 is retired and §6a forbids reintroducing
// the string scan as defense-in-depth, because it produces false rejections of
// legitimate publisher variants while adding nothing the wholesale-replaced CSP
// (INV-11/INV-12) does not already guarantee. What survives is size caps (§5),
// the selector grammar (§5a), and https-only URLs (§5, now justified by
// INV-13).
func (o *Op) Validate() error {
	switch o.Op {
	case OpAddStyle, OpAddScript:
		// Variant-root ops (§6a): they append to the variant root rather than
		// mutating a matched element, so a selector is meaningless.
		if o.Selector != "" {
			return errf("%s: unexpected field 'selector'", o.Op)
		}
	case OpSetText, OpSetAttribute, OpReplaceClass, OpAddClass, OpRemoveClass,
		OpApplyStyle, OpSetImageSrc, OpSetHTML:
		if err := ValidateSelector(o.Selector); err != nil {
			return err
		}
	default:
		return errf("unknown op type %q", o.Op)
	}
	switch o.Op {
	case OpSetText:
		if err := o.forbidExcept("value"); err != nil {
			return err
		}
		if len(o.Value) > MaxTextBytes {
			return errf("setText: value %d bytes exceeds max %d", len(o.Value), MaxTextBytes)
		}
		if !noControlChars(strings.ReplaceAll(strings.ReplaceAll(o.Value, "\n", ""), "\t", "")) {
			return errf("setText: value has illegal control character")
		}
		return nil
	case OpSetAttribute:
		if err := o.forbidExcept("name", "value"); err != nil {
			return err
		}
		if !allowedAttrName(o.Name) {
			return errf("setAttribute: attribute %q not on allowlist", o.Name)
		}
		if len(o.Value) > MaxAttrValueLength {
			return errf("setAttribute: value %d bytes exceeds max %d", len(o.Value), MaxAttrValueLength)
		}
		if !noControlChars(o.Value) {
			return errf("setAttribute: value has illegal control character")
		}
		return nil
	case OpReplaceClass:
		if err := o.forbidExcept("from", "to"); err != nil {
			return err
		}
		if !isClassIdent(o.From) || !isClassIdent(o.To) {
			return errf("replaceClass: from/to must be single class idents")
		}
		return nil
	case OpAddClass, OpRemoveClass:
		if err := o.forbidExcept("value"); err != nil {
			return err
		}
		if !isClassIdent(o.Value) {
			return errf("%s: value must be a single class ident", o.Op)
		}
		return nil
	case OpApplyStyle:
		if err := o.forbidExcept("props"); err != nil {
			return err
		}
		return ValidateStyleProps(o.Props)
	case OpSetImageSrc:
		if err := o.forbidExcept("url"); err != nil {
			return err
		}
		return ValidateURL(o.URL)
	case OpSetHTML:
		if err := o.forbidExcept("html"); err != nil {
			return err
		}
		if o.HTML == "" {
			return errf("setHTML: html is required")
		}
		if len(o.HTML) > MaxRawHTMLBytes {
			return errf("setHTML: html %d bytes exceeds max %d", len(o.HTML), MaxRawHTMLBytes)
		}
		return nil
	case OpAddStyle:
		if err := o.forbidExcept("css"); err != nil {
			return err
		}
		if o.CSS == "" {
			return errf("addStyle: css is required")
		}
		// The per-variant style budget (§5) is enforced by Variant.Validate,
		// which sees every applyStyle and addStyle in the variant; this per-op
		// check only bounds a single op so an oversize one fails at its source.
		if len(o.CSS) > MaxStylePatchBytes {
			return errf("addStyle: css %d bytes exceeds max %d", len(o.CSS), MaxStylePatchBytes)
		}
		return nil
	case OpAddScript:
		if err := o.forbidExcept("code", "src"); err != nil {
			return err
		}
		switch {
		case o.Src != "" && o.Code != "":
			// §6a: src and code are alternative sources for ONE body.
			return errf("addScript: src and code are alternatives; supply exactly one")
		case o.Src != "":
			// src is a publish-time fetch input (§6a). Here it is only gated on
			// scheme/length; the §4a resolved-address checks and the fetch that
			// inlines the body belong to the publish pipeline.
			if err := ValidateURL(o.Src); err != nil {
				return errf("addScript: %w", err)
			}
			return nil
		case o.Code != "":
			// The per-revision script budget (§5) is enforced by
			// VariantSet.Validate across every variant.
			if len(o.Code) > MaxRawScriptBytes {
				return errf("addScript: code %d bytes exceeds max %d", len(o.Code), MaxRawScriptBytes)
			}
			return nil
		default:
			return errf("addScript: one of src or code is required")
		}
	default:
		// Unreachable: the selector switch above already rejected unknown types.
		return errf("unknown op type %q", o.Op)
	}
}

// styleBytes reports how much of the variant's §5 style-patch budget this op
// consumes. applyStyle and addStyle are two spellings of the same thing (a CSS
// patch on the variant) and therefore share one budget.
func (o *Op) styleBytes() int {
	switch o.Op {
	case OpApplyStyle:
		n := 0
		for prop, val := range o.Props {
			n += len(prop) + len(val)
		}
		return n
	case OpAddStyle:
		return len(o.CSS)
	default:
		return 0
	}
}

// forbidExcept rejects the op if any data field other than those named (plus the
// always-present op/selector) is populated. This closes the door on an op
// carrying stray fields for a different op type.
func (o *Op) forbidExcept(allowed ...string) error {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	if o.Value != "" && !ok["value"] {
		return errf("%s: unexpected field 'value'", o.Op)
	}
	if o.Name != "" && !ok["name"] {
		return errf("%s: unexpected field 'name'", o.Op)
	}
	if o.From != "" && !ok["from"] {
		return errf("%s: unexpected field 'from'", o.Op)
	}
	if o.To != "" && !ok["to"] {
		return errf("%s: unexpected field 'to'", o.Op)
	}
	if o.URL != "" && !ok["url"] {
		return errf("%s: unexpected field 'url'", o.Op)
	}
	if len(o.Props) > 0 && !ok["props"] {
		return errf("%s: unexpected field 'props'", o.Op)
	}
	if o.HTML != "" && !ok["html"] {
		return errf("%s: unexpected field 'html'", o.Op)
	}
	if o.CSS != "" && !ok["css"] {
		return errf("%s: unexpected field 'css'", o.Op)
	}
	if o.Code != "" && !ok["code"] {
		return errf("%s: unexpected field 'code'", o.Op)
	}
	if o.Src != "" && !ok["src"] {
		return errf("%s: unexpected field 'src'", o.Op)
	}
	return nil
}

// signature is a stable key for duplicate-op detection within a variant.
func (o *Op) signature() string {
	var b strings.Builder
	b.WriteString(string(o.Op))
	b.WriteByte('|')
	b.WriteString(o.Selector)
	b.WriteByte('|')
	b.WriteString(o.Name)
	b.WriteByte('|')
	b.WriteString(o.Value)
	b.WriteByte('|')
	b.WriteString(o.From)
	b.WriteByte('|')
	b.WriteString(o.To)
	b.WriteByte('|')
	b.WriteString(o.URL)
	b.WriteByte('|')
	b.WriteString(o.HTML)
	b.WriteByte('|')
	b.WriteString(o.CSS)
	b.WriteByte('|')
	b.WriteString(o.Code)
	b.WriteByte('|')
	b.WriteString(o.Src)
	return b.String()
}

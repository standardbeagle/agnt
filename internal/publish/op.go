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

// Validate enforces the declarative op contract (spec §6): the op type is known,
// the selector is in the restricted grammar, only the fields legal for the op
// type are populated, and each field is within its allowlist/limit. There is no
// op that accepts markup or code — INV-6 holds structurally.
func (o *Op) Validate() error {
	if err := ValidateSelector(o.Selector); err != nil {
		return err
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
	default:
		return errf("unknown op type %q", o.Op)
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
	return b.String()
}

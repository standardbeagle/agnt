package publish

import (
	"regexp"
	"strings"
)

// Restricted selector grammar from the P1 spec §5a (deny-by-default). Accept
// ONLY this subset; reject everything else:
//
//	selector   := compound ( combinator compound )*   # max depth 6
//	combinator := WS | '>'                             # descendant / child only
//	compound   := ( type | '*' )? ( '.'ident | '#'ident | attr | pseudo )*
//	attr       := '[' ident ( ( '=' | '^=' | '$=' | '*=' ) quoted )? ']'
//	pseudo     := ':' ( 'first-child' | 'last-child' | 'nth-child(' INT ')' )
//	ident      := [A-Za-z_][A-Za-z0-9_-]*
//
// Forbidden (and thus rejected because they never match the above): :has(),
// :is(), :where(), :not(), sibling combinators (~ +), pseudo-elements (::x),
// namespace |, functional pseudos other than nth-child(INT), comments,
// @-anything, whitespace-smuggled tokens.
var (
	identRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*`)
	fullIdRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	// attr requires the value (when present) to be quoted with no quote/angle
	// chars inside — blocks smuggled markup.
	attrRe   = regexp.MustCompile(`^\[[A-Za-z_][A-Za-z0-9_-]*(?:(?:=|\^=|\$=|\*=)(?:"[^"<>]*"|'[^'<>]*'))?\]`)
	pseudoRe = regexp.MustCompile(`^:(?:first-child|last-child|nth-child\(-?\d+\))`)
)

// ValidateSelector reports nil iff sel is in the restricted grammar (§5a) and
// within the length/depth limits (§5).
func ValidateSelector(sel string) error {
	if sel == "" {
		return errf("selector: empty")
	}
	if len(sel) > MaxSelectorLength {
		return errf("selector: length %d exceeds max %d", len(sel), MaxSelectorLength)
	}
	compounds, err := splitCompounds(sel)
	if err != nil {
		return errf("selector %q: %w", sel, err)
	}
	if len(compounds) > MaxSelectorDepth {
		return errf("selector %q: depth %d exceeds max %d", sel, len(compounds), MaxSelectorDepth)
	}
	for _, c := range compounds {
		if err := validateCompound(c); err != nil {
			return errf("selector %q: %w", sel, err)
		}
	}
	return nil
}

// splitCompounds tokenizes sel into its compound selectors, honoring only the
// descendant (whitespace) and child ('>') combinators. It is bracket- and
// quote-aware so whitespace/'>' inside an [attr="..."] value are not treated as
// combinators. Malformed combinator operands (leading/trailing/doubled '>') are
// rejected.
func splitCompounds(sel string) ([]string, error) {
	var compounds []string
	var cur strings.Builder
	depth := 0        // inside [ ... ]
	var quote byte    // inside a quoted attr value
	sawWS := false    // whitespace seen since the last compound char
	sawChild := false // '>' seen since the last compound char

	start := func(c byte) {
		if (sawWS || sawChild) && cur.Len() > 0 {
			compounds = append(compounds, cur.String())
			cur.Reset()
		}
		sawWS, sawChild = false, false
		cur.WriteByte(c)
	}

	for i := 0; i < len(sel); i++ {
		c := sel[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		if depth > 0 {
			cur.WriteByte(c)
			switch c {
			case '"', '\'':
				quote = c
			case ']':
				depth--
			}
			continue
		}
		switch c {
		case ' ', '\t', '\n', '\r', '\f':
			if cur.Len() > 0 {
				sawWS = true
			}
		case '>':
			if sawChild {
				return nil, errf("double child combinator")
			}
			if cur.Len() == 0 {
				return nil, errf("child combinator without left operand")
			}
			sawChild = true
		case '[':
			start('[')
			depth++
		default:
			start(c)
		}
	}
	if quote != 0 || depth > 0 {
		return nil, errf("unterminated attribute")
	}
	if sawChild {
		return nil, errf("child combinator without right operand")
	}
	if cur.Len() > 0 {
		compounds = append(compounds, cur.String())
	}
	if len(compounds) == 0 {
		return nil, errf("empty selector")
	}
	return compounds, nil
}

// validateCompound checks one compound against the §5a compound production.
func validateCompound(s string) error {
	i := 0
	// optional leading type or universal '*'
	if i < len(s) && s[i] == '*' {
		i++
	} else if m := identRe.FindString(s); m != "" {
		i += len(m)
	}
	for i < len(s) {
		switch s[i] {
		case '.', '#':
			m := identRe.FindString(s[i+1:])
			if m == "" {
				return errf("bad class/id at %q", s[i:])
			}
			i += 1 + len(m)
		case '[':
			m := attrRe.FindString(s[i:])
			if m == "" {
				return errf("bad attribute selector at %q", s[i:])
			}
			i += len(m)
		case ':':
			m := pseudoRe.FindString(s[i:])
			if m == "" {
				return errf("forbidden or malformed pseudo at %q", s[i:])
			}
			i += len(m)
		default:
			return errf("unexpected character %q in compound", string(s[i]))
		}
	}
	if i == 0 {
		return errf("empty compound")
	}
	return nil
}

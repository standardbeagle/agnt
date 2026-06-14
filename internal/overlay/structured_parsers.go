package overlay

import "github.com/standardbeagle/agnt/internal/classify"

// StructuredError aliases the classify type. The structured parsers (Prisma /
// DB-auth folding) live in internal/classify — the single source of truth
// shared with the `proc output` extract path. These thin wrappers keep the
// overlay AlertScanner call sites unchanged.
type StructuredError = classify.StructuredError

// runStructuredParsers returns the first structured match for signal, or false.
func runStructuredParsers(signal string, recent []string) (*StructuredError, bool) {
	return classify.RunStructuredLine(signal, recent)
}

// isStructuralPrefix reports whether a line is a block header/banner that a
// structured parser should fold rather than surface standalone.
func isStructuralPrefix(line string) bool {
	return classify.IsStructuralPrefix(line)
}

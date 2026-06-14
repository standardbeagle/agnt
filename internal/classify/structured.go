package classify

import (
	"regexp"
	"strings"
)

// StructuredError is a compact, parsed representation of a multi-line or
// otherwise noisy error block. Structured parsers fold a raw stderr block
// (banner + call-site + cause spread across several lines) into a single
// token-efficient line so consumers emit {kind, message, file:line, cause}
// instead of a dump.
//
// This is the layer over the catch-all: the catch-all guarantees no real error
// is dropped (surfacing it verbatim as `unparsed`); the structured parsers
// upgrade the highest-value, recognizable shapes (Prisma/ORM, DB-auth) to a
// compact classified line. Pattern anchors are informed by the research docs
// under docs/error-formats/.
type StructuredError struct {
	Kind     string // "prisma", "db-auth", "db-conn"
	Category string // alert category for downstream grouping
	Severity Severity
	Message  string // primary cause/message (the actionable text)
	Op       string // failing operation, e.g. prisma.user.findUnique() (optional)
	FileLine string // file:line call site (optional)
	Hint     string // remediation hint, carried for routing (not in Compact line)
}

// Compact renders the structured error as one line:
//
//	[kind] message (op @ file:line)
//
// The remediation Hint is deliberately NOT concatenated here — it is carried as
// a struct field for the routing layer, keeping the surfaced line as short as
// possible. Absent op/file:line are omitted.
func (e StructuredError) Compact() string {
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(e.Kind)
	b.WriteString("] ")
	b.WriteString(e.Message)

	var loc string
	switch {
	case e.Op != "" && e.FileLine != "":
		loc = e.Op + " @ " + e.FileLine
	case e.Op != "":
		loc = e.Op
	case e.FileLine != "":
		loc = e.FileLine
	}
	if loc != "" {
		b.WriteString(" (")
		b.WriteString(loc)
		b.WriteString(")")
	}
	return b.String()
}

// structuredParser inspects the current signal line plus the recent non-empty
// lines (oldest→newest) preceding it and, if it recognizes a known error shape,
// returns the folded StructuredError. Parsers run only on lines the regular
// pattern bank did not classify.
type structuredParser func(signal string, recent []string) (*StructuredError, bool)

var (
	// Prisma block header line, e.g. "prisma:error".
	prismaHeaderRe = regexp.MustCompile(`^prisma:(error|warn)`)
	// Prisma invocation banner: Invalid `prisma.user.findUnique()` invocation
	prismaInvocationRe = regexp.MustCompile("Invalid `(prisma\\.[\\w$]+\\.[\\w$]+\\(\\))` invocation")
	// Bare file:line[:col] call-site line (Prisma prints it on its own line).
	fileLineRe = regexp.MustCompile(`^(/?[\w./\-]+\.[A-Za-z]\w*):(\d+)(?::\d+)?$`)
)

// prismaCause classifies a Prisma cause line, returning a short remediation
// hint. The wording is Prisma-specific (its exact client/engine messages), so a
// match attributes the line to Prisma even when the banner scrolled out of the
// recent window.
func prismaCause(signal string) (hint string, ok bool) {
	switch {
	case strings.Contains(signal, "Authentication failed against database server"):
		return "check DB credentials", true
	case strings.Contains(signal, "Can't reach database server"):
		return "DB unreachable — check host/port and that it is running", true
	case strings.Contains(signal, "Unique constraint failed"):
		return "duplicate value violates a unique index", true
	case strings.Contains(signal, "Foreign key constraint failed"):
		return "referenced row is missing or locked", true
	case strings.Contains(signal, "Timed out fetching a new connection from the connection pool"):
		return "Prisma connection pool exhausted", true
	}
	return "", false
}

// parsePrisma folds a Prisma client error block into one structured line.
func parsePrisma(signal string, recent []string) (*StructuredError, bool) {
	hint, ok := prismaCause(signal)
	if !ok {
		return nil, false
	}
	se := &StructuredError{
		Kind:     "prisma",
		Category: "prisma",
		Severity: SeverityError,
		Message:  signal,
		Hint:     hint,
	}
	// Enrich with the call site from the preceding banner/file:line lines.
	for _, line := range recent {
		if m := prismaInvocationRe.FindStringSubmatch(line); m != nil {
			se.Op = m[1]
		}
		if m := fileLineRe.FindStringSubmatch(line); m != nil {
			se.FileLine = m[1] + ":" + m[2]
		}
	}
	return se, true
}

var (
	pgAuthRe    = regexp.MustCompile(`(?i)password authentication failed for user`)
	dbTooManyRe = regexp.MustCompile(`(?i)too many connections|remaining connection slots are reserved`)
	dbNoExistRe = regexp.MustCompile(`(?i)database "?[\w$-]+"? does not exist`)
)

// parseDBAuth folds a raw Postgres/MySQL driver auth or connection failure (no
// ORM banner) into a structured line. ECONNREFUSED is intentionally NOT handled
// here — the existing `connection-refused` line rule classifies it before
// structured parsers run.
func parseDBAuth(signal string, _ []string) (*StructuredError, bool) {
	switch {
	case pgAuthRe.MatchString(signal):
		return &StructuredError{Kind: "db-auth", Category: "database", Severity: SeverityError, Message: signal, Hint: "check DB user/password"}, true
	case dbNoExistRe.MatchString(signal):
		return &StructuredError{Kind: "db-auth", Category: "database", Severity: SeverityError, Message: signal, Hint: "create the database or fix its name"}, true
	case dbTooManyRe.MatchString(signal):
		return &StructuredError{Kind: "db-conn", Category: "database", Severity: SeverityError, Message: signal, Hint: "connection pool / server max connections exhausted"}, true
	}
	return nil, false
}

// structuredParsers is the ordered registry: most specific first.
var structuredParsers = []structuredParser{
	parsePrisma,
	parseDBAuth,
}

// RunStructuredLine returns the first structured match for signal, or false.
func RunStructuredLine(signal string, recent []string) (*StructuredError, bool) {
	for _, p := range structuredParsers {
		if se, ok := p(signal, recent); ok {
			return se, true
		}
	}
	return nil, false
}

// IsStructuralPrefix reports whether a line is a block header/banner that
// precedes a cause line and should be folded by a structured parser rather than
// surfaced standalone (it would otherwise trip the catch-all as `unparsed`
// noise — e.g. "prisma:error" contains "error", the invocation banner contains
// "Invalid"). The cause line that follows reaches back for these via the recent
// ring. A prefix with no following recognized cause is simply dropped — a bare
// header is not independently actionable.
func IsStructuralPrefix(line string) bool {
	return prismaHeaderRe.MatchString(line) || prismaInvocationRe.MatchString(line)
}

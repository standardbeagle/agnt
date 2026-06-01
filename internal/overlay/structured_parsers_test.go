package overlay

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectBatches runs the block through a scanner and returns all matches.
func collectBatches(t *testing.T, lines []string, scriptID string) []*AlertMatch {
	t.Helper()
	var got []*AlertMatch
	var mu sync.Mutex
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  30 * time.Millisecond,
		DedupeWindow: 60 * time.Second,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			got = append(got, b.Matches...)
			mu.Unlock()
		},
	})
	defer scanner.Stop()
	for _, l := range lines {
		scanner.ProcessLine(l, scriptID)
	}
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	out := make([]*AlertMatch, len(got))
	copy(out, got)
	return out
}

// TestG3_PrismaBlockFoldsToOneStructuredLine: the multi-line Prisma auth block
// must surface as EXACTLY ONE structured alert — kind=prisma, carrying the
// extracted call site (op + file:line) on the same line as the cause — instead
// of the several `unparsed` alerts G2 would emit for the header/banner/cause.
func TestG3_PrismaBlockFoldsToOneStructuredLine(t *testing.T) {
	got := collectBatches(t, prismaAuthBlock, "api")

	require.Len(t, got, 1, "the whole Prisma block must fold to a single structured alert, got %d: %+v", len(got), got)
	m := got[0]
	require.NotNil(t, m.Pattern)
	assert.Equal(t, "prisma", m.Pattern.Category, "structured Prisma alert must carry the prisma category")
	assert.Equal(t, AlertSeverityError, m.Pattern.Severity)

	line := m.Line
	assert.NotContains(t, line, "\n", "structured surfacing must be a single line, not a raw block")
	assert.Contains(t, line, "Authentication failed against database server", "cause must be preserved")
	assert.Contains(t, line, "/app/src/db.ts:42", "file:line call site must be extracted from the banner block")
	assert.Contains(t, line, "prisma.user.findUnique()", "the failing model.op must be extracted")
}

// TestG3_TokenRotReduced: the single structured line must be measurably shorter
// than the raw multi-line block it replaces (token-rot reduction, per the [G3]
// acceptance criterion).
func TestG3_TokenRotReduced(t *testing.T) {
	got := collectBatches(t, prismaAuthBlock, "api")
	require.Len(t, got, 1)

	rawBlock := strings.Join(prismaAuthBlock, "\n")
	structured := got[0].Line
	assert.Less(t, len(structured), len(rawBlock),
		"structured line (%d chars) must be shorter than the raw block (%d chars)", len(structured), len(rawBlock))
}

// TestG3_GenericPgAuthStructured: a raw Postgres driver auth failure (no ORM
// banner) folds to a db-auth structured alert with a remediation hint captured.
func TestG3_GenericPgAuthStructured(t *testing.T) {
	got := collectBatches(t, []string{
		`error: password authentication failed for user "ai_user"`,
	}, "api")

	require.Len(t, got, 1)
	m := got[0]
	require.NotNil(t, m.Pattern)
	assert.Equal(t, "database", m.Pattern.Category)
	assert.Equal(t, AlertSeverityError, m.Pattern.Severity)
	assert.Contains(t, m.Line, "password authentication failed for user")
}

// TestG3_NoFalsePositiveOnOrdinaryLines: ordinary log lines that aren't a known
// structured error shape must NOT be picked up by a structured parser (they may
// still flow through the existing pattern bank / catch-all, but the structured
// layer must stay silent).
func TestG3_NoFalsePositiveOnOrdinaryLines(t *testing.T) {
	se, ok := runStructuredParsers("Server listening on port 3000", nil)
	assert.False(t, ok, "ordinary line must not match a structured parser")
	assert.Nil(t, se)

	se2, ok2 := runStructuredParsers("GET /api/users 200 12ms", nil)
	assert.False(t, ok2)
	assert.Nil(t, se2)
}

// TestG3_StructuralPrefixWithoutCauseIsDropped pins the intentional design:
// a structural prefix line (prisma block header or invocation banner) that is
// NOT followed by a recognized cause produces NO alert — a bare header is not
// independently actionable, and surfacing it as `unparsed` would be noise. This
// is the suppression's documented trade-off; the test gives future widenings of
// prismaCause a signal if the boundary shifts.
func TestG3_StructuralPrefixWithoutCauseIsDropped(t *testing.T) {
	got := collectBatches(t, []string{
		"prisma:error",
		"Invalid `prisma.user.findUnique()` invocation in",
		"/app/src/db.ts:42:18",
	}, "api")
	assert.Empty(t, got, "structural prefixes with no following recognized cause must not surface standalone")
}

// TestG3_StructuralPrefixThenCauseSurfacesOnce is the complement: once a
// recognized cause DOES follow, the whole block surfaces as exactly one
// structured alert (guards that suppression never swallows a real error).
func TestG3_StructuralPrefixThenCauseSurfacesOnce(t *testing.T) {
	got := collectBatches(t, prismaAuthBlock, "api")
	require.Len(t, got, 1, "prefix + cause must surface as exactly one structured alert")
	assert.Equal(t, "prisma", got[0].Pattern.Category)
}

// TestStructuredError_Compact verifies the one-line render shape and omission of
// absent fields.
func TestStructuredError_Compact(t *testing.T) {
	full := StructuredError{Kind: "prisma", Message: "boom", Op: "prisma.user.find()", FileLine: "db.ts:42"}
	assert.Equal(t, "[prisma] boom (prisma.user.find() @ db.ts:42)", full.Compact())

	noLoc := StructuredError{Kind: "db-auth", Message: "bad creds"}
	assert.Equal(t, "[db-auth] bad creds", noLoc.Compact())

	opOnly := StructuredError{Kind: "prisma", Message: "boom", Op: "prisma.user.find()"}
	assert.Equal(t, "[prisma] boom (prisma.user.find())", opOnly.Compact())
}

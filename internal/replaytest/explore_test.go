package replaytest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct{ findings []BreadthFinding }

func (s stubRunner) Run(ctx context.Context, seed ExploreSeed) (BreadthFinding, error) {
	return s.findings[seed.Index], nil
}

func TestExploreMergesAndPromotes(t *testing.T) {
	sc := &Scenario{Name: "x"}
	rep := NewReport("x")
	runner := stubRunner{findings: []BreadthFinding{
		{Crashes: []Crash{{Route: "/a", Selector: "btn", Error: "boom"}}, NewAssertions: []Assertion{{Selector: "h2", Type: AssertPresent}}},
		{Crashes: nil, NewAssertions: nil},
	}}
	err := Explore(context.Background(), sc, rep, runner, 2, "")
	require.NoError(t, err)
	assert.Equal(t, 1, rep.CrashCount())
	assert.Len(t, rep.NewAsserts, 1)
}

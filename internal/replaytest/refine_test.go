package replaytest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubLLM struct{ maskSelectors []string }

func (s stubLLM) RefineAssertions(ctx context.Context, steps []Step) ([]Step, error) {
	for i := range steps {
		for j := range steps[i].Assertions {
			for _, m := range s.maskSelectors {
				if steps[i].Assertions[j].Selector == m {
					steps[i].Assertions[j].Mask = true
				}
			}
		}
	}
	return steps, nil
}

func TestRefineMasksVolatile(t *testing.T) {
	sc := &Scenario{Steps: []Step{{Assertions: []Assertion{
		{Selector: ".timestamp", Type: AssertText, Expect: "12:04"},
		{Selector: "h1", Type: AssertText, Expect: "Today"},
	}}}}
	err := Refine(context.Background(), sc, stubLLM{maskSelectors: []string{".timestamp"}})
	require.NoError(t, err)
	assert.True(t, sc.Steps[0].Assertions[0].Mask)
	assert.False(t, sc.Steps[0].Assertions[1].Mask)
}

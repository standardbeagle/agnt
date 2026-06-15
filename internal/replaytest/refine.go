package replaytest

import "context"

// LLMClient refines auto-captured assertions: masking volatile content and
// keeping high-signal checks. Injected so CI runs against a stub.
type LLMClient interface {
	RefineAssertions(ctx context.Context, steps []Step) ([]Step, error)
}

// Refine mutates the Scenario's steps in place using the provided client.
func Refine(ctx context.Context, sc *Scenario, client LLMClient) error {
	steps, err := client.RefineAssertions(ctx, sc.Steps)
	if err != nil {
		return err
	}
	sc.Steps = steps
	return nil
}

package replaytest

import "context"

// ExploreSeed identifies one exploration run within a breadth fan-out.
type ExploreSeed struct {
	Index int
	Route string
}

// BreadthFinding holds the output of a single exploration seed.
type BreadthFinding struct {
	StatesVisited int         `json:"states_visited"`
	Crashes       []Crash     `json:"crashes"`
	NewAssertions []Assertion `json:"new_assertions"`
}

// BreadthRunner runs one exploration seed (in production: dispatch a
// browser-debugger subagent against an isolated worker-mocked context).
type BreadthRunner interface {
	Run(ctx context.Context, seed ExploreSeed) (BreadthFinding, error)
}

// Explore fans out `agents` seeds through the runner and merges findings into
// the report; newly discovered stable assertions are promoted onto the report.
func Explore(ctx context.Context, sc *Scenario, rep *Report, runner BreadthRunner, agents int, preset string) error {
	for i := 0; i < agents; i++ {
		f, err := runner.Run(ctx, ExploreSeed{Index: i})
		if err != nil {
			return err
		}
		rep.Crashes = append(rep.Crashes, f.Crashes...)
		rep.NewAsserts = append(rep.NewAsserts, f.NewAssertions...)
	}
	return nil
}

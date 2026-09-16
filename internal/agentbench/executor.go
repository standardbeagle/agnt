package agentbench

// Call is one tool invocation the follower issues while replaying a
// scenario's contract workflow.
type Call struct {
	Tool   string
	Action string
	Args   map[string]any
	// Code carries the page JS for proxy exec calls.
	Code string
}

// key is the fixture-lookup identity of a call.
func (c Call) key() string {
	return c.Tool + "\x00" + c.Action + "\x00" + canonicalArgs(c.Args) + "\x00" + c.Code
}

// Response is what an Executor returns for one Call. Text is the compact
// response body; the follower reads the next action from Next when set
// (the raw-JSON `next` field path) and otherwise parses the first `next:`
// line of Text (the compact-text path). Useful/FindingKind mirror the
// Step fields — the fixture author knows which call produces the finding.
type Response struct {
	Text        string
	Next        string
	Useful      bool
	FindingKind string
	// Prescribed marks Next as a contract-prescribed step, explicitly
	// recorded in the fixture — not a pointer the shipped tool emitted.
	// Formatter-rendered responses never carry it: their next: text is
	// owned by the shipped formatter.
	Prescribed bool
}

// Executor answers follower calls. Implementations must fail loudly on an
// unexpected call — never invent a response and never silently end a run.
type Executor interface {
	Execute(call Call) (Response, error)
}

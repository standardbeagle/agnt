package overlay

import "strings"

// PaletteCommand is a single entry in the overview command palette. The palette
// replaces the old free-text shell box: instead of typing a blind command, the
// user types to filter this list, highlights an entry, and presses Enter to run
// it. Commands that take an argument document it in Arg (e.g. "<port>").
type PaletteCommand struct {
	Name string // command keyword typed to match (e.g. "kill-port")
	Arg  string // argument hint shown in the list, "" if the command takes none
	Desc string // one-line description of what the command does
}

// paletteCommands is the static registry of overview commands. Order is the
// display order when the filter query is empty.
var paletteCommands = []PaletteCommand{
	{Name: "start", Arg: "<script>", Desc: "start a stopped script"},
	{Name: "stop", Arg: "<script>", Desc: "stop a running script"},
	{Name: "restart", Arg: "<script>", Desc: "restart a script"},
	{Name: "kill-port", Arg: "<port>", Desc: "free a TCP port (kill its owner)"},
	{Name: "kill-orphans", Desc: "reap orphaned process groups"},
	{Name: "restart-proxy", Arg: "<id>", Desc: "restart a reverse proxy"},
	{Name: "stop-proxy", Arg: "<id>", Desc: "stop a reverse proxy"},
	{Name: "stop-tunnel", Arg: "<id>", Desc: "stop a tunnel"},
	{Name: "toggle-ports", Desc: "show/hide system & infra ports"},
	{Name: "dismiss", Arg: "<n>", Desc: "dismiss a notice by number"},
	{Name: "dismiss-all", Desc: "dismiss all notices"},
	{Name: "summarize", Desc: "AI summary of system state"},
	{Name: "reconnect", Desc: "reconnect to the daemon"},
	{Name: "run", Arg: "<shell…>", Desc: "run an ad-hoc shell command"},
}

// filterPaletteCommands splits the buffer into a command query (the first
// whitespace-delimited token) and the remaining args, then returns the registry
// entries whose Name is prefixed by the query. An empty query matches every
// command. The returned query/args let the caller both render the filter and
// dispatch the highlighted command with its argument.
func filterPaletteCommands(buffer string) (matches []PaletteCommand, query, args string) {
	trimmed := strings.TrimLeft(buffer, " ")
	if i := strings.IndexByte(trimmed, ' '); i >= 0 {
		query = trimmed[:i]
		args = strings.TrimSpace(trimmed[i+1:])
	} else {
		query = trimmed
	}

	ql := strings.ToLower(query)
	for _, c := range paletteCommands {
		if ql == "" || strings.HasPrefix(c.Name, ql) {
			matches = append(matches, c)
		}
	}
	return matches, query, args
}

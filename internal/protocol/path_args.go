package protocol

import hubprotocol "github.com/standardbeagle/go-cli-server/protocol"

// PathArgs returns path as a positional argument when the wire format can
// carry it, and nothing when it cannot. Arguments are whitespace-delimited,
// so ValidateCommand refuses a path containing a space (or the terminator, or
// a lone "--"). Commands that take a project path also carry it as the JSON
// payload's "directory", which the daemon reads first; the positional copy is
// sent only when it fits, so a daemon that predates the payload still works
// for ordinary paths.
func PathArgs(path string) []string {
	if path == "" {
		return nil
	}
	if hubprotocol.ValidateCommand(&hubprotocol.Command{Verb: "PATH", Args: []string{path}}) != nil {
		return nil
	}
	return []string{path}
}

// PathPayload is the JSON payload that carries a command's project path.
type PathPayload struct {
	Directory string `json:"directory"`
}

package daemonclient

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// daemonBinaryNames are the executable basenames (lower-cased, without a
// ".exe" suffix) an agnt daemon can run as: the real binary, the
// `<exe>-daemon` copy autostart provisions (daemonCopyPath), and the legacy
// devtool-mcp alias with its copy.
var daemonBinaryNames = map[string]bool{
	"agnt":               true,
	"agnt-daemon":        true,
	"devtool-mcp":        true,
	"devtool-mcp-daemon": true,
}

// processIdentity reads a process's executable path and argv. A var so tests
// can feed an identity for any PID without a real process; the production
// value is the platform reader (readProcessIdentity).
var processIdentity = readProcessIdentity

// agntDaemonMatcher returns the ProcessMatcher handed to go-cli-server. The
// matcher's verdict decides whether a PID read from a (possibly stale) PID
// file is terminated, so it must positively identify an agnt daemon for this
// socket — after PID reuse that PID can belong to anything, including this
// very process (the MCP server or CLI running the check). A substring match on
// "agnt" anywhere in the command line or image path killed editors, shells and
// node processes whose paths merely contained the word.
func agntDaemonMatcher(socketPath string) func(pid int) bool {
	return func(pid int) bool {
		if pid <= 0 || pid == os.Getpid() {
			return false
		}
		exe, argv, ok := processIdentity(pid)
		if !ok {
			return false
		}
		return isAgntDaemonArgv(exe, argv, socketPath)
	}
}

// isAgntDaemonArgv reports whether a process with executable path exe and
// argument vector argv is an agnt daemon serving socketPath:
//
//   - the executable basename is an agnt daemon binary (daemonBinaryNames);
//   - its first two positional arguments are `daemon start` (root flags
//     --socket <v>, --socket=<v>, -d/--debug may precede them);
//   - when it names a socket with --socket, that socket is socketPath. A daemon
//     launched without --socket (default path or AGNT_SOCKET) cannot be told
//     apart here and is accepted on the first two rules alone.
func isAgntDaemonArgv(exe string, argv []string, socketPath string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(exe, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	if !daemonBinaryNames[base] || len(argv) < 3 {
		return false
	}

	var positional []string
	sock, haveSock := "", false
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--socket":
			if i+1 < len(argv) {
				sock, haveSock = argv[i+1], true
				i++
			}
		case strings.HasPrefix(a, "--socket="):
			sock, haveSock = strings.TrimPrefix(a, "--socket="), true
		case strings.HasPrefix(a, "-"):
			// boolean flag (-d/--debug) or a flag of the start subcommand
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) < 2 || positional[0] != "daemon" || positional[1] != "start" {
		return false
	}
	if haveSock && socketPath != "" && !sameSocketPath(sock, socketPath) {
		return false
	}
	return true
}

// sameSocketPath compares two socket paths; Windows pipe names are
// case-insensitive.
func sameSocketPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

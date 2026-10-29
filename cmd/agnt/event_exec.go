package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// defaultExecTimeout bounds one `incidents watch -- <command>` run so a hung
// command cannot stall the digest stream behind it.
const defaultExecTimeout = 30 * time.Second

// expandEventArgs substitutes {message}, {level}, {session} and {json} inside
// each argv element. The command is never handed to a shell: incident text can
// be page-authored (a visitor on a tunnelled proxy), so it must only ever land
// in a discrete argument, an environment variable or stdin. strings.Replacer
// is single-pass, so substituted text is not itself re-expanded.
func expandEventArgs(argv []string, line digestLine) []string {
	raw, _ := json.Marshal(line)
	r := strings.NewReplacer(
		"{message}", line.Message,
		"{level}", line.Level,
		"{session}", line.SessionCode,
		"{json}", string(raw),
	)
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = r.Replace(a)
	}
	return out
}

// runEventCommand runs argv once for one digest: the JSON line on stdin, the
// same fields in AGNT_EVENT_* environment variables, output to out. It returns
// the error rather than swallowing it; the caller reports it visibly.
func runEventCommand(ctx context.Context, argv []string, line digestLine, timeout time.Duration, out io.Writer) error {
	if len(argv) == 0 {
		return fmt.Errorf("no command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw, _ := json.Marshal(line)
	expanded := expandEventArgs(argv, line)
	c := exec.CommandContext(ctx, expanded[0], expanded[1:]...)
	c.Stdin = bytes.NewReader(append(raw, '\n'))
	c.Stdout = out
	c.Stderr = out
	c.Env = append(os.Environ(),
		"AGNT_EVENT_TYPE="+line.Type,
		"AGNT_EVENT_LEVEL="+line.Level,
		"AGNT_EVENT_MESSAGE="+line.Message,
		"AGNT_EVENT_SESSION="+line.SessionCode,
	)
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s: timed out after %s", expanded[0], timeout)
		}
		return fmt.Errorf("%s: %w", expanded[0], err)
	}
	return nil
}

// digestCommandSink returns the per-digest handler for `incidents watch --
// <command>`. Runs are sequential and in stream order. A failed run is reported
// on errOut and the stream continues: the digest already passed every delivery
// gate, and one bad run must not end the watch (a callback error would drop the
// stream and reconnect).
func digestCommandSink(ctx context.Context, argv []string, timeout time.Duration, cmdOut, errOut io.Writer) func(digestLine) {
	return func(line digestLine) {
		if err := runEventCommand(ctx, argv, line, timeout, cmdOut); err != nil && ctx.Err() == nil {
			fmt.Fprintf(errOut, "incidents watch: command failed: %v\n", err)
		}
	}
}

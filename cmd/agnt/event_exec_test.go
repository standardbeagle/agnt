package main

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExpandEventArgs(t *testing.T) {
	line := digestLine{Type: "incident_digest", SessionCode: "s1", Level: "error", Message: "boom {level}"}
	got := expandEventArgs([]string{"send", "{session}", "[{level}] {message}", "plain"}, line)
	// The substituted message contains "{level}": single-pass replacement must
	// leave it literal.
	want := []string{"send", "s1", "[error] boom {level}", "plain"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandEventArgs_NoShellInterpretation(t *testing.T) {
	line := digestLine{Message: `x"; rm -rf / #`}
	got := expandEventArgs([]string{"{message}"}, line)
	if len(got) != 1 || got[0] != line.Message {
		t.Fatalf("message must stay one argv element verbatim, got %q", got)
	}
}

func TestRunEventCommand_DeliversArgsEnvAndStdin(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(testHelperEnv, "event")
	var out bytes.Buffer
	line := digestLine{Type: "incident_digest", SessionCode: "s1", Level: "error", Message: "page broke"}
	if err := runEventCommand(context.Background(), []string{exe, "{session}", "{message}"}, line, 10*time.Second, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{"args=s1|page broke", "level=error", "message=page broke", `"session_code":"s1"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunEventCommand_FailureAndTimeout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(testHelperEnv, "exit:3")
	if err := runEventCommand(context.Background(), []string{exe}, digestLine{}, 10*time.Second, &bytes.Buffer{}); err == nil {
		t.Fatal("non-zero exit must be an error")
	}
	t.Setenv(testHelperEnv, "sleep")
	err = runEventCommand(context.Background(), []string{exe}, digestLine{}, 200*time.Millisecond, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
}

func TestDigestCommandSink_ReportsFailureAndContinues(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(testHelperEnv, "exit:3")
	var cmdOut, errOut bytes.Buffer
	sink := digestCommandSink(context.Background(), []string{exe}, 10*time.Second, &cmdOut, &errOut)
	sink(digestLine{Message: "one"})
	sink(digestLine{Message: "two"})
	if got := strings.Count(errOut.String(), "command failed"); got != 2 {
		t.Fatalf("want a visible failure per digest (2), got %d:\n%s", got, errOut.String())
	}
}

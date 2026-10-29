package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fakeLookPath(installed ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, c := range installed {
		set[c] = true
	}
	return func(c string) (string, error) {
		if set[c] {
			return "/bin/" + c, nil
		}
		return "", errors.New("not found")
	}
}

func TestDetectInstalledAgents(t *testing.T) {
	got := detectInstalledAgents(fakeLookPath("aider", "claude", "unrelated"))
	if want := []string{"claude", "aider"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (offer order, known agents only)", got, want)
	}
	if got := detectInstalledAgents(fakeLookPath()); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestChooseAgent(t *testing.T) {
	tests := []struct {
		name      string
		installed []string
		input     string
		want      string
		wantErr   bool
		wantOut   string
	}{
		{"none installed", nil, "", "", true, ""},
		{"single is picked without prompting", []string{"codex"}, "", "codex", false, "Starting Codex"},
		{"number selects", []string{"claude", "codex"}, "2\n", "codex", false, "1) Claude Code"},
		{"enter takes first", []string{"claude", "codex"}, "\n", "claude", false, ""},
		{"bad input reprompts", []string{"claude", "codex"}, "9\nx\n1\n", "claude", false, "Enter a number from 1 to 2"},
		{"eof is an error", []string{"claude", "codex"}, "", "", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := chooseAgent(tt.installed, bufio.NewReader(strings.NewReader(tt.input)), &out)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Fatalf("output %q missing %q", out.String(), tt.wantOut)
			}
		})
	}
}

func TestEmptyProjectNotice(t *testing.T) {
	empty := t.TempDir()
	if emptyProjectNotice(empty) == "" {
		t.Fatal("empty directory should produce a notice")
	}
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "package.json"), []byte(`{"name":"app","scripts":{"dev":"vite"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := emptyProjectNotice(web); got != "" {
		t.Fatalf("node project got notice %q, want none", got)
	}
}

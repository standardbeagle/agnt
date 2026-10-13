package aichannel

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// currentNpmShim is the `.cmd` text cmd-shim generates today for a node
// script. entry is the shim-relative script path in backslash form.
func currentNpmShim(entry string) string {
	return strings.Join([]string{
		`@ECHO off`,
		`GOTO start`,
		`:find_dp0`,
		`SET dp0=%~dp0`,
		`EXIT /b`,
		`:start`,
		`SETLOCAL`,
		`CALL :find_dp0`,
		``,
		`IF EXIST "%dp0%\node.exe" (`,
		`  SET "_prog=%dp0%\node.exe"`,
		`) ELSE (`,
		`  SET "_prog=node"`,
		`  SET PATHEXT=%PATHEXT:;.JS;=;%`,
		`)`,
		``,
		`endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & "%_prog%"  "%dp0%\` + entry + `" %*`,
		``,
	}, "\r\n")
}

// legacyNpmShim is the older two-branch form.
func legacyNpmShim(entry string) string {
	return strings.Join([]string{
		`@IF EXIST "%~dp0\node.exe" (`,
		`  "%~dp0\node.exe"  "%~dp0\` + entry + `" %*`,
		`) ELSE (`,
		`  @SETLOCAL`,
		`  @SET PATHEXT=%PATHEXT:;.JS;=;%`,
		`  node  "%~dp0\` + entry + `" %*`,
		`)`,
		``,
	}, "\r\n")
}

// TestParseNpmCmdShim covers the shim shapes that are resolved and the ones
// that must not be guessed at. Pure text in, struct out: runs on every OS.
func TestParseNpmCmdShim(t *testing.T) {
	const entry = `node_modules\@anthropic-ai\claude-code\cli.js`
	cases := []struct {
		name    string
		content string
		want    shimTarget
		ok      bool
	}{
		{
			name:    "current cmd-shim output",
			content: currentNpmShim(entry),
			want:    shimTarget{Entry: entry, Node: true},
			ok:      true,
		},
		{
			name:    "legacy two-branch shim",
			content: legacyNpmShim(entry),
			want:    shimTarget{Entry: entry, Node: true},
			ok:      true,
		},
		{
			name:    "LF line endings",
			content: strings.ReplaceAll(currentNpmShim(entry), "\r\n", "\n"),
			want:    shimTarget{Entry: entry, Node: true},
			ok:      true,
		},
		{
			name:    "quoted path containing spaces",
			content: currentNpmShim(`node_modules\my pkg\dist dir\cli.js`),
			want:    shimTarget{Entry: `node_modules\my pkg\dist dir\cli.js`, Node: true},
			ok:      true,
		},
		{
			name:    "node flags between node and the script",
			content: "@ECHO off\r\nnode --no-warnings --enable-source-maps \"%~dp0\\" + entry + "\" %*\r\n",
			want:    shimTarget{Entry: entry, Node: true, NodeArgs: []string{"--no-warnings", "--enable-source-maps"}},
			ok:      true,
		},
		{
			name:    "parent-relative entry (linked package)",
			content: currentNpmShim(`..\lib\pkg\bin\cli.js`),
			want:    shimTarget{Entry: `..\lib\pkg\bin\cli.js`, Node: true},
			ok:      true,
		},
		{
			name:    "native binary launched without an interpreter",
			content: "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\"%dp0%\\node_modules\\pkg\\bin\\tool.exe\"   %*\r\n",
			want:    shimTarget{Entry: `node_modules\pkg\bin\tool.exe`},
			ok:      true,
		},

		// Not shims, or shims this cannot reproduce faithfully.
		{name: "empty", content: ""},
		{name: "ordinary batch file", content: "@echo off\r\necho hello %*\r\n"},
		{name: "wrapper around another command", content: "@echo off\r\nclaude --verbose %*\r\n"},
		{name: "script path not under the shim dir", content: "@echo off\r\nnode \"C:\\tools\\cli.js\" %*\r\n"},
		{name: "interpreter is not node", content: "@echo off\r\npython \"%~dp0\\tool.py\" %*\r\n"},
		{name: "%_prog% without the node assignment", content: "@echo off\r\n\"%_prog%\" \"%dp0%\\" + entry + "\" %*\r\n"},
		{name: "script with no interpreter", content: "@echo off\r\n\"%dp0%\\" + entry + "\" %*\r\n"},
		{name: "path needing further expansion", content: "@echo off\r\nnode \"%~dp0\\%PKG%\\cli.js\" %*\r\n"},
		{name: "non-flag argument before the script", content: "@echo off\r\nnode run \"%~dp0\\" + entry + "\" %*\r\n"},
		{name: "arguments not forwarded", content: "@echo off\r\nnode \"%~dp0\\" + entry + "\"\r\n"},
		{name: "unterminated quote", content: "@echo off\r\nnode \"%~dp0\\" + entry + " %*\r\n"},
		{name: "pnpm NODE_PATH shim", content: "@SETLOCAL\r\n@SET NODE_PATH=%~dp0\\..\\x\r\n" + currentNpmShim(entry)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseNpmCmdShim(tc.content)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (target %+v)", ok, tc.ok, got)
			}
			if ok && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("target = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// writeShimFixture lays out <dir>/<name>.cmd pointing at a script that exists,
// and returns the shim and script paths.
func writeShimFixture(t *testing.T, dir, name, entry, content string) (shim, script string) {
	t.Helper()
	script = filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(entry, `\`, "/")))
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(script, []byte("// fixture entry point\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	shim = filepath.Join(dir, name+".cmd")
	if err := os.WriteFile(shim, []byte(content), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return shim, script
}

// TestResolveNpmCmdShim covers which node is chosen and the reasons a shim is
// reported unresolvable. Filesystem only — no process is started — so it runs
// on every OS.
func TestResolveNpmCmdShim(t *testing.T) {
	const entry = `node_modules\pkg\cli.js`
	noNode := func(string) (string, error) { return "", errors.New("not found") }
	pathNode := func(name string) (string, error) { return filepath.Join("on-path", name+".exe"), nil }

	t.Run("prefers node.exe beside the shim", func(t *testing.T) {
		dir := t.TempDir()
		shim, script := writeShimFixture(t, dir, "claude", entry, currentNpmShim(entry))
		local := filepath.Join(dir, "node.exe")
		if err := os.WriteFile(local, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := resolveNpmCmdShim(shim, pathNode)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.Path != local || !reflect.DeepEqual(got.Args, []string{script}) {
			t.Fatalf("got %+v, want %s [%s]", got, local, script)
		}
	})

	t.Run("falls back to node on PATH", func(t *testing.T) {
		shim, script := writeShimFixture(t, t.TempDir(), "claude", entry, legacyNpmShim(entry))
		got, err := resolveNpmCmdShim(shim, pathNode)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.Path != filepath.Join("on-path", "node.exe") || !reflect.DeepEqual(got.Args, []string{script}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("node flags precede the script", func(t *testing.T) {
		content := "node --no-warnings \"%~dp0\\" + entry + "\" %*\r\n"
		shim, script := writeShimFixture(t, t.TempDir(), "tool", entry, content)
		got, err := resolveNpmCmdShim(shim, pathNode)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !reflect.DeepEqual(got.Args, []string{"--no-warnings", script}) {
			t.Fatalf("args = %v", got.Args)
		}
	})

	t.Run("native binary needs no node", func(t *testing.T) {
		const bin = `node_modules\pkg\bin\tool.exe`
		shim, exe := writeShimFixture(t, t.TempDir(), "tool", bin, "\"%dp0%\\"+bin+"\"   %*\r\n")
		got, err := resolveNpmCmdShim(shim, noNode)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.Path != exe || len(got.Args) != 0 {
			t.Fatalf("got %+v, want %s with no args", got, exe)
		}
	})

	failures := []struct {
		name    string
		prepare func(t *testing.T) string
		want    string
	}{
		{"no node anywhere", func(t *testing.T) string {
			shim, _ := writeShimFixture(t, t.TempDir(), "claude", entry, currentNpmShim(entry))
			return shim
		}, "needs node"},
		{"entry script missing", func(t *testing.T) string {
			shim, script := writeShimFixture(t, t.TempDir(), "claude", entry, currentNpmShim(entry))
			if err := os.Remove(script); err != nil {
				t.Fatal(err)
			}
			return shim
		}, "does not exist"},
		{"not a shim", func(t *testing.T) string {
			shim, _ := writeShimFixture(t, t.TempDir(), "wrapper", entry, "@echo off\r\nclaude --verbose %*\r\n")
			return shim
		}, "not a recognized npm command shim"},
		{"unreadable", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "missing.cmd")
		}, "cannot read"},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveNpmCmdShim(tc.prepare(t), noNode)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestIsBatchShimPath(t *testing.T) {
	for path, want := range map[string]bool{
		`C:\npm\claude.cmd`: true, `C:\npm\CLAUDE.CMD`: true, `x.bat`: true,
		`C:\bin\claude.exe`: false, `claude`: false, `/usr/bin/claude`: false,
	} {
		if got := isBatchShimPath(path); got != want {
			t.Errorf("isBatchShimPath(%q) = %v, want %v", path, got, want)
		}
	}
}

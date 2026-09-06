package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), AgntConfigFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func readConfig(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return string(body)
}

// TestSetProxyStatusURL_PreservesComments is the reason this edits text rather
// than re-marshalling: the shipped default config is mostly commented-out
// documentation, and a one-key change from the overlay must not delete it.
func TestSetProxyStatusURL_PreservesComments(t *testing.T) {
	path := writeConfig(t, `// Agnt Configuration
proxies {
    // the dev server, fronted for browser debugging
    dev {
        url "http://localhost:5173"
        listen-port 19191
    }
}
`)
	if err := SetProxyStatusURL(path, "dev", "https://box.tail1234.ts.net"); err != nil {
		t.Fatalf("SetProxyStatusURL: %v", err)
	}

	got := readConfig(t, path)
	for _, keep := range []string{
		"// Agnt Configuration",
		"// the dev server, fronted for browser debugging",
		`url "http://localhost:5173"`,
		"listen-port 19191",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("edit dropped %q from the file:\n%s", keep, got)
		}
	}
	if !strings.Contains(got, `status-url "https://box.tail1234.ts.net"`) {
		t.Errorf("status-url not written:\n%s", got)
	}

	cfg, err := LoadAgntConfigFile(path)
	if err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if cfg.Proxies["dev"].StatusURL != "https://box.tail1234.ts.net" {
		t.Errorf("parsed StatusURL = %q", cfg.Proxies["dev"].StatusURL)
	}
	if cfg.Proxies["dev"].ListenPort != 19191 {
		t.Errorf("edit disturbed listen-port: %d", cfg.Proxies["dev"].ListenPort)
	}
}

func TestSetProxyStatusURL_ReplacesExistingValue(t *testing.T) {
	path := writeConfig(t, `proxies {
    dev {
        url "http://localhost:5173"
        status-url "https://old.example.com"
    }
}
`)
	if err := SetProxyStatusURL(path, "dev", "https://new.example.com"); err != nil {
		t.Fatalf("SetProxyStatusURL: %v", err)
	}
	got := readConfig(t, path)
	if strings.Contains(got, "old.example.com") {
		t.Errorf("old value survived:\n%s", got)
	}
	if strings.Count(got, "status-url") != 1 {
		t.Errorf("expected exactly one status-url line:\n%s", got)
	}
}

func TestSetProxyStatusURL_CreatesMissingBlocks(t *testing.T) {
	// A config that never declared this proxy still has to accept the pin,
	// otherwise the overlay command would work only on projects that already
	// hand-wrote the block.
	path := writeConfig(t, "scripts {\n    dev {\n        run \"npm run dev\"\n    }\n}\n")
	if err := SetProxyStatusURL(path, "web", "https://box.tail1234.ts.net"); err != nil {
		t.Fatalf("SetProxyStatusURL: %v", err)
	}
	cfg, err := LoadAgntConfigFile(path)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, readConfig(t, path))
	}
	if cfg.Proxies["web"] == nil || cfg.Proxies["web"].StatusURL != "https://box.tail1234.ts.net" {
		t.Fatalf("proxy block not created:\n%s", readConfig(t, path))
	}
	if cfg.Scripts["dev"] == nil {
		t.Errorf("edit disturbed the scripts block:\n%s", readConfig(t, path))
	}
}

func TestSetProxyStatusURL_EmptyValueRemovesKey(t *testing.T) {
	path := writeConfig(t, `proxies {
    dev {
        url "http://localhost:5173"
        status-url "https://box.tail1234.ts.net"
    }
}
`)
	if err := SetProxyStatusURL(path, "dev", ""); err != nil {
		t.Fatalf("SetProxyStatusURL: %v", err)
	}
	got := readConfig(t, path)
	if strings.Contains(got, "status-url") {
		t.Errorf("key not removed:\n%s", got)
	}
	if !strings.Contains(got, `url "http://localhost:5173"`) {
		t.Errorf("removal took the sibling with it:\n%s", got)
	}
}

// TestSetProxyStatusURL_RefusesUnparseableResult pins the safety property: the
// file the daemon loads must always parse, so a change that would break it is
// refused and the original is left byte-identical.
func TestSetProxyStatusURL_RefusesUnparseableResult(t *testing.T) {
	body := "proxies {\n    dev {\n        url \"http://localhost:5173\"\n" // deliberately unbalanced
	path := writeConfig(t, body)

	err := SetProxyStatusURL(path, "dev", "https://box.tail1234.ts.net")
	if err == nil {
		t.Fatal("edit of an unbalanced config was accepted")
	}
	if got := readConfig(t, path); got != body {
		t.Errorf("refused edit still modified the file:\n%s", got)
	}
}

// TestStripComment_IgnoresURLSlashes guards the one place a naive comment strip
// would corrupt a value: the "//" inside every URL this feature writes.
func TestStripComment_IgnoresURLSlashes(t *testing.T) {
	line := `        status-url "https://box.tail1234.ts.net" // pinned`
	got := stripComment(line)
	if !strings.Contains(got, "https://box.tail1234.ts.net") {
		t.Errorf("stripComment ate the URL: %q", got)
	}
	if strings.Contains(got, "pinned") {
		t.Errorf("stripComment kept the trailing comment: %q", got)
	}
}

func TestSetProxyStatusURL_QuotesAreEscaped(t *testing.T) {
	path := writeConfig(t, "proxies {\n    dev {\n        url \"http://localhost:5173\"\n    }\n}\n")
	if err := SetProxyStatusURL(path, "dev", `https://box.ts.net/"odd"`); err != nil {
		t.Fatalf("SetProxyStatusURL: %v", err)
	}
	cfg, err := LoadAgntConfigFile(path)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, readConfig(t, path))
	}
	if cfg.Proxies["dev"].StatusURL != `https://box.ts.net/"odd"` {
		t.Errorf("round trip lost the quotes: %q", cfg.Proxies["dev"].StatusURL)
	}
}

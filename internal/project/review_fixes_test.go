package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasWord_BoundaryAfterSkippedMatch(t *testing.T) {
	cases := []struct {
		hay, word string
		want      bool
	}{
		{"aaa", "aa", false},
		{"xdevdev", "dev", false},
		{"dev", "dev", true},
		{"npm run dev", "dev", true},
		{"xdev dev", "dev", true},
		{"untestable", "test", false},
	}
	for _, c := range cases {
		if got := hasWord(c.hay, c.word); got != c.want {
			t.Errorf("hasWord(%q,%q)=%v want %v", c.hay, c.word, got, c.want)
		}
	}
}

func TestDetectNode_ScriptsMetadataSorted(t *testing.T) {
	dir := t.TempDir()
	pkg := `{"name":"x","scripts":{"zeta":"a","alpha":"b","mid":"c","build":"d"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		p := detectNode(dir)
		if got := p.Metadata["scripts"]; got != "alpha,build,mid,zeta" {
			t.Fatalf("scripts metadata not sorted: %q", got)
		}
	}
}

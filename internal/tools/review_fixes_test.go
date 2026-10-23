package tools

import (
	"context"
	"errors"
	"testing"
)

func TestSplitFileLine_WindowsDrivePath(t *testing.T) {
	cases := map[string][2]string{
		`C:\proj\a.go:12:3`: {`C:\proj\a.go`, "12"},
		`C:\proj\a.go:12`:   {`C:\proj\a.go`, "12"},
		"src/a.go:7:1":      {"src/a.go", "7"},
		"a.go":              {"a.go", ""},
	}
	for in, want := range cases {
		f, l := splitFileLine(in)
		if f != want[0] || l != want[1] {
			t.Errorf("splitFileLine(%q) = (%q,%q), want %v", in, f, l, want)
		}
	}
}

func TestValidateToken(t *testing.T) {
	for _, bad := range []string{"my proc", "a\nb", "a\tb"} {
		if validateToken("id", bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if validateToken("id", "abc:dev-1") != nil || validateToken("id", "") != nil {
		t.Error("plain id rejected")
	}
}

func TestWaitForSignal_UnreadableProcessEndsWithError(t *testing.T) {
	calls := 0
	res := waitForSignal(context.Background(), func() ([]string, error) {
		calls++
		return nil, errors.New("process not found")
	}, []string{"ready"}, 60000, 1)
	if res.Error == "" || res.TimedOut || calls != 3 {
		t.Fatalf("res=%+v calls=%d, want early Error after 3 failed polls", res, calls)
	}
}

package protocol

import (
	"reflect"
	"testing"
)

func TestPathArgs_OmitsPathsTheWireCannotCarry(t *testing.T) {
	cases := map[string][]string{
		"/home/u/proj":      {"/home/u/proj"},
		`C:\Users\u\proj`:   {`C:\Users\u\proj`},
		"/home/u/my proj":   nil,
		"/home/u/tab\tproj": nil,
		"/a;;b":             nil,
		"":                  nil,
	}
	for path, want := range cases {
		if got := PathArgs(path); !reflect.DeepEqual(got, want) {
			t.Errorf("PathArgs(%q) = %q, want %q", path, got, want)
		}
	}
}

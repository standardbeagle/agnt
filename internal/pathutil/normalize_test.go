package pathutil

import "testing"

func TestNormalizeTrailingSlash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty stays empty", "", ""},
		{"root preserved", "/", "/"},
		{"single trailing slash stripped", "/foo/", "/foo"},
		{"multiple trailing slashes stripped", "/foo/bar///", "/foo/bar"},
		{"no slash unchanged", "foo", "foo"},
		{"no trailing slash unchanged", "/foo/bar", "/foo/bar"},
		{"relative dot unchanged", ".", "."},
		{"home dir", "/home/user/", "/home/user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeTrailingSlash(tt.in); got != tt.want {
				t.Errorf("NormalizeTrailingSlash(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

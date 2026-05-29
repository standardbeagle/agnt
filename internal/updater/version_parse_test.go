package updater

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVersion_EdgeCases(t *testing.T) {
	cases := []struct {
		in                  string
		wantErr             bool
		major, minor, patch int
	}{
		{in: "1.2.3", major: 1, minor: 2, patch: 3},
		{in: "v1.2.3", major: 1, minor: 2, patch: 3},       // v prefix stripped
		{in: "01.2.3", major: 1, minor: 2, patch: 3},       // leading zero parsed decimal
		{in: "1.2.3-beta.1", major: 1, minor: 2, patch: 3}, // pre-release stripped
		{in: "1.2.3+build7", major: 1, minor: 2, patch: 3}, // build metadata stripped
		{in: "1.2", wantErr: true},                         // missing patch
		{in: "1", wantErr: true},                           // missing minor+patch
		{in: "", wantErr: true},                            // empty
		{in: "x.y.z", wantErr: true},                       // non-numeric
		{in: "99999999999999999999.0.0", wantErr: true},    // overflow
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			maj, min, pat, err := parseVersion(c.in)
			if c.wantErr {
				require.Error(t, err, "expected parse error for %q", c.in)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.major, maj)
			assert.Equal(t, c.minor, min)
			assert.Equal(t, c.patch, pat)
		})
	}
}

func TestIsNewer_AllBranches(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		current string
		want    bool
		wantErr bool
	}{
		{name: "newer major", tag: "v2.0.0", current: "1.9.9", want: true},
		{name: "older major", tag: "v1.0.0", current: "2.0.0", want: false},
		{name: "newer minor", tag: "v1.5.0", current: "1.4.9", want: true},
		{name: "older minor", tag: "v1.4.9", current: "1.5.0", want: false},
		{name: "newer patch", tag: "v1.4.5", current: "1.4.4", want: true},
		{name: "older patch", tag: "v1.4.3", current: "1.4.5", want: false},
		{name: "equal", tag: "v1.4.4", current: "1.4.4", want: false},
		{name: "current has v prefix", tag: "v1.0.1", current: "v1.0.0", want: true},
		{name: "bad release version", tag: "vgarbage", current: "1.0.0", wantErr: true},
		{name: "bad current version", tag: "v1.0.0", current: "garbage", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rel := &GitHubRelease{TagName: c.tag}
			got, err := rel.IsNewer(c.current)
			if c.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

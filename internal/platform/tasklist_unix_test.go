//go:build !windows

package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSplitTasklistCSVLine verifies the shared tasklist.exe CSV splitter
// respects quoted fields (the memory column embeds commas like "45,000 K")
// and strips surrounding quotes. This is the consolidated primitive used by
// both platform.scanWSLWindows and config.parseTasklistCSV.
func TestSplitTasklistCSVLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{
			name: "quoted fields with comma in memory column",
			line: `"chrome.exe","1234","Console","1","45,000 K"`,
			want: []string{"chrome.exe", "1234", "Console", "1", "45,000 K"},
		},
		{
			name: "name and pid extracted before comma-bearing column",
			line: `"node.exe","9876","Services","0","1,234,567 K"`,
			want: []string{"node.exe", "9876", "Services", "0", "1,234,567 K"},
		},
		{
			name: "unquoted simple row",
			line: `a,b,c`,
			want: []string{"a", "b", "c"},
		},
		{
			name: "single field",
			line: `"only"`,
			want: []string{"only"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitTasklistCSVLine(tc.line)
			assert.Equal(t, tc.want, got)
		})
	}
}

package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDirectoryFilterGlobalPresenceRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		filter    DirectoryFilter
		wantKey   bool
		wantValue bool
	}{
		{name: "omitted"},
		{name: "explicit false", filter: DirectoryFilter{GlobalOverride: Bool(false)}, wantKey: true},
		{name: "explicit true", filter: DirectoryFilter{GlobalOverride: Bool(true)}, wantKey: true, wantValue: true},
		{name: "legacy true", filter: DirectoryFilter{Global: true}, wantKey: true, wantValue: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(data), `"global"`); got != btoi(tc.wantKey) {
				t.Fatalf("global key count = %d in %s", got, data)
			}
			var decoded DirectoryFilter
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			value, specified := decoded.GlobalSetting()
			if specified != tc.wantKey || value != tc.wantValue {
				t.Fatalf("GlobalSetting() = (%v,%v), want (%v,%v)", value, specified, tc.wantValue, tc.wantKey)
			}
		})
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

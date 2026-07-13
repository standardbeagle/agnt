package tools

import (
	"errors"
	"testing"
)

func TestResolveEffectiveGlobal(t *testing.T) {
	for _, tc := range []struct {
		name       string
		explicit   *bool
		configured bool
		configErr  error
		want       bool
		wantErr    bool
		wantCalls  int
	}{
		{name: "omitted config false", configured: false, want: false, wantCalls: 1},
		{name: "omitted config true", configured: true, want: true, wantCalls: 1},
		{name: "omitted malformed config", configErr: errors.New("malformed config"), wantErr: true, wantCalls: 1},
		{name: "explicit false bypasses config", explicit: boolPointer(false), want: false},
		{name: "explicit true bypasses config", explicit: boolPointer(true), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, err := resolveEffectiveGlobal(tc.explicit, func() (bool, error) { calls++; return tc.configured, tc.configErr })
			if (err != nil) != tc.wantErr || got != tc.want || calls != tc.wantCalls {
				t.Fatalf("got (%v,%v,calls=%d), want (%v,err=%v,calls=%d)", got, err, calls, tc.want, tc.wantErr, tc.wantCalls)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

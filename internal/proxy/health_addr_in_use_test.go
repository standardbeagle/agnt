package proxy

import (
	"errors"
	"testing"
)

func TestIsAddressInUse(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"unix in use":            {errors.New("listen tcp 127.0.0.1:80: bind: address already in use"), true},
		"windows in use":         {errors.New("Only one usage of each socket address (protocol/network address/port) is normally permitted"), true},
		"windows reserved range": {errors.New("listen tcp 127.0.0.1:49816: bind: An attempt was made to access a socket in a way forbidden by its access permissions."), true},
		"unrelated":              {errors.New("listen tcp: lookup nowhere: no such host"), false},
		"nil":                    {nil, false},
	} {
		if got := isAddressInUse(tc.err); got != tc.want {
			t.Errorf("%s: isAddressInUse = %v, want %v", name, got, tc.want)
		}
	}
}

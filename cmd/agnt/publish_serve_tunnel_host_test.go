package main

import "testing"

func TestTunnelLocalHost(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1:9000":   "127.0.0.1",
		"0.0.0.0:9000":     "127.0.0.1",
		"[::]:9000":        "127.0.0.1",
		":9000":            "127.0.0.1",
		"192.168.1.5:9000": "192.168.1.5",
		"garbage":          "",
	} {
		if got := tunnelLocalHost(in); got != want {
			t.Errorf("tunnelLocalHost(%q) = %q, want %q", in, got, want)
		}
	}
}

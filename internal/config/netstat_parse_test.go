package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseNetstatExeAllListeners(t *testing.T) {
	output := []byte(`
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:80             0.0.0.0:0              LISTENING       1234
  TCP    0.0.0.0:443            0.0.0.0:0              LISTENING       5678
  TCP    [::]:80                [::]:0                 LISTENING       1234
  TCP    127.0.0.1:5432         0.0.0.0:0              LISTENING       9999
  TCP    [::1]:3000             [::]:0                 ABHÖREN         7777
  TCP    10.0.0.5:54321         93.184.216.34:443     ESTABLISHED     2222
  UDP    0.0.0.0:53             *:*                                    3333
`)

	owners := parseNetstatExeAllListeners(output)

	// Ports 80, 443, 3000, 5432 — localized state is accepted by stable shape.
	if len(owners) != 4 {
		t.Fatalf("got %d owners, want 4: %+v", len(owners), owners)
	}

	byPort := make(map[int]PortOwner)
	for _, o := range owners {
		byPort[o.Port] = o
		if !o.Windows {
			t.Errorf("port %d: Windows=false, want true", o.Port)
		}
	}

	for port, wantPID := range map[int]int{80: 1234, 443: 5678, 3000: 7777, 5432: 9999} {
		o, ok := byPort[port]
		if !ok {
			t.Errorf("missing port %d", port)
			continue
		}
		if o.PID != wantPID {
			t.Errorf("port %d: pid = %d, want %d", port, o.PID, wantPID)
		}
	}

	if _, ok := byPort[54321]; ok {
		t.Error("ESTABLISHED connection should not be listed as a listener")
	}
	if _, ok := byPort[53]; ok {
		t.Error("UDP socket should not be listed as a TCP listener")
	}
}

func TestParseNetstatExeAllListeners_Empty(t *testing.T) {
	if got := parseNetstatExeAllListeners(nil); got != nil {
		t.Errorf("nil input: got %+v, want nil", got)
	}
	if got := parseNetstatExeAllListeners([]byte("garbage\nno tcp rows here\n")); got != nil {
		t.Errorf("garbage input: got %+v, want nil", got)
	}
}

func TestSortPortOwners(t *testing.T) {
	owners := []PortOwner{{Port: 8080}, {Port: 80}, {Port: 443}}
	sortPortOwners(owners)
	want := []int{80, 443, 8080}
	for i, w := range want {
		if owners[i].Port != w {
			t.Errorf("sorted[%d] = %d, want %d", i, owners[i].Port, w)
		}
	}
}

// TestParseNetstatExePIDs exercises the pure parser behind the WSL
// netstat.exe port-owner fallback. This is the visibility path that lets
// the daemon see a Windows-side process holding a port a Linux dev server
// wants — a wrong parse here means the doctor/preflight either misses a
// real conflict or fingers an innocent PID.
func TestParseNetstatExePIDs(t *testing.T) {
	tests := []struct {
		name string
		out  string
		port int
		want []int
	}{
		{
			name: "single ipv4 listening row",
			out:  "  TCP    0.0.0.0:80    0.0.0.0:0    LISTENING    1234",
			port: 80,
			want: []int{1234},
		},
		{
			name: "ipv6 wildcard listening row",
			out:  "  TCP    [::]:80    [::]:0    LISTENING    5678",
			port: 80,
			want: []int{5678},
		},
		{
			name: "German localized listening state",
			out:  "  TCP    0.0.0.0:443    0.0.0.0:0    ABHÖREN    2468",
			port: 443,
			want: []int{2468},
		},
		{
			name: "ipv4 and ipv6 rows for same pid dedup to one",
			out: strings.Join([]string{
				"  TCP    0.0.0.0:8080    0.0.0.0:0    LISTENING    4321",
				"  TCP    [::]:8080    [::]:0    LISTENING    4321",
			}, "\n"),
			port: 8080,
			want: []int{4321},
		},
		{
			name: "two distinct pids on same port preserved in order",
			out: strings.Join([]string{
				"  TCP    127.0.0.1:3000    0.0.0.0:0    LISTENING    11",
				"  TCP    0.0.0.0:3000    0.0.0.0:0    LISTENING    22",
			}, "\n"),
			port: 3000,
			want: []int{11, 22},
		},
		{
			name: "ignores ESTABLISHED, UDP, and non-matching ports",
			out: strings.Join([]string{
				"  TCP    0.0.0.0:443    10.0.0.5:51234    ESTABLISHED    900",
				"  UDP    0.0.0.0:80    *:*    1100",
				"  TCP    0.0.0.0:9999    0.0.0.0:0    LISTENING    1300",
				"  TCP    0.0.0.0:80    0.0.0.0:0    LISTENING    1400",
			}, "\n"),
			port: 80,
			want: []int{1400},
		},
		{
			name: "port 80 must NOT match a :8080 listener (suffix false positive guard)",
			out: strings.Join([]string{
				"  TCP    0.0.0.0:8080    0.0.0.0:0    LISTENING    7777",
				"  TCP    0.0.0.0:1180    0.0.0.0:0    LISTENING    8888",
			}, "\n"),
			port: 80,
			want: nil,
		},
		{
			name: "malformed and zero-pid rows dropped",
			out: strings.Join([]string{
				"  TCP    0.0.0.0:80    0.0.0.0:0    LISTENING    notapid",
				"  TCP    0.0.0.0:80    0.0.0.0:0    LISTENING    0",
				"  TCP    0.0.0.0:80",
				"  TCP    0.0.0.0:80    0.0.0.0:0    LISTENING    2500",
			}, "\n"),
			port: 80,
			want: []int{2500},
		},
		{
			name: "empty output yields nil",
			out:  "",
			port: 80,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNetstatExePIDs([]byte(tc.out), tc.port)
			assert.Equal(t, tc.want, got)
		})
	}
}

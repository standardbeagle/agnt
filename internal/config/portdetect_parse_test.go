//go:build !windows

package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// TestParseProcNetTCPLine exercises the /proc/net/tcp{,6} row parser shared
// by detectPortsForPIDProc and findPIDsByPortProc. The hex local-port decode
// and the 0A (TCP_LISTEN) state filter are the load-bearing bits: a wrong
// decode maps a socket to the wrong port, and a missing state filter would
// treat outbound/established connections as listeners.
func TestParseProcNetTCPLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantInode string
		wantPort  int
		wantOK    bool
	}{
		{
			// :1F90 = 0x1F90 = 8080. inode is field index 9.
			name:      "listening ipv4 hex port 1F90 decodes to 8080",
			line:      "   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 987654 1 0000000000000000 100 0 0 10 0",
			wantInode: "987654",
			wantPort:  8080,
			wantOK:    true,
		},
		{
			// :0050 = 80.
			name:      "listening port 80",
			line:      "   1: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0",
			wantInode: "111",
			wantPort:  80,
			wantOK:    true,
		},
		{
			// ipv6 row: longer hex local IP, same :HEXPORT suffix. :01BB = 443.
			name:      "ipv6 listening row port 443",
			line:      "   2: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 222 1 0000000000000000 100 0 0 10 0",
			wantInode: "222",
			wantPort:  443,
			wantOK:    true,
		},
		{
			// State 01 = ESTABLISHED — must be rejected by the 0A filter.
			name:   "non-0A state (established) is dropped",
			line:   "   3: 0100007F:1F90 0100007F:ABCD 01 00000000:00000000 00:00000000 00000000  1000        0 333 1 0000000000000000 100 0 0 10 0",
			wantOK: false,
		},
		{
			// Truncated row (< 10 whitespace fields).
			name:   "too few fields dropped",
			line:   "   4: 00000000:1F90 0A 333",
			wantOK: false,
		},
		{
			// Malformed (non-hex) port — hex.DecodeString fails.
			name:   "non-hex port dropped",
			line:   "   5: 00000000:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444 1 0000000000000000 100 0 0 10 0",
			wantOK: false,
		},
		{
			// 1-byte hex port (odd length / not exactly 2 bytes) rejected.
			name:   "single-byte hex port rejected (len != 2)",
			line:   "   6: 00000000:50 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555 1 0000000000000000 100 0 0 10 0",
			wantOK: false,
		},
		{
			// Port 0 (:0000) rejected by the >0 guard.
			name:   "zero port rejected",
			line:   "   7: 00000000:0000 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 666 1 0000000000000000 100 0 0 10 0",
			wantOK: false,
		},
		{
			name:   "blank line dropped",
			line:   "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inode, port, ok := parseProcNetTCPLine(tc.line)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.True(t, ok)
				assert.Equal(t, tc.wantInode, inode)
				assert.Equal(t, tc.wantPort, port)
			} else {
				assert.Empty(t, inode, "rejected rows return empty inode")
				assert.Zero(t, port, "rejected rows return zero port")
			}
		})
	}
}

// TestParseProcNetTCPLine_PortRoundsCorrectly is a focused property check:
// hex local-port decode is big-endian 2-byte, so the high byte dominates.
// A regression to little-endian (byte-swap) would silently map 8080->0x901F
// = 36895. Anchoring a few known values guards the endianness.
func TestParseProcNetTCPLine_PortRoundsCorrectly(t *testing.T) {
	cases := map[string]int{
		"1F90": 8080,
		"0050": 80,
		"01BB": 443,
		"0BB8": 3000,
		"C000": 49152,
	}
	for hexPort, want := range cases {
		line := "   0: 00000000:" + hexPort + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 42 1 0000000000000000 100 0 0 10 0"
		inode, port, ok := parseProcNetTCPLine(line)
		require.True(t, ok, "hex %s should parse", hexPort)
		assert.Equal(t, "42", inode)
		assert.Equal(t, want, port, "hex %s must decode big-endian to %d", hexPort, want)
	}
}

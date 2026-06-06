//go:build !windows

package config

import "testing"

func TestParseNetstatExeAllListeners(t *testing.T) {
	output := []byte(`
Active Connections

  Proto  Local Address          Foreign Address        State           PID
  TCP    0.0.0.0:80             0.0.0.0:0              LISTENING       1234
  TCP    0.0.0.0:443            0.0.0.0:0              LISTENING       5678
  TCP    [::]:80                [::]:0                 LISTENING       1234
  TCP    127.0.0.1:5432         0.0.0.0:0              LISTENING       9999
  TCP    10.0.0.5:54321         93.184.216.34:443     ESTABLISHED     2222
  UDP    0.0.0.0:53             *:*                                    3333
`)

	owners := parseNetstatExeAllListeners(output)

	// Ports 80, 443, 5432 — established/udp rows excluded, :80 deduped across v4/v6.
	if len(owners) != 3 {
		t.Fatalf("got %d owners, want 3: %+v", len(owners), owners)
	}

	byPort := make(map[int]PortOwner)
	for _, o := range owners {
		byPort[o.Port] = o
		if !o.Windows {
			t.Errorf("port %d: Windows=false, want true", o.Port)
		}
	}

	for port, wantPID := range map[int]int{80: 1234, 443: 5678, 5432: 9999} {
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

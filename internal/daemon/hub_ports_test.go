package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
)

func TestPortIsSystem(t *testing.T) {
	tests := []struct {
		name   string
		owner  config.PortOwner
		status string
		want   bool
	}{
		{"managed never system", config.PortOwner{Port: 53, Name: "systemd-resolve"}, "managed", false},
		{"conflict never system", config.PortOwner{Port: 80, Name: "nginx"}, "conflict", false},
		{"windows-side is system", config.PortOwner{Port: 7680, PID: 4, Name: "svchost.exe", Windows: true}, "unmanaged", true},
		{"unattributed is system", config.PortOwner{Port: 631, PID: 0, Name: ""}, "unmanaged", true},
		{"privileged low port is system", config.PortOwner{Port: 22, PID: 700, Name: "sshd"}, "unmanaged", true},
		{"named daemon is system", config.PortOwner{Port: 5353, PID: 800, Name: "avahi-daemon"}, "unmanaged", true},
		{"dev node high port is NOT system", config.PortOwner{Port: 3000, PID: 1234, Name: "node"}, "unmanaged", false},
		{"postgres is NOT system", config.PortOwner{Port: 5432, PID: 2000, Name: "postgres"}, "unmanaged", false},
		{"docker is NOT system", config.PortOwner{Port: 2375, PID: 2100, Name: "dockerd"}, "unmanaged", false},
		{"custom go server high port is NOT system", config.PortOwner{Port: 8080, PID: 3000, Name: "myserver"}, "unmanaged", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := portIsSystem(tt.owner, tt.status); got != tt.want {
				t.Errorf("portIsSystem(%+v, %q) = %v, want %v", tt.owner, tt.status, got, tt.want)
			}
		})
	}
}

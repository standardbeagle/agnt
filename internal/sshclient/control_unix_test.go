//go:build !windows

package sshclient

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListenControlTransportLiveCollisionPreservesOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.ctl")
	first, err := listenControlTransport(path)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	defer first.Close()
	go ServeControl(first, "/first/project", nil)

	second, err := listenControlTransport(path)
	if err == nil {
		second.Close()
		t.Fatal("second listener replaced live owner")
	}
	if !strings.Contains(err.Error(), "already connected") {
		t.Fatalf("collision error = %v", err)
	}

	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("dial preserved owner: %v", err)
	}
	defer conn.Close()
	response, err := pingControl(conn)
	if err != nil || response.ProjectRoot != "/first/project" {
		t.Fatalf("preserved route response=%+v err=%v", response, err)
	}
}

func TestListenControlTransportReclaimsConclusiveStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.ctl")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("stale listen: %v", err)
	}
	stale.Close()

	replacement, err := listenControlTransport(path)
	if err != nil {
		t.Fatalf("reclaim stale socket: %v", err)
	}
	replacement.Close()
}

func TestDiscoverControlHostsPreservesInconclusiveBusySocket(t *testing.T) {
	home := withSandboxedHome(t)
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "busy.ctl")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen busy socket: %v", err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	release := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-release
	}()

	hosts, err := discoverControlHosts(20*time.Millisecond, func(conn net.Conn) (controlResponse, error) {
		_ = conn.SetDeadline(time.Now().Add(20 * time.Millisecond))
		if _, writeErr := conn.Write([]byte(`{"kind":"ping"}` + "\n")); writeErr != nil {
			return controlResponse{}, writeErr
		}
		_, readErr := bufio.NewReader(conn).ReadString('\n')
		return controlResponse{}, readErr
	})
	<-accepted
	close(release)
	if err != nil || len(hosts) != 0 {
		t.Fatalf("busy discovery hosts=%v err=%v", hosts, err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("inconclusive busy socket was removed: %v", err)
	}
}

func TestDiscoverControlHostsRemovesConclusiveStaleSocket(t *testing.T) {
	home := withSandboxedHome(t)
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "stale.ctl")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen stale socket: %v", err)
	}
	listener.Close()

	hosts, err := discoverControlHosts(20*time.Millisecond, pingControl)
	if err != nil || len(hosts) != 0 {
		t.Fatalf("stale discovery hosts=%v err=%v", hosts, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("stale socket remains: %v", err)
	}
}

//go:build !windows

package sshclient

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func installControlUnixHook(t *testing.T, stage, path string, fn func()) {
	t.Helper()
	var once sync.Once
	controlUnixTestHook.Store(&controlUnixHook{run: func(gotStage, gotPath string) {
		if gotStage == stage && gotPath == path {
			once.Do(fn)
		}
	}})
	t.Cleanup(func() { controlUnixTestHook.Store(nil) })
}

func replaceWithLiveControl(t *testing.T, path, project string) net.Listener {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove stale path: %v", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind replacement: %v", err)
	}
	go ServeControl(listener, project, nil)
	return listener
}

func makeStaleControlSocket(t *testing.T, path string) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind stale socket: %v", err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}
}

func assertControlRoute(t *testing.T, path, project string) {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("dial replacement: %v", err)
	}
	defer conn.Close()
	response, err := pingControl(conn)
	if err != nil || response.ProjectRoot != project {
		t.Fatalf("replacement route response=%+v err=%v", response, err)
	}
}

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
	makeStaleControlSocket(t, path)

	replacement, err := listenControlTransport(path)
	if err != nil {
		t.Fatalf("reclaim stale socket: %v", err)
	}
	replacement.Close()
}

func TestListenControlTransportReplacementSurvivesEveryClaimInterval(t *testing.T) {
	for _, stage := range []string{"listen-after-probe", "after-quarantine", "listen-before-delete"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "replace.ctl")
			makeStaleControlSocket(t, path)
			var replacement net.Listener
			installControlUnixHook(t, stage, path, func() {
				replacement = replaceWithLiveControl(t, path, "/replacement")
			})
			listener, err := listenControlTransport(path)
			if listener != nil {
				listener.Close()
				t.Fatal("stale claimant replaced injected live owner")
			}
			if err == nil {
				t.Fatal("expected collision after replacement")
			}
			defer replacement.Close()
			assertControlRoute(t, path, "/replacement")
		})
	}
}

func TestOwnedControlListenerCloseNeverUnlinksNewerGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generation.ctl")
	first, err := listenControlTransport(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close generation A: %v", err)
	}

	second, err := listenControlTransport(path)
	if err != nil {
		t.Fatal(err)
	}
	var third net.Listener
	installControlUnixHook(t, "after-quarantine", path, func() {
		third = replaceWithLiveControl(t, path, "/generation-c")
	})
	if err := second.Close(); err == nil {
		t.Fatal("generation B close did not surface quarantine collision")
	}
	defer third.Close()
	assertControlRoute(t, path, "/generation-c")
}

func TestDiscoverControlHostsPreservesInconclusiveBusySocket(t *testing.T) {
	withShortSandboxedHome(t)
	home := os.Getenv("HOME")
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
	withShortSandboxedHome(t)
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "stale.ctl")
	makeStaleControlSocket(t, path)

	hosts, err := discoverControlHosts(20*time.Millisecond, pingControl)
	if err != nil || len(hosts) != 0 {
		t.Fatalf("stale discovery hosts=%v err=%v", hosts, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("stale socket remains: %v", err)
	}
}

func TestDiscoverControlHostsReplacementSurvivesEveryClaimInterval(t *testing.T) {
	for _, stage := range []string{"discover-after-probe", "after-quarantine", "discover-before-delete"} {
		t.Run(stage, func(t *testing.T) {
			home := withSandboxedHome(t)
			dir := filepath.Join(home, ".agnt", "ssh")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "replace.ctl")
			makeStaleControlSocket(t, path)
			var replacement net.Listener
			installControlUnixHook(t, stage, path, func() {
				replacement = replaceWithLiveControl(t, path, "/replacement")
			})
			_, _ = discoverControlHosts(20*time.Millisecond, pingControl)
			defer replacement.Close()
			assertControlRoute(t, path, "/replacement")
		})
	}
}

func TestDiscoverControlHostsSurfacesQuarantineCollisionAndPreservesClaim(t *testing.T) {
	withShortSandboxedHome(t)
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "collision.ctl")
	makeStaleControlSocket(t, path)
	var replacement net.Listener
	installControlUnixHook(t, "after-quarantine", path, func() {
		replacement = replaceWithLiveControl(t, path, "/replacement")
	})
	_, err := discoverControlHosts(20*time.Millisecond, pingControl)
	if err == nil || !strings.Contains(err.Error(), "quarantined endpoint") {
		t.Fatalf("quarantine collision error = %v", err)
	}
	defer replacement.Close()
	assertControlRoute(t, path, "/replacement")
	quarantines, globErr := filepath.Glob(filepath.Join(dir, ".q*", "s"))
	if globErr != nil || len(quarantines) != 1 {
		t.Fatalf("preserved quarantine = %v, glob err=%v", quarantines, globErr)
	}
}

func TestDiscoverControlHostsSurfacesQuarantineDeleteFailure(t *testing.T) {
	withShortSandboxedHome(t)
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "delete-error.ctl")
	makeStaleControlSocket(t, path)
	installControlUnixHook(t, "discover-before-delete", path, func() {
		quarantines, _ := filepath.Glob(filepath.Join(dir, ".q*", "s"))
		for _, quarantine := range quarantines {
			_ = os.Remove(quarantine)
		}
	})
	_, err := discoverControlHosts(20*time.Millisecond, pingControl)
	if err == nil || !strings.Contains(err.Error(), "removing quarantined stale socket") {
		t.Fatalf("quarantine delete error = %v", err)
	}
}

func TestListenAndDiscoveryPreserveNonSocketControlPath(t *testing.T) {
	home := withSandboxedHome(t)
	dir := filepath.Join(home, ".agnt", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "regular.ctl")
	if err := os.WriteFile(path, []byte("owner marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if listener, err := listenControlTransport(path); err == nil {
		listener.Close()
		t.Fatal("non-socket path was replaced")
	}
	if _, err := discoverControlHosts(20*time.Millisecond, pingControl); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "owner marker" {
		t.Fatalf("non-socket path changed: content=%q err=%v", content, err)
	}
}

func TestListenAndDiscoveryPreserveInconclusiveResponders(t *testing.T) {
	responses := []struct {
		name     string
		response string
	}{
		{name: "malformed", response: "not-json\n"},
		{name: "rejected", response: "{\"ok\":false,\"error\":\"busy\"}\n"},
		{name: "timeout"},
	}
	for _, operation := range []string{"listen", "discover"} {
		for _, tc := range responses {
			t.Run(operation+"-"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				if operation == "discover" {
					withShortSandboxedHome(t)
					home := os.Getenv("HOME")
					dir = filepath.Join(home, ".agnt", "ssh")
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(dir, "inconclusive.ctl")
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				release := make(chan struct{})
				go func() {
					conn, acceptErr := listener.Accept()
					if acceptErr != nil {
						return
					}
					defer conn.Close()
					_, _ = bufio.NewReader(conn).ReadString('\n')
					if tc.response == "" {
						<-release
						return
					}
					_, _ = conn.Write([]byte(tc.response))
				}()

				if operation == "listen" {
					if replacement, err := listenControlTransport(path); err == nil {
						replacement.Close()
						t.Fatal("inconclusive responder was replaced")
					}
				} else {
					_, _ = discoverControlHosts(20*time.Millisecond, func(conn net.Conn) (controlResponse, error) {
						_ = conn.SetDeadline(time.Now().Add(20 * time.Millisecond))
						if _, err := conn.Write([]byte("{\"kind\":\"ping\"}\n")); err != nil {
							return controlResponse{}, err
						}
						line, err := bufio.NewReader(conn).ReadBytes('\n')
						if err != nil {
							return controlResponse{}, err
						}
						var response controlResponse
						if err := json.Unmarshal(line, &response); err != nil {
							return controlResponse{}, err
						}
						if !response.OK {
							return controlResponse{}, errors.New("rejected")
						}
						return response, nil
					})
				}
				close(release)
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("inconclusive responder path removed: %v", err)
				}
			})
		}
	}
}

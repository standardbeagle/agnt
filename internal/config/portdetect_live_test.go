//go:build linux || darwin

package config

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperPortClient is not a test: re-executed by
// TestFindPIDsByPort_ListenerNotClient, it connects to the port named in
// AGNT_PORT_CLIENT, reports that it is connected, and holds the connection
// until its stdin closes.
func TestHelperPortClient(t *testing.T) {
	port := os.Getenv("AGNT_PORT_CLIENT")
	if port == "" {
		t.Skip("helper process for TestFindPIDsByPort_ListenerNotClient")
	}
	conn, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(3)
	}
	defer conn.Close()
	os.Stdout.WriteString("connected\n")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// A port-owner lookup feeds port-conflict reports and port kills, so it must
// name the process LISTENING on the port and never a process that merely has
// a connection to it. On macOS the lookup was `lsof -ti :PORT`, which matches
// both; a browser connected to a dev server was reported as the port's owner.
func TestFindPIDsByPort_ListenerNotClient(t *testing.T) {
	// No t.Parallel: starts a real process.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	accepted := make(chan net.Conn, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- c
		}
	}()

	client := exec.Command(os.Args[0], "-test.run=^TestHelperPortClient$")
	client.Env = append(os.Environ(), "AGNT_PORT_CLIENT="+strconv.Itoa(port))
	stdin, err := client.StdinPipe()
	require.NoError(t, err)
	stdout, err := client.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, client.Start())
	defer func() {
		_ = stdin.Close()
		_ = client.Wait()
	}()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err, "client did not connect")
	require.Equal(t, "connected\n", line)
	select {
	case c := <-accepted:
		defer c.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("listener never accepted the client")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pids := FindPIDsByPort(ctx, port)

	assert.Contains(t, pids, os.Getpid(), "the listening process owns the port")
	assert.NotContains(t, pids, client.Process.Pid, "a process connected to the port is not its owner")
}

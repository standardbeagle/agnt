package sshclient

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/require"
)

func TestNotifyFileArrived_UsesTypedDeveloperEventProtocol(t *testing.T) {
	socket := filepath.Join(shortSocketDir(t), "daemon.sock")
	ln, err := net.Listen("unix", socket)
	require.NoError(t, err)
	defer ln.Close()

	got := make(chan *hubproto.Command, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		registry := hubproto.NewVerbRegistry()
		registry.RegisterVerb(protocol.VerbPorts)
		registry.RegisterSubVerbForVerb(protocol.VerbPorts, protocol.SubVerbDeveloperEvent)
		cmd, parseErr := hubproto.NewParserWithRegistry(conn, registry).ParseCommand()
		if parseErr == nil {
			got <- cmd
			_ = hubproto.NewWriter(conn).WriteOK("delivered")
		}
	}()

	root := filepath.Join(t.TempDir(), "project")
	remote := filepath.Join(root, ".agnt-inbox", "mock.png")
	require.NoError(t, NotifyFileArrived(socket, "agent-one", root, remote, 24*1024))

	cmd := <-got
	require.Equal(t, protocol.VerbPorts, cmd.Verb)
	require.Equal(t, protocol.SubVerbDeveloperEvent, cmd.SubVerb)
	var payload protocol.DeveloperEvent
	require.NoError(t, json.Unmarshal(cmd.Data, &payload))
	require.Equal(t, "file_arrived", payload.Kind)
	require.Equal(t, root, payload.ProjectPath)
	require.Equal(t, "[agnt] file arrived: .agnt-inbox/mock.png (24KB)", payload.Message)
}

func TestNotifyFileArrived_RejectsPathOutsideProject(t *testing.T) {
	err := NotifyFileArrived("unused", "agent", "/project", "/other/file", 1)
	require.ErrorContains(t, err, "outside project")
}

func TestNotifyFileArrived_HungDaemonTimesOut(t *testing.T) {
	old := fileArrivalNoticeTimeout
	fileArrivalNoticeTimeout = 100 * time.Millisecond
	defer func() { fileArrivalNoticeTimeout = old }()

	socket := filepath.Join(shortSocketDir(t), "daemon.sock")
	ln, err := net.Listen("unix", socket)
	require.NoError(t, err)
	defer ln.Close()
	hold := make(chan struct{})
	defer close(hold)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		<-hold // accept, then never answer
	}()

	root := filepath.Join(t.TempDir(), "project")
	remote := filepath.Join(root, ".agnt-inbox", "mock.png")
	done := make(chan error, 1)
	go func() { done <- NotifyFileArrived(socket, "s", root, remote, 1) }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("NotifyFileArrived blocked on a hung daemon")
	}
}

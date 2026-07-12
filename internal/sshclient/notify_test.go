package sshclient

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/require"
)

func TestNotifyFileArrived_UsesSessionHostNoticeProtocol(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
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
		cmd, parseErr := hubproto.NewParser(conn).ParseCommand()
		if parseErr == nil {
			got <- cmd
			_ = hubproto.NewWriter(conn).WriteOK("delivered")
		}
	}()

	root := filepath.Join(t.TempDir(), "project")
	remote := filepath.Join(root, ".agnt-inbox", "mock.png")
	require.NoError(t, NotifyFileArrived(socket, "agent-one", root, remote, 24*1024))

	cmd := <-got
	require.Equal(t, protocol.VerbSessionHost, cmd.Verb)
	// The generic parser has no custom SESSION-HOST subverb registry, so it
	// preserves NOTICE as the first positional token. The daemon's registered
	// parser promotes the same token to SubVerb before dispatch.
	require.Equal(t, []string{"NOTICE"}, cmd.Args)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(cmd.Data, &payload))
	require.Equal(t, "agent-one", payload["session_name"])
	require.Equal(t, root, payload["project_path"])
	require.Equal(t, "[agnt] file arrived: .agnt-inbox/mock.png (24KB)", payload["message"])
}

func TestNotifyFileArrived_RejectsPathOutsideProject(t *testing.T) {
	err := NotifyFileArrived("unused", "agent", "/project", "/other/file", 1)
	require.ErrorContains(t, err, "outside project")
}

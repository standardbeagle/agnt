package sshclient

import (
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// FileArrivalNotifier runs after an upload has been atomically installed.
// An error is returned to the push caller so notification loss is never silent.
type FileArrivalNotifier func(remotePath string, size int64) error

// NotifyFileArrived sends actionable file context to the remote daemon's agent
// surface through SESSION-HOST NOTICE. The daemon owns final PTY delivery.
func NotifyFileArrived(daemonSocket, sessionName, projectRoot, remotePath string, size int64) error {
	rel, err := filepath.Rel(projectRoot, remotePath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sshclient: file-arrival path %q is outside project %q", remotePath, projectRoot)
	}
	payload, err := json.Marshal(map[string]string{
		"session_name": sessionName,
		"project_path": projectRoot,
		"message":      fmt.Sprintf("[agnt] file arrived: %s (%s)", filepath.ToSlash(rel), formatNoticeSize(size)),
	})
	if err != nil {
		return fmt.Errorf("sshclient: encoding file-arrival notice: %w", err)
	}

	conn, err := net.Dial("unix", daemonSocket)
	if err != nil {
		return fmt.Errorf("sshclient: connecting to daemon for file-arrival notice: %w", err)
	}
	defer conn.Close()
	if err := hubproto.NewWriter(conn).WriteCommandWithSubVerb(protocol.VerbSessionHost, "NOTICE", nil, payload); err != nil {
		return fmt.Errorf("sshclient: sending file-arrival notice: %w", err)
	}
	resp, err := hubproto.NewParser(conn).ParseResponse()
	if err != nil {
		return fmt.Errorf("sshclient: reading file-arrival notice response: %w", err)
	}
	if resp.Type != hubproto.ResponseOK {
		return fmt.Errorf("sshclient: file-arrival notice rejected: [%s] %s", resp.Code, resp.Message)
	}
	return nil
}

func formatNoticeSize(size int64) string {
	const kb = int64(1024)
	const mb = 1024 * kb
	switch {
	case size >= mb && size%mb == 0:
		return fmt.Sprintf("%dMB", size/mb)
	case size >= kb && size%kb == 0:
		return fmt.Sprintf("%dKB", size/kb)
	default:
		return fmt.Sprintf("%dB", size)
	}
}

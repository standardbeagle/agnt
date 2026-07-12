package testenv_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/sessionhost"
	"github.com/stretchr/testify/require"
)

func TestScrollbackReplayIsByteExactANSI(t *testing.T) {
	source := []byte("\x1b[2J\x1b[H\x1b[31mred\x1b[0m\n\x00\xff")
	want := []byte("\x1b[2J\x1b[H\x1b[31mred\x1b[0m\r\n\x00\xff")
	encoded := base64.StdEncoding.EncodeToString(source)
	s, err := sessionhost.Create(sessionhost.CreateConfig{
		Name: "ansi-replay", ProjectPath: t.TempDir(), Command: "/bin/sh",
		Args: []string{"-c", "printf '" + encoded + "' | base64 -d; sleep 30"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { killSession(t, s) })

	var gotFrame []byte
	var gotOK bool
	require.Eventually(t, func() bool {
		ch, id, _ := s.Attach(4)
		defer s.Detach(id)

		select {
		case <-ch: // replay marker is always the first buffered frame
		case <-time.After(200 * time.Millisecond):
			return false
		}

		select {
		case raw := <-ch:
			frame, err := decodeStdoutFrame(raw)
			if err != nil {
				return false
			}
			gotFrame = frame
			gotOK = string(frame) == string(want)
			return gotOK
		case <-time.After(200 * time.Millisecond):
			return false
		}
	}, 3*time.Second, 10*time.Millisecond)
	require.True(t, gotOK, "expected byte-exact ANSI scrollback replay")
	require.Equal(t, want, gotFrame)

	ch, id, primary := s.Attach(4)
	defer s.Detach(id)
	markerRaw := <-ch
	var marker sessionhost.Frame
	require.NoError(t, json.Unmarshal(markerRaw, &marker))
	require.Equal(t, "replay-marker", marker.Type)
	var markerData sessionhost.ReplayMarkerData
	require.NoError(t, json.Unmarshal(marker.Data, &markerData))
	require.False(t, markerData.Truncated)
	require.True(t, primary)
	require.Equal(t, want, readStdoutFrame(t, <-ch))
}

// decodeStdoutFrame is the require-free counterpart to readStdoutFrame, safe
// to call from require.Eventually's condition closure: require.FailNow (via
// require.NoError/require.Equal) must only run on the test's own goroutine,
// but Eventually invokes the condition on a goroutine it spawns internally.
func decodeStdoutFrame(raw []byte) ([]byte, error) {
	var frame sessionhost.Frame
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, err
	}
	if frame.Type != "stdout" {
		return nil, fmt.Errorf("unexpected frame type %q", frame.Type)
	}
	var encoded string
	if err := json.Unmarshal(frame.Data, &encoded); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(encoded)
}

func readStdoutFrame(t *testing.T, raw []byte) []byte {
	t.Helper()
	var frame sessionhost.Frame
	require.NoError(t, json.Unmarshal(raw, &frame))
	require.Equal(t, "stdout", frame.Type)
	var encoded string
	require.NoError(t, json.Unmarshal(frame.Data, &encoded))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	return decoded
}

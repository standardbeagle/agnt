package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	hubclient "github.com/standardbeagle/go-cli-server/client"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/require"
)

// TestHubProgressKeepsSilentRequestAlive reproduces the production failure in
// which a healthy, long-running daemon handler emitted no bytes before the
// client's I/O deadline. STATUS frames are transport liveness, not the final
// response, so the request must remain pending until its JSON result arrives.
func TestHubProgressKeepsSilentRequestAlive(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hub.sock")
	h := hubpkg.New(hubpkg.Config{
		SocketPath:     sock,
		MaxClients:     4,
		WriteTimeout:   time.Second,
		StatusInterval: 10 * time.Millisecond,
	})
	require.NoError(t, h.RegisterCommand(hubpkg.CommandDefinition{
		Verb: "SLOW",
		Handler: func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
			time.Sleep(65 * time.Millisecond)
			return conn.WriteJSON([]byte(`{"result":"done"}`))
		},
	}))
	require.NoError(t, h.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, h.Stop(ctx))
	})

	c := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(25*time.Millisecond))
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Request("SLOW").JSON()
	require.NoError(t, err)
	require.Equal(t, "done", got["result"])
}

// TestHubProgressDoesNotBlockOtherClients pins the daemon-wide responsiveness
// invariant: one long request may occupy its own connection, but STATUS writes
// and handler execution must not delay an independent control-plane request.
func TestHubProgressDoesNotBlockOtherClients(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hub.sock")
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	h := hubpkg.New(hubpkg.Config{
		SocketPath:     sock,
		MaxClients:     4,
		WriteTimeout:   time.Second,
		StatusInterval: 10 * time.Millisecond,
	})
	require.NoError(t, h.RegisterCommand(hubpkg.CommandDefinition{
		Verb: "BLOCKED",
		Handler: func(ctx context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return conn.WriteOK("released")
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}))
	require.NoError(t, h.Start())
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, h.Stop(ctx))
	})

	slow := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(25*time.Millisecond))
	fast := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(250*time.Millisecond))
	t.Cleanup(func() { _ = slow.Close() })
	t.Cleanup(func() { _ = fast.Close() })

	slowDone := make(chan error, 1)
	go func() {
		err := slow.Request("BLOCKED").OK()
		slowDone <- err
	}()
	<-started

	_, err := fast.Request(hubproto.VerbInfo).JSON()
	require.NoError(t, err)

	close(release)
	require.NoError(t, <-slowDone)
}

// TestHubProgressDoesNotContaminateChunkedPayload ensures STATUS remains
// out-of-band even when it is interleaved with application CHUNK frames.
func TestHubProgressDoesNotContaminateChunkedPayload(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hub.sock")
	h := hubpkg.New(hubpkg.Config{
		SocketPath:     sock,
		MaxClients:     4,
		WriteTimeout:   time.Second,
		StatusInterval: 10 * time.Millisecond,
	})
	require.NoError(t, h.RegisterCommand(hubpkg.CommandDefinition{
		Verb: "STREAM",
		Handler: func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
			if err := conn.WriteChunk([]byte("a")); err != nil {
				return err
			}
			time.Sleep(45 * time.Millisecond)
			if err := conn.WriteChunk([]byte("b")); err != nil {
				return err
			}
			return conn.WriteEnd()
		},
	}))
	require.NoError(t, h.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, h.Stop(ctx))
	})

	c := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(25*time.Millisecond))
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Request("STREAM").Chunked()
	require.NoError(t, err)
	require.Equal(t, []byte("ab"), got)
}

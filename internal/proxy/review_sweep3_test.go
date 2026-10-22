package proxy

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A connection remembers at most maxConnCaptures capture ids, however many
// frames a page sends and never names in a panel_message.
func TestRememberCapture_BoundedPerConnection(t *testing.T) {
	c := &wsConnState{captures: make(map[string]string)}
	for i := 0; i < maxConnCaptures*3; i++ {
		c.rememberCapture("id-"+strconv.Itoa(i), "/tmp/f"+strconv.Itoa(i))
	}
	assert.LessOrEqual(t, len(c.captures), maxConnCaptures)
	assert.Equal(t, "/tmp/f"+strconv.Itoa(maxConnCaptures*3-1), c.captures["id-"+strconv.Itoa(maxConnCaptures*3-1)],
		"the newest capture is kept")

	// Replacing an id at the cap evicts nothing.
	before := len(c.captures)
	for id := range c.captures {
		c.rememberCapture(id, "/tmp/replaced")
		break
	}
	assert.Equal(t, before, len(c.captures))
}

// An overlay that answers an event with an error status did not take it: the
// notifier reports that, not "delivered".
func TestOverlayNotifier_NonSuccessStatusIsAnError(t *testing.T) {
	ln, sock := listenUnix(t)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	n := NewOverlayNotifier()
	n.SetEndpoint(sock)
	err := n.NotifyPanelMessage("p", &PanelMessage{Message: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")

	ok := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	ln2, sock2 := listenUnix(t)
	go func() { _ = ok.Serve(ln2) }()
	t.Cleanup(func() { _ = ok.Close() })
	n.SetEndpoint(sock2)
	assert.NoError(t, n.NotifyPanelMessage("p", &PanelMessage{Message: "hi"}))
}

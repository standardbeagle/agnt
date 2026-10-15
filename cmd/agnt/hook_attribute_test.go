package main

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAttributeHookProject pins how a hook event gets its project_path: the
// daemon drops toasts and tool events without one, so a hook line wired
// without --project-path must fall back to the payload's cwd. The daemon
// round trips live in hook_test.go (unix-only harness).
func TestAttributeHookProject(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "proj")
	payload := []byte(`{"session_id":"s","cwd":` + strconv.Quote(cwd) + `,"hook_event_name":"Stop"}`)

	got := attributeHookProject(nil, payload)
	assert.Equal(t, cwd, got["project_path"], "payload cwd attributes an unattributed event")

	got = attributeHookProject(map[string]string{"session_id": "s"}, payload)
	assert.Equal(t, cwd, got["project_path"])
	assert.Equal(t, "s", got["session_id"], "existing tags survive")

	got = attributeHookProject(map[string]string{"project_path": "/explicit"}, payload)
	assert.Equal(t, "/explicit", got["project_path"], "explicit --project-path wins over payload cwd")

	assert.Nil(t, attributeHookProject(nil, nil), "no payload leaves tags alone")
	assert.Nil(t, attributeHookProject(nil, []byte(`not json`)), "malformed payload leaves tags alone")
	assert.Nil(t, attributeHookProject(nil, []byte(`{"cwd":""}`)), "empty cwd leaves tags alone")
}

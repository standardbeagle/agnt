package daemon

import (
	"context"
	"encoding/json"

	"github.com/standardbeagle/agnt/internal/store"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) storeActions() map[string]handlerFn {
	return map[string]handlerFn{
		"GET":     noCtx(d.hubHandleStoreGet),
		"SET":     noCtx(d.hubHandleStoreSet),
		"DELETE":  noCtx(d.hubHandleStoreDelete),
		"LIST":    noCtx(d.hubHandleStoreList),
		"CLEAR":   noCtx(d.hubHandleStoreClear),
		"GET-ALL": noCtx(d.hubHandleStoreGetAll),
	}
}

func (d *Daemon) hubHandleStore(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	actions := d.storeActions()
	return newCommandRouter("STORE").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown STORE sub-command",
				Command:      "STORE",
				ValidActions: routerSubVerbs(actions),
			})
		}).
		dispatch(ctx, conn, cmd, actions)
}

// hubHandleStoreGet handles STORE GET command.

func (d *Daemon) hubHandleStoreGet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[struct {
		Scope    string `json:"scope"`
		ScopeKey string `json:"scope_key"`
		Key      string `json:"key"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}

	if req.Scope == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "scope is required")
	}
	if req.Key == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "key is required")
	}

	// Get project path from session
	basePath := d.getSessionProjectPath(conn)
	if basePath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}

	entry, err := d.storem.Get(basePath, req.Scope, req.ScopeKey, req.Key)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Secret entries are write-only on this read surface: expose the masked
	// ref (name + last-4 fingerprint), never the value.
	data, _ := json.Marshal(store.MaskedForRead(req.Key, entry))
	return conn.WriteJSON(data)
}

// hubHandleStoreSet handles STORE SET command.

func (d *Daemon) hubHandleStoreSet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[struct {
		Scope    string         `json:"scope"`
		ScopeKey string         `json:"scope_key"`
		Key      string         `json:"key"`
		Value    interface{}    `json:"value"`
		Metadata map[string]any `json:"metadata,omitempty"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}

	if req.Scope == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "scope is required")
	}
	if req.Key == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "key is required")
	}

	// Get project path from session
	basePath := d.getSessionProjectPath(conn)
	if basePath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}

	if err := d.storem.Set(basePath, req.Scope, req.ScopeKey, req.Key, req.Value, req.Metadata); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK("value stored")
}

// hubHandleStoreDelete handles STORE DELETE command.

func (d *Daemon) hubHandleStoreDelete(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[struct {
		Scope    string `json:"scope"`
		ScopeKey string `json:"scope_key"`
		Key      string `json:"key"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}

	if req.Scope == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "scope is required")
	}
	if req.Key == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "key is required")
	}

	// Get project path from session
	basePath := d.getSessionProjectPath(conn)
	if basePath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}

	if err := d.storem.Delete(basePath, req.Scope, req.ScopeKey, req.Key); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK("key deleted")
}

// hubHandleStoreList handles STORE LIST command.

func (d *Daemon) hubHandleStoreList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[struct {
		Scope    string `json:"scope"`
		ScopeKey string `json:"scope_key"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}

	if req.Scope == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "scope is required")
	}

	// Get project path from session
	basePath := d.getSessionProjectPath(conn)
	if basePath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}

	keys, err := d.storem.List(basePath, req.Scope, req.ScopeKey)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"keys": keys,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleStoreClear handles STORE CLEAR command.

func (d *Daemon) hubHandleStoreClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[struct {
		Scope    string `json:"scope"`
		ScopeKey string `json:"scope_key"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}

	if req.Scope == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "scope is required")
	}

	// Get project path from session
	basePath := d.getSessionProjectPath(conn)
	if basePath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}

	if err := d.storem.Clear(basePath, req.Scope, req.ScopeKey); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK("scope cleared")
}

// hubHandleStoreGetAll handles STORE GET-ALL command.

func (d *Daemon) hubHandleStoreGetAll(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[struct {
		Scope    string `json:"scope"`
		ScopeKey string `json:"scope_key"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}

	if req.Scope == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "scope is required")
	}

	// Get project path from session
	basePath := d.getSessionProjectPath(conn)
	if basePath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}

	entries, err := d.storem.GetAll(basePath, req.Scope, req.ScopeKey)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Mask secret entries on the bulk read surface too (never the value).
	masked := make(map[string]*store.StoreEntry, len(entries))
	for key, e := range entries {
		masked[key] = store.MaskedForRead(key, e)
	}

	resp := map[string]interface{}{
		"entries": masked,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleDoctor handles the DOCTOR command.
// Runs all health checks and returns a structured diagnostic report.

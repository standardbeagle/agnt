package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/store"
)

// wireSessionClient gives a proxy's browser session/store API
// (scripts/session.js, scripts/store.js → "session_request"/"store_request")
// its backend: an in-process client bound to the proxy's own project.
//
// In-process, not a loopback hub client. The proxy runs inside the daemon, so
// dialing the daemon's own socket would add a connection per browser request
// and — worse — a hub connection has no session bound to it, so every scoped
// verb would have to be told a scope by its caller, which here is the browser.
// This client instead calls the same registry, scheduler and store the hub's
// SESSION and STORE verbs call, with the project fixed at wiring time.
//
// Called from wireProxyLogger, the chokepoint every proxy-creation path
// (explicit start, URL detection, fallback, restart, restore) goes through.
func (d *Daemon) wireSessionClient(server *proxy.ProxyServer) {
	if server == nil || server.Path == "" {
		// A proxy with no project has nothing to be scoped to; its browser
		// API stays unavailable rather than becoming unscoped.
		return
	}
	projectPath := server.Path
	server.SetSessionClientFactory(func() (proxy.SessionClient, error) {
		return &projectSessionClient{d: d, projectPath: projectPath}, nil
	})
}

// projectSessionClient is the proxy.SessionClient for one project. Every
// method acts on that project only; nothing a caller passes can widen it.
//
// Sessions: the project is the one the proxy's owning session runs in,
// resolved the way overlayEndpointForProject resolves it (the most specific
// active session at or above the proxy's path). With no such session every
// session operation fails closed. A session code or task id that belongs to
// another project is refused, never looked up "anyway".
//
// Store: the base path is the proxy's own path, the same one wireSecretSink
// writes to. scope/scope_key select a namespace inside that project's store
// (the store hashes scope_key into a file name), so they cannot reach another
// project's data.
type projectSessionClient struct {
	d           *Daemon
	projectPath string
}

var (
	errBrowserGlobalScope = errors.New("global scope is not available to a proxy's browser API")
	errNoProjectSession   = errors.New("no agnt session is running for this proxy's project")
)

// Close implements proxy.SessionClient. There is no connection to release.
func (c *projectSessionClient) Close() error { return nil }

// ownerProject returns the project path of the session that owns this proxy.
func (c *projectSessionClient) ownerProject() (string, error) {
	if c.d.sessionRegistry == nil {
		return "", errNoProjectSession
	}
	owner, ok := c.d.sessionRegistry.FindByDirectory(c.projectPath)
	if !ok {
		return "", errNoProjectSession
	}
	return owner.ProjectPath, nil
}

// checkFilter refuses a filter that names any scope but this project. The
// proxy's handlers never send one; this is the second lock on the same door.
func (c *projectSessionClient) checkFilter(filter protocol.DirectoryFilter) error {
	if filter.Global || (filter.GlobalOverride != nil && *filter.GlobalOverride) {
		return errBrowserGlobalScope
	}
	if filter.SessionCode != "" {
		return fmt.Errorf("a session-code scope is not available to a proxy's browser API")
	}
	if filter.Directory != "" && normalizePath(filter.Directory) != normalizePath(c.projectPath) {
		return fmt.Errorf("directory %q is not this proxy's project", filter.Directory)
	}
	return nil
}

// projectSession returns the session with this code if it belongs to the
// proxy's project. A code from another project gets the same answer as one
// that does not exist, so the API is not an oracle for other projects' codes.
func (c *projectSessionClient) projectSession(code string) (*Session, error) {
	project, err := c.ownerProject()
	if err != nil {
		return nil, err
	}
	session, ok := c.d.sessionRegistry.Get(code)
	if !ok || normalizePath(session.ProjectPath) != normalizePath(project) {
		return nil, fmt.Errorf("session %q not found in this proxy's project", code)
	}
	return session, nil
}

// browserSessionView is what a page may see of a session: enough to pick one
// and address it. The hub's ToJSON also carries the overlay socket path, PIDs
// and a job handle, which a browser has no use for.
func browserSessionView(s *Session) map[string]interface{} {
	full := s.ToJSON()
	return map[string]interface{}{
		"code":         full["code"],
		"project_path": full["project_path"],
		"command":      full["command"],
		"status":       full["status"],
		"started_at":   full["started_at"],
		"kind":         full["kind"],
	}
}

func (c *projectSessionClient) SessionList(filter protocol.DirectoryFilter) (map[string]interface{}, error) {
	if err := c.checkFilter(filter); err != nil {
		return nil, err
	}
	project, err := c.ownerProject()
	if err != nil {
		return nil, err
	}
	sessions := c.d.sessionRegistry.List(project, false)
	list := make([]map[string]interface{}, 0, len(sessions))
	for _, s := range sessions {
		list = append(list, browserSessionView(s))
	}
	return map[string]interface{}{
		"sessions":  list,
		"count":     len(list),
		"directory": project,
		"global":    false,
	}, nil
}

func (c *projectSessionClient) SessionGet(code string) (map[string]interface{}, error) {
	session, err := c.projectSession(code)
	if err != nil {
		return nil, err
	}
	return browserSessionView(session), nil
}

func (c *projectSessionClient) SessionSend(code, message string) (map[string]interface{}, error) {
	session, err := c.projectSession(code)
	if err != nil {
		return nil, err
	}
	if session.GetStatus() != SessionStatusActive {
		return nil, fmt.Errorf("session %q is not active", code)
	}
	if err := c.d.sendMessageToOverlay(session.OverlayPath, message); err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}
	return map[string]interface{}{
		"success":      true,
		"session_code": code,
		"message_len":  len(message),
	}, nil
}

func (c *projectSessionClient) SessionSchedule(code, durationStr, message string) (map[string]interface{}, error) {
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		return nil, fmt.Errorf("invalid duration %q: %w", durationStr, err)
	}
	session, err := c.projectSession(code)
	if err != nil {
		return nil, err
	}
	if c.d.scheduler == nil {
		return nil, errors.New("scheduler is not available")
	}
	task, err := c.d.scheduler.Schedule(code, duration, message, session.ProjectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to schedule: %w", err)
	}
	return map[string]interface{}{
		"task_id":      task.ID,
		"session_code": code,
		"deliver_at":   task.GetDeliverAt().Format(time.RFC3339),
		"message_len":  len(message),
	}, nil
}

func (c *projectSessionClient) SessionTasks(filter protocol.DirectoryFilter) (map[string]interface{}, error) {
	if err := c.checkFilter(filter); err != nil {
		return nil, err
	}
	project, err := c.ownerProject()
	if err != nil {
		return nil, err
	}
	list := make([]map[string]interface{}, 0)
	if c.d.scheduler != nil {
		// project is never empty here (ownerProject returned a session's
		// path), so ListTasks cannot fall into its "no filter" branch.
		for _, t := range c.d.scheduler.ListTasks(project, false) {
			list = append(list, t.ToJSON())
		}
	}
	return map[string]interface{}{
		"tasks":     list,
		"count":     len(list),
		"directory": project,
		"global":    false,
	}, nil
}

func (c *projectSessionClient) SessionCancel(taskID string) error {
	project, err := c.ownerProject()
	if err != nil {
		return err
	}
	if c.d.scheduler == nil {
		return errors.New("scheduler is not available")
	}
	task, ok := c.d.scheduler.GetTask(taskID)
	if !ok || normalizePath(task.ProjectPath) != normalizePath(project) {
		return fmt.Errorf("task %q not found in this proxy's project", taskID)
	}
	return c.d.scheduler.Cancel(taskID)
}

// storeAvailable guards every store method.
func (c *projectSessionClient) storeAvailable() error {
	if c.d.storem == nil {
		return errors.New("store is not available")
	}
	return nil
}

// errSecretEntry is the refusal for a browser write that would touch a secret.
// Secret entries are written only by the secret-entry path (wireSecretSink)
// and consumed only by env injection at spawn; this surface may see their
// masked refs and nothing more.
func errSecretEntry(key string) error {
	return fmt.Errorf("%q is a secret entry: it cannot be written, deleted or cleared through the browser store API", key)
}

func (c *projectSessionClient) StoreGet(req protocol.StoreGetRequest) (map[string]interface{}, error) {
	if err := c.storeAvailable(); err != nil {
		return nil, err
	}
	entry, err := c.d.storem.Get(c.projectPath, req.Scope, req.ScopeKey, req.Key)
	if err != nil {
		return nil, err
	}
	// Same masking as STORE GET: a secret reads as its ref, never its value.
	return jsonObject(store.MaskedForRead(req.Key, entry))
}

func (c *projectSessionClient) StoreSet(req protocol.StoreSetRequest) error {
	if err := c.storeAvailable(); err != nil {
		return err
	}
	// A page must not be able to mint a secret (which env injection would
	// then hand to every managed process) nor replace one that exists.
	for _, reserved := range []string{store.MetaSecret, store.MetaFingerprint} {
		if _, ok := req.Metadata[reserved]; ok {
			return fmt.Errorf("metadata key %q is reserved for secret entries and cannot be set through the browser store API", reserved)
		}
	}
	if existing, err := c.d.storem.Get(c.projectPath, req.Scope, req.ScopeKey, req.Key); err == nil && store.IsSecretEntry(existing) {
		return errSecretEntry(req.Key)
	}
	return c.d.storem.Set(c.projectPath, req.Scope, req.ScopeKey, req.Key, req.Value, req.Metadata)
}

func (c *projectSessionClient) StoreDelete(req protocol.StoreDeleteRequest) error {
	if err := c.storeAvailable(); err != nil {
		return err
	}
	if existing, err := c.d.storem.Get(c.projectPath, req.Scope, req.ScopeKey, req.Key); err == nil && store.IsSecretEntry(existing) {
		return errSecretEntry(req.Key)
	}
	return c.d.storem.Delete(c.projectPath, req.Scope, req.ScopeKey, req.Key)
}

func (c *projectSessionClient) StoreList(req protocol.StoreListRequest) (map[string]interface{}, error) {
	if err := c.storeAvailable(); err != nil {
		return nil, err
	}
	keys, err := c.d.storem.List(c.projectPath, req.Scope, req.ScopeKey)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"keys": keys}, nil
}

func (c *projectSessionClient) StoreClear(req protocol.StoreClearRequest) error {
	if err := c.storeAvailable(); err != nil {
		return err
	}
	// Clear removes the whole scope file, secrets included. Refuse while it
	// holds one, rather than silently keeping some keys of a "clear".
	entries, err := c.d.storem.GetAll(c.projectPath, req.Scope, req.ScopeKey)
	if err != nil {
		return err
	}
	for key, e := range entries {
		if store.IsSecretEntry(e) {
			return fmt.Errorf("this scope holds secret entries (%q): it cannot be cleared through the browser store API; delete the other keys individually", key)
		}
	}
	return c.d.storem.Clear(c.projectPath, req.Scope, req.ScopeKey)
}

func (c *projectSessionClient) StoreGetAll(req protocol.StoreGetAllRequest) (map[string]interface{}, error) {
	if err := c.storeAvailable(); err != nil {
		return nil, err
	}
	entries, err := c.d.storem.GetAll(c.projectPath, req.Scope, req.ScopeKey)
	if err != nil {
		return nil, err
	}
	masked := make(map[string]*store.StoreEntry, len(entries))
	for key, e := range entries {
		masked[key] = store.MaskedForRead(key, e)
	}
	return map[string]interface{}{"entries": masked}, nil
}

// jsonObject renders v as the generic object the SessionClient interface
// returns, through its JSON form so the browser sees the wire field names.
func jsonObject(v interface{}) (map[string]interface{}, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

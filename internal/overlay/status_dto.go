package overlay

import (
	"encoding/json"
	"time"
)

// This file replaces the hand-rolled map[string]interface{} walking that used
// to live in every StatusFetcher.fetch* method. Each daemon response is decoded
// into a typed DTO whose json tags match the daemon's wire keys, then converted
// to the overlay display struct. Adding a field is now: add it to the DTO + one
// assignment, instead of a brittle `m["x"].(string)` access in two places.
//
// decodeResult re-marshals the already-parsed map and unmarshals it into out.
// RequestJSON hands back a map[string]interface{}; round-tripping it through
// json is cheap for these small status payloads and lets the type system handle
// coercion and missing fields instead of per-field comma-ok assertions.
func decodeResult(result map[string]interface{}, out interface{}) bool {
	b, err := json.Marshal(result)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, out) == nil
}

// parseRFC3339 returns the parsed time or the zero time on any error.
func parseRFC3339(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// --- Script ---

type scriptDTO struct {
	Name       string `json:"name"`
	ProcessID  string `json:"process_id"`
	State      string `json:"state"`
	Command    string `json:"command"`
	StartCount int64  `json:"start_count"`
	FailCount  int64  `json:"fail_count"`
	LastError  string `json:"last_error"`
	HasAlerts  bool   `json:"has_alerts"`
}

func (d scriptDTO) toInfo() ScriptInfo {
	return ScriptInfo{
		Name:       d.Name,
		ProcessID:  d.ProcessID,
		State:      d.State,
		Command:    d.Command,
		StartCount: d.StartCount,
		FailCount:  d.FailCount,
		LastError:  d.LastError,
		HasAlerts:  d.HasAlerts,
	}
}

// --- Process ---

type processDTO struct {
	ID        string   `json:"id"`
	Command   string   `json:"command"`
	State     string   `json:"state"`
	RuntimeMS int64    `json:"runtime_ms"`
	URLs      []string `json:"urls"`
}

func (d processDTO) toInfo() ProcessInfo {
	return ProcessInfo{
		ID:      d.ID,
		Command: d.Command,
		State:   d.State,
		Runtime: time.Duration(d.RuntimeMS) * time.Millisecond,
		URLs:    d.URLs,
	}
}

// --- Proxy ---

type proxyDTO struct {
	ID            string   `json:"id"`
	TargetURL     string   `json:"target_url"`
	ListenAddr    string   `json:"listen_addr"`
	Status        string   `json:"status"`
	WaitingFor    []string `json:"waiting_for"`
	TunnelURL     string   `json:"tunnel_url"`
	TunnelRunning bool     `json:"tunnel_running"`
	Uptime        string   `json:"uptime"`
	TotalRequests int64    `json:"total_requests"`
	Stats         struct {
		ErrorCount int `json:"error_count"`
	} `json:"stats"`
}

func (d proxyDTO) toInfo() ProxyInfo {
	return ProxyInfo{
		ID:            d.ID,
		TargetURL:     d.TargetURL,
		ListenAddr:    d.ListenAddr,
		State:         d.Status,
		WaitingOn:     d.WaitingFor,
		ErrorCount:    d.Stats.ErrorCount,
		HasErrors:     d.Stats.ErrorCount > 0,
		TunnelURL:     d.TunnelURL,
		TunnelRunning: d.TunnelRunning,
		Uptime:        d.Uptime,
		TotalRequests: d.TotalRequests,
	}
}

// --- Ports / orphans ---

type portDTO struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Name    string `json:"name"`
	Windows bool   `json:"windows"`
	Status  string `json:"status"`
	System  bool   `json:"system"`
}

func (d portDTO) toInfo() PortInfo {
	return PortInfo{Port: d.Port, PID: d.PID, Name: d.Name, Windows: d.Windows, Status: d.Status, System: d.System}
}

type orphanDTO struct {
	PGID  int `json:"pgid"`
	Count int `json:"count"`
}

func (d orphanDTO) toInfo() OrphanInfo {
	return OrphanInfo{PGID: d.PGID, Count: d.Count}
}

// --- Startup log ---

type startupLogDTO struct {
	ScriptName string `json:"script_name"`
	Level      string `json:"level"`
	EventType  string `json:"event_type"`
	Message    string `json:"message"`
	Timestamp  string `json:"timestamp"`
}

func (d startupLogDTO) toInfo() StartupLogEntry {
	return StartupLogEntry{
		ScriptName: d.ScriptName,
		Level:      d.Level,
		EventType:  d.EventType,
		Message:    d.Message,
		Timestamp:  parseRFC3339(d.Timestamp),
	}
}

// --- Browser sessions (CURRENTPAGE LIST) ---

type browserSessionDTO struct {
	SessionID    string `json:"session_id"`
	URL          string `json:"url"`
	Interactions int    `json:"interaction_count"`
	Mutations    int    `json:"mutation_count"`
	LastActivity string `json:"last_activity"`
}

func (d browserSessionDTO) toSession(proxyID string) BrowserSession {
	return BrowserSession{
		ProxyID:      proxyID,
		SessionID:    d.SessionID,
		URL:          d.URL,
		Interactions: d.Interactions,
		Mutations:    d.Mutations,
		LastActivity: parseRFC3339(d.LastActivity),
	}
}

// --- Recent errors (PROXYLOG QUERY error entries) ---

type proxyLogEntryDTO struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Error     struct {
		Message string `json:"message"`
	} `json:"error"`
}

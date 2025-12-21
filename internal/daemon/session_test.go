package daemon

import (
	"testing"
	"time"
)

func TestSessionRegistry_Register(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Register a session
	session := &Session{
		Code:        "test-1",
		OverlayPath: "/tmp/overlay.sock",
		ProjectPath: "/home/user/project",
		Command:     "claude",
		Args:        []string{"--model", "opus"},
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}

	err := registry.Register(session)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Verify registration
	got, found := registry.Get("test-1")
	if !found {
		t.Fatal("Get() returned false, expected true")
	}
	if got.Code != session.Code {
		t.Errorf("Get() Code = %v, want %v", got.Code, session.Code)
	}
	if got.Command != session.Command {
		t.Errorf("Get() Command = %v, want %v", got.Command, session.Command)
	}
}

func TestSessionRegistry_Register_Duplicate(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	session := &Session{
		Code:      "test-1",
		Command:   "claude",
		StartedAt: time.Now(),
		Status:    SessionStatusActive,
	}

	err := registry.Register(session)
	if err != nil {
		t.Fatalf("First Register() error = %v", err)
	}

	// Try to register duplicate
	session2 := &Session{
		Code:      "test-1",
		Command:   "copilot",
		StartedAt: time.Now(),
		Status:    SessionStatusActive,
	}
	err = registry.Register(session2)
	if err == nil {
		t.Error("Second Register() should return error for duplicate code")
	}
}

func TestSessionRegistry_Register_EmptyCode(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	session := &Session{
		Code:    "",
		Command: "claude",
	}

	err := registry.Register(session)
	if err == nil {
		t.Error("Register() should return error for empty code")
	}
}

func TestSessionRegistry_Unregister(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	session := &Session{
		Code:      "test-1",
		Command:   "claude",
		StartedAt: time.Now(),
		Status:    SessionStatusActive,
	}

	err := registry.Register(session)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Unregister
	err = registry.Unregister("test-1")
	if err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}

	// Verify removal
	_, found := registry.Get("test-1")
	if found {
		t.Error("Get() returned true after Unregister, expected false")
	}
}

func TestSessionRegistry_Unregister_NotFound(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	err := registry.Unregister("nonexistent")
	if err == nil {
		t.Error("Unregister() should return error for nonexistent code")
	}
}

func TestSessionRegistry_Heartbeat(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	initialTime := time.Now().Add(-time.Minute)
	session := &Session{
		Code:      "test-1",
		Command:   "claude",
		StartedAt: initialTime,
		Status:    SessionStatusActive,
		LastSeen:  initialTime,
	}

	err := registry.Register(session)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Send heartbeat
	time.Sleep(10 * time.Millisecond) // Ensure time passes
	err = registry.Heartbeat("test-1")
	if err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}

	// Verify LastSeen updated
	got, _ := registry.Get("test-1")
	if !got.LastSeen.After(initialTime) {
		t.Error("Heartbeat() did not update LastSeen")
	}
}

func TestSessionRegistry_Heartbeat_NotFound(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	err := registry.Heartbeat("nonexistent")
	if err == nil {
		t.Error("Heartbeat() should return error for nonexistent code")
	}
}

func TestSessionRegistry_List(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Register multiple sessions
	for i, code := range []string{"test-1", "test-2", "test-3"} {
		session := &Session{
			Code:        code,
			ProjectPath: "/project" + string(rune('1'+i)),
			Command:     "claude",
			StartedAt:   time.Now(),
			Status:      SessionStatusActive,
		}
		_ = registry.Register(session)
	}

	sessions := registry.List("", true) // global list
	if len(sessions) != 3 {
		t.Errorf("List() returned %d sessions, want 3", len(sessions))
	}
}

func TestSessionRegistry_ListByProject(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Register sessions in different projects
	session1 := &Session{
		Code:        "test-1",
		ProjectPath: "/home/user/project-a",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
	}
	session2 := &Session{
		Code:        "test-2",
		ProjectPath: "/home/user/project-a",
		Command:     "copilot",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
	}
	session3 := &Session{
		Code:        "test-3",
		ProjectPath: "/home/user/project-b",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
	}

	_ = registry.Register(session1)
	_ = registry.Register(session2)
	_ = registry.Register(session3)

	// List sessions in project-a
	sessions := registry.List("/home/user/project-a", false)
	if len(sessions) != 2 {
		t.Errorf("List() for project-a returned %d sessions, want 2", len(sessions))
	}

	// List sessions in project-b
	sessions = registry.List("/home/user/project-b", false)
	if len(sessions) != 1 {
		t.Errorf("List() for project-b returned %d sessions, want 1", len(sessions))
	}

	// List sessions in nonexistent project
	sessions = registry.List("/home/user/project-c", false)
	if len(sessions) != 0 {
		t.Errorf("List() for project-c returned %d sessions, want 0", len(sessions))
	}
}

func TestSessionRegistry_ActiveCount(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	if registry.ActiveCount() != 0 {
		t.Errorf("ActiveCount() = %d, want 0", registry.ActiveCount())
	}

	session := &Session{
		Code:      "test-1",
		Command:   "claude",
		StartedAt: time.Now(),
		Status:    SessionStatusActive,
	}
	_ = registry.Register(session)

	if registry.ActiveCount() != 1 {
		t.Errorf("ActiveCount() = %d, want 1", registry.ActiveCount())
	}

	_ = registry.Unregister("test-1")

	if registry.ActiveCount() != 0 {
		t.Errorf("ActiveCount() = %d, want 0", registry.ActiveCount())
	}
}

func TestGenerateSessionCode(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Generate first code
	code1 := registry.GenerateSessionCode("claude")
	if code1 != "claude-1" {
		t.Errorf("GenerateSessionCode() = %v, want claude-1", code1)
	}

	// Register it
	session := &Session{
		Code:      code1,
		Command:   "claude",
		StartedAt: time.Now(),
		Status:    SessionStatusActive,
	}
	_ = registry.Register(session)

	// Generate second code
	code2 := registry.GenerateSessionCode("claude")
	if code2 != "claude-2" {
		t.Errorf("GenerateSessionCode() = %v, want claude-2", code2)
	}

	// Generate code for different command
	code3 := registry.GenerateSessionCode("copilot")
	if code3 != "copilot-1" {
		t.Errorf("GenerateSessionCode() = %v, want copilot-1", code3)
	}
}

func TestSessionRegistry_Info(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Add some sessions
	session := &Session{
		Code:      "test-1",
		Command:   "claude",
		StartedAt: time.Now(),
		Status:    SessionStatusActive,
	}
	_ = registry.Register(session)

	info := registry.Info()
	if info.ActiveCount != 1 {
		t.Errorf("Info() ActiveCount = %d, want 1", info.ActiveCount)
	}
	if info.TotalRegistered != 1 {
		t.Errorf("Info() TotalRegistered = %d, want 1", info.TotalRegistered)
	}
}

func TestSession_GetStatus(t *testing.T) {
	session := &Session{
		Code:   "test-1",
		Status: SessionStatusActive,
	}

	if session.GetStatus() != SessionStatusActive {
		t.Errorf("GetStatus() = %v, want active", session.GetStatus())
	}

	session.SetStatus(SessionStatusDisconnected)
	if session.GetStatus() != SessionStatusDisconnected {
		t.Errorf("GetStatus() = %v, want disconnected", session.GetStatus())
	}
}

func TestSession_IsActive(t *testing.T) {
	session := &Session{
		Code:   "test-1",
		Status: SessionStatusActive,
	}

	if !session.IsActive() {
		t.Error("IsActive() = false, want true")
	}

	session.SetStatus(SessionStatusDisconnected)
	if session.IsActive() {
		t.Error("IsActive() = true, want false")
	}
}

func TestSession_UpdateLastSeen(t *testing.T) {
	oldTime := time.Now().Add(-time.Hour)
	session := &Session{
		Code:     "test-1",
		Status:   SessionStatusDisconnected,
		LastSeen: oldTime,
	}

	session.UpdateLastSeen()

	if !session.LastSeen.After(oldTime) {
		t.Error("UpdateLastSeen() did not update LastSeen")
	}
	if session.GetStatus() != SessionStatusActive {
		t.Error("UpdateLastSeen() did not set status to active")
	}
}

func TestSession_ToJSON(t *testing.T) {
	now := time.Now()
	session := &Session{
		Code:        "test-1",
		OverlayPath: "/tmp/overlay.sock",
		ProjectPath: "/project",
		Command:     "claude",
		Args:        []string{"--model", "opus"},
		StartedAt:   now,
		Status:      SessionStatusActive,
		LastSeen:    now,
	}

	json := session.ToJSON()
	if json["code"] != "test-1" {
		t.Errorf("ToJSON() code = %v, want test-1", json["code"])
	}
	if json["command"] != "claude" {
		t.Errorf("ToJSON() command = %v, want claude", json["command"])
	}
	if json["status"] != "active" {
		t.Errorf("ToJSON() status = %v, want active", json["status"])
	}
}

func TestSessionRegistry_FindByDirectory(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Register session for /home/user/project
	session := &Session{
		Code:        "proj-1",
		ProjectPath: "/home/user/project",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	_ = registry.Register(session)

	tests := []struct {
		name      string
		directory string
		wantCode  string
		wantFound bool
	}{
		{
			name:      "exact match",
			directory: "/home/user/project",
			wantCode:  "proj-1",
			wantFound: true,
		},
		{
			name:      "subdirectory match",
			directory: "/home/user/project/src",
			wantCode:  "proj-1",
			wantFound: true,
		},
		{
			name:      "deep subdirectory match",
			directory: "/home/user/project/src/internal/foo",
			wantCode:  "proj-1",
			wantFound: true,
		},
		{
			name:      "no match - parent directory",
			directory: "/home/user",
			wantCode:  "",
			wantFound: false,
		},
		{
			name:      "no match - sibling directory",
			directory: "/home/user/other-project",
			wantCode:  "",
			wantFound: false,
		},
		{
			name:      "no match - similar prefix but not path boundary",
			directory: "/home/user/project-backup",
			wantCode:  "",
			wantFound: false,
		},
		{
			name:      "empty directory",
			directory: "",
			wantCode:  "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, ok := registry.FindByDirectory(tt.directory)
			if ok != tt.wantFound {
				t.Errorf("FindByDirectory(%q) found = %v, want %v", tt.directory, ok, tt.wantFound)
				return
			}
			if ok && found.Code != tt.wantCode {
				t.Errorf("FindByDirectory(%q) code = %v, want %v", tt.directory, found.Code, tt.wantCode)
			}
		})
	}
}

func TestSessionRegistry_FindByDirectory_MostSpecificMatch(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Register sessions at different depths
	session1 := &Session{
		Code:        "root-1",
		ProjectPath: "/home/user",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	session2 := &Session{
		Code:        "proj-1",
		ProjectPath: "/home/user/project",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}
	_ = registry.Register(session1)
	_ = registry.Register(session2)

	// Should find the most specific (deepest) match
	found, ok := registry.FindByDirectory("/home/user/project/src")
	if !ok {
		t.Fatal("FindByDirectory should find a session")
	}
	if found.Code != "proj-1" {
		t.Errorf("FindByDirectory should find most specific match, got %v want proj-1", found.Code)
	}

	// But exact match to parent should find parent
	found, ok = registry.FindByDirectory("/home/user")
	if !ok {
		t.Fatal("FindByDirectory should find a session")
	}
	if found.Code != "root-1" {
		t.Errorf("FindByDirectory should find root session, got %v want root-1", found.Code)
	}
}

func TestSessionRegistry_FindByDirectory_OnlyActiveSession(t *testing.T) {
	registry := NewSessionRegistry(60 * time.Second)

	// Register an inactive session
	session := &Session{
		Code:        "inactive-1",
		ProjectPath: "/home/user/project",
		Command:     "claude",
		StartedAt:   time.Now(),
		Status:      SessionStatusDisconnected, // Not active
		LastSeen:    time.Now(),
	}
	_ = registry.Register(session)

	// Should not find disconnected session
	_, ok := registry.FindByDirectory("/home/user/project")
	if ok {
		t.Error("FindByDirectory should not find disconnected sessions")
	}
}

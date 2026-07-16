package process

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownCancelsRegisteredStartingProcessBeforeSpawn(t *testing.T) {
	pm := NewProcessManager(ManagerConfig{})
	proc := NewManagedProcess(ProcessConfig{ID: "starting", Command: "must-not-spawn"})

	registered := make(chan struct{})
	releaseStart := make(chan struct{})
	pm.startGuardHook = func() {
		close(registered)
		<-releaseStart
	}

	startResult := make(chan error, 1)
	go func() { startResult <- pm.Start(context.Background(), proc) }()
	<-registered

	shutdownResult := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { shutdownResult <- pm.Shutdown(ctx) }()

	select {
	case <-proc.ctx.Done():
		// Shutdown observed StateStarting and cancelled it.
	case <-ctx.Done():
		t.Fatal("shutdown did not cancel registered StateStarting process")
	}
	close(releaseStart)

	if err := <-startResult; !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Start error=%v, want ErrShuttingDown", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("Shutdown error=%v", err)
	}
	if proc.cmd == nil || proc.cmd.Process != nil {
		t.Fatal("process was spawned after shutdown")
	}
	if proc.State() != StateFailed {
		t.Fatalf("state=%s, want Failed", proc.State())
	}
}

func TestShutdownStartingWaitIsBoundedByContext(t *testing.T) {
	pm := NewProcessManager(ManagerConfig{})
	proc := NewManagedProcess(ProcessConfig{ID: "blocked-start", Command: "must-not-spawn"})

	registered := make(chan struct{})
	releaseStart := make(chan struct{})
	pm.startGuardHook = func() {
		close(registered)
		<-releaseStart
	}
	startResult := make(chan error, 1)
	go func() { startResult <- pm.Start(context.Background(), proc) }()
	<-registered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pm.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown error=%v, want context.Canceled", err)
	}

	close(releaseStart)
	if err := <-startResult; !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Start error=%v, want ErrShuttingDown", err)
	}
}

func TestShutdownCannotSnapshotBetweenFinalCheckAndSpawn(t *testing.T) {
	pm := NewProcessManager(ManagerConfig{})
	proc := NewManagedProcess(ProcessConfig{ID: "spawn-gate", Command: "/definitely/not/a/real/command"})

	atSpawnGate := make(chan struct{})
	releaseSpawn := make(chan struct{})
	shutdownAtGate := make(chan struct{})
	pm.spawnGuardHook = func() {
		close(atSpawnGate)
		<-releaseSpawn
	}
	pm.shutdownGuardHook = func() { close(shutdownAtGate) }

	startResult := make(chan error, 1)
	go func() { startResult <- pm.Start(context.Background(), proc) }()
	<-atSpawnGate

	shutdownResult := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { shutdownResult <- pm.Shutdown(ctx) }()
	<-shutdownAtGate

	if pm.IsShuttingDown() {
		t.Fatal("Shutdown crossed spawn gate while Start held ordering lock")
	}
	close(releaseSpawn)

	if err := <-startResult; err == nil {
		t.Fatal("invalid command unexpectedly started")
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("Shutdown error=%v", err)
	}
	if proc.cmd == nil || proc.cmd.Process != nil {
		t.Fatal("regression must reach cmd.Start but never spawn a process")
	}
}

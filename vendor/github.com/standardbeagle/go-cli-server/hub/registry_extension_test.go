package hub

import (
	"context"
	"testing"

	"github.com/standardbeagle/go-cli-server/protocol"
)

func TestCommandRegistry_ExtendAddsSubVerbWithoutReplacingVerb(t *testing.T) {
	r := NewCommandRegistry()

	called := ""
	base := func(context.Context, *Connection, *protocol.Command) error {
		called = "base"
		return nil
	}
	if err := r.Register(CommandDefinition{
		Verb:     "PROC",
		SubVerbs: []string{"STATUS"},
		Handler:  base,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	run := func(context.Context, *Connection, *protocol.Command) error {
		called = "run"
		return nil
	}
	if err := r.Extend(CommandDefinition{
		Verb:     "PROC",
		SubVerbs: []string{"RUN"},
		Handler:  run,
	}); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	if err := r.Dispatch(context.Background(), nil, &protocol.Command{Verb: "PROC", SubVerb: "RUN"}); err != nil {
		t.Fatalf("Dispatch RUN: %v", err)
	}
	if called != "run" {
		t.Fatalf("RUN dispatched to %q, want run", called)
	}

	called = ""
	if err := r.Dispatch(context.Background(), nil, &protocol.Command{Verb: "PROC", SubVerb: "STATUS"}); err != nil {
		t.Fatalf("Dispatch STATUS: %v", err)
	}
	if called != "base" {
		t.Fatalf("STATUS dispatched to %q, want base", called)
	}
}

func TestCommandRegistry_ReplaceSubHandlerRequiresExistingSubVerb(t *testing.T) {
	r := NewCommandRegistry()

	called := ""
	original := func(context.Context, *Connection, *protocol.Command) error {
		called = "original"
		return nil
	}
	if err := r.Register(CommandDefinition{
		Verb:     "PROC",
		SubVerbs: []string{"STATUS"},
		Handler:  original,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	replacement := func(context.Context, *Connection, *protocol.Command) error {
		called = "replacement"
		return nil
	}
	if err := r.ReplaceSubHandler("PROC", "STATUS", replacement); err != nil {
		t.Fatalf("ReplaceSubHandler existing: %v", err)
	}
	if err := r.Dispatch(context.Background(), nil, &protocol.Command{Verb: "PROC", SubVerb: "STATUS"}); err != nil {
		t.Fatalf("Dispatch STATUS: %v", err)
	}
	if called != "replacement" {
		t.Fatalf("STATUS dispatched to %q, want replacement", called)
	}

	if err := r.ReplaceSubHandler("PROC", "RUN", replacement); err == nil {
		t.Fatal("ReplaceSubHandler missing subverb returned nil, want error")
	}
}

func TestCommandRegistry_ExtensionsAreInstanceScoped(t *testing.T) {
	makeRegistry := func(label string) *CommandRegistry {
		r := NewCommandRegistry()
		h := func(context.Context, *Connection, *protocol.Command) error { return nil }
		if err := r.Register(CommandDefinition{
			Verb:     "PROC",
			SubVerbs: []string{"STATUS"},
			Handler:  h,
		}); err != nil {
			t.Fatalf("%s Register: %v", label, err)
		}
		return r
	}

	extended := makeRegistry("extended")
	untouched := makeRegistry("untouched")

	run := func(context.Context, *Connection, *protocol.Command) error { return nil }
	if err := extended.Extend(CommandDefinition{
		Verb:     "PROC",
		SubVerbs: []string{"RUN"},
		Handler:  run,
	}); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	if !containsString(extended.ValidSubVerbs("PROC"), "RUN") {
		t.Fatalf("extended registry missing RUN: %v", extended.ValidSubVerbs("PROC"))
	}
	if containsString(untouched.ValidSubVerbs("PROC"), "RUN") {
		t.Fatalf("untouched registry picked up RUN extension: %v", untouched.ValidSubVerbs("PROC"))
	}
}

func TestCommandRegistry_ReplaceCommandHandlerIsInstanceScoped(t *testing.T) {
	makeRegistry := func(label string) *CommandRegistry {
		r := NewCommandRegistry()
		h := func(context.Context, *Connection, *protocol.Command) error { return nil }
		if err := r.Register(CommandDefinition{
			Verb:    "RUN-JSON",
			Handler: h,
		}); err != nil {
			t.Fatalf("%s Register: %v", label, err)
		}
		return r
	}

	replaced := makeRegistry("replaced")
	untouched := makeRegistry("untouched")

	called := ""
	replacement := func(context.Context, *Connection, *protocol.Command) error {
		called = "replacement"
		return nil
	}
	if err := replaced.ReplaceCommandHandler("RUN-JSON", replacement); err != nil {
		t.Fatalf("ReplaceCommandHandler: %v", err)
	}
	if err := replaced.Dispatch(context.Background(), nil, &protocol.Command{Verb: "RUN-JSON"}); err != nil {
		t.Fatalf("Dispatch replaced RUN-JSON: %v", err)
	}
	if called != "replacement" {
		t.Fatalf("replaced registry dispatched to %q, want replacement", called)
	}

	called = "untouched"
	if err := untouched.Dispatch(context.Background(), nil, &protocol.Command{Verb: "RUN-JSON"}); err != nil {
		t.Fatalf("Dispatch untouched RUN-JSON: %v", err)
	}
	if called != "untouched" {
		t.Fatalf("untouched registry unexpectedly used replacement")
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

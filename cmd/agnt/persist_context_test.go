package main

import (
	"strings"
	"testing"
)

func TestUpsertManagedBlock_EmptyFile(t *testing.T) {
	out := upsertManagedBlock("", "HELLO")
	if !strings.Contains(out, persistBlockBegin) || !strings.Contains(out, persistBlockEnd) {
		t.Fatalf("missing markers:\n%s", out)
	}
	if !strings.Contains(out, "HELLO") {
		t.Fatalf("missing body:\n%s", out)
	}
}

func TestUpsertManagedBlock_PreservesUserContent(t *testing.T) {
	existing := "# My Project\n\nUser notes here.\n"
	out := upsertManagedBlock(existing, "AGNT")
	if !strings.Contains(out, "# My Project") || !strings.Contains(out, "User notes here.") {
		t.Fatalf("user content lost:\n%s", out)
	}
	if !strings.Contains(out, "AGNT") {
		t.Fatalf("body not appended:\n%s", out)
	}
}

func TestUpsertManagedBlock_ReplacesInPlace(t *testing.T) {
	first := upsertManagedBlock("# Doc\n\nkeep me\n", "OLD BODY")
	second := upsertManagedBlock(first, "NEW BODY")
	if strings.Contains(second, "OLD BODY") {
		t.Fatalf("old body not replaced:\n%s", second)
	}
	if !strings.Contains(second, "NEW BODY") {
		t.Fatalf("new body missing:\n%s", second)
	}
	if !strings.Contains(second, "keep me") {
		t.Fatalf("user content lost on replace:\n%s", second)
	}
	if strings.Count(second, persistBlockBegin) != 1 {
		t.Fatalf("expected exactly one managed block, got %d:\n%s",
			strings.Count(second, persistBlockBegin), second)
	}
}

func TestUpsertManagedBlock_Idempotent(t *testing.T) {
	once := upsertManagedBlock("user stuff\n", "BODY")
	twice := upsertManagedBlock(once, "BODY")
	if once != twice {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

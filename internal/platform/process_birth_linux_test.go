//go:build linux

package platform

import (
	"os"
	"testing"
)

func TestProcessBirthID_CurrentProcessStable(t *testing.T) {
	first, ok := ProcessBirthID(os.Getpid())
	if !ok || first == "" {
		t.Fatal("current process birth ID unavailable")
	}
	second, ok := ProcessBirthID(os.Getpid())
	if !ok || second != first {
		t.Fatalf("birth ID unstable: first=%q second=%q ok=%v", first, second, ok)
	}
}

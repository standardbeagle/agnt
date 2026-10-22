package proxy

import (
	"strings"
	"testing"
)

func TestCapDataKeepsSmallAndMarksLarge(t *testing.T) {
	small := map[string]interface{}{"a": "b"}
	if got := capData(small); got["a"] != "b" {
		t.Fatalf("small map changed: %v", got)
	}
	big := map[string]interface{}{"blob": strings.Repeat("x", maxFrameDataBytes+1)}
	got := capData(big)
	if got["truncated"] != true {
		t.Fatalf("large map not truncated: %v", got)
	}
	if _, kept := got["blob"]; kept {
		t.Fatal("large payload survived the cap")
	}
}

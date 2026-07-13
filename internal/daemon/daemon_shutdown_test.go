package daemon

import (
	"context"
	"testing"
	"time"
)

func TestFindPortHoldersWithRetryUsingFreePortStopsAfterFirstScan(t *testing.T) {
	scans := 0
	sleeps := 0
	linux, windows := findPortHoldersWithRetryUsing(context.Background(), 43210,
		func(context.Context, int) ([]int, []int) {
			scans++
			return nil, nil
		},
		func(int) bool { return false },
		func(context.Context, time.Duration) bool {
			sleeps++
			return true
		},
	)

	if len(linux) != 0 || len(windows) != 0 {
		t.Fatalf("holders = (%v, %v), want none", linux, windows)
	}
	if scans != 1 {
		t.Fatalf("scans = %d, want 1", scans)
	}
	if sleeps != 0 {
		t.Fatalf("sleeps = %d, want 0", sleeps)
	}
}

func TestFindPortHoldersWithRetryUsingFindsDelayedHolder(t *testing.T) {
	for name, emptyScans := range map[string]int{"one_empty_scan": 1, "two_empty_scans": 2} {
		t.Run(name, func(t *testing.T) {
			scans := 0
			sleeps := 0
			linux, windows := findPortHoldersWithRetryUsing(context.Background(), 43210,
				func(context.Context, int) ([]int, []int) {
					scans++
					if scans <= emptyScans {
						return nil, nil
					}
					return []int{1234}, nil
				},
				func(int) bool { return true },
				func(context.Context, time.Duration) bool {
					sleeps++
					return true
				},
			)

			if len(linux) != 1 || linux[0] != 1234 || len(windows) != 0 {
				t.Fatalf("holders = (%v, %v), want ([1234], none)", linux, windows)
			}
			if scans != emptyScans+1 {
				t.Fatalf("scans = %d, want %d", scans, emptyScans+1)
			}
			if sleeps != emptyScans {
				t.Fatalf("sleeps = %d, want %d", sleeps, emptyScans)
			}
		})
	}
}

//go:build integration

package replaytest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// startFixtureSPA serves internal/replaytest/testdata/spa over httptest and
// returns the server's base URL plus a proxyID. The proxyID is empty: the
// worker bundle mocks /api/items entirely in-page, so the driver navigates
// directly to BaseURL and no reverse proxy is needed.
func startFixtureSPA(t *testing.T) (baseURL string, proxyID string) {
	t.Helper()
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata/spa")))
	t.Cleanup(srv.Close)
	return srv.URL, ""
}

func fixtureScenario(baseURL string) *Scenario {
	return &Scenario{
		Name:    "spa-items",
		Version: 1,
		BaseURL: baseURL,
		Steps: []Step{
			{
				Index:    0,
				Kind:     StepNavigate,
				Selector: "/",
				Assertions: []Assertion{
					{Selector: "h1", Type: AssertText, Expect: "Hello"},
				},
			},
		},
		Recordings: []Recording{
			{
				Match:   MatchKey{Method: "GET", Path: "/api/items"},
				Status:  200,
				Headers: map[string]string{"content-type": "application/json"},
				BodyRef: "b1",
				Hits:    1,
			},
		},
		Blobs: map[string]string{
			"b1": `{"items":[{"name":"Hello"}]}`,
		},
	}
}

// TestDriverSeedLanePassAndFail exercises the chromedp seed lane end to end:
// a clean replay asserts h1 == "Hello" and PASSES; with the "empty_array"
// preset the items array empties, h1 becomes "" and the assertion FAILS.
func TestDriverSeedLanePassAndFail(t *testing.T) {
	baseURL, proxyID := startFixtureSPA(t)

	t.Run("baseline_pass", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		d := NewDriver(proxyID)
		rep, err := d.RunSeed(ctx, fixtureScenario(baseURL), "")
		if err != nil {
			t.Fatalf("RunSeed baseline: %v", err)
		}
		if !rep.Passed() {
			t.Fatalf("expected baseline PASS, got fail: seeds=%+v crashes=%+v", rep.Seeds, rep.Crashes)
		}
	})

	t.Run("empty_array_fail", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		d := NewDriver(proxyID)
		rep, err := d.RunSeed(ctx, fixtureScenario(baseURL), "empty_array")
		if err != nil {
			t.Fatalf("RunSeed empty_array: %v", err)
		}
		if rep.Passed() {
			t.Fatalf("expected empty_array FAIL, got pass: seeds=%+v", rep.Seeds)
		}
	})
}

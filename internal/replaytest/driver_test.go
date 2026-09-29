package replaytest

import (
	"errors"
	"testing"
)

// fakeProxyResolver is a minimal ProxyResolver for unit-testing
// resolveProxyURL without a daemon or chromedp.
type fakeProxyResolver struct {
	status map[string]interface{}
	err    error
}

func (f *fakeProxyResolver) ProxyStatus(id string) (map[string]interface{}, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.status, nil
}

// TestResolveProxyURL_Empty confirms an empty proxyID never touches the
// resolver — the bundle mocks the network in-page in that case, so no proxy
// URL (and no chromedp --proxy-server flag) is needed.
func TestResolveProxyURL_Empty(t *testing.T) {
	d := NewDriver("")
	url, err := d.resolveProxyURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "" {
		t.Fatalf("url = %q, want empty", url)
	}
}

// TestResolveProxyURL_NoResolver pins the historical bug: passing a proxy id
// through NewDriver (which has no resolver) must fail loudly instead of
// silently handing chromedp the bare id as a --proxy-server value (which
// chromedp would happily pass through as garbage, breaking replay silently).
func TestResolveProxyURL_NoResolver(t *testing.T) {
	d := NewDriver("dev")
	_, err := d.resolveProxyURL()
	if err == nil {
		t.Fatal("expected error for proxy id with no resolver, got nil")
	}
}

// TestResolveProxyURL_ResolvesFromStatus is the fix under test: the driver
// resolves the proxy id to the actual listen URL the daemon reports, not the
// bare id string.
func TestResolveProxyURL_ResolvesFromStatus(t *testing.T) {
	resolver := &fakeProxyResolver{status: map[string]interface{}{
		"id":  "dev",
		"url": "http://127.0.0.1:53211",
	}}
	d := NewDriverWithProxy("dev", resolver)
	url, err := d.resolveProxyURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "http://127.0.0.1:53211" {
		t.Fatalf("url = %q, want http://127.0.0.1:53211", url)
	}
}

// TestResolveProxyURL_ResolverError surfaces a daemon lookup failure instead
// of falling back to something silently wrong.
func TestResolveProxyURL_ResolverError(t *testing.T) {
	resolver := &fakeProxyResolver{err: errors.New("proxy not found")}
	d := NewDriverWithProxy("dev", resolver)
	_, err := d.resolveProxyURL()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestResolveProxyURL_MissingURLField errors rather than handing chromedp an
// empty --proxy-server value when the daemon's status response is missing
// the url field.
func TestResolveProxyURL_MissingURLField(t *testing.T) {
	resolver := &fakeProxyResolver{status: map[string]interface{}{"id": "dev"}}
	d := NewDriverWithProxy("dev", resolver)
	_, err := d.resolveProxyURL()
	if err == nil {
		t.Fatal("expected error for missing url field, got nil")
	}
}

package tools

import "testing"

// The daemon reads PROXYLOG QUERY Limit 0 as "no cap" and returns the whole
// ring buffer, while the tool documented (and reported in pagination) a default
// of 100. The default must be on the wire, not only in the pagination block.
func TestProxyLogQueryFilter_AppliesDocumentedDefaultLimit(t *testing.T) {
	if got := proxyLogQueryFilter(ProxyLogInput{ProxyID: "dev"}).Limit; got != defaultProxyLogQueryLimit {
		t.Fatalf("omitted limit sent as %d, want the documented default %d", got, defaultProxyLogQueryLimit)
	}
	if got := proxyLogQueryFilter(ProxyLogInput{ProxyID: "dev", Limit: 7}).Limit; got != 7 {
		t.Fatalf("explicit limit sent as %d, want 7", got)
	}
}

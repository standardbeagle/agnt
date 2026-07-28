package publish

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// staticResolver is the injected seam: every test in this file uses one, so the
// suite never performs a DNS lookup or opens a socket. There is deliberately no
// fallback to the system resolver — a test that forgot to inject would panic on
// a nil Resolver rather than silently reaching the network.
func staticResolver(addrs ...string) Resolver {
	parsed := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		parsed = append(parsed, netip.MustParseAddr(a))
	}
	return func(context.Context, string) ([]netip.Addr, error) { return parsed, nil }
}

func failingResolver(err error) Resolver {
	return func(context.Context, string) ([]netip.Addr, error) { return nil, err }
}

// TestCheckUpstreamOrigin_DeniesResolvedAddress is the core of INV-13: the
// hostname is attacker-chosen text, so a perfectly public-looking name that
// resolves into a denied range must be refused. Every row uses the SAME public
// hostname to prove the decision comes from the resolved address alone.
func TestCheckUpstreamOrigin_DeniesResolvedAddress(t *testing.T) {
	denied := []struct {
		name string
		addr string
	}{
		{"loopback v4", "127.0.0.1"},
		{"loopback v4 high", "127.255.255.254"},
		{"loopback v6", "::1"},
		{"rfc1918 10/8", "10.0.0.1"},
		{"rfc1918 172.16/12 low", "172.16.0.1"},
		{"rfc1918 172.16/12 high", "172.31.255.254"},
		{"rfc1918 192.168/16", "192.168.1.1"},
		{"link-local v4", "169.254.1.1"},
		{"cloud metadata", "169.254.169.254"},
		{"ecs task metadata", "169.254.170.2"},
		{"alibaba metadata", "100.100.100.200"},
		{"gcp metadata via fd00:ec2", "fd00:ec2::254"},
		{"link-local v6", "fe80::1"},
		{"unique-local v6", "fc00::1"},
		{"unique-local v6 fd", "fd12:3456::1"},
		{"unspecified v4", "0.0.0.0"},
		{"0/8", "0.1.2.3"},
		{"unspecified v6", "::"},
		{"broadcast", "255.255.255.255"},
		{"reserved 240/4", "240.0.0.1"},
		{"multicast v4", "224.0.0.1"},
		{"multicast v6", "ff02::1"},
		{"cgnat 100.64/10", "100.64.0.1"},
		{"ipv4-mapped private", "::ffff:10.0.0.1"},
		{"ipv4-mapped metadata", "::ffff:169.254.169.254"},
		{"ipv4-compatible loopback", "::127.0.0.1"},
		{"6to4 embedding loopback", "2002:7f00:1::1"},
		{"teredo", "2001::1"},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckUpstreamOrigin(context.Background(),
				"https://totally-public.example.com/app", staticResolver(tc.addr))
			if err == nil {
				t.Fatalf("addr %s must be denied, got allow (%v)", tc.addr, got)
			}
			if got != nil {
				t.Fatalf("denied origin must return no dial addresses, got %v", got)
			}
			if !strings.Contains(err.Error(), tc.addr) && !strings.Contains(err.Error(), "denied") {
				t.Fatalf("error must name the offending address or say denied: %v", err)
			}
		})
	}
}

// TestCheckUpstreamOrigin_DeniesEncodedLiterals covers the normalization
// requirement: alternate spellings of a denied literal are decoded BEFORE the
// deny check, never resolved as if they were hostnames. The resolver is set to
// return a public address so that any row passing would prove the literal was
// (wrongly) treated as a DNS name.
func TestCheckUpstreamOrigin_DeniesEncodedLiterals(t *testing.T) {
	encoded := []string{
		"https://127.0.0.1/",
		"https://2130706433/",         // decimal 127.0.0.1
		"https://0177.0.0.1/",         // octal first octet
		"https://0x7f.0.0.1/",         // hex first octet
		"https://0x7f000001/",         // hex whole address
		"https://127.1/",              // inet_aton short form
		"https://[::ffff:127.0.0.1]/", // IPv4-mapped IPv6
		"https://[::ffff:7f00:1]/",    // IPv4-mapped, hex spelling
		"https://2852039166/",         // decimal 169.254.169.254
		"https://[::1]/",
		"https://0/", // decimal 0.0.0.0
	}
	for _, raw := range encoded {
		t.Run(raw, func(t *testing.T) {
			if _, err := CheckUpstreamOrigin(context.Background(), raw,
				staticResolver("93.184.216.34")); err == nil {
				t.Fatalf("%s must be denied after normalization", raw)
			}
		})
	}
}

// TestCheckUpstreamOrigin_AllowsPublic pins the positive case, including that
// the validated addresses are RETURNED so S6 can pin-and-dial exactly them.
func TestCheckUpstreamOrigin_AllowsPublic(t *testing.T) {
	addrs, err := CheckUpstreamOrigin(context.Background(),
		"https://example.com/app", staticResolver("93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"))
	if err != nil {
		t.Fatalf("public host must be allowed: %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("must return every validated address for pin-and-dial, got %v", addrs)
	}
	if addrs[0].String() != "93.184.216.34" || !addrs[1].Is6() {
		t.Fatalf("returned addresses must match the validated set, got %v", addrs)
	}

	// A public IP literal needs no resolution at all.
	lit, err := CheckUpstreamOrigin(context.Background(), "https://93.184.216.34/",
		failingResolver(errors.New("resolver must not be consulted for a literal")))
	if err != nil {
		t.Fatalf("public IP literal must be allowed without resolving: %v", err)
	}
	if len(lit) != 1 || lit[0].String() != "93.184.216.34" {
		t.Fatalf("literal must be returned as its own dial address, got %v", lit)
	}
}

// TestCheckUpstreamOrigin_AnyDeniedAddressDeniesHost: a multi-answer name is
// denied if ANY answer is denied. Accepting because one answer happened to be
// public is the DNS-rebinding hole.
func TestCheckUpstreamOrigin_AnyDeniedAddressDeniesHost(t *testing.T) {
	orders := [][]string{
		{"93.184.216.34", "169.254.169.254"},
		{"169.254.169.254", "93.184.216.34"},
		{"93.184.216.34", "2606:2800:220:1::1", "::ffff:10.1.2.3"},
	}
	for _, answers := range orders {
		got, err := CheckUpstreamOrigin(context.Background(), "https://example.com/",
			staticResolver(answers...))
		if err == nil {
			t.Fatalf("answers %v contain a denied address, must deny; got %v", answers, got)
		}
	}
}

// TestCheckUpstreamOrigin_FailsClosed: every ambiguity denies. No fallback path
// may turn "I could not tell" into "allow".
func TestCheckUpstreamOrigin_FailsClosed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		res  Resolver
	}{
		{"resolver error", "https://example.com/", failingResolver(errors.New("SERVFAIL"))},
		{"empty answer", "https://example.com/", staticResolver()},
		{"nil resolver", "https://example.com/", nil},
		{"http scheme", "http://example.com/", staticResolver("93.184.216.34")},
		{"file scheme", "file:///etc/passwd", staticResolver("93.184.216.34")},
		{"javascript scheme", "javascript:alert(1)", staticResolver("93.184.216.34")},
		{"empty url", "", staticResolver("93.184.216.34")},
		{"no host", "https:///path", staticResolver("93.184.216.34")},
		{"unparseable numeric host", "https://0x/", staticResolver("93.184.216.34")},
		{"overflowing numeric host", "https://999999999999/", staticResolver("93.184.216.34")},
		{"five-part numeric host", "https://1.2.3.4.5/", staticResolver("93.184.216.34")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CheckUpstreamOrigin(context.Background(), tc.raw, tc.res); err == nil {
				t.Fatalf("%s must fail closed", tc.name)
			}
		})
	}
}

// TestDeniedReason_PropertySweep sweeps the whole IPv4 space at a coarse stride
// plus every denied prefix's boundaries, asserting the classifier agrees with
// the stdlib's own notion of non-global addresses wherever the stdlib has one.
// This is the density check: one test covering ~16k addresses beats 30 thin ones.
func TestDeniedReason_PropertySweep(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b += 4 {
			addr := netip.AddrFrom4([4]byte{byte(a), byte(b), 7, 9})
			reason, denied := deniedReason(addr)
			if stdlibNonGlobal(addr) && !denied {
				t.Fatalf("%s is loopback/private/link-local/multicast/unspecified per stdlib but was allowed", addr)
			}
			if denied && reason == "" {
				t.Fatalf("%s denied without a reason", addr)
			}
		}
	}
	// Boundary pairs: last denied address and the first allowed one after it.
	boundaries := []struct {
		last, next string
	}{
		{"10.255.255.255", "11.0.0.0"},
		{"172.31.255.255", "172.32.0.0"},
		{"192.168.255.255", "192.169.0.0"},
		{"127.255.255.255", "128.0.0.0"},
		{"169.254.255.255", "169.255.0.0"},
		{"100.127.255.255", "100.128.0.0"},
		{"0.255.255.255", "1.0.0.0"},
	}
	for _, b := range boundaries {
		if _, denied := deniedReason(netip.MustParseAddr(b.last)); !denied {
			t.Fatalf("%s (prefix boundary) must be denied", b.last)
		}
		if _, denied := deniedReason(netip.MustParseAddr(b.next)); denied {
			t.Fatalf("%s is public and must be allowed (over-broad prefix)", b.next)
		}
	}
	// 172.15/16 and 172.32/16 sit just outside RFC1918 and must stay allowed.
	for _, ok := range []string{"172.15.0.1", "172.32.0.1", "8.8.8.8", "1.1.1.1", "93.184.216.34"} {
		if _, denied := deniedReason(netip.MustParseAddr(ok)); denied {
			t.Fatalf("%s must be allowed", ok)
		}
	}
	// An invalid Addr is denied, not allowed by omission.
	if _, denied := deniedReason(netip.Addr{}); !denied {
		t.Fatalf("zero Addr must be denied")
	}
}

func stdlibNonGlobal(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsMulticast() || a.IsUnspecified()
}

// TestNormalizeHostLiteral pins the decoder directly, including the forms that
// must be REFUSED rather than guessed at.
func TestNormalizeHostLiteral(t *testing.T) {
	ok := map[string]string{
		"127.0.0.1":          "127.0.0.1",
		"2130706433":         "127.0.0.1",
		"0177.0.0.1":         "127.0.0.1",
		"0x7f.0.0.1":         "127.0.0.1",
		"0x7f000001":         "127.0.0.1",
		"127.1":              "127.0.0.1",
		"10.0.65534":         "10.0.255.254",
		"::ffff:127.0.0.1":   "127.0.0.1", // unmapped
		"93.184.216.34":      "93.184.216.34",
		"2606:2800:220:1::1": "2606:2800:220:1::1",
		"0":                  "0.0.0.0",
		"4294967295":         "255.255.255.255",
	}
	for in, want := range ok {
		addr, isLiteral, err := normalizeHostLiteral(in)
		if err != nil || !isLiteral {
			t.Fatalf("%s must decode as a literal, got literal=%v err=%v", in, isLiteral, err)
		}
		if addr.String() != want {
			t.Fatalf("%s decoded to %s, want %s", in, addr, want)
		}
	}
	// Real hostnames are not literals and carry no error — they go to the resolver.
	for _, name := range []string{"example.com", "metadata.google.internal", "dead.beef", "a1.example"} {
		if _, isLiteral, err := normalizeHostLiteral(name); isLiteral || err != nil {
			t.Fatalf("%s must be treated as a DNS name, got literal=%v err=%v", name, isLiteral, err)
		}
	}
	// Numeric-looking but undecodable: fail closed, never fall through to DNS.
	for _, bad := range []string{"0x", "999999999999", "1.2.3.4.5", "256.1.1.1", "0779.0.0.1", "0x1g"} {
		if _, _, err := normalizeHostLiteral(bad); err == nil {
			t.Fatalf("%s must fail closed", bad)
		}
	}
}

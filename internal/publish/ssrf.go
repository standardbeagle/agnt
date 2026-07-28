package publish

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// This file implements INV-13 of
// docs/superpowers/specs/2026-07-13-public-walkthrough-publish-security.md §4a.
//
// WHAT THIS IS: open-relay hygiene. Proxying a publisher-named live origin
// makes the daemon fetch an arbitrary URL and hand back the bytes. Unconstrained
// that is a relay into whatever network the daemon can see — a developer
// laptop's LAN, a CI runner's VPC, a cloud instance's metadata service. The
// deny-list below closes that.
//
// WHAT THIS IS NOT: an anti-phishing or anti-deception control. §4a is explicit
// that a publisher dressing up a legitimate public site is out of the threat
// model — the publisher holds the dev session and is trusted by construction,
// and the honest mitigation is disclosure via the always-on demo indicator
// (INV-14), not this allowlist. It could not work as one anyway: attackers use
// public origins, which are exactly what this permits.

// Resolver is the DNS seam. It is injected so the guard stays a pure function
// of (URL, resolver answer) — unit tests supply a static answer and never open
// a socket, and callers can supply a resolver whose answer they then pin.
// A nil Resolver denies (fail closed); there is deliberately no implicit
// fallback to the system resolver.
type Resolver func(ctx context.Context, host string) ([]netip.Addr, error)

// SystemResolver is the production Resolver, backed by net.DefaultResolver.
func SystemResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// deniedPrefixes is the whole policy: §4a's table, nothing more. Order is
// irrelevant (first match wins only for the error message).
var deniedPrefixes = []struct {
	prefix netip.Prefix
	reason string
}{
	// IPv4
	{netip.MustParsePrefix("0.0.0.0/8"), "unspecified/this-network"},
	{netip.MustParsePrefix("10.0.0.0/8"), "RFC1918 private"},
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade NAT"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local (incl. 169.254.169.254 cloud metadata)"},
	{netip.MustParsePrefix("172.16.0.0/12"), "RFC1918 private"},
	{netip.MustParsePrefix("192.168.0.0/16"), "RFC1918 private"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved (incl. 255.255.255.255 broadcast)"},
	// IPv6. ::/96 covers the unspecified address, ::1 loopback, and the
	// IPv4-compatible embeddings (::127.0.0.1) in one prefix.
	{netip.MustParsePrefix("::/96"), "unspecified/loopback/IPv4-compatible"},
	{netip.MustParsePrefix("fc00::/7"), "unique-local (incl. fd00:ec2::254 cloud metadata)"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
	// 6to4 and Teredo embed an arbitrary IPv4 address, so they are a direct
	// route to a denied v4 target (2002:7f00:1::1 is 127.0.0.1). Both are
	// deprecated/obsolete for origin servers, so denying the prefixes outright
	// is cheaper and safer than decoding the embedded address.
	{netip.MustParsePrefix("2002::/16"), "6to4 (embeds arbitrary IPv4)"},
	{netip.MustParsePrefix("2001::/32"), "Teredo (embeds arbitrary IPv4)"},
	// NAT64 is the one IPv4-embedding prefix family that is NOT obsolete: an
	// IPv6-only cloud/CI subnet — precisely the environment §4a is about —
	// reaches IPv4 through a translator at the well-known prefix, so
	// 64:ff9b::a9fe:a9fe IS 169.254.169.254. Denied for the same reason as
	// 6to4: the embedded address is arbitrary, so the prefix goes whole.
	{netip.MustParsePrefix("64:ff9b::/96"), "NAT64 well-known prefix (embeds arbitrary IPv4)"},
	{netip.MustParsePrefix("64:ff9b:1::/48"), "NAT64 local-use prefix RFC 8215 (embeds arbitrary IPv4)"},
}

// deniedAddrs are §4a entries that are single addresses rather than ranges and
// that no prefix above already covers.
var deniedAddrs = map[netip.Addr]string{
	netip.MustParseAddr("100.100.100.200"): "Alibaba cloud metadata",
}

// deniedReason classifies one address. It operates on an unmapped address, so
// ::ffff:10.0.0.1 is judged as 10.0.0.1 rather than waved through as "IPv6,
// therefore not RFC1918". An invalid address is denied — never allowed by
// omission.
//
// A zone identifier (fe80::1%eth0, and via RFC 6874 the URL form
// https://[fe80::1%25eth0]/) is REJECTED OUTRIGHT rather than stripped. Two
// reasons, and the first alone is decisive:
//
//  1. A zone scopes an address to one local interface. That is the definition
//     of not-a-public-origin, so no legitimate upstream can carry one — even
//     when the bare address is public. Rejecting is therefore both simpler and
//     strictly safer than stripping-then-classifying.
//  2. Stripping would leave the zone able to re-enter. netip.Prefix.Contains
//     returns false by documented design for a zoned Addr, and a zone is part
//     of Addr identity so a map lookup misses too — that is how a zone silently
//     defeated this whole table at once, and how a zoned address could be
//     returned as a validated dial address. This check runs FIRST, before the
//     map and the prefix walk, so neither can be reached with a zone attached
//     and nothing that reaches the returned slice can carry one.
//
// Unmap() drops a zone as a side effect, which is why the IPv4-mapped rows
// survived incidentally; correctness must not rest on that.
func deniedReason(addr netip.Addr) (string, bool) {
	if !addr.IsValid() {
		return "invalid address", true
	}
	if addr.Zone() != "" {
		return "interface-scoped zone identifier (never a public origin)", true
	}
	addr = addr.Unmap()
	if reason, ok := deniedAddrs[addr]; ok {
		return reason, true
	}
	for _, d := range deniedPrefixes {
		if d.prefix.Contains(addr) {
			return d.reason, true
		}
	}
	return "", false
}

// CheckUpstreamOrigin is the INV-13 guard. It validates rawURL (https-only, via
// the existing ValidateURL), normalizes the host, resolves it when it is a name,
// and rejects the origin unless EVERY resolved address is outside the deny-list.
// On success it returns those validated addresses. Every returned address has
// passed deniedReason, so none of them carries a zone identifier — S6 dials
// exactly this slice, and a zoned address handed back would be a live hole.
//
// The caller MUST dial one of the returned addresses rather than re-resolving
// the hostname. Validating a name and then letting a transport resolve it again
// leaves a TOCTOU window in which a rebinding resolver answers publicly for the
// check and privately for the dial; returning the addresses exists so the caller
// can pin-and-dial and close that window. This function cannot close it alone —
// it does not own the transport. Redirect hops are likewise the caller's
// obligation: §4a requires re-running this check on every hop, with a depth cap
// that fails closed.
//
// Every ambiguity denies: a nil resolver, a resolver error, an empty answer, a
// host that looks numeric but does not decode. There is no fallback path that
// turns "could not tell" into "allowed".
func CheckUpstreamOrigin(ctx context.Context, rawURL string, resolve Resolver) ([]netip.Addr, error) {
	if err := ValidateURL(rawURL); err != nil {
		return nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errf("origin: unparseable: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errf("origin: missing host")
	}

	addr, isLiteral, err := normalizeHostLiteral(host)
	if err != nil {
		return nil, errf("origin %q: %w", host, err)
	}

	var addrs []netip.Addr
	if isLiteral {
		addrs = []netip.Addr{addr}
	} else {
		if resolve == nil {
			return nil, errf("origin %q: no resolver supplied", host)
		}
		resolved, err := resolve(ctx, host)
		if err != nil {
			return nil, errf("origin %q: resolve failed: %w", host, err)
		}
		if len(resolved) == 0 {
			return nil, errf("origin %q: resolved to no addresses", host)
		}
		addrs = make([]netip.Addr, 0, len(resolved))
		for _, a := range resolved {
			addrs = append(addrs, a.Unmap())
		}
	}

	// Every address must pass. One public answer among private ones does not
	// make the host safe — that is exactly the DNS-rebinding shape.
	for _, a := range addrs {
		if reason, denied := deniedReason(a); denied {
			return nil, errf("origin %q resolves to denied address %s (%s)", host, a, reason)
		}
	}
	return addrs, nil
}

// normalizeHostLiteral decides whether host is an IP literal and, if so, decodes
// it to a canonical netip.Addr. Alternate IPv4 spellings that inet_aton accepts
// (decimal 2130706433, octal 0177.0.0.1, hex 0x7f000001, short 127.1) are
// decoded HERE, before the deny check, so they cannot be laundered past a
// literal-prefix table or handed to a resolver that would decode them itself.
//
// Returns (addr, true, nil) for a literal, (zero, false, nil) for a DNS name,
// and an error for a host that is numeric in shape but does not decode — that
// case fails closed rather than falling through to DNS.
func normalizeHostLiteral(host string) (netip.Addr, bool, error) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return netip.Addr{}, false, errf("empty host")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap(), true, nil
	}
	if !looksNumeric(host) {
		return netip.Addr{}, false, nil
	}
	addr, err := parseInetAton(host)
	if err != nil {
		return netip.Addr{}, false, err
	}
	return addr, true, nil
}

// looksNumeric reports whether every dot-separated label is an integer literal
// (decimal, or 0x-prefixed hex — a leading 0 means octal, still all digits).
// A real hostname's rightmost label is alphabetic, so this cannot capture one;
// "dead.beef" is not numeric because its labels lack a 0x prefix.
func looksNumeric(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return false
		}
		if strings.HasPrefix(label, "0x") || strings.HasPrefix(label, "0X") {
			continue
		}
		for _, r := range label {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// parseInetAton decodes the historical IPv4 forms: 1 to 4 parts, where the last
// part absorbs all remaining octets (127.1 == 127.0.0.1), each part decimal,
// octal (leading 0) or hex (leading 0x).
func parseInetAton(host string) (netip.Addr, error) {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return netip.Addr{}, errf("not a valid address: %d numeric parts", len(parts))
	}
	values := make([]uint64, 0, len(parts))
	for _, p := range parts {
		// base 0 gives Go's own 0x/0-prefix handling; reject signs and
		// underscores, which ParseUint base 0 would otherwise accept.
		if strings.ContainsAny(p, "_+-") {
			return netip.Addr{}, errf("not a valid address: malformed part %q", p)
		}
		v, err := strconv.ParseUint(p, 0, 64)
		if err != nil {
			return netip.Addr{}, errf("not a valid address: part %q: %w", p, err)
		}
		values = append(values, v)
	}
	// Every part but the last is one octet; the last fills the remainder.
	var packed uint64
	last := len(values) - 1
	for i, v := range values {
		if i < last {
			if v > 0xff {
				return netip.Addr{}, errf("not a valid address: octet %d out of range", v)
			}
			packed |= v << uint(8*(3-i))
			continue
		}
		// The last part covers the 5-len(values) octets the leading parts did
		// not: 4 parts leave it one octet, 1 part leaves it all four.
		limit := uint64(1) << uint(8*(5-len(values)))
		if v >= limit {
			return netip.Addr{}, errf("not a valid address: trailing value %d out of range", v)
		}
		packed |= v
	}
	return netip.AddrFrom4([4]byte{
		byte(packed >> 24), byte(packed >> 16), byte(packed >> 8), byte(packed),
	}), nil
}

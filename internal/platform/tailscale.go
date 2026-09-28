package platform

import (
	"context"
	"encoding/json"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

// TailscaleDNSName returns this node's MagicDNS name (no trailing dot),
// e.g. "machine.tailnet.ts.net". Returns "" if tailscale is unavailable,
// not logged in, or the lookup times out.
//
// If ctx has no deadline, a 2s timeout is applied. Safe to call without
// tailscale installed — exec errors collapse to "".
func TailscaleDNSName(ctx context.Context) string {
	output := tailscaleStatusJSON(ctx)
	if output == nil {
		return ""
	}
	return parseTailscaleDNSName(output)
}

// TailscaleSelfIdentities returns every authority (hostname or IP, no
// port) under which this node is reachable on its tailnet: the MagicDNS
// name plus each tailscale IP. Returns nil if tailscale is unavailable,
// not logged in, or the lookup times out.
//
// Used by the proxy's WebSocket origin check to accept same-origin
// requests that arrive over the tailnet without opening the check to
// DNS rebinding: an attacker-controlled hostname is never one of these.
func TailscaleSelfIdentities(ctx context.Context) []string {
	output := tailscaleStatusJSON(ctx)
	if output == nil {
		return nil
	}
	return parseTailscaleSelfIdentities(output)
}

// tailnetRange is the CGNAT block tailscale assigns node addresses from.
// TailscaleIP refuses anything outside it, so an address it returns can only
// ever be a tailnet interface -- never a LAN or public one.
var tailnetRange = netip.MustParsePrefix("100.64.0.0/10")

// TailscaleIP returns this node's tailnet IPv4 address, e.g. "100.101.102.103".
// Returns "" if tailscale is unavailable, not logged in, the lookup times out,
// or the node has no address inside the tailnet range.
//
// IPv4 only: a bind address is joined to a port as host:port, and an IPv6
// literal there needs brackets. Every tailscale node has a 100.x address.
func TailscaleIP(ctx context.Context) string {
	output := tailscaleStatusJSON(ctx)
	if output == nil {
		return ""
	}
	return parseTailscaleIP(output)
}

// IsTailnetAddress reports whether addr is an IPv4 address inside the tailnet
// range. Callers that grant a tailnet address a posture a LAN address does not
// get must check the address itself, so the grant cannot be widened by whatever
// produced it.
func IsTailnetAddress(addr string) bool {
	parsed, err := netip.ParseAddr(addr)
	return err == nil && parsed.Is4() && tailnetRange.Contains(parsed)
}

// parseTailscaleIP extracts the first Self.TailscaleIPs entry that is an IPv4
// address inside the tailnet range. Returns "" on parse failure or no match.
//
// Split out for testability without spawning the binary.
func parseTailscaleIP(output []byte) string {
	var status tailscaleStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return ""
	}
	for _, raw := range status.Self.TailscaleIPs {
		if IsTailnetAddress(raw) {
			return raw
		}
	}
	return ""
}

func tailscaleStatusJSON(ctx context.Context) []byte {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	return output
}

type tailscaleStatus struct {
	Self struct {
		DNSName      string   `json:"DNSName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
}

// parseTailscaleDNSName extracts Self.DNSName from `tailscale status --json`
// output and trims any trailing dot. Returns "" on parse failure.
//
// Split out for testability without spawning the binary.
func parseTailscaleDNSName(output []byte) string {
	var status tailscaleStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return ""
	}
	return strings.TrimSuffix(status.Self.DNSName, ".")
}

// parseTailscaleSelfIdentities extracts Self.DNSName (trailing dot trimmed)
// and Self.TailscaleIPs. Returns nil on parse failure or when the node has
// no identity at all.
func parseTailscaleSelfIdentities(output []byte) []string {
	var status tailscaleStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return nil
	}
	var ids []string
	if name := strings.TrimSuffix(status.Self.DNSName, "."); name != "" {
		ids = append(ids, name)
	}
	for _, ip := range status.Self.TailscaleIPs {
		if ip != "" {
			ids = append(ids, ip)
		}
	}
	return ids
}

// TailscaleWhois returns the login name of the human who owns the tailnet
// node at ip, e.g. "user@example.com", as tailscaled authenticates it: the
// address maps to a WireGuard key, and the key to its owner. Returns "" when
// tailscale is unavailable, the lookup fails or times out, ip is not a
// tailnet peer, or the node is tagged (tagged devices belong to the tailnet,
// not to a person, so they carry no identity to authorize).
//
// If ctx has no deadline, a 2s timeout is applied.
func TailscaleWhois(ctx context.Context, ip string) string {
	if _, err := netip.ParseAddr(ip); err != nil {
		return ""
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	output, err := exec.CommandContext(ctx, "tailscale", "whois", "--json", ip).Output()
	if err != nil {
		return ""
	}
	return parseTailscaleWhois(output)
}

// parseTailscaleWhois extracts UserProfile.LoginName from `tailscale whois
// --json`, refusing tagged nodes. Split out for testability.
func parseTailscaleWhois(output []byte) string {
	var who struct {
		Node struct {
			Tags []string `json:"Tags"`
		} `json:"Node"`
		UserProfile struct {
			LoginName string `json:"LoginName"`
		} `json:"UserProfile"`
	}
	if err := json.Unmarshal(output, &who); err != nil {
		return ""
	}
	if len(who.Node.Tags) > 0 {
		return ""
	}
	login := strings.TrimSpace(who.UserProfile.LoginName)
	if login == "" || login == "tagged-devices" || !strings.Contains(login, "@") {
		return ""
	}
	return login
}

// tailnetRange6 is the ULA block tailscale assigns node IPv6 addresses from.
var tailnetRange6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")

// IsTailnetPeerAddress reports whether addr (IPv4 or IPv6) is inside a range
// tailscale assigns node addresses from. Unlike IsTailnetAddress it accepts
// IPv6, because it classifies a connection's peer, not a bind address.
func IsTailnetPeerAddress(addr string) bool {
	parsed, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	parsed = parsed.Unmap()
	return (parsed.Is4() && tailnetRange.Contains(parsed)) || tailnetRange6.Contains(parsed)
}

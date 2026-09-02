package platform

import (
	"context"
	"encoding/json"
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

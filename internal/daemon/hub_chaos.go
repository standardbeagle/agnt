package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) chaosActions() map[string]handlerFn {
	return map[string]handlerFn{
		"ENABLE":       noCtx(d.hubHandleChaosEnable),
		"DISABLE":      noCtx(d.hubHandleChaosDisable),
		"STATUS":       noCtx(d.hubHandleChaosStatus),
		"PRESET":       noCtx(d.hubHandleChaosPreset),
		"SET":          noCtx(d.hubHandleChaosSet),
		"ADD-RULE":     noCtx(d.hubHandleChaosAddRule),
		"REMOVE-RULE":  noCtx(d.hubHandleChaosRemoveRule),
		"LIST-RULES":   noCtx(d.hubHandleChaosListRules),
		"STATS":        noCtx(d.hubHandleChaosStats),
		"CLEAR":        noCtx(d.hubHandleChaosClear),
		"LIST-PRESETS": connOnly(d.hubHandleChaosListPresets),
	}
}

func (d *Daemon) hubHandleChaos(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("CHAOS").dispatch(ctx, conn, cmd, d.chaosActions())
}

// hubHandleChaosEnable handles CHAOS ENABLE command.

func (d *Daemon) hubHandleChaosEnable(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS ENABLE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.ChaosEngine().Enable()
	return conn.WriteOK("chaos enabled")
}

// hubHandleChaosDisable handles CHAOS DISABLE command.

func (d *Daemon) hubHandleChaosDisable(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS DISABLE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.ChaosEngine().Disable()
	return conn.WriteOK("chaos disabled")
}

// hubHandleChaosStatus handles CHAOS STATUS command.

func (d *Daemon) hubHandleChaosStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS STATUS requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	engine := p.ChaosEngine()
	config := engine.GetConfig()
	snap := engine.Snapshot()

	// rules is the live rule set (Snapshot): config.Rules holds only what the
	// last SET or PRESET installed, not rules added or removed since.
	resp := map[string]interface{}{
		"enabled": engine.IsEnabled(),
		"config":  config,
		"rules":   snap.Rules,
		"stats":   snap.Stats,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleChaosPreset handles CHAOS PRESET command.

func (d *Daemon) hubHandleChaosPreset(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS PRESET requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	config, err := unmarshalCommand[struct {
		Preset string `json:"chaos_preset"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid chaos preset payload: %v", err))
	}

	if config.Preset == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "chaos_preset is required")
	}

	presetConfig := proxy.GetPreset(config.Preset)
	if presetConfig == nil {
		availablePresets := proxy.ListPresets()
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("unknown preset %q. Available: %s", config.Preset, strings.Join(availablePresets, ", ")))
	}

	if err := p.ChaosEngine().SetConfig(presetConfig); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK(fmt.Sprintf("preset %s applied", config.Preset))
}

// hubHandleChaosSet handles CHAOS SET command.

func (d *Daemon) hubHandleChaosClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS CLEAR requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.ChaosEngine().Clear()
	return conn.WriteOK("chaos cleared")
}

// hubHandleChaosListPresets handles CHAOS LIST-PRESETS command.

func (d *Daemon) hubHandleChaosListPresets(conn *hubpkg.Connection) error {
	presets := proxy.ListPresets()

	data, _ := json.Marshal(map[string]interface{}{"presets": presets})
	return conn.WriteJSON(data)
}

// hubHandleSession handles the SESSION command.
func (d *Daemon) hubHandleChaosSet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS SET requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// SetConfig replaces the whole rule set, so a payload that does not decode
	// must be refused: applying the zero config would wipe every rule.
	config, err := unmarshalCommand[proxy.ChaosConfig](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid chaos config: %v", err))
	}

	if err := p.ChaosEngine().SetConfig(&config); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK("chaos config set")
}

// hubHandleChaosAddRule handles CHAOS ADD-RULE command.
func (d *Daemon) hubHandleChaosAddRule(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS ADD-RULE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// A payload that does not decode is refused as such; reading the zero
	// value on would report it as a missing rule id.
	wrapper, err := unmarshalCommand[struct {
		Rule proxy.ChaosRule `json:"chaos_rule"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid chaos rule: %v", err))
	}

	if wrapper.Rule.ID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "rule id is required")
	}

	if err := p.ChaosEngine().AddRule(&wrapper.Rule); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK("rule added")
}

// hubHandleChaosRemoveRule handles CHAOS REMOVE-RULE command.
func (d *Daemon) hubHandleChaosRemoveRule(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS REMOVE-RULE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	config, err := unmarshalCommand[struct {
		RuleID string `json:"chaos_rule_id"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid chaos rule id payload: %v", err))
	}

	if config.RuleID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "chaos_rule_id is required")
	}

	if !p.ChaosEngine().RemoveRule(config.RuleID) {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("no chaos rule %q on proxy %s", config.RuleID, proxyID))
	}
	return conn.WriteOK("rule removed")
}

// hubHandleChaosListRules handles CHAOS LIST-RULES command.
func (d *Daemon) hubHandleChaosListRules(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS LIST-RULES requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// The live rule set: config.Rules holds only what the last SET or PRESET
	// installed, and misses rules added (ADD-RULE) or removed since.
	rules := p.ChaosEngine().Snapshot().Rules

	data, _ := json.Marshal(map[string]interface{}{"rules": rules})
	return conn.WriteJSON(data)
}

// hubHandleChaosStats handles CHAOS STATS command.
func (d *Daemon) hubHandleChaosStats(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS STATS requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	stats := p.ChaosEngine().GetStats()

	data, _ := json.Marshal(stats)
	return conn.WriteJSON(data)
}

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleChaos(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "CHAOS %s: args=%v", cmd.SubVerb, cmd.Args)
	switch cmd.SubVerb {
	case "ENABLE":
		return d.hubHandleChaosEnable(conn, cmd)
	case "DISABLE":
		return d.hubHandleChaosDisable(conn, cmd)
	case "STATUS":
		return d.hubHandleChaosStatus(conn, cmd)
	case "PRESET":
		return d.hubHandleChaosPreset(conn, cmd)
	case "SET":
		return d.hubHandleChaosSet(conn, cmd)
	case "ADD-RULE":
		return d.hubHandleChaosAddRule(conn, cmd)
	case "REMOVE-RULE":
		return d.hubHandleChaosRemoveRule(conn, cmd)
	case "LIST-RULES":
		return d.hubHandleChaosListRules(conn, cmd)
	case "STATS":
		return d.hubHandleChaosStats(conn, cmd)
	case "CLEAR":
		return d.hubHandleChaosClear(conn, cmd)
	case "LIST-PRESETS":
		return d.hubHandleChaosListPresets(conn)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown CHAOS sub-command",
			Command:      "CHAOS",
			ValidActions: []string{"ENABLE", "DISABLE", "STATUS", "PRESET", "SET", "ADD-RULE", "REMOVE-RULE", "LIST-RULES", "STATS", "CLEAR", "LIST-PRESETS"},
		})
	}
}

// hubHandleChaosEnable handles CHAOS ENABLE command.

func (d *Daemon) hubHandleChaosEnable(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS ENABLE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.getSessionScopedProxy(conn, proxyID)
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	engine := p.ChaosEngine()
	config := engine.GetConfig()

	resp := map[string]interface{}{
		"enabled": engine.IsEnabled(),
		"config":  config,
		"stats":   engine.GetStats(),
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var config struct {
		Preset string `json:"chaos_preset"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var config proxy.ChaosConfig
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var wrapper struct {
		Rule proxy.ChaosRule `json:"chaos_rule"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &wrapper)
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var config struct {
		RuleID string `json:"chaos_rule_id"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
	}

	if config.RuleID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "chaos_rule_id is required")
	}

	p.ChaosEngine().RemoveRule(config.RuleID)
	return conn.WriteOK("rule removed")
}

// hubHandleChaosListRules handles CHAOS LIST-RULES command.
func (d *Daemon) hubHandleChaosListRules(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS LIST-RULES requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	config := p.ChaosEngine().GetConfig()
	var rules []*proxy.ChaosRule
	if config != nil {
		rules = config.Rules
	}
	if rules == nil {
		rules = []*proxy.ChaosRule{}
	}

	data, _ := json.Marshal(map[string]interface{}{"rules": rules})
	return conn.WriteJSON(data)
}

// hubHandleChaosStats handles CHAOS STATS command.
func (d *Daemon) hubHandleChaosStats(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS STATS requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	stats := p.ChaosEngine().GetStats()

	data, _ := json.Marshal(stats)
	return conn.WriteJSON(data)
}

package agentadapter

import "time"

// overriddenAdapter wraps an Adapter and applies an Override on top of
// its behavior. Kept private because callers should always go through
// [Registry.Lookup] to pick up overrides.
type overriddenAdapter struct {
	inner    Adapter
	override Override
}

func withOverride(a Adapter, ov Override) Adapter {
	// A zero override collapses back to the bare adapter to avoid an
	// unnecessary wrapper on the hot path.
	if !ov.Disabled && ov.FlagName == "" && ov.StdinDelay == 0 {
		return a
	}
	return &overriddenAdapter{inner: a, override: ov}
}

func (o *overriddenAdapter) Name() string { return o.inner.Name() }

func (o *overriddenAdapter) Matches(command string) bool {
	return o.inner.Matches(command)
}

func (o *overriddenAdapter) BuildArgs(baseArgs []string, prompt string) []string {
	if o.override.Disabled {
		return cloneArgs(baseArgs)
	}
	// For flag-based adapters the FlagName override changes which CLI
	// flag carries the prompt. stdin-based adapters ignore FlagName
	// entirely.
	if o.override.FlagName != "" {
		if c, ok := o.inner.(*claudeAdapter); ok {
			tmp := *c
			tmp.flag = o.override.FlagName
			return tmp.BuildArgs(baseArgs, prompt)
		}
	}
	return o.inner.BuildArgs(baseArgs, prompt)
}

func (o *overriddenAdapter) InitialStdin(prompt string) []byte {
	if o.override.Disabled {
		return nil
	}
	return o.inner.InitialStdin(prompt)
}

func (o *overriddenAdapter) StdinDelay() time.Duration {
	if o.override.StdinDelay > 0 {
		return o.override.StdinDelay
	}
	return o.inner.StdinDelay()
}

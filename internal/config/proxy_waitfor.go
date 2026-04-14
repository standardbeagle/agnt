package config

import "fmt"

// validateProxyWaitFor ensures every script name listed in a proxy's
// `wait-for` block resolves to a declared script. A typo in a
// `wait-for` entry would otherwise cause the proxy to gate forever
// (nothing ever signals ready for "dev-bakcend"), so we fail loudly
// at parse time per the config contracts in
// .claude/rules/config-contracts.md.
//
// Also rejects a proxy that lists itself in `wait-for` by way of a
// same-named script, since proxy → proxy dependencies are out of
// scope — only script-ready gates are supported.
func validateProxyWaitFor(proxies map[string]*ProxyConfig, scripts map[string]*ScriptConfig) error {
	for proxyName, proxyCfg := range proxies {
		if proxyCfg == nil || len(proxyCfg.WaitFor) == 0 {
			continue
		}
		seen := make(map[string]struct{}, len(proxyCfg.WaitFor))
		for _, dep := range proxyCfg.WaitFor {
			if dep == "" {
				return fmt.Errorf("proxy %q has empty wait-for entry", proxyName)
			}
			if _, dup := seen[dep]; dup {
				return fmt.Errorf("proxy %q has duplicate wait-for entry %q", proxyName, dep)
			}
			seen[dep] = struct{}{}

			if _, ok := scripts[dep]; !ok {
				return fmt.Errorf("proxy %q wait-for references unknown script %q", proxyName, dep)
			}
		}
	}
	return nil
}

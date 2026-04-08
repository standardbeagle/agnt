// Package proxy implements a reverse HTTP proxy with JavaScript injection,
// traffic logging, and chaos engineering support for browser debugging.
//
// Key types:
//   - ProxyServer: the core proxy, created via ProxyManager.Create()
//   - ProxyManager: registry and lifecycle manager for all proxies
//   - TrafficLogger: circular-buffer log of HTTP traffic and browser events
//   - ChaosEngine: configurable fault injection (latency, errors, bandwidth)
//
// Proxies inject a __devtool JavaScript API into HTML responses for
// browser-side diagnostics, error capture, and real-time metrics.
package proxy

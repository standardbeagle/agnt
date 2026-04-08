// Package config defines configuration types for agnt, parsed from
// .agnt.kdl files using KDL format. It also provides shell resolution,
// platform detection (WSL-aware), and port conflict utilities.
//
// Key types:
//   - AgntConfig: top-level project configuration
//   - ScriptConfig: per-script autostart configuration
//   - ProxyConfig: per-proxy configuration with fallback-port support
package config

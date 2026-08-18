// Package httpcaps is the single place an HTTP listener in this repo is given
// its transport and connection bounds. It exists so that "an uncapped server"
// is the harder thing to write than a capped one: every listener — the three
// potentially-public ones (dev proxy, public walkthrough plane, `agnt publish
// serve`) and the two loopback overlays — is stood up through NewServer +
// LimitListener rather than a bare &http.Server{}, so a sixth listener added by
// omission inherits the caps instead of shipping unbounded.
//
// # Why these bounds (AGNT_PUBLIC exposure posture, AGNT.md § Exposure Posture)
//
// The operator declared the first three listeners "potentially-public" and
// hardened to public standard UNCONDITIONALLY — the same listener can be pointed
// at a cloudflare tunnel a moment later, so the caps must hold in every posture
// and widening exposure stays purely a routing decision. Each bound closes a
// concrete unbounded-resource hole:
//
//   - ReadHeaderTimeout — slowloris: a client dribbling header bytes forever pins
//     one goroutine + one fd per connection, bounded otherwise only by the host
//     fd limit. This is the single most important cap and is safe on EVERY
//     listener (it only times the request-line + headers, never the body).
//   - ReadTimeout / WriteTimeout — a slow-read or slow-write peer that never
//     finishes its request/response body holds the connection open indefinitely.
//     These bound the WHOLE request/response, so they are unsafe on a listener
//     that streams or hijacks (see Streaming below).
//   - IdleTimeout — a client hoarding idle keep-alive connections it never reuses.
//   - MaxHeaderBytes — Go's silent 1 MiB-per-connection header buffer, unbounded
//     in aggregate precisely because the connection count was unbounded. Made
//     explicit here (same 1 MiB) so the aggregate is MaxConns × MaxHeaderBytes.
//   - MaxConns (LimitListener) — the aggregate goroutine/fd/memory bound. Mirrors
//     the in-repo control-socket precedent (daemon.go MaxClients:100); 256 gives
//     a browser opening parallel connections and a many-viewer public plane
//     headroom while still being a hard ceiling.
package httpcaps

import (
	"net"
	"net/http"
	"time"

	"golang.org/x/net/netutil"
)

// House defaults. One set, applied everywhere; a listener that genuinely cannot
// take a whole-request read/write deadline sets Streaming rather than inventing
// its own numbers.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 30 * time.Second
	DefaultWriteTimeout      = 60 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultMaxHeaderBytes    = 1 << 20 // 1 MiB — Go's default, made explicit.
	DefaultMaxConns          = 256
)

// Caps is the bound set for one listener. The zero value is intentionally NOT
// usable — construct via Default() or Streaming() so "forgot to cap" cannot
// silently compile into an unbounded server.
type Caps struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxConns          int

	// Streaming drops ReadTimeout and WriteTimeout to 0 (unbounded) for a
	// listener whose core function is to stream or hijack — the dev reverse proxy
	// (arbitrary-length proxied bodies, and WebSocket upgrades like Vite HMR that
	// hijack the connection) and the overlays (long-lived gorilla/websocket
	// connections over a unix socket). A whole-request write deadline would sever
	// those. Slowloris and fd exhaustion are still bounded on a streaming listener
	// by ReadHeaderTimeout + IdleTimeout + MaxConns, which the header/idle/connection
	// phases obey regardless of body streaming.
	Streaming bool
}

// Default returns the house cap set for a request/response listener that serves
// bounded payloads (the public plane, `agnt publish serve`).
func Default() Caps {
	return Caps{
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		MaxHeaderBytes:    DefaultMaxHeaderBytes,
		MaxConns:          DefaultMaxConns,
	}
}

// Streaming returns the house cap set for a listener that streams or hijacks:
// same caps as Default but with the whole-request read/write deadlines dropped.
func Streaming() Caps {
	c := Default()
	c.Streaming = true
	return c
}

// effReadWrite returns the read/write timeouts to actually apply, honouring the
// Streaming carve-out.
func (c Caps) effReadWrite() (read, write time.Duration) {
	if c.Streaming {
		return 0, 0
	}
	return c.ReadTimeout, c.WriteTimeout
}

// NewServer builds an *http.Server with this cap set applied. Use Apply for a
// server the caller must construct itself (e.g. one that needs a BaseContext).
func (c Caps) NewServer(h http.Handler) *http.Server {
	return c.Apply(&http.Server{Handler: h})
}

// Apply stamps the timeout + header bounds onto an already-constructed server
// (so a caller that needs BaseContext/ConnState can still build the struct) and
// returns it. It only sets the cap fields; Handler/Addr/BaseContext are left as
// the caller set them.
func (c Caps) Apply(s *http.Server) *http.Server {
	read, write := c.effReadWrite()
	s.ReadHeaderTimeout = c.ReadHeaderTimeout
	s.ReadTimeout = read
	s.WriteTimeout = write
	s.IdleTimeout = c.IdleTimeout
	if c.MaxHeaderBytes > 0 {
		s.MaxHeaderBytes = c.MaxHeaderBytes
	}
	return s
}

// LimitListener wraps ln so at most MaxConns connections are served
// concurrently; the (MaxConns+1)th Accept blocks until one closes. A
// non-positive MaxConns returns ln unwrapped (no cap) — Default()/Streaming()
// never produce that, so it only happens for a caller that deliberately zeroed
// it. Addr() passes through, so a :0 bind stays observable.
func (c Caps) LimitListener(ln net.Listener) net.Listener {
	if c.MaxConns <= 0 {
		return ln
	}
	return netutil.LimitListener(ln, c.MaxConns)
}

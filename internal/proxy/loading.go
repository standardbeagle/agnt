package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// loadingPageHTML is a self-contained HTML page shown when the upstream server
// is not yet reachable. It displays a "waiting for server" message with a live
// timer and auto-refreshes every 3 seconds so the browser seamlessly picks up
// the real content once the server starts.
const loadingPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="3">
<title>Waiting for server...</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{--bg:#f8f9fa;--fg:#1a1a2e;--muted:#6c757d;--card:#fff;--border:#dee2e6;--accent:#4361ee}
@media(prefers-color-scheme:dark){
:root{--bg:#1a1a2e;--fg:#e8e8e8;--muted:#8a8a9a;--card:#16213e;--border:#2a2a4a;--accent:#4cc9f0}
}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
background:var(--bg);color:var(--fg);display:flex;align-items:center;
justify-content:center;min-height:100vh}
.panel{background:var(--card);border:1px solid var(--border);border-radius:12px;
padding:2.5rem;max-width:480px;width:90%%;text-align:center}
.spinner{width:40px;height:40px;border:3px solid var(--border);
border-top-color:var(--accent);border-radius:50%%;
animation:spin 1s linear infinite;margin:0 auto 1.5rem}
@keyframes spin{to{transform:rotate(360deg)}}
h1{font-size:1.25rem;margin-bottom:.5rem}
.target{color:var(--accent);font-family:monospace;font-size:.95rem;
word-break:break-all;margin-bottom:1rem}
.timer{font-size:2rem;font-variant-numeric:tabular-nums;
font-weight:600;color:var(--fg);margin-bottom:.75rem}
.hint{color:var(--muted);font-size:.85rem;line-height:1.5}
.history{margin-top:1rem;text-align:left;max-height:150px;overflow-y:auto;
font-family:monospace;font-size:.8rem;color:var(--muted);line-height:1.6;
background:var(--bg);border-radius:6px;padding:.5rem .75rem}
.history-title{font-size:.75rem;text-transform:uppercase;letter-spacing:.05em;
margin-bottom:.25rem;color:var(--muted)}
</style>
</head>
<body>
<div class="panel">
<div class="spinner"></div>
<h1>Waiting for server</h1>
<div class="target">%s</div>
<div class="timer" id="timer">%s</div>
<p class="hint">The page will refresh automatically when the server is ready.</p>
%s
</div>
<script>
(function(){
var start=%d;
var el=document.getElementById("timer");
function pad(n){return n<10?"0"+n:""+n}
function update(){
var s=Math.floor((Date.now()/1000)-start);
var m=Math.floor(s/60);s=s%%60;
el.textContent=pad(m)+":"+pad(s);
}
update();setInterval(update,1000);
})();
</script>
</body>
</html>`

// acceptsHTML returns true if the request's Accept header includes text/html.
// This distinguishes browser navigation from API/XHR requests so we only serve
// the loading page to actual page loads.
func acceptsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// maxLoadingWait is how long the loading page auto-refreshes before showing a permanent error.
const maxLoadingWait = 60 * time.Second

// maxConnAttempts is the max number of connection attempts to retain.
const maxConnAttempts = 20

// ConnAttempt records a failed connection attempt for the loading page.
type ConnAttempt struct {
	Timestamp time.Time
	Event     string // "connection_refused", "timeout", "reset", etc.
	Message   string
}

// recordConnAttempt adds a connection attempt to the log.
func (ps *ProxyServer) recordConnAttempt(event, message string) {
	ps.connAttemptsMu.Lock()
	defer ps.connAttemptsMu.Unlock()
	ps.connAttempts = append(ps.connAttempts, ConnAttempt{
		Timestamp: time.Now(),
		Event:     event,
		Message:   message,
	})
	if len(ps.connAttempts) > maxConnAttempts {
		ps.connAttempts = ps.connAttempts[len(ps.connAttempts)-maxConnAttempts:]
	}
}

// getConnAttempts returns a copy of recent connection attempts.
func (ps *ProxyServer) getConnAttempts() []ConnAttempt {
	ps.connAttemptsMu.Lock()
	defer ps.connAttemptsMu.Unlock()
	result := make([]ConnAttempt, len(ps.connAttempts))
	copy(result, ps.connAttempts)
	return result
}

// serveLoadingPage writes the loading page HTML as an HTTP response.
// After maxLoadingWait, it shows a permanent error page instead of auto-refreshing.
func (ps *ProxyServer) serveLoadingPage(w http.ResponseWriter, _ *http.Request, targetURL string) {
	elapsed := time.Since(ps.startTime)

	if elapsed > maxLoadingWait {
		ps.serveErrorPage(w, targetURL, elapsed)
		return
	}

	mins := int(elapsed.Minutes())
	secs := int(elapsed.Seconds()) % 60
	timerStr := fmt.Sprintf("%02d:%02d", mins, secs)
	startUnix := ps.startTime.Unix()

	// Build connection history HTML
	historyHTML := ""
	attempts := ps.getConnAttempts()
	if len(attempts) > 0 {
		var lines []string
		for _, a := range attempts {
			ts := a.Timestamp.Format("15:04:05")
			lines = append(lines, fmt.Sprintf("%s  %s", ts, a.Event))
		}
		historyHTML = fmt.Sprintf(`<div class="history"><div class="history-title">Connection Log (%d attempts)</div>%s</div>`,
			len(attempts), strings.Join(lines, "<br>"))
	}

	page := fmt.Sprintf(loadingPageHTML, targetURL, timerStr, historyHTML, startUnix)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Retry-After", "3")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(page))
}

// errorPageHTML is shown when the upstream server hasn't started after maxLoadingWait.
const errorPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Server not responding</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{--bg:#f8f9fa;--fg:#1a1a2e;--muted:#6c757d;--card:#fff;--border:#dee2e6;--err:#e63946}
@media(prefers-color-scheme:dark){
:root{--bg:#1a1a2e;--fg:#e8e8e8;--muted:#8a8a9a;--card:#16213e;--border:#2a2a4a;--err:#ff6b6b}
}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
background:var(--bg);color:var(--fg);display:flex;align-items:center;
justify-content:center;min-height:100vh}
.panel{background:var(--card);border:1px solid var(--border);border-radius:12px;
padding:2.5rem;max-width:520px;width:90%%;text-align:center}
.icon{font-size:2.5rem;margin-bottom:1rem}
h1{font-size:1.25rem;color:var(--err);margin-bottom:.5rem}
.target{font-family:monospace;font-size:.95rem;word-break:break-all;margin-bottom:1rem;
color:var(--muted)}
.msg{color:var(--fg);font-size:.95rem;line-height:1.6;margin-bottom:1.5rem;text-align:left}
.msg code{background:var(--bg);padding:.15em .4em;border-radius:4px;font-size:.9em}
.retry{display:inline-block;padding:.6rem 1.5rem;background:var(--err);color:#fff;
border:none;border-radius:6px;font-size:.95rem;cursor:pointer;text-decoration:none}
.retry:hover{opacity:.9}
.elapsed{color:var(--muted);font-size:.8rem;margin-top:1rem}
</style>
</head>
<body>
<div class="panel">
<div class="icon">&#x26A0;</div>
<h1>Server not responding</h1>
<div class="target">%s</div>
<div class="msg">
The upstream server at this address did not become reachable after %s.<br><br>
Check that:<br>
&bull; The server process is running<br>
&bull; It is listening on the expected port<br>
&bull; There are no startup errors in the process output
</div>
<a class="retry" href="javascript:location.reload()">Retry</a>
<div class="elapsed">Proxy has been waiting since startup (%s ago)</div>
</div>
</body>
</html>`

// serveErrorPage writes a permanent error page when the upstream never started.
func (ps *ProxyServer) serveErrorPage(w http.ResponseWriter, targetURL string, elapsed time.Duration) {
	mins := int(elapsed.Minutes())
	secs := int(elapsed.Seconds()) % 60
	elapsedStr := fmt.Sprintf("%dm%02ds", mins, secs)

	page := fmt.Sprintf(errorPageHTML, targetURL, elapsedStr, elapsedStr)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	w.Write([]byte(page))
}

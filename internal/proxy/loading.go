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
</style>
</head>
<body>
<div class="panel">
<div class="spinner"></div>
<h1>Waiting for server</h1>
<div class="target">%s</div>
<div class="timer" id="timer">%s</div>
<p class="hint">The page will refresh automatically when the server is ready.</p>
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

// serveLoadingPage writes the loading page HTML as an HTTP response.
// It uses the proxy's start time to show elapsed seconds.
func (ps *ProxyServer) serveLoadingPage(w http.ResponseWriter, targetURL string) {
	elapsed := time.Since(ps.startTime)
	mins := int(elapsed.Minutes())
	secs := int(elapsed.Seconds()) % 60
	timerStr := fmt.Sprintf("%02d:%02d", mins, secs)
	startUnix := ps.startTime.Unix()

	page := fmt.Sprintf(loadingPageHTML, targetURL, timerStr, startUnix)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Retry-After", "3")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(page))
}

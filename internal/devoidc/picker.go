package devoidc

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// pickerParams are the authorization request fields the picker form carries
// through to /pick. prompt is deliberately dropped: the pick is the prompt.
var pickerParams = []string{
	"client_id", "redirect_uri", "response_type", "scope", "state", "nonce",
	"code_challenge", "code_challenge_method",
}

type pickerOption struct {
	Name, Label, Email, Roles string
}

type pickerField struct{ Name, Value string }

var pickerTemplate = template.Must(template.New("picker").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Choose a persona · agnt dev issuer</title>
<style>
:root{color-scheme:light dark;--bg:#f8fafc;--card:#fff;--text:#0f172a;--muted:#64748b;--line:#e2e8f0;--accent:#4f46e5}
@media (prefers-color-scheme:dark){:root{--bg:#0b1120;--card:#111827;--text:#e5e7eb;--muted:#94a3b8;--line:#1f2937;--accent:#818cf8}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--text);font:15px/1.4 system-ui,sans-serif;padding:16px;box-sizing:border-box}
main{width:100%;max-width:420px}
h1{font-size:18px;margin:0 0 4px}
p{margin:0 0 16px;color:var(--muted)}
button{display:block;width:100%;text-align:left;background:var(--card);color:inherit;border:1px solid var(--line);border-radius:10px;padding:12px 14px;margin:0 0 10px;font:inherit;cursor:pointer}
button:hover,button:focus-visible{border-color:var(--accent);outline:none}
.name{font-weight:600}.meta{color:var(--muted);font-size:13px}
</style></head>
<body><main>
<h1>Sign in as</h1>
<p>agnt dev issuer: pick a persona. No password; these are test identities from .agnt.kdl.</p>
{{range .Options}}<form method="post" action="{{$.Action}}">
{{range $.Fields}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
{{end}}<button type="submit" name="persona" value="{{.Name}}"><span class="name">{{.Label}}</span><br><span class="meta">{{.Email}}{{if .Roles}} · {{.Roles}}{{end}}</span></button>
</form>
{{end}}</main></body></html>`))

func renderPicker(w http.ResponseWriter, cfg *Config, allowed []string, q url.Values) {
	var fields []pickerField
	for _, name := range pickerParams {
		if v := q.Get(name); v != "" {
			fields = append(fields, pickerField{name, v})
		}
	}
	options := make([]pickerOption, 0, len(allowed))
	for _, name := range allowed {
		p := cfg.Personas[name]
		label := p.DisplayName
		if label == "" {
			label = name
		}
		options = append(options, pickerOption{Name: name, Label: label, Email: p.Email, Roles: strings.Join(p.Roles, ", ")})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = pickerTemplate.Execute(w, map[string]any{"Action": Prefix + "/pick", "Fields": fields, "Options": options})
}

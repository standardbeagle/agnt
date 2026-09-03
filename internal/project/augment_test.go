package project

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func mustDetect(t *testing.T, dir string) *Project {
	t.Helper()
	p, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const trackCompose = `
services:
  db:
    ports: ["127.0.0.1:${TRACK_DB_PORT:-5432}:5432"]
  track-api:
    ports: ["127.0.0.1:${TRACK_PORT:-5000}:8080"]
  track-web:
    ports: ["127.0.0.1:${TRACK_WEB_PORT:-5173}:80"]
`

// A solution-only root with a compose file (the Track shape): dotnet stays
// the primary type for test/build, compose supplies the dev topology, and
// the nested app scan yields to compose so nothing double-starts.
func TestDetect_ComposeAccumulatesOntoDotnet(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"Track.slnx":                                   "<Solution/>",
		"docker-compose.yml":                           trackCompose,
		"src/Track.Api/Track.Api.csproj":               `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"src/Track.Api/Properties/launchSettings.json": `{"profiles":{"http":{"applicationUrl":"http://localhost:5000"}}}`,
	})
	p := mustDetect(t, dir)
	if p.Type != ProjectDotnet {
		t.Fatalf("type = %s, want dotnet", p.Type)
	}
	if p.Metadata["compose"] != "docker-compose.yml" {
		t.Fatalf("compose metadata = %q", p.Metadata["compose"])
	}
	if len(p.Servers) != 1 || p.Servers[0].Run != "docker compose up" || p.Servers[0].Proxy {
		t.Fatalf("servers = %+v, want the single compose orchestration script without a proxy", p.Servers)
	}
	want := []PortProxy{{"db", 5432}, {"track-api", 5000}, {"track-web", 5173}}
	if len(p.Proxies) != len(want) {
		t.Fatalf("proxies = %+v, want %+v", p.Proxies, want)
	}
	for i := range want {
		if p.Proxies[i] != want[i] {
			t.Errorf("proxy %d = %+v, want %+v (sorted by service name)", i, p.Proxies[i], want[i])
		}
	}
}

func TestDetect_ComposeOnlyRoot(t *testing.T) {
	dir := writeTree(t, map[string]string{"compose.yaml": trackCompose})
	p := mustDetect(t, dir)
	if p.Type != ProjectCompose {
		t.Fatalf("type = %s, want compose", p.Type)
	}
	if len(p.Proxies) != 3 || len(p.Servers) != 1 {
		t.Fatalf("servers=%+v proxies=%+v", p.Servers, p.Proxies)
	}
}

func TestDetect_NestedDotnetWebProjects(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"Track.slnx":                                   "<Solution/>",
		"src/Track.Api/Track.Api.csproj":               `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"src/Track.Api/Properties/launchSettings.json": `{"profiles":{"https":{"applicationUrl":"https://localhost:7001;http://localhost:5000"}}}`,
		"src/Track.Core/Track.Core.csproj":             `<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		"src/Track.Web/Track.Web.csproj":               `<Project Sdk="Microsoft.NET.Sdk.BlazorWebAssembly"></Project>`,
	})
	p := mustDetect(t, dir)
	if len(p.Servers) != 2 {
		t.Fatalf("servers = %+v, want api + web (library skipped)", p.Servers)
	}
	api, web := p.Servers[0], p.Servers[1]
	if api.Name != "api" || api.Cwd != "src/Track.Api" || api.Port != 5000 || api.Run != "dotnet watch run" || !api.Proxy {
		t.Errorf("api server = %+v", api)
	}
	if web.Name != "web" || web.Cwd != "src/Track.Web" || web.Port != 0 {
		t.Errorf("web server = %+v (no launchSettings → port unknown)", web)
	}
}

func TestDetect_RootCsprojDoesNotScanNested(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"Site.csproj":          `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"tools/Gen/Gen.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
	})
	p := mustDetect(t, dir)
	if len(p.Servers) != 0 {
		t.Fatalf("a root project is the app; nested scan must not run: %+v", p.Servers)
	}
}

func TestDetect_Procfile(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"Procfile": "web: bundle exec puma -C config/puma.rb\nworker: bundle exec sidekiq\nrelease: rake db:migrate\n# comment\n",
	})
	p := mustDetect(t, dir)
	if p.Type != ProjectUnknown || p.Metadata["procfile"] != "Procfile" {
		t.Fatalf("type=%s procfile=%q", p.Type, p.Metadata["procfile"])
	}
	if len(p.Servers) != 2 {
		t.Fatalf("servers = %+v, want web + worker (release skipped)", p.Servers)
	}
	if !p.Servers[0].Proxy || p.Servers[0].Name != "web" || p.Servers[1].Proxy {
		t.Errorf("only web gets a proxy: %+v", p.Servers)
	}

	// Procfile.dev wins over Procfile; a framework server suppresses both.
	dir = writeTree(t, map[string]string{
		"Procfile":     "web: foreman-prod\n",
		"Procfile.dev": "web: bin/rails server\ncss: yarn watch:css\n",
	})
	p = mustDetect(t, dir)
	if p.Metadata["procfile"] != "Procfile.dev" || p.Servers[0].Run != "bin/rails server" {
		t.Errorf("Procfile.dev must take precedence: %+v", p.Servers)
	}
	dir = writeTree(t, map[string]string{"manage.py": "", "requirements.txt": "django", "Procfile": "web: gunicorn app\n"})
	p = mustDetect(t, dir)
	if len(p.Servers) != 1 || p.Servers[0].Run != "python manage.py runserver" {
		t.Errorf("framework server wins over Procfile: %+v", p.Servers)
	}
}

func TestDetect_Django(t *testing.T) {
	dir := writeTree(t, map[string]string{"manage.py": "", "requirements.txt": "django"})
	p := mustDetect(t, dir)
	if p.Type != ProjectPython || p.Metadata["framework"] != "django" {
		t.Fatalf("type=%s framework=%q", p.Type, p.Metadata["framework"])
	}
	if len(p.Servers) != 1 || p.Servers[0].Port != 8000 || !p.Servers[0].Proxy {
		t.Fatalf("servers = %+v", p.Servers)
	}
}

func TestDetect_Ruby(t *testing.T) {
	t.Run("rails with bin/dev", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"Gemfile": "gem 'rails'", "bin/rails": "", "bin/dev": "", "Procfile.dev": "web: bin/rails s\n", "spec/x_spec.rb": "", ".rubocop.yml": ""})
		p := mustDetect(t, dir)
		if p.Type != ProjectRuby || p.Metadata["framework"] != "rails" || p.Metadata["linter"] != "rubocop" {
			t.Fatalf("type=%s meta=%v", p.Type, p.Metadata)
		}
		if p.Servers[0].Run != "bin/dev" || p.Servers[0].Port != 3000 {
			t.Errorf("server = %+v", p.Servers[0])
		}
		if c := GetCommandByName(p, "test"); c == nil || c.Args[1] != "rspec" {
			t.Errorf("spec/ selects rspec: %+v", c)
		}
		if len(p.Servers) != 1 {
			t.Errorf("Procfile.dev must not add a second web server: %+v", p.Servers)
		}
	})
	t.Run("rails plain", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"Gemfile": "gem 'rails'", "config/application.rb": ""})
		p := mustDetect(t, dir)
		if p.Servers[0].Run != "bin/rails server" {
			t.Errorf("server = %+v", p.Servers[0])
		}
		if c := GetCommandByName(p, "test"); c == nil || c.Command != "bin/rails" {
			t.Errorf("no spec/ → bin/rails test: %+v", c)
		}
	})
	t.Run("jekyll", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"Gemfile": "gem \"jekyll\"", "_config.yml": "title: x"})
		p := mustDetect(t, dir)
		if p.Metadata["framework"] != "jekyll" || p.Servers[0].Port != 4000 || p.Servers[0].Run != "bundle exec jekyll serve" {
			t.Errorf("jekyll: meta=%v servers=%+v", p.Metadata, p.Servers)
		}
	})
	t.Run("gem", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"Gemfile": "gem 'rake'"})
		p := mustDetect(t, dir)
		if len(p.Servers) != 0 || GetCommandByName(p, "test") == nil {
			t.Errorf("plain gem: servers=%+v", p.Servers)
		}
	})
}

func TestDetect_PHP(t *testing.T) {
	t.Run("laravel 11 with composer dev", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"composer.json": `{"require-dev":{"laravel/pint":"^1"},"scripts":{"dev":"npx concurrently ..."}}`, "artisan": ""})
		p := mustDetect(t, dir)
		if p.Type != ProjectPHP || p.Metadata["framework"] != "laravel" {
			t.Fatalf("type=%s meta=%v", p.Type, p.Metadata)
		}
		if p.Servers[0].Run != "composer run dev" || p.Servers[0].Port != 8000 {
			t.Errorf("server = %+v", p.Servers[0])
		}
		if GetCommandByName(p, "lint") == nil {
			t.Error("pint dependency → lint command")
		}
	})
	t.Run("laravel classic", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"composer.json": `{}`, "artisan": ""})
		p := mustDetect(t, dir)
		if p.Servers[0].Run != "php artisan serve" || GetCommandByName(p, "lint") != nil {
			t.Errorf("server=%+v lint=%v", p.Servers[0], GetCommandByName(p, "lint"))
		}
	})
	t.Run("composer library", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"composer.json": `{}`})
		p := mustDetect(t, dir)
		if len(p.Servers) != 0 || GetCommandByName(p, "test") == nil {
			t.Errorf("library: servers=%+v", p.Servers)
		}
	})
}

func TestDetect_Elixir(t *testing.T) {
	dir := writeTree(t, map[string]string{"mix.exs": `defp deps do [{:phoenix, "~> 1.7"}] end`})
	p := mustDetect(t, dir)
	if p.Type != ProjectElixir || p.Metadata["framework"] != "phoenix" || p.Servers[0].Run != "mix phx.server" || p.Servers[0].Port != 4000 {
		t.Fatalf("type=%s meta=%v servers=%+v", p.Type, p.Metadata, p.Servers)
	}
	dir = writeTree(t, map[string]string{"mix.exs": `defp deps do [{:jason, "~> 1.0"}] end`})
	p = mustDetect(t, dir)
	if len(p.Servers) != 0 || GetCommandByName(p, "test") == nil {
		t.Errorf("plain mix: servers=%+v", p.Servers)
	}
}

func TestDetect_Hugo(t *testing.T) {
	dir := writeTree(t, map[string]string{"hugo.toml": "baseURL = '/'", "content/_index.md": ""})
	p := mustDetect(t, dir)
	if p.Type != ProjectHugo || p.Servers[0].Run != "hugo server" || p.Servers[0].Port != 1313 {
		t.Fatalf("type=%s servers=%+v", p.Type, p.Servers)
	}
	// config.toml alone is not Hugo; with layouts/ it is.
	dir = writeTree(t, map[string]string{"config.toml": "", "content/x.md": ""})
	if p = mustDetect(t, dir); len(p.Servers) != 0 {
		t.Errorf("config.toml + content only must not detect hugo: %+v", p.Servers)
	}
	dir = writeTree(t, map[string]string{"config.toml": "", "content/x.md": "", "layouts/index.html": ""})
	if p = mustDetect(t, dir); len(p.Servers) != 1 {
		t.Errorf("config.toml + content + layouts is hugo: %+v", p.Servers)
	}
	// A Node root (tailwind for the theme) keeps its type; Hugo adds the server.
	dir = writeTree(t, map[string]string{"package.json": `{"name":"site"}`, "hugo.yaml": "", "content/x.md": ""})
	p = mustDetect(t, dir)
	if p.Type != ProjectNode || len(p.Servers) != 1 || p.Servers[0].Run != "hugo server" {
		t.Errorf("node + hugo: type=%s servers=%+v", p.Type, p.Servers)
	}
}

func TestDetect_Mkdocs(t *testing.T) {
	dir := writeTree(t, map[string]string{"mkdocs.yml": "site_name: x"})
	p := mustDetect(t, dir)
	if p.Type != ProjectMkdocs || p.Servers[0].Name != "docs" || p.Servers[0].Port != 8000 {
		t.Fatalf("type=%s servers=%+v", p.Type, p.Servers)
	}
	// A Python library with docs: python stays primary, docs server added.
	dir = writeTree(t, map[string]string{"pyproject.toml": "[project]\nname='lib'", "mkdocs.yml": ""})
	p = mustDetect(t, dir)
	if p.Type != ProjectPython || len(p.Servers) != 1 || p.Servers[0].Run != "mkdocs serve" {
		t.Errorf("python + mkdocs: type=%s servers=%+v", p.Type, p.Servers)
	}
	// Django beats the docs server.
	dir = writeTree(t, map[string]string{"pyproject.toml": "", "manage.py": "", "mkdocs.yml": ""})
	p = mustDetect(t, dir)
	if len(p.Servers) != 1 || p.Servers[0].Run != "python manage.py runserver" {
		t.Errorf("django + mkdocs: servers=%+v", p.Servers)
	}
}

func TestLaunchSettingsPort(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.json": `{"profiles":{"z":{"applicationUrl":"https://localhost:7001"},"a":{"applicationUrl":"https://localhost:7002;http://localhost:5002"}}}`,
		"b.json": `not json`,
	})
	if got := launchSettingsPort(filepath.Join(dir, "a.json")); got != 5002 {
		t.Errorf("got %d, want first http port in profile-name order", got)
	}
	if got := launchSettingsPort(filepath.Join(dir, "b.json")); got != 0 {
		t.Errorf("malformed → 0, got %d", got)
	}
	if got := launchSettingsPort(filepath.Join(dir, "missing.json")); got != 0 {
		t.Errorf("missing → 0, got %d", got)
	}
}

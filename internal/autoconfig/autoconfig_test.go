package autoconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseGenerated asserts the generated KDL is valid .agnt.kdl and returns it.
func parseGenerated(t *testing.T, kdl string) *config.AgntConfig {
	t.Helper()
	cfg, err := config.ParseAgntConfig(kdl)
	require.NoError(t, err, "generated KDL must parse:\n%s", kdl)
	return cfg
}

func TestGenerate_NodeWebApp(t *testing.T) {
	p := &project.Project{
		Type:           project.ProjectNode,
		Name:           "web",
		PackageManager: "npm",
		Commands:       project.DefaultNodeCommands("npm"),
		Metadata:       map[string]string{"scripts": "dev,test,lint,build"},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	// dev server autostarts; lint/test are on-demand.
	require.Contains(t, cfg.Scripts, "dev")
	assert.True(t, cfg.Scripts["dev"].Autostart, "dev server must autostart")
	assert.Equal(t, "npm run dev", cfg.Scripts["dev"].Run)
	require.Contains(t, cfg.Scripts, "test")
	assert.False(t, cfg.Scripts["test"].Autostart, "test is on-demand")
	require.Contains(t, cfg.Scripts, "lint")
	// A proxy fronts the dev server, linked to the dev script.
	require.Contains(t, cfg.Proxies, "dev")
	assert.Equal(t, "dev", cfg.Proxies["dev"].Script)
}

func TestGenerate_NodeLibrary_NoDevScript_NoProxy(t *testing.T) {
	// A library: package.json has test/lint but no dev/start script.
	p := &project.Project{
		Type:           project.ProjectNode,
		Name:           "lib",
		PackageManager: "pnpm",
		Commands:       project.DefaultNodeCommands("pnpm"),
		Metadata:       map[string]string{"scripts": "test,lint,build"},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	assert.NotContains(t, cfg.Scripts, "dev", "no dev script declared → no dev server")
	assert.Empty(t, cfg.Proxies, "a library gets no proxy")
	assert.Contains(t, cfg.Scripts, "test")
	assert.Contains(t, cfg.Scripts, "lint")
}

func TestGenerate_Dotnet(t *testing.T) {
	p := &project.Project{
		Type:     project.ProjectDotnet,
		Name:     "Site",
		Commands: project.DefaultDotnetCommands(),
		Metadata: map[string]string{"project": "Site.csproj"},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	require.Contains(t, cfg.Scripts, "dev")
	assert.Equal(t, "dotnet watch run", cfg.Scripts["dev"].Run)
	assert.True(t, cfg.Scripts["dev"].Autostart)
	require.Contains(t, cfg.Proxies, "dev", "dotnet site gets a proxy")
}

// A solution-only root (projects under src/) cannot run `dotnet watch run`
// from the cwd, so the generated config must not autostart a script that is
// known to fail; test/build stay registered.
func TestGenerate_DotnetSolutionOnlyRoot_NoDevScript(t *testing.T) {
	p := &project.Project{
		Type:     project.ProjectDotnet,
		Name:     "Track",
		Commands: project.DefaultDotnetCommands(),
		Metadata: map[string]string{"solution": "Track.slnx"},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	assert.NotContains(t, cfg.Scripts, "dev")
	assert.Empty(t, cfg.Proxies)
	assert.Contains(t, cfg.Scripts, "test")
	assert.Contains(t, cfg.Scripts, "build")
}

func TestGenerate_Go_NoProxyButScripts(t *testing.T) {
	p := &project.Project{
		Type:     project.ProjectGo,
		Name:     "tool",
		Commands: project.DefaultGoCommands(),
		Metadata: map[string]string{},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	// Plain Go: no assumed server → no proxy, no autostart, but test/lint wired.
	assert.Empty(t, cfg.Proxies, "plain Go gets no proxy (run is not assumed a server)")
	assert.Contains(t, cfg.Scripts, "test")
	assert.Contains(t, cfg.Scripts, "lint")
	assert.Equal(t, "go test -v ./...", cfg.Scripts["test"].Run)
	for name, sc := range cfg.Scripts {
		assert.False(t, sc.Autostart, "no script should autostart for plain Go (%s)", name)
	}
}

func TestGenerate_Wails_HasProxy(t *testing.T) {
	p := &project.Project{
		Type:     project.ProjectGo,
		Name:     "desktop",
		Commands: project.DefaultWailsCommands(),
		Metadata: map[string]string{"framework": "wails"},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	require.Contains(t, cfg.Scripts, "dev")
	assert.True(t, cfg.Scripts["dev"].Autostart)
	assert.Contains(t, cfg.Proxies, "dev", "wails dev serves a frontend → proxy")
}

func TestGenerate_UnknownAndEmpty(t *testing.T) {
	_, ok := Generate(nil)
	assert.False(t, ok)

	_, ok = Generate(&project.Project{Type: project.ProjectUnknown})
	assert.False(t, ok)

	// Known type but no useful commands → not confident.
	_, ok = Generate(&project.Project{Type: project.ProjectNode, Metadata: map[string]string{}})
	assert.False(t, ok)
}

func TestGenerate_RootIndexHTMLWithoutBuildSignal(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0o644))

	kdl, ok := Generate(&project.Project{
		Path: dir,
		Type: project.ProjectUnknown,
		Name: "landing-page",
	})
	require.True(t, ok, "a root index.html with no build signal is a static web project")

	cfg := parseGenerated(t, kdl)
	require.Contains(t, cfg.Scripts, "dev")
	assert.True(t, cfg.Scripts["dev"].Autostart)
	assert.Contains(t, cfg.Scripts["dev"].Run, "ThreadingHTTPServer")
	assert.Contains(t, cfg.Scripts["dev"].Run, "http://localhost:8000")
	require.Contains(t, cfg.Proxies, "dev")
	assert.Equal(t, "dev", cfg.Proxies["dev"].Script)
	assert.Equal(t, 8000, cfg.Proxies["dev"].FallbackPort)
}

func TestGenerate_HeaderIsCommentedAndParses(t *testing.T) {
	p := &project.Project{
		Type:     project.ProjectDotnet,
		Name:     "App",
		Commands: project.DefaultDotnetCommands(),
		Metadata: map[string]string{},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(kdl, "//"), "starts with an editable header comment")
	parseGenerated(t, kdl) // header must not break parsing
}

func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return dir
}

func detectAndGenerate(t *testing.T, dir string) *config.AgntConfig {
	t.Helper()
	p, err := project.Detect(dir)
	require.NoError(t, err)
	kdl, ok := Generate(p)
	require.True(t, ok, "expected a generated config for %s", p.Type)
	return parseGenerated(t, kdl)
}

// The Track shape: solution-only dotnet root with docker-compose. The
// generated config autostarts compose and fronts each published service
// with a fixed-port proxy, and keeps dotnet test/build on demand.
func TestGenerate_ComposeOnDotnetRoot(t *testing.T) {
	cfg := detectAndGenerate(t, writeFixture(t, map[string]string{
		"Track.slnx": "<Solution/>",
		"docker-compose.yml": `
services:
  db:
    ports: ["127.0.0.1:${TRACK_DB_PORT:-5432}:5432"]
  track-api:
    ports: ["127.0.0.1:${TRACK_PORT:-5000}:8080"]
  track-web:
    ports: ["127.0.0.1:${TRACK_WEB_PORT:-5173}:80"]
`,
	}))
	require.Contains(t, cfg.Scripts, "compose")
	assert.Equal(t, "docker compose up", cfg.Scripts["compose"].Run)
	assert.True(t, cfg.Scripts["compose"].Autostart)
	assert.NotContains(t, cfg.Scripts, "dev", "no dotnet watch run at a solution-only root")
	assert.Contains(t, cfg.Scripts, "test")
	assert.Contains(t, cfg.Scripts, "build")
	require.Len(t, cfg.Proxies, 3)
	assert.Equal(t, 5173, cfg.Proxies["track-web"].Port)
	assert.Equal(t, 5000, cfg.Proxies["track-api"].Port)
	assert.Equal(t, 5432, cfg.Proxies["db"].Port)
	assert.Empty(t, cfg.Proxies["track-web"].Script, "fixed-port proxies are not script-linked")
}

func TestGenerate_NestedDotnetApps(t *testing.T) {
	cfg := detectAndGenerate(t, writeFixture(t, map[string]string{
		"Track.slnx":                                   "<Solution/>",
		"src/Track.Api/Track.Api.csproj":               `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		"src/Track.Api/Properties/launchSettings.json": `{"profiles":{"http":{"applicationUrl":"http://localhost:5000"}}}`,
	}))
	require.Contains(t, cfg.Scripts, "api")
	assert.Equal(t, "dotnet watch run", cfg.Scripts["api"].Run)
	assert.Equal(t, "src/Track.Api", cfg.Scripts["api"].Cwd)
	assert.Equal(t, []int{5000}, cfg.Scripts["api"].Ports)
	assert.True(t, cfg.Scripts["api"].Autostart)
	require.Contains(t, cfg.Proxies, "api")
	assert.Equal(t, "api", cfg.Proxies["api"].Script)
	assert.Equal(t, 5000, cfg.Proxies["api"].FallbackPort)
}

func TestGenerate_FrameworkDefaultsCarryFallbackPort(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		run   string
		port  int
	}{
		{"django", map[string]string{"manage.py": "", "requirements.txt": ""}, "python manage.py runserver", 8000},
		{"rails", map[string]string{"Gemfile": "gem 'rails'", "bin/rails": ""}, "bin/rails server", 3000},
		{"laravel", map[string]string{"composer.json": "{}", "artisan": ""}, "php artisan serve", 8000},
		{"phoenix", map[string]string{"mix.exs": "{:phoenix, \"~> 1.7\"}"}, "mix phx.server", 4000},
		{"hugo", map[string]string{"hugo.toml": "", "content/_index.md": ""}, "hugo server", 1313},
		{"jekyll", map[string]string{"Gemfile": "gem 'jekyll'", "_config.yml": ""}, "bundle exec jekyll serve", 4000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := detectAndGenerate(t, writeFixture(t, tc.files))
			require.Contains(t, cfg.Scripts, "dev")
			assert.Equal(t, tc.run, cfg.Scripts["dev"].Run)
			assert.True(t, cfg.Scripts["dev"].Autostart)
			require.Contains(t, cfg.Proxies, "dev")
			assert.Equal(t, "dev", cfg.Proxies["dev"].Script)
			assert.Equal(t, tc.port, cfg.Proxies["dev"].FallbackPort)
		})
	}
	cfg := detectAndGenerate(t, writeFixture(t, map[string]string{"mkdocs.yml": ""}))
	assert.Equal(t, 8000, cfg.Proxies["docs"].FallbackPort)
}

// A Procfile entry named like an on-demand command keeps its name; the
// worker autostarts without a proxy; only web is proxied (port unknown,
// left to URL detection).
func TestGenerate_ProcfileEntries(t *testing.T) {
	cfg := detectAndGenerate(t, writeFixture(t, map[string]string{
		"go.mod":   "module x\n",
		"Procfile": "web: go run ./cmd/web\nworker: go run ./cmd/worker\ntest: watchexec go test ./...\n",
	}))
	assert.Equal(t, "go run ./cmd/web", cfg.Scripts["web"].Run)
	assert.True(t, cfg.Scripts["worker"].Autostart)
	assert.Equal(t, "watchexec go test ./...", cfg.Scripts["test"].Run, "Procfile's test entry wins over the Go default")
	require.Len(t, cfg.Proxies, 1)
	assert.Equal(t, "web", cfg.Proxies["web"].Script)
	assert.Zero(t, cfg.Proxies["web"].FallbackPort)
}

// The Track shape: a solution root whose compose file already publishes the
// ports its own apps bind. Compose autostarts; the apps are configured with
// the working directory that makes them runnable, and left switched off.
func TestGenerate_ManualAppsAreConfiguredButNotAutostarted(t *testing.T) {
	p := &project.Project{
		Type:     project.ProjectDotnet,
		Name:     "Track",
		Commands: project.DefaultDotnetCommands(),
		Metadata: map[string]string{"solution": "Track.slnx", "compose": "docker-compose.yml"},
		Servers: []project.Server{
			{Name: "compose", Run: "docker compose up"},
			{Name: "api", Run: "dotnet watch run", Cwd: "src/Track.Api", Port: 5000, Proxy: true, Manual: true},
			{Name: "web", Run: "pnpm dev", Cwd: "src/Track.Web", Proxy: true, Manual: true},
		},
		Proxies: []project.PortProxy{{Name: "track-api", Port: 5000}},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	assert.True(t, cfg.Scripts["compose"].Autostart, "the declared topology autostarts")

	require.Contains(t, cfg.Scripts, "api")
	assert.False(t, cfg.Scripts["api"].Autostart, "the app must not fight compose for port 5000")
	assert.Equal(t, "src/Track.Api", cfg.Scripts["api"].Cwd, "the command needs its own directory to run at all")
	assert.Equal(t, []int{5000}, cfg.Scripts["api"].Ports)
	assert.False(t, cfg.Scripts["web"].Autostart)
	assert.Equal(t, "src/Track.Web", cfg.Scripts["web"].Cwd)

	// The file says why they are off, so the config does not read as broken.
	assert.Contains(t, kdl, "docker-compose.yml already serves their ports")

	// Each app keeps its proxy, created when the developer turns the script on.
	assert.Equal(t, "api", cfg.Proxies["api"].Script)
	assert.Equal(t, 5000, cfg.Proxies["api"].FallbackPort)
	assert.Equal(t, 5000, cfg.Proxies["track-api"].Port, "the compose service keeps its own port proxy")
}

// A dotnet solution root with no compose file: the nested app is the dev
// server, so it autostarts. Guards the regression that started this work —
// `dotnet watch run` generated at a root that holds no project file.
func TestGenerate_NestedAppAutostartsWithItsOwnDirectory(t *testing.T) {
	p := &project.Project{
		Type:     project.ProjectDotnet,
		Name:     "Track",
		Commands: project.DefaultDotnetCommands(),
		Metadata: map[string]string{"solution": "Track.slnx"},
		Servers:  []project.Server{{Name: "api", Run: "dotnet watch run", Cwd: "src/Track.Api", Port: 5000, Proxy: true}},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	require.Contains(t, cfg.Scripts, "api")
	assert.True(t, cfg.Scripts["api"].Autostart)
	assert.Equal(t, "src/Track.Api", cfg.Scripts["api"].Cwd, "without a cwd the command fails: no project file at the root")
	assert.NotContains(t, cfg.Scripts, "dev", "the solution root itself is not runnable")
}

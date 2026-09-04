package project

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// augment runs the cross-cutting detectors that add dev servers on top of
// the primary type. Order is precedence: compose is the declared dev
// topology and always wins; Procfile and the docs-site generators only fill
// in when nothing declared a server yet, so two sources never autostart the
// same port twice.
//
// The nested app scans are the exception. A repository can hold both a
// compose topology and the app sources it builds from, and a developer needs
// the per-app servers to run one locally. They are always detected, but when
// something already declared the topology they are marked Manual: written
// into the config with the right command, directory and port, and left off
// autostart so nothing double-binds a port compose already publishes.
func augment(p *Project) {
	if p.Metadata == nil {
		p.Metadata = make(map[string]string)
	}
	augmentCompose(p)
	augmentProcfile(p)
	augmentNestedApps(p)
	augmentHugo(p)
	augmentMkdocs(p)
}

// nestedAppRoots are the glob prefixes searched for per-app sources under a
// root that is a solution, a monorepo, or a compose topology. One level deep
// only: deeper nesting is a build-system concern, not a dev-server one.
var nestedAppRoots = []string{"*", "src/*", "apps/*", "services/*", "packages/*"}

// nestedMatches returns the paths matching fileName under every nested app
// root, sorted and de-duplicated (the globs overlap by design).
func nestedMatches(root, fileName string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, prefix := range nestedAppRoots {
		matches, _ := filepath.Glob(filepath.Join(root, prefix, fileName))
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	sort.Strings(out)
	return out
}

// augmentNestedApps finds the runnable applications inside a multi-project
// root. Whether they autostart depends on what came before: with a topology
// already declared they are registered Manual (see augment).
func augmentNestedApps(p *Project) {
	if !isMultiAppRoot(p) {
		return
	}
	manual := p.hasServer()
	before := len(p.Servers)
	augmentNestedDotnet(p)
	augmentNestedNode(p)
	if manual {
		for i := before; i < len(p.Servers); i++ {
			p.Servers[i].Manual = true
		}
	}
}

// composeFiles are the names `docker compose` reads by default, in its own
// precedence order.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// augmentCompose turns a compose file into one autostart script
// (`docker compose up`) plus one port proxy per service that publishes a
// host port. Services without a published port are not reachable from the
// host, so they get no proxy.
func augmentCompose(p *Project) {
	for _, name := range composeFiles {
		path := filepath.Join(p.Path, name)
		if !fileExists(path) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		ports, err := parseComposePorts(data)
		if err != nil {
			return
		}
		p.Metadata["compose"] = name
		if p.Type == ProjectUnknown {
			p.Type = ProjectCompose
		}
		p.Servers = append(p.Servers, Server{Name: "compose", Run: "docker compose up"})
		names := make([]string, 0, len(ports))
		for svc := range ports {
			names = append(names, svc)
		}
		sort.Strings(names)
		for _, svc := range names {
			p.Proxies = append(p.Proxies, PortProxy{Name: svc, Port: ports[svc]})
		}
		return
	}
}

var procfileLine = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.+)$`)

// augmentProcfile reads Procfile.dev (preferred: it is the local topology)
// or Procfile. Every entry becomes an autostart script; `web` is the HTTP
// server and gets a proxy, with the port left to URL detection since
// Procfile commands conventionally read $PORT. `release` is a deploy hook
// and is skipped.
func augmentProcfile(p *Project) {
	if p.hasServer() {
		return
	}
	for _, name := range []string{"Procfile.dev", "Procfile"} {
		path := filepath.Join(p.Path, name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var servers []Server
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			m := procfileLine.FindStringSubmatch(line)
			if m == nil || m[1] == "release" {
				continue
			}
			servers = append(servers, Server{Name: m[1], Run: m[2], Proxy: m[1] == "web"})
		}
		f.Close()
		if len(servers) > 0 {
			p.Metadata["procfile"] = name
			p.Servers = append(p.Servers, servers...)
			return
		}
	}
}

// isMultiAppRoot reports whether the root is a container of applications
// rather than an application itself. A Go module, a Python package or a Node
// app at the root owns its own dev server, and scanning beneath it would
// autostart a side artifact — a documentation site, an example app — as if it
// were the project.
func isMultiAppRoot(p *Project) bool {
	switch p.Type {
	case ProjectDotnet:
		// A root .csproj is the app; only a solution root holds others.
		return p.Metadata["project"] == ""
	case ProjectNode:
		// A workspace root declares no dev script of its own.
		return !rootPackageServes(p.Path)
	case ProjectCompose, ProjectUnknown:
		return true
	default:
		return false
	}
}

// augmentNestedDotnet handles a solution root: each nested web project becomes
// `dotnet watch run` in its own directory, with the port read from its
// launchSettings.json so the proxy exists before the first request. Running it
// from the root instead is the failure this exists to prevent — with no
// project file there, `dotnet watch` exits immediately.
func augmentNestedDotnet(p *Project) {
	if p.Type != ProjectDotnet {
		return
	}
	for _, csproj := range nestedMatches(p.Path, "*.csproj") {
		if !isDotnetWebProject(csproj) {
			continue
		}
		dir := filepath.Dir(csproj)
		rel, err := filepath.Rel(p.Path, dir)
		if err != nil {
			continue
		}
		p.Servers = append(p.Servers, Server{
			Name:  uniqueServerName(p, scriptNameFromProject(strings.TrimSuffix(filepath.Base(csproj), ".csproj"))),
			Run:   "dotnet watch run",
			Cwd:   filepath.ToSlash(rel),
			Port:  launchSettingsPort(filepath.Join(dir, "Properties", "launchSettings.json")),
			Proxy: true,
		})
	}
}

// augmentNestedNode finds front-ends and Node services inside a multi-project
// root: any nested package.json that declares a `dev` (or `start`) script
// becomes a server run with that directory's own package manager. A library
// package declares neither, so a monorepo's shared packages are skipped.
//
// Only a multi-app root is scanned (see isMultiAppRoot): a Next.js app with
// an apps/ directory owns its own dev script and must not grow a second,
// competing server.
func augmentNestedNode(p *Project) {
	for _, pkg := range nestedMatches(p.Path, "package.json") {
		dir := filepath.Dir(pkg)
		script := firstDeclaredScript(pkg, "dev", "start")
		if script == "" {
			continue
		}
		rel, err := filepath.Rel(p.Path, dir)
		if err != nil {
			continue
		}
		p.Servers = append(p.Servers, Server{
			Name:  uniqueServerName(p, scriptNameFromProject(filepath.Base(dir))),
			Run:   nodeRunCommand(detectPackageManager(dir), script),
			Cwd:   filepath.ToSlash(rel),
			Proxy: true,
		})
	}
}

// rootPackageServes reports whether the root package.json declares its own
// dev/start script, making the root the application.
func rootPackageServes(root string) bool {
	return firstDeclaredScript(filepath.Join(root, "package.json"), "dev", "start") != ""
}

// firstDeclaredScript returns the first of names that package.json declares
// with a non-empty command, or "" when the file declares none of them.
func firstDeclaredScript(packageJSON string, names ...string) string {
	data, err := os.ReadFile(packageJSON)
	if err != nil {
		return ""
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	for _, n := range names {
		if strings.TrimSpace(pkg.Scripts[n]) != "" {
			return n
		}
	}
	return ""
}

// nodeRunCommand renders "run the named package script" for a package
// manager. npm and bun need the `run` verb; pnpm and yarn take the script
// name directly.
func nodeRunCommand(packageManager, script string) string {
	switch packageManager {
	case "pnpm", "yarn":
		return packageManager + " " + script
	case "bun":
		return "bun run " + script
	default:
		return "npm run " + script
	}
}

// uniqueServerName keeps script names distinct: two nested apps can share a
// directory name (services/api and apps/api), and a duplicate name would make
// one script silently replace the other in the generated config.
func uniqueServerName(p *Project, name string) string {
	taken := make(map[string]bool, len(p.Servers))
	for _, s := range p.Servers {
		taken[s.Name] = true
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

// isDotnetWebProject reports whether the csproj uses a web SDK. Only the
// SDK attribute is consulted; a library or console project never binds.
func isDotnetWebProject(csproj string) bool {
	data, err := os.ReadFile(csproj)
	if err != nil {
		return false
	}
	head := string(data)
	return strings.Contains(head, `Sdk="Microsoft.NET.Sdk.Web"`) ||
		strings.Contains(head, `Sdk="Microsoft.NET.Sdk.BlazorWebAssembly"`)
}

// scriptNameFromProject derives a script name from a project name:
// "Track.Api" -> "api", "Track.Web" -> "web", "Track" -> "track".
func scriptNameFromProject(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

var urlPort = regexp.MustCompile(`^https?://[^:/]+:(\d+)`)

// launchSettingsPort returns the port of the first http applicationUrl in
// a launchSettings.json profile (https is skipped: the proxy targets plain
// http and dotnet's dev cert is not trusted by the proxy). 0 when absent.
func launchSettingsPort(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var ls struct {
		Profiles map[string]struct {
			ApplicationURL string `json:"applicationUrl"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &ls); err != nil {
		return 0
	}
	names := make([]string, 0, len(ls.Profiles))
	for n := range ls.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, u := range strings.Split(ls.Profiles[n].ApplicationURL, ";") {
			u = strings.TrimSpace(u)
			if !strings.HasPrefix(u, "http://") {
				continue
			}
			if m := urlPort.FindStringSubmatch(u); m != nil {
				if port := atoi(m[1]); port > 0 {
					return port
				}
			}
		}
	}
	return 0
}

// augmentHugo recognises a Hugo site by its config file plus a content
// directory. `hugo server` binds 1313.
func augmentHugo(p *Project) {
	if p.hasServer() {
		return
	}
	hasConfig := false
	for _, name := range []string{"hugo.toml", "hugo.yaml", "hugo.json", "config.toml", "config.yaml"} {
		if fileExists(filepath.Join(p.Path, name)) {
			hasConfig = true
			break
		}
	}
	if !hasConfig || !dirExists(filepath.Join(p.Path, "content")) {
		return
	}
	// config.toml/yaml alone is ambiguous; require a Hugo-shaped tree.
	if !fileExists(filepath.Join(p.Path, "hugo.toml")) && !fileExists(filepath.Join(p.Path, "hugo.yaml")) && !fileExists(filepath.Join(p.Path, "hugo.json")) {
		if !dirExists(filepath.Join(p.Path, "layouts")) && !dirExists(filepath.Join(p.Path, "themes")) && !dirExists(filepath.Join(p.Path, "archetypes")) {
			return
		}
	}
	p.Metadata["framework"] = "hugo"
	if p.Type == ProjectUnknown {
		p.Type = ProjectHugo
		p.Commands = DefaultHugoCommands()
	}
	p.Servers = append(p.Servers, Server{Name: "dev", Run: "hugo server", Port: 1313, Proxy: true})
}

// augmentMkdocs recognises an mkdocs site. On a Python library root the
// docs server is still worth a proxy when nothing else serves.
func augmentMkdocs(p *Project) {
	if p.hasServer() || !fileExists(filepath.Join(p.Path, "mkdocs.yml")) {
		return
	}
	p.Metadata["framework"] = "mkdocs"
	if p.Type == ProjectUnknown {
		p.Type = ProjectMkdocs
		p.Commands = DefaultMkdocsCommands()
	}
	p.Servers = append(p.Servers, Server{Name: "docs", Run: "mkdocs serve", Port: 8000, Proxy: true})
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// composerHasScript reports whether composer.json declares scripts.<name>.
func composerHasScript(path, name string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var c struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return false
	}
	_, ok := c.Scripts[name]
	return ok
}

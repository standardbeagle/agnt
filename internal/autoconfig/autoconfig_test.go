package autoconfig

import (
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
		Metadata: map[string]string{},
	}
	kdl, ok := Generate(p)
	require.True(t, ok)

	cfg := parseGenerated(t, kdl)
	require.Contains(t, cfg.Scripts, "dev")
	assert.Equal(t, "dotnet watch run", cfg.Scripts["dev"].Run)
	assert.True(t, cfg.Scripts["dev"].Autostart)
	require.Contains(t, cfg.Proxies, "dev", "dotnet site gets a proxy")
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

package config

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDependsOn_ArgsFormat(t *testing.T) {
	t.Run("single dependency default timeout", func(t *testing.T) {
		cfg, err := ParseAgntConfig(`scripts {
    api {
        run "go run ."
        autostart true
        depends-on "redis"
    }
    redis {
        run "redis-server"
        autostart true
    }
}`)
		require.NoError(t, err)
		api := cfg.Scripts["api"]
		require.Len(t, api.DependsOn, 1)
		assert.Equal(t, "redis", api.DependsOn[0].Name)
		assert.Equal(t, DefaultDependencyTimeout, api.DependsOn[0].Timeout)
	})

	t.Run("multiple dependencies default timeout", func(t *testing.T) {
		cfg, err := ParseAgntConfig(`scripts {
    api {
        run "go run ."
        autostart true
        depends-on "redis" "postgres"
    }
    redis {
        run "redis-server"
        autostart true
    }
    postgres {
        run "pg_ctl start"
        autostart true
    }
}`)
		require.NoError(t, err)
		api := cfg.Scripts["api"]
		require.Len(t, api.DependsOn, 2)
		names := []string{api.DependsOn[0].Name, api.DependsOn[1].Name}
		sort.Strings(names)
		assert.Equal(t, []string{"postgres", "redis"}, names)
	})

	t.Run("args with node-level timeout", func(t *testing.T) {
		cfg, err := ParseAgntConfig(`scripts {
    api {
        run "go run ."
        autostart true
        depends-on "redis" timeout=45
    }
    redis {
        run "redis-server"
        autostart true
    }
}`)
		require.NoError(t, err)
		api := cfg.Scripts["api"]
		require.Len(t, api.DependsOn, 1)
		assert.Equal(t, "redis", api.DependsOn[0].Name)
		assert.Equal(t, 45*time.Second, api.DependsOn[0].Timeout)
	})
}

func TestParseDependsOn_ChildNodeFormat(t *testing.T) {
	t.Run("per-dependency timeouts", func(t *testing.T) {
		cfg, err := ParseAgntConfig(`scripts {
    api {
        run "go run ."
        autostart true
        depends-on {
            redis timeout=30
            postgres timeout=60
        }
    }
    redis {
        run "redis-server"
        autostart true
    }
    postgres {
        run "pg_ctl start"
        autostart true
    }
}`)
		require.NoError(t, err)
		api := cfg.Scripts["api"]
		require.Len(t, api.DependsOn, 2)

		depMap := make(map[string]time.Duration)
		for _, d := range api.DependsOn {
			depMap[d.Name] = d.Timeout
		}
		assert.Equal(t, 30*time.Second, depMap["redis"])    // explicit timeout=30 in KDL
		assert.Equal(t, 60*time.Second, depMap["postgres"]) // explicit timeout=60 in KDL
	})

	t.Run("child nodes with default timeout", func(t *testing.T) {
		cfg, err := ParseAgntConfig(`scripts {
    api {
        run "go run ."
        autostart true
        depends-on {
            redis
        }
    }
    redis {
        run "redis-server"
        autostart true
    }
}`)
		require.NoError(t, err)
		api := cfg.Scripts["api"]
		require.Len(t, api.DependsOn, 1)
		assert.Equal(t, "redis", api.DependsOn[0].Name)
		assert.Equal(t, DefaultDependencyTimeout, api.DependsOn[0].Timeout)
	})
}

func TestParseDependsOn_UnknownDependency(t *testing.T) {
	_, err := ParseAgntConfig(`scripts {
    api {
        run "go run ."
        autostart true
        depends-on "nonexistent"
    }
}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown script")
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestParseDependsOn_CircularDependency(t *testing.T) {
	_, err := ParseAgntConfig(`scripts {
    a {
        run "echo a"
        autostart true
        depends-on "b"
    }
    b {
        run "echo b"
        autostart true
        depends-on "a"
    }
}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestParseDependsOn_NoDependencies(t *testing.T) {
	cfg, err := ParseAgntConfig(`scripts {
    api {
        run "go run ."
        autostart true
    }
}`)
	require.NoError(t, err)
	assert.Nil(t, cfg.Scripts["api"].DependsOn)
}

func TestTopologicalSort_SimpleChain(t *testing.T) {
	// c -> b -> a
	scripts := map[string]*ScriptConfig{
		"a": {Run: "echo a"},
		"b": {Run: "echo b", DependsOn: DependsOnList{{Name: "a", Timeout: DefaultDependencyTimeout}}},
		"c": {Run: "echo c", DependsOn: DependsOnList{{Name: "b", Timeout: DefaultDependencyTimeout}}},
	}

	layers, err := TopologicalSort(scripts)
	require.NoError(t, err)
	require.Len(t, layers, 3)

	assert.Equal(t, []string{"a"}, layers[0])
	assert.Equal(t, []string{"b"}, layers[1])
	assert.Equal(t, []string{"c"}, layers[2])
}

func TestTopologicalSort_Diamond(t *testing.T) {
	//     a
	//    / \
	//   b   c
	//    \ /
	//     d
	scripts := map[string]*ScriptConfig{
		"a": {Run: "echo a"},
		"b": {Run: "echo b", DependsOn: DependsOnList{{Name: "a", Timeout: DefaultDependencyTimeout}}},
		"c": {Run: "echo c", DependsOn: DependsOnList{{Name: "a", Timeout: DefaultDependencyTimeout}}},
		"d": {Run: "echo d", DependsOn: DependsOnList{
			{Name: "b", Timeout: DefaultDependencyTimeout},
			{Name: "c", Timeout: DefaultDependencyTimeout},
		}},
	}

	layers, err := TopologicalSort(scripts)
	require.NoError(t, err)
	require.Len(t, layers, 3)

	assert.Equal(t, []string{"a"}, layers[0])

	// b and c should be in the same layer (parallel)
	sort.Strings(layers[1])
	assert.Equal(t, []string{"b", "c"}, layers[1])

	assert.Equal(t, []string{"d"}, layers[2])
}

func TestTopologicalSort_ParallelRoots(t *testing.T) {
	// a, b, c are all independent
	scripts := map[string]*ScriptConfig{
		"a": {Run: "echo a"},
		"b": {Run: "echo b"},
		"c": {Run: "echo c"},
	}

	layers, err := TopologicalSort(scripts)
	require.NoError(t, err)
	require.Len(t, layers, 1)

	sort.Strings(layers[0])
	assert.Equal(t, []string{"a", "b", "c"}, layers[0])
}

func TestTopologicalSort_Cycle(t *testing.T) {
	scripts := map[string]*ScriptConfig{
		"a": {Run: "echo a", DependsOn: DependsOnList{{Name: "b", Timeout: DefaultDependencyTimeout}}},
		"b": {Run: "echo b", DependsOn: DependsOnList{{Name: "c", Timeout: DefaultDependencyTimeout}}},
		"c": {Run: "echo c", DependsOn: DependsOnList{{Name: "a", Timeout: DefaultDependencyTimeout}}},
	}

	_, err := TopologicalSort(scripts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestTopologicalSort_UnknownDependency(t *testing.T) {
	scripts := map[string]*ScriptConfig{
		"a": {Run: "echo a", DependsOn: DependsOnList{{Name: "nonexistent", Timeout: DefaultDependencyTimeout}}},
	}

	_, err := TopologicalSort(scripts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown script")
}

func TestTopologicalSort_Empty(t *testing.T) {
	layers, err := TopologicalSort(map[string]*ScriptConfig{})
	require.NoError(t, err)
	assert.Empty(t, layers)
}

func TestTopologicalSort_SingleNode(t *testing.T) {
	scripts := map[string]*ScriptConfig{
		"a": {Run: "echo a"},
	}

	layers, err := TopologicalSort(scripts)
	require.NoError(t, err)
	require.Len(t, layers, 1)
	assert.Equal(t, []string{"a"}, layers[0])
}

func TestTopologicalSort_ComplexDAG(t *testing.T) {
	// Layer 0: db, cache (no deps)
	// Layer 1: api (depends on db, cache), worker (depends on cache)
	// Layer 2: frontend (depends on api)
	scripts := map[string]*ScriptConfig{
		"db":       {Run: "pg start"},
		"cache":    {Run: "redis-server"},
		"api":      {Run: "go run .", DependsOn: DependsOnList{{Name: "db"}, {Name: "cache"}}},
		"worker":   {Run: "worker start", DependsOn: DependsOnList{{Name: "cache"}}},
		"frontend": {Run: "npm run dev", DependsOn: DependsOnList{{Name: "api"}}},
	}

	layers, err := TopologicalSort(scripts)
	require.NoError(t, err)
	require.Len(t, layers, 3)

	sort.Strings(layers[0])
	assert.Equal(t, []string{"cache", "db"}, layers[0])

	sort.Strings(layers[1])
	assert.Equal(t, []string{"api", "worker"}, layers[1])

	assert.Equal(t, []string{"frontend"}, layers[2])
}

func TestValidateDependencies_NonAutostartWarning(t *testing.T) {
	scripts := map[string]*ScriptConfig{
		"api":   {Run: "go run .", Autostart: true, DependsOn: DependsOnList{{Name: "redis"}}},
		"redis": {Run: "redis-server", Autostart: false},
	}

	warnings, err := ValidateDependencies(scripts)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "not set to autostart")
	assert.Contains(t, warnings[0], "redis")
}

func TestValidateDependencies_NoWarningsWhenAllAutostart(t *testing.T) {
	scripts := map[string]*ScriptConfig{
		"api":   {Run: "go run .", Autostart: true, DependsOn: DependsOnList{{Name: "redis"}}},
		"redis": {Run: "redis-server", Autostart: true},
	}

	warnings, err := ValidateDependencies(scripts)
	require.NoError(t, err)
	assert.Empty(t, warnings)
}

func TestValidateDependencies_SelfDependency(t *testing.T) {
	scripts := map[string]*ScriptConfig{
		"a": {Run: "echo a", DependsOn: DependsOnList{{Name: "a"}}},
	}

	_, err := ValidateDependencies(scripts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestParseDependsOn_FullConfigIntegration(t *testing.T) {
	cfg, err := ParseAgntConfig(`scripts {
    db {
        run "pg_ctl start"
        autostart true
    }
    cache {
        run "redis-server"
        autostart true
    }
    api {
        run "go run ./cmd/server"
        autostart true
        depends-on {
            db timeout=60
            cache timeout=30
        }
    }
    frontend {
        run "npm run dev"
        autostart true
        depends-on "api"
    }
}

proxies {
    dev {
        script "frontend"
        fallback-port 3000
    }
}`)
	require.NoError(t, err)

	// Verify dependencies parsed correctly
	api := cfg.Scripts["api"]
	require.Len(t, api.DependsOn, 2)

	frontend := cfg.Scripts["frontend"]
	require.Len(t, frontend.DependsOn, 1)
	assert.Equal(t, "api", frontend.DependsOn[0].Name)
	assert.Equal(t, DefaultDependencyTimeout, frontend.DependsOn[0].Timeout)

	// Verify topological sort works with this config
	layers, err := TopologicalSort(cfg.Scripts)
	require.NoError(t, err)
	require.Len(t, layers, 3)

	// Layer 0: db, cache
	sort.Strings(layers[0])
	assert.Equal(t, []string{"cache", "db"}, layers[0])

	// Layer 1: api
	assert.Equal(t, []string{"api"}, layers[1])

	// Layer 2: frontend
	assert.Equal(t, []string{"frontend"}, layers[2])

	// Proxies should still work
	assert.Len(t, cfg.Proxies, 1)
}

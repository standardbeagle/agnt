package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAgntConfig_ProxyWaitFor_Valid(t *testing.T) {
	kdl := `
scripts {
    dev-frontend {
        run "vite"
        autostart true
    }
    dev-backend {
        run "dotnet watch"
        autostart true
    }
}

proxies {
    dev {
        script "dev-frontend"
        wait-for "dev-backend"
        fallback-port 5173
    }
}
`
	cfg, err := ParseAgntConfig(kdl)
	require.NoError(t, err)
	require.NotNil(t, cfg.Proxies["dev"])
	assert.Equal(t, []string{"dev-backend"}, cfg.Proxies["dev"].WaitFor)
}

func TestParseAgntConfig_ProxyWaitFor_MultipleDeps(t *testing.T) {
	kdl := `
scripts {
    dev-frontend {
        run "vite"
        autostart true
    }
    dev-backend {
        run "dotnet watch"
        autostart true
    }
    dev-lib {
        run "npm run watch"
        autostart true
    }
}

proxies {
    dev {
        script "dev-frontend"
        wait-for "dev-backend" "dev-lib"
    }
}
`
	cfg, err := ParseAgntConfig(kdl)
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-backend", "dev-lib"}, cfg.Proxies["dev"].WaitFor)
}

func TestParseAgntConfig_ProxyWaitFor_UnknownScript(t *testing.T) {
	kdl := `
scripts {
    dev-frontend {
        run "vite"
        autostart true
    }
}

proxies {
    dev {
        script "dev-frontend"
        wait-for "dev-bakcend"
    }
}
`
	_, err := ParseAgntConfig(kdl)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dev-bakcend")
	assert.Contains(t, err.Error(), "wait-for")
}

func TestParseAgntConfig_ProxyWaitFor_DuplicateEntry(t *testing.T) {
	kdl := `
scripts {
    dev-frontend {
        run "vite"
        autostart true
    }
    dev-backend {
        run "dotnet watch"
        autostart true
    }
}

proxies {
    dev {
        script "dev-frontend"
        wait-for "dev-backend" "dev-backend"
    }
}
`
	_, err := ParseAgntConfig(kdl)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestParseAgntConfig_ProxyWaitFor_NoWaitFor_Regression(t *testing.T) {
	// A proxy with no wait-for must parse identically to today.
	kdl := `
scripts {
    dev {
        run "vite"
        autostart true
    }
}

proxies {
    dev {
        script "dev"
        fallback-port 5173
    }
}
`
	cfg, err := ParseAgntConfig(kdl)
	require.NoError(t, err)
	assert.Nil(t, cfg.Proxies["dev"].WaitFor)
}

func TestValidateProxyWaitFor_Direct(t *testing.T) {
	scripts := map[string]*ScriptConfig{
		"a": {},
		"b": {},
	}
	tests := []struct {
		name      string
		proxies   map[string]*ProxyConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "ok — no wait-for",
			proxies: map[string]*ProxyConfig{"p": {Script: "a"}},
		},
		{
			name:    "ok — all resolve",
			proxies: map[string]*ProxyConfig{"p": {Script: "a", WaitFor: []string{"b"}}},
		},
		{
			name:      "unknown dep",
			proxies:   map[string]*ProxyConfig{"p": {Script: "a", WaitFor: []string{"ghost"}}},
			wantErr:   true,
			errSubstr: "ghost",
		},
		{
			name:      "empty entry",
			proxies:   map[string]*ProxyConfig{"p": {Script: "a", WaitFor: []string{""}}},
			wantErr:   true,
			errSubstr: "empty",
		},
		{
			name:      "duplicate entry",
			proxies:   map[string]*ProxyConfig{"p": {Script: "a", WaitFor: []string{"b", "b"}}},
			wantErr:   true,
			errSubstr: "duplicate",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProxyWaitFor(tc.proxies, scripts)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

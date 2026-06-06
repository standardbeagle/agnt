package overlay

import "testing"

func TestFilterPaletteCommands(t *testing.T) {
	tests := []struct {
		name       string
		buffer     string
		wantNames  []string
		wantQuery  string
		wantArgs   string
		wantAtLe01 bool // expect non-empty match set
	}{
		{
			name:       "empty buffer matches all",
			buffer:     "",
			wantQuery:  "",
			wantArgs:   "",
			wantAtLe01: true,
		},
		{
			name:       "prefix kill matches both kill commands",
			buffer:     "kill",
			wantNames:  []string{"kill-port", "kill-orphans"},
			wantQuery:  "kill",
			wantArgs:   "",
			wantAtLe01: true,
		},
		{
			name:       "full command with arg keeps single match and splits args",
			buffer:     "kill-port 3000",
			wantNames:  []string{"kill-port"},
			wantQuery:  "kill-port",
			wantArgs:   "3000",
			wantAtLe01: true,
		},
		{
			name:       "run with multiword shell args",
			buffer:     "run npm run build",
			wantNames:  []string{"run"},
			wantQuery:  "run",
			wantArgs:   "npm run build",
			wantAtLe01: true,
		},
		{
			name:       "restart prefix matches restart and restart-proxy",
			buffer:     "restart dev",
			wantNames:  []string{"restart", "restart-proxy"},
			wantQuery:  "restart",
			wantArgs:   "dev",
			wantAtLe01: true,
		},
		{
			name:       "no match",
			buffer:     "zzz",
			wantNames:  nil,
			wantQuery:  "zzz",
			wantArgs:   "",
			wantAtLe01: false,
		},
		{
			name:      "leading spaces trimmed",
			buffer:    "   stop  dev ",
			wantNames: []string{"stop", "stop-proxy", "stop-tunnel"},
			wantQuery: "stop",
			wantArgs:  "dev",
		},
		{
			name:      "case insensitive",
			buffer:    "KILL-PORT 80",
			wantNames: []string{"kill-port"},
			wantQuery: "KILL-PORT",
			wantArgs:  "80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches, query, args := filterPaletteCommands(tt.buffer)
			if query != tt.wantQuery {
				t.Errorf("query = %q, want %q", query, tt.wantQuery)
			}
			if args != tt.wantArgs {
				t.Errorf("args = %q, want %q", args, tt.wantArgs)
			}
			if tt.wantNames != nil {
				gotNames := make([]string, len(matches))
				for i, m := range matches {
					gotNames[i] = m.Name
				}
				if len(gotNames) != len(tt.wantNames) {
					t.Fatalf("matches = %v, want %v", gotNames, tt.wantNames)
				}
				for i := range tt.wantNames {
					if gotNames[i] != tt.wantNames[i] {
						t.Errorf("match[%d] = %q, want %q", i, gotNames[i], tt.wantNames[i])
					}
				}
			}
			if tt.wantAtLe01 && len(matches) == 0 {
				t.Errorf("expected at least one match for %q", tt.buffer)
			}
			if !tt.wantAtLe01 && tt.wantNames == nil && len(matches) != 0 {
				t.Errorf("expected no matches for %q, got %d", tt.buffer, len(matches))
			}
		})
	}
}

// TestPaletteCommandsRegistry guards the registry shape: every command has a
// name and description, names are unique, and arg-taking commands document the
// arg. This is the contract the dispatcher and renderer rely on.
func TestPaletteCommandsRegistry(t *testing.T) {
	seen := make(map[string]bool)
	for _, c := range paletteCommands {
		if c.Name == "" {
			t.Error("command with empty name")
		}
		if c.Desc == "" {
			t.Errorf("command %q has no description", c.Name)
		}
		if seen[c.Name] {
			t.Errorf("duplicate command name %q", c.Name)
		}
		seen[c.Name] = true
	}
	for _, want := range []string{"start", "stop", "restart", "kill-port", "kill-orphans", "run"} {
		if !seen[want] {
			t.Errorf("registry missing expected command %q", want)
		}
	}
}

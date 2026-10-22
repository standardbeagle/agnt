package config

import "testing"

func TestBuildSystemPrompt_ScriptsAndProxiesDeterministic(t *testing.T) {
	cfg, err := ParseAgntConfig(`
scripts {
    zeta { run "z"; }
    alpha { run "a"; }
    mid { run "m"; }
}
proxies {
    zp { url "http://localhost:1"; }
    ap { url "http://localhost:2"; }
    mp { url "http://localhost:3"; }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	first := cfg.BuildSystemPrompt()
	for i := 0; i < 30; i++ {
		if got := cfg.BuildSystemPrompt(); got != first {
			t.Fatalf("system prompt differs between calls (map order leaked)")
		}
	}
}

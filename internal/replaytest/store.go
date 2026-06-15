package replaytest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store persists Scenarios and reports under <projectDir>/.agnt/replaytests/.
type Store struct{ dir string }

func NewStore(projectDir string) *Store {
	return &Store{dir: filepath.Join(projectDir, ".agnt", "replaytests")}
}

func (s *Store) SaveScenario(sc *Scenario) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := sc.MarshalJSON()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, sc.Name+".json"), data, 0o644)
}

func (s *Store) LoadScenario(name string) (*Scenario, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, name+".json"))
	if err != nil {
		return nil, err
	}
	return UnmarshalScenario(data)
}

func (s *Store) SaveReport(name string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, name+".report.json"), data, 0o644)
}

func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && !strings.HasSuffix(n, ".report.json") {
			names = append(names, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(names)
	return names, nil
}

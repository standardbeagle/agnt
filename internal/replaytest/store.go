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
	return writeAtomic(filepath.Join(s.dir, sc.Name+".json"), data, 0o644)
}

// writeAtomic writes data to a temp file in the target's directory, then renames
// it into place. A crash mid-write leaves the stale-but-intact file (never a
// truncated one), and concurrent writers to the same path resolve last-wins
// without corruption.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
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
	return writeAtomic(filepath.Join(s.dir, name+".report.json"), data, 0o644)
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

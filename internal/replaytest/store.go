package replaytest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Store persists Scenarios and reports under <projectDir>/.agnt/replaytests/.
type Store struct{ dir string }

func NewStore(projectDir string) *Store {
	return &Store{dir: filepath.Join(projectDir, ".agnt", "replaytests")}
}

// scenarioName is the whole grammar of a scenario name. It admits no path
// separator and no leading dot, so a name can only ever address a file directly
// inside the store directory.
var scenarioName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidateName rejects a scenario name that could address anything other than
// its own file in the store: the name is caller-supplied and becomes a file
// name, so "../../victim" would otherwise read and overwrite project files.
// A ".report" suffix is refused because <name>.report.json is the report file.
func ValidateName(name string) error {
	if !scenarioName.MatchString(name) || strings.HasSuffix(name, ".report") {
		return fmt.Errorf("invalid scenario name %q: use letters, digits, '.', '_' and '-' (max 128), starting with a letter or digit, not ending in .report", name)
	}
	return nil
}

// file returns the store path for name with the given extension, refusing any
// name ValidateName rejects.
func (s *Store) file(name, ext string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, name+ext), nil
}

func (s *Store) SaveScenario(sc *Scenario) error {
	path, err := s.file(sc.Name, ".json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := sc.MarshalJSON()
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o644)
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
	path, err := s.file(name, ".json")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return UnmarshalScenario(data)
}

func (s *Store) SaveReport(name string, data []byte) error {
	path, err := s.file(name, ".report.json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return writeAtomic(path, data, 0o644)
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

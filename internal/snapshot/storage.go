package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Storage handles baseline persistence
type Storage struct {
	basePath string
}

// NewStorage creates a new storage manager
func NewStorage(basePath string) (*Storage, error) {
	if basePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		basePath = filepath.Join(home, ".agnt", "baselines")
	}

	// Create base directory if it doesn't exist
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create baselines dir: %w", err)
	}

	return &Storage{basePath: basePath}, nil
}

// containedPath resolves a path component relative to basePath and verifies
// the result stays within basePath. Returns the resolved path or an error
// if the path would escape.
func (s *Storage) containedPath(components ...string) (string, error) {
	joined := filepath.Join(append([]string{s.basePath}, components...)...)
	resolved := filepath.Clean(joined)
	// Ensure resolved path is within basePath (with trailing separator to
	// avoid prefix-of-longer-name matches like /base matching /baseline).
	base := filepath.Clean(s.basePath) + string(filepath.Separator)
	if !strings.HasPrefix(resolved+string(filepath.Separator), base) && resolved != filepath.Clean(s.basePath) {
		return "", fmt.Errorf("path %q escapes base directory", filepath.Join(components...))
	}
	return resolved, nil
}

// SaveBaseline persists a baseline to disk
func (s *Storage) SaveBaseline(baseline *Baseline) error {
	baselineDir, err := s.containedPath(baseline.Name)
	if err != nil {
		return fmt.Errorf("invalid baseline name: %w", err)
	}

	// Create baseline directory
	if err = os.MkdirAll(baselineDir, 0755); err != nil {
		return fmt.Errorf("create baseline dir: %w", err)
	}

	// Save metadata
	metadataPath := filepath.Join(baselineDir, "metadata.json")
	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	return nil
}

// LoadBaseline loads a baseline from disk
func (s *Storage) LoadBaseline(name string) (*Baseline, error) {
	baselineDir, err := s.containedPath(name)
	if err != nil {
		return nil, fmt.Errorf("invalid baseline name: %w", err)
	}
	metadataPath := filepath.Join(baselineDir, "metadata.json")

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("baseline '%s' not found", name)
		}
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	return &baseline, nil
}

// ListBaselines returns all available baselines
func (s *Storage) ListBaselines() ([]*Baseline, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Baseline{}, nil
		}
		return nil, fmt.Errorf("read baselines dir: %w", err)
	}

	var baselines []*Baseline
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		baseline, err := s.LoadBaseline(entry.Name())
		if err != nil {
			// Skip invalid baselines
			continue
		}
		baselines = append(baselines, baseline)
	}

	// Sort by timestamp (newest first)
	sort.Slice(baselines, func(i, j int) bool {
		return baselines[i].Timestamp.After(baselines[j].Timestamp)
	})

	return baselines, nil
}

// DeleteBaseline removes a baseline from disk
func (s *Storage) DeleteBaseline(name string) error {
	baselineDir, err := s.containedPath(name)
	if err != nil {
		return fmt.Errorf("invalid baseline name: %w", err)
	}

	if err := os.RemoveAll(baselineDir); err != nil {
		return fmt.Errorf("remove baseline: %w", err)
	}

	return nil
}

// GetBaselinePath returns the directory path for a baseline.
// Returns an error if the name would escape the base directory.
func (s *Storage) GetBaselinePath(name string) (string, error) {
	return s.containedPath(name)
}

// SaveScreenshot saves a screenshot file to a baseline
func (s *Storage) SaveScreenshot(baselineName, filename string, data []byte) error {
	screenshotPath, err := s.containedPath(baselineName, filename)
	if err != nil {
		return fmt.Errorf("invalid screenshot path: %w", err)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(screenshotPath), 0755); err != nil {
		return fmt.Errorf("create baseline dir: %w", err)
	}

	if err := os.WriteFile(screenshotPath, data, 0644); err != nil {
		return fmt.Errorf("write screenshot: %w", err)
	}

	return nil
}

// GetScreenshotPath returns the path to a screenshot file.
// Returns an error if the path would escape the base directory.
func (s *Storage) GetScreenshotPath(baselineName, filename string) (string, error) {
	return s.containedPath(baselineName, filename)
}

// SaveDiff saves comparison results to disk
func (s *Storage) SaveDiff(result *CompareResult) error {
	// Create diffs directory
	diffsPath := filepath.Join(s.basePath, "..", "diffs")
	if err := os.MkdirAll(diffsPath, 0755); err != nil {
		return fmt.Errorf("create diffs dir: %w", err)
	}

	// Create diff-specific directory
	diffName := fmt.Sprintf("%s-%s", result.BaselineName, time.Now().Format("20060102-150405"))
	diffDir := filepath.Join(diffsPath, diffName)
	if err := os.MkdirAll(diffDir, 0755); err != nil {
		return fmt.Errorf("create diff dir: %w", err)
	}

	// Save report
	reportPath := filepath.Join(diffDir, "report.json")
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}

	if err := os.WriteFile(reportPath, data, 0644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	return nil
}

// GetDiffsPath returns the diffs directory path
func (s *Storage) GetDiffsPath() string {
	return filepath.Join(s.basePath, "..", "diffs")
}

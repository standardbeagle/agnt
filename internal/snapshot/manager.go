package snapshot

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// decodePageImage resolves the raw PNG bytes for a capture. It accepts either
// inline base64 (ScreenshotData) or a filesystem path (ScreenshotPath) — the
// latter is the bridge from the `screenshot` action, which writes a PNG to
// audit storage and returns its path via proxylog. ScreenshotData wins when
// both are set.
func decodePageImage(page PageCapture) ([]byte, error) {
	if page.ScreenshotData != "" {
		return base64.StdEncoding.DecodeString(page.ScreenshotData)
	}
	if page.ScreenshotPath != "" {
		return os.ReadFile(page.ScreenshotPath)
	}
	return nil, fmt.Errorf("no screenshot_data or screenshot_path for %s", page.URL)
}

// Manager orchestrates snapshot operations
type Manager struct {
	storage *Storage
	differ  *Differ
}

// NewManager creates a new snapshot manager
func NewManager(storagePath string, diffThreshold float64) (*Manager, error) {
	storage, err := NewStorage(storagePath)
	if err != nil {
		return nil, fmt.Errorf("create storage: %w", err)
	}

	differ := NewDiffer(diffThreshold)

	return &Manager{
		storage: storage,
		differ:  differ,
	}, nil
}

// CreateBaseline captures screenshots and saves them as a baseline
func (m *Manager) CreateBaseline(name string, pages []PageCapture) (*Baseline, error) {
	// Get git info if available
	gitCommit, gitBranch := m.getGitInfo()

	baseline := &Baseline{
		Name:      name,
		Timestamp: time.Now(),
		GitCommit: gitCommit,
		GitBranch: gitBranch,
		Pages:     make([]PageState, 0, len(pages)),
		Config: Config{
			DiffThreshold: m.differ.threshold,
		},
	}

	// Process each page
	for i, page := range pages {
		// Resolve screenshot bytes (inline base64 or file path)
		data, err := decodePageImage(page)
		if err != nil {
			return nil, fmt.Errorf("decode screenshot for %s: %w", page.URL, err)
		}

		// Generate filename from URL
		filename := m.generateFilename(page.URL, i)

		// Save screenshot
		if err := m.storage.SaveScreenshot(name, filename, data); err != nil {
			return nil, fmt.Errorf("save screenshot: %w", err)
		}

		// Add to baseline
		baseline.Pages = append(baseline.Pages, PageState{
			URL:        page.URL,
			Viewport:   page.Viewport,
			Screenshot: filename,
			Timestamp:  time.Now(),
		})
	}

	// Save baseline metadata
	if err := m.storage.SaveBaseline(baseline); err != nil {
		return nil, fmt.Errorf("save baseline: %w", err)
	}

	return baseline, nil
}

// CompareToBaseline compares current screenshots to a baseline. threshold is the
// per-call diff sensitivity (0.0-1.0); a value <= 0 falls back to the manager's
// configured default, so the tool's diff_threshold parameter is honored.
func (m *Manager) CompareToBaseline(baselineName string, currentPages []PageCapture, threshold float64) (*CompareResult, error) {
	if threshold <= 0 {
		threshold = m.differ.threshold
	}

	// Load baseline
	baseline, err := m.storage.LoadBaseline(baselineName)
	if err != nil {
		return nil, fmt.Errorf("load baseline: %w", err)
	}

	result := &CompareResult{
		BaselineName: baselineName,
		Timestamp:    time.Now(),
		Pages:        make([]PageComparison, 0),
		Summary: Summary{
			TotalPages: len(baseline.Pages),
		},
	}

	// Create map of current pages by URL
	currentMap := make(map[string]PageCapture)
	for _, page := range currentPages {
		currentMap[page.URL] = page
	}

	totalDiff := 0.0

	// Compare each baseline page
	for _, baselinePage := range baseline.Pages {
		currentPage, exists := currentMap[baselinePage.URL]
		if !exists {
			// Page missing from current capture
			result.Pages = append(result.Pages, PageComparison{
				URL:         baselinePage.URL,
				HasChanges:  true,
				Description: "Page not captured in current snapshot",
			})
			result.Summary.PagesChanged++
			continue
		}

		// Save current screenshot temporarily
		currentFilename := "current_" + baselinePage.Screenshot
		currentData, err := decodePageImage(currentPage)
		if err != nil {
			return nil, fmt.Errorf("decode current screenshot: %w", err)
		}

		currentBaseDir, err := m.storage.GetBaselinePath(baselineName)
		if err != nil {
			return nil, fmt.Errorf("invalid baseline name: %w", err)
		}
		currentPath := filepath.Join(currentBaseDir, currentFilename)
		if err := m.storage.SaveScreenshot(baselineName, currentFilename, currentData); err != nil {
			return nil, fmt.Errorf("save current screenshot: %w", err)
		}

		// Compare images
		baselinePath, err := m.storage.GetScreenshotPath(baselineName, baselinePage.Screenshot)
		if err != nil {
			return nil, fmt.Errorf("invalid screenshot path: %w", err)
		}
		diffPercentage, diffImg, err := m.differ.Compare(baselinePath, currentPath)
		if err != nil {
			result.Pages = append(result.Pages, PageComparison{
				URL:         baselinePage.URL,
				HasChanges:  true,
				Description: fmt.Sprintf("Comparison error: %v", err),
			})
			result.Summary.PagesChanged++
			continue
		}

		// Save diff image
		diffFilename := "diff_" + baselinePage.Screenshot
		diffBaseDir, err := m.storage.GetBaselinePath(baselineName)
		if err != nil {
			return nil, fmt.Errorf("invalid baseline name: %w", err)
		}
		diffPath := filepath.Join(diffBaseDir, diffFilename)
		if err := m.differ.SaveDiffImage(diffImg, diffPath); err != nil {
			return nil, fmt.Errorf("save diff image: %w", err)
		}

		hasChanges := diffPercentage > threshold
		if hasChanges {
			result.Summary.PagesChanged++
		} else {
			result.Summary.PagesUnchanged++
		}

		result.Pages = append(result.Pages, PageComparison{
			URL:               baselinePage.URL,
			DiffPercentage:    diffPercentage * 100, // Convert to percentage
			DiffImagePath:     diffPath,
			BaselineImagePath: baselinePath,
			CurrentImagePath:  currentPath,
			HasChanges:        hasChanges,
			Description:       m.generateDiffDescription(diffPercentage),
		})

		totalDiff += diffPercentage
	}

	// Calculate summary
	if len(baseline.Pages) > 0 {
		result.Summary.AverageDiff = (totalDiff / float64(len(baseline.Pages))) * 100
	}
	result.Summary.HasRegressions = result.Summary.PagesChanged > 0

	// Save diff report
	if err := m.storage.SaveDiff(result); err != nil {
		return nil, fmt.Errorf("save diff: %w", err)
	}

	return result, nil
}

// ListBaselines returns all available baselines
func (m *Manager) ListBaselines() ([]*Baseline, error) {
	return m.storage.ListBaselines()
}

// DeleteBaseline removes a baseline
func (m *Manager) DeleteBaseline(name string) error {
	return m.storage.DeleteBaseline(name)
}

// GetBaseline loads a specific baseline
func (m *Manager) GetBaseline(name string) (*Baseline, error) {
	return m.storage.LoadBaseline(name)
}

// Helper functions

func (m *Manager) generateFilename(url string, index int) string {
	// Create hash of URL for filename
	hash := sha256.Sum256([]byte(url))
	hashStr := hex.EncodeToString(hash[:])[:8]

	// Clean URL for filename
	name := strings.TrimPrefix(url, "http://")
	name = strings.TrimPrefix(name, "https://")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, ":", "_")
	if len(name) > 30 {
		name = name[:30]
	}

	return fmt.Sprintf("%d_%s_%s.png", index, name, hashStr)
}

func (m *Manager) generateDiffDescription(diffPercentage float64) string {
	percent := diffPercentage * 100

	if percent == 0 {
		return "No visual changes detected"
	} else if percent < 0.1 {
		return "Minimal changes (< 0.1%)"
	} else if percent < 1.0 {
		return fmt.Sprintf("Minor changes (%.2f%%)", percent)
	} else if percent < 5.0 {
		return fmt.Sprintf("Moderate changes (%.2f%%)", percent)
	} else {
		return fmt.Sprintf("Significant changes (%.2f%%)", percent)
	}
}

func (m *Manager) getGitInfo() (commit, branch string) {
	// Try to get git commit
	if cmd := exec.Command("git", "rev-parse", "HEAD"); cmd.Run() == nil {
		if output, err := cmd.Output(); err == nil {
			commit = strings.TrimSpace(string(output))
			if len(commit) > 7 {
				commit = commit[:7]
			}
		}
	}

	// Try to get git branch
	if cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD"); cmd.Run() == nil {
		if output, err := cmd.Output(); err == nil {
			branch = strings.TrimSpace(string(output))
		}
	}

	return
}

// PageCapture represents a page to capture or that was captured
type PageCapture struct {
	URL            string   `json:"url"`
	Viewport       Viewport `json:"viewport"`
	ScreenshotData string   `json:"screenshot_data,omitempty"` // Base64 encoded PNG
	ScreenshotPath string   `json:"screenshot_path,omitempty"` // Filesystem path (bridge from screenshot action)
}

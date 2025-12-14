package snapshot

import "time"

// Baseline represents a saved set of screenshots for comparison
type Baseline struct {
	Name      string       `json:"name"`
	Timestamp time.Time    `json:"timestamp"`
	GitCommit string       `json:"git_commit,omitempty"`
	GitBranch string       `json:"git_branch,omitempty"`
	Pages     []PageState  `json:"pages"`
	Config    Config       `json:"config"`
}

// PageState represents the captured state of a single page
type PageState struct {
	URL        string            `json:"url"`
	Viewport   Viewport          `json:"viewport"`
	Screenshot string            `json:"screenshot"` // Filename
	Timestamp  time.Time         `json:"timestamp"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Viewport represents screen dimensions
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Config holds snapshot configuration
type Config struct {
	DiffThreshold float64        `json:"diff_threshold"` // 0.0 - 1.0
	IgnoreRegions []IgnoreRegion `json:"ignore_regions,omitempty"`
}

// IgnoreRegion defines areas to skip during comparison
type IgnoreRegion struct {
	Selector string `json:"selector"`
	Reason   string `json:"reason"`
}

// CompareResult holds the results of a baseline comparison
type CompareResult struct {
	BaselineName string           `json:"baseline_name"`
	Timestamp    time.Time        `json:"timestamp"`
	Pages        []PageComparison `json:"pages"`
	Summary      Summary          `json:"summary"`
}

// PageComparison holds diff results for a single page
type PageComparison struct {
	URL              string  `json:"url"`
	DiffPercentage   float64 `json:"diff_percentage"`
	DiffImagePath    string  `json:"diff_image_path"`
	BaselineImagePath string `json:"baseline_image_path"`
	CurrentImagePath  string `json:"current_image_path"`
	HasChanges       bool    `json:"has_changes"`
	Description      string  `json:"description,omitempty"`
}

// Summary provides high-level comparison statistics
type Summary struct {
	TotalPages      int     `json:"total_pages"`
	PagesChanged    int     `json:"pages_changed"`
	PagesUnchanged  int     `json:"pages_unchanged"`
	AverageDiff     float64 `json:"average_diff"`
	HasRegressions  bool    `json:"has_regressions"`
}

package updater

import (
	"context"
	"log"
	"sync"
	"time"
)

// UpdateInfo holds information about available updates
type UpdateInfo struct {
	Available      bool      `json:"available"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	CurrentVersion string    `json:"current_version"`
	ReleaseURL     string    `json:"release_url,omitempty"`
	ReleaseNotes   string    `json:"release_notes,omitempty"`
	LastChecked    time.Time `json:"last_checked"`
	CheckError     string    `json:"check_error,omitempty"`
}

// UpdateChecker periodically checks for updates
type UpdateChecker struct {
	mu             sync.RWMutex
	currentVersion string
	checkInterval  time.Duration
	githubChecker  *GitHubChecker
	updateInfo     UpdateInfo
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

// Config holds configuration for the update checker
type Config struct {
	CurrentVersion string
	CheckInterval  time.Duration
	GitHubRepo     string
	Enabled        bool
}

// DefaultConfig returns default update checker configuration
func DefaultConfig() Config {
	return Config{
		CurrentVersion: "unknown",
		CheckInterval:  24 * time.Hour,
		GitHubRepo:     DefaultGitHubRepo,
		Enabled:        true,
	}
}

// NewUpdateChecker creates a new update checker
func NewUpdateChecker(config Config) *UpdateChecker {
	if config.CheckInterval == 0 {
		config.CheckInterval = 24 * time.Hour
	}

	ctx, cancel := context.WithCancel(context.Background())

	uc := &UpdateChecker{
		currentVersion: config.CurrentVersion,
		checkInterval:  config.CheckInterval,
		githubChecker:  NewGitHubChecker(config.GitHubRepo),
		updateInfo: UpdateInfo{
			CurrentVersion: config.CurrentVersion,
			LastChecked:    time.Time{}, // Zero time indicates never checked
		},
		ctx:    ctx,
		cancel: cancel,
	}

	return uc
}

// Start begins periodic update checking
func (uc *UpdateChecker) Start() {
	uc.wg.Add(1)
	go uc.checkLoop()
}

// Stop stops the update checker
func (uc *UpdateChecker) Stop() {
	uc.cancel()
	uc.wg.Wait()
}

// checkLoop runs the periodic update check
func (uc *UpdateChecker) checkLoop() {
	defer uc.wg.Done()

	// Check immediately on start
	uc.checkForUpdates()

	ticker := time.NewTicker(uc.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-uc.ctx.Done():
			return
		case <-ticker.C:
			uc.checkForUpdates()
		}
	}
}

// checkForUpdates performs a single update check
func (uc *UpdateChecker) checkForUpdates() {
	log.Printf("[UpdateChecker] Checking for updates...")

	release, err := uc.githubChecker.CheckLatestRelease()
	if err != nil {
		log.Printf("[UpdateChecker] Failed to check for updates: %v", err)
		uc.mu.Lock()
		uc.updateInfo.CheckError = err.Error()
		uc.updateInfo.LastChecked = time.Now()
		uc.mu.Unlock()
		return
	}

	isNewer, err := release.IsNewer(uc.currentVersion)
	if err != nil {
		log.Printf("[UpdateChecker] Failed to compare versions: %v", err)
		uc.mu.Lock()
		uc.updateInfo.CheckError = err.Error()
		uc.updateInfo.LastChecked = time.Now()
		uc.mu.Unlock()
		return
	}

	uc.mu.Lock()
	uc.updateInfo = UpdateInfo{
		Available:      isNewer,
		LatestVersion:  release.GetVersion(),
		CurrentVersion: uc.currentVersion,
		ReleaseURL:     release.HTMLURL,
		ReleaseNotes:   release.Body,
		LastChecked:    time.Now(),
		CheckError:     "",
	}
	uc.mu.Unlock()

	if isNewer {
		log.Printf("[UpdateChecker] Update available: %s -> %s",
			uc.currentVersion, release.GetVersion())
	} else {
		log.Printf("[UpdateChecker] No update available (current: %s, latest: %s)",
			uc.currentVersion, release.GetVersion())
	}
}

// GetUpdateInfo returns the current update information
func (uc *UpdateChecker) GetUpdateInfo() UpdateInfo {
	uc.mu.RLock()
	defer uc.mu.RUnlock()
	return uc.updateInfo
}

// CheckNow triggers an immediate update check
func (uc *UpdateChecker) CheckNow() {
	uc.checkForUpdates()
}

package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AuditDirName is the directory name for storing audit data within .agnt
const AuditDirName = "audit"

// GetAuditDir returns the audit directory path for a project.
// Creates the directory if it doesn't exist.
// Returns absolute path to .agnt/audit in the current working directory.
func GetAuditDir() (string, error) {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	// Create .agnt/audit directory path
	auditDir := filepath.Join(cwd, ".agnt", AuditDirName)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create audit directory: %w", err)
	}

	return auditDir, nil
}

// SaveAuditData saves audit data as a JSON file in the .agnt/audit directory.
// Returns the file path and any error.
func SaveAuditData(auditType, label string, result json.RawMessage) (string, error) {
	auditDir, err := GetAuditDir()
	if err != nil {
		return "", err
	}

	// Create filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	safeLabel := sanitizeFilename(label)
	filename := fmt.Sprintf("audit-%s-%s-%s.json", auditType, safeLabel, timestamp)
	filePath := filepath.Join(auditDir, filename)

	// Create audit data structure
	auditData := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"auditType": auditType,
		"label":     label,
		"result":    result,
	}

	// Marshal to JSON with pretty formatting
	data, err := json.MarshalIndent(auditData, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal audit data: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write audit file: %w", err)
	}

	return filePath, nil
}

// sanitizeFilename replaces characters that are not safe for filenames.
func sanitizeFilename(name string) string {
	// Replace characters that are problematic in filenames
	replacer := strings.NewReplacer(
		" ", "_",
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "-",
		"?", "-",
		"\"", "",
		"<", "",
		">", "",
		"|", "-",
	)
	safe := replacer.Replace(name)

	// Limit length to avoid filesystem issues
	if len(safe) > 50 {
		safe = safe[:50]
	}

	return safe
}

// FormatAuditReference formats a reference to the audit directory for AI agent messages.
func FormatAuditReference() string {
	auditDir, err := GetAuditDir()
	if err != nil {
		// Fallback to relative path if we can't get absolute path
		return ".agnt/audit/"
	}
	return auditDir + "/"
}

// AuditSummaryFile is the name of the summary file in the audit directory.
const AuditSummaryFile = "SUMMARY.md"

// UpdateAuditSummary creates or updates the SUMMARY.md file in .agnt/audit/
// with a listing of all files and their descriptions.
func UpdateAuditSummary() error {
	auditDir, err := GetAuditDir()
	if err != nil {
		return err
	}

	summaryPath := filepath.Join(auditDir, AuditSummaryFile)

	// Collect all files in the audit directory
	files, err := os.ReadDir(auditDir)
	if err != nil {
		return fmt.Errorf("failed to read audit directory: %w", err)
	}

	// Build summary content
	var summary strings.Builder
	summary.WriteString("# Audit Data Summary\n\n")
	summary.WriteString(fmt.Sprintf("Generated: %s\n\n", time.Now().Format(time.RFC3339)))
	summary.WriteString("This directory contains all screenshots, audit results, and related data from agnt.\n\n")
	summary.WriteString("## Directory Structure\n\n")
	summary.WriteString("```\n.agnt/audit/\n")
	summary.WriteString("├── SUMMARY.md           (this file)\n")
	summary.WriteString("├── screenshots/         (captured screenshots)\n")
	summary.WriteString("│   ├── screenshot-*.png\n")
	summary.WriteString("│   └── sketch-*.png\n")
	summary.WriteString("└── audit-*.json         (detailed audit results)\n")
	summary.WriteString("```\n\n")
	summary.WriteString("## Files\n\n")

	// Separate into categories
	var screenshots []string
	var audits []string
	var other []string

	for _, f := range files {
		if f.IsDir() {
			if f.Name() == "screenshots" {
				// List screenshot files
				screenshotDir := filepath.Join(auditDir, "screenshots")
				screenshotFiles, _ := os.ReadDir(screenshotDir)
				for _, sf := range screenshotFiles {
					screenshots = append(screenshots, fmt.Sprintf("screenshots/%s", sf.Name()))
				}
			}
			continue
		}
		name := f.Name()
		switch {
		case strings.HasPrefix(name, "screenshot-") || strings.HasPrefix(name, "sketch-"):
			// Screenshots saved at root level (legacy)
			screenshots = append(screenshots, name)
		case strings.HasPrefix(name, "audit-"):
			audits = append(audits, name)
		case name != AuditSummaryFile:
			other = append(other, name)
		}
	}

	// Sort files by name (newest first due to timestamp in filename)
	sort.Sort(sort.Reverse(sort.StringSlice(screenshots)))
	sort.Sort(sort.Reverse(sort.StringSlice(audits)))
	sort.Sort(sort.Reverse(sort.StringSlice(other)))

	// Write screenshots section
	if len(screenshots) > 0 {
		summary.WriteString("### Screenshots\n\n")
		for _, s := range screenshots {
			summary.WriteString(fmt.Sprintf("- `%s`\n", s))
		}
		summary.WriteString("\n")
	}

	// Write audit files section
	if len(audits) > 0 {
		summary.WriteString("### Audit Results\n\n")
		for _, a := range audits {
			summary.WriteString(fmt.Sprintf("- `%s`\n", a))
		}
		summary.WriteString("\n")
	}

	// Write other files section
	if len(other) > 0 {
		summary.WriteString("### Other Files\n\n")
		for _, o := range other {
			summary.WriteString(fmt.Sprintf("- `%s`\n", o))
		}
		summary.WriteString("\n")
	}

	summary.WriteString("---\n\n")
	summary.WriteString("## Usage\n\n")
	summary.WriteString("AI coding agents can use these files to:\n")
	summary.WriteString("- View screenshots to understand the visual state of the application\n")
	summary.WriteString("- Analyze audit results to identify accessibility, performance, or quality issues\n")
	summary.WriteString("- Reference specific files when implementing fixes\n\n")
	summary.WriteString("### Example Commands\n\n")
	summary.WriteString("```bash\n")
	summary.WriteString("# View the most recent screenshot\n")
	summary.WriteString("ls -lt .agnt/audit/screenshots/ | head -5\n\n")
	summary.WriteString("# Read a specific audit result\n")
	summary.WriteString("cat .agnt/audit/audit-accessibility-*.json | jq '.result'\n\n")
	summary.WriteString("# Count total issues across audits\n")
	summary.WriteString("grep -r '\"type\"' .agnt/audit/audit-*.json | wc -l\n")
	summary.WriteString("```\n")

	// Write summary to file
	if err := os.WriteFile(summaryPath, []byte(summary.String()), 0644); err != nil {
		return fmt.Errorf("failed to write summary file: %w", err)
	}

	return nil
}

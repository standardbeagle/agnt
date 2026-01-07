// Package license provides feature licensing and validation.
//
// BETA STATUS: All features are currently unlocked during beta.
// When agnt reaches stable release, premium features will require a license.
package license

import "time"

// Feature represents a licensed feature in agnt.
type Feature string

const (
	// Free tier features (always available)
	FeatureBasicProxy            Feature = "basic-proxy"            // Basic reverse proxy
	FeatureProcessManagement     Feature = "process-management"     // Process management
	FeatureBasicAccessibility    Feature = "basic-accessibility"    // Basic a11y audit
	FeatureStandardAccessibility Feature = "standard-accessibility" // Standard a11y audit (axe-core)
	FeatureFastAccessibility     Feature = "fast-accessibility"     // Fast a11y improvements mode
	FeatureScreenshots           Feature = "screenshots"            // Screenshot capture
	FeatureFloatingIndicator     Feature = "floating-indicator"     // Floating indicator panel

	// Premium features (will require license after beta)
	FeatureSketchMode        Feature = "sketch-mode"        // Wireframing/sketch mode
	FeatureDesignMode        Feature = "design-mode"        // AI-assisted design iteration
	FeatureComprehensiveA11y Feature = "comprehensive-a11y" // Comprehensive accessibility audit
	FeatureTunneling         Feature = "tunneling"          // Tunnel integration
	FeatureVisualRegression  Feature = "visual-regression"  // Snapshot/baseline testing
	FeatureChaosProxy        Feature = "chaos-proxy"        // Chaos engineering for proxy
)

// License represents a user's license configuration.
type License struct {
	// Valid indicates if the license is valid
	Valid bool

	// Email of the license holder
	Email string

	// Features enabled by this license
	Features []Feature

	// ExpiresAt is when the license expires (nil = perpetual)
	ExpiresAt *time.Time

	// BetaMode indicates if we're in beta (all features unlocked)
	BetaMode bool
}

var (
	// currentLicense holds the active license
	// During beta, this is always a fully-unlocked license
	currentLicense = &License{
		Valid:    true,
		Email:    "beta@agnt.dev",
		Features: allFeatures(),
		BetaMode: true,
	}
)

// allFeatures returns all available features.
func allFeatures() []Feature {
	return []Feature{
		FeatureBasicProxy,
		FeatureProcessManagement,
		FeatureBasicAccessibility,
		FeatureStandardAccessibility,
		FeatureFastAccessibility,
		FeatureScreenshots,
		FeatureFloatingIndicator,
		FeatureSketchMode,
		FeatureDesignMode,
		FeatureComprehensiveA11y,
		FeatureTunneling,
		FeatureVisualRegression,
		FeatureChaosProxy,
	}
}

// HasFeature checks if the current license has access to a feature.
//
// BETA: Currently always returns true for all features.
// After beta, this will perform actual license validation.
func HasFeature(feature Feature) bool {
	// TODO: When licensing is enabled, uncomment this:
	// return currentLicense.Valid && hasFeature(currentLicense.Features, feature)

	// During beta, all features are unlocked
	return true
}

// RequireFeature checks if a feature is available, returning an error if not.
//
// BETA: Currently always returns nil.
// After beta, this will return descriptive errors with upgrade links.
func RequireFeature(feature Feature) error {
	// TODO: When licensing is enabled, implement proper error handling:
	// if !HasFeature(feature) {
	//     return &FeatureLockedError{
	//         Feature: feature,
	//         Message: fmt.Sprintf("%s requires a paid license", feature),
	//         UpgradeURL: "https://agnt.dev/pricing",
	//     }
	// }

	// During beta, all features are available
	return nil
}

// IsBeta returns true if we're in beta mode (all features unlocked).
func IsBeta() bool {
	return currentLicense.BetaMode
}

// GetLicense returns the current license information.
func GetLicense() *License {
	return currentLicense
}

// SetLicense sets the current license (for testing or future license loading).
func SetLicense(lic *License) {
	currentLicense = lic
}

// LoadLicense loads a license from a file path.
//
// BETA: Not implemented - returns unlocked beta license.
// TODO: Implement Ed25519 signature verification when licensing is enabled.
func LoadLicense(path string) (*License, error) {
	// TODO: Implement license loading:
	// - Read license file
	// - Verify Ed25519 signature
	// - Check expiration
	// - Parse features
	// - Return validated license

	// During beta, return fully unlocked license
	return currentLicense, nil
}

// FeatureLockedError is returned when a feature is not available in the current license.
type FeatureLockedError struct {
	Feature    Feature
	Message    string
	UpgradeURL string
}

func (e *FeatureLockedError) Error() string {
	return e.Message
}

// IsPremiumFeature returns true if the feature will require a license after beta.
func IsPremiumFeature(feature Feature) bool {
	switch feature {
	case FeatureSketchMode,
		FeatureDesignMode,
		FeatureComprehensiveA11y,
		FeatureTunneling,
		FeatureVisualRegression,
		FeatureChaosProxy:
		return true
	default:
		return false
	}
}

// GetFeatureDescription returns a human-readable description of a feature.
func GetFeatureDescription(feature Feature) string {
	descriptions := map[Feature]string{
		FeatureBasicProxy:            "Basic reverse proxy with traffic logging",
		FeatureProcessManagement:     "Process and script management",
		FeatureBasicAccessibility:    "Basic accessibility audit",
		FeatureStandardAccessibility: "Industry-standard axe-core accessibility audit",
		FeatureFastAccessibility:     "Fast accessibility improvements mode",
		FeatureScreenshots:           "Screenshot capture and logging",
		FeatureFloatingIndicator:     "Floating indicator panel for browser",
		FeatureSketchMode:            "Wireframing and sketch mode (Beta - will require license)",
		FeatureDesignMode:            "AI-assisted design iteration (Beta - will require license)",
		FeatureComprehensiveA11y:     "Comprehensive CSS-aware accessibility audit (Beta - will require license)",
		FeatureTunneling:             "Tunnel integration for mobile testing (Beta - will require license)",
		FeatureVisualRegression:      "Visual regression testing with baselines (Beta - will require license)",
		FeatureChaosProxy:            "Chaos engineering for network conditions (Beta - will require license)",
	}

	if desc, ok := descriptions[feature]; ok {
		return desc
	}
	return string(feature)
}

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/license"
)

// getEnvSigningKey reads the signing private key from the environment, used by
// `agnt license issue` when --key is not given (server/CI flow).
func getEnvSigningKey() string {
	return strings.TrimSpace(os.Getenv("AGNT_LICENSE_SIGNING_KEY"))
}

// activateCmd is the top-level activation entry point users reach for first.
var activateCmd = &cobra.Command{
	Use:   "activate <key>",
	Short: "Install an agnt Pro license key",
	Long: `Validate a license key against the embedded public key and install it.

The key is the base32 license blob issued for your purchase. It is verified
offline (no network) and stored per-user under XDG state.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runActivate(args[0])
	},
}

// licenseCmd groups status / deactivate / issue / keygen.
var licenseCmd = &cobra.Command{
	Use:   "license",
	Short: "Manage the agnt Pro license",
}

var licenseStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the installed license status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLicenseStatus()
	},
}

var licenseDeactivateCmd = &cobra.Command{
	Use:   "deactivate",
	Short: "Remove the installed license",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := license.Remove(); err != nil {
			return err
		}
		fmt.Println("License removed.")
		return nil
	},
}

var (
	issueKey   string
	issueEmail string
	issueCust  string
	issuePlan  string
	issueDays  int
	issueCaps  []string
)

var licenseIssueCmd = &cobra.Command{
	Use:   "issue",
	Short: "Mint a license blob with a signing private key (server/admin use)",
	Long: `Sign a license payload into a distributable blob.

This is the same operation a Stripe webhook performs server-side. The signing
private key must be kept secret and is never embedded in the client binary.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLicenseIssue()
	},
}

var licenseKeygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "Generate a new signing keypair (server/admin setup)",
	Long: `Print a fresh ECDSA keypair. Embed the public key in the client
(internal/license/pubkey.b32) and keep the private key secret on the signing
server. Run once per signing identity.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		priv, pub, err := license.GenerateKeypair()
		if err != nil {
			return err
		}
		fmt.Println("PRIVATE (keep secret, server-side only):")
		fmt.Println(priv)
		fmt.Println()
		fmt.Println("PUBLIC (embed in internal/license/pubkey.b32):")
		fmt.Println(pub)
		return nil
	},
}

func init() {
	licenseIssueCmd.Flags().StringVar(&issueKey, "key", "", "signing private key (base32); else AGNT_LICENSE_SIGNING_KEY")
	licenseIssueCmd.Flags().StringVar(&issueEmail, "email", "", "licensee email (required)")
	licenseIssueCmd.Flags().StringVar(&issueCust, "customer-id", "", "upstream customer id")
	licenseIssueCmd.Flags().StringVar(&issuePlan, "plan", "team", "plan label")
	licenseIssueCmd.Flags().IntVar(&issueDays, "days", 365, "days until expiry")
	licenseIssueCmd.Flags().StringSliceVar(&issueCaps, "caps", []string{
		string(license.CapWholeSite),
		string(license.CapAnalysisReport),
		string(license.CapComponentExtract),
		string(license.CapAdvancedTesting),
	}, "granted capability keys")

	licenseCmd.AddCommand(licenseStatusCmd)
	licenseCmd.AddCommand(licenseDeactivateCmd)
	licenseCmd.AddCommand(licenseIssueCmd)
	licenseCmd.AddCommand(licenseKeygenCmd)

	rootCmd.AddCommand(activateCmd)
	rootCmd.AddCommand(licenseCmd)
}

func runActivate(key string) error {
	payload, err := license.Validate(key)
	if err != nil {
		return fmt.Errorf("invalid license key: %w", err)
	}
	if err := license.Save(key); err != nil {
		return fmt.Errorf("storing license: %w", err)
	}
	st := license.Evaluate(payload, time.Now())
	fmt.Printf("License activated for %s (%s).\n", payload.Email, payload.Plan)
	printStatus(st)
	return nil
}

func runLicenseStatus() error {
	blob, err := license.Load()
	if err == license.ErrNoLicense {
		fmt.Println("No license installed. Run `agnt activate <key>` to enable Pro features.")
		return nil
	}
	if err != nil {
		return err
	}
	payload, err := license.Validate(blob)
	if err != nil {
		fmt.Println("Installed license is INVALID — re-run `agnt activate <key>` with a valid key.")
		return nil
	}
	printStatus(license.Evaluate(payload, time.Now()))
	return nil
}

func printStatus(st license.Status) {
	if st.Payload != nil {
		fmt.Printf("  Email:        %s\n", st.Payload.Email)
		fmt.Printf("  Plan:         %s\n", st.Payload.Plan)
		fmt.Printf("  Expiry:       %s\n", st.Payload.Expiry.Format("2006-01-02"))
		fmt.Printf("  Capabilities: %s\n", strings.Join(st.Payload.Capabilities, ", "))
	}
	fmt.Printf("  State:        %s\n", st.State)
	switch st.State {
	case license.StateValid:
		fmt.Printf("  Days left:    %d\n", st.DaysLeft)
	case license.StateGrace:
		fmt.Printf("  GRACE:        expired; Pro works for %d more day(s). Renew soon.\n", st.DaysLeft)
	case license.StateExpired:
		fmt.Println("  EXPIRED:      Pro features are blocked. Renew and re-activate.")
	}
}

func runLicenseIssue() error {
	key := issueKey
	if key == "" {
		key = getEnvSigningKey()
	}
	if key == "" {
		return fmt.Errorf("no signing key: pass --key or set AGNT_LICENSE_SIGNING_KEY")
	}
	if issueEmail == "" {
		return fmt.Errorf("--email is required")
	}
	nowT := time.Now().UTC()
	p := &license.Payload{
		Email:        issueEmail,
		CustomerID:   issueCust,
		Plan:         issuePlan,
		IssuedAt:     nowT,
		Expiry:       nowT.Add(time.Duration(issueDays) * 24 * time.Hour),
		Capabilities: issueCaps,
	}
	blob, err := license.Mint(key, p)
	if err != nil {
		return fmt.Errorf("minting license: %w", err)
	}
	fmt.Println(blob)
	return nil
}

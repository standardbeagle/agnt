package license

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/hyperboloide/lk"
)

// embeddedPubKeyB32 is the base32-encoded ECDSA public key shipped in the
// binary. The matching private key is the signing key and never ships — see
// docs/superpowers/specs/2026-06-08-self-hosted-licensing-design.md.
//
//go:embed pubkey.b32
var embeddedPubKeyB32 string

// verifyKey is the public key used to check license signatures. It defaults to
// the embedded production key and is swappable in tests via SetVerifyKeyForTest
// so the suite signs with an ephemeral keypair instead of the real one.
var verifyKey *lk.PublicKey

func init() {
	k, err := lk.PublicKeyFromB32String(strings.TrimSpace(embeddedPubKeyB32))
	if err != nil {
		// A malformed embedded key is a build-time mistake, not a runtime
		// condition — fail loud so it is caught before shipping.
		panic(fmt.Sprintf("license: embedded public key is invalid: %v", err))
	}
	verifyKey = k
}

// SetVerifyKeyForTest overrides the verification key and returns a restore
// func. Test-only: production code never calls it.
func SetVerifyKeyForTest(k *lk.PublicKey) (restore func()) {
	prev := verifyKey
	verifyKey = k
	return func() { verifyKey = prev }
}

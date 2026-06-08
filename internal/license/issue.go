package license

import (
	"strings"

	"github.com/hyperboloide/lk"
)

// Mint signs a payload into a license blob using the base32-encoded private
// signing key, returning the base32 blob handed to a customer.
//
// This is the integration seam for the Stripe webhook → license issuance flow:
// a future self-hosted service builds a Payload from the customer record and
// calls Mint with the signing key kept server-side. Kept client-side too for
// manual issuance (`agnt license issue`) and tests. The private key must NEVER
// be embedded in or distributed with the client binary.
func Mint(privKeyB32 string, p *Payload) (string, error) {
	priv, err := lk.PrivateKeyFromB32String(strings.TrimSpace(privKeyB32))
	if err != nil {
		return "", err
	}
	data, err := p.marshal()
	if err != nil {
		return "", err
	}
	lic, err := lk.NewLicense(priv, data)
	if err != nil {
		return "", err
	}
	return lic.ToB32String()
}

// GenerateKeypair returns a fresh (private, public) base32 keypair for operator
// / server setup (`agnt license keygen`). The public half is embedded in the
// client; the private half is the signing key.
func GenerateKeypair() (privB32, pubB32 string, err error) {
	priv, err := lk.NewPrivateKey()
	if err != nil {
		return "", "", err
	}
	privB32, err = priv.ToB32String()
	if err != nil {
		return "", "", err
	}
	return privB32, priv.GetPublicKey().ToB32String(), nil
}

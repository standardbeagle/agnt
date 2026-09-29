package platform

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

var errExit = errors.New("exit status 1")

func TestParseTailscaleCertDomain(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"https enabled for this node",
			`{"Self":{"DNSName":"build1.tnet.ts.net."},"CertDomains":["build1.tnet.ts.net"]}`, "build1.tnet.ts.net"},
		{"https off: no CertDomains",
			`{"Self":{"DNSName":"build1.tnet.ts.net."}}`, ""},
		{"https off: empty CertDomains",
			`{"Self":{"DNSName":"build1.tnet.ts.net."},"CertDomains":[]}`, ""},
		{"a cert domain that is not this node's name",
			`{"Self":{"DNSName":"build1.tnet.ts.net."},"CertDomains":["other.tnet.ts.net"]}`, ""},
		{"no MagicDNS name",
			`{"Self":{"DNSName":""},"CertDomains":[""]}`, ""},
		{"case differs", `{"Self":{"DNSName":"Build1.TNet.ts.net."},"CertDomains":["build1.tnet.ts.net"]}`, "build1.tnet.ts.net"},
		{"malformed", `not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTailscaleCertDomain([]byte(tt.input)); got != tt.want {
				t.Errorf("parseTailscaleCertDomain = %q, want %q", got, tt.want)
			}
		})
	}
}

// selfSignedPEM returns a cert and its key as `tailscale cert --cert-file=-
// --key-file=-` prints them: the certificate first, then the key.
func selfSignedPEM(t *testing.T, host string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestParseTailscaleCertPair(t *testing.T) {
	notAfter := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	certPEM, keyPEM := selfSignedPEM(t, "build1.tnet.ts.net", notAfter)

	cert, err := parseTailscaleCertPair(append(append([]byte{}, certPEM...), keyPEM...))
	if err != nil {
		t.Fatalf("cert then key on one stream: %v", err)
	}
	if cert.Leaf == nil || !cert.Leaf.NotAfter.Equal(notAfter) {
		t.Fatalf("Leaf must be parsed so expiry can drive renewal, got %+v", cert.Leaf)
	}
	if got := cert.Leaf.DNSNames; len(got) != 1 || got[0] != "build1.tnet.ts.net" {
		t.Errorf("Leaf.DNSNames = %v", got)
	}

	for name, out := range map[string][]byte{
		"key missing":  certPEM,
		"cert missing": keyPEM,
		"empty":        nil,
	} {
		if _, err := parseTailscaleCertPair(out); err == nil {
			t.Errorf("%s: parsed a pair that is not one", name)
		}
	}
}

// The error names the fix, because "cert access denied" is what every
// non-root user sees until they are made tailscale's operator.
func TestTailscaleCertError(t *testing.T) {
	err := tailscaleCertError("build1.tnet.ts.net", []byte("Access denied: cert access denied\n\nUse 'sudo tailscale cert ...'.\n"), errExit)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, want := range []string{"cert access denied", "tailscale set --operator="} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must contain %q", msg, want)
		}
	}
	other := tailscaleCertError("build1.tnet.ts.net", []byte("some other failure\n"), errExit).Error()
	if !strings.Contains(other, "some other failure") || strings.Contains(other, "--operator") {
		t.Errorf("other failures pass their first line through without the operator hint: %q", other)
	}
	if bare := tailscaleCertError("build1.tnet.ts.net", nil, errExit).Error(); !strings.Contains(bare, errExit.Error()) {
		t.Errorf("no stderr: the exec error must still be named: %q", bare)
	}
}

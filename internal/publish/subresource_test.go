package publish

import "testing"

// TestSubresourceSignerRoundTrip is the baseline: a signature the signer minted
// verifies against exactly the tuple it was minted for.
func TestSubresourceSignerRoundTrip(t *testing.T) {
	s, err := NewSubresourceSigner()
	if err != nil {
		t.Fatalf("NewSubresourceSigner: %v", err)
	}
	sig := s.Sign("share-1", "https://demo.example.com/a.css", 1)
	if sig == "" {
		t.Fatal("Sign returned an empty signature")
	}
	if !s.Verify("share-1", "https://demo.example.com/a.css", 1, sig) {
		t.Fatal("a freshly minted signature failed to verify")
	}
}

// TestSubresourceSignerRejectsEveryAlteredField is INV-16's binding: change any
// bound field and the signature must die. Each row is a real attack — a viewer
// pointing the route at a URL of their choosing, replaying another share's
// reference under their own token, or claiming a shallower depth to walk the
// nesting chain deeper than the cap allows.
func TestSubresourceSignerRejectsEveryAlteredField(t *testing.T) {
	s, err := NewSubresourceSigner()
	if err != nil {
		t.Fatalf("NewSubresourceSigner: %v", err)
	}
	const (
		shareID = "share-1"
		absURL  = "https://demo.example.com/a.css"
		depth   = 1
	)
	sig := s.Sign(shareID, absURL, depth)

	cases := []struct {
		name    string
		shareID string
		absURL  string
		depth   int
		sig     string
	}{
		{"foreign share", "share-2", absURL, depth, sig},
		{"viewer-chosen url", shareID, "https://evil.example.com/x.css", depth, sig},
		{"metadata url", shareID, "https://169.254.169.254/latest/meta-data/", depth, sig},
		{"depth downgrade", shareID, absURL, 2, sig},
		{"empty signature", shareID, absURL, depth, ""},
		{"forged signature", shareID, absURL, depth, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"truncated signature", shareID, absURL, depth, sig[:len(sig)-1]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if s.Verify(c.shareID, c.absURL, c.depth, c.sig) {
				t.Fatalf("Verify accepted an altered reference (%s)", c.name)
			}
		})
	}
}

// TestSubresourceSignerFieldsAreLengthPrefixed pins the concatenation defence:
// without length prefixes, ("ab","c") and ("a","bc") MAC identically, so a URL
// signed for one share would verify under a prefix-shifted sibling.
func TestSubresourceSignerFieldsAreLengthPrefixed(t *testing.T) {
	s, err := NewSubresourceSigner()
	if err != nil {
		t.Fatalf("NewSubresourceSigner: %v", err)
	}
	a := s.Sign("ab", "chttps://x/", 1)
	b := s.Sign("a", "bchttps://x/", 1)
	if a == b {
		t.Fatal("shifting a byte between the share id and the URL produced the same MAC (fields are not length-prefixed)")
	}
}

// TestSubresourceSignerKeysAreDistinct: two signers must not share a key, or a
// restart would keep honouring references minted before it (and a test could not
// tell a real MAC from a constant).
func TestSubresourceSignerKeysAreDistinct(t *testing.T) {
	a, err := NewSubresourceSigner()
	if err != nil {
		t.Fatalf("NewSubresourceSigner: %v", err)
	}
	b, err := NewSubresourceSigner()
	if err != nil {
		t.Fatalf("NewSubresourceSigner: %v", err)
	}
	sig := a.Sign("share-1", "https://demo.example.com/a.css", 1)
	if b.Verify("share-1", "https://demo.example.com/a.css", 1, sig) {
		t.Fatal("a second signer verified the first signer's MAC — the key is not per-instance")
	}
}

// TestNilSubresourceSignerFailsClosed: a handler that could not obtain a key
// must refuse everything rather than degrade into an unbound route.
func TestNilSubresourceSignerFailsClosed(t *testing.T) {
	var s *SubresourceSigner
	if got := s.Sign("share-1", "https://demo.example.com/a.css", 1); got != "" {
		t.Fatalf("nil signer minted %q", got)
	}
	if s.Verify("share-1", "https://demo.example.com/a.css", 1, "") {
		t.Fatal("nil signer verified an empty signature")
	}
	if s.Verify("share-1", "https://demo.example.com/a.css", 1, "anything") {
		t.Fatal("nil signer verified a signature")
	}
}

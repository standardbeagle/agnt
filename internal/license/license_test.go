package license

import (
	"errors"
	"testing"
	"time"

	"github.com/hyperboloide/lk"
)

// testKeypair generates an ephemeral keypair and points verification at it,
// returning the private-key b32 for minting. The restore is registered on t.
func testKeypair(t *testing.T) (privB32 string) {
	t.Helper()
	priv, err := lk.NewPrivateKey()
	if err != nil {
		t.Fatalf("NewPrivateKey: %v", err)
	}
	privB32, err = priv.ToB32String()
	if err != nil {
		t.Fatalf("ToB32String: %v", err)
	}
	restore := SetVerifyKeyForTest(priv.GetPublicKey())
	t.Cleanup(restore)
	return privB32
}

func samplePayload(expiry time.Time) *Payload {
	return &Payload{
		Email:        "team@example.com",
		CustomerID:   "cus_123",
		Plan:         "team",
		IssuedAt:     expiry.Add(-365 * 24 * time.Hour),
		Expiry:       expiry,
		Capabilities: []string{string(CapWholeSite), string(CapAnalysisReport)},
	}
}

func TestMintValidateRoundTrip(t *testing.T) {
	priv := testKeypair(t)
	want := samplePayload(time.Now().Add(30 * 24 * time.Hour))

	blob, err := Mint(priv, want)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	got, err := Validate(blob)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Email != want.Email || got.CustomerID != want.CustomerID || got.Plan != want.Plan {
		t.Errorf("payload mismatch: got %+v want %+v", got, want)
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("expiry mismatch: got %v want %v", got.Expiry, want.Expiry)
	}
	if !got.Grants(CapWholeSite) || got.Grants(CapAdvancedTesting) {
		t.Errorf("capabilities wrong: %v", got.Capabilities)
	}
}

func TestValidateRejectsTampered(t *testing.T) {
	priv := testKeypair(t)
	blob, err := Mint(priv, samplePayload(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// Flip a character in the middle of the blob.
	b := []byte(blob)
	mid := len(b) / 2
	if b[mid] == 'A' {
		b[mid] = 'B'
	} else {
		b[mid] = 'A'
	}
	if _, err := Validate(string(b)); !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("tampered blob: want ErrInvalidLicense, got %v", err)
	}
}

func TestValidateRejectsWrongKey(t *testing.T) {
	// Mint with one key, verify against a different embedded key.
	other, err := lk.NewPrivateKey()
	if err != nil {
		t.Fatalf("NewPrivateKey: %v", err)
	}
	otherB32, _ := other.ToB32String()
	blob, err := Mint(otherB32, samplePayload(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// Verification key is a different ephemeral key.
	testKeypair(t)
	if _, err := Validate(blob); !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("wrong-key blob: want ErrInvalidLicense, got %v", err)
	}
}

func TestValidateRejectsGarbage(t *testing.T) {
	testKeypair(t)
	for _, s := range []string{"", "not-a-license", "!!!", "AAAA"} {
		if _, err := Validate(s); !errors.Is(err, ErrInvalidLicense) {
			t.Errorf("garbage %q: want ErrInvalidLicense, got %v", s, err)
		}
	}
}

func TestEvaluateStates(t *testing.T) {
	base := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	expiry := base.Add(10 * 24 * time.Hour)
	p := samplePayload(expiry)

	cases := []struct {
		name      string
		now       time.Time
		wantState State
		wantDays  int
	}{
		{"valid mid-term", base, StateValid, 10},
		{"valid 1 day left", expiry.Add(-12 * time.Hour), StateValid, 1},
		{"just expired -> grace", expiry.Add(time.Minute), StateGrace, 14},
		{"grace last day", expiry.Add(GracePeriod - time.Hour), StateGrace, 1},
		{"grace boundary -> expired", expiry.Add(GracePeriod), StateExpired, 0},
		{"long expired", expiry.Add(GracePeriod + 100*24*time.Hour), StateExpired, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Evaluate(p, c.now)
			if s.State != c.wantState {
				t.Errorf("state: got %v want %v", s.State, c.wantState)
			}
			if s.DaysLeft != c.wantDays {
				t.Errorf("daysLeft: got %d want %d", s.DaysLeft, c.wantDays)
			}
		})
	}
}

func TestEvaluateNilPayloadIsMissing(t *testing.T) {
	if s := Evaluate(nil, time.Now()); s.State != StateMissing {
		t.Errorf("nil payload: want Missing, got %v", s.State)
	}
}

func TestEmbeddedKeyParses(t *testing.T) {
	// init() would have panicked on a bad embedded key; assert it loaded.
	if verifyKey == nil {
		t.Fatal("embedded verifyKey is nil")
	}
}

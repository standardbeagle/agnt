package license

import (
	"errors"
	"strings"
	"time"

	"github.com/hyperboloide/lk"
)

// GracePeriod is how long Pro features keep working past Expiry before they
// hard-block. Warnings surface throughout the window.
const GracePeriod = 14 * 24 * time.Hour

// State is the serving state of a stored license.
type State int

const (
	// StateMissing means no license blob is stored.
	StateMissing State = iota
	// StateInvalid means a blob is stored but failed signature/parse.
	StateInvalid
	// StateValid means the license is signed and within its paid term.
	StateValid
	// StateGrace means past Expiry but within GracePeriod — Pro still serves,
	// with a warning.
	StateGrace
	// StateExpired means past Expiry + GracePeriod — Pro blocks.
	StateExpired
)

func (s State) String() string {
	switch s {
	case StateMissing:
		return "missing"
	case StateInvalid:
		return "invalid"
	case StateValid:
		return "valid"
	case StateGrace:
		return "grace"
	case StateExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// serving reports whether Pro features may run in this state.
func (s State) serving() bool { return s == StateValid || s == StateGrace }

// ErrInvalidLicense is returned by Validate when a blob fails verification.
var ErrInvalidLicense = errors.New("license: signature verification failed")

// Validate verifies a license blob against the embedded public key and decodes
// its payload. A tampered, wrong-key, or malformed blob yields ErrInvalidLicense
// (wrapped). The blob is the base32 string handed to the customer.
func Validate(blob string) (*Payload, error) {
	lic, err := lk.LicenseFromB32String(strings.TrimSpace(blob))
	if err != nil {
		return nil, errors.Join(ErrInvalidLicense, err)
	}
	ok, err := lic.Verify(verifyKey)
	if err != nil {
		return nil, errors.Join(ErrInvalidLicense, err)
	}
	if !ok {
		return nil, ErrInvalidLicense
	}
	payload, err := unmarshalPayload(lic.Data)
	if err != nil {
		return nil, errors.Join(ErrInvalidLicense, err)
	}
	return payload, nil
}

// Status is the evaluated view of a license at a point in time.
type Status struct {
	State    State
	Payload  *Payload // nil when State is Missing or Invalid
	DaysLeft int      // days until block: to Expiry when Valid, to grace-end when Grace; 0 otherwise
}

// Evaluate computes serving state from a payload and the current time. A nil
// payload is StateMissing. Clock is trusted (good-faith compliance only).
func Evaluate(p *Payload, now time.Time) Status {
	if p == nil {
		return Status{State: StateMissing}
	}
	graceEnd := p.Expiry.Add(GracePeriod)
	switch {
	case now.Before(p.Expiry):
		return Status{State: StateValid, Payload: p, DaysLeft: daysBetween(now, p.Expiry)}
	case now.Before(graceEnd):
		return Status{State: StateGrace, Payload: p, DaysLeft: daysBetween(now, graceEnd)}
	default:
		return Status{State: StateExpired, Payload: p}
	}
}

// daysBetween returns whole days from now until t, rounded up, never negative.
func daysBetween(now, t time.Time) int {
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	days := int(d / (24 * time.Hour))
	if d%(24*time.Hour) != 0 {
		days++
	}
	return days
}

package finding

import "fmt"

// Profile selects which checks run: bug, changed, release, or full.
type Profile string

const (
	ProfileBug     Profile = "bug"
	ProfileChanged Profile = "changed"
	ProfileRelease Profile = "release"
	ProfileFull    Profile = "full"
)

// ParseProfile parses s into a Profile, rejecting unknown values with an
// error naming the value.
func ParseProfile(s string) (Profile, error) {
	switch Profile(s) {
	case ProfileBug, ProfileChanged, ProfileRelease, ProfileFull:
		return Profile(s), nil
	default:
		return "", fmt.Errorf("unknown profile %q: want one of bug, changed, release, full", s)
	}
}

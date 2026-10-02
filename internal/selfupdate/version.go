package selfupdate

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Version is a inari release version, vMAJOR.MINOR.PATCH. Pre-release suffixes
// are rejected: inari does not publish them, and refusing them means a tag that
// sorts ambiguously is never installed.
type Version struct {
	major, minor, patch int
}

// ParseVersion accepts only the canonical form, so String round-trips exactly.
// Release URLs are built from String, which keeps a tag read from the network
// from ever contributing anything but digits and dots to a download path.
func ParseVersion(s string) (Version, error) {
	rest, ok := strings.CutPrefix(s, "v")
	parts := strings.Split(rest, ".")
	if !ok || len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid release version %q", s)
	}
	var n [3]int
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || part != strconv.Itoa(value) {
			return Version{}, fmt.Errorf("invalid release version %q", s)
		}
		n[i] = value
	}
	return Version{major: n[0], minor: n[1], patch: n[2]}, nil
}

func (v Version) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch)
}

// Compare returns -1, 0, or +1 as v is older than, equal to, or newer than o.
func (v Version) Compare(o Version) int {
	return cmp.Or(
		cmp.Compare(v.major, o.major),
		cmp.Compare(v.minor, o.minor),
		cmp.Compare(v.patch, o.patch),
	)
}

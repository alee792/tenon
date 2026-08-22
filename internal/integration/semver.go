package integration

import (
	"strconv"
	"strings"
)

// semver is a parsed semantic version: the numeric core plus an optional
// prerelease. Build metadata is parsed and discarded because it does not
// affect precedence. Tenon compatibility ranges compare versions with this
// ordering; nothing here mutates a version string.
type semver struct {
	major, minor, patch int
	pre                 string
}

// parseSemver parses an exact MAJOR.MINOR.PATCH version with an optional
// -prerelease and +build. It rejects a missing component, a leading zero, and
// any non-numeric core component: the manifest pins an exact semantic version,
// not a loose one.
func parseSemver(s string) (semver, bool) {
	if s == "" {
		return semver{}, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
		if pre == "" {
			return semver{}, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var nums [3]int
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{nums[0], nums[1], nums[2], pre}, true
}

// compareSemver orders two versions by SemVer precedence: the numeric core
// first, then a prerelease as lower than its release, then prerelease
// identifiers field by field.
func compareSemver(a, b semver) int {
	if c := cmpInt(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpInt(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpInt(a.patch, b.patch); c != 0 {
		return c
	}
	switch {
	case a.pre == "" && b.pre == "":
		return 0
	case a.pre == "":
		return 1 // a release outranks any prerelease of the same core
	case b.pre == "":
		return -1
	default:
		return comparePre(a.pre, b.pre)
	}
}

// comparePre compares two prerelease strings by dot-separated identifier:
// numeric identifiers compare numerically and rank below alphanumeric ones,
// and a shorter prefix-equal prerelease ranks lower.
func comparePre(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aNum := strconv.Atoi(as[i])
		bn, bNum := strconv.Atoi(bs[i])
		switch {
		case aNum == nil && bNum == nil:
			if c := cmpInt(an, bn); c != 0 {
				return c
			}
		case aNum == nil:
			return -1 // numeric identifiers have lower precedence
		case bNum == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(as), len(bs))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

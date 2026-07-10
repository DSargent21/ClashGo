package updater

import (
	"strconv"
	"strings"
)

// normalizeVersion strips a leading "v" / "V" so callers can use either
// "v0.1.0" or "0.1.0" interchangeably. Returns "" for empty input.
func normalizeVersion(raw string) string {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return v
}

// CompareVersions returns -1, 0, +1.
//
// Pre-releases sort BEFORE the corresponding non-prerelease (matching
// semver.org §11). Within the same major.minor.patch, pre-release
// identifiers are compared lexically with numeric segments compared
// numerically; identifiers that are purely numeric sort lower than
// non-numeric ones.
//
// Examples:
//
//	0.1.0-beta < 0.1.0
//	0.2.0-alpha < 0.2.0-beta (alpha lex < beta)
//	0.2.0-1   < 0.2.0-2
//	0.2.0-dev < 0.2.0-rc.1?  No — non-numeric identifiers come first in pre-release
//	  per semver.org, so a.0 < a.b
//
// We honor the semver.org rule: a pre-release identifier that consists
// of only digits compares numerically; identifiers with letters or
// hyphens compare lexically. Numeric identifiers ALWAYS have lower
// precedence than non-numeric.
func CompareVersions(a, b string) int {
	a = normalizeVersion(a)
	b = normalizeVersion(b)
	if a == b {
		return 0
	}

	aCore, aPre := splitPre(a)
	bCore, bPre := splitPre(b)

	// Compare core (major.minor.patch) numerically.
	if c := compareCore(aCore, bCore); c != 0 {
		return c
	}

	// Core equal — pre-release decides.
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		// non-pre > pre
		return +1
	case bPre == "":
		return -1
	default:
		return comparePre(aPre, bPre)
	}
}

// splitPre splits "0.1.0-beta.1" → ("0.1.0", "beta.1").
func splitPre(v string) (core, pre string) {
	if !strings.Contains(v, "-") {
		return v, ""
	}
	idx := strings.Index(v, "-")
	return v[:idx], v[idx+1:]
}

// compareCore compares "1.2.3" dotted strings segment by segment
// numerically. Mismatched length: extra segments are treated as 0
// on the shorter side (so "1.2" == "1.2.0").
func compareCore(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	n := len(aParts)
	if len(bParts) > n {
		n = len(bParts)
	}
	for i := 0; i < n; i++ {
		var ai, bi int
		if i < len(aParts) {
			ai, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bi, _ = strconv.Atoi(bParts[i])
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return +1
		}
	}
	return 0
}

// comparePre compares two dot-separated pre-release identifiers per
// semver.org §11.
//
//  1. Identifiers consisting of only digits are compared numerically.
//  2. Identifiers with letters or hyphens are compared lexically (ASCII).
//  3. Numeric identifiers always have lower precedence than non-numeric.
//  4. A larger set of identifiers sorts higher than a smaller set when
//     all preceding identifiers are equal.
func comparePre(a, b string) int {
	aIDs := strings.Split(a, ".")
	bIDs := strings.Split(b, ".")

	n := len(aIDs)
	if len(bIDs) > n {
		n = len(bIDs)
	}
	for i := 0; i < n; i++ {
		var ai, bi string
		if i < len(aIDs) {
			ai = aIDs[i]
		}
		if i < len(bIDs) {
			bi = bIDs[i]
		}

		// "missing identifier" implies the shorter side is "lower"
		// (fewer identifiers ranks higher... wait, semver 11.4 says
		// "A larger set of pre-release fields has a higher precedence
		// than a smaller set, if all of the preceding identifiers are
		// equal."). So if one side is missing AND the other has more,
		// shorter side wins.
		if ai == "" && bi != "" {
			return -1
		}
		if ai != "" && bi == "" {
			return +1
		}

		aNum, aIsNum := parseInt(ai)
		bNum, bIsNum := parseInt(bi)

		switch {
		case aIsNum && bIsNum:
			if aNum < bNum {
				return -1
			}
			if aNum > bNum {
				return +1
			}
		case aIsNum && !bIsNum:
			// numeric < non-numeric
			return -1
		case !aIsNum && bIsNum:
			return +1
		default:
			if ai < bi {
				return -1
			}
			if ai > bi {
				return +1
			}
		}
	}
	return 0
}

// parseInt returns (value, true) if s is purely digits (no sign, no
// letters). Non-numeric identifiers like "beta" or "rc1" return (0, false).
func parseInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, _ := strconv.Atoi(s)
	return n, true
}

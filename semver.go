package main

import (
	"fmt"
	"strconv"
	"strings"
)

type semver struct {
	Major, Minor, Patch int
}

func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return semver{}, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return semver{}, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return semver{}, false
	}
	patch, err := strconv.Atoi(strings.SplitN(parts[2], "-", 2)[0])
	if err != nil {
		return semver{}, false
	}
	return semver{major, minor, patch}, true
}

func (v semver) Less(other semver) bool {
	if v.Major != other.Major {
		return v.Major < other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor < other.Minor
	}
	return v.Patch < other.Patch
}

func (v semver) GreaterOrEqual(other semver) bool {
	return !v.Less(other)
}

// satisfiesConstraint checks if version satisfies a constraint string.
// Supported: ">=1.0.0" or ">=1.0.0 <2.0.0".
func satisfiesConstraint(version, constraint string) (bool, error) {
	v, ok := parseSemver(version)
	if !ok {
		return false, fmt.Errorf("cannot parse version %q", version)
	}

	parts := strings.Fields(constraint)
	for _, part := range parts {
		if strings.HasPrefix(part, ">=") {
			min, ok := parseSemver(strings.TrimPrefix(part, ">="))
			if !ok {
				return false, fmt.Errorf("cannot parse constraint %q", part)
			}
			if !v.GreaterOrEqual(min) {
				return false, nil
			}
		} else if strings.HasPrefix(part, "<") {
			max, ok := parseSemver(strings.TrimPrefix(part, "<"))
			if !ok {
				return false, fmt.Errorf("cannot parse constraint %q", part)
			}
			if !v.Less(max) {
				return false, nil
			}
		} else {
			return false, fmt.Errorf("unsupported constraint operator in %q", part)
		}
	}
	return true, nil
}

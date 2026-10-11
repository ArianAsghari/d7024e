package registry

import (
	"fmt"
	"strconv"
	"strings"
)

const Latest = "latest"

type Version struct {
	Major, Minor, Patch uint32
}

func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: %q is not MAJOR.MINOR.PATCH", ErrInvalidVersion, s)
	}
	var n [3]uint32
	for i, p := range parts {
		x, err := parseComponent(p)
		if err != nil {
			return Version{}, fmt.Errorf("%w: %q: %v", ErrInvalidVersion, s, err)
		}
		n[i] = x
	}
	return Version{n[0], n[1], n[2]}, nil
}

func parseComponent(p string) (uint32, error) {
	if p == "" {
		return 0, fmt.Errorf("empty component")
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return 0, fmt.Errorf("component %q is not a decimal number", p)
		}
	}
	if len(p) > 1 && p[0] == '0' {
		return 0, fmt.Errorf("component %q has a leading zero", p)
	}
	x, err := strconv.ParseUint(p, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("component %q is too large", p)
	}
	return uint32(x), nil
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

func (v Version) Compare(o Version) int {
	if c := cmpUint32(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpUint32(v.Minor, o.Minor); c != 0 {
		return c
	}
	return cmpUint32(v.Patch, o.Patch)
}

func (v Version) Less(o Version) bool {
	return v.Compare(o) < 0
}

func (v Version) MarshalText() ([]byte, error) {
	return []byte(v.String()), nil
}

func (v *Version) UnmarshalText(text []byte) error {
	parsed, err := ParseVersion(string(text))
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

func cmpUint32(a, b uint32) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

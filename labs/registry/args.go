package registry

import (
	"fmt"
	"strings"
)

const publishUsage = "usage: publish [--force] [--prev=PREVIOUS-VERSION] DOMAIN:PACKAGE:VERSION FILENAME"

type PackageRef struct {
	Domain  string
	Package string
}

func (p PackageRef) String() string {
	return p.Domain + ":" + p.Package
}

type Spec struct {
	PackageRef
	Version Version
	Latest  bool
}

func (s Spec) String() string {
	if s.Latest {
		return s.PackageRef.String() + ":" + Latest
	}
	return s.PackageRef.String() + ":" + s.Version.String()
}

type PublishArgs struct {
	Force    bool
	Prev     *Version
	Spec     Spec
	Filename string
}

func ParsePackage(s string) (PackageRef, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return PackageRef{}, fmt.Errorf("%w: %q is not DOMAIN:PACKAGE", ErrBadArgs, s)
	}
	return newPackageRef(s, parts[0], parts[1])
}

func ParseSpec(s string) (Spec, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return Spec{}, fmt.Errorf("%w: %q is not DOMAIN:PACKAGE:VERSION", ErrBadArgs, s)
	}
	ref, err := newPackageRef(s, parts[0], parts[1])
	if err != nil {
		return Spec{}, err
	}
	if parts[2] == Latest {
		return Spec{PackageRef: ref, Latest: true}, nil
	}
	v, err := ParseVersion(parts[2])
	if err != nil {
		return Spec{}, err
	}
	return Spec{PackageRef: ref, Version: v}, nil
}

func newPackageRef(whole, domain, pkg string) (PackageRef, error) {
	if err := ValidateDomainName(domain); err != nil {
		return PackageRef{}, fmt.Errorf("%q: %w", whole, err)
	}
	if err := ValidatePackageName(pkg); err != nil {
		return PackageRef{}, fmt.Errorf("%q: %w", whole, err)
	}
	return PackageRef{Domain: domain, Package: pkg}, nil
}

func ParsePublishArgs(args []string) (PublishArgs, error) {
	var out PublishArgs
	var positional []string
	havePrev := false
	setPrev := func(value string) error {
		if havePrev {
			return fmt.Errorf("%w: --prev given twice", ErrBadArgs)
		}
		v, err := ParseVersion(value)
		if err != nil {
			return fmt.Errorf("--prev: %w", err)
		}
		havePrev = true
		out.Prev = &v
		return nil
	}
	literal := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case literal:
			positional = append(positional, a)
		case a == "--":
			literal = true
		case a == "--force":
			out.Force = true
		case a == "--prev":
			if i+1 >= len(args) {
				return PublishArgs{}, fmt.Errorf("%w: --prev needs a version", ErrBadArgs)
			}
			i++
			if err := setPrev(args[i]); err != nil {
				return PublishArgs{}, err
			}
		case strings.HasPrefix(a, "--prev="):
			if err := setPrev(strings.TrimPrefix(a, "--prev=")); err != nil {
				return PublishArgs{}, err
			}
		case strings.HasPrefix(a, "-") && a != "-":
			return PublishArgs{}, fmt.Errorf("%w: unknown flag %q; %s", ErrBadArgs, a, publishUsage)
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 2 {
		return PublishArgs{}, fmt.Errorf("%w: want DOMAIN:PACKAGE:VERSION and FILENAME, got %d arguments; %s", ErrBadArgs, len(positional), publishUsage)
	}
	spec, err := ParseSpec(positional[0])
	if err != nil {
		return PublishArgs{}, err
	}
	if spec.Latest {
		return PublishArgs{}, fmt.Errorf("%w: %q is reserved and cannot be published", ErrBadArgs, Latest)
	}
	if positional[1] == "" {
		return PublishArgs{}, fmt.Errorf("%w: empty filename", ErrBadArgs)
	}
	out.Spec = spec
	out.Filename = positional[1]
	return out, nil
}

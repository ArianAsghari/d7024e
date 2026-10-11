package registry

import (
	"errors"
	"testing"
)

func TestParsePublishArgs(t *testing.T) {
	v := func(s string) *Version { x := mustVersion(t, s); return &x }
	spec := func(d, p, ver string) Spec {
		return Spec{PackageRef: PackageRef{Domain: d, Package: p}, Version: mustVersion(t, ver)}
	}
	cases := []struct {
		name string
		args []string
		want PublishArgs
	}{
		{"plain", []string{"rfin.ch:java-pair:1.0.0", "pair.jar"}, PublishArgs{Spec: spec("rfin.ch", "java-pair", "1.0.0"), Filename: "pair.jar"}},
		{"force first", []string{"--force", "rfin.ch:java-pair:1.0.0", "pair.jar"}, PublishArgs{Force: true, Spec: spec("rfin.ch", "java-pair", "1.0.0"), Filename: "pair.jar"}},
		{"force last", []string{"rfin.ch:java-pair:1.0.0", "pair.jar", "--force"}, PublishArgs{Force: true, Spec: spec("rfin.ch", "java-pair", "1.0.0"), Filename: "pair.jar"}},
		{"force between", []string{"rfin.ch:java-pair:1.0.0", "--force", "pair.jar"}, PublishArgs{Force: true, Spec: spec("rfin.ch", "java-pair", "1.0.0"), Filename: "pair.jar"}},
		{"prev with equals", []string{"--prev=1.2.3", "a.com:p:2.0.0", "f"}, PublishArgs{Prev: v("1.2.3"), Spec: spec("a.com", "p", "2.0.0"), Filename: "f"}},
		{"prev with separate value", []string{"--prev", "1.2.3", "a.com:p:2.0.0", "f"}, PublishArgs{Prev: v("1.2.3"), Spec: spec("a.com", "p", "2.0.0"), Filename: "f"}},
		{"prev after the positionals", []string{"a.com:p:2.0.0", "f", "--prev=1.2.3"}, PublishArgs{Prev: v("1.2.3"), Spec: spec("a.com", "p", "2.0.0"), Filename: "f"}},
		{"force and prev", []string{"--force", "--prev=0.0.1", "a.com:p:0.0.1", "f"}, PublishArgs{Force: true, Prev: v("0.0.1"), Spec: spec("a.com", "p", "0.0.1"), Filename: "f"}},
		{"force twice", []string{"--force", "--force", "a.com:p:1.0.0", "f"}, PublishArgs{Force: true, Spec: spec("a.com", "p", "1.0.0"), Filename: "f"}},
		{"filename with a path and colons", []string{"a.com:p:1.0.0", "/tmp/dir/p:1.jar"}, PublishArgs{Spec: spec("a.com", "p", "1.0.0"), Filename: "/tmp/dir/p:1.jar"}},
		{"double dash allows a filename that looks like a flag", []string{"a.com:p:1.0.0", "--", "--force"}, PublishArgs{Spec: spec("a.com", "p", "1.0.0"), Filename: "--force"}},
		{"flags before the double dash still count", []string{"--force", "--", "a.com:p:1.0.0", "-x"}, PublishArgs{Force: true, Spec: spec("a.com", "p", "1.0.0"), Filename: "-x"}},
		{"a lone dash is a filename", []string{"a.com:p:1.0.0", "-"}, PublishArgs{Spec: spec("a.com", "p", "1.0.0"), Filename: "-"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePublishArgs(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if got.Force != tc.want.Force || got.Spec != tc.want.Spec || got.Filename != tc.want.Filename {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if (got.Prev == nil) != (tc.want.Prev == nil) || got.Prev != nil && *got.Prev != *tc.want.Prev {
				t.Fatalf("Prev = %v, want %v", got.Prev, tc.want.Prev)
			}
		})
	}
}

func TestParsePublishArgsRejects(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want error
	}{
		{"no arguments", nil, ErrBadArgs},
		{"only flags", []string{"--force"}, ErrBadArgs},
		{"missing filename", []string{"a.com:p:1.0.0"}, ErrBadArgs},
		{"too many arguments", []string{"a.com:p:1.0.0", "f", "g"}, ErrBadArgs},
		{"unknown flag", []string{"--frce", "a.com:p:1.0.0", "f"}, ErrBadArgs},
		{"single dash flag", []string{"-force", "a.com:p:1.0.0", "f"}, ErrBadArgs},
		{"flag with a value it does not take", []string{"--force=true", "a.com:p:1.0.0", "f"}, ErrBadArgs},
		{"prev without a value", []string{"a.com:p:1.0.0", "f", "--prev"}, ErrBadArgs},
		{"prev with an empty value", []string{"--prev=", "a.com:p:1.0.0", "f"}, ErrInvalidVersion},
		{"prev with a bad version", []string{"--prev=1.2", "a.com:p:1.0.0", "f"}, ErrInvalidVersion},
		{"prev latest", []string{"--prev=latest", "a.com:p:1.0.0", "f"}, ErrInvalidVersion},
		{"separate prev with a bad version", []string{"--prev", "x", "a.com:p:1.0.0", "f"}, ErrInvalidVersion},
		{"prev twice", []string{"--prev=1.0.0", "--prev=1.0.1", "a.com:p:2.0.0", "f"}, ErrBadArgs},
		{"publishing latest", []string{"a.com:p:latest", "f"}, ErrBadArgs},
		{"version missing", []string{"a.com:p", "f"}, ErrBadArgs},
		{"too many parts", []string{"a.com:p:1.0.0:x", "f"}, ErrBadArgs},
		{"bad version", []string{"a.com:p:1.0", "f"}, ErrInvalidVersion},
		{"bad domain", []string{"A.com:p:1.0.0", "f"}, ErrInvalidName},
		{"bad package", []string{"a.com:P:1.0.0", "f"}, ErrInvalidName},
		{"empty filename", []string{"a.com:p:1.0.0", ""}, ErrBadArgs},
		{"flag after the double dash is a positional", []string{"--", "a.com:p:1.0.0", "f", "--force"}, ErrBadArgs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePublishArgs(tc.args)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got.Filename != "" || got.Prev != nil || got.Force {
				t.Fatalf("a failed parse returned %+v", got)
			}
		})
	}
}

func TestParseSpec(t *testing.T) {
	got, err := ParseSpec("rfin.ch:java-pair:1.2.10")
	if err != nil || got.Domain != "rfin.ch" || got.Package != "java-pair" || got.Version != (Version{1, 2, 10}) || got.Latest {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got.String() != "rfin.ch:java-pair:1.2.10" {
		t.Errorf("String = %q", got)
	}
	got, err = ParseSpec("rfin.ch:java-pair:latest")
	if err != nil || !got.Latest || got.String() != "rfin.ch:java-pair:latest" {
		t.Fatalf("latest: %+v, %v", got, err)
	}

	bad := map[string]error{
		"":                         ErrBadArgs,
		"rfin.ch":                  ErrBadArgs,
		"rfin.ch:java-pair":        ErrBadArgs,
		"rfin.ch:java-pair:":       ErrInvalidVersion,
		":java-pair:1.0.0":         ErrInvalidName,
		"rfin.ch::1.0.0":           ErrInvalidName,
		"rfin.ch:java-pair:1.0.0:": ErrBadArgs,
		"rfin.ch:java-pair:LATEST": ErrInvalidVersion,
		"rfin.ch:java-pair:v1.0.0": ErrInvalidVersion,
		"RFIN.ch:java-pair:1.0.0":  ErrInvalidName,
		"rfin.ch:java pair:1.0.0":  ErrInvalidName,
		"rfin.ch:java-pair: 1.0.0": ErrInvalidVersion,
	}
	for in, want := range bad {
		if _, err := ParseSpec(in); !errors.Is(err, want) {
			t.Errorf("ParseSpec(%q): err = %v, want %v", in, err, want)
		}
	}
}

func TestParsePackage(t *testing.T) {
	got, err := ParsePackage("rfin.ch:java-pair")
	if err != nil || got != (PackageRef{"rfin.ch", "java-pair"}) || got.String() != "rfin.ch:java-pair" {
		t.Fatalf("got %+v, %v", got, err)
	}
	bad := map[string]error{
		"":                         ErrBadArgs,
		"rfin.ch":                  ErrBadArgs,
		"rfin.ch:java-pair:1.0.0":  ErrBadArgs,
		"rfin.ch:java-pair:latest": ErrBadArgs,
		":java-pair":               ErrInvalidName,
		"rfin.ch:":                 ErrInvalidName,
		"rfin.ch:Java":             ErrInvalidName,
		"Rfin.ch:java":             ErrInvalidName,
	}
	for in, want := range bad {
		if _, err := ParsePackage(in); !errors.Is(err, want) {
			t.Errorf("ParsePackage(%q): err = %v, want %v", in, err, want)
		}
	}
}

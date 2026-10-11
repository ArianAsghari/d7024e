package registry

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"
)

func TestParseVersion(t *testing.T) {
	good := []struct {
		in   string
		want Version
	}{
		{"0.0.0", Version{0, 0, 0}},
		{"1.2.3", Version{1, 2, 3}},
		{"10.20.30", Version{10, 20, 30}},
		{"0.0.1", Version{0, 0, 1}},
		{"4294967295.4294967295.4294967295", Version{4294967295, 4294967295, 4294967295}},
	}
	for _, tc := range good {
		got, err := ParseVersion(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
		if got.String() != tc.in {
			t.Errorf("String() = %q, want %q", got.String(), tc.in)
		}
	}

	bad := []string{
		"", "1", "1.2", "1.2.3.4", "1..3", ".2.3", "1.2.", "+1.2.3", "1.+2.3", "-1.2.3",
		"1.2.-3", "01.2.3", "1.02.3", "1.2.03", "1.2.3-beta", "1.2.3+build", "v1.2.3", "a.b.c",
		" 1.2.3", "1.2.3 ", "1. 2.3", "latest", "1.2.x", "4294967296.0.0", "0.99999999999.0",
		"1_0.2.3", "0x1.2.3", "١.٢.٣", "1.2.3\n",
	}
	for _, in := range bad {
		if v, err := ParseVersion(in); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("ParseVersion(%q) = %v, %v; want ErrInvalidVersion", in, v, err)
		}
	}
}

func TestVersionOrder(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.9", "1.2.10", -1},
		{"1.2.10", "1.2.9", 1},
		{"1.9.9", "1.10.0", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"0.0.0", "0.0.1", -1},
		{"3.4.5", "3.4.5", 0},
		{"1.0.0", "1.0.0", 0},
		{"0.1.0", "0.0.9", 1},
		{"10.0.0", "9.0.0", 1},
	}
	for _, tc := range cases {
		a, b := mustVersion(t, tc.a), mustVersion(t, tc.b)
		if got := a.Compare(b); got != tc.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := a.Less(b); got != (tc.want < 0) {
			t.Errorf("%s.Less(%s) = %v", tc.a, tc.b, got)
		}
	}
}

func TestVersionSortIsNumeric(t *testing.T) {
	shuffled := []string{"1.10.0", "1.2.10", "1.2.9", "0.9.0", "10.0.0", "1.9.9", "2.0.0", "1.2.0"}
	vs := make([]Version, len(shuffled))
	for i, s := range shuffled {
		vs[i] = mustVersion(t, s)
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Less(vs[j]) })
	want := []string{"0.9.0", "1.2.0", "1.2.9", "1.2.10", "1.9.9", "1.10.0", "2.0.0", "10.0.0"}
	for i, v := range vs {
		if v.String() != want[i] {
			t.Fatalf("sorted[%d] = %s, want %s", i, v, want[i])
		}
	}
}

func TestVersionJSON(t *testing.T) {
	b, err := json.Marshal(Version{1, 2, 3})
	if err != nil || string(b) != `"1.2.3"` {
		t.Fatalf("Marshal = %s, %v", b, err)
	}
	var v Version
	if err := json.Unmarshal([]byte(`"4.5.6"`), &v); err != nil || v != (Version{4, 5, 6}) {
		t.Fatalf("Unmarshal = %v, %v", v, err)
	}
	for _, in := range []string{`"latest"`, `"1.2"`, `"01.2.3"`} {
		if err := json.Unmarshal([]byte(in), &v); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("Unmarshal(%s) error = %v", in, err)
		}
	}
	if err := json.Unmarshal([]byte(`123`), &v); err == nil {
		t.Error("a JSON number must not decode as a version")
	}
}

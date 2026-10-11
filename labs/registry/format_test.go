package registry

import (
	"strings"
	"testing"
)

func TestShortHash(t *testing.T) {
	h := "ab12cd34" + strings.Repeat("0", 52) + "ef56"
	if got := ShortHash(h); got != "ab12cd34…ef56" {
		t.Fatalf("ShortHash = %q", got)
	}
	for _, short := range []string{"", "abc", "0123456789abc"} {
		if got := ShortHash(short); got != short {
			t.Errorf("ShortHash(%q) = %q", short, got)
		}
	}
	if got := ShortHash("0123456789abcd"); got != "01234567…abcd" {
		t.Errorf("ShortHash of 14 characters = %q", got)
	}
}

func TestFormatChain(t *testing.T) {
	a := Hash([]byte("a"))
	b := Hash([]byte("b"))
	c := Hash([]byte("c"))
	d := Hash([]byte("d"))
	chain := []ChainEntry{
		{Version{1, 10, 0}, a, b},
		{Version{1, 2, 9}, c, d},
	}
	want := "rfin.ch:java-pair has 2 versions (newest first)\n" +
		"  1.10.0  record " + ShortHash(a) + "  blob " + ShortHash(b) + "  (latest)\n" +
		"  1.2.9   record " + ShortHash(c) + "  blob " + ShortHash(d) + "\n"
	if got := FormatChain("rfin.ch", "java-pair", chain); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := FormatChain("rfin.ch", "java-pair", chain[:1]); !strings.HasPrefix(got, "rfin.ch:java-pair has 1 version (newest first)\n") {
		t.Errorf("singular: %q", got)
	}
	if got := FormatChain("rfin.ch", "java-pair", nil); got != "rfin.ch:java-pair has no versions\n" {
		t.Errorf("empty: %q", got)
	}
	if strings.Contains(FormatChain("rfin.ch", "java-pair", chain), a) {
		t.Error("full hashes must not appear in the output")
	}
}

func TestFormatOwner(t *testing.T) {
	pub, _ := mustKey(t)
	got := FormatOwner("rfin.ch", pub)
	want := "rfin.ch: kademlia-pk=" + EncodePublicKey(pub) + " (fingerprint " + Fingerprint(pub) + ")"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDescribeValue(t *testing.T) {
	b := newBuilder(t)
	h := b.chain("1.0.0", "1.1.0")
	first, second := b.imm[h[0]], b.imm[h[1]]
	ptr := b.pointer("1.1.0", h[1])
	blob := Hash(content("1.0.0"))

	cases := []struct {
		name  string
		key   string
		value []byte
		want  string
		ok    bool
	}{
		{"first version record", h[0], first, "version-record example.com:tool 1.0.0 blob=" + ShortHash(blob) + " prev=none", true},
		{"later version record", h[1], second, "version-record example.com:tool 1.1.0 blob=" + ShortHash(Hash(content("1.1.0"))) + " prev=" + ShortHash(h[0]), true},
		{"version record under the wrong key", Hash([]byte("x")), second, "version-record example.com:tool 1.1.0 blob=" + ShortHash(Hash(content("1.1.0"))) + " prev=" + ShortHash(h[0]) + " (key mismatch)", true},
		{"latest pointer", b.key(), ptr, "latest-pointer example.com:tool -> 1.1.0 record=" + ShortHash(h[1]), true},
		{"latest pointer under the wrong key", h[0], ptr, "latest-pointer example.com:tool -> 1.1.0 record=" + ShortHash(h[1]) + " (key mismatch)", true},
		{"package blob", Hash(content("1.0.0")), content("1.0.0"), "", false},
		{"empty value", Hash(nil), nil, "", false},
		{"plain json", Hash([]byte("{}")), []byte("{}"), "", false},
		{"record with an unknown member", h[0], []byte(strings.Replace(string(first), `"sig"`, `"extra":1,"sig"`, 1)), "", false},
		{"truncated record", h[0], first[:len(first)-5], "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DescribeValue(tc.key, tc.value)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("DescribeValue = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
	for _, c := range cases {
		if got, _ := DescribeValue(c.key, c.value); strings.Contains(got, h[0]) || strings.Contains(got, blob) {
			t.Errorf("%s: a full hash leaked into %q", c.name, got)
		}
	}
}

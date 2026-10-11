package registry

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestZeroHash(t *testing.T) {
	if ZeroHash != strings.Repeat("0", 64) {
		t.Fatalf("ZeroHash = %q", ZeroHash)
	}
}

func TestPointerKey(t *testing.T) {
	sum := sha256.Sum256([]byte("rfin.ch:java-pair:latest"))
	if got, want := PointerKey("rfin.ch", "java-pair"), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("PointerKey = %s, want %s", got, want)
	}
	if PointerKey("a.com", "b") == PointerKey("a.com", "c") || PointerKey("a.com", "b") == PointerKey("b.com", "b") {
		t.Fatal("pointer keys must differ per domain and package")
	}
}

func TestRecordHashIsSHA256OfBytes(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	if RecordHash([]byte("abc")) != hex.EncodeToString(sum[:]) || Hash([]byte("abc")) != RecordHash([]byte("abc")) {
		t.Fatal("RecordHash must be lowercase hex SHA-256")
	}
}

func TestVersionRecordRoundTrip(t *testing.T) {
	pub, priv := mustKey(t)
	blob := Hash([]byte("blob"))
	r, err := NewVersionRecord(priv, "rfin.ch", "java-pair", Version{1, 2, 3}, blob, ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), `{"tag":"version-record","domain-name":"rfin.ch","package-name":"java-pair","version":"1.2.3","blobHash":"`+blob+`","previous-version-record":"`+ZeroHash+`","sig":"`) {
		t.Fatalf("unexpected encoding %s", raw)
	}
	got, err := DecodeVersionRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *r {
		t.Fatalf("round trip changed the record: %+v vs %+v", got, r)
	}
	if err := got.Verify(pub); err != nil {
		t.Fatal(err)
	}
}

func TestSignatureCoversTheHashOfEveryOtherField(t *testing.T) {
	pub, priv := mustKey(t)
	r, err := NewVersionRecord(priv, "rfin.ch", "java-pair", Version{1, 2, 3}, Hash([]byte("x")), ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.SigningHash()
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(`{"tag":"version-record","domain-name":"rfin.ch","package-name":"java-pair","version":"1.2.3","blobHash":"` + r.BlobHash + `","previous-version-record":"` + ZeroHash + `"}`))
	if hex.EncodeToString(b) != hex.EncodeToString(want[:]) {
		t.Fatal("signing payload is not the hash of the record without sig")
	}
	sig, _ := hex.DecodeString(r.Sig)
	if !ed25519.Verify(pub, want[:], sig) {
		t.Fatal("sig is not an Ed25519 signature over that hash")
	}

	mutations := map[string]func(*VersionRecord){
		"domain":   func(r *VersionRecord) { r.Domain = "evil.com" },
		"package":  func(r *VersionRecord) { r.Package = "other" },
		"version":  func(r *VersionRecord) { r.Version.Patch++ },
		"blobHash": func(r *VersionRecord) { r.BlobHash = flipHex(r.BlobHash) },
		"previous": func(r *VersionRecord) { r.Previous = flipHex(r.Previous) },
	}
	for name, mutate := range mutations {
		c := *r
		mutate(&c)
		if err := c.Verify(pub); !errors.Is(err, ErrBadSignature) {
			t.Errorf("changing %s: Verify = %v, want ErrBadSignature", name, err)
		}
	}
}

func TestLatestPointerRoundTrip(t *testing.T) {
	pub, priv := mustKey(t)
	rec := Hash([]byte("record"))
	p, err := NewLatestPointer(priv, "rfin.ch", "java-pair", Version{2, 0, 1}, rec)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), `{"tag":"latest-pointer","domain-name":"rfin.ch","package-name":"java-pair","version":"2.0.1","versionRecordHash":"`+rec+`","sig":"`) {
		t.Fatalf("unexpected encoding %s", raw)
	}
	got, err := DecodeLatestPointer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *p || got.Verify(pub) != nil {
		t.Fatalf("round trip failed: %+v", got)
	}
	other, _ := mustKey(t)
	if err := got.Verify(other); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify with the wrong key = %v", err)
	}
	for name, mutate := range map[string]func(*LatestPointer){
		"domain":  func(p *LatestPointer) { p.Domain = "evil.com" },
		"package": func(p *LatestPointer) { p.Package = "other" },
		"version": func(p *LatestPointer) { p.Version.Major++ },
		"record":  func(p *LatestPointer) { p.VersionRecordHash = flipHex(p.VersionRecordHash) },
	} {
		c := *got
		mutate(&c)
		if err := c.Verify(pub); !errors.Is(err, ErrBadSignature) {
			t.Errorf("changing %s: Verify = %v, want ErrBadSignature", name, err)
		}
	}
}

func TestVersionRecordAndPointerSignaturesAreNotInterchangeable(t *testing.T) {
	pub, priv := mustKey(t)
	h := Hash([]byte("h"))
	r, _ := NewVersionRecord(priv, "a.com", "p", Version{1, 0, 0}, h, h)
	p, _ := NewLatestPointer(priv, "a.com", "p", Version{1, 0, 0}, h)
	p.Sig = r.Sig
	if err := p.Verify(pub); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a record signature verified on a pointer: %v", err)
	}
}

func TestConstructorsRejectBadInput(t *testing.T) {
	_, priv := mustKey(t)
	good := Hash([]byte("x"))
	v := Version{1, 0, 0}
	if _, err := NewVersionRecord(priv[:10], "a.com", "p", v, good, ZeroHash); !errors.Is(err, ErrBadKey) {
		t.Errorf("short private key: %v", err)
	}
	if _, err := NewLatestPointer(priv[:10], "a.com", "p", v, good); !errors.Is(err, ErrBadKey) {
		t.Errorf("short private key: %v", err)
	}
	if _, err := NewVersionRecord(priv, "A.com", "p", v, good, ZeroHash); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad domain: %v", err)
	}
	if _, err := NewVersionRecord(priv, "a.com", "P", v, good, ZeroHash); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad package: %v", err)
	}
	if _, err := NewVersionRecord(priv, "a.com", "p", v, "abc", ZeroHash); !errors.Is(err, ErrMalformed) {
		t.Errorf("bad blob hash: %v", err)
	}
	if _, err := NewVersionRecord(priv, "a.com", "p", v, good, strings.ToUpper(good)); !errors.Is(err, ErrMalformed) {
		t.Errorf("uppercase previous hash: %v", err)
	}
	if _, err := NewLatestPointer(priv, "a.com", "p", v, "xyz"); !errors.Is(err, ErrMalformed) {
		t.Errorf("bad record hash: %v", err)
	}
	if _, err := NewLatestPointer(priv, "", "p", v, good); !errors.Is(err, ErrInvalidName) {
		t.Errorf("empty domain: %v", err)
	}
}

func TestEncodeRefusesInvalidRecords(t *testing.T) {
	_, priv := mustKey(t)
	good := Hash([]byte("x"))
	r, _ := NewVersionRecord(priv, "a.com", "p", Version{1, 0, 0}, good, ZeroHash)
	p, _ := NewLatestPointer(priv, "a.com", "p", Version{1, 0, 0}, good)

	unsigned := *r
	unsigned.Sig = ""
	if _, err := unsigned.Encode(); !errors.Is(err, ErrMalformed) {
		t.Errorf("unsigned record encoded: %v", err)
	}
	wrongTag := *p
	wrongTag.Tag = TagVersionRecord
	if _, err := wrongTag.Encode(); !errors.Is(err, ErrMalformed) {
		t.Errorf("pointer with the wrong tag encoded: %v", err)
	}
	unsignedPtr := *p
	unsignedPtr.Sig = ""
	if _, err := unsignedPtr.Encode(); !errors.Is(err, ErrMalformed) {
		t.Errorf("unsigned pointer encoded: %v", err)
	}
}

func TestVerifyRejectsUnusableKeysAndSignatures(t *testing.T) {
	pub, priv := mustKey(t)
	r, _ := NewVersionRecord(priv, "a.com", "p", Version{1, 0, 0}, Hash([]byte("x")), ZeroHash)
	if err := r.Verify(pub[:5]); !errors.Is(err, ErrBadKey) {
		t.Errorf("short public key: %v", err)
	}
	c := *r
	c.Sig = "zz"
	if err := c.Verify(pub); !errors.Is(err, ErrBadSignature) {
		t.Errorf("garbage signature: %v", err)
	}
}

func validRecordJSON(t *testing.T) (string, ed25519.PublicKey) {
	pub, priv := mustKey(t)
	r, err := NewVersionRecord(priv, "a.com", "p", Version{1, 0, 0}, Hash([]byte("x")), ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), pub
}

func TestDecodeVersionRecordIsStrict(t *testing.T) {
	good, _ := validRecordJSON(t)
	if _, err := DecodeVersionRecord([]byte(good)); err != nil {
		t.Fatal(err)
	}
	upperHash := strings.Replace(good, Hash([]byte("x")), strings.ToUpper(Hash([]byte("x"))), 1)
	cases := map[string]string{
		"empty":               "",
		"not json":            "hello",
		"null":                "null",
		"array":               "[]",
		"trailing newline":    good + "\n",
		"trailing garbage":    good + "x",
		"trailing second doc": good + good,
		"leading space":       " " + good,
		"unknown field":       strings.Replace(good, `"sig"`, `"extra":1,"sig"`, 1),
		"wrong tag":           strings.Replace(good, `"version-record"`, `"latest-pointer"`, 1),
		"empty tag":           strings.Replace(good, `"version-record"`, `""`, 1),
		"missing sig":         good[:strings.Index(good, `,"sig"`)] + "}",
		"short sig":           good[:len(good)-4] + `"}`,
		"uppercase hash":      upperHash,
		"short hash":          strings.Replace(good, Hash([]byte("x")), "abcd", 1),
		"version with plus":   strings.Replace(good, `"1.0.0"`, `"+1.0.0"`, 1),
		"version latest":      strings.Replace(good, `"1.0.0"`, `"latest"`, 1),
		"version leading 0":   strings.Replace(good, `"1.0.0"`, `"01.0.0"`, 1),
		"version null":        strings.Replace(good, `"1.0.0"`, `null`, 1),
		"version number":      strings.Replace(good, `"1.0.0"`, `1`, 1),
		"uppercase domain":    strings.Replace(good, `"a.com"`, `"A.com"`, 1),
		"domain with colon":   strings.Replace(good, `"a.com"`, `"a:com"`, 1),
		"package with slash":  strings.Replace(good, `"p"`, `"p/q"`, 1),
		"swapped members":     `{"domain-name":"a.com",` + good[1:],
		"field name case":     strings.Replace(good, `"blobHash"`, `"BlobHash"`, 1),
		"duplicate member":    strings.Replace(good, `"tag":"version-record",`, `"tag":"version-record","tag":"version-record",`, 1),
		"escaped character":   strings.Replace(good, `"a.com"`, "\"a\\u002ecom\"", 1),
		"oversize":            `{"tag":"` + strings.Repeat("a", MaxRecordSize) + `"}`,
	}
	for name, in := range cases {
		if _, err := DecodeVersionRecord([]byte(in)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestDecodeLatestPointerIsStrict(t *testing.T) {
	_, priv := mustKey(t)
	p, _ := NewLatestPointer(priv, "a.com", "p", Version{1, 0, 0}, Hash([]byte("r")))
	raw, _ := p.Encode()
	good := string(raw)
	if _, err := DecodeLatestPointer([]byte(good)); err != nil {
		t.Fatal(err)
	}
	recordRaw, _ := validRecordJSON(t)
	cases := map[string]string{
		"a version record":  recordRaw,
		"trailing data":     good + " ",
		"unknown field":     strings.Replace(good, `"sig"`, `"blobHash":"x","sig"`, 1),
		"wrong tag":         strings.Replace(good, `"latest-pointer"`, `"version-record"`, 1),
		"bad record hash":   strings.Replace(good, Hash([]byte("r")), "nothex", 1),
		"version latest":    strings.Replace(good, `"1.0.0"`, `"latest"`, 1),
		"empty package":     strings.Replace(good, `"p"`, `""`, 1),
		"not json":          "{",
		"missing version":   strings.Replace(good, `"version":"1.0.0",`, ``, 1),
		"zero-length input": "",
	}
	for name, in := range cases {
		if _, err := DecodeLatestPointer([]byte(in)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestNameValidation(t *testing.T) {
	goodDomains := []string{"a", "example.com", "rfin.ch", "sub.domain.example.org", "xn--bcher-kva.example", "a-b.c0", strings.Repeat("a", 63) + ".com"}
	for _, d := range goodDomains {
		if err := ValidateDomainName(d); err != nil {
			t.Errorf("domain %q rejected: %v", d, err)
		}
	}
	badDomains := []string{
		"", ".", "a.", ".a", "a..b", "-a.com", "a-.com", "A.com", "a_b.com", "a b.com", "a:b.com", "a/b.com",
		"ä.com", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "a",
	}
	for _, d := range badDomains {
		if err := ValidateDomainName(d); !errors.Is(err, ErrInvalidName) {
			t.Errorf("domain %q: err = %v, want ErrInvalidName", d, err)
		}
	}

	goodPackages := []string{"a", "java-pair", "lib_2", "x.y.z", "0day", strings.Repeat("a", 100)}
	for _, p := range goodPackages {
		if err := ValidatePackageName(p); err != nil {
			t.Errorf("package %q rejected: %v", p, err)
		}
	}
	badPackages := []string{"", ".", "..", "-a", "_a", ".a", "A", "a b", "a:b", "a/b", "ä", strings.Repeat("a", 101), "latest\n"}
	for _, p := range badPackages {
		if err := ValidatePackageName(p); !errors.Is(err, ErrInvalidName) {
			t.Errorf("package %q: err = %v, want ErrInvalidName", p, err)
		}
	}
}

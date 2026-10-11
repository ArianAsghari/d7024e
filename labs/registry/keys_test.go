package registry

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPublicKeyEncoding(t *testing.T) {
	pub, _ := mustKey(t)
	s := EncodePublicKey(pub)
	if len(s) != 44 || !strings.HasSuffix(s, "=") {
		t.Fatalf("EncodePublicKey = %q, want 44 characters of padded standard base64", s)
	}
	got, err := DecodePublicKey(s)
	if err != nil || !pub.Equal(got) {
		t.Fatalf("round trip: %v, %v", got, err)
	}
	if got := TXTRecord(pub); got != "kademlia-pk="+s {
		t.Fatalf("TXTRecord = %q", got)
	}
}

func TestDecodePublicKeyRejects(t *testing.T) {
	pub, _ := mustKey(t)
	good := EncodePublicKey(pub)
	urlSafe := base64.URLEncoding.EncodeToString([]byte(strings.Repeat("\xfb\xff", 16)))
	cases := map[string]string{
		"empty":                "",
		"not base64":           "!!!!",
		"unpadded":             strings.TrimSuffix(good, "="),
		"too short":            base64.StdEncoding.EncodeToString(pub[:31]),
		"too long":             base64.StdEncoding.EncodeToString(append(append([]byte{}, pub...), 0)),
		"url alphabet":         urlSafe,
		"embedded newline":     good[:10] + "\n" + good[10:],
		"trailing newline":     good + "\n",
		"leading space":        " " + good,
		"nonzero padding bits": good[:42] + "B=",
	}
	for name, in := range cases {
		if _, err := DecodePublicKey(in); !errors.Is(err, ErrBadKey) {
			t.Errorf("%s: err = %v, want ErrBadKey", name, err)
		}
	}
}

func TestFingerprint(t *testing.T) {
	pub, _ := mustKey(t)
	fp := Fingerprint(pub)
	if !regexp.MustCompile(`^[0-9a-f]{4}(:[0-9a-f]{4}){3}$`).MatchString(fp) {
		t.Fatalf("Fingerprint = %q", fp)
	}
	other, _ := mustKey(t)
	if Fingerprint(other) == fp {
		t.Fatal("different keys share a fingerprint")
	}
	if Fingerprint(pub) != fp {
		t.Fatal("fingerprint is not deterministic")
	}
}

func TestGenerateKeyAndPublicKeyOf(t *testing.T) {
	pub, priv := mustKey(t)
	if !pub.Equal(PublicKeyOf(priv)) {
		t.Fatal("PublicKeyOf does not match")
	}
	if len(pub) != ed25519.PublicKeySize || len(priv) != ed25519.PrivateKeySize {
		t.Fatal("unexpected key sizes")
	}
}

func TestSaveAndLoadPrivateKey(t *testing.T) {
	_, priv := mustKey(t)
	path := filepath.Join(t.TempDir(), "owner.key")
	if err := SavePrivateKey(path, priv); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
		}
	}
	got, err := LoadPrivateKey(path)
	if err != nil || !priv.Equal(got) {
		t.Fatalf("LoadPrivateKey = %v, %v", got, err)
	}
	if err := SavePrivateKey(path, priv); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("overwriting a key file: %v, want fs.ErrExist", err)
	}
	again, err := LoadPrivateKey(path)
	if err != nil || !priv.Equal(again) {
		t.Fatal("a refused overwrite damaged the key file")
	}
}

func TestSavePrivateKeyErrors(t *testing.T) {
	_, priv := mustKey(t)
	if err := SavePrivateKey(filepath.Join(t.TempDir(), "k"), priv[:7]); !errors.Is(err, ErrBadKey) {
		t.Errorf("short key: %v", err)
	}
	if err := SavePrivateKey(filepath.Join(t.TempDir(), "missing-dir", "k"), priv); err == nil {
		t.Error("saving into a missing directory succeeded")
	}
}

func TestLoadPrivateKeyErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadPrivateKey(filepath.Join(dir, "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("absent file: %v", err)
	}
	if _, err := LoadPrivateKey(write("junk", "not base64!\n")); !errors.Is(err, ErrBadKey) {
		t.Errorf("junk file: %v", err)
	}
	short := base64.StdEncoding.EncodeToString([]byte("too short"))
	if _, err := LoadPrivateKey(write("short", short)); !errors.Is(err, ErrBadKey) {
		t.Errorf("short seed: %v", err)
	}
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	if _, err := LoadPrivateKey(write("ok", seed+"\n")); err != nil {
		t.Errorf("valid seed with a trailing newline: %v", err)
	}
}

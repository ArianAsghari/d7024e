package registry

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeDNS(records map[string][]string) LookupTXTFunc {
	return func(ctx context.Context, name string) ([]string, error) {
		recs, ok := records[name]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
		}
		return recs, nil
	}
}

func TestDNSOwnersPublicKey(t *testing.T) {
	pub, _ := mustKey(t)
	other, _ := mustKey(t)
	txt := TXTRecord(pub)
	lookupErr := errors.New("connection refused")

	cases := []struct {
		name    string
		lookup  LookupTXTFunc
		wantKey bool
		wantErr error
	}{
		{"single record", fakeDNS(map[string][]string{"example.com": {txt}}), true, nil},
		{"other TXT records are ignored", fakeDNS(map[string][]string{"example.com": {"v=spf1 -all", txt, "google-site-verification=abc"}}), true, nil},
		{"no TXT records at all", fakeDNS(map[string][]string{"example.com": {}}), false, ErrNoOwner},
		{"only unrelated records", fakeDNS(map[string][]string{"example.com": {"v=spf1 -all"}}), false, ErrNoOwner},
		{"unknown domain", fakeDNS(nil), false, ErrNoOwner},
		{"two different records", fakeDNS(map[string][]string{"example.com": {txt, TXTRecord(other)}}), false, ErrOwnerRecord},
		{"two identical records", fakeDNS(map[string][]string{"example.com": {txt, txt}}), false, ErrOwnerRecord},
		{"valid record next to a broken one", fakeDNS(map[string][]string{"example.com": {txt, "kademlia-pk=%%%"}}), false, ErrOwnerRecord},
		{"bad base64", fakeDNS(map[string][]string{"example.com": {"kademlia-pk=%%%%"}}), false, ErrOwnerRecord},
		{"empty value", fakeDNS(map[string][]string{"example.com": {"kademlia-pk="}}), false, ErrOwnerRecord},
		{"wrong length", fakeDNS(map[string][]string{"example.com": {"kademlia-pk=" + EncodePublicKey(pub[:16])}}), false, ErrOwnerRecord},
		{"surrounding whitespace", fakeDNS(map[string][]string{"example.com": {txt + " "}}), false, ErrOwnerRecord},
		{"wrong prefix case", fakeDNS(map[string][]string{"example.com": {"Kademlia-PK=" + EncodePublicKey(pub)}}), false, ErrNoOwner},
		{"resolver failure", func(context.Context, string) ([]string, error) { return nil, lookupErr }, false, ErrOwnerLookup},
		{"dns timeout", func(context.Context, string) ([]string, error) {
			return nil, &net.DNSError{Err: "i/o timeout", Name: "example.com", IsTimeout: true}
		}, false, ErrOwnerLookup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &DNSOwners{Lookup: tc.lookup}
			got, err := d.PublicKey("example.com")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if got != nil {
					t.Fatalf("a key was returned together with an error: %x", got)
				}
				return
			}
			if err != nil || !pub.Equal(got) {
				t.Fatalf("PublicKey = %x, %v", got, err)
			}
		})
	}
}

func TestDNSOwnersWrapsResolverError(t *testing.T) {
	lookupErr := errors.New("connection refused")
	d := &DNSOwners{Lookup: func(context.Context, string) ([]string, error) { return nil, lookupErr }}
	_, err := d.PublicKey("example.com")
	if !errors.Is(err, ErrOwnerLookup) || !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want both ErrOwnerLookup and the resolver error", err)
	}
}

func TestDNSOwnersRejectsInvalidDomainBeforeLookup(t *testing.T) {
	called := false
	d := &DNSOwners{Lookup: func(context.Context, string) ([]string, error) { called = true; return nil, nil }}
	for _, name := range []string{"", "Example.com", "a b.com", "x..y"} {
		if _, err := d.PublicKey(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q: err = %v", name, err)
		}
	}
	if called {
		t.Fatal("the resolver was queried for an invalid name")
	}
}

func TestDNSOwnersQueriesTheDomainWithADeadline(t *testing.T) {
	pub, _ := mustKey(t)
	var gotName string
	var gotDeadline time.Time
	d := &DNSOwners{Timeout: time.Minute, Lookup: func(ctx context.Context, name string) ([]string, error) {
		gotName = name
		gotDeadline, _ = ctx.Deadline()
		return []string{TXTRecord(pub)}, nil
	}}
	before := time.Now()
	if _, err := d.PublicKey("rfin.ch"); err != nil {
		t.Fatal(err)
	}
	if gotName != "rfin.ch" {
		t.Errorf("looked up %q", gotName)
	}
	if gotDeadline.Before(before.Add(30*time.Second)) || gotDeadline.After(before.Add(2*time.Minute)) {
		t.Errorf("deadline %v does not reflect the configured timeout", gotDeadline.Sub(before))
	}
}

func TestDNSOwnersDefaults(t *testing.T) {
	d := NewDNSOwners()
	if d.Lookup == nil || d.Timeout <= 0 {
		t.Fatalf("NewDNSOwners = %+v", d)
	}
	zero := &DNSOwners{}
	if _, err := zero.PublicKey("Not A Domain"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("zero value: %v", err)
	}
}

func TestDNSOwnersCancelsSlowLookups(t *testing.T) {
	d := &DNSOwners{Timeout: 20 * time.Millisecond, Lookup: func(ctx context.Context, name string) ([]string, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, err := d.PublicKey("example.com")
	if !errors.Is(err, ErrOwnerLookup) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseOwnerTXT(t *testing.T) {
	pub, _ := mustKey(t)
	got, err := ParseOwnerTXT([]string{"unrelated", TXTRecord(pub)})
	if err != nil || !pub.Equal(got) {
		t.Fatalf("ParseOwnerTXT = %v, %v", got, err)
	}
	if _, err := ParseOwnerTXT(nil); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("nil records: %v", err)
	}
}

func TestStaticOwners(t *testing.T) {
	pub, _ := mustKey(t)
	s := NewStaticOwners()
	if _, err := s.PublicKey("example.com"); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("empty table: %v", err)
	}
	if err := s.Set("example.com", pub); err != nil {
		t.Fatal(err)
	}
	got, err := s.PublicKey("example.com")
	if err != nil || !pub.Equal(got) {
		t.Fatalf("PublicKey = %v, %v", got, err)
	}
	got[0] ^= 0xff
	again, _ := s.PublicKey("example.com")
	if !pub.Equal(again) {
		t.Fatal("mutating a returned key changed the table")
	}
	pub[0] ^= 0xff
	again, _ = s.PublicKey("example.com")
	if pub.Equal(again) {
		t.Fatal("mutating the key passed to Set changed the table")
	}

	if err := s.Set("Example.com", pub); !errors.Is(err, ErrInvalidName) {
		t.Errorf("bad domain: %v", err)
	}
	if err := s.Set("other.org", pub[:3]); !errors.Is(err, ErrBadKey) {
		t.Errorf("bad key: %v", err)
	}
	if err := s.Set("a.org", pub); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("z.org", pub); err != nil {
		t.Fatal(err)
	}
	if got := s.Domains(); strings.Join(got, ",") != "a.org,example.com,z.org" {
		t.Fatalf("Domains = %v", got)
	}
}

func TestStaticOwnersFromFile(t *testing.T) {
	a, _ := mustKey(t)
	b, _ := mustKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "owners.json")
	content := fmt.Sprintf("{\n  \"example.com\": %q,\n  \"rfin.ch\": %q\n}\n", EncodePublicKey(a), EncodePublicKey(b))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStaticOwners(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PublicKey("example.com"); !a.Equal(got) {
		t.Error("example.com has the wrong key")
	}
	if got, _ := s.PublicKey("rfin.ch"); !b.Equal(got) {
		t.Error("rfin.ch has the wrong key")
	}
	if got := s.Domains(); len(got) != 2 {
		t.Errorf("Domains = %v", got)
	}

	if _, err := LoadStaticOwners(filepath.Join(dir, "absent.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("absent file: %v", err)
	}
}

func TestParseStaticOwnersRejects(t *testing.T) {
	pub, _ := mustKey(t)
	good := EncodePublicKey(pub)
	cases := map[string]string{
		"not json":           "hello",
		"empty input":        "",
		"array":              `[]`,
		"value not a string": `{"example.com": 1}`,
		"bad base64":         `{"example.com": "%%%"}`,
		"wrong length":       `{"example.com": "` + EncodePublicKey(pub[:8]) + `"}`,
		"invalid domain":     `{"Example.com": "` + good + `"}`,
		"trailing data":      `{"example.com": "` + good + `"} {}`,
		"trailing garbage":   `{"example.com": "` + good + `"}x`,
	}
	for name, in := range cases {
		if _, err := ParseStaticOwners([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if s, err := ParseStaticOwners([]byte(`{}`)); err != nil || len(s.Domains()) != 0 {
		t.Errorf("empty object: %v, %v", s, err)
	}
	_, err := LoadStaticOwners(writeTemp(t, `{"example.com": "%%%"}`))
	if err == nil || !strings.Contains(err.Error(), "owners.json") {
		t.Errorf("the error should name the file: %v", err)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "owners.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStaticOwnersConcurrency(t *testing.T) {
	s := NewStaticOwners()
	keys := make([]ed25519.PublicKey, 8)
	for i := range keys {
		keys[i], _ = mustKey(t)
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				domain := fmt.Sprintf("d%d.example.com", i%10)
				switch (g + i) % 3 {
				case 0:
					if err := s.Set(domain, keys[g]); err != nil {
						t.Error(err)
						return
					}
				case 1:
					if pub, err := s.PublicKey(domain); err == nil && len(pub) != ed25519.PublicKeySize {
						t.Error("short key")
						return
					}
				default:
					_ = s.Domains()
				}
			}
		}()
	}
	wg.Wait()
	if len(s.Domains()) == 0 {
		t.Fatal("no domain was stored")
	}
}

package registry

import (
	"crypto/ed25519"
	"fmt"
	"strings"
)

func ShortHash(h string) string {
	if len(h) <= 13 {
		return h
	}
	return h[:8] + "…" + h[len(h)-4:]
}

func FormatChain(domain, pkg string, chain []ChainEntry) string {
	var b strings.Builder
	switch len(chain) {
	case 0:
		fmt.Fprintf(&b, "%s:%s has no versions\n", domain, pkg)
	case 1:
		fmt.Fprintf(&b, "%s:%s has 1 version (newest first)\n", domain, pkg)
	default:
		fmt.Fprintf(&b, "%s:%s has %d versions (newest first)\n", domain, pkg, len(chain))
	}
	width := 0
	for _, e := range chain {
		width = max(width, len(e.Version.String()))
	}
	for i, e := range chain {
		fmt.Fprintf(&b, "  %-*s  record %s  blob %s", width, e.Version, ShortHash(e.RecordHash), ShortHash(e.BlobHash))
		if i == 0 {
			b.WriteString("  (latest)")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func FormatOwner(domain string, pub ed25519.PublicKey) string {
	return fmt.Sprintf("%s: %s (fingerprint %s)", domain, TXTRecord(pub), Fingerprint(pub))
}

func DescribeValue(key string, value []byte) (string, bool) {
	if rec, err := DecodeVersionRecord(value); err == nil {
		prev := "none"
		if rec.Previous != ZeroHash {
			prev = ShortHash(rec.Previous)
		}
		desc := fmt.Sprintf("version-record %s:%s %s blob=%s prev=%s", rec.Domain, rec.Package, rec.Version, ShortHash(rec.BlobHash), prev)
		if Hash(value) != key {
			desc += " (key mismatch)"
		}
		return desc, true
	}
	if ptr, err := DecodeLatestPointer(value); err == nil {
		desc := fmt.Sprintf("latest-pointer %s:%s -> %s record=%s", ptr.Domain, ptr.Package, ptr.Version, ShortHash(ptr.VersionRecordHash))
		if PointerKey(ptr.Domain, ptr.Package) != key {
			desc += " (key mismatch)"
		}
		return desc, true
	}
	return "", false
}

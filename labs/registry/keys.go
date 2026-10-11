package registry

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

const TXTPrefix = "kademlia-pk="

func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func PublicKeyOf(priv ed25519.PrivateKey) ed25519.PublicKey {
	return priv.Public().(ed25519.PublicKey)
}

func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: public key is not standard base64: %v", ErrBadKey, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key has %d bytes, want %d", ErrBadKey, len(raw), ed25519.PublicKeySize)
	}
	// The decoder skips CR and LF, so only the round trip proves there was nothing else in s.
	if base64.StdEncoding.EncodeToString(raw) != s {
		return nil, fmt.Errorf("%w: public key is not in canonical base64", ErrBadKey)
	}
	return ed25519.PublicKey(raw), nil
}

func TXTRecord(pub ed25519.PublicKey) string {
	return TXTPrefix + EncodePublicKey(pub)
}

func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + ":" + h[4:8] + ":" + h[8:12] + ":" + h[12:16]
}

func SavePrivateKey(path string, priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("%w: private key has %d bytes", ErrBadKey, len(priv))
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	line := base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a base64 key file: %v", ErrBadKey, path, err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: %s holds %d bytes, want a %d byte seed", ErrBadKey, path, len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

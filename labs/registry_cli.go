package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"d7024e/kademlia"
	"d7024e/registry"
	"d7024e/registry/dht"
)

const (
	defaultOwnersFile = "owners.json"
	defaultKeysDir    = "keys"

	publishUsage = "usage: publish [--force] [--prev=PREVIOUS-VERSION] DOMAIN:PACKAGE:VERSION FILENAME"
	installUsage = "usage: install DOMAIN:PACKAGE:VERSION [FILENAME]"
	keygenUsage  = "usage: keygen DOMAIN"
	showUsage    = "usage: show rt|ds|DOMAIN:PACKAGE|dns DOMAIN"
)

func getOwnersFile() string {
	if p := os.Getenv("OWNERS_FILE"); p != "" {
		return p
	}
	return defaultOwnersFile
}

func getKeysDir() string {
	if p := os.Getenv("KEYS_DIR"); p != "" {
		return p
	}
	return defaultKeysDir
}

// fileOwners reads the file again whenever it changes, so nodes that share it
// (a mounted volume) see a domain registered by keygen on another node. A
// missing file means no domain has an owner.
type fileOwners struct {
	path string

	mu     sync.Mutex
	info   os.FileInfo
	owners *registry.StaticOwners
	err    error
}

func (f *fileOwners) PublicKey(domain string) (ed25519.PublicKey, error) {
	owners, err := f.load()
	if err != nil {
		return nil, err
	}
	pub, err := owners.PublicKey(domain)
	if errors.Is(err, registry.ErrNoOwner) {
		return nil, fmt.Errorf("%w: %s is not listed in %s", registry.ErrNoOwner, domain, f.path)
	}
	return pub, err
}

func (f *fileOwners) load() (*registry.StaticOwners, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, err := os.Stat(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		f.info, f.owners, f.err = nil, registry.NewStaticOwners(), nil
		return f.owners, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", registry.ErrOwnerLookup, err)
	}
	if f.info == nil || !os.SameFile(f.info, info) || !f.info.ModTime().Equal(info.ModTime()) || f.info.Size() != info.Size() {
		f.info = info
		f.owners, f.err = registry.LoadStaticOwners(f.path)
	}
	return f.owners, f.err
}

func installRegistry(node *kademlia.Kademlia) {
	dht.Install(node, &fileOwners{path: getOwnersFile()})
}

func newRegistryClient(node *kademlia.Kademlia) *registry.Client {
	return registry.NewClient(dht.NewBackend(node), &fileOwners{path: getOwnersFile()})
}

func oneLine(err error) string {
	var parts []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); line != "" && (len(parts) == 0 || parts[len(parts)-1] != line) {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "; ")
}

func registryCommand(out io.Writer, node *kademlia.Kademlia, cmd string, args []string) {
	switch cmd {
	case "keygen":
		keygenCommand(out, args)
	case "publish":
		publishCommand(out, node, args)
	case "install":
		installCommand(out, node, args)
	}
}

func publishCommand(out io.Writer, node *kademlia.Kademlia, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(out, publishUsage)
		return
	}
	parsed, err := registry.ParsePublishArgs(args)
	if err != nil {
		fmt.Fprintf(out, "publish failed: %s\n", oneLine(err))
		return
	}
	data, err := readPackageFile(parsed.Filename)
	if err != nil {
		fmt.Fprintf(out, "publish failed: %s\n", oneLine(err))
		return
	}
	priv, err := loadDomainKey(parsed.Spec.Domain)
	if err != nil {
		fmt.Fprintf(out, "publish failed: %s\n", oneLine(err))
		return
	}
	res, err := newRegistryClient(node).Publish(registry.PublishOptions{
		Domain:  parsed.Spec.Domain,
		Package: parsed.Spec.Package,
		Version: parsed.Spec.Version,
		Data:    data,
		Prev:    parsed.Prev,
		Force:   parsed.Force,
	}, priv)
	if err != nil {
		fmt.Fprintf(out, "publish failed: %s\n", oneLine(err))
		return
	}
	fmt.Fprintf(out, "published %s (package %s, record %s)\n", parsed.Spec, registry.ShortHash(res.BlobHash), registry.ShortHash(res.RecordHash))
}

// One byte more than the limit is read so an oversized file is refused without
// being loaded whole.
func readPackageFile(name string) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, kademlia.MaxValueSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > kademlia.MaxValueSize {
		return nil, fmt.Errorf("%s is larger than the %d byte limit on stored values", name, kademlia.MaxValueSize)
	}
	return data, nil
}

func loadDomainKey(domain string) (ed25519.PrivateKey, error) {
	dir := getKeysDir()
	priv, err := registry.LoadPrivateKey(filepath.Join(dir, domain))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no private key for %s in %s (create one with: keygen %s)", domain, dir, domain)
	}
	return priv, err
}

func installCommand(out io.Writer, node *kademlia.Kademlia, args []string) {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(out, installUsage)
		return
	}
	spec, err := registry.ParseSpec(args[0])
	if err != nil {
		fmt.Fprintf(out, "install failed: %s\n", oneLine(err))
		return
	}
	version := registry.Latest
	if !spec.Latest {
		version = spec.Version.String()
	}
	got, err := newRegistryClient(node).Install(spec.Domain, spec.Package, version)
	if err != nil {
		fmt.Fprintf(out, "install failed: %s\n", oneLine(err))
		return
	}
	filename := fmt.Sprintf("%s-%s", spec.Package, got.Version)
	if len(args) == 2 {
		filename = args[1]
	}
	if err := os.WriteFile(filename, got.Data, 0o644); err != nil {
		fmt.Fprintf(out, "install failed: %s\n", oneLine(err))
		return
	}
	suffix := ""
	if spec.Latest {
		suffix = ", latest"
	}
	fmt.Fprintf(out, "installed %s:%s %s (%d bytes, package %s%s) to %s\n",
		spec.Domain, spec.Package, got.Version, len(got.Data), registry.ShortHash(got.BlobHash), suffix, filename)
}

// The owners file stands in for the DNS TXT record a real deployment would use.
func keygenCommand(out io.Writer, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(out, keygenUsage)
		return
	}
	domain := args[0]
	if err := registry.ValidateDomainName(domain); err != nil {
		fmt.Fprintf(out, "keygen failed: %s\n", oneLine(err))
		return
	}
	ownersPath, keysDir := getOwnersFile(), getKeysDir()
	owners := registry.NewStaticOwners()
	if _, err := os.Stat(ownersPath); err == nil {
		if owners, err = registry.LoadStaticOwners(ownersPath); err != nil {
			fmt.Fprintf(out, "keygen failed: %s\n", oneLine(err))
			return
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(out, "keygen failed: %s\n", oneLine(err))
		return
	}
	if _, err := owners.PublicKey(domain); err == nil {
		fmt.Fprintf(out, "keygen failed: %s already has an owner key in %s; not replacing it\n", domain, ownersPath)
		return
	}
	pub, priv, err := registry.GenerateKey()
	if err != nil {
		fmt.Fprintf(out, "keygen failed: %s\n", oneLine(err))
		return
	}
	keyPath := filepath.Join(keysDir, domain)
	if err := os.MkdirAll(keysDir, 0o700); err != nil {
		fmt.Fprintf(out, "keygen failed: %s\n", oneLine(err))
		return
	}
	if err := registry.SavePrivateKey(keyPath, priv); err != nil {
		if errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(out, "keygen failed: private key %s already exists; not overwriting it\n", keyPath)
		} else {
			fmt.Fprintf(out, "keygen failed: %s\n", oneLine(err))
		}
		return
	}
	err = owners.Set(domain, pub)
	if err == nil {
		err = writeOwners(ownersPath, owners)
	}
	if err != nil {
		_ = os.Remove(keyPath)
		fmt.Fprintf(out, "keygen failed: %s\n", oneLine(err))
		return
	}
	fmt.Fprintf(out, "created private key %s\n", keyPath)
	fmt.Fprintf(out, "registered %s in %s\n", registry.FormatOwner(domain, pub), ownersPath)
}

// The rename means a node reading the file concurrently sees either the old
// table or the new one.
func writeOwners(path string, owners *registry.StaticOwners) error {
	table := map[string]string{}
	for _, domain := range owners.Domains() {
		pub, err := owners.PublicKey(domain)
		if err != nil {
			return err
		}
		table[domain] = registry.EncodePublicKey(pub)
	}
	data, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".owners-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp makes the file private to its owner; nodes in other containers
	// may run as another user.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func registryShow(out io.Writer, node *kademlia.Kademlia, args []string) bool {
	switch {
	case len(args) > 0 && args[0] == "dns":
		if len(args) != 2 {
			fmt.Fprintln(out, "usage: show dns DOMAIN")
			return true
		}
		showDNS(out, args[1])
	case len(args) > 0 && strings.Contains(args[0], ":"):
		if len(args) != 1 {
			fmt.Fprintln(out, "usage: show DOMAIN:PACKAGE")
			return true
		}
		showPackage(out, node, args[0])
	default:
		return false
	}
	return true
}

func showDNS(out io.Writer, domain string) {
	if err := registry.ValidateDomainName(domain); err != nil {
		fmt.Fprintf(out, "show dns failed: %s\n", oneLine(err))
		return
	}
	pub, err := (&fileOwners{path: getOwnersFile()}).PublicKey(domain)
	switch {
	case errors.Is(err, registry.ErrNoOwner):
		fmt.Fprintf(out, "%s: no owner key known (not listed in %s)\n", domain, getOwnersFile())
	case err != nil:
		fmt.Fprintf(out, "show dns failed: %s\n", oneLine(err))
	default:
		fmt.Fprintln(out, registry.FormatOwner(domain, pub))
	}
}

func showPackage(out io.Writer, node *kademlia.Kademlia, name string) {
	ref, err := registry.ParsePackage(name)
	if err != nil {
		fmt.Fprintf(out, "show failed: %s\n", oneLine(err))
		return
	}
	chain, err := newRegistryClient(node).Chain(ref.Domain, ref.Package)
	if err != nil {
		fmt.Fprintf(out, "show failed: %s\n", oneLine(err))
		return
	}
	fmt.Fprint(out, registry.FormatChain(ref.Domain, ref.Package, chain))
}

func describeEntry(entry kademlia.DataEntry) string {
	if desc, ok := registry.DescribeValue(entry.Key, entry.Value); ok {
		return " " + desc
	}
	return ""
}

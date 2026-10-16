package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"d7024e/experiment"
	"d7024e/kademlia"
	"d7024e/registry"
)

type cliEnv struct {
	t          *testing.T
	dir        string
	ownersPath string
	keysDir    string
	nodes      []*kademlia.Kademlia
}

func newCLIEnv(t *testing.T, nodes int) *cliEnv {
	t.Helper()
	dir := t.TempDir()
	e := &cliEnv{t: t, dir: dir, ownersPath: filepath.Join(dir, "state", "owners.json"), keysDir: filepath.Join(dir, "keys")}
	t.Setenv("OWNERS_FILE", e.ownersPath)
	t.Setenv("KEYS_DIR", e.keysDir)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	network, err := experiment.Build(experiment.BuildConfig{N: nodes, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range network.Nodes {
		installRegistry(node)
	}
	e.nodes = network.Nodes
	return e
}

func (e *cliEnv) run(node int, line string) string {
	e.t.Helper()
	cmd, args := parseCommand(line)
	var out bytes.Buffer
	if !dispatchCommand(&out, nil, e.nodes[node], nil, cmd, args) {
		e.t.Fatalf("%q stopped the CLI", line)
	}
	return out.String()
}

func (e *cliEnv) write(name, content string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *cliEnv) read(name string) string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.dir, name))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

func (e *cliEnv) expect(node int, line, want string) string {
	e.t.Helper()
	out := e.run(node, line)
	if !strings.Contains(out, want) {
		e.t.Fatalf("%q printed %q, want it to contain %q", line, out, want)
	}
	return out
}

func (e *cliEnv) expectOneLine(node int, line string, wants ...string) {
	e.t.Helper()
	out := e.run(node, line)
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		e.t.Fatalf("%q printed %q, want exactly one line", line, out)
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			e.t.Fatalf("%q printed %q, want it to contain %q", line, out, want)
		}
	}
}

// replaceKeyWithStranger replaces the domain's private key with one that is not the owner's.
func (e *cliEnv) replaceKeyWithStranger(domain string) {
	e.t.Helper()
	path := filepath.Join(e.keysDir, domain)
	if err := os.Remove(path); err != nil {
		e.t.Fatal(err)
	}
	_, priv, err := registry.GenerateKey()
	if err != nil {
		e.t.Fatal(err)
	}
	if err := registry.SavePrivateKey(path, priv); err != nil {
		e.t.Fatal(err)
	}
}

func TestPublishReportsPartialPointerUpdate(t *testing.T) {
	e := newCLIEnv(t, 3)
	e.expect(0, "keygen example.com", "created private key")
	e.write("package.txt", "package bytes")
	e.nodes[1].SetValueValidator(func(_ *kademlia.KademliaID, _, _ []byte) error {
		return errors.New("replica refused update")
	})
	e.expectOneLine(0, "publish example.com:demo:1.0.0 package.txt",
		"publish failed:", "updating the latest pointer", "replica refused update",
		"any replicas that accepted the latest pointer keep it")
	key := registry.PointerKey("example.com", "demo")
	for i, node := range e.nodes {
		stored := false
		for _, entry := range node.Entries() {
			stored = stored || entry.Key == key
		}
		if stored != (i != 1) {
			t.Fatalf("node %d stored pointer = %v; want only the two accepting replicas", i, stored)
		}
	}
}

func TestKeygenCreatesKeyAndRegistersOwner(t *testing.T) {
	e := newCLIEnv(t, 3)
	out := e.expect(0, "keygen example.com", "created private key "+filepath.Join(e.keysDir, "example.com"))
	if !strings.Contains(out, "registered example.com: kademlia-pk=") || !strings.Contains(out, e.ownersPath) {
		t.Fatalf("keygen output = %q", out)
	}
	info, err := os.Stat(filepath.Join(e.keysDir, "example.com"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private key file: %v, %v", info, err)
	}
	priv, err := registry.LoadPrivateKey(filepath.Join(e.keysDir, "example.com"))
	if err != nil {
		t.Fatal(err)
	}
	owners, err := registry.LoadStaticOwners(e.ownersPath)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := owners.PublicKey("example.com")
	if err != nil || !pub.Equal(registry.PublicKeyOf(priv)) {
		t.Fatalf("registered key does not belong to the private key: %v", err)
	}
	if info, err := os.Stat(e.ownersPath); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("owners file: %v, %v", info, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(e.ownersPath), ".owners-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}

	e.expect(1, "show dns example.com", registry.TXTRecord(pub))
	e.expect(1, "show dns example.com", registry.Fingerprint(pub))

	e.expect(0, "keygen other.org", "created private key")
	owners, err = registry.LoadStaticOwners(e.ownersPath)
	if err != nil || len(owners.Domains()) != 2 {
		t.Fatalf("owners after a second keygen: %v, %v", owners.Domains(), err)
	}
	if again, _ := owners.PublicKey("example.com"); !again.Equal(pub) {
		t.Fatal("registering a second domain changed the first")
	}
}

func TestKeygenRefusesToOverwrite(t *testing.T) {
	e := newCLIEnv(t, 3)
	e.run(0, "keygen example.com")
	keyBefore, ownersBefore := e.read("keys/example.com"), e.read("state/owners.json")
	e.expectOneLine(0, "keygen example.com", "keygen failed:", "already has an owner key", "not replacing")
	if e.read("keys/example.com") != keyBefore || e.read("state/owners.json") != ownersBefore {
		t.Fatal("a refused keygen changed a file")
	}

	// A key file without a registration, for example one copied in by hand.
	_, priv, _ := registry.GenerateKey()
	if err := registry.SavePrivateKey(filepath.Join(e.keysDir, "mine.org"), priv); err != nil {
		t.Fatal(err)
	}
	e.expectOneLine(0, "keygen mine.org", "keygen failed:", "already exists", "not overwriting")
	owners, _ := registry.LoadStaticOwners(e.ownersPath)
	if _, err := owners.PublicKey("mine.org"); err == nil {
		t.Fatal("a refused keygen registered the domain")
	}
}

func TestKeygenErrors(t *testing.T) {
	e := newCLIEnv(t, 3)
	if out := e.run(0, "keygen"); !strings.Contains(out, "usage: keygen DOMAIN") {
		t.Fatalf("keygen without arguments: %q", out)
	}
	if out := e.run(0, "keygen a.com b.com"); !strings.Contains(out, "usage: keygen DOMAIN") {
		t.Fatalf("keygen with two arguments: %q", out)
	}
	e.expectOneLine(0, "keygen Not_A_Domain", "keygen failed:", "invalid name")
	if _, err := os.Stat(e.keysDir); err == nil {
		t.Fatal("an invalid domain created the key directory")
	}

	if err := os.MkdirAll(filepath.Dir(e.ownersPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.ownersPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.expectOneLine(0, "keygen example.com", "keygen failed:", "owners file")
	if _, err := os.Stat(filepath.Join(e.keysDir, "example.com")); err == nil {
		t.Fatal("keygen left a key behind although the owners file is unusable")
	}
	if e.read("state/owners.json") != "{not json" {
		t.Fatal("keygen rewrote an owners file it could not read")
	}
}

func TestKeygenRemovesKeyWhenOwnersCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not bind root")
	}
	e := newCLIEnv(t, 3)
	locked := filepath.Join(e.dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	t.Setenv("OWNERS_FILE", filepath.Join(locked, "owners.json"))
	e.expectOneLine(0, "keygen example.com", "keygen failed:")
	if _, err := os.Stat(filepath.Join(e.keysDir, "example.com")); err == nil {
		t.Fatal("the private key of an unregistered domain was kept")
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	e.expect(0, "keygen example.com", "created private key")
}

func TestKeygenReportsUnusableOwnersLocation(t *testing.T) {
	e := newCLIEnv(t, 3)
	blocker := filepath.Join(e.dir, "blocker")
	e.write("blocker", "a file, not a directory")
	t.Setenv("OWNERS_FILE", filepath.Join(blocker, "owners.json"))
	e.expectOneLine(0, "keygen example.com", "keygen failed:")
	if _, err := os.Stat(filepath.Join(e.keysDir, "example.com")); err == nil {
		t.Fatal("a key was created although the owners file cannot exist")
	}
}

func TestPublishInstallAndShowAcrossNodes(t *testing.T) {
	e := newCLIEnv(t, 12)
	e.run(0, "keygen example.com")
	e.write("tool-v1.bin", "tool version one")
	e.write("tool-v11.bin", "tool version one-one, a little longer")

	out := e.expect(0, "publish example.com:tool:1.0.0 tool-v1.bin", "published example.com:tool:1.0.0 (package ")
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, ", record ") {
		t.Fatalf("publish output = %q", out)
	}
	e.expect(0, "publish example.com:tool:1.1.0 tool-v11.bin", "published example.com:tool:1.1.0")

	out = e.expect(7, "install example.com:tool:latest", "installed example.com:tool 1.1.0 (37 bytes, package ")
	if !strings.Contains(out, ", latest) to tool-1.1.0") {
		t.Fatalf("install output = %q", out)
	}
	if e.read("tool-1.1.0") != "tool version one-one, a little longer" {
		t.Fatalf("installed file holds %q", e.read("tool-1.1.0"))
	}
	e.expect(9, "install example.com:tool:1.0.0 elder.bin", "to elder.bin")
	if e.read("elder.bin") != "tool version one" {
		t.Fatalf("installed old version holds %q", e.read("elder.bin"))
	}
	e.expect(9, "install example.com:tool:1.1.0", "installed example.com:tool 1.1.0 (37 bytes, package ")

	out = e.expect(5, "show example.com:tool", "example.com:tool has 2 versions (newest first)")
	if !strings.Contains(out, "1.1.0") || !strings.Contains(out, "(latest)") || !strings.Contains(out, "1.0.0") {
		t.Fatalf("show chain = %q", out)
	}
	if strings.Index(out, "1.1.0") > strings.Index(out, "1.0.0") {
		t.Fatalf("chain is not newest first: %q", out)
	}

	out = e.expect(0, "show ds", "latest-pointer example.com:tool -> 1.1.0 record=")
	for _, want := range []string{"version-record example.com:tool 1.0.0 blob=", "prev=none", "version-record example.com:tool 1.1.0 blob="} {
		if !strings.Contains(out, want) {
			t.Fatalf("show ds lacks %q:\n%s", want, out)
		}
	}
}

func TestPublishErrors(t *testing.T) {
	e := newCLIEnv(t, 12)
	e.write("pkg.bin", "contents")
	e.write("big.bin", strings.Repeat("x", kademlia.MaxValueSize+1))

	if out := e.run(0, "publish"); !strings.Contains(out, "usage: publish [--force] [--prev=PREVIOUS-VERSION] DOMAIN:PACKAGE:VERSION FILENAME") {
		t.Fatalf("publish without arguments: %q", out)
	}
	for line, want := range map[string]string{
		"publish example.com:tool:1.0.0":                  "want DOMAIN:PACKAGE:VERSION and FILENAME",
		"publish --bogus example.com:tool:1.0.0 pkg.bin":  "unknown flag",
		"publish --prev=x example.com:tool:1.0.0 pkg.bin": "invalid version",
		"publish example.com:tool pkg.bin":                "not DOMAIN:PACKAGE:VERSION",
		"publish example.com:tool:latest pkg.bin":         "reserved",
		"publish Example.com:tool:1.0.0 pkg.bin":          "invalid name",
		"publish example.com:tool:1.0 pkg.bin":            "MAJOR.MINOR.PATCH",
		"publish example.com:tool:1.0.0 missing.bin":      "no such file",
		"publish example.com:tool:1.0.0 big.bin":          "4096 byte limit",
		"publish example.com:tool:1.0.0 pkg.bin":          "no private key for example.com",
	} {
		e.expectOneLine(0, line, "publish failed:", want)
	}
	e.expectOneLine(0, "publish example.com:tool:1.0.0 pkg.bin", "keygen example.com")

	_, priv, _ := registry.GenerateKey()
	if err := os.MkdirAll(e.keysDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := registry.SavePrivateKey(filepath.Join(e.keysDir, "example.com"), priv); err != nil {
		t.Fatal(err)
	}
	e.expectOneLine(0, "publish example.com:tool:1.0.0 pkg.bin", "publish failed:", "domain has no owner key", "not listed in")
	e.expectOneLine(0, "publish --force example.com:tool:1.0.0 pkg.bin", "publish failed:", "domain has no owner key")
}

func TestPublishRejectsInvalidUpdates(t *testing.T) {
	e := newCLIEnv(t, 12)
	e.run(0, "keygen example.com")
	e.write("one.bin", "one")
	e.write("two.bin", "two")
	e.write("three.bin", "three")
	e.expect(0, "publish example.com:tool:1.0.0 one.bin", "published")
	e.expect(0, "publish example.com:tool:1.1.0 two.bin", "published")

	// Without --force the node refuses before uploading anything.
	e.expectOneLine(0, "publish example.com:tool:1.0.5 three.bin", "publish failed:", "refusing to publish example.com:tool:1.0.5", "roll back")
	e.expectOneLine(0, "publish example.com:tool:1.1.0 three.bin", "publish failed:", "refusing to publish", "fork")
	e.expectOneLine(0, "publish --prev=0.9.0 example.com:tool:1.2.0 three.bin", "publish failed:", "unknown version", "0.9.0")
	e.expectOneLine(0, "publish --prev=1.0.0 example.com:tool:1.2.0 three.bin", "publish failed:", "refusing to publish", "fork")
	e.expectOneLine(0, "publish --prev=1.1.0 example.com:tool:1.0.0 three.bin", "publish failed:", "refusing to publish", "roll back")
	for i, node := range e.nodes {
		for _, entry := range node.Entries() {
			if entry.Key == registry.Hash([]byte("three")) {
				t.Fatalf("node %d holds a package that was refused before upload", i)
			}
		}
	}

	// With --force the attempt reaches the replicas, which refuse it.
	e.expectOneLine(1, "publish --force example.com:tool:1.0.5 three.bin", "publish failed:", "updating the latest pointer", "roll back")
	e.expectOneLine(1, "publish --force example.com:tool:1.1.0 three.bin", "publish failed:", "updating the latest pointer", "fork")
	e.expectOneLine(1, "publish --force --prev=1.0.0 example.com:tool:1.2.0 three.bin", "publish failed:", "updating the latest pointer", "fork")
	e.expectOneLine(1, "publish --force --prev=1.1.0 example.com:tool:1.0.0 three.bin", "publish failed:", "updating the latest pointer", "roll back")

	// A signature from anyone but the owner is refused with and without --force.
	e.replaceKeyWithStranger("example.com")
	e.expectOneLine(2, "publish example.com:tool:1.2.0 three.bin", "publish failed:", "refusing to publish", "not signed by the owner of example.com")
	e.expectOneLine(2, "publish --force example.com:tool:1.2.0 three.bin", "publish failed:", "updating the latest pointer", "not signed by the owner of example.com")

	out := e.expect(6, "show example.com:tool", "example.com:tool has 2 versions")
	if !strings.Contains(out, "1.1.0") || strings.Contains(out, "1.2.0") {
		t.Fatalf("the chain changed: %q", out)
	}
}

// Exercise the same command path as the demo with real UDP RPCs. Simulated
// transports return Go errors directly; UDP must also carry replica refusals
// back to the publisher without making --force bypass receiver validation.
func TestPublishForceOverUDP(t *testing.T) {
	e := newCLIEnv(t, 3)
	contacts := make([]kademlia.Contact, len(e.nodes))
	tables := make([]*kademlia.RoutingTable, len(e.nodes))
	for i := range e.nodes {
		port := reserveTestPort(t)
		address := "127.0.0.1:" + strconv.Itoa(port)
		me := kademlia.NewContact(idFromAddress(address), address)
		table := kademlia.NewRoutingTable(me)
		transport := kademlia.NewNetwork(me)
		transport.Timeout = time.Second
		transport.Retries = 0
		node := kademlia.NewKademlia(table, transport)
		node.SetLookupLogger(nil)
		transport.AttachKademlia(node)
		installRegistry(node)
		if err := transport.Listen("127.0.0.1", port); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = transport.Close() })
		e.nodes[i], contacts[i], tables[i] = node, me, table
	}
	for i, table := range tables {
		for j, contact := range contacts {
			if i != j {
				table.AddContact(contact)
			}
		}
	}
	e.expect(0, "keygen example.com", "created private key")
	e.write("first.bin", "first release")
	e.write("second.bin", "second release")
	e.expect(0, "publish --force example.com:tool:1.0.0 first.bin", "published")
	e.expect(1, "publish --force --prev=1.0.0 example.com:tool:2.0.0 second.bin", "published")

	stored := func(node *kademlia.Kademlia, key string) []byte {
		for _, entry := range node.Entries() {
			if entry.Key == key {
				return entry.Value
			}
		}
		return nil
	}
	pointerKey := registry.PointerKey("example.com", "tool")
	before := make([][]byte, len(e.nodes))
	for i, node := range e.nodes {
		before[i] = stored(node, pointerKey)
		if before[i] == nil {
			t.Fatalf("UDP replica %d did not receive the valid latest pointer", i)
		}
	}
	cases := []struct {
		name, arguments, reason string
	}{
		{"rollback", "example.com:tool:1.5.0", "roll back"},
		{"same-version fork", "example.com:tool:2.0.0", "fork"},
		{"older-parent fork", "--prev=1.0.0 example.com:tool:3.0.0", "fork"},
		{"same-version predecessor", "--prev=2.0.0 example.com:tool:2.0.0", "fork"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := "invalid publication: " + tc.name
			e.write("invalid.bin", payload)
			command := "publish " + tc.arguments + " invalid.bin"
			e.expectOneLine(0, command, "publish failed:", "refusing to publish", tc.reason)
			for i, node := range e.nodes {
				if stored(node, registry.Hash([]byte(payload))) != nil {
					t.Fatalf("UDP replica %d received a package refused by local preflight", i)
				}
			}
			command = "publish --force " + tc.arguments + " invalid.bin"
			out := e.expect(0, command, "updating the latest pointer")
			if !strings.Contains(out, tc.reason) || !strings.Contains(out, "refused by 3 nodes") || strings.Contains(out, "refusing to publish") {
				t.Fatalf("forced publication did not report a receiver refusal: %q", out)
			}
			t.Logf("%s\n%s", command, strings.TrimSpace(out))
			for i, node := range e.nodes {
				if !bytes.Equal(stored(node, registry.Hash([]byte(payload))), []byte(payload)) {
					t.Fatalf("UDP replica %d did not receive the forced package upload", i)
				}
				if !bytes.Equal(stored(node, pointerKey), before[i]) {
					t.Fatalf("UDP replica %d changed its latest pointer after an invalid forced update", i)
				}
			}
		})
	}
	e.expect(2, "install example.com:tool:latest verified.bin", "2.0.0")
	if got := e.read("verified.bin"); got != "second release" {
		t.Fatalf("latest package after forced attempts = %q", got)
	}
}

func TestInstallErrors(t *testing.T) {
	e := newCLIEnv(t, 12)
	e.run(0, "keygen example.com")
	e.write("one.bin", "one")
	e.expect(0, "publish example.com:tool:1.0.0 one.bin", "published")

	for _, line := range []string{"install", "install a:b:1.0.0 x y"} {
		if out := e.run(3, line); !strings.Contains(out, "usage: install DOMAIN:PACKAGE:VERSION [FILENAME]") {
			t.Fatalf("%q printed %q", line, out)
		}
	}
	for line, want := range map[string]string{
		"install example.com:tool":                   "not DOMAIN:PACKAGE:VERSION",
		"install example.com:tool:one":               "MAJOR.MINOR.PATCH",
		"install Example.com:tool:1.0.0":             "invalid name",
		"install example.com:tool:2.0.0":             "unknown version",
		"install example.com:nothing:latest":         "not found",
		"install unknown.net:tool:latest":            "domain has no owner key",
		"install example.com:tool:1.0.0 .":           "install failed:",
		"install example.com:tool:1.0.0 /dev/null/x": "install failed:",
	} {
		e.expectOneLine(3, line, "install failed:", want)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "tool-1.0.0")); err == nil {
		t.Fatal("a failed install left a file behind")
	}
}

func TestShowRegistryErrorsAndUsage(t *testing.T) {
	e := newCLIEnv(t, 5)
	for _, line := range []string{"show dns", "show dns a.com b.com"} {
		if out := e.run(0, line); !strings.Contains(out, "usage: show dns DOMAIN") {
			t.Fatalf("%q printed %q", line, out)
		}
	}
	if out := e.run(0, "show a.com:tool extra"); !strings.Contains(out, "usage: show DOMAIN:PACKAGE") {
		t.Fatalf("show with two arguments printed %q", out)
	}
	for _, line := range []string{"show bogus", "show"} {
		if out := e.run(0, line); !strings.Contains(out, "usage: show rt|ds|DOMAIN:PACKAGE|dns DOMAIN") {
			t.Fatalf("%q printed %q", line, out)
		}
	}
	e.expectOneLine(0, "show dns unknown.net", "unknown.net: no owner key known", e.ownersPath)
	e.expectOneLine(0, "show dns Not_A_Domain", "show dns failed:", "invalid name")
	e.expectOneLine(0, "show a:b:c", "show failed:", "not DOMAIN:PACKAGE")
	e.expectOneLine(0, "show example.com:tool", "show failed:", "domain has no owner key")

	e.run(0, "keygen example.com")
	e.expectOneLine(0, "show example.com:tool", "show failed:", "latest pointer of example.com:tool", "not found")
	e.expectOneLine(0, "show Example.com:tool", "show failed:", "invalid name")

	if err := os.WriteFile(e.ownersPath, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.expectOneLine(0, "show dns example.com", "show dns failed:", "owners file")
}

func TestShowDsDescribesOnlyRegistryValues(t *testing.T) {
	e := newCLIEnv(t, 3)
	e.write("plain.txt", "just bytes")
	e.expect(0, "put plain.txt", "stored as ")
	out := e.run(0, "show ds")
	if !strings.Contains(out, "(10 bytes)\n") || strings.Contains(out, "version-record") || strings.Contains(out, "latest-pointer") {
		t.Fatalf("show ds = %q", out)
	}
	if got := describeEntry(kademlia.DataEntry{Key: strings.Repeat("a", 64), Value: []byte("{}")}); got != "" {
		t.Fatalf("describeEntry of a plain value = %q", got)
	}
}

func TestFileOwnersFollowTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owners.json")
	owners := &fileOwners{path: path}

	if _, err := owners.PublicKey("example.com"); !errors.Is(err, registry.ErrNoOwner) || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing owners file: %v", err)
	}

	pubA, _, _ := registry.GenerateKey()
	table := registry.NewStaticOwners()
	if err := table.Set("example.com", pubA); err != nil {
		t.Fatal(err)
	}
	if err := writeOwners(path, table); err != nil {
		t.Fatal(err)
	}
	if got, err := owners.PublicKey("example.com"); err != nil || !got.Equal(pubA) {
		t.Fatalf("after the file appears: %v", err)
	}
	if _, err := owners.PublicKey("other.org"); !errors.Is(err, registry.ErrNoOwner) {
		t.Fatalf("unlisted domain: %v", err)
	}

	pubB, _, _ := registry.GenerateKey()
	if err := table.Set("other.org", pubB); err != nil {
		t.Fatal(err)
	}
	if err := writeOwners(path, table); err != nil {
		t.Fatal(err)
	}
	if got, err := owners.PublicKey("other.org"); err != nil || !got.Equal(pubB) {
		t.Fatalf("after another node registers a domain: %v", err)
	}

	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := owners.PublicKey("example.com"); !errors.Is(err, registry.ErrOwnerRecord) {
		t.Fatalf("corrupt owners file: %v", err)
	}
	if _, err := owners.PublicKey("example.com"); !errors.Is(err, registry.ErrOwnerRecord) {
		t.Fatalf("corrupt owners file, second read: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := owners.PublicKey("example.com"); !errors.Is(err, registry.ErrNoOwner) {
		t.Fatalf("removed owners file: %v", err)
	}

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	unusable := &fileOwners{path: filepath.Join(blocker, "owners.json")}
	if _, err := unusable.PublicKey("example.com"); !errors.Is(err, registry.ErrOwnerLookup) {
		t.Fatalf("owners file below a regular file: %v", err)
	}
}

func TestDefaultRegistryLocations(t *testing.T) {
	t.Setenv("OWNERS_FILE", "")
	t.Setenv("KEYS_DIR", "")
	if getOwnersFile() != "owners.json" || getKeysDir() != "keys" {
		t.Fatalf("defaults are %q and %q", getOwnersFile(), getKeysDir())
	}
	t.Setenv("OWNERS_FILE", "/shared/owners.json")
	t.Setenv("KEYS_DIR", "/secrets")
	if getOwnersFile() != "/shared/owners.json" || getKeysDir() != "/secrets" {
		t.Fatalf("overrides are %q and %q", getOwnersFile(), getKeysDir())
	}
}

func TestOneLineJoinsAndDeduplicates(t *testing.T) {
	err := errors.Join(errors.New("first"), errors.New("  second  "), errors.New("second"), errors.New(""))
	if got := oneLine(err); got != "first; second" {
		t.Fatalf("oneLine = %q", got)
	}
	if got := oneLine(errors.New("plain")); got != "plain" {
		t.Fatalf("oneLine = %q", got)
	}
}

func TestRunNodeServesTheRegistry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OWNERS_FILE", filepath.Join(dir, "owners.json"))
	t.Setenv("KEYS_DIR", filepath.Join(dir, "keys"))
	t.Setenv("BIND_IP", "127.0.0.1")
	t.Setenv("PORT", strconv.Itoa(reserveTestPort(t)))
	t.Setenv("BOOTSTRAP", "")
	t.Setenv("HEADLESS", "")
	t.Setenv("REPLICATION_INTERVAL", "1h")
	t.Setenv("REFRESH_INTERVAL", "1h")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.WriteFile("pkg.bin", []byte("served by a real node"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := strings.Join([]string{
		"publish example.com:tool:1.0.0 pkg.bin",
		"keygen example.com",
		"publish example.com:tool:1.0.0 pkg.bin",
		"install example.com:tool:latest",
		"show example.com:tool",
		"show dns example.com",
		"show ds",
		"exit",
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := runNode(context.Background(), strings.NewReader(script), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"publish failed: no private key for example.com",
		"created private key",
		"published example.com:tool:1.0.0",
		"installed example.com:tool 1.0.0 (21 bytes",
		"example.com:tool has 1 version (newest first)",
		"example.com: kademlia-pk=",
		"latest-pointer example.com:tool -> 1.0.0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("node output lacks %q:\n%s", want, out.String())
		}
	}
	if data, err := os.ReadFile("tool-1.0.0"); err != nil || string(data) != "served by a real node" {
		t.Fatalf("installed file: %q, %v", data, err)
	}
}

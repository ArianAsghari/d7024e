package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"d7024e/kademlia"
)

func newTestNode(t *testing.T) (*kademlia.RoutingTable, *kademlia.Kademlia, *kademlia.Network) {
	t.Helper()
	me := kademlia.NewContact(idFromAddress("127.0.0.1:9000"), "127.0.0.1:9000")
	routingTable := kademlia.NewRoutingTable(me)
	network := kademlia.NewNetwork(me)
	network.Timeout = 20 * time.Millisecond
	network.Retries = 0
	node := kademlia.NewKademlia(routingTable, network)
	network.AttachKademlia(node)
	return routingTable, node, network
}

func TestNodeStartupMessageIsUsable(t *testing.T) {
	me := kademlia.NewContact(idFromAddress("127.0.0.1:9000"), "127.0.0.1:9000")
	if got := me.String(); !strings.Contains(got, "127.0.0.1:9000") {
		t.Errorf("Contact.String() = %q, want it to contain the address", got)
	}
}

func TestGetPort(t *testing.T) {
	t.Setenv("PORT", "")
	os.Unsetenv("PORT")
	if got := getPort(); got != 8000 {
		t.Errorf("getPort() with no PORT set = %d, want 8000", got)
	}

	t.Setenv("PORT", "9999")
	if got := getPort(); got != 9999 {
		t.Errorf("getPort() with PORT=9999 = %d, want 9999", got)
	}

	t.Setenv("PORT", "not-a-number")
	if got := getPort(); got != 8000 {
		t.Errorf("getPort() with invalid PORT = %d, want fallback 8000", got)
	}
}

func TestGetOutboundIP(t *testing.T) {
	if got := net.ParseIP(getOutboundIP()); got == nil {
		t.Errorf("getOutboundIP() = %q, want a valid IP address", getOutboundIP())
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		line     string
		wantCmd  string
		wantArgs []string
	}{
		{"", "", nil},
		{"   ", "", nil},
		{"exit", "exit", []string{}},
		{"PING 1.2.3.4:8000", "ping", []string{"1.2.3.4:8000"}},
		{"  show   rt  ", "show", []string{"rt"}},
	}
	for _, c := range cases {
		cmd, args := parseCommand(c.line)
		if cmd != c.wantCmd || !equalStrings(args, c.wantArgs) {
			t.Errorf("parseCommand(%q) = (%q, %v), want (%q, %v)", c.line, cmd, args, c.wantCmd, c.wantArgs)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTruncateID(t *testing.T) {
	short := "abcd"
	if got := truncateID(short); got != short {
		t.Errorf("truncateID(%q) = %q, want unchanged", short, got)
	}
	long := strings.Repeat("a", 64)
	got := truncateID(long)
	if got != "aaaaaaaa…aaaa" {
		t.Errorf("truncateID(long) = %q, want truncated form", got)
	}
}

func TestDispatchCommandExitStopsLoop(t *testing.T) {
	var out bytes.Buffer
	routingTable, node, network := newTestNode(t)
	if cont := dispatchCommand(&out, routingTable, node, network, "exit", nil); cont {
		t.Fatal("dispatchCommand(exit) should return false")
	}
	if !strings.Contains(out.String(), "bye") {
		t.Errorf("exit output = %q, want to contain \"bye\"", out.String())
	}
}

func TestDispatchCommandUnknown(t *testing.T) {
	var out bytes.Buffer
	routingTable, node, network := newTestNode(t)
	if cont := dispatchCommand(&out, routingTable, node, network, "frobnicate", nil); !cont {
		t.Fatal("dispatchCommand(unknown) should return true")
	}
	if !strings.Contains(out.String(), `unknown command "frobnicate"`) {
		t.Errorf("unknown output = %q", out.String())
	}
}

func TestDispatchCommandPutGetRoundTrip(t *testing.T) {
	routingTable, node, network := newTestNode(t)
	dir := t.TempDir()

	inPath := filepath.Join(dir, "in.txt")
	if err := os.WriteFile(inPath, []byte("hello kademlia"), 0o644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	var out bytes.Buffer
	dispatchCommand(&out, routingTable, node, network, "put", []string{inPath})
	putOutput := out.String()
	if !strings.HasPrefix(putOutput, "stored as ") {
		t.Fatalf("put output = %q, want prefix \"stored as \"", putOutput)
	}
	key := strings.TrimSpace(strings.TrimPrefix(putOutput, "stored as "))

	out.Reset()
	dispatchCommand(&out, routingTable, node, network, "get", []string{key})
	if got := out.String(); !strings.Contains(got, "hello kademlia") || !strings.Contains(got, "(found locally)") {
		t.Errorf("get output = %q, want value and \"(found locally)\"", got)
	}

	outPath := filepath.Join(dir, "out.txt")
	out.Reset()
	dispatchCommand(&out, routingTable, node, network, "get", []string{key, outPath})
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(data) != "hello kademlia" {
		t.Errorf("output file content = %q, want %q", data, "hello kademlia")
	}
}

func TestDispatchCommandGetNotFound(t *testing.T) {
	var out bytes.Buffer
	routingTable, node, network := newTestNode(t)
	missingKey := strings.Repeat("00", 32)
	dispatchCommand(&out, routingTable, node, network, "get", []string{missingKey})
	if !strings.Contains(out.String(), "get failed") {
		t.Errorf("get output = %q, want a failure message", out.String())
	}
}

func TestDispatchCommandShowEmptyRoutingTableAndDataStore(t *testing.T) {
	var out bytes.Buffer
	routingTable, node, network := newTestNode(t)

	dispatchCommand(&out, routingTable, node, network, "show", []string{"rt"})
	if !strings.Contains(out.String(), "(no contacts yet)") {
		t.Errorf("show rt output = %q, want \"(no contacts yet)\"", out.String())
	}

	out.Reset()
	dispatchCommand(&out, routingTable, node, network, "show", []string{"ds"})
	if !strings.Contains(out.String(), "(empty)") {
		t.Errorf("show ds output = %q, want \"(empty)\"", out.String())
	}
}

func TestDispatchCommandShowDsAfterPut(t *testing.T) {
	routingTable, node, network := newTestNode(t)
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.txt")
	os.WriteFile(inPath, []byte("data"), 0o644)

	var out bytes.Buffer
	dispatchCommand(&out, routingTable, node, network, "put", []string{inPath})

	out.Reset()
	dispatchCommand(&out, routingTable, node, network, "show", []string{"ds"})
	if !strings.Contains(out.String(), "bytes") {
		t.Errorf("show ds output = %q, want an entry with a byte count", out.String())
	}
}

func TestDispatchCommandPingFailsFast(t *testing.T) {
	var out bytes.Buffer
	routingTable, node, network := newTestNode(t)
	dispatchCommand(&out, routingTable, node, network, "ping", []string{"127.0.0.1:1"})
	if !strings.Contains(out.String(), "ping failed") {
		t.Errorf("ping output = %q, want a failure message", out.String())
	}
}

func TestDispatchCommandPingSucceeds(t *testing.T) {
	reserved, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("reserve UDP port: %v", err)
	}
	peerAddress := reserved.LocalAddr().String()
	if err := reserved.Close(); err != nil {
		t.Fatalf("close reserved socket: %v", err)
	}

	peerMe := kademlia.NewContact(idFromAddress(peerAddress), peerAddress)
	peerNetwork := kademlia.NewNetwork(peerMe)
	peerNode := kademlia.NewKademlia(kademlia.NewRoutingTable(peerMe), peerNetwork)
	peerNetwork.AttachKademlia(peerNode)
	host, portText, err := net.SplitHostPort(peerAddress)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", peerAddress, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portText, err)
	}
	if err := peerNetwork.Listen(host, port); err != nil {
		t.Fatalf("peer Listen: %v", err)
	}
	defer peerNetwork.Close()

	var out bytes.Buffer
	routingTable, node, network := newTestNode(t)
	dispatchCommand(&out, routingTable, node, network, "ping", []string{peerAddress})
	if !strings.Contains(out.String(), "pong from") {
		t.Errorf("ping output = %q, want a pong response", out.String())
	}
}

func TestFormatRoutingTableWithContacts(t *testing.T) {
	routingTable, node, network := newTestNode(t)
	known := kademlia.NewContact(idFromAddress("127.0.0.1:7000"), "127.0.0.1:7000")
	routingTable.AddContact(known)

	var out bytes.Buffer
	dispatchCommand(&out, routingTable, node, network, "show", []string{"rt"})
	if !strings.Contains(out.String(), "bucket ") || !strings.Contains(out.String(), "127.0.0.1:7000") {
		t.Errorf("show rt output = %q, want a bucket listing the known contact", out.String())
	}
}

func TestDispatchCommandPutMissingFile(t *testing.T) {
	var out bytes.Buffer
	routingTable, node, network := newTestNode(t)
	dispatchCommand(&out, routingTable, node, network, "put", []string{filepath.Join(t.TempDir(), "does-not-exist")})
	if !strings.Contains(out.String(), "put failed") {
		t.Errorf("put output = %q, want a failure message", out.String())
	}
}

func TestDispatchCommandUsageErrors(t *testing.T) {
	routingTable, node, network := newTestNode(t)
	cases := []struct {
		cmd  string
		args []string
	}{
		{"ping", nil},
		{"put", nil},
		{"get", nil},
		{"show", nil},
		{"show", []string{"bogus"}},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if cont := dispatchCommand(&out, routingTable, node, network, c.cmd, c.args); !cont {
			t.Errorf("dispatchCommand(%q, %v) should return true", c.cmd, c.args)
		}
		if !strings.Contains(out.String(), "usage:") {
			t.Errorf("dispatchCommand(%q, %v) output = %q, want a usage message", c.cmd, c.args, out.String())
		}
	}
}

func TestRunCLIProcessesCommandsUntilExit(t *testing.T) {
	routingTable, node, network := newTestNode(t)
	in := strings.NewReader("show ds\nexit\n")
	var out bytes.Buffer
	runCLI(in, &out, routingTable, node, network)
	if !strings.Contains(out.String(), "(empty)") || !strings.Contains(out.String(), "bye") {
		t.Errorf("runCLI output = %q, want both the show ds and exit output", out.String())
	}
}

func TestRunCLIStopsAtEOFWithoutExit(t *testing.T) {
	routingTable, node, network := newTestNode(t)
	in := strings.NewReader("show ds\n")
	var out bytes.Buffer
	runCLI(in, &out, routingTable, node, network)
	if strings.Contains(out.String(), "bye") {
		t.Errorf("runCLI output = %q, should not print \"bye\" without an exit command", out.String())
	}
}

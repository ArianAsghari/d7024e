package kademlia

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func TestNetworkPingRoundTrip(t *testing.T) {
	serverNetwork, _, serverContact := startTestNode(t, testID(20))
	defer serverNetwork.Close()

	clientNetwork := NewNetwork(NewContact(testID(10), "127.0.0.1:9999"))
	clientNetwork.Timeout = 200 * time.Millisecond
	clientNetwork.Retries = 0

	rtt, err := clientNetwork.SendPingMessage(&serverContact)
	if err != nil {
		t.Fatalf("SendPingMessage returned error: %v", err)
	}
	if rtt <= 0 {
		t.Fatalf("ping RTT = %v, want > 0", rtt)
	}
}

func TestNetworkFindContactRoundTrip(t *testing.T) {
	serverNetwork, serverNode, serverContact := startTestNode(t, testID(20))
	defer serverNetwork.Close()

	known := NewContact(testID(3), "127.0.0.1:34567")
	serverNode.routingTable.AddContact(known)

	clientNetwork := NewNetwork(NewContact(testID(10), "127.0.0.1:9999"))
	clientNetwork.Timeout = 200 * time.Millisecond
	clientNetwork.Retries = 0

	contacts, err := clientNetwork.SendFindContactMessage(&serverContact, testID(2))
	if err != nil {
		t.Fatalf("SendFindContactMessage returned error: %v", err)
	}
	if len(contacts) == 0 || !contacts[0].ID.Equals(known.ID) || contacts[0].Address != known.Address {
		t.Fatalf("FIND_NODE returned %v, want closest contact %v first", contacts, known)
	}
}

func TestNetworkStoreThenFindData(t *testing.T) {
	serverNetwork, _, serverContact := startTestNode(t, testID(20))
	defer serverNetwork.Close()

	clientNetwork := NewNetwork(NewContact(testID(10), "127.0.0.1:9999"))
	clientNetwork.Timeout = 200 * time.Millisecond
	clientNetwork.Retries = 0

	data := []byte("network round trip")
	key := hashData(data)
	if err := clientNetwork.SendStoreMessage(&serverContact, key, data); err != nil {
		t.Fatalf("SendStoreMessage returned error: %v", err)
	}

	result, err := clientNetwork.SendFindDataMessage(&serverContact, key)
	if err != nil {
		t.Fatalf("SendFindDataMessage returned error: %v", err)
	}
	if !result.Found || string(result.Value) != string(data) {
		t.Fatalf("FIND_VALUE result = %+v, want found data %q", result, data)
	}
}

func TestNetworkStoreRejectsHashMismatch(t *testing.T) {
	serverNetwork, _, serverContact := startTestNode(t, testID(20))
	defer serverNetwork.Close()

	clientNetwork := NewNetwork(NewContact(testID(10), "127.0.0.1:9999"))
	clientNetwork.Timeout = 200 * time.Millisecond
	clientNetwork.Retries = 0

	err := clientNetwork.SendStoreMessage(&serverContact, hashData([]byte("expected")), []byte("tampered"))
	if err == nil || err.Error() != ErrHashMismatch.Error() {
		t.Fatalf("STORE mismatch error = %v, want %v", err, ErrHashMismatch)
	}
}

func TestNetworkRetriesAndTimesOut(t *testing.T) {
	blackhole, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("start UDP blackhole: %v", err)
	}
	defer blackhole.Close()

	contact := NewContact(testID(20), blackhole.LocalAddr().String())
	clientNetwork := NewNetwork(NewContact(testID(10), "127.0.0.1:9999"))
	clientNetwork.Timeout = 20 * time.Millisecond
	clientNetwork.Retries = 1

	start := time.Now()
	_, err = clientNetwork.SendPingMessage(&contact)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("SendPingMessage unexpectedly succeeded")
	}
	if elapsed < 35*time.Millisecond {
		t.Fatalf("RPC returned too quickly (%v), expected two timeout attempts", elapsed)
	}
}

func startTestNode(t *testing.T, id *KademliaID) (*Network, *Kademlia, Contact) {
	t.Helper()

	address := reserveUDPAddress(t)
	me := NewContact(id, address)
	routingTable := NewRoutingTable(me)
	network := NewNetwork(me)
	node := NewKademlia(routingTable, network)
	network.AttachKademlia(node)
	network.Timeout = 200 * time.Millisecond
	network.Retries = 0

	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portText, err)
	}
	if err := network.Listen(host, port); err != nil {
		t.Fatalf("Listen returned error: %v", err)
	}

	return network, node, me
}

func reserveUDPAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("reserve UDP port: %v", err)
	}
	address := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatalf("close reserved UDP socket: %v", err)
	}
	return address
}

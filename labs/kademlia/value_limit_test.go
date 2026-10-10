package kademlia

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValueSizeLimitAtAllStoreEntryPoints(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	rt.AddContact(NewContact(testID(1), "peer"))
	rpc := &fakeRPCClient{}
	node := NewKademlia(rt, rpc)
	value := make([]byte, MaxValueSize+1)
	key := hashData(value)
	if got, err := node.Store(value); !errors.Is(err, ErrValueTooLarge) || got != "" {
		t.Fatalf("oversized Store = %s, %v", got, err)
	}
	if err := node.StoreValue(key, value); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("oversized StoreValue = %v", err)
	}
	if err := node.StoreAt(key, value); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("oversized StoreAt = %v", err)
	}
	if err := node.StoreAt(nil, []byte("pointer")); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("nil-key StoreAt = %v", err)
	}
	if err := NewNetwork(rt.me).SendStoreMessage(nil, key, value); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("oversized network store = %v", err)
	}
	if len(rpc.contactCalls) != 0 || len(rpc.storeCalls) != 0 || len(node.Entries()) != 0 {
		t.Fatal("oversized value caused network calls or local writes")
	}
	for _, size := range []int{0, 255, MaxValueSize} {
		value := make([]byte, size)
		if err := node.StoreValue(hashData(value), value); err != nil {
			t.Fatalf("size %d was rejected: %v", size, err)
		}
	}
}

func TestLookupDataRejectsOversizedRemoteValue(t *testing.T) {
	value := make([]byte, MaxValueSize+1)
	key := hashData(value)
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	rt.AddContact(NewContact(testID(1), "peer"))
	node := NewKademlia(rt, &maintenanceRPC{data: func(*Contact, *KademliaID) (FindDataResult, error) {
		return FindDataResult{Found: true, Value: value}, nil
	}})
	if _, _, err := node.LookupData(key.String()); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("oversized FIND_VALUE = %v", err)
	}
	if len(node.Entries()) != 0 {
		t.Fatal("oversized reply was cached")
	}
}

func TestUDPValueLimitRoundTripAndIncomingRejection(t *testing.T) {
	transport, node, peer := startTestNode(t, testID(1))
	defer transport.Close()
	client := NewNetwork(NewContact(testID(0), "127.0.0.1:9999"))
	client.Timeout = time.Second
	client.Retries = 0
	value := bytes.Repeat([]byte{0xff}, MaxValueSize)
	key := hashData(value)
	if err := client.SendStoreMessage(&peer, key, value); err != nil {
		t.Fatal(err)
	}
	result, err := client.SendFindDataMessage(&peer, key)
	if err != nil || !result.Found || !bytes.Equal(result.Value, value) {
		t.Fatalf("maximum-sized UDP value: found = %v, error = %v", result.Found, err)
	}
	value = append(value, 0xff)
	oversizedKey := hashData(value)
	// Bypass the sender's guard to prove an untrusted STORE cannot evade
	// the server's guard. This payload still fits inside a UDP datagram.
	response, err := client.roundTrip(&peer, rpcMessage{Type: rpcStore, Key: oversizedKey.String(), Data: value})
	if err != nil || !strings.Contains(response.Error, ErrValueTooLarge.Error()) {
		t.Fatalf("incoming oversized STORE: %+v, error = %v", response, err)
	}
	if _, ok := node.localValue(oversizedKey); ok {
		t.Fatal("server retained oversized incoming value")
	}
	if _, err := client.roundTrip(&peer, rpcMessage{Type: rpcStore, Data: make([]byte, maxDatagram)}); err == nil {
		t.Fatal("oversized encoded datagram was accepted")
	}
}

func TestParallelLookupsOverLocalUDP(t *testing.T) {
	transport, node, _ := startTestNode(t, testID(0))
	defer transport.Close()
	value := []byte("value from a real local node")
	key := hashData(value)
	var target Contact
	for i := byte(1); i <= 4; i++ {
		peerTransport, peer, contact := startTestNode(t, testID(i))
		defer peerTransport.Close()
		node.routingTable.AddContact(contact)
		if i == 1 {
			if err := peer.StoreValue(key, value); err != nil {
				t.Fatal(err)
			}
		}
		target = contact
	}
	contacts, err := node.LookupContact(&target)
	if err != nil || len(contacts) != 4 || !contacts[0].ID.Equals(target.ID) {
		t.Fatalf("UDP contacts = %v, error = %v", contacts, err)
	}
	got, source, err := node.LookupData(key.String())
	if err != nil || source == nil || !bytes.Equal(got, value) {
		t.Fatalf("UDP lookup value = %q, source = %v, error = %v", got, source, err)
	}
}

package kademlia

import (
	"errors"
	"testing"
)

type fakeRPCClient struct {
	findContacts map[string][]Contact
	findData     map[string]FindDataResult
	requestError map[string]error
	contactCalls []string
	dataCalls    []string
	storeCalls   []storeCall
}

type storeCall struct {
	address string
	key     KademliaID
	data    []byte
}

func (fake *fakeRPCClient) SendFindContactMessage(contact *Contact, _ *KademliaID) ([]Contact, error) {
	fake.contactCalls = append(fake.contactCalls, contact.Address)
	if err := fake.requestError[contact.Address]; err != nil {
		return nil, err
	}
	return fake.findContacts[contact.Address], nil
}

func (fake *fakeRPCClient) SendFindDataMessage(contact *Contact, _ *KademliaID) (FindDataResult, error) {
	fake.dataCalls = append(fake.dataCalls, contact.Address)
	if err := fake.requestError[contact.Address]; err != nil {
		return FindDataResult{}, err
	}
	return fake.findData[contact.Address], nil
}

func (fake *fakeRPCClient) SendStoreMessage(contact *Contact, key *KademliaID, data []byte) error {
	fake.storeCalls = append(fake.storeCalls, storeCall{contact.Address, *key, cloneBytes(data)})
	return fake.requestError[contact.Address]
}

func TestLookupContactIterativelyDiscoversCloserNodes(t *testing.T) {
	me := NewContact(testID(0), "me")
	nodeA := NewContact(testID(8), "node-a")
	nodeB := NewContact(testID(2), "node-b")
	target := NewContact(testID(3), "")
	routingTable := NewRoutingTable(me, 2)
	routingTable.AddContact(nodeA)
	fake := &fakeRPCClient{
		findContacts: map[string][]Contact{"node-a": {nodeB}},
		findData:     make(map[string]FindDataResult),
		requestError: make(map[string]error),
	}

	contacts, err := NewKademlia(routingTable, fake).LookupContact(&target)
	if err != nil {
		t.Fatalf("LookupContact returned error: %v", err)
	}
	if len(fake.contactCalls) != 2 || fake.contactCalls[0] != "node-a" || fake.contactCalls[1] != "node-b" {
		t.Fatalf("query order = %v, want [node-a node-b]", fake.contactCalls)
	}
	if len(contacts) != 2 || !contacts[0].ID.Equals(nodeB.ID) || !contacts[1].ID.Equals(nodeA.ID) {
		t.Fatalf("result was not sorted by XOR distance: %v", contacts)
	}
}

func TestLookupContactContinuesAfterUnresponsiveNode(t *testing.T) {
	me := NewContact(testID(0), "me")
	failed := NewContact(testID(2), "failed")
	responsive := NewContact(testID(4), "responsive")
	target := NewContact(testID(3), "")
	routingTable := NewRoutingTable(me, 2)
	routingTable.AddContact(failed)
	routingTable.AddContact(responsive)
	fake := &fakeRPCClient{
		findContacts: make(map[string][]Contact),
		findData:     make(map[string]FindDataResult),
		requestError: map[string]error{"failed": errors.New("timeout")},
	}

	contacts, err := NewKademlia(routingTable, fake).LookupContact(&target)
	if err != nil {
		t.Fatalf("LookupContact returned error: %v", err)
	}
	if len(contacts) != 1 || contacts[0].Address != "responsive" {
		t.Fatalf("responsive contacts = %v, want only responsive", contacts)
	}
}

func TestLookupDataAcceptsOnlyValueMatchingHash(t *testing.T) {
	data := []byte("distributed systems")
	key := hashData(data)
	me := NewContact(testID(0), "me")
	node := NewContact(testID(1), "node")
	routingTable := NewRoutingTable(me)
	routingTable.AddContact(node)
	fake := &fakeRPCClient{
		findContacts: make(map[string][]Contact),
		findData: map[string]FindDataResult{
			"node": {Found: true, Value: data},
		},
		requestError: make(map[string]error),
	}
	kademlia := NewKademlia(routingTable, fake)

	got, source, err := kademlia.LookupData(key.String())
	if err != nil {
		t.Fatalf("LookupData returned error: %v", err)
	}
	if string(got) != string(data) || source == nil || source.Address != "node" {
		t.Fatalf("LookupData got %q from %v", got, source)
	}

	// The verified result is cached locally, so a second lookup sends no RPC.
	got, source, err = kademlia.LookupData(key.String())
	if err != nil || string(got) != string(data) || source != nil {
		t.Fatalf("local LookupData got %q from %v with error %v", got, source, err)
	}
	if len(fake.dataCalls) != 1 {
		t.Fatalf("FIND_VALUE calls = %d, want 1", len(fake.dataCalls))
	}
}

func TestLookupDataFollowsContactsUntilValueIsFound(t *testing.T) {
	data := []byte("found on the second node")
	key := hashData(data)
	me := NewContact(testID(0), "me")
	nodeA := NewContact(testID(8), "node-a")
	nodeB := NewContact(testID(2), "node-b")
	routingTable := NewRoutingTable(me, 2)
	routingTable.AddContact(nodeA)
	fake := &fakeRPCClient{
		findContacts: make(map[string][]Contact),
		findData: map[string]FindDataResult{
			"node-a": {Contacts: []Contact{nodeB}},
			"node-b": {Found: true, Value: data},
		},
		requestError: make(map[string]error),
	}

	got, source, err := NewKademlia(routingTable, fake).LookupData(key.String())
	if err != nil {
		t.Fatalf("LookupData returned error: %v", err)
	}
	if string(got) != string(data) || source == nil || source.Address != "node-b" {
		t.Fatalf("LookupData got %q from %v, want node-b", got, source)
	}
	if len(fake.dataCalls) != 2 || fake.dataCalls[0] != "node-a" || fake.dataCalls[1] != "node-b" {
		t.Fatalf("FIND_VALUE order = %v, want [node-a node-b]", fake.dataCalls)
	}
}

func TestLookupDataRejectsHashMismatch(t *testing.T) {
	expectedKey := hashData([]byte("expected"))
	me := NewContact(testID(0), "me")
	node := NewContact(testID(1), "node")
	routingTable := NewRoutingTable(me)
	routingTable.AddContact(node)
	fake := &fakeRPCClient{
		findContacts: make(map[string][]Contact),
		findData: map[string]FindDataResult{
			"node": {Found: true, Value: []byte("tampered")},
		},
		requestError: make(map[string]error),
	}

	_, _, err := NewKademlia(routingTable, fake).LookupData(expectedKey.String())
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("LookupData error = %v, want ErrHashMismatch", err)
	}
}

func TestStoreReplicatesToKClosestIncludingSelf(t *testing.T) {
	data := []byte("package bytes")
	key := hashData(data)
	meID := xorLastByte(key, 2)
	remoteID := xorLastByte(key, 1)
	farID := xorLastByte(key, 3)
	me := NewContact(meID, "me")
	remote := NewContact(remoteID, "remote")
	far := NewContact(farID, "far")
	routingTable := NewRoutingTable(me, 2)
	routingTable.AddContact(remote)
	routingTable.AddContact(far)
	fake := &fakeRPCClient{
		findContacts: make(map[string][]Contact),
		findData:     make(map[string]FindDataResult),
		requestError: make(map[string]error),
	}
	kademlia := NewKademlia(routingTable, fake)

	gotKey, err := kademlia.Store(data)
	if err != nil {
		t.Fatalf("Store returned error: %v", err)
	}
	if gotKey != key.String() {
		t.Fatalf("Store key = %s, want %s", gotKey, key)
	}
	if len(fake.storeCalls) != 1 || fake.storeCalls[0].address != "remote" {
		t.Fatalf("remote STORE calls = %v, want only remote", fake.storeCalls)
	}
	if !fake.storeCalls[0].key.Equals(key) || string(fake.storeCalls[0].data) != string(data) {
		t.Fatal("STORE RPC did not contain the expected content hash and data")
	}
	if local, ok := kademlia.localValue(key); !ok || string(local) != string(data) {
		t.Fatal("Store did not keep a local copy when self was among the k closest")
	}
}

func TestStoreValueRejectsMismatchedKey(t *testing.T) {
	kademlia := NewKademlia(nil, nil)
	if err := kademlia.StoreValue(hashData([]byte("one")), []byte("two")); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("StoreValue error = %v, want ErrHashMismatch", err)
	}
}

func TestLookupDataRejectsInvalidHash(t *testing.T) {
	kademlia := NewKademlia(NewRoutingTable(NewContact(testID(0), "me")), nil)
	if _, _, err := kademlia.LookupData("not-a-hash"); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("LookupData error = %v, want ErrInvalidHash", err)
	}
}

func xorLastByte(key *KademliaID, distance byte) *KademliaID {
	id := *key
	id[IDLength-1] ^= distance
	return &id
}

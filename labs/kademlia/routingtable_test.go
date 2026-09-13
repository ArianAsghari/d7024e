package kademlia

import "testing"

func TestRoutingTableUsesDefaultAndCustomK(t *testing.T) {
	me := NewContact(testID(0), "me")
	if got := NewRoutingTable(me).K(); got != DefaultK {
		t.Fatalf("default K = %d, want %d", got, DefaultK)
	}

	routingTable := NewRoutingTable(me, 2)
	for i := byte(0); i < 3; i++ {
		id := KademliaID{}
		id[0] = 0x80
		id[IDLength-1] = i
		routingTable.AddContact(NewContact(&id, "node"))
	}

	if got := routingTable.buckets[0].Len(); got != 2 {
		t.Fatalf("bucket length = %d, want configured capacity 2", got)
	}
}

func TestRoutingTableBucketIndex(t *testing.T) {
	routingTable := NewRoutingTable(NewContact(testID(0), "me"))

	mostSignificantBit := KademliaID{}
	mostSignificantBit[0] = 0x80
	if got := routingTable.getBucketIndex(&mostSignificantBit); got != 0 {
		t.Errorf("most-significant differing bit uses bucket %d, want 0", got)
	}

	leastSignificantBit := KademliaID{}
	leastSignificantBit[IDLength-1] = 0x01
	if got := routingTable.getBucketIndex(&leastSignificantBit); got != 255 {
		t.Errorf("least-significant differing bit uses bucket %d, want 255", got)
	}
}

func TestRoutingTableIgnoresSelfAndDuplicateContacts(t *testing.T) {
	me := NewContact(testID(0), "me")
	routingTable := NewRoutingTable(me)
	other := NewContact(testID(4), "other")

	routingTable.AddContact(me)
	routingTable.AddContact(other)
	routingTable.AddContact(other)

	contacts := routingTable.FindClosestContacts(testID(0), DefaultK)
	if len(contacts) != 1 {
		t.Fatalf("closest contact count = %d, want 1", len(contacts))
	}
	if !contacts[0].ID.Equals(other.ID) {
		t.Fatalf("closest contact = %s, want %s", contacts[0].ID, other.ID)
	}
}

func TestFindClosestContactsSortsByXORDistanceAndLimitsCount(t *testing.T) {
	routingTable := NewRoutingTable(NewContact(testID(0), "me"))
	for _, value := range []byte{8, 2, 5} {
		routingTable.AddContact(NewContact(testID(value), "node"))
	}

	contacts := routingTable.FindClosestContacts(testID(0), 2)
	if len(contacts) != 2 {
		t.Fatalf("closest contact count = %d, want 2", len(contacts))
	}
	if !contacts[0].ID.Equals(testID(2)) || !contacts[1].ID.Equals(testID(5)) {
		t.Fatalf("closest contacts = [%s, %s], want IDs ending in [02, 05]", contacts[0].ID, contacts[1].ID)
	}
	if got := routingTable.FindClosestContacts(testID(0), 0); len(got) != 0 {
		t.Fatalf("zero-count lookup returned %d contacts", len(got))
	}
}

func testID(lastByte byte) *KademliaID {
	id := KademliaID{}
	id[IDLength-1] = lastByte
	return &id
}

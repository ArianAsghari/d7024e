package kademlia

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// storeTestNode builds a node whose routing table holds peers at XOR distance
// 1..peers from key. The node itself is far from the key, so with k = 10 and
// at most nine peers all of them are replicas and this node is not.
func storeTestNode(t *testing.T, key *KademliaID, peers int, store func(*Contact, *KademliaID, []byte) error) (*Kademlia, []Contact) {
	t.Helper()
	routingTable := NewRoutingTable(NewContact(xorLastByte(key, 0x80), "me"))
	contacts := make([]Contact, peers)
	for i := range contacts {
		contacts[i] = NewContact(xorLastByte(key, byte(i+1)), fmt.Sprintf("peer-%d", i+1))
		routingTable.AddContact(contacts[i])
	}
	node := NewKademlia(routingTable, &maintenanceRPC{store: store})
	node.SetLookupLogger(nil)
	return node, contacts
}

// TestStoreAtSendsToAllReplicasAtTheSameTime proves the STOREs overlap without
// relying on timing: every handler blocks until all nine have started, so a
// sender that waited for each reply before the next STORE would never get
// there and the handlers would time out.
func TestStoreAtSendsToAllReplicasAtTheSameTime(t *testing.T) {
	const peers = 9
	data := []byte("pointer value")
	key := hashData(data)

	var arrived atomic.Int32
	allStarted := make(chan struct{})
	var once sync.Once
	node, _ := storeTestNode(t, key, peers, func(*Contact, *KademliaID, []byte) error {
		if arrived.Add(1) == peers {
			once.Do(func() { close(allStarted) })
		}
		select {
		case <-allStarted:
			return nil
		case <-time.After(3 * time.Second):
			return errors.New("STORE was not sent at the same time as the others")
		}
	})

	if err := node.StoreAt(key, data); err != nil {
		t.Fatalf("StoreAt = %v", err)
	}
	if got := arrived.Load(); got != peers {
		t.Fatalf("%d STORE RPCs sent, want %d", got, peers)
	}
}

// TestStoreAtWaitsOnlyForTheSlowestDestination checks the elapsed time with
// real delays: nine replicas that each take 100 ms, one after another, would
// need 900 ms.
func TestStoreAtWaitsOnlyForTheSlowestDestination(t *testing.T) {
	const peers, delay = 9, 100 * time.Millisecond
	data := []byte("pointer value")
	key := hashData(data)
	node, _ := storeTestNode(t, key, peers, func(*Contact, *KademliaID, []byte) error {
		time.Sleep(delay)
		return nil
	})

	start := time.Now()
	if err := node.StoreAt(key, data); err != nil {
		t.Fatalf("StoreAt = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*delay {
		t.Fatalf("StoreAt took %v for %d replicas of %v each; they were not sent together", elapsed, peers, delay)
	}
}

// TestStoreAtJoinsErrorsInDistanceOrder makes the closest replica reply last.
// The joined error must still list the destinations from the closest to the
// farthest, as the sequential version did.
func TestStoreAtJoinsErrorsInDistanceOrder(t *testing.T) {
	data := []byte("pointer value")
	key := hashData(data)
	node, _ := storeTestNode(t, key, 3, func(contact *Contact, _ *KademliaID, _ []byte) error {
		// peer-1 is the closest and is the slowest; peer-2 succeeds.
		switch contact.Address {
		case "peer-1":
			time.Sleep(80 * time.Millisecond)
			return errors.New("refused by peer-1")
		case "peer-3":
			return errors.New("timeout from peer-3")
		}
		return nil
	})

	err := node.StoreAt(key, data)
	if err == nil {
		t.Fatal("StoreAt hid the failed destinations")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "store on peer-1:") || !strings.HasPrefix(lines[1], "store on peer-3:") {
		t.Fatalf("errors are not in distance order: %q", lines)
	}
}

// TestStoreAtKeepsLocalCopyAndReportsOnlyFailedDestinations covers what
// StoreAt does besides sending: this node keeps its own copy when it is among
// the k closest, and the returned error names only the destinations whose
// STORE failed.
func TestStoreAtKeepsLocalCopyAndReportsOnlyFailedDestinations(t *testing.T) {
	data := []byte("pointer value")
	key := hashData(data)
	routingTable := NewRoutingTable(NewContact(xorLastByte(key, 1), "me"))
	routingTable.AddContact(NewContact(xorLastByte(key, 2), "good"))
	routingTable.AddContact(NewContact(xorLastByte(key, 3), "bad"))
	node := NewKademlia(routingTable, &maintenanceRPC{
		store: func(contact *Contact, _ *KademliaID, _ []byte) error {
			if contact.Address == "bad" {
				return errors.New("timeout")
			}
			return nil
		},
	})
	node.SetLookupLogger(nil)

	err := node.StoreAt(key, data)
	if err == nil || !strings.Contains(err.Error(), "store on bad:") || strings.Contains(err.Error(), "store on good:") {
		t.Fatalf("StoreAt error = %v", err)
	}
	if value, ok := node.localValue(key); !ok || string(value) != string(data) {
		t.Fatal("this node is among the k closest but did not keep its own copy")
	}
}

package kademlia

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Callbacks are configured before use. Tests that share callback state protect
// it with a mutex, atomics, or channels, just as a simulated transport must.
type maintenanceRPC struct {
	ping  func(*Contact) error
	find  func(*Contact, *KademliaID) ([]Contact, error)
	store func(*Contact, *KademliaID, []byte) error
}

func (rpc *maintenanceRPC) SendPingMessage(contact *Contact) (time.Duration, error) {
	if rpc.ping != nil {
		return time.Millisecond, rpc.ping(contact)
	}
	return time.Millisecond, nil
}

func (rpc *maintenanceRPC) SendFindContactMessage(contact *Contact, target *KademliaID) ([]Contact, error) {
	if rpc.find != nil {
		return rpc.find(contact, target)
	}
	return nil, nil
}

func (rpc *maintenanceRPC) SendFindDataMessage(*Contact, *KademliaID) (FindDataResult, error) {
	return FindDataResult{}, nil
}

func (rpc *maintenanceRPC) SendStoreMessage(contact *Contact, key *KademliaID, value []byte) error {
	if rpc.store != nil {
		return rpc.store(contact, key, value)
	}
	return nil
}

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not complete; possible lock held across network I/O")
	}
}

func TestFullBucketPingsLeastRecentlySeen(t *testing.T) {
	for _, alive := range []bool{true, false} {
		name := "dead contact replaced"
		if alive {
			name = "live contact retained and refreshed"
		}
		t.Run(name, func(t *testing.T) {
			rt := NewRoutingTable(NewContact(testID(0), "me"), 2)
			var pinged []string
			NewKademlia(rt, &maintenanceRPC{ping: func(contact *Contact) error {
				pinged = append(pinged, contact.Address)
				if !alive {
					return errors.New("timeout")
				}
				return nil
			}})
			rt.AddContact(NewContact(testID(8), "oldest"))
			rt.AddContact(NewContact(testID(9), "recent"))
			rt.AddContact(NewContact(testID(10), "new"))
			if len(pinged) != 1 || pinged[0] != "oldest" {
				t.Fatalf("PING calls = %v, want [oldest]", pinged)
			}
			contacts := rt.buckets[252].GetContactAndCalcDistance(testID(0))
			first := "new"
			if alive {
				first = "oldest"
			}
			if len(contacts) != 2 || contacts[0].Address != first || contacts[1].Address != "recent" {
				t.Fatalf("bucket order = %v, want [%s recent]", contacts, first)
			}
		})
	}
}

func TestEvictionRechecksContactAfterConcurrentActivity(t *testing.T) {
	for _, refreshOldest := range []bool{true, false} {
		t.Run(map[bool]string{true: "oldest heard from", false: "unrelated contact heard from"}[refreshOldest], func(t *testing.T) {
			rt := NewRoutingTable(NewContact(testID(0), "me"), 2)
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var probes atomic.Int32
			NewKademlia(rt, &maintenanceRPC{ping: func(*Contact) error {
				if probes.Add(1) == 1 {
					close(started)
				}
				<-release
				return errors.New("timeout")
			}})
			rt.AddContact(NewContact(testID(8), "oldest"))
			rt.AddContact(NewContact(testID(9), "recent"))
			go func() {
				defer close(done)
				rt.AddContact(NewContact(testID(10), "new"))
			}()
			waitFor(t, started)
			progress := make(chan struct{})
			go func() {
				defer close(progress)
				id := byte(9)
				if refreshOldest {
					id = 8
				}
				rt.AddContact(NewContact(testID(id), "updated-address"))
				// A concurrent new arrival must not start another probe.
				rt.AddContact(NewContact(testID(11), "another-new"))
				_ = rt.FindClosestContacts(testID(0), 2)
				_ = rt.Buckets()
			}()
			waitFor(t, progress)
			close(release)
			waitFor(t, done)
			if probes.Load() != 1 {
				t.Fatalf("concurrent probes = %d, want 1", probes.Load())
			}
			contacts := rt.FindClosestContacts(testID(0), 2)
			if len(contacts) != 2 {
				t.Fatalf("bucket length = %d, want 2", len(contacts))
			}
			if refreshOldest && (!contacts[0].ID.Equals(testID(8)) || contacts[0].Address != "updated-address") {
				t.Fatal("timeout evicted a contact heard from during the probe")
			}
			if !refreshOldest && !contacts[1].ID.Equals(testID(10)) {
				t.Fatal("unrelated activity prevented replacing the unresponsive contact")
			}
		})
	}
}

func TestRefreshTargetsCoverTheirExactBuckets(t *testing.T) {
	me := hashData([]byte("refresh target test"))
	rt := NewRoutingTable(NewContact(me, "me"))
	for index := 0; index < IDLength*8; index++ {
		target, err := refreshTarget(me, index)
		if err != nil || rt.getBucketIndex(target) != index {
			t.Fatalf("refresh target for bucket %d: %v, %v", index, target, err)
		}
	}
	for _, index := range []int{-1, 256} {
		if _, err := refreshTarget(me, index); !errors.Is(err, ErrInvalidTarget) {
			t.Fatalf("invalid index %d: %v", index, err)
		}
	}
	if _, err := refreshTarget(nil, 0); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("nil ID: %v", err)
	}
}

func TestLookupReferralDoesNotRefreshUnresponsiveContact(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"), 2)
	oldest := NewContact(testID(8), "oldest")
	recent := NewContact(testID(9), "recent")
	rt.AddContact(oldest)
	rt.AddContact(recent)
	var pinged string
	node := NewKademlia(rt, &maintenanceRPC{
		find: func(contact *Contact, _ *KademliaID) ([]Contact, error) {
			if contact.Address == "oldest" {
				return nil, errors.New("offline")
			}
			return []Contact{oldest}, nil
		},
		ping: func(contact *Contact) error {
			pinged = contact.Address
			return errors.New("offline")
		},
	})
	if _, err := node.LookupContact(&recent); err != nil {
		t.Fatal(err)
	}
	rt.AddContact(NewContact(testID(10), "new"))
	if pinged != "oldest" {
		t.Fatalf("referral refreshed an offline peer: pinged %q instead of oldest", pinged)
	}
}

func TestJoinLooksUpSelfThenRefreshesFartherBuckets(t *testing.T) {
	me := NewContact(testID(0), "me")
	bootstrapID, neighborID := KademliaID{}, KademliaID{}
	bootstrapID[0], neighborID[0] = 0x10, 0x04 // bucket 3 and bucket 5
	bootstrap := NewContact(&bootstrapID, "bootstrap")
	neighbor := NewContact(&neighborID, "neighbor")
	var targets []KademliaID
	var events []string
	rpc := &maintenanceRPC{
		ping: func(contact *Contact) error {
			events = append(events, "ping "+contact.Address)
			return nil
		},
		find: func(contact *Contact, target *KademliaID) ([]Contact, error) {
			events = append(events, "lookup")
			if len(targets) == 0 || !targets[len(targets)-1].Equals(target) {
				targets = append(targets, *target)
			}
			return []Contact{neighbor}, nil
		},
	}
	rt := NewRoutingTable(me)
	if err := NewKademlia(rt, rpc).Join(bootstrap); err != nil {
		t.Fatal(err)
	}
	if events[0] != "ping bootstrap" || len(targets) != 6 || !targets[0].Equals(me.ID) {
		t.Fatalf("join sequence: events %v, targets %v", events, targets)
	}
	for index, target := range targets[1:] {
		if got := rt.getBucketIndex(&target); got != index {
			t.Fatalf("refresh #%d targets bucket %d", index, got)
		}
	}
	if got := rt.FindClosestContacts(me.ID, 10); len(got) != 2 || !got[0].ID.Equals(neighbor.ID) {
		t.Fatalf("populated routing table: %v", got)
	}
}

func TestJoinFailures(t *testing.T) {
	me := NewContact(testID(0), "me")
	bootstrapID := KademliaID{}
	bootstrapID[0] = 0x40 // one farther bucket needs refreshing
	bootstrap := NewContact(&bootstrapID, "bootstrap")
	for _, tc := range []struct {
		name string
		node *Kademlia
		seed Contact
	}{
		{"no table", NewKademlia(nil, nil), bootstrap},
		{"no network", NewKademlia(NewRoutingTable(me), nil), bootstrap},
		{"missing ID", NewKademlia(NewRoutingTable(me), &maintenanceRPC{}), Contact{}},
		{"self", NewKademlia(NewRoutingTable(me), &maintenanceRPC{}), me},
		{"unreachable bootstrap", NewKademlia(NewRoutingTable(me), &maintenanceRPC{ping: func(*Contact) error { return errors.New("offline") }}), bootstrap},
		{"failed self lookup", NewKademlia(NewRoutingTable(me), &maintenanceRPC{find: func(*Contact, *KademliaID) ([]Contact, error) { return nil, errors.New("offline") }}), bootstrap},
		{"failed refresh", NewKademlia(NewRoutingTable(me), &maintenanceRPC{find: func(_ *Contact, target *KademliaID) ([]Contact, error) {
			if target.Equals(me.ID) {
				return nil, nil
			}
			return nil, errors.New("offline")
		}}), bootstrap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.node.Join(tc.seed); err == nil {
				t.Fatal("expected join error")
			}
		})
	}
}

func TestRepublishRestoresReplicaAfterChurnAndKeepsLocalValue(t *testing.T) {
	value := []byte("must survive a replica leaving")
	key := hashData(value)
	me := NewContact(xorLastByte(key, 2), "me")
	departed := NewContact(xorLastByte(key, 1), "departed")
	replacement := NewContact(key, "replacement")
	remote := NewKademlia(NewRoutingTable(replacement), nil)
	rt := NewRoutingTable(me, 2)
	rt.AddContact(departed)
	rt.AddContact(replacement)
	node := NewKademlia(rt, &maintenanceRPC{
		find: func(contact *Contact, _ *KademliaID) ([]Contact, error) {
			if contact.Address == "departed" {
				return nil, errors.New("offline")
			}
			return nil, nil
		},
		store: func(contact *Contact, key *KademliaID, data []byte) error {
			if contact.Address != "replacement" {
				t.Errorf("attempted store on %s", contact.Address)
			}
			return remote.StoreValue(key, data)
		},
	})
	if err := node.StoreValue(key, value); err != nil {
		t.Fatal(err)
	}
	if err := node.Republish(); err != nil {
		t.Fatal(err)
	}
	for _, instance := range []*Kademlia{node, remote} {
		if got, ok := instance.localValue(key); !ok || string(got) != string(value) {
			t.Fatal("republishing did not retain and restore the value")
		}
	}
}

func TestReplicationReleasesDataLockAndContinuesAfterFailure(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	rt.AddContact(NewContact(testID(1), "peer"))
	var node *Kademlia
	calls := 0
	node = NewKademlia(rt, &maintenanceRPC{store: func(*Contact, *KademliaID, []byte) error {
		calls++
		// Simulate an incoming STORE while our replication RPC is in flight.
		_ = node.StoreValue(hashData([]byte("new")), []byte("new"))
		_ = node.Entries()
		return errors.New("remote store failed")
	}})
	for _, value := range []string{"first", "second"} {
		_ = node.StoreValue(hashData([]byte(value)), []byte(value))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := node.Republish(); err == nil {
			t.Error("replication failures were not returned")
		}
	}()
	waitFor(t, done)
	if calls != 2 || len(node.Entries()) != 3 {
		t.Fatalf("snapshot replication made %d calls and retained %d values", calls, len(node.Entries()))
	}
}

func TestPeriodicReplicationStopsOnCancellation(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	rt.AddContact(NewContact(testID(1), "peer"))
	stored := make(chan struct{}, 10)
	node := NewKademlia(rt, &maintenanceRPC{store: func(*Contact, *KademliaID, []byte) error {
		select {
		case stored <- struct{}{}:
		default:
		}
		return nil
	}})
	value := []byte("periodic value")
	_ = node.StoreValue(hashData(value), value)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := node.RunRepublisher(ctx, 2*time.Millisecond); err != nil {
			t.Error(err)
		}
	}()
	waitFor(t, stored)
	waitFor(t, stored) // More than one pass; this is a periodic worker.
	cancel()
	waitFor(t, done)
	if _, ok := node.localValue(hashData(value)); !ok {
		t.Fatal("stopping replication expired a local value")
	}
	if err := node.republish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pass: %v", err)
	}
	if err := node.RunRepublisher(context.Background(), 0); err == nil {
		t.Fatal("zero interval accepted")
	}
}

func TestConcurrentRoutingAndDataAccess(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"), 2)
	node := NewKademlia(rt, &maintenanceRPC{})
	var workers sync.WaitGroup
	for worker := 1; worker <= 12; worker++ {
		workers.Add(1)
		go func(worker byte) {
			defer workers.Done()
			for i := 0; i < 30; i++ {
				value := []byte{worker, byte(i)}
				key := hashData(value)
				if err := node.StoreValue(key, value); err != nil {
					t.Error(err)
				}
				value[0] = 0 // Stored bytes must not alias caller memory.
				got, ok := node.localValue(key)
				if !ok || got[0] != worker {
					t.Error("stored value was corrupted")
				}
				rt.AddContact(NewContact(testID(worker), "peer"))
				_ = rt.FindClosestContacts(key, 2)
				_ = rt.Buckets()
				entries := node.Entries()
				if len(entries) > 0 {
					entries[0].Value[0] = 0 // Snapshots must also own their bytes.
				}
			}
		}(byte(worker))
	}
	workers.Wait()
	if len(node.Entries()) != 360 {
		t.Fatalf("lost concurrent writes: got %d entries", len(node.Entries()))
	}
}

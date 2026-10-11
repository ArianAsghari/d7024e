package kademlia

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestValueValidatorAcceptsUpdatesAndPreservesRejectedValue(t *testing.T) {
	node := NewKademlia(NewRoutingTable(NewContact(testID(0), "me")), nil)
	node.SetLookupLogger(nil)
	key := hashData([]byte("example.com:package:latest"))
	rejected := errors.New("invalid update")
	var currents []string
	node.SetValueValidator(func(gotKey *KademliaID, current, incoming []byte) error {
		if !gotKey.Equals(key) {
			t.Errorf("validator key = %s, want %s", gotKey, key)
		}
		currents = append(currents, string(current))
		if len(incoming) != 1 || incoming[0] < '1' || incoming[0] > '3' || bytes.Compare(incoming, current) < 0 {
			return rejected
		}
		return nil
	})
	for _, value := range []string{"1", "3", "3"} {
		if err := node.StoreValue(key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"2", "invalid"} {
		if err := node.StoreValue(key, []byte(value)); !errors.Is(err, rejected) {
			t.Fatalf("rejected update = %v, want %v", err, rejected)
		}
	}
	if got, source, err := node.LookupData(key.String()); err != nil || string(got) != "3" || source != nil {
		t.Fatalf("local mutable lookup = %q, %v, %v", got, source, err)
	}
	if want := []string{"", "1", "3", "3", "3"}; len(currents) != len(want) {
		t.Fatalf("current values = %v, want %v", currents, want)
	} else {
		for i := range want {
			if currents[i] != want[i] {
				t.Fatalf("current values = %v, want %v", currents, want)
			}
		}
	}
	node.SetValueValidator(nil)
	if err := node.StoreValue(key, []byte("4")); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("disabled validator = %v", err)
	}
	if got, ok := node.localValue(key); !ok || string(got) != "3" {
		t.Fatal("disabling validation lost the accepted value")
	}
}

func TestValueValidatorCannotBypassBasicGuards(t *testing.T) {
	node := NewKademlia(nil, nil)
	node.SetValueValidator(func(*KademliaID, []byte, []byte) error {
		t.Error("validator called for a blob, invalid key, or oversized value")
		return nil
	})
	blob := []byte("immutable blob")
	if err := node.StoreValue(hashData(blob), blob); err != nil {
		t.Fatal(err)
	}
	if err := node.StoreValue(nil, blob); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("nil key = %v", err)
	}
	if err := node.StoreValue(testID(1), make([]byte, MaxValueSize+1)); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("oversized mutable value = %v", err)
	}
	if len(node.Entries()) != 1 {
		t.Fatal("invalid values were stored")
	}
}

func TestContentAddressedStoreCannotOverwriteMutablePointer(t *testing.T) {
	node := NewKademlia(nil, nil)
	preimage := []byte("example.com:package:latest")
	key := hashData(preimage)
	node.SetValueValidator(func(*KademliaID, []byte, []byte) error { return nil })
	if err := node.StoreValue(key, []byte("accepted pointer")); err != nil {
		t.Fatal(err)
	}
	for _, disableValidator := range []bool{false, true} {
		if disableValidator {
			node.SetValueValidator(nil)
		}
		if err := node.StoreValue(key, preimage); !errors.Is(err, ErrValueConflict) {
			t.Fatalf("blob overwrite = %v, want ErrValueConflict", err)
		}
		if got, ok := node.localValue(key); !ok || string(got) != "accepted pointer" {
			t.Fatalf("blob overwrote accepted pointer: %q", got)
		}
	}
}

func TestValueValidatorReceivesIndependentCopies(t *testing.T) {
	node := NewKademlia(nil, nil)
	key := hashData([]byte("mutable key"))
	originalKey := *key
	incoming := []byte("first")
	rejected := errors.New("rejected")
	calls := 0
	var callbackKey *KademliaID
	var callbackValue []byte
	node.SetValueValidator(func(key *KademliaID, current, value []byte) error {
		calls++
		callbackKey, callbackValue = key, value
		key[0] ^= 0xff
		value[0] = '!'
		if current != nil {
			current[0] = '!'
		}
		if calls == 2 {
			return rejected
		}
		return nil
	})
	if err := node.StoreValue(key, incoming); err != nil {
		t.Fatal(err)
	}
	callbackKey[0] ^= 0xff
	callbackValue[0] = '?'
	if !key.Equals(&originalKey) || string(incoming) != "first" {
		t.Fatal("validator mutated caller arguments")
	}
	incoming[0] = '?'
	if err := node.StoreValue(key, []byte("second")); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	got, ok := node.localValue(key)
	if !ok || string(got) != "first" {
		t.Fatalf("stored value was mutated: %q", got)
	}
	got[0] = '?'
	entries := node.Entries()
	entries[0].Value[0] = '?'
	if got, _ := node.localValue(key); string(got) != "first" {
		t.Fatal("returned data aliases stored bytes")
	}
}

func TestValueValidatorRechecksConcurrentUpdates(t *testing.T) {
	for _, initial := range []string{"", "existing"} {
		t.Run("initial="+initial, func(t *testing.T) {
			node := NewKademlia(nil, nil)
			key := hashData([]byte("mutable key"))
			node.SetValueValidator(func(*KademliaID, []byte, []byte) error { return nil })
			if initial != "" {
				if err := node.StoreValue(key, []byte(initial)); err != nil {
					t.Fatal(err)
				}
			}
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			conflict := errors.New("fork or rollback")
			var calls atomic.Int32
			node.SetValueValidator(func(_ *KademliaID, current, incoming []byte) error {
				calls.Add(1)
				if string(current) != initial {
					return conflict
				}
				if string(incoming) == "slow update" {
					close(started)
					<-release
				}
				return nil
			})
			slowDone := make(chan struct{})
			var slowErr error
			go func() {
				defer close(slowDone)
				slowErr = node.StoreValue(key, []byte("slow update"))
			}()
			waitFor(t, started)
			fastDone := make(chan struct{})
			go func() {
				defer close(fastDone)
				if err := node.StoreValue(key, []byte("winning update")); err != nil {
					t.Error(err)
				}
			}()
			waitFor(t, fastDone)
			release <- struct{}{}
			waitFor(t, slowDone)
			if !errors.Is(slowErr, conflict) || calls.Load() != 3 {
				t.Fatalf("concurrent update = %v after %d validations", slowErr, calls.Load())
			}
			if got, ok := node.localValue(key); !ok || string(got) != "winning update" {
				t.Fatalf("concurrent update overwrote winner: %q", got)
			}
		})
	}
}

func TestLookupDataValidatesMutableRepliesWithoutCaching(t *testing.T) {
	for _, validPeer := range []bool{false, true} {
		t.Run(map[bool]string{false: "all rejected", true: "continue to valid peer"}[validPeer], func(t *testing.T) {
			key := hashData([]byte("mutable key"))
			rt := NewRoutingTable(NewContact(xorLastByte(key, 3), "me"))
			rt.AddContact(NewContact(xorLastByte(key, 1), "invalid"))
			if validPeer {
				rt.AddContact(NewContact(xorLastByte(key, 2), "valid"))
			}
			rpc := &fakeRPCClient{findData: map[string]FindDataResult{
				"invalid": {Found: true, Value: []byte("invalid")},
				"valid":   {Found: true, Value: []byte("accepted")},
			}}
			node := NewKademlia(rt, rpc)
			node.SetLookupLogger(nil)
			if err := node.SetAlpha(1); err != nil {
				t.Fatal(err)
			}
			rejected := errors.New("invalid pointer")
			node.SetValueValidator(func(_ *KademliaID, current, incoming []byte) error {
				if current != nil {
					t.Error("invalid reply was cached")
				}
				if string(incoming) != "accepted" {
					return rejected
				}
				return nil
			})
			got, source, err := node.LookupData(key.String())
			if !validPeer {
				if !errors.Is(err, rejected) || len(node.Entries()) != 0 {
					t.Fatalf("rejected lookup = %q, %v", got, err)
				}
				return
			}
			if err != nil || string(got) != "accepted" || source == nil || source.Address != "valid" {
				t.Fatalf("mutable lookup = %q, %v, %v", got, source, err)
			}
			got, source, err = node.LookupData(key.String())
			if err != nil || string(got) != "accepted" || source == nil || len(rpc.dataCalls) != 4 {
				t.Fatalf("repeat mutable lookup = %q, %v, %v", got, source, err)
			}
			if len(node.Entries()) != 0 {
				t.Fatal("mutable lookup created a read cache")
			}
		})
	}
}

func TestMutableReadIsNotServedOrRepublishedAsAReplica(t *testing.T) {
	_, reader, replica := simPair(t, SimConfig{})
	key := hashData([]byte("example.com:package:latest"))
	validate := func(*KademliaID, []byte, []byte) error { return nil }
	reader.SetValueValidator(validate)
	replica.SetValueValidator(validate)
	reader.routingTable.AddContact(*contactB())
	if err := replica.StoreValue(key, []byte("first pointer")); err != nil {
		t.Fatal(err)
	}
	if got, _, err := reader.LookupData(key.String()); err != nil || string(got) != "first pointer" {
		t.Fatalf("initial read = %q, %v", got, err)
	}
	// FIND_VALUE on the reader must refer other clients onward, not return
	// the old pointer. Republish must also leave that pointer on its replica.
	readerContact := reader.routingTable.Me()
	result, err := replica.network.SendFindDataMessage(&readerContact, key)
	if err != nil || result.Found || len(result.Contacts) == 0 {
		t.Fatalf("reader FIND_VALUE = %+v, %v", result, err)
	}
	if err := reader.Republish(); err != nil || len(reader.Entries()) != 0 {
		t.Fatalf("reader replication retained a pointer: %v", err)
	}
	if err := replica.StoreValue(key, []byte("second pointer")); err != nil {
		t.Fatal(err)
	}
	if got, _, err := reader.LookupData(key.String()); err != nil || string(got) != "second pointer" {
		t.Fatalf("updated read = %q, %v", got, err)
	}
}

func TestStoreAtAndRepublishPreserveMutableKey(t *testing.T) {
	for _, localReplica := range []bool{false, true} {
		t.Run(map[bool]string{false: "remote replicas", true: "including self"}[localReplica], func(t *testing.T) {
			key := hashData([]byte("example.com:package:latest"))
			meDistance := byte(3)
			if localReplica {
				meDistance = 0
			}
			rt := NewRoutingTable(NewContact(xorLastByte(key, meDistance), "me"), 2)
			rt.AddContact(NewContact(xorLastByte(key, 1), "near"))
			rt.AddContact(NewContact(xorLastByte(key, 2), "far"))
			rpc := &fakeRPCClient{}
			node := NewKademlia(rt, rpc)
			node.SetLookupLogger(nil)
			node.SetValueValidator(func(*KademliaID, []byte, []byte) error { return nil })
			value := []byte("latest pointer")
			if err := node.StoreAt(key, value); err != nil {
				t.Fatal(err)
			}
			if _, ok := node.localValue(key); ok != localReplica {
				t.Fatalf("local replica = %v, want %v", ok, localReplica)
			}
			wantCalls := 2
			if localReplica {
				wantCalls = 1
			}
			if len(rpc.storeCalls) != wantCalls {
				t.Fatalf("STORE calls = %d, want %d", len(rpc.storeCalls), wantCalls)
			}
			// Republish must preserve a held pointer even after the closest
			// nodes have changed and this node is no longer among them.
			if err := node.StoreValue(key, value); err != nil {
				t.Fatal(err)
			}
			if err := node.Republish(); err != nil {
				t.Fatal(err)
			}
			if len(rpc.storeCalls) != 2*wantCalls {
				t.Fatalf("replication STORE calls = %d", len(rpc.storeCalls))
			}
			for _, call := range rpc.storeCalls {
				if !call.key.Equals(key) || !bytes.Equal(call.data, value) || (localReplica && call.address != "near") {
					t.Fatalf("wrong mutable replica: %+v", call)
				}
			}
			if got, ok := node.localValue(key); !ok || !bytes.Equal(got, value) || len(node.Entries()) != 1 {
				t.Fatal("replication lost the pointer or created a copy under its value hash")
			}
		})
	}
}

func TestStoreAtReturnsValidationErrorsFromDestinations(t *testing.T) {
	key := hashData([]byte("mutable key"))
	rt := NewRoutingTable(NewContact(key, "me"))
	rt.AddContact(NewContact(xorLastByte(key, 1), "peer"))
	rejected := errors.New("rejected by peer")
	rpc := &maintenanceRPC{store: func(*Contact, *KademliaID, []byte) error { return rejected }}
	node := NewKademlia(rt, rpc)
	node.SetLookupLogger(nil)
	if err := node.StoreAt(key, []byte("pointer")); !errors.Is(err, rejected) || !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("destination errors = %v", err)
	}
	if len(node.Entries()) != 0 {
		t.Fatal("rejected pointer stored locally")
	}
}

func TestNetworkMutableStoreCatchesUpThroughLookupData(t *testing.T) {
	for _, missingRecord := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete history", true: "missing history"}[missingRecord], func(t *testing.T) {
			providerTransport, provider, providerContact := startTestNode(t, testID(1))
			defer providerTransport.Close()
			serverTransport, server, serverContact := startTestNode(t, testID(2))
			defer serverTransport.Close()
			provider.SetLookupLogger(nil)
			server.SetLookupLogger(nil)
			server.routingTable.AddContact(providerContact)
			key := hashData([]byte("example.com:package:latest"))
			// Opaque records are only predecessor hashes in this core test.
			// Encoding, signatures, and chain policy belong to the registry.
			firstHash := hashData([]byte("first version"))
			second := []byte(firstHash.String())
			secondHash := hashData(second)
			third := []byte(secondHash.String())
			thirdHash := hashData(third)
			if !missingRecord {
				if err := provider.StoreValue(secondHash, second); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.StoreValue(thirdHash, third); err != nil {
				t.Fatal(err)
			}
			server.SetValueValidator(func(*KademliaID, []byte, []byte) error { return nil })
			if err := server.StoreValue(key, []byte(firstHash.String())); err != nil {
				t.Fatal(err)
			}
			rejected := errors.New("invalid pointer update")
			server.SetValueValidator(func(gotKey *KademliaID, current, incoming []byte) error {
				if !gotKey.Equals(key) || string(current) != firstHash.String() || string(incoming) != thirdHash.String() {
					return rejected
				}
				// The validator can fetch and cache more than one missing
				// immutable record before it accepts the new latest pointer.
				previous, _, err := server.LookupData(string(incoming))
				if err != nil {
					return err
				}
				previous, _, err = server.LookupData(string(previous))
				if err != nil {
					return err
				}
				if !bytes.Equal(previous, current) {
					return rejected
				}
				return nil
			})
			client := NewNetwork(Contact{})
			client.Timeout = time.Second
			client.Retries = 0
			err := client.SendStoreMessage(&serverContact, key, []byte(thirdHash.String()))
			want := thirdHash.String()
			if missingRecord {
				want = firstHash.String()
				if err == nil || err.Error() != ErrValueNotFound.Error() {
					t.Fatalf("missing history STORE = %v", err)
				}
			} else if err != nil {
				t.Fatalf("catch-up STORE = %v", err)
			}
			// A subsequent rejected STORE cannot damage the last accepted head.
			if err := client.SendStoreMessage(&serverContact, key, []byte("invalid")); err == nil || err.Error() != rejected.Error() {
				t.Fatalf("invalid STORE = %v", err)
			}
			result, err := client.SendFindDataMessage(&serverContact, key)
			if err != nil || !result.Found || string(result.Value) != want {
				t.Fatalf("mutable FIND_VALUE = %+v, %v; want %s", result, err, want)
			}
			if got, ok := server.localValue(thirdHash); !ok || !bytes.Equal(got, third) {
				t.Fatal("missing record lookup did not cache the fetched record")
			}
			if _, ok := server.localValue(secondHash); ok == missingRecord {
				t.Fatal("incorrect cache result for the second version record")
			}
		})
	}
}

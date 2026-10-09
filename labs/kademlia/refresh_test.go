package kademlia

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func ageBucket(rt *RoutingTable, index int) {
	bucket := rt.buckets[index]
	bucket.mu.Lock()
	bucket.lastLookup = time.Now().Add(-2 * time.Hour)
	bucket.mu.Unlock()
}

func TestRefreshIdleBucketsSkipsActiveRangesAndDiscoversPeers(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	peer := NewContact(testID(1), "peer")
	rt.AddContact(peer)
	farID := KademliaID{0x80}
	far := NewContact(&farID, "new-far-peer")
	var calls atomic.Int32
	node := NewKademlia(rt, &maintenanceRPC{find: func(_ *Contact, target *KademliaID) ([]Contact, error) {
		calls.Add(1)
		if rt.getBucketIndex(target) == 0 {
			return []Contact{far}, nil
		}
		return nil, nil
	}})
	node.SetLookupLogger(nil)
	ageBucket(rt, 0) // Empty buckets must still be refreshed.
	ageBucket(rt, 255)
	if _, err := node.LookupContact(&peer); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	if err := node.RefreshIdleBuckets(time.Hour); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || rt.buckets[0].Len() != 1 {
		t.Fatalf("calls = %d, newly populated bucket length = %d", calls.Load(), rt.buckets[0].Len())
	}
	if err := node.RefreshIdleBuckets(time.Hour); err != nil || calls.Load() != 2 {
		t.Fatalf("fresh buckets were re-queried: calls = %d, error = %v", calls.Load(), err)
	}
}

func TestValueLookupTouchesItsTargetRange(t *testing.T) {
	value := []byte("remote refresh activity")
	key := hashData(value)
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	rt.AddContact(NewContact(testID(1), "peer"))
	ageBucket(rt, rt.getBucketIndex(key))
	var refreshCalls atomic.Int32
	node := NewKademlia(rt, &maintenanceRPC{
		data: func(*Contact, *KademliaID) (FindDataResult, error) {
			return FindDataResult{Found: true, Value: value}, nil
		},
		find: func(*Contact, *KademliaID) ([]Contact, error) {
			refreshCalls.Add(1)
			return nil, nil
		},
	})
	if _, _, err := node.LookupData(key.String()); err != nil {
		t.Fatal(err)
	}
	if err := node.RefreshIdleBuckets(time.Hour); err != nil || refreshCalls.Load() != 0 {
		t.Fatalf("value lookup did not postpone refresh: calls = %d, error = %v", refreshCalls.Load(), err)
	}
}

func TestRefreshContinuesAfterFailureAndLimitsRetries(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	rt.AddContact(NewContact(testID(1), "offline"))
	ageBucket(rt, 0)
	ageBucket(rt, 1)
	var calls atomic.Int32
	node := NewKademlia(rt, &maintenanceRPC{find: func(*Contact, *KademliaID) ([]Contact, error) {
		calls.Add(1)
		return nil, errors.New("offline")
	}})
	if err := node.RefreshIdleBuckets(time.Hour); !errors.Is(err, ErrLookupFailed) || calls.Load() != 2 {
		t.Fatalf("failed pass: calls = %d, error = %v", calls.Load(), err)
	}
	if err := node.RefreshIdleBuckets(time.Hour); err != nil || calls.Load() != 2 {
		t.Fatalf("failed buckets retried before interval: calls = %d, error = %v", calls.Load(), err)
	}
}

func TestBucketRefreshWorkerRepeatsAndCancelsBetweenBuckets(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	rt.AddContact(NewContact(testID(1), "peer"))
	called := make(chan struct{}, 1024)
	node := NewKademlia(rt, &maintenanceRPC{find: func(*Contact, *KademliaID) ([]Contact, error) {
		called <- struct{}{}
		return nil, nil
	}})
	node.SetLookupLogger(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := node.RunBucketRefresher(ctx, time.Millisecond); err != nil {
			t.Error(err)
		}
	}()
	// A second sweep must happen, beyond the first 256 bucket lookups.
	for i := 0; i <= IDLength*8; i++ {
		waitFor(t, called)
	}
	cancel()
	waitFor(t, done)
	if err := node.refreshIdleBuckets(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pass: %v", err)
	}

	ageBucket(rt, 0)
	ageBucket(rt, 1)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	node = NewKademlia(rt, &maintenanceRPC{find: func(*Contact, *KademliaID) ([]Contact, error) {
		calls.Add(1)
		cancel()
		return nil, nil
	}})
	if err := node.refreshIdleBuckets(ctx, time.Hour); !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancellation between buckets: calls = %d, error = %v", calls.Load(), err)
	}
}

func TestRefreshConfigurationAndIsolatedNode(t *testing.T) {
	for _, tc := range []struct {
		node *Kademlia
		want error
	}{
		{NewKademlia(nil, nil), ErrNoRoutingTable},
		{NewKademlia(NewRoutingTable(NewContact(testID(0), "me")), nil), ErrNoNetwork},
		{NewKademlia(NewRoutingTable(NewContact(testID(0), "me")), &maintenanceRPC{}), nil},
	} {
		if err := tc.node.RefreshIdleBuckets(time.Hour); !errors.Is(err, tc.want) {
			t.Fatalf("refresh error = %v, want %v", err, tc.want)
		}
		if err := tc.node.RefreshIdleBuckets(0); err == nil {
			t.Fatal("invalid idle duration accepted")
		}
		if err := tc.node.RunBucketRefresher(context.Background(), -time.Second); err == nil {
			t.Fatal("invalid ticker interval accepted")
		}
	}
}

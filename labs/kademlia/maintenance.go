package kademlia

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"time"
)

const DefaultReplicationInterval = time.Hour

// Join introduces this node to an existing network, discovers its closest
// neighbors, then refreshes every bucket farther away than its closest neighbor.
// The transport must already be listening so other nodes can contact us.
func (kademlia *Kademlia) Join(bootstrap Contact) error {
	if kademlia.routingTable == nil || kademlia.routingTable.me.ID == nil {
		return ErrNoRoutingTable
	}
	if bootstrap.ID == nil || bootstrap.Address == "" || bootstrap.ID.Equals(kademlia.routingTable.me.ID) {
		return errors.New("bootstrap must identify a different node with an address")
	}
	if kademlia.network == nil {
		return ErrNoNetwork
	}
	if _, err := kademlia.network.SendPingMessage(&bootstrap); err != nil {
		return fmt.Errorf("ping bootstrap: %w", err)
	}
	kademlia.routingTable.AddContact(bootstrap)

	me := kademlia.routingTable.me
	neighbors, err := kademlia.LookupContact(&me)
	if err != nil {
		return fmt.Errorf("join self lookup: %w", err)
	}
	if len(neighbors) == 0 {
		return ErrLookupFailed
	}

	// Index 0 covers the farthest range in this implementation; larger
	// indices share more leading bits with our ID and are therefore closer.
	closestBucket := kademlia.routingTable.getBucketIndex(neighbors[0].ID)
	var refreshErrors []error
	for index := 0; index < closestBucket; index++ {
		target, err := refreshTarget(me.ID, index)
		if err != nil {
			return err
		}
		contact := NewContact(target, "")
		if _, err := kademlia.LookupContact(&contact); err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("refresh bucket %d: %w", index, err))
		}
	}
	return errors.Join(refreshErrors...)
}

// refreshTarget preserves the prefix before index, flips that bit, and
// randomizes all remaining bits, putting the target inside exactly one bucket.
func refreshTarget(me *KademliaID, index int) (*KademliaID, error) {
	if me == nil || index < 0 || index >= IDLength*8 {
		return nil, ErrInvalidTarget
	}
	var target KademliaID
	if _, err := rand.Read(target[:]); err != nil {
		return nil, fmt.Errorf("generate bucket refresh target: %w", err)
	}
	byteIndex, bitIndex := index/8, uint(index%8)
	copy(target[:byteIndex], me[:byteIndex])
	bit := byte(1 << (7 - bitIndex))
	prefix := byte(0xff << (8 - bitIndex))
	target[byteIndex] = (me[byteIndex] & prefix) | ((me[byteIndex] ^ bit) & bit) | (target[byteIndex] & (bit - 1))
	return &target, nil
}

// Republish restores replicas of all locally held values on the current k
// closest nodes. It never deletes a local value or assigns an expiration time.
func (kademlia *Kademlia) Republish() error {
	return kademlia.republish(context.Background())
}

func (kademlia *Kademlia) republish(ctx context.Context) error {
	// Entries copies values while holding dataMu; all network I/O happens
	// after that snapshot's lock has been released.
	var publishErrors []error
	for _, entry := range kademlia.Entries() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := kademlia.Store(entry.Value); err != nil {
			publishErrors = append(publishErrors, fmt.Errorf("republish %s: %w", entry.Key, err))
		}
	}
	return errors.Join(publishErrors...)
}

// RunRepublisher runs one replication pass per interval until ctx is canceled.
// Passes never overlap. Cancellation stops between stored values; an in-flight
// Store completes using the transport's configured RPC timeout/retry policy.
func (kademlia *Kademlia) RunRepublisher(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("replication interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := kademlia.republish(ctx); err != nil && ctx.Err() == nil {
				log.Printf("kademlia: periodic replication: %v", err)
			}
		}
	}
}

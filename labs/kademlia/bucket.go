package kademlia

import (
	"container/list"
	"sync"
	"time"
)

// bucket definition
// contains a List
type bucket struct {
	mu       sync.RWMutex
	list     *list.List
	capacity int
	probing  bool
	sequence uint64
}

type bucketEntry struct {
	contact Contact
	seen    uint64
}

// newBucket returns a new instance of a bucket
func newBucket(capacity int) *bucket {
	return &bucket{
		list:     list.New(),
		capacity: capacity,
	}
}

// AddContact adds the Contact to the front of the bucket
// or moves it to the front of the bucket if it already existed
func (bucket *bucket) AddContact(contact Contact, ping func(*Contact) (time.Duration, error)) {
	bucket.mu.Lock()
	for e := bucket.list.Front(); e != nil; e = e.Next() {
		entry := e.Value.(*bucketEntry)
		if contact.ID.Equals(entry.contact.ID) {
			bucket.sequence++
			entry.contact = contact
			entry.seen = bucket.sequence
			bucket.list.MoveToFront(e)
			bucket.mu.Unlock()
			return
		}
	}

	if bucket.list.Len() < bucket.capacity {
		bucket.sequence++
		bucket.list.PushFront(&bucketEntry{contact, bucket.sequence})
		bucket.mu.Unlock()
		return
	}
	// One outstanding probe per bucket bounds work during concurrent arrivals.
	// Existing contacts may still be refreshed while that probe is in flight.
	if ping == nil || bucket.probing {
		bucket.mu.Unlock()
		return
	}
	oldest := bucket.list.Back()
	entry := oldest.Value.(*bucketEntry)
	oldContact, seen := entry.contact, entry.seen
	bucket.probing = true
	bucket.mu.Unlock()

	// Never hold a bucket lock across network I/O. A PING response may itself
	// update this bucket, and other RPC handlers must be able to make progress.
	_, err := ping(&oldContact)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	bucket.probing = false
	if err == nil {
		bucket.sequence++
		entry.seen = bucket.sequence
		bucket.list.MoveToFront(oldest)
	} else if entry.seen == seen {
		// A timeout cannot evict a contact heard from after the probe began.
		bucket.list.Remove(oldest)
		bucket.sequence++
		bucket.list.PushFront(&bucketEntry{contact, bucket.sequence})
	}
}

// GetContactAndCalcDistance returns an array of Contacts where
// the distance has already been calculated
func (bucket *bucket) GetContactAndCalcDistance(target *KademliaID) []Contact {
	bucket.mu.RLock()
	defer bucket.mu.RUnlock()

	var contacts []Contact

	for elt := bucket.list.Front(); elt != nil; elt = elt.Next() {
		contact := elt.Value.(*bucketEntry).contact
		contact.CalcDistance(target)
		contacts = append(contacts, contact)
	}

	return contacts
}

// Len return the size of the bucket
func (bucket *bucket) Len() int {
	bucket.mu.RLock()
	defer bucket.mu.RUnlock()

	return bucket.list.Len()
}

package kademlia

import (
	"container/list"
	"sync"
)

// bucket definition
// contains a List
type bucket struct {
	mu       sync.RWMutex
	list     *list.List
	capacity int
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
func (bucket *bucket) AddContact(contact Contact) {
	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	var element *list.Element
	for e := bucket.list.Front(); e != nil; e = e.Next() {
		nodeID := e.Value.(Contact).ID

		if (contact).ID.Equals(nodeID) {
			element = e
		}
	}

	if element == nil {
		if bucket.list.Len() < bucket.capacity {
			bucket.list.PushFront(contact)
		}
	} else {
		bucket.list.MoveToFront(element)
	}
}

// GetContactAndCalcDistance returns an array of Contacts where
// the distance has already been calculated
func (bucket *bucket) GetContactAndCalcDistance(target *KademliaID) []Contact {
	bucket.mu.RLock()
	defer bucket.mu.RUnlock()

	var contacts []Contact

	for elt := bucket.list.Front(); elt != nil; elt = elt.Next() {
		contact := elt.Value.(Contact)
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

package kademlia

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
)

var (
	ErrInvalidHash    = errors.New("hash must be a 256-bit hexadecimal string")
	ErrHashMismatch   = errors.New("value does not match its hash")
	ErrInvalidTarget  = errors.New("lookup target has no ID")
	ErrNoRoutingTable = errors.New("kademlia has no routing table")
	ErrNoNetwork      = errors.New("kademlia has no network client")
	ErrLookupFailed   = errors.New("no contacted node responded")
	ErrValueNotFound  = errors.New("value not found")
)

// FindDataResult represents the two legal FIND_VALUE responses: either the
// value was found, or the remote node returned contacts closer to the key.
type FindDataResult struct {
	Value    []byte
	Contacts []Contact
	Found    bool
}

// RPCClient is the contract between the core Kademlia algorithm (Person B)
// and the real or simulated network implementation (Person A).
type RPCClient interface {
	SendFindContactMessage(contact *Contact, target *KademliaID) ([]Contact, error)
	SendFindDataMessage(contact *Contact, hash *KademliaID) (FindDataResult, error)
	SendStoreMessage(contact *Contact, hash *KademliaID, data []byte) error
}

// Kademlia contains the node's routing table, network transport, and local
// key-value store. The data-store mutex permits RPC handlers and local calls
// to access values concurrently.
type Kademlia struct {
	routingTable *RoutingTable
	network      RPCClient

	dataMu sync.RWMutex
	data   map[string][]byte
}

// NewKademlia wires the core algorithm to a routing table and a network.
func NewKademlia(routingTable *RoutingTable, network RPCClient) *Kademlia {
	return &Kademlia{
		routingTable: routingTable,
		network:      network,
		data:         make(map[string][]byte),
	}
}

type lookupCandidate struct {
	contact Contact
	queried bool
	failed  bool
}

// LookupContact performs the iterative Kademlia node lookup. Sprint 1 uses a
// sequential lookup (alpha=1): one closest unqueried node is probed at a time.
func (kademlia *Kademlia) LookupContact(target *Contact) ([]Contact, error) {
	if target == nil || target.ID == nil {
		return nil, ErrInvalidTarget
	}
	if kademlia.routingTable == nil {
		return nil, ErrNoRoutingTable
	}

	candidates := make(map[string]*lookupCandidate)
	kademlia.addCandidates(candidates, kademlia.routingTable.FindClosestContacts(target.ID, kademlia.routingTable.K()))
	if len(candidates) == 0 {
		return []Contact{}, nil
	}
	if kademlia.network == nil {
		return nil, ErrNoNetwork
	}

	for {
		next := kademlia.nextCandidate(candidates, target.ID)
		if next == nil {
			break
		}

		candidate := candidates[next.ID.String()]
		candidate.queried = true
		contacts, err := kademlia.network.SendFindContactMessage(next, target.ID)
		if err != nil {
			candidate.failed = true
			continue
		}
		kademlia.routingTable.AddContact(*next)

		for _, contact := range contacts {
			kademlia.routingTable.AddContact(contact)
		}
		kademlia.addCandidates(candidates, contacts)
	}

	contacts := sortedCandidates(candidates, target.ID, kademlia.routingTable.K(), true)
	if len(contacts) == 0 {
		return nil, ErrLookupFailed
	}
	return contacts, nil
}

// LookupData searches locally and then performs an iterative FIND_VALUE
// lookup. Any returned value is accepted only when SHA-256(value) equals key.
func (kademlia *Kademlia) LookupData(hash string) ([]byte, *Contact, error) {
	key, err := parseHash(hash)
	if err != nil {
		return nil, nil, err
	}
	if kademlia.routingTable == nil {
		return nil, nil, ErrNoRoutingTable
	}

	if value, ok := kademlia.localValue(key); ok {
		return value, nil, nil
	}

	candidates := make(map[string]*lookupCandidate)
	kademlia.addCandidates(candidates, kademlia.routingTable.FindClosestContacts(key, kademlia.routingTable.K()))
	if len(candidates) == 0 {
		return nil, nil, ErrValueNotFound
	}
	if kademlia.network == nil {
		return nil, nil, ErrNoNetwork
	}

	sawHashMismatch := false
	for {
		next := kademlia.nextCandidate(candidates, key)
		if next == nil {
			break
		}

		candidate := candidates[next.ID.String()]
		candidate.queried = true
		result, requestErr := kademlia.network.SendFindDataMessage(next, key)
		if requestErr != nil {
			candidate.failed = true
			continue
		}
		kademlia.routingTable.AddContact(*next)

		if result.Found {
			if !dataMatchesHash(key, result.Value) {
				sawHashMismatch = true
				log.Printf("kademlia: discarded invalid value for key %s from %s", key, next.Address)
				continue
			}

			value := cloneBytes(result.Value)
			_ = kademlia.StoreValue(key, value)
			source := *next
			return value, &source, nil
		}

		for _, contact := range result.Contacts {
			kademlia.routingTable.AddContact(contact)
		}
		kademlia.addCandidates(candidates, result.Contacts)
	}

	if sawHashMismatch {
		return nil, nil, ErrHashMismatch
	}
	return nil, nil, ErrValueNotFound
}

// Store hashes data, finds the k closest nodes, and sends the key-value pair
// to each of them. If this node is among the k closest, it stores a local copy.
func (kademlia *Kademlia) Store(data []byte) (string, error) {
	if kademlia.routingTable == nil {
		return "", ErrNoRoutingTable
	}

	key := hashData(data)
	target := NewContact(key, "")
	contacts, lookupErr := kademlia.LookupContact(&target)
	contacts = append(contacts, kademlia.routingTable.me)
	contacts = closestUniqueContacts(contacts, key, kademlia.routingTable.K())

	var storeErrors []error
	if lookupErr != nil && !errors.Is(lookupErr, ErrLookupFailed) {
		storeErrors = append(storeErrors, lookupErr)
	}

	for i := range contacts {
		contact := contacts[i]
		if contact.ID.Equals(kademlia.routingTable.me.ID) {
			if err := kademlia.StoreValue(key, data); err != nil {
				storeErrors = append(storeErrors, err)
			}
			continue
		}
		if kademlia.network == nil {
			storeErrors = append(storeErrors, ErrNoNetwork)
			continue
		}
		if err := kademlia.network.SendStoreMessage(&contact, key, cloneBytes(data)); err != nil {
			storeErrors = append(storeErrors, fmt.Errorf("store on %s: %w", contact.Address, err))
		} else {
			kademlia.routingTable.AddContact(contact)
		}
	}

	return key.String(), errors.Join(storeErrors...)
}

// StoreValue validates and stores an incoming key-value pair. Person A's
// STORE RPC handler can call this method before acknowledging the request.
func (kademlia *Kademlia) StoreValue(key *KademliaID, data []byte) error {
	if key == nil || !dataMatchesHash(key, data) {
		return ErrHashMismatch
	}

	kademlia.dataMu.Lock()
	defer kademlia.dataMu.Unlock()
	if kademlia.data == nil {
		kademlia.data = make(map[string][]byte)
	}
	kademlia.data[key.String()] = cloneBytes(data)
	return nil
}

func (kademlia *Kademlia) localValue(key *KademliaID) ([]byte, bool) {
	kademlia.dataMu.RLock()
	defer kademlia.dataMu.RUnlock()

	value, ok := kademlia.data[key.String()]
	if !ok || !dataMatchesHash(key, value) {
		return nil, false
	}
	return cloneBytes(value), true
}

func (kademlia *Kademlia) addCandidates(candidates map[string]*lookupCandidate, contacts []Contact) {
	for _, contact := range contacts {
		if contact.ID == nil || contact.Address == "" {
			continue
		}
		if kademlia.routingTable.me.ID != nil && contact.ID.Equals(kademlia.routingTable.me.ID) {
			continue
		}
		id := contact.ID.String()
		if _, exists := candidates[id]; !exists {
			candidates[id] = &lookupCandidate{contact: contact}
		}
	}
}

func (kademlia *Kademlia) nextCandidate(candidates map[string]*lookupCandidate, target *KademliaID) *Contact {
	ordered := sortedCandidateEntries(candidates, target, false)
	limit := kademlia.routingTable.K()
	if len(ordered) < limit {
		limit = len(ordered)
	}
	for _, candidate := range ordered[:limit] {
		if !candidate.queried {
			contact := candidate.contact
			return &contact
		}
	}
	return nil
}

func sortedCandidates(candidates map[string]*lookupCandidate, target *KademliaID, limit int, responsiveOnly bool) []Contact {
	entries := sortedCandidateEntries(candidates, target, responsiveOnly)
	if len(entries) < limit {
		limit = len(entries)
	}
	contacts := make([]Contact, 0, limit)
	for _, candidate := range entries[:limit] {
		contacts = append(contacts, candidate.contact)
	}
	return contacts
}

func sortedCandidateEntries(candidates map[string]*lookupCandidate, target *KademliaID, responsiveOnly bool) []*lookupCandidate {
	entries := make([]*lookupCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.failed || (responsiveOnly && !candidate.queried) {
			continue
		}
		entries = append(entries, candidate)
	}
	sort.Slice(entries, func(i, j int) bool {
		left := entries[i].contact.ID.CalcDistance(target)
		right := entries[j].contact.ID.CalcDistance(target)
		return left.Less(right)
	})
	return entries
}

func closestUniqueContacts(contacts []Contact, target *KademliaID, limit int) []Contact {
	unique := make(map[string]Contact)
	for _, contact := range contacts {
		if contact.ID != nil {
			unique[contact.ID.String()] = contact
		}
	}

	ordered := make([]Contact, 0, len(unique))
	for _, contact := range unique {
		contact.CalcDistance(target)
		ordered = append(ordered, contact)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Less(&ordered[j])
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

func parseHash(value string) (*KademliaID, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != IDLength {
		return nil, ErrInvalidHash
	}
	key := KademliaID{}
	copy(key[:], decoded)
	return &key, nil
}

func hashData(data []byte) *KademliaID {
	sum := sha256.Sum256(data)
	key := KademliaID(sum)
	return &key
}

func dataMatchesHash(key *KademliaID, data []byte) bool {
	return key != nil && hashData(data).Equals(key)
}

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}

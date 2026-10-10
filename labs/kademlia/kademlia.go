package kademlia

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// MaxValueSize leaves room for base64 encoding and JSON metadata while keeping
// value RPCs below even the small UDP send limits used by local development
// systems. The same limit applies to real and simulated transports.
const MaxValueSize = 4 * 1024

var (
	ErrInvalidHash    = errors.New("hash must be a 256-bit hexadecimal string")
	ErrHashMismatch   = errors.New("value does not match its hash")
	ErrValueConflict  = errors.New("content-addressed value would replace a mutable value")
	ErrInvalidTarget  = errors.New("lookup target has no ID")
	ErrNoRoutingTable = errors.New("kademlia has no routing table")
	ErrNoNetwork      = errors.New("kademlia has no network client")
	ErrLookupFailed   = errors.New("no contacted node responded")
	ErrValueNotFound  = errors.New("value not found")
	ErrValueTooLarge  = errors.New("value exceeds size limit")
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
// Implementations must allow concurrent calls, including lookup probes.
type RPCClient interface {
	SendPingMessage(contact *Contact) (time.Duration, error)
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
	configMu     sync.RWMutex
	alpha        int
	lookupLogger *slog.Logger
	lookupID     atomic.Uint64
	validator    ValueValidator

	dataMu sync.RWMutex
	data   map[string][]byte
}

// NewKademlia wires the core algorithm to a routing table and a network.
// The table belongs to this node and must not yet be in concurrent use.
func NewKademlia(routingTable *RoutingTable, network RPCClient) *Kademlia {
	if routingTable != nil && network != nil {
		routingTable.ping = network.SendPingMessage
	}
	return &Kademlia{
		routingTable: routingTable,
		network:      network,
		alpha:        DefaultAlpha,
		lookupLogger: defaultLookupLogger,
		data:         make(map[string][]byte),
	}
}

type lookupCandidate struct {
	contact Contact
	queried bool
	failed  bool
}

// LookupContact queries up to alpha of the closest unqueried candidates per
// round. It stops once the k closest non-failed candidates have been queried.
func (kademlia *Kademlia) LookupContact(target *Contact) (contacts []Contact, err error) {
	targetID := ""
	if target != nil && target.ID != nil {
		targetID = target.ID.String()
	}
	trace := kademlia.startLookup("FIND_NODE", targetID)
	defer func() {
		foundTarget := false
		for _, contact := range contacts {
			foundTarget = foundTarget || contact.ID.String() == targetID
		}
		trace.complete(err, len(contacts) > 0, false, foundTarget)
	}()
	if target == nil || target.ID == nil {
		return nil, ErrInvalidTarget
	}
	if kademlia.routingTable == nil {
		return nil, ErrNoRoutingTable
	}
	kademlia.routingTable.touchLookup(target.ID)

	candidates := make(map[string]*lookupCandidate)
	kademlia.addCandidates(candidates, kademlia.routingTable.FindClosestContacts(target.ID, kademlia.routingTable.K()))
	if len(candidates) == 0 {
		return []Contact{}, nil
	}
	if kademlia.network == nil {
		return nil, ErrNoNetwork
	}

	for {
		batch := kademlia.nextCandidates(candidates, target.ID, trace.alpha)
		if len(batch) == 0 {
			break
		}
		for _, reply := range kademlia.lookupRound(batch, target.ID, false, trace) {
			if reply.err != nil {
				candidates[reply.contact.ID.String()].failed = true
				continue
			}
			kademlia.routingTable.AddContact(reply.contact)
			// Referrals do not prove that those peers are alive.
			kademlia.addCandidates(candidates, reply.result.Contacts)
		}
	}

	contacts = sortedCandidates(candidates, target.ID, kademlia.routingTable.K(), true)
	if len(contacts) == 0 {
		return nil, ErrLookupFailed
	}
	return contacts, nil
}

// LookupData searches locally and then performs an iterative FIND_VALUE
// lookup. Remote values must match their hash or pass the configured validator
// before being cached. A local hit returns the last accepted value; it does not
// poll peers for newer mutable values.
func (kademlia *Kademlia) LookupData(hash string) (value []byte, source *Contact, err error) {
	trace := kademlia.startLookup("FIND_VALUE", hash)
	defer func() { trace.complete(err, err == nil, err == nil && source == nil, false) }()
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
	kademlia.routingTable.touchLookup(key)

	candidates := make(map[string]*lookupCandidate)
	kademlia.addCandidates(candidates, kademlia.routingTable.FindClosestContacts(key, kademlia.routingTable.K()))
	if len(candidates) == 0 {
		return nil, nil, ErrValueNotFound
	}
	if kademlia.network == nil {
		return nil, nil, ErrNoNetwork
	}

	var invalidValueErr error
	for {
		batch := kademlia.nextCandidates(candidates, key, trace.alpha)
		if len(batch) == 0 {
			break
		}
		for _, reply := range kademlia.lookupRound(batch, key, true, trace) {
			candidate := candidates[reply.contact.ID.String()]
			if reply.err != nil {
				candidate.failed = true
				continue
			}
			kademlia.routingTable.AddContact(reply.contact)
			if reply.result.Found {
				if validationErr := kademlia.StoreValue(key, reply.result.Value); validationErr != nil {
					candidate.failed = true
					invalidValueErr = errors.Join(invalidValueErr, validationErr)
					log.Printf("kademlia: discarded invalid value for key %s from %s: %v", key, reply.contact.Address, validationErr)
					continue
				}

				value := cloneBytes(reply.result.Value)
				source := reply.contact
				return value, &source, nil
			}
			kademlia.addCandidates(candidates, reply.result.Contacts)
		}
	}

	if invalidValueErr != nil {
		return nil, nil, invalidValueErr
	}
	return nil, nil, ErrValueNotFound
}

// Store hashes data, finds the k closest nodes, and sends the key-value pair
// to each of them. If this node is among the k closest, it stores a local copy.
func (kademlia *Kademlia) Store(data []byte) (string, error) {
	if err := validateValueSize(data); err != nil {
		return "", err
	}
	if kademlia.routingTable == nil {
		return "", ErrNoRoutingTable
	}

	key := hashData(data)
	return key.String(), kademlia.StoreAt(key, data)
}

// StoreAt publishes data under an explicit key to the k closest nodes. Each
// destination validates the pair through StoreValue, including this node when
// it is among the k closest. Errors are joined; successful replicas are kept
// even if another destination rejects the value. The caller can use StoreAt
// to publish a mutable pointer without changing its key when the value changes.
func (kademlia *Kademlia) StoreAt(key *KademliaID, data []byte) error {
	if err := validateValueSize(data); err != nil {
		return err
	}
	if key == nil {
		return ErrInvalidHash
	}
	if kademlia.routingTable == nil {
		return ErrNoRoutingTable
	}
	keyCopy := *key
	key = &keyCopy
	data = cloneBytes(data)
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

	return errors.Join(storeErrors...)
}

// DataEntry is a single locally stored key-value pair, exposed for display
// (e.g. the CLI's "show ds" command).
type DataEntry struct {
	Key   string
	Value []byte
}

// Entries returns every locally stored key-value pair, sorted by key.
func (kademlia *Kademlia) Entries() []DataEntry {
	kademlia.dataMu.RLock()
	defer kademlia.dataMu.RUnlock()

	entries := make([]DataEntry, 0, len(kademlia.data))
	for key, value := range kademlia.data {
		entries = append(entries, DataEntry{Key: key, Value: cloneBytes(value)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

func (kademlia *Kademlia) localValue(key *KademliaID) ([]byte, bool) {
	kademlia.dataMu.RLock()
	defer kademlia.dataMu.RUnlock()

	value, ok := kademlia.data[key.String()]
	// Every write passes through StoreValue. Mutable values were validated on
	// acceptance and must remain readable even though their hash is not the key.
	if !ok {
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

func (kademlia *Kademlia) nextCandidates(candidates map[string]*lookupCandidate, target *KademliaID, alpha int) []Contact {
	ordered := sortedCandidateEntries(candidates, target, false)
	limit := kademlia.routingTable.K()
	if len(ordered) < limit {
		limit = len(ordered)
	}
	batch := make([]Contact, 0, min(alpha, limit))
	for _, candidate := range ordered[:limit] {
		if !candidate.queried {
			candidate.queried = true
			batch = append(batch, candidate.contact)
			if len(batch) == alpha {
				break
			}
		}
	}
	return batch
}

func validateValueSize(data []byte) error {
	if len(data) > MaxValueSize {
		return fmt.Errorf("%w: got %d bytes, maximum is %d bytes", ErrValueTooLarge, len(data), MaxValueSize)
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

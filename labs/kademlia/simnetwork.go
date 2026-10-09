package kademlia

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// SimConfig controls the behaviour of a SimNetwork.
//
// Each RPC attempt consists of two legs, the request and the response. Every
// leg takes a latency drawn uniformly from [MinLatency, MaxLatency] and is
// independently lost with probability PacketLoss. A lost leg makes the attempt
// fail; the caller then retries, up to Retries extra attempts, exactly like
// Network.Retries does for the real UDP transport. An RPC fails when every
// attempt failed.
//
// Every loss and latency decision is a pure function of (Seed, sender,
// receiver, call number, attempt, leg), so the behaviour of the network itself
// is exactly repeatable from its seed.
type SimConfig struct {
	Seed       int64
	MinLatency time.Duration
	MaxLatency time.Duration
	PacketLoss float64 // probability in [0, 1] that one leg is lost
	Retries    int     // extra attempts after the first, like Network.Retries

	// RealTime makes RPCs sleep for their simulated round-trip time. By
	// default simulated latency is virtual: it is reported (PING returns it)
	// but costs no wall-clock time, which keeps thousand-node runs fast.
	RealTime bool
}

// RPCEvent describes one completed RPC (a request together with all its
// retries) as seen by the network. Experiments use it to count the probes a
// lookup sends without the core algorithm having to know about the experiment.
type RPCEvent struct {
	Type string // "PING", "FIND_NODE", "FIND_VALUE" or "STORE"
	From string // sender address
	To   string // receiver address
	OK   bool   // false if no attempt got an answer, or the receiver refused the request
}

var errSimPacketLost = errors.New("simnetwork: packet lost")

// SimNetwork is an in-process stand-in for the UDP transport: any number of
// Kademlia nodes exchange RPCs through function calls, with configurable
// latency and packet loss. It lets the unmodified core algorithm run with
// thousands of nodes in a single process.
//
// Each node gets its own RPCClient (simHandle) because an RPC must know who
// sent it, just as every real UDP request carries its sender: the receiver
// learns the sender's contact details while serving the request. SimNetwork
// itself is the shared medium that finds the receiving node by address.
//
// The handlers mirror Network.handlePacket: FIND_NODE and FIND_VALUE answer
// from the receiver's routing table and data store, STORE goes through
// StoreValue (so K = hash(V) is enforced exactly as on the real network), and
// the receiver adds the sender to its routing table after computing its answer.
type SimNetwork struct {
	mu       sync.RWMutex
	nodes    map[string]*Kademlia
	cfg      SimConfig
	observer func(RPCEvent)
}

// NewSimNetwork creates an empty simulated network.
func NewSimNetwork(cfg SimConfig) *SimNetwork {
	return &SimNetwork{nodes: make(map[string]*Kademlia), cfg: sanitizeSimConfig(cfg)}
}

func sanitizeSimConfig(cfg SimConfig) SimConfig {
	cfg.PacketLoss = max(0, min(1, cfg.PacketLoss))
	cfg.Retries = max(0, cfg.Retries)
	cfg.MinLatency = max(0, cfg.MinLatency)
	cfg.MaxLatency = max(cfg.MinLatency, cfg.MaxLatency)
	return cfg
}

// Config returns the current settings.
func (s *SimNetwork) Config() SimConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SetPacketLoss changes the per-leg loss probability for all later RPCs.
func (s *SimNetwork) SetPacketLoss(probability float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.PacketLoss = max(0, min(1, probability))
}

// SetRetries changes the number of extra attempts for all later RPCs.
func (s *SimNetwork) SetRetries(retries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Retries = max(0, retries)
}

// SetObserver registers fn to be called after every completed RPC; nil removes
// it. fn runs on the goroutine that made the RPC and must be safe for
// concurrent use if nodes are used concurrently.
func (s *SimNetwork) SetObserver(fn func(RPCEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = fn
}

// NewNode builds a Kademlia node attached to this network and reachable under
// me.Address. k is the bucket size / replication factor; 0 selects DefaultK.
func (s *SimNetwork) NewNode(me Contact, k int) (*Kademlia, error) {
	if me.ID == nil || me.Address == "" {
		return nil, errors.New("simnetwork: a node needs an ID and an address")
	}
	handle := &simHandle{net: s, me: me, calls: make(map[string]uint64)}
	node := NewKademlia(NewRoutingTable(me, k), handle)
	handle.node = node

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.nodes[me.Address]; exists {
		return nil, fmt.Errorf("simnetwork: address %q is already registered", me.Address)
	}
	s.nodes[me.Address] = node
	return node, nil
}

// Remove takes the node at address off the network, as if it had crashed:
// later RPCs to it fail. It reports whether such a node existed.
func (s *SimNetwork) Remove(address string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.nodes[address]
	delete(s.nodes, address)
	return existed
}

// Node returns the node registered at address, or nil.
func (s *SimNetwork) Node(address string) *Kademlia {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodes[address]
}

// NodeCount returns how many nodes are on the network.
func (s *SimNetwork) NodeCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.nodes)
}

// simHandle is one node's RPCClient: its view of the SimNetwork.
type simHandle struct {
	net  *SimNetwork
	me   Contact
	node *Kademlia

	mu    sync.Mutex
	calls map[string]uint64 // RPCs started per destination, to key the random draws
}

func (h *simHandle) nextCall(to string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls[to]++
	return h.calls[to]
}

// call performs one RPC of the given type to contact. serve runs the
// receiver's side of the exchange; it runs once per attempt whose request leg
// was delivered (so a lost response still causes the server-side effects, and
// a retry repeats them). It returns the simulated round-trip time of the
// successful attempt. The observer, if any, is told how the RPC ended; a
// non-nil refused marks the RPC as unsuccessful even though it was answered.
func (h *simHandle) call(rpc string, contact *Contact, serve func(peer *Kademlia), refused func() error) (time.Duration, error) {
	if contact == nil || contact.Address == "" {
		return 0, errors.New("RPC contact has no address")
	}
	rtt, err := h.exchange(contact, serve)
	if err == nil && refused != nil {
		err = refused()
	}
	h.net.mu.RLock()
	observer := h.net.observer
	h.net.mu.RUnlock()
	if observer != nil {
		observer(RPCEvent{Type: rpc, From: h.me.Address, To: contact.Address, OK: err == nil})
	}
	return rtt, err
}

func (h *simHandle) exchange(contact *Contact, serve func(peer *Kademlia)) (time.Duration, error) {
	h.net.mu.RLock()
	peer := h.net.nodes[contact.Address]
	h.net.mu.RUnlock()
	if peer == nil {
		return 0, fmt.Errorf("simnetwork: no node at %s", contact.Address)
	}

	cfg := h.net.Config()
	call := h.nextCall(contact.Address)
	var lastErr error
	for attempt := 0; attempt <= cfg.Retries; attempt++ {
		requestLatency, lost := cfg.leg(h.me.Address, contact.Address, call, attempt, 0)
		if lost {
			lastErr = errSimPacketLost
			continue
		}
		serve(peer)
		responseLatency, lost := cfg.leg(contact.Address, h.me.Address, call, attempt, 1)
		if lost {
			lastErr = errSimPacketLost
			continue
		}
		rtt := requestLatency + responseLatency
		if cfg.RealTime && rtt > 0 {
			time.Sleep(rtt)
		}
		return rtt, nil
	}
	return 0, fmt.Errorf("simnetwork: no response from %s after %d attempt(s): %w", contact.Address, cfg.Retries+1, lastErr)
}

// leg decides the fate of one packet: its latency and whether it is lost.
func (cfg SimConfig) leg(from, to string, call uint64, attempt, leg int) (time.Duration, bool) {
	lost := cfg.PacketLoss > 0 && simDraw(cfg.Seed, from, to, call, attempt, leg, 0) < cfg.PacketLoss
	latency := cfg.MinLatency
	if cfg.MaxLatency > cfg.MinLatency {
		latency += time.Duration(simDraw(cfg.Seed, from, to, call, attempt, leg, 1) * float64(cfg.MaxLatency-cfg.MinLatency))
	}
	return latency, lost
}

// simDraw returns a pseudo-random number in [0, 1) that depends only on its
// arguments.
func simDraw(seed int64, from, to string, call uint64, attempt, leg, purpose int) float64 {
	h := fnv.New64a()
	var buf [8]byte
	put := func(v uint64) {
		for i := range buf {
			buf[i] = byte(v >> (8 * i))
		}
		_, _ = h.Write(buf[:])
	}
	put(uint64(seed))
	_, _ = h.Write([]byte(from))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(to))
	_, _ = h.Write([]byte{0})
	put(call)
	put(uint64(attempt)<<16 | uint64(leg)<<8 | uint64(purpose))
	x := h.Sum64()
	// splitmix64 finalizer: FNV alone leaves visible structure in the low bits.
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / (1 << 53)
}

func (h *simHandle) SendPingMessage(contact *Contact) (time.Duration, error) {
	rtt, err := h.call("PING", contact, func(peer *Kademlia) {
		peer.routingTable.AddContact(h.me)
	}, nil)
	if err != nil {
		return 0, err
	}
	h.node.routingTable.AddContact(*contact)
	return rtt, nil
}

func (h *simHandle) SendFindContactMessage(contact *Contact, target *KademliaID) ([]Contact, error) {
	if target == nil {
		return nil, ErrInvalidTarget
	}
	var contacts []Contact
	_, err := h.call("FIND_NODE", contact, func(peer *Kademlia) {
		contacts = peer.routingTable.FindClosestContacts(target, peer.routingTable.K())
		peer.routingTable.AddContact(h.me)
	}, nil)
	if err != nil {
		return nil, err
	}
	return routableCopies(contacts), nil
}

func (h *simHandle) SendFindDataMessage(contact *Contact, hash *KademliaID) (FindDataResult, error) {
	if hash == nil {
		return FindDataResult{}, ErrInvalidHash
	}
	var result FindDataResult
	_, err := h.call("FIND_VALUE", contact, func(peer *Kademlia) {
		if value, ok := peer.localValue(hash); ok {
			result = FindDataResult{Value: value, Found: true}
		} else {
			result = FindDataResult{Contacts: routableCopies(peer.routingTable.FindClosestContacts(hash, peer.routingTable.K()))}
		}
		peer.routingTable.AddContact(h.me)
	}, nil)
	if err != nil {
		return FindDataResult{}, err
	}
	return result, nil
}

func (h *simHandle) SendStoreMessage(contact *Contact, hash *KademliaID, data []byte) error {
	if hash == nil {
		return ErrInvalidHash
	}
	var storeErr error
	_, err := h.call("STORE", contact, func(peer *Kademlia) {
		storeErr = peer.StoreValue(hash, cloneBytes(data))
		peer.routingTable.AddContact(h.me)
	}, func() error { return storeErr })
	return err
}

// routableCopies rebuilds contacts the way they arrive over the wire: only ID
// and address survive, and entries without an address are dropped.
func routableCopies(contacts []Contact) []Contact {
	result := make([]Contact, 0, len(contacts))
	for _, contact := range contacts {
		if contact.ID == nil || contact.Address == "" {
			continue
		}
		result = append(result, NewContact(contact.ID, contact.Address))
	}
	return result
}

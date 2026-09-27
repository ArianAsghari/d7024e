package kademlia

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	rpcPing        = "PING"
	rpcPingReply   = "PING_REPLY"
	rpcFindNode    = "FIND_NODE"
	rpcFindReply   = "FIND_NODE_REPLY"
	rpcFindValue   = "FIND_VALUE"
	rpcValueReply  = "FIND_VALUE_REPLY"
	rpcStore       = "STORE"
	rpcStoreReply  = "STORE_REPLY"
	defaultTimeout = 500 * time.Millisecond
	defaultRetries = 2
	maxDatagram    = 64 * 1024
)

var (
	ErrNetworkNotReady = errors.New("network is not attached to a Kademlia node")
	ErrRPCRequestID    = errors.New("RPC response request ID mismatch")
	ErrRPCResponseType = errors.New("unexpected RPC response type")
)

// Network implements the real UDP transport used by a Kademlia node.
// Timeout and Retries are deliberately configurable because the lab's
// reliability experiments depend on the timeout/retry policy.
type Network struct {
	me Contact

	mu       sync.RWMutex
	kademlia *Kademlia
	conn     *net.UDPConn
	closed   bool

	Timeout time.Duration
	Retries int
}

type wireContact struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}

type rpcMessage struct {
	Type      string        `json:"type"`
	RequestID string        `json:"request_id"`
	Sender    *wireContact  `json:"sender,omitempty"`
	Target    string        `json:"target,omitempty"`
	Key       string        `json:"key,omitempty"`
	Data      []byte        `json:"data,omitempty"`
	Contacts  []wireContact `json:"contacts,omitempty"`
	Found     bool          `json:"found,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// NewNetwork creates a UDP transport for me. AttachKademlia must be called
// before serving FIND_NODE, FIND_VALUE, or STORE requests.
func NewNetwork(me Contact) *Network {
	return &Network{
		me:      me,
		Timeout: defaultTimeout,
		Retries: defaultRetries,
	}
}

// AttachKademlia gives the RPC handlers access to the routing table and local
// data store after the Kademlia object has been constructed.
func (network *Network) AttachKademlia(kademlia *Kademlia) {
	network.mu.Lock()
	defer network.mu.Unlock()
	network.kademlia = kademlia
}

// Listen binds the network transport and starts the UDP receive loop.
// The caller can continue immediately; Close stops the loop.
func (network *Network) Listen(ip string, port int) error {
	address := net.JoinHostPort(ip, strconv.Itoa(port))
	udpAddress, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return fmt.Errorf("resolve listen address %s: %w", address, err)
	}

	conn, err := net.ListenUDP("udp", udpAddress)
	if err != nil {
		return fmt.Errorf("listen UDP on %s: %w", address, err)
	}

	network.mu.Lock()
	if network.conn != nil && !network.closed {
		network.mu.Unlock()
		_ = conn.Close()
		return errors.New("network is already listening")
	}
	network.conn = conn
	network.closed = false
	network.mu.Unlock()

	go network.receiveLoop(conn)
	return nil
}

// Close stops a listening Network. Calling Close more than once is safe.
func (network *Network) Close() error {
	network.mu.Lock()
	defer network.mu.Unlock()
	if network.conn == nil || network.closed {
		return nil
	}
	network.closed = true
	return network.conn.Close()
}

// LocalAddr returns the bound UDP address, or nil when Listen has not run.
func (network *Network) LocalAddr() net.Addr {
	network.mu.RLock()
	defer network.mu.RUnlock()
	if network.conn == nil {
		return nil
	}
	return network.conn.LocalAddr()
}

// SendPingMessage sends a PING RPC and returns its round-trip time.
func (network *Network) SendPingMessage(contact *Contact) (time.Duration, error) {
	if contact == nil {
		return 0, errors.New("ping contact is nil")
	}
	start := time.Now()
	response, err := network.roundTrip(contact, rpcMessage{Type: rpcPing})
	if err != nil {
		return 0, err
	}
	if response.Type != rpcPingReply {
		return 0, fmt.Errorf("%w: got %q", ErrRPCResponseType, response.Type)
	}
	return time.Since(start), nil
}

// SendFindContactMessage performs a FIND_NODE RPC.
func (network *Network) SendFindContactMessage(contact *Contact, target *KademliaID) ([]Contact, error) {
	if target == nil {
		return nil, ErrInvalidTarget
	}
	response, err := network.roundTrip(contact, rpcMessage{Type: rpcFindNode, Target: target.String()})
	if err != nil {
		return nil, err
	}
	if response.Type != rpcFindReply {
		return nil, fmt.Errorf("%w: got %q", ErrRPCResponseType, response.Type)
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}
	return contactsFromWire(response.Contacts)
}

// SendFindDataMessage performs a FIND_VALUE RPC.
func (network *Network) SendFindDataMessage(contact *Contact, hash *KademliaID) (FindDataResult, error) {
	if hash == nil {
		return FindDataResult{}, ErrInvalidHash
	}
	response, err := network.roundTrip(contact, rpcMessage{Type: rpcFindValue, Key: hash.String()})
	if err != nil {
		return FindDataResult{}, err
	}
	if response.Type != rpcValueReply {
		return FindDataResult{}, fmt.Errorf("%w: got %q", ErrRPCResponseType, response.Type)
	}
	if response.Error != "" {
		return FindDataResult{}, errors.New(response.Error)
	}
	contacts, err := contactsFromWire(response.Contacts)
	if err != nil {
		return FindDataResult{}, err
	}
	return FindDataResult{Value: cloneBytes(response.Data), Contacts: contacts, Found: response.Found}, nil
}

// SendStoreMessage performs a STORE RPC. The remote side verifies K = hash(V)
// before acknowledging the request.
func (network *Network) SendStoreMessage(contact *Contact, hash *KademliaID, data []byte) error {
	if hash == nil {
		return ErrInvalidHash
	}
	response, err := network.roundTrip(contact, rpcMessage{Type: rpcStore, Key: hash.String(), Data: cloneBytes(data)})
	if err != nil {
		return err
	}
	if response.Type != rpcStoreReply {
		return fmt.Errorf("%w: got %q", ErrRPCResponseType, response.Type)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return nil
}

func (network *Network) roundTrip(contact *Contact, request rpcMessage) (rpcMessage, error) {
	if contact == nil || contact.Address == "" {
		return rpcMessage{}, errors.New("RPC contact has no address")
	}

	requestID, err := newRequestID()
	if err != nil {
		return rpcMessage{}, fmt.Errorf("create request ID: %w", err)
	}
	request.RequestID = requestID
	if network.me.ID != nil && network.me.Address != "" {
		sender := contactToWire(network.me)
		request.Sender = &sender
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return rpcMessage{}, fmt.Errorf("encode %s request: %w", request.Type, err)
	}
	if len(payload) > maxDatagram {
		return rpcMessage{}, fmt.Errorf("RPC message too large for UDP datagram: %d bytes", len(payload))
	}

	remote, err := net.ResolveUDPAddr("udp", contact.Address)
	if err != nil {
		return rpcMessage{}, fmt.Errorf("resolve %s: %w", contact.Address, err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		return rpcMessage{}, fmt.Errorf("dial %s: %w", contact.Address, err)
	}
	defer conn.Close()

	timeout := network.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	retries := network.Retries
	if retries < 0 {
		retries = 0
	}

	buffer := make([]byte, maxDatagram)
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if _, err := conn.Write(payload); err != nil {
			lastErr = err
			continue
		}
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return rpcMessage{}, err
		}

		for {
			n, err := conn.Read(buffer)
			if err != nil {
				lastErr = err
				break
			}
			var response rpcMessage
			if err := json.Unmarshal(buffer[:n], &response); err != nil {
				lastErr = err
				continue
			}
			if response.RequestID != requestID {
				lastErr = ErrRPCRequestID
				continue
			}
			return response, nil
		}
	}

	return rpcMessage{}, fmt.Errorf("%s RPC to %s failed after %d attempt(s): %w", request.Type, contact.Address, retries+1, lastErr)
}

func (network *Network) receiveLoop(conn *net.UDPConn) {
	buffer := make([]byte, maxDatagram)
	for {
		n, remote, err := conn.ReadFromUDP(buffer)
		if err != nil {
			network.mu.RLock()
			closed := network.closed
			network.mu.RUnlock()
			if closed {
				return
			}
			continue
		}

		packet := append([]byte(nil), buffer[:n]...)
		go network.handlePacket(conn, remote, packet)
	}
}

func (network *Network) handlePacket(conn *net.UDPConn, remote *net.UDPAddr, packet []byte) {
	var request rpcMessage
	if err := json.Unmarshal(packet, &request); err != nil || request.RequestID == "" {
		return
	}

	if request.Sender != nil {
		if sender, err := contactFromWire(*request.Sender); err == nil {
			network.addContact(sender)
		}
	}

	response := rpcMessage{RequestID: request.RequestID}
	switch request.Type {
	case rpcPing:
		response.Type = rpcPingReply

	case rpcFindNode:
		response.Type = rpcFindReply
		target, err := parseWireID(request.Target)
		if err != nil {
			response.Error = err.Error()
			break
		}
		kademlia := network.getKademlia()
		if kademlia == nil || kademlia.routingTable == nil {
			response.Error = ErrNetworkNotReady.Error()
			break
		}
		response.Contacts = contactsToWire(kademlia.routingTable.FindClosestContacts(target, kademlia.routingTable.K()))

	case rpcFindValue:
		response.Type = rpcValueReply
		key, err := parseWireID(request.Key)
		if err != nil {
			response.Error = err.Error()
			break
		}
		kademlia := network.getKademlia()
		if kademlia == nil || kademlia.routingTable == nil {
			response.Error = ErrNetworkNotReady.Error()
			break
		}
		if value, ok := kademlia.localValue(key); ok {
			response.Found = true
			response.Data = value
		} else {
			response.Contacts = contactsToWire(kademlia.routingTable.FindClosestContacts(key, kademlia.routingTable.K()))
		}

	case rpcStore:
		response.Type = rpcStoreReply
		key, err := parseWireID(request.Key)
		if err != nil {
			response.Error = err.Error()
			break
		}
		kademlia := network.getKademlia()
		if kademlia == nil {
			response.Error = ErrNetworkNotReady.Error()
			break
		}
		if err := kademlia.StoreValue(key, request.Data); err != nil {
			response.Error = err.Error()
		}

	default:
		return
	}

	payload, err := json.Marshal(response)
	if err != nil || len(payload) > maxDatagram {
		return
	}
	_, _ = conn.WriteToUDP(payload, remote)
}

func (network *Network) getKademlia() *Kademlia {
	network.mu.RLock()
	defer network.mu.RUnlock()
	return network.kademlia
}

func (network *Network) addContact(contact Contact) {
	kademlia := network.getKademlia()
	if kademlia != nil && kademlia.routingTable != nil {
		kademlia.routingTable.AddContact(contact)
	}
}

func newRequestID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func contactToWire(contact Contact) wireContact {
	id := ""
	if contact.ID != nil {
		id = contact.ID.String()
	}
	return wireContact{ID: id, Address: contact.Address}
}

func contactsToWire(contacts []Contact) []wireContact {
	result := make([]wireContact, 0, len(contacts))
	for _, contact := range contacts {
		if contact.ID == nil || contact.Address == "" {
			continue
		}
		result = append(result, contactToWire(contact))
	}
	return result
}

func contactsFromWire(contacts []wireContact) ([]Contact, error) {
	result := make([]Contact, 0, len(contacts))
	for _, item := range contacts {
		contact, err := contactFromWire(item)
		if err != nil {
			return nil, err
		}
		result = append(result, contact)
	}
	return result, nil
}

func contactFromWire(contact wireContact) (Contact, error) {
	id, err := parseWireID(contact.ID)
	if err != nil {
		return Contact{}, err
	}
	if strings.TrimSpace(contact.Address) == "" {
		return Contact{}, errors.New("contact address is empty")
	}
	return NewContact(id, contact.Address), nil
}

func parseWireID(value string) (*KademliaID, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != IDLength {
		return nil, ErrInvalidHash
	}
	id := KademliaID{}
	copy(id[:], decoded)
	return &id, nil
}

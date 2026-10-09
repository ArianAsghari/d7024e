// Package experiment builds large simulated Kademlia networks and runs the
// lab's evaluation experiments on them. It produces JSON-lines event logs that
// the external script scripts/analyze.py turns into tables and figures.
//
// The package only uses the public API of package kademlia plus the
// simulated network; it does not require any change to the core algorithm.
package experiment

import (
	"crypto/sha256"
	"fmt"
	"math/rand"

	"d7024e/kademlia"
)

// BuildConfig describes one randomly generated network.
type BuildConfig struct {
	N    int   // number of nodes
	K    int   // bucket size / replication factor (0 = kademlia.DefaultK)
	Seed int64 // seeds topology generation, bootstrap choice and simulated loss

	// Sim holds latency, loss and retry settings. Its Seed is overwritten with
	// BuildConfig.Seed.
	Sim kademlia.SimConfig
}

// Network is a built simulated network.
type Network struct {
	Sim          *kademlia.SimNetwork
	Nodes        []*kademlia.Kademlia
	Contacts     []kademlia.Contact // Contacts[i] belongs to Nodes[i]
	JoinFailures int                // nodes whose Join reported an error
}

// IDFromAddress derives a node ID as SHA-256("IP:port"), the lab's
// ID = hash(IP|port) rule.
func IDFromAddress(address string) *kademlia.KademliaID {
	id := kademlia.KademliaID(sha256.Sum256([]byte(address)))
	return &id
}

// RandomAddresses returns n distinct random "IP:port" strings. Node IDs are
// derived from them with IDFromAddress, so randomizing addresses randomizes
// the topology.
func RandomAddresses(rng *rand.Rand, n int) []string {
	seen := make(map[string]bool, n)
	addresses := make([]string, 0, n)
	for len(addresses) < n {
		address := fmt.Sprintf("10.%d.%d.%d:%d", rng.Intn(256), rng.Intn(256), 1+rng.Intn(254), 1024+rng.Intn(64000))
		if !seen[address] {
			seen[address] = true
			addresses = append(addresses, address)
		}
	}
	return addresses
}

// Build creates cfg.N nodes with random addresses. The first node is the
// network's founder; every other node joins through a random earlier node
// using the full join procedure (bootstrap ping, self-lookup, bucket refresh),
// so routing tables look like those of a network that grew organically.
func Build(cfg BuildConfig) (*Network, error) {
	if cfg.N < 1 {
		return nil, fmt.Errorf("network needs at least one node, got %d", cfg.N)
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	simCfg := cfg.Sim
	simCfg.Seed = cfg.Seed
	sim := kademlia.NewSimNetwork(simCfg)

	network := &Network{Sim: sim}
	for i, address := range RandomAddresses(rng, cfg.N) {
		contact := kademlia.NewContact(IDFromAddress(address), address)
		node, err := sim.NewNode(contact, cfg.K)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			bootstrap := network.Contacts[rng.Intn(i)]
			if err := node.Join(bootstrap); err != nil {
				network.JoinFailures++
			}
		}
		network.Nodes = append(network.Nodes, node)
		network.Contacts = append(network.Contacts, contact)
	}
	return network, nil
}

// Closest returns the contact in the network whose ID is nearest (by XOR) to
// target, ignoring the node with ID exclude (pass nil to consider every node).
// A lookup never returns the node that performs it, so the ground truth for a
// lookup started at node X is Closest(target, X's ID).
func (network *Network) Closest(target, exclude *kademlia.KademliaID) kademlia.Contact {
	var best kademlia.Contact
	for _, contact := range network.Contacts {
		if exclude != nil && contact.ID.Equals(exclude) {
			continue
		}
		if best.ID == nil || contact.ID.CalcDistance(target).Less(best.ID.CalcDistance(target)) {
			best = contact
		}
	}
	return best
}

// Holds reports whether node stores the value with the given hex key locally.
func Holds(node *kademlia.Kademlia, key string) bool {
	for _, entry := range node.Entries() {
		if entry.Key == key {
			return true
		}
	}
	return false
}

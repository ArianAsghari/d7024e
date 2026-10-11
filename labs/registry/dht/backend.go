// Package dht connects the registry package to a Kademlia node: Backend stores
// and fetches registry values through the node, and Install makes the node
// enforce the registry rules on every latest pointer it is asked to keep.
package dht

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"d7024e/kademlia"
	"d7024e/registry"
)

type Backend struct {
	node *kademlia.Kademlia
}

var _ registry.Backend = (*Backend)(nil)

func NewBackend(node *kademlia.Kademlia) *Backend {
	return &Backend{node: node}
}

// PutImmutable fails if any of the closest nodes did not take the value;
// replicas that accepted it are kept.
func (b *Backend) PutImmutable(data []byte) (string, error) {
	hash, err := b.node.Store(data)
	if err != nil {
		return hash, summarize(err)
	}
	return hash, nil
}

func (b *Backend) GetImmutable(hash string) ([]byte, error) {
	return lookup(b.node, hash)
}

func (b *Backend) PutMutable(key string, value []byte) error {
	id, err := parseKey(key)
	if err != nil {
		return err
	}
	if err := b.node.StoreAt(id, value); err != nil {
		return summarize(err)
	}
	// A pointer this node cached earlier would otherwise stay the version that
	// LookupData returns here, because a local hit never asks the network. The
	// replicas have already validated the value, so a failure here is harmless.
	_ = b.node.StoreValue(id, value)
	return nil
}

// GetMutable can return a stale pointer: LookupData answers from the local
// store without asking the network whether a newer one exists.
func (b *Backend) GetMutable(key string) ([]byte, error) {
	return lookup(b.node, key)
}

func lookup(node *kademlia.Kademlia, key string) ([]byte, error) {
	value, _, err := node.LookupData(key)
	switch {
	case err == nil:
		return value, nil
	case errors.Is(err, kademlia.ErrValueNotFound):
		return nil, fmt.Errorf("%w: %s", registry.ErrNotFound, registry.ShortHash(key))
	default:
		return nil, &failure{err: err}
	}
}

// failure marks an error that is not an absent value. A rejected pointer can
// carry registry.ErrNotFound (one of its version records could not be
// fetched), which callers read as "nothing published yet".
type failure struct{ err error }

func (f *failure) Error() string { return f.err.Error() }

func (f *failure) Is(target error) bool {
	return target != registry.ErrNotFound && errors.Is(f.err, target)
}

func parseKey(key string) (*kademlia.KademliaID, error) {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != kademlia.IDLength || hex.EncodeToString(raw) != key {
		return nil, fmt.Errorf("storage key %q is not a lowercase 256-bit hex string", key)
	}
	return kademlia.NewKademliaID(key), nil
}

var storePrefix = regexp.MustCompile(`^store on \S+: `)

// Unwrap keeps every destination's error reachable for errors.Is while the
// message stays on one line.
type storeFailure struct {
	msg string
	err error
}

func (f *storeFailure) Error() string { return f.msg }
func (f *storeFailure) Unwrap() error { return f.err }

func summarize(err error) error {
	var leaves []error
	flatten(err, &leaves)
	counts := map[string]int{}
	var order []string
	for _, leaf := range leaves {
		reason := storePrefix.ReplaceAllString(strings.TrimSpace(leaf.Error()), "")
		reason = strings.Join(strings.Fields(reason), " ")
		if counts[reason] == 0 {
			order = append(order, reason)
		}
		counts[reason]++
	}
	parts := make([]string, 0, len(order))
	for _, reason := range order {
		if counts[reason] > 1 {
			reason = fmt.Sprintf("%s (refused by %d nodes)", reason, counts[reason])
		}
		parts = append(parts, reason)
	}
	return &storeFailure{msg: strings.Join(parts, "; "), err: err}
}

func flatten(err error, out *[]error) {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, inner := range joined.Unwrap() {
			flatten(inner, out)
		}
		return
	}
	*out = append(*out, err)
}

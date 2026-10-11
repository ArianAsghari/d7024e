package dht

import (
	"errors"

	"d7024e/kademlia"
	"d7024e/registry"
)

// NewValidator returns a registry.Validator whose Fetch asks the node for
// version records by hash, so a node that fell behind can catch up while it
// validates a newer pointer. Set MaxChain on the result before passing it to
// ValueValidator to change the history length it will follow.
func NewValidator(node *kademlia.Kademlia, owners registry.DomainOwners) *registry.Validator {
	return &registry.Validator{
		Owners: owners,
		Fetch:  func(hash string) ([]byte, error) { return lookup(node, hash) },
	}
}

func ValueValidator(v *registry.Validator) kademlia.ValueValidator {
	return func(key *kademlia.KademliaID, current, incoming []byte) error {
		if key == nil {
			return errors.New("value has no key")
		}
		return v.Validate(key.String(), current, incoming)
	}
}

// Install makes node accept latest pointers only as the registry rules allow.
// Call it before the node serves RPCs.
func Install(node *kademlia.Kademlia, owners registry.DomainOwners) *registry.Validator {
	v := NewValidator(node, owners)
	node.SetValueValidator(ValueValidator(v))
	return v
}

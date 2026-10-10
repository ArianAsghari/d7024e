package kademlia

import "bytes"

// ValueValidator decides whether a non-content-addressed value may be stored.
// current is the locally accepted value, or nil when no value is stored.
// Returning nil accepts incoming; an error leaves the stored value unchanged.
// Content-addressed blobs bypass this hook but cannot overwrite an accepted
// mutable value (ErrValueConflict). Size limits always apply.
//
// The callback receives copies and runs without Kademlia locks. It may call
// LookupData to fetch missing immutable version records by hash before deciding.
// It must support concurrent calls and repeated validation: if another write
// changes current while validation runs, StoreValue retries with the new value.
// Accepting identical updates allows STORE retries and periodic replication.
type ValueValidator func(key *KademliaID, current, incoming []byte) error

// SetValueValidator configures validation for subsequent incoming mutable
// values. A store already in progress uses its original validator. Passing nil
// restores ErrHashMismatch for non-content-addressed writes; previously accepted
// values remain readable. Install the registry's validator before serving RPCs.
func (kademlia *Kademlia) SetValueValidator(validator ValueValidator) {
	kademlia.configMu.Lock()
	defer kademlia.configMu.Unlock()
	kademlia.validator = validator
}

// StoreValue validates and stores an incoming key-value pair. Both STORE RPCs
// and remote FIND_VALUE results use this path. Mutable values replace current
// only if the validator accepts them against the value still stored at commit.
func (kademlia *Kademlia) StoreValue(key *KademliaID, data []byte) error {
	if err := validateValueSize(data); err != nil {
		return err
	}
	if key == nil {
		return ErrHashMismatch
	}
	keyCopy := *key
	keyString := keyCopy.String()
	data = cloneBytes(data)
	var validator ValueValidator
	if !dataMatchesHash(&keyCopy, data) {
		kademlia.configMu.RLock()
		validator = kademlia.validator
		kademlia.configMu.RUnlock()
		if validator == nil {
			return ErrHashMismatch
		}
	}

	for {
		var current []byte
		var exists bool
		if validator != nil {
			kademlia.dataMu.RLock()
			current, exists = kademlia.data[keyString]
			current = cloneBytes(current)
			kademlia.dataMu.RUnlock()

			validationKey := keyCopy
			if err := validator(&validationKey, cloneBytes(current), cloneBytes(data)); err != nil {
				return err
			}
		}

		kademlia.dataMu.Lock()
		stored, stillExists := kademlia.data[keyString]
		// A named pointer has a known key preimage ("domain:package:latest").
		// Storing that string as an ordinary blob must not bypass validation
		// and overwrite an already accepted pointer.
		if validator == nil && stillExists && !dataMatchesHash(&keyCopy, stored) {
			kademlia.dataMu.Unlock()
			return ErrValueConflict
		}
		if validator != nil && (exists != stillExists || !bytes.Equal(current, stored)) {
			kademlia.dataMu.Unlock()
			continue
		}
		if kademlia.data == nil {
			kademlia.data = make(map[string][]byte)
		}
		kademlia.data[keyString] = data
		kademlia.dataMu.Unlock()
		return nil
	}
}

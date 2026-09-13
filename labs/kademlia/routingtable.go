package kademlia

// DefaultK is both the default bucket capacity and replication factor.
const DefaultK = 10

// RoutingTable definition
// keeps a refrence contact of me and an array of buckets
type RoutingTable struct {
	me      Contact
	k       int
	buckets [IDLength * 8]*bucket
}

// NewRoutingTable returns a new instance of a RoutingTable
// An optional k overrides DefaultK, which makes the parameter easy to adjust
// while preserving the starter code's one-argument constructor.
func NewRoutingTable(me Contact, sizes ...int) *RoutingTable {
	k := DefaultK
	if len(sizes) > 0 && sizes[0] > 0 {
		k = sizes[0]
	}

	routingTable := &RoutingTable{me: me, k: k}
	for i := 0; i < IDLength*8; i++ {
		routingTable.buckets[i] = newBucket(k)
	}
	return routingTable
}

// K returns the routing table's configured bucket capacity.
func (routingTable *RoutingTable) K() int {
	return routingTable.k
}

// AddContact add a new contact to the correct Bucket
func (routingTable *RoutingTable) AddContact(contact Contact) {
	if contact.ID == nil || routingTable.me.ID == nil || contact.ID.Equals(routingTable.me.ID) {
		return
	}

	bucketIndex := routingTable.getBucketIndex(contact.ID)
	bucket := routingTable.buckets[bucketIndex]
	bucket.AddContact(contact)
}

// FindClosestContacts finds the count closest Contacts to the target in the RoutingTable
func (routingTable *RoutingTable) FindClosestContacts(target *KademliaID, count int) []Contact {
	if target == nil || count <= 0 {
		return []Contact{}
	}

	var candidates ContactCandidates
	for _, bucket := range routingTable.buckets {
		candidates.Append(bucket.GetContactAndCalcDistance(target))
	}

	candidates.Sort()

	if count > candidates.Len() {
		count = candidates.Len()
	}

	return candidates.GetContacts(count)
}

// getBucketIndex get the correct Bucket index for the KademliaID
func (routingTable *RoutingTable) getBucketIndex(id *KademliaID) int {
	distance := id.CalcDistance(routingTable.me.ID)
	for i := 0; i < IDLength; i++ {
		for j := 0; j < 8; j++ {
			if (distance[i]>>uint8(7-j))&0x1 != 0 {
				return i*8 + j
			}
		}
	}

	return IDLength*8 - 1
}

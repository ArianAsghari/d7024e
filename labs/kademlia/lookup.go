package kademlia

import (
	"errors"
	"log/slog"
	"os"
	"time"
)

const DefaultAlpha = 3

var defaultLookupLogger = slog.New(slog.NewJSONHandler(os.Stderr, nil))

// SetAlpha changes the parallelism used by subsequent lookups. Each lookup
// snapshots its settings, so changing alpha cannot race with an active round.
func (kademlia *Kademlia) SetAlpha(alpha int) error {
	if alpha < 1 {
		return errors.New("alpha must be positive")
	}
	kademlia.configMu.Lock()
	defer kademlia.configMu.Unlock()
	kademlia.alpha = alpha
	return nil
}

func (kademlia *Kademlia) Alpha() int {
	kademlia.configMu.RLock()
	defer kademlia.configMu.RUnlock()
	if kademlia.alpha < 1 {
		return DefaultAlpha
	}
	return kademlia.alpha
}

// SetLookupLogger redirects lookup events, for example to an experiment's
// JSON log file. Passing nil explicitly disables lookup logging. The handler
// must support concurrent calls, as the standard slog handlers do.
func (kademlia *Kademlia) SetLookupLogger(logger *slog.Logger) {
	kademlia.configMu.Lock()
	defer kademlia.configMu.Unlock()
	kademlia.lookupLogger = logger
}

type lookupTrace struct {
	logger *slog.Logger
	alpha  int
	start  time.Time
	probes int
	rounds int
}

func (kademlia *Kademlia) startLookup(rpc, target string) *lookupTrace {
	kademlia.configMu.RLock()
	alpha, logger := kademlia.alpha, kademlia.lookupLogger
	kademlia.configMu.RUnlock()
	if alpha < 1 {
		alpha = DefaultAlpha
	}
	trace := &lookupTrace{logger: logger, alpha: alpha, start: time.Now()}
	if logger != nil {
		nodeID := ""
		if kademlia.routingTable != nil && kademlia.routingTable.me.ID != nil {
			nodeID = kademlia.routingTable.me.ID.String()
		}
		trace.logger = logger.With("node_id", nodeID, "lookup_id", kademlia.lookupID.Add(1),
			"rpc", rpc, "target", target, "alpha", alpha)
	}
	return trace
}

func (trace *lookupTrace) probe(contact Contact) {
	trace.probes++
	if trace.logger != nil {
		trace.logger.Info("lookup probe", "event", "lookup_probe", "round", trace.rounds,
			"peer_id", contact.ID.String(), "peer_address", contact.Address)
	}
}

func (trace *lookupTrace) complete(err error, success, local, foundTarget bool) {
	if trace.logger == nil {
		return
	}
	reason := ""
	if err != nil {
		reason = err.Error()
	} else if !success {
		reason = "no contacts"
	}
	trace.logger.Info("lookup complete", "event", "lookup_complete", "success", success,
		"error", reason, "probes", trace.probes, "rounds", trace.rounds,
		"elapsed_us", time.Since(trace.start).Microseconds(), "local", local, "found_target", foundTarget)
}

type lookupReply struct {
	contact Contact
	result  FindDataResult
	err     error
}

// lookupRound waits for the entire bounded batch before returning, including
// when a value is found. Thus no lookup RPC outlives its call. Only this caller
// owns candidate maps, tracing counters, and routing-table updates; workers
// communicate results through the channel. RPCClient bounds RPC duration.
func (kademlia *Kademlia) lookupRound(batch []Contact, target *KademliaID, value bool, trace *lookupTrace) []lookupReply {
	trace.rounds++
	responses := make(chan lookupReply, len(batch))
	for _, contact := range batch {
		trace.probe(contact)
		go func(contact Contact) {
			reply := lookupReply{contact: contact}
			if value {
				reply.result, reply.err = kademlia.network.SendFindDataMessage(&contact, target)
			} else {
				reply.result.Contacts, reply.err = kademlia.network.SendFindContactMessage(&contact, target)
			}
			responses <- reply
		}(contact)
	}
	results := make([]lookupReply, 0, len(batch))
	for range batch {
		results = append(results, <-responses)
	}
	return results
}

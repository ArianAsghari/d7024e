package kademlia

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// simPair returns two nodes, "a" and "b", on a fresh simulated network.
func simPair(t *testing.T, cfg SimConfig) (*SimNetwork, *Kademlia, *Kademlia) {
	t.Helper()
	network := NewSimNetwork(cfg)
	a, err := network.NewNode(NewContact(testID(1), "a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := network.NewNode(NewContact(testID(2), "b"), 0)
	if err != nil {
		t.Fatal(err)
	}
	return network, a, b
}

// holds reports whether node stores value locally.
func holds(node *Kademlia, value []byte) bool {
	_, ok := node.localValue(hashData(value))
	return ok
}

func contactB() *Contact {
	contact := NewContact(testID(2), "b")
	return &contact
}

func TestSimPingReportsVirtualRoundTripTime(t *testing.T) {
	_, a, _ := simPair(t, SimConfig{MinLatency: 5 * time.Millisecond, MaxLatency: 5 * time.Millisecond})
	start := time.Now()
	rtt, err := a.network.SendPingMessage(contactB())
	if err != nil || rtt != 10*time.Millisecond {
		t.Fatalf("ping = %v, %v; want exactly 2 x 5ms", rtt, err)
	}
	if time.Since(start) > 5*time.Millisecond {
		t.Fatal("virtual latency must not cost wall-clock time")
	}
}

func TestSimRealTimeSleepsForTheRoundTrip(t *testing.T) {
	_, a, _ := simPair(t, SimConfig{MinLatency: 10 * time.Millisecond, MaxLatency: 10 * time.Millisecond, RealTime: true})
	start := time.Now()
	if _, err := a.network.SendPingMessage(contactB()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("RealTime ping took %v, want at least 20ms", elapsed)
	}
}

func TestSimLatencyStaysWithinBounds(t *testing.T) {
	_, a, _ := simPair(t, SimConfig{Seed: 5, MinLatency: 2 * time.Millisecond, MaxLatency: 6 * time.Millisecond})
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		rtt, err := a.network.SendPingMessage(contactB())
		if err != nil || rtt < 4*time.Millisecond || rtt > 12*time.Millisecond {
			t.Fatalf("rtt = %v, %v; want within [4ms, 12ms]", rtt, err)
		}
		seen[rtt] = true
	}
	if len(seen) < 10 {
		t.Fatalf("latency does not vary: only %d distinct values", len(seen))
	}
}

func TestSimPeersLearnEachOther(t *testing.T) {
	_, a, b := simPair(t, SimConfig{})
	if _, err := a.network.SendPingMessage(contactB()); err != nil {
		t.Fatal(err)
	}
	if got := a.routingTable.FindClosestContacts(testID(2), 5); len(got) != 1 || got[0].Address != "b" {
		t.Fatalf("pinger did not learn the responder: %v", got)
	}
	if got := b.routingTable.FindClosestContacts(testID(1), 5); len(got) != 1 || got[0].Address != "a" {
		t.Fatalf("responder did not learn the sender: %v", got)
	}
}

func TestSimFindNodeAndFindValueAnswerFromThePeer(t *testing.T) {
	_, a, b := simPair(t, SimConfig{})
	third := NewContact(testID(3), "c")
	b.routingTable.AddContact(third)

	contacts, err := a.network.SendFindContactMessage(contactB(), testID(3))
	if err != nil || len(contacts) == 0 || contacts[0].Address != "c" {
		t.Fatalf("FIND_NODE = %v, %v", contacts, err)
	}
	if contacts[0].distance != nil {
		t.Fatal("contacts must arrive like wire contacts, without a precomputed distance")
	}

	value := []byte("stored on b")
	if err := b.StoreValue(hashData(value), value); err != nil {
		t.Fatal(err)
	}
	result, err := a.network.SendFindDataMessage(contactB(), hashData(value))
	if err != nil || !result.Found || string(result.Value) != string(value) {
		t.Fatalf("FIND_VALUE hit = %+v, %v", result, err)
	}
	result, err = a.network.SendFindDataMessage(contactB(), hashData([]byte("absent")))
	if err != nil || result.Found || len(result.Contacts) == 0 {
		t.Fatalf("FIND_VALUE miss = %+v, %v", result, err)
	}
}

func TestSimStoreEnforcesTheHashLikeTheRealNetwork(t *testing.T) {
	_, a, b := simPair(t, SimConfig{Retries: 3})
	value := []byte("valid")
	if err := a.network.SendStoreMessage(contactB(), hashData(value), value); err != nil || !holds(b, value) {
		t.Fatalf("valid STORE failed: %v", err)
	}
	if err := a.network.SendStoreMessage(contactB(), hashData([]byte("other")), value); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("mismatched STORE error = %v, want ErrHashMismatch", err)
	}
}

func TestSimRejectsMalformedRequests(t *testing.T) {
	_, a, _ := simPair(t, SimConfig{})
	if _, err := a.network.SendFindContactMessage(contactB(), nil); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("FIND_NODE without target: %v", err)
	}
	if _, err := a.network.SendFindDataMessage(contactB(), nil); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("FIND_VALUE without key: %v", err)
	}
	if err := a.network.SendStoreMessage(contactB(), nil, nil); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("STORE without key: %v", err)
	}
	if _, err := a.network.SendPingMessage(nil); err == nil {
		t.Fatal("PING without contact succeeded")
	}
	if _, err := a.network.SendPingMessage(&Contact{ID: testID(2)}); err == nil {
		t.Fatal("PING without address succeeded")
	}
}

func TestSimTotalLossFailsAfterAllAttempts(t *testing.T) {
	_, a, _ := simPair(t, SimConfig{PacketLoss: 1, Retries: 2})
	_, err := a.network.SendPingMessage(contactB())
	if !errors.Is(err, errSimPacketLost) {
		t.Fatalf("error = %v, want packet loss", err)
	}
	if want := "after 3 attempt(s)"; err == nil || !contains(err.Error(), want) {
		t.Fatalf("error %q should mention %q", err, want)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func pingOutcomes(a *Kademlia, count int) []bool {
	outcomes := make([]bool, count)
	for i := range outcomes {
		_, err := a.network.SendPingMessage(contactB())
		outcomes[i] = err == nil
	}
	return outcomes
}

func successRate(outcomes []bool) float64 {
	ok := 0
	for _, success := range outcomes {
		if success {
			ok++
		}
	}
	return float64(ok) / float64(len(outcomes))
}

func TestSimLossRateMatchesTheoryAndRetriesRecoverIt(t *testing.T) {
	// One attempt needs both legs to survive: (1-p)^2 = 0.25 for p = 0.5.
	_, a, _ := simPair(t, SimConfig{Seed: 11, PacketLoss: 0.5})
	if rate := successRate(pingOutcomes(a, 2000)); rate < 0.20 || rate > 0.30 {
		t.Fatalf("success rate without retries = %.3f, want about 0.25", rate)
	}
	// Four attempts: 1 - 0.75^4 = 0.684.
	_, a, _ = simPair(t, SimConfig{Seed: 11, PacketLoss: 0.5, Retries: 3})
	if rate := successRate(pingOutcomes(a, 2000)); rate < 0.63 || rate > 0.74 {
		t.Fatalf("success rate with 3 retries = %.3f, want about 0.684", rate)
	}
}

func TestSimIsExactlyRepeatableFromItsSeed(t *testing.T) {
	run := func(seed int64) []bool {
		_, a, _ := simPair(t, SimConfig{Seed: seed, PacketLoss: 0.5})
		return pingOutcomes(a, 200)
	}
	first, second, other := run(7), run(7), run(8)
	same := func(x, y []bool) bool {
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	}
	if !same(first, second) {
		t.Fatal("the same seed must produce the same losses")
	}
	if same(first, other) {
		t.Fatal("a different seed should produce different losses")
	}
}

func TestSimLostResponseStillRunsTheServer(t *testing.T) {
	// If only the response is lost, the request was delivered: the store
	// happened even though the caller sees an error.
	for seed := int64(0); seed < 500; seed++ {
		_, a, b := simPair(t, SimConfig{Seed: seed, PacketLoss: 0.5})
		value := []byte("lost ack")
		err := a.network.SendStoreMessage(contactB(), hashData(value), value)
		if err != nil && holds(b, value) {
			return
		}
	}
	t.Fatal("never observed a delivered request with a lost response")
}

func TestSimRemoveSimulatesACrash(t *testing.T) {
	network, a, _ := simPair(t, SimConfig{})
	if network.NodeCount() != 2 || network.Node("b") == nil {
		t.Fatal("setup")
	}
	if !network.Remove("b") || network.Remove("b") {
		t.Fatal("Remove should report whether the node existed")
	}
	if network.NodeCount() != 1 || network.Node("b") != nil {
		t.Fatal("node still registered")
	}
	if _, err := a.network.SendPingMessage(contactB()); err == nil {
		t.Fatal("ping to a removed node succeeded")
	}
}

func TestSimNewNodeValidation(t *testing.T) {
	network := NewSimNetwork(SimConfig{})
	if _, err := network.NewNode(Contact{Address: "x"}, 0); err == nil {
		t.Fatal("node without ID accepted")
	}
	if _, err := network.NewNode(NewContact(testID(1), ""), 0); err == nil {
		t.Fatal("node without address accepted")
	}
	if _, err := network.NewNode(NewContact(testID(1), "x"), 4); err != nil {
		t.Fatal(err)
	}
	if _, err := network.NewNode(NewContact(testID(2), "x"), 0); err == nil {
		t.Fatal("duplicate address accepted")
	}
	if node := network.Node("x"); node.routingTable.K() != 4 {
		t.Fatalf("k = %d, want the configured 4", node.routingTable.K())
	}
}

func TestSimConfigIsSanitizedAndAdjustable(t *testing.T) {
	network := NewSimNetwork(SimConfig{PacketLoss: 3, Retries: -1, MinLatency: 5 * time.Millisecond, MaxLatency: time.Millisecond})
	cfg := network.Config()
	if cfg.PacketLoss != 1 || cfg.Retries != 0 || cfg.MaxLatency != cfg.MinLatency {
		t.Fatalf("config not sanitized: %+v", cfg)
	}
	network.SetPacketLoss(-2)
	network.SetRetries(-5)
	if cfg := network.Config(); cfg.PacketLoss != 0 || cfg.Retries != 0 {
		t.Fatalf("setters not clamped: %+v", cfg)
	}
	network.SetPacketLoss(0.25)
	network.SetRetries(4)
	if cfg := network.Config(); cfg.PacketLoss != 0.25 || cfg.Retries != 4 {
		t.Fatalf("setters ignored: %+v", cfg)
	}
}

func TestSimDrawIsUniformAndDeterministic(t *testing.T) {
	if simDraw(1, "a", "b", 1, 0, 0, 0) != simDraw(1, "a", "b", 1, 0, 0, 0) {
		t.Fatal("simDraw is not deterministic")
	}
	sum, buckets := 0.0, make([]int, 10)
	const draws = 20000
	for i := 0; i < draws; i++ {
		v := simDraw(3, "from", "to", uint64(i), i%3, i%2, 0)
		if v < 0 || v >= 1 {
			t.Fatalf("draw %v out of range", v)
		}
		sum += v
		buckets[int(v*10)]++
	}
	if mean := sum / draws; mean < 0.49 || mean > 0.51 {
		t.Fatalf("mean = %.4f, want about 0.5", mean)
	}
	for i, n := range buckets {
		if n < draws/10*85/100 || n > draws/10*115/100 {
			t.Fatalf("bucket %d holds %d of %d draws; not uniform", i, n, draws)
		}
	}
}

func TestSimObserverSeesEveryCompletedRPC(t *testing.T) {
	network, a, b := simPair(t, SimConfig{})
	var mu sync.Mutex
	var events []RPCEvent
	network.SetObserver(func(e RPCEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	value := []byte("observed")
	_, _ = a.network.SendPingMessage(contactB())
	_, _ = a.network.SendFindContactMessage(contactB(), testID(5))
	_, _ = a.network.SendFindDataMessage(contactB(), hashData(value))
	_ = a.network.SendStoreMessage(contactB(), hashData(value), value)
	_ = a.network.SendStoreMessage(contactB(), hashData(value), []byte("wrong value")) // refused: hash mismatch
	network.Remove("b")
	_, _ = a.network.SendPingMessage(contactB()) // unreachable
	_ = b

	want := []RPCEvent{
		{"PING", "a", "b", true}, {"FIND_NODE", "a", "b", true}, {"FIND_VALUE", "a", "b", true},
		{"STORE", "a", "b", true}, {"STORE", "a", "b", false}, {"PING", "a", "b", false},
	}
	if len(events) != len(want) {
		t.Fatalf("observed %d events, want %d: %v", len(events), len(want), events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}
	network.SetObserver(nil)
	_, _ = a.network.SendPingMessage(contactB()) // must not panic without an observer
}

func TestSimSupportsConcurrentUse(t *testing.T) {
	network := NewSimNetwork(SimConfig{Seed: 2, PacketLoss: 0.2, Retries: 1})
	var nodes []*Kademlia
	for i := 1; i <= 12; i++ {
		node, err := network.NewNode(NewContact(testID(byte(i)), fmt.Sprintf("n%d", i)), 3)
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
	}
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func(i int, node *Kademlia) {
			defer wg.Done()
			bootstrap := NewContact(testID(byte((i+1)%12+1)), fmt.Sprintf("n%d", (i+1)%12+1))
			_ = node.Join(bootstrap)
			value := []byte(fmt.Sprintf("value-%d", i))
			_, _ = node.Store(value)
			_, _, _ = node.LookupData(hashData(value).String())
		}(i, node)
	}
	wg.Wait()
}

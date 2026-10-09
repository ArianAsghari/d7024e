package kademlia

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Each probe blocks until the test releases it. A sequential implementation
// cannot enter all probes in a batch, regardless of how quickly the CPU runs.
func TestLookupParallelismAndProbeLogs(t *testing.T) {
	for _, valueLookup := range []bool{false, true} {
		for _, alpha := range []int{1, 3, 20} {
			t.Run(fmt.Sprintf("value=%v/alpha=%d", valueLookup, alpha), func(t *testing.T) {
				const peers = 5
				entered := make(chan string, peers)
				release := make(chan struct{}, peers)
				defer close(release)
				var active, maximum atomic.Int32
				probe := func(contact *Contact) {
					n := active.Add(1)
					for old := maximum.Load(); n > old; old = maximum.Load() {
						if maximum.CompareAndSwap(old, n) {
							break
						}
					}
					entered <- contact.Address
					<-release
					active.Add(-1)
				}
				rpc := &maintenanceRPC{
					find: func(contact *Contact, _ *KademliaID) ([]Contact, error) {
						probe(contact)
						return nil, nil
					},
					data: func(contact *Contact, _ *KademliaID) (FindDataResult, error) {
						probe(contact)
						return FindDataResult{}, nil
					},
				}
				rt := NewRoutingTable(NewContact(testID(0), "me"), peers)
				for i := 1; i <= peers; i++ {
					rt.AddContact(NewContact(testID(byte(i)), fmt.Sprint(i)))
				}
				node := NewKademlia(rt, rpc)
				if node.Alpha() != DefaultAlpha {
					t.Fatal("wrong default alpha")
				}
				if err := node.SetAlpha(alpha); err != nil {
					t.Fatal(err)
				}
				var logs bytes.Buffer
				node.SetLookupLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
				done := make(chan struct{})
				go func() {
					defer close(done)
					if valueLookup {
						if _, _, err := node.LookupData(testID(1).String()); !errors.Is(err, ErrValueNotFound) {
							t.Errorf("missing value: %v", err)
						}
					} else {
						target := NewContact(testID(1), "")
						contacts, err := node.LookupContact(&target)
						if err != nil || len(contacts) != peers || !contacts[0].ID.Equals(target.ID) {
							t.Errorf("contacts = %v, error = %v", contacts, err)
						}
					}
				}()
				seen := make(map[string]bool)
				for remaining := peers; remaining > 0; {
					n := min(alpha, remaining)
					for i := 0; i < n; i++ {
						select {
						case address := <-entered:
							if seen[address] {
								t.Fatalf("peer %s queried twice", address)
							}
							seen[address] = true
						case <-time.After(3 * time.Second):
							t.Fatal("probes did not run concurrently")
						}
					}
					// An active lookup must keep its original alpha snapshot.
					_ = node.SetAlpha(1)
					for i := 0; i < n; i++ {
						release <- struct{}{}
					}
					remaining -= n
				}
				waitFor(t, done)
				if got := int(maximum.Load()); got != min(alpha, peers) || active.Load() != 0 {
					t.Fatalf("maximum active = %d, remaining = %d", got, active.Load())
				}
				decoder := json.NewDecoder(&logs)
				probes, completed := 0, 0
				for decoder.More() {
					var event map[string]any
					if err := decoder.Decode(&event); err != nil {
						t.Fatal(err)
					}
					if event["alpha"] != float64(alpha) || event["lookup_id"] != float64(1) || event["node_id"] != rt.me.ID.String() {
						t.Fatalf("incorrect lookup context: %v", event)
					}
					switch event["event"] {
					case "lookup_probe":
						probes++
					case "lookup_complete":
						completed++
						if event["probes"] != float64(peers) || event["rounds"] != float64((peers+alpha-1)/alpha) || event["success"] != !valueLookup || event["found_target"] != !valueLookup {
							t.Fatalf("incorrect outcome: %v", event)
						}
					}
				}
				if probes != peers || completed != 1 {
					t.Fatalf("logged %d probes and %d outcomes", probes, completed)
				}
			})
		}
	}
}

func TestLookupContactConvergesWithDuplicateReferralsAndFailures(t *testing.T) {
	rt := NewRoutingTable(NewContact(testID(0), "me"), 2)
	a, b := NewContact(testID(8), "a"), NewContact(testID(9), "b")
	dead, closest := NewContact(testID(1), "dead"), NewContact(testID(2), "closest")
	rt.AddContact(a)
	rt.AddContact(b)
	rpc := &fakeRPCClient{
		findContacts: map[string][]Contact{
			"a": {dead, closest, closest, a, rt.me, {}},
			"b": {dead, closest}, "closest": {dead, a, b},
		},
		requestError: map[string]error{"dead": errors.New("offline")},
	}
	node := NewKademlia(rt, rpc)
	contacts, err := node.LookupContact(&rt.me)
	if err != nil || len(contacts) != 2 || contacts[0].Address != "closest" || contacts[1].Address != "a" {
		t.Fatalf("contacts = %v, error = %v", contacts, err)
	}
	seen := make(map[string]bool)
	for _, address := range rpc.contactCalls {
		if seen[address] {
			t.Fatalf("queried %s twice", address)
		}
		seen[address] = true
	}
	if len(seen) != 4 {
		t.Fatalf("queried %v", seen)
	}
}

func TestLookupDataWaitsForRoundAndCachesValidValue(t *testing.T) {
	value := []byte("valid parallel result")
	key := hashData(value)
	rt := NewRoutingTable(NewContact(testID(0), "me"))
	for i := byte(1); i <= 3; i++ {
		rt.AddContact(NewContact(testID(i), fmt.Sprint(i)))
	}
	entered, returned := make(chan struct{}, 3), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	node := NewKademlia(rt, &maintenanceRPC{data: func(contact *Contact, _ *KademliaID) (FindDataResult, error) {
		entered <- struct{}{}
		if contact.Address == "1" {
			close(returned)
			return FindDataResult{Found: true, Value: value}, nil
		}
		<-release
		return FindDataResult{}, errors.New("timeout")
	}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		got, source, err := node.LookupData(key.String())
		if err != nil || !bytes.Equal(got, value) || source == nil || source.Address != "1" {
			t.Errorf("value = %q, source = %v, error = %v", got, source, err)
		}
	}()
	for i := 0; i < 3; i++ {
		waitFor(t, entered)
	}
	waitFor(t, returned)
	select {
	case <-done:
		t.Fatal("lookup left unfinished RPCs behind")
	default:
	}
	once.Do(func() { close(release) })
	waitFor(t, done)
	var logs bytes.Buffer
	node.SetLookupLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	got, source, err := node.LookupData(key.String())
	if err != nil || source != nil || !bytes.Equal(got, value) {
		t.Fatalf("cached value = %q, source = %v, error = %v", got, source, err)
	}
	var event map[string]any
	if err := json.Unmarshal(logs.Bytes(), &event); err != nil || event["success"] != true || event["local"] != true || event["probes"] != float64(0) {
		t.Fatalf("local lookup log = %s, error = %v", logs.String(), err)
	}
}

func TestLookupValidationAndFailuresAreLogged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rt     *RoutingTable
		rpc    RPCClient
		target *Contact
		want   error
	}{
		{"invalid target", nil, nil, nil, ErrInvalidTarget},
		{"missing table", nil, nil, &Contact{ID: testID(1)}, ErrNoRoutingTable},
		{"all offline", NewRoutingTable(NewContact(testID(0), "me")), &maintenanceRPC{find: func(*Contact, *KademliaID) ([]Contact, error) { return nil, errors.New("offline") }}, &Contact{ID: testID(1)}, ErrLookupFailed},
		{"missing network", NewRoutingTable(NewContact(testID(0), "me")), nil, &Contact{ID: testID(1)}, ErrNoNetwork},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.rt != nil {
				tc.rt.AddContact(NewContact(testID(1), "peer"))
			}
			node := NewKademlia(tc.rt, tc.rpc)
			var logs bytes.Buffer
			node.SetLookupLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
			if _, err := node.LookupContact(tc.target); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
			var event map[string]any
			if err := json.Unmarshal(lines[len(lines)-1], &event); err != nil || event["success"] != false || event["error"] == "" {
				t.Fatalf("missing failure outcome: %s", logs.String())
			}
		})
	}
	node := NewKademlia(nil, nil)
	for _, invalid := range []int{0, -1} {
		if err := node.SetAlpha(invalid); err == nil || node.Alpha() != DefaultAlpha {
			t.Fatal("invalid alpha changed the configuration")
		}
	}
}

func TestValueLookupContinuesBeyondInvalidClosestPeer(t *testing.T) {
	value := []byte("fallback past a dishonest peer")
	key := hashData(value)
	seed := NewContact(xorLastByte(key, 8), "seed")
	invalid := NewContact(xorLastByte(key, 1), "invalid")
	valid := NewContact(xorLastByte(key, 2), "valid")
	rt := NewRoutingTable(NewContact(xorLastByte(key, 16), "me"), 1)
	rt.AddContact(seed)
	rpc := &fakeRPCClient{findData: map[string]FindDataResult{
		"seed":    {Contacts: []Contact{invalid, valid}},
		"invalid": {Found: true, Value: []byte("tampered")},
		"valid":   {Found: true, Value: value},
	}}
	node := NewKademlia(rt, rpc)
	got, source, err := node.LookupData(key.String())
	if err != nil || !bytes.Equal(got, value) || source == nil || source.Address != "valid" {
		t.Fatalf("fallback = %q from %v, error = %v", got, source, err)
	}
	if len(rpc.dataCalls) != 3 {
		t.Fatalf("probed %v, want seed, invalid and valid", rpc.dataCalls)
	}
}

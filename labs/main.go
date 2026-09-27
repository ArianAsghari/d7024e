// Package main starts a single Kademlia node and its interactive CLI.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"d7024e/kademlia"
)

// getOutboundIP returns this container's non-loopback IPv4 address.
// This is a heuristic: it assumes a single relevant network interface,
// which holds for the simple Docker setups we use in this lab.
func getOutboundIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return "127.0.0.1"
}

// getPort reads the RPC port from the PORT env var, defaulting to 8000.
func getPort() int {
	if p := os.Getenv("PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	return 8000
}

// idFromAddress derives a KademliaID as hash(IP|port), per the lab spec.
func idFromAddress(address string) *kademlia.KademliaID {
	sum := sha256.Sum256([]byte(address))
	return kademlia.NewKademliaID(hex.EncodeToString(sum[:]))
}

func main() {
	ip := getOutboundIP()
	port := getPort()
	address := fmt.Sprintf("%s:%d", ip, port)

	id := idFromAddress(address)
	me := kademlia.NewContact(id, address)

	fmt.Printf("Starting Kademlia node %s\n", me.String())

	routingTable := kademlia.NewRoutingTable(me)
	network := kademlia.NewNetwork(me)
	node := kademlia.NewKademlia(routingTable, network)
	network.AttachKademlia(node)
	if err := network.Listen(ip, port); err != nil {
		panic(err)
	}
	defer network.Close()

	runCLI(os.Stdin, os.Stdout, routingTable, node, network)
}

// runCLI reads newline-terminated commands from in and writes prompts/output
// to out, until "exit" is received or in reaches EOF. Kept independent of
// os.Stdin/os.Stdout so it can be driven by an in-memory reader/writer in tests.
func runCLI(in io.Reader, out io.Writer, routingTable *kademlia.RoutingTable, node *kademlia.Kademlia, network *kademlia.Network) {
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "> ")
		if !scanner.Scan() {
			return
		}
		cmd, args := parseCommand(scanner.Text())
		if cmd == "" {
			continue
		}
		if !dispatchCommand(out, routingTable, node, network, cmd, args) {
			return
		}
	}
}

// parseCommand splits a raw input line into a lowercase command word and its
// remaining whitespace-separated arguments.
func parseCommand(line string) (string, []string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	return strings.ToLower(fields[0]), fields[1:]
}

// dispatchCommand runs a single parsed command against node/network. It
// returns false when the CLI loop should stop (the "exit" command).
func dispatchCommand(out io.Writer, routingTable *kademlia.RoutingTable, node *kademlia.Kademlia, network *kademlia.Network, cmd string, args []string) bool {
	switch cmd {
	case "exit":
		fmt.Fprintln(out, "bye")
		return false

	case "ping":
		if len(args) != 1 {
			fmt.Fprintln(out, "usage: ping IP:PORT")
			return true
		}
		target := kademlia.NewContact(idFromAddress(args[0]), args[0])
		rtt, err := network.SendPingMessage(&target)
		if err != nil {
			fmt.Fprintf(out, "ping failed: %v\n", err)
			return true
		}
		fmt.Fprintf(out, "pong from %s in %s\n", args[0], rtt)

	case "put":
		if len(args) != 1 {
			fmt.Fprintln(out, "usage: put FILENAME")
			return true
		}
		data, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Fprintf(out, "put failed: %v\n", err)
			return true
		}
		key, storeErr := node.Store(data)
		if storeErr != nil {
			fmt.Fprintf(out, "stored as %s (with warnings: %v)\n", key, storeErr)
		} else {
			fmt.Fprintf(out, "stored as %s\n", key)
		}

	case "get":
		if len(args) < 1 || len(args) > 2 {
			fmt.Fprintln(out, "usage: get KEY [FILENAME]")
			return true
		}
		value, source, err := node.LookupData(args[0])
		if err != nil {
			fmt.Fprintf(out, "get failed: %v\n", err)
			return true
		}
		if len(args) == 2 {
			if err := os.WriteFile(args[1], value, 0o644); err != nil {
				fmt.Fprintf(out, "get failed: %v\n", err)
				return true
			}
			fmt.Fprintf(out, "wrote %d bytes to %s\n", len(value), args[1])
		} else {
			fmt.Fprintf(out, "%s\n", value)
		}
		if source != nil {
			fmt.Fprintf(out, "(found on %s)\n", source.Address)
		} else {
			fmt.Fprintln(out, "(found locally)")
		}

	case "show":
		if len(args) != 1 {
			fmt.Fprintln(out, "usage: show rt|ds")
			return true
		}
		switch args[0] {
		case "rt":
			fmt.Fprint(out, formatRoutingTable(routingTable.Me(), routingTable.Buckets()))
		case "ds":
			fmt.Fprint(out, formatDataStore(node.Entries()))
		default:
			fmt.Fprintln(out, "usage: show rt|ds")
		}

	default:
		fmt.Fprintf(out, "unknown command %q\n", cmd)
	}
	return true
}

// formatRoutingTable renders a bucket-by-bucket view of the routing table
// for the "show rt" command: self contact first, then each non-empty bucket
// with its members, IDs truncated for readability.
func formatRoutingTable(me kademlia.Contact, buckets []kademlia.RoutingTableBucket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "self: %s %s\n", truncateID(me.ID.String()), me.Address)
	if len(buckets) == 0 {
		fmt.Fprintln(&b, "(no contacts yet)")
		return b.String()
	}
	for _, bucket := range buckets {
		fmt.Fprintf(&b, "bucket %d:\n", bucket.Index)
		for _, contact := range bucket.Contacts {
			fmt.Fprintf(&b, "  %s %s\n", truncateID(contact.ID.String()), contact.Address)
		}
	}
	return b.String()
}

// formatDataStore renders the local key-value store for the "show ds" command.
func formatDataStore(entries []kademlia.DataEntry) string {
	if len(entries) == 0 {
		return "(empty)\n"
	}
	var b strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&b, "%s (%d bytes)\n", truncateID(entry.Key), len(entry.Value))
	}
	return b.String()
}

// truncateID shortens a 64-character hex ID for display (first 8, last 4).
func truncateID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:8] + "\u2026" + id[len(id)-4:]
}

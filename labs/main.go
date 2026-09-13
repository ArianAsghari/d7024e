// Package main starts a single Kademlia node.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

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

	// kademlia.Listen is currently a no-op stub (network.go). Once it's
	// implemented to actually receive UDP RPCs, this call will block (or
	// spawn its own goroutine) as appropriate. The sleep loop below just
	// keeps the container alive in the meantime so it doesn't exit.
	kademlia.Listen(ip, port)

	for {
		time.Sleep(time.Hour)
	}
}

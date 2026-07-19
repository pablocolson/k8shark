// Package capture provides live packet sources. Real capture is Linux-only
// (AF_PACKET, no libpcap/cgo dependency); other platforms return an error so
// the worker transparently falls back to demo mode.
package capture

import "github.com/google/gopacket"

// RingStats reports cumulative AF_PACKET ring counters: how many packets the
// kernel delivered to this socket, and how many it dropped because the ring
// filled up before userspace drained it. That drop is the most common source
// of capture loss on a busy node and is otherwise invisible from userspace —
// WorkerStats.Dropped only counts the downstream sink buffer, well after
// these packets would already be gone.
type RingStats struct {
	Packets uint64
	Drops   uint64
}

// PacketSource yields captured packets and can be closed.
type PacketSource interface {
	Packets() <-chan gopacket.Packet
	// Stats returns cumulative ring counters, or ok=false if unavailable.
	Stats() (RingStats, bool)
	Close() error
}

// Ports configures which TCP/UDP ports the kernel-level BPF filter accepts,
// in addition to ICMP/ICMPv6 (always accepted regardless of these lists —
// see buildL7Filter). This is what makes operator-configured ports
// (--redis-ports, --valkey-ports, --amqp-ports, --http-ports, ...) actually
// reach the kernel filter, not just userspace protocol dispatch.
type Ports struct {
	TCP []int
	UDP []int
}

// NewLive opens a live capture on the given interface ("" = auto/any). Only
// implemented on Linux; elsewhere it returns ErrUnsupported.
func NewLive(iface string, snaplen int, ports Ports) (PacketSource, error) {
	return newLive(iface, snaplen, ports)
}

// Package capture provides live packet sources. Real capture is Linux-only
// (AF_PACKET, no libpcap/cgo dependency); other platforms return an error so
// the worker transparently falls back to demo mode.
package capture

import "github.com/google/gopacket"

// PacketSource yields captured packets and can be closed.
type PacketSource interface {
	Packets() <-chan gopacket.Packet
	Close() error
}

// NewLive opens a live capture on the given interface ("" = auto/any). Only
// implemented on Linux; elsewhere it returns ErrUnsupported.
func NewLive(iface string, snaplen int) (PacketSource, error) {
	return newLive(iface, snaplen)
}

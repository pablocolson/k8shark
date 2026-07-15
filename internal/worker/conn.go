package worker

import (
	"strconv"

	"github.com/google/gopacket"
	"github.com/pablocolson/k8shark/pkg/api"
)

// connID identifies one direction's TCP connection independent of how it was
// discovered. AF_PACKET reports it as a pair of gopacket flows (real IPs and
// ports); the eBPF TLS uprobe path (see tls_pipeline.go) reports it as a
// synthetic pid/tid-keyed identity (Phase 2a has no in-kernel 4-tuple
// resolution yet — that is Phase 2b). Both feed the same connID-keyed
// dissectors so decrypted TLS streams pair through the exact same
// p.conns/p.flows state as plaintext AF_PACKET streams.
type connID struct {
	srcIP   string
	dstIP   string
	srcPort int
	dstPort int
}

// key returns the canonical connection key. It MUST order identically to the
// existing connKey(netFlow, transport) so entries reaching the pipeline via
// different sources (AF_PACKET vs. eBPF) still join the same FIFO pairing
// state when they happen to share a key.
func (c connID) key() string {
	a := c.srcIP + ":" + strconv.Itoa(c.srcPort)
	b := c.dstIP + ":" + strconv.Itoa(c.dstPort)
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

// endpoints returns the (src, dst) api.Endpoint pair, unresolved (Name/
// Namespace are filled in later by hub-side enrichment, same as the
// AF_PACKET path today).
func (c connID) endpoints() (src, dst api.Endpoint) {
	return api.Endpoint{IP: c.srcIP, Port: c.srcPort}, api.Endpoint{IP: c.dstIP, Port: c.dstPort}
}

// connIDFromFlows builds a connID from the gopacket flows AF_PACKET's TCP
// reassembler hands the pipeline.
func connIDFromFlows(netFlow, transport gopacket.Flow) connID {
	return connID{
		srcIP:   netFlow.Src().String(),
		dstIP:   netFlow.Dst().String(),
		srcPort: portOf(transport.Src().String()),
		dstPort: portOf(transport.Dst().String()),
	}
}

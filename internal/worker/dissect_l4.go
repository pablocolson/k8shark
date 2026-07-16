package worker

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/pablocolson/k8shark/pkg/api"
)

// flowState accumulates L4 metadata for one connection/flow that is not L7
// dissected. TCP flows are emitted on close (FIN/RST) or idle timeout; UDP flows
// on idle timeout.
type flowState struct {
	proto     api.Protocol
	src, dst  api.Endpoint
	firstSeen time.Time
	lastSeen  time.Time
	packets   int64
	bytes     int64
	synSeen   bool
	finSeen   bool
	rstSeen   bool
	l7        bool // dissected by an L7 parser -> never emit a generic flow
	emitted   bool // already emitted on close -> don't re-emit

	// L3 header (copied once from the first packet's l4meta)
	srcMAC, dstMAC string
	ipVersion      int
	ttl            int
	ipFlags        string
	headerHex      string

	// TCP handshake samples & per-direction accounting
	clientFlags, serverFlags     flagSet
	seqStart, ackStart           uint32
	window, mss                  int
	synTS, synAckTS              time.Time
	retransmits                  int
	nextSeqClient, nextSeqServer uint32 // expected next seq per direction
	haveSeqClient, haveSeqServer bool
	clientBytes, serverBytes     int64
	clientPackets, serverPackets int64

	// rawBuf samples this flow's payload bytes (either direction, up to
	// rawCap total — there's no clean request/response split to preserve
	// separately for an undissected flow) for the Raw tab. rawTotal tracks
	// how much payload was actually seen, for the truncated flag.
	rawBuf   []byte
	rawTotal int
}

// rawView renders the flow's sampled payload as a RawView, or nil if none
// was captured (raw capture disabled, or a flow with no payload bytes, e.g.
// a bare SYN/FIN handshake).
func (f *flowState) rawView() *api.RawView {
	if len(f.rawBuf) == 0 {
		return nil
	}
	return &api.RawView{
		Hex:       hexDump(f.rawBuf, len(f.rawBuf)),
		Bytes:     f.rawTotal,
		Truncated: f.rawTotal > len(f.rawBuf),
	}
}

// captureRaw appends up to rawCap total bytes of payload into f.rawBuf,
// tracking the true total in rawTotal regardless of the cap. No-op once
// disabled (rawCap < 0) or the cap is already reached.
func (f *flowState) captureRaw(payload []byte, rawCap int) {
	if len(payload) == 0 || rawCap < 0 {
		return
	}
	f.rawTotal += len(payload)
	if room := rawCap - len(f.rawBuf); room > 0 {
		take := len(payload)
		if take > room {
			take = room
		}
		f.rawBuf = append(f.rawBuf, payload[:take]...)
	}
}

func (f *flowState) flagStr() string {
	var fl []string
	if f.synSeen {
		fl = append(fl, "SYN")
	}
	if f.finSeen {
		fl = append(fl, "FIN")
	}
	if f.rstSeen {
		fl = append(fl, "RST")
	}
	return strings.Join(fl, ",")
}

// newFlow initialises a flow, orienting src->dst as client->server using the
// heuristic that the ephemeral (higher) port is the client.
func newFlow(proto api.Protocol, netFlow, transport gopacket.Flow, ts time.Time) *flowState {
	sp := portOf(transport.Src().String())
	dp := portOf(transport.Dst().String())
	var src, dst api.Endpoint
	if sp >= dp {
		src = api.Endpoint{IP: netFlow.Src().String(), Port: sp}
		dst = api.Endpoint{IP: netFlow.Dst().String(), Port: dp}
	} else {
		src = api.Endpoint{IP: netFlow.Dst().String(), Port: dp}
		dst = api.Endpoint{IP: netFlow.Src().String(), Port: sp}
	}
	return &flowState{proto: proto, src: src, dst: dst, firstSeen: ts, lastSeen: ts}
}

// markL7 flags a connection as L7-dissected so trackTCP won't emit a duplicate
// generic flow for it.
func (p *pipeline) markL7(key string) {
	p.flowMu.Lock()
	if f := p.flows[key]; f != nil {
		f.l7 = true
	} else {
		p.flows[key] = &flowState{l7: true}
	}
	p.flowMu.Unlock()
}

// trackTCP accounts one TCP packet, enriches the flow's L4 metadata and emits
// the flow when it closes.
func (p *pipeline) trackTCP(netFlow, transport gopacket.Flow, tcp *layers.TCP, length int, ts time.Time, meta l4meta) {
	key := connKey(netFlow, transport)
	var closed *flowState

	p.flowMu.Lock()
	f := p.flows[key]
	if f == nil {
		f = newFlow(api.ProtocolTCP, netFlow, transport, ts)
		p.flows[key] = f
	} else if f.firstSeen.IsZero() {
		// Was created by markL7 before any packet; fill in orientation.
		nf := newFlow(api.ProtocolTCP, netFlow, transport, ts)
		f.src, f.dst, f.firstSeen = nf.src, nf.dst, ts
	}
	f.lastSeen = ts
	f.packets++
	f.bytes += int64(length)
	f.captureRaw(tcp.Payload, p.rawCap)

	// Copy the L3 header fields once (from whichever direction is seen first).
	if f.srcMAC == "" && meta.srcMAC != "" {
		f.srcMAC, f.dstMAC = meta.srcMAC, meta.dstMAC
	}
	if f.ipVersion == 0 && meta.ipVersion != 0 {
		f.ipVersion, f.ttl, f.ipFlags = meta.ipVersion, meta.ttl, meta.ipFlags
	}
	if f.headerHex == "" && meta.headerHex != "" {
		f.headerHex = meta.headerHex
	}

	// Per-direction accounting. f.src is oriented client->server by newFlow, so
	// a packet whose (netFlow.Src, transport.Src) equals f.src is client->server.
	clientToServer := netFlow.Src().String() == f.src.IP && portOf(transport.Src().String()) == f.src.Port
	payloadLen := len(tcp.Payload)
	if clientToServer {
		f.clientBytes += int64(length)
		f.clientPackets++
		f.clientFlags |= tcpFlagSet(tcp)
		f.window = int(tcp.Window)
		if payloadLen > 0 {
			if f.haveSeqClient && tcp.Seq < f.nextSeqClient {
				f.retransmits++
			} else {
				f.nextSeqClient = tcp.Seq + uint32(payloadLen)
				f.haveSeqClient = true
			}
		}
		if tcp.SYN && !tcp.ACK {
			f.seqStart = tcp.Seq
			f.synTS = ts
			f.mss = parseMSS(tcp.Options)
			f.window = int(tcp.Window)
		}
	} else {
		f.serverBytes += int64(length)
		f.serverPackets++
		f.serverFlags |= tcpFlagSet(tcp)
		if payloadLen > 0 {
			if f.haveSeqServer && tcp.Seq < f.nextSeqServer {
				f.retransmits++
			} else {
				f.nextSeqServer = tcp.Seq + uint32(payloadLen)
				f.haveSeqServer = true
			}
		}
		if tcp.SYN && tcp.ACK {
			f.ackStart = tcp.Seq
			f.synAckTS = ts
		}
	}

	if tcp.SYN {
		f.synSeen = true
	}
	if tcp.FIN {
		f.finSeen = true
	}
	if tcp.RST {
		f.rstSeen = true
	}
	if (f.finSeen || f.rstSeen) && !f.emitted && !f.l7 {
		f.emitted = true
		cp := *f
		closed = &cp
	}
	p.flowMu.Unlock()

	if closed != nil {
		reason := "FIN"
		if closed.rstSeen {
			reason = "RST"
		}
		p.emitFlow(closed, reason)
	}
}

// parseMSS returns the MSS advertised in a SYN's TCP options, or 0.
func parseMSS(opts []layers.TCPOption) int {
	for _, o := range opts {
		if o.OptionType == layers.TCPOptionKindMSS && len(o.OptionData) == 2 {
			return int(o.OptionData[0])<<8 | int(o.OptionData[1])
		}
	}
	return 0
}

// buildL4Info renders a flowState copy into the wire L4Info. Returns nil if the
// flow never saw a packet (only a markL7 placeholder).
func buildL4Info(f *flowState) *api.L4Info {
	if f.firstSeen.IsZero() {
		return nil
	}
	info := &api.L4Info{
		SrcMAC: f.srcMAC, DstMAC: f.dstMAC, IPVersion: f.ipVersion,
		TTL: f.ttl, IPFlags: f.ipFlags,
		ClientTCPFlags: f.clientFlags.String(), ServerTCPFlags: f.serverFlags.String(),
		SeqStart: f.seqStart, AckStart: f.ackStart, Window: f.window, MSS: f.mss,
		Retransmits: f.retransmits, DurationMs: f.lastSeen.Sub(f.firstSeen).Milliseconds(),
		ClientBytes: f.clientBytes, ServerBytes: f.serverBytes,
		ClientPackets: f.clientPackets, ServerPackets: f.serverPackets,
		HeaderHex: f.headerHex,
	}
	if !f.synTS.IsZero() && !f.synAckTS.IsZero() {
		info.RTTMs = float64(f.synAckTS.Sub(f.synTS).Microseconds()) / 1000.0
	}
	return info
}

// snapshotL4 returns a copy of the L4Info for a connection, or nil if unknown.
// It takes only flowMu (never nested inside p.mu) to avoid lock-order deadlock.
func (p *pipeline) snapshotL4(key string) *api.L4Info {
	p.flowMu.Lock()
	f := p.flows[key]
	if f == nil || f.firstSeen.IsZero() {
		p.flowMu.Unlock()
		return nil
	}
	cp := *f
	p.flowMu.Unlock()
	return buildL4Info(&cp)
}

// trackUDP accounts one non-DNS UDP packet. UDP has no close signal, so these
// flows are emitted only by flushFlows on idle.
func (p *pipeline) trackUDP(netFlow, transport gopacket.Flow, length int, ts time.Time, payload []byte) {
	key := connKey(netFlow, transport)
	p.flowMu.Lock()
	f := p.flows[key]
	if f == nil {
		f = newFlow(api.ProtocolUDP, netFlow, transport, ts)
		p.flows[key] = f
	}
	f.lastSeen = ts
	f.packets++
	f.bytes += int64(length)
	f.captureRaw(payload, p.rawCap)
	p.flowMu.Unlock()
}

// handleICMP emits one entry per ICMP packet (echo, unreachable, etc.).
func (p *pipeline) handleICMP(netFlow gopacket.Flow, icmp *layers.ICMPv4, length int, ts time.Time) {
	desc, status := icmpDesc(icmp.TypeCode)
	p.sink.emit(&api.Entry{
		ID:          p.node + "-icmp-" + strconv.FormatUint(p.seq.Add(1), 36),
		Protocol:    api.ProtocolICMP,
		Timestamp:   ts,
		Node:        p.node,
		Source:      api.Endpoint{IP: netFlow.Src().String()},
		Destination: api.Endpoint{IP: netFlow.Dst().String()},
		Request:     api.Payload{Summary: desc, Bytes: int64(length), Raw: rawViewFromBytes(icmp.Payload, p.rawCap)},
		Response:    api.Payload{Summary: desc},
		Status:      status,
	})
}

// flushFlows emits flows idle longer than the timeout and reaps closed/L7 ones.
func (p *pipeline) flushFlows(idle time.Duration) {
	now := time.Now()
	var toEmit []*flowState
	p.flowMu.Lock()
	for k, f := range p.flows {
		if f.emitted {
			delete(p.flows, k)
			continue
		}
		if now.Sub(f.lastSeen) > idle {
			if !f.l7 && !f.firstSeen.IsZero() {
				cp := *f
				toEmit = append(toEmit, &cp)
			}
			delete(p.flows, k)
		}
	}
	p.flowMu.Unlock()
	for _, f := range toEmit {
		p.emitFlow(f, "idle")
	}
}

func (p *pipeline) emitFlow(f *flowState, reason string) {
	status := "success"
	if f.rstSeen {
		status = "error"
	}
	p.sink.emit(&api.Entry{
		ID:          p.node + "-l4-" + strconv.FormatUint(p.seq.Add(1), 36),
		Protocol:    f.proto,
		Timestamp:   f.firstSeen,
		ElapsedMs:   f.lastSeen.Sub(f.firstSeen).Milliseconds(),
		Node:        p.node,
		Source:      f.src,
		Destination: f.dst,
		Request:     api.Payload{Summary: flowLabel(f), Packets: f.packets, Bytes: f.bytes, Flags: f.flagStr(), Raw: f.rawView()},
		Response:    api.Payload{Summary: reason + " · " + humanBytes(f.bytes) + " · " + strconv.FormatInt(f.packets, 10) + " pkts"},
		Status:      status,
		L4:          buildL4Info(f),
	})
}

// --- helpers ----------------------------------------------------------------

var wellKnownPorts = map[int]string{
	22: "ssh", 25: "smtp", 110: "pop3", 143: "imap", 443: "https", 587: "smtp",
	853: "dns-tls", 1433: "mssql", 3306: "mysql", 3389: "rdp", 5432: "postgres",
	5672: "amqp", 6379: "redis", 8883: "mqtt", 9042: "cassandra", 9092: "kafka",
	11211: "memcached", 27017: "mongodb", 2379: "etcd", 2181: "zookeeper",
}

func flowLabel(f *flowState) string {
	if name := wellKnownPorts[f.dst.Port]; name != "" {
		return string(f.proto) + " " + name
	}
	return string(f.proto) + "/" + strconv.Itoa(f.dst.Port)
}

func icmpDesc(tc layers.ICMPv4TypeCode) (string, string) {
	switch tc.Type() {
	case layers.ICMPv4TypeDestinationUnreachable, layers.ICMPv4TypeTimeExceeded:
		return tc.String(), "error"
	case layers.ICMPv4TypeSourceQuench, layers.ICMPv4TypeRedirect:
		return tc.String(), "warning"
	default:
		return tc.String(), "success"
	}
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return strconv.FormatFloat(float64(b)/(1<<20), 'f', 1, 64) + " MB"
	case b >= 1<<10:
		return strconv.FormatFloat(float64(b)/(1<<10), 'f', 1, 64) + " KB"
	default:
		return strconv.FormatInt(b, 10) + " B"
	}
}

// Package worker runs on every node. It captures packets (AF_PACKET on Linux),
// reassembles TCP streams, dissects L7 protocols and ships paired entries to the
// hub. When live capture is unavailable (non-Linux, or no privileges) it falls
// back to a synthetic demo feed so the dashboard is always populated.
package worker

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/tcpassembly"
	"github.com/pablocolson/k8shark/internal/worker/capture"
	"github.com/pablocolson/k8shark/pkg/api"
)

// Options configures a worker run.
type Options struct {
	HubURL  string // ws:// URL of the hub worker endpoint
	Node    string // node name this worker reports as
	Iface   string // capture interface ("" = any)
	Demo    bool   // force synthetic traffic instead of live capture
	DemoRPS int    // synthetic entries/sec in demo mode

	// RESP (Redis wire protocol) port labelling. Valkey and Redis are wire
	// identical, so the label is operator-supplied config, not wire-detected.
	// Default (both empty) is port 6379 -> redis, matching prior behavior.
	RedisPorts  []int // extra RESP ports labelled "redis" (in addition to the 6379 default)
	ValkeyPorts []int // RESP ports labelled "valkey"
	AMQPPorts   []int // extra AMQP 0-9-1 ports (in addition to the 5672 default)

	// Capture-depth bounds (all per-direction). Defaults preserve prior behavior.
	CaptureBodies bool // capture & render request/response bodies (default true)
	BodyBytes     int  // per-direction body cap (0 => DefaultBodyCaptureBytes)
	RawBytes      int  // per-direction raw hex cap (0 => DefaultRawCaptureBytes; <0 disables raw)

	// eBPF TLS uprobe capture (hybrid layer, additive to AF_PACKET). Off by
	// default: AF_PACKET alone is unaffected either way. Linux-only; a
	// non-Linux worker logs a warning and continues on AF_PACKET alone.
	EnableTLS   bool   // attach uprobes to OpenSSL/boringssl libssl/libcrypto
	EnableGoTLS bool   // Phase 2b, not yet implemented — logs a warning if set
	ProcRoot    string // "" => "/proc"; use "/host/proc" when hostPID mounts proc elsewhere
}

// Run starts the worker and blocks until ctx is cancelled.
func Run(ctx context.Context, log *slog.Logger, opts Options) error {
	if opts.DemoRPS <= 0 {
		opts.DemoRPS = 25
	}
	s := newSink(opts.HubURL, opts.Node, log)
	go s.run()

	stop := ctx.Done()

	if opts.Demo {
		log.Info("worker started (demo mode)", "node", opts.Node, "rps", opts.DemoRPS)
		runDemo(s, opts.Node, opts.DemoRPS, stop)
		return nil
	}

	// AF_PACKET and eBPF TLS both feed one pipeline and are independent: either
	// can be unavailable without disabling the other. Build the pipeline first.
	p := newPipeline(s, opts.Node, log)
	if len(opts.RedisPorts) > 0 || len(opts.ValkeyPorts) > 0 {
		p.respPorts = buildRespPorts(opts.RedisPorts, opts.ValkeyPorts)
	}
	if len(opts.AMQPPorts) > 0 {
		p.amqpPorts = buildAMQPPorts(opts.AMQPPorts)
	}
	p.applyCaptureOpts(opts)

	// AF_PACKET (plaintext L3/L4/L7) — best effort. A failure here does NOT fall
	// back to synthetic traffic: demo is opt-in (--demo) only, so a broken
	// capture surfaces loudly instead of masquerading as realistic data (which
	// once hid a non-root worker silently emitting a fake "shop" namespace).
	var src capture.PacketSource
	if live, err := capture.NewLive(opts.Iface, 65536); err != nil {
		log.Error("AF_PACKET capture unavailable", "err", err,
			"hint", "worker likely not root or missing NET_RAW; pass --demo for synthetic traffic")
	} else {
		src = live
		defer src.Close()
		log.Info("AF_PACKET capture started", "node", opts.Node, "iface", opts.Iface)
	}

	// eBPF TLS (decrypted plaintext) — independent of AF_PACKET, so it still
	// starts even when AF_PACKET above failed. Feeds the same pipeline p.
	tlsUp := false
	if opts.EnableTLS {
		tlsUp = startTLSCapture(ctx, log, p, opts)
	}

	if src == nil && !tlsUp {
		log.Error("no capture source available — worker idle, no traffic will be reported",
			"hint", "check privileges (root, NET_RAW, BPF) or pass --demo for synthetic traffic")
	}

	return captureLoop(ctx, log, p, src)
}

// captureLoop drives packet consumption plus the periodic flush/gc tickers.
// src may be nil (AF_PACKET unavailable): the gc/flush loop still runs so an
// eBPF-TLS-only worker prunes its pipeline state, and it blocks until ctx is
// cancelled rather than exiting.
func captureLoop(ctx context.Context, log *slog.Logger, p *pipeline, src capture.PacketSource) error {
	var (
		assembler *tcpassembly.Assembler
		packets   <-chan gopacket.Packet
	)
	if src != nil {
		assembler = tcpassembly.NewAssembler(tcpassembly.NewStreamPool(&tcpStreamFactory{p: p}))
		packets = src.Packets()
	}

	flush := time.NewTicker(30 * time.Second)
	defer flush.Stop()
	gc := time.NewTicker(15 * time.Second)
	defer gc.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-flush.C:
			if assembler != nil {
				assembler.FlushOlderThan(time.Now().Add(-2 * time.Minute))
			}
		case <-gc.C:
			p.gc()
			p.flushFlows(20 * time.Second)
		case pkt, ok := <-packets:
			if !ok {
				// AF_PACKET stream ended; stop selecting on it but keep the gc
				// loop alive for any eBPF TLS capture still feeding the pipeline.
				packets = nil
				continue
			}
			p.route(assembler, pkt)
		}
	}
}

// buildRespPorts merges the default RESP port (6379 -> redis) with operator
// overrides. RedisPorts entries are applied first, then ValkeyPorts, so a port
// listed in both wins as "valkey".
func buildRespPorts(redisPorts, valkeyPorts []int) map[int]api.Protocol {
	respPorts := map[int]api.Protocol{redisPort: api.ProtocolRedis}
	for _, port := range redisPorts {
		respPorts[port] = api.ProtocolRedis
	}
	for _, port := range valkeyPorts {
		respPorts[port] = api.ProtocolValkey
	}
	return respPorts
}

// buildAMQPPorts merges the default AMQP port (5672) with operator-supplied
// extra ports.
func buildAMQPPorts(extra []int) map[int]bool {
	ports := map[int]bool{amqpPort: true}
	for _, port := range extra {
		ports[port] = true
	}
	return ports
}

// route dispatches a packet: TCP goes to the L7 assembler and the L4 flow
// tracker; UDP goes to the DNS handler or the L4 flow tracker; ICMP is emitted
// per-packet. Anything L7-dissected is flagged so it isn't double-counted as a
// generic flow.
func (p *pipeline) route(assembler *tcpassembly.Assembler, pkt gopacket.Packet) {
	net := pkt.NetworkLayer()
	if net == nil {
		return
	}
	ts := pkt.Metadata().Timestamp
	length := pkt.Metadata().Length
	if length == 0 {
		length = len(pkt.Data())
	}

	if tl := pkt.Layer(layers.LayerTypeTCP); tl != nil {
		tcp, _ := tl.(*layers.TCP)
		meta := extractL4Meta(pkt, p.headerHexCap)
		assembler.AssembleWithTimestamp(net.NetworkFlow(), tcp, ts)
		p.trackTCP(net.NetworkFlow(), tcp.TransportFlow(), tcp, length, ts, meta)
		return
	}
	if ul := pkt.Layer(layers.LayerTypeUDP); ul != nil {
		udp, _ := ul.(*layers.UDP)
		if dl := pkt.Layer(layers.LayerTypeDNS); dl != nil {
			dns, _ := dl.(*layers.DNS)
			p.handleDNS(net.NetworkFlow(), udp, dns)
			return
		}
		p.trackUDP(net.NetworkFlow(), udp.TransportFlow(), length, ts)
		return
	}
	if il := pkt.Layer(layers.LayerTypeICMPv4); il != nil {
		icmp, _ := il.(*layers.ICMPv4)
		p.handleICMP(net.NetworkFlow(), icmp, length, ts)
	}
}

// l4meta is the per-packet L3/L4 header data trackTCP needs to build L4Info.
// It is extracted in route() while the raw packet layers are still available
// (the reassembled L7 stream the dissectors see has already lost them).
type l4meta struct {
	srcMAC, dstMAC string
	ipVersion      int
	ttl            int
	ipFlags        string
	headerHex      string // bounded hexdump of eth+ip+tcp header bytes
}

// extractL4Meta reads the Ethernet/IP header fields from a packet and builds a
// bounded header hexdump (capped at capBytes; <=0 skips the dump).
func extractL4Meta(pkt gopacket.Packet, capBytes int) l4meta {
	var m l4meta
	var hdr []byte
	if eth, ok := pkt.Layer(layers.LayerTypeEthernet).(*layers.Ethernet); ok {
		m.srcMAC = eth.SrcMAC.String()
		m.dstMAC = eth.DstMAC.String()
		hdr = append(hdr, eth.LayerContents()...)
	}
	if ip4, ok := pkt.Layer(layers.LayerTypeIPv4).(*layers.IPv4); ok {
		m.ipVersion = 4
		m.ttl = int(ip4.TTL)
		m.ipFlags = ipv4Flags(ip4.Flags)
		hdr = append(hdr, ip4.LayerContents()...)
	} else if ip6, ok := pkt.Layer(layers.LayerTypeIPv6).(*layers.IPv6); ok {
		m.ipVersion = 6
		m.ttl = int(ip6.HopLimit)
		hdr = append(hdr, ip6.LayerContents()...)
	}
	if tcp, ok := pkt.Layer(layers.LayerTypeTCP).(*layers.TCP); ok {
		hdr = append(hdr, tcp.LayerContents()...)
	}
	if capBytes > 0 && len(hdr) > 0 {
		m.headerHex = hexDump(hdr, capBytes)
	}
	return m
}

// ipv4Flags renders the DF/MF fragment flags.
func ipv4Flags(f layers.IPv4Flag) string {
	var out []string
	if f&layers.IPv4DontFragment != 0 {
		out = append(out, "DF")
	}
	if f&layers.IPv4MoreFragments != 0 {
		out = append(out, "MF")
	}
	return strings.Join(out, ",")
}

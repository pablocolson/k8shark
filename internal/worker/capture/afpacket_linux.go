//go:build linux

package capture

import (
	"os"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
	"github.com/google/gopacket/layers"
)

// liveSource wraps an AF_PACKET ring and adapts it to a gopacket.PacketSource.
type liveSource struct {
	tp  *afpacket.TPacket
	src *gopacket.PacketSource
}

// afpacketComputeSize picks a TPACKET_V3 ring geometry sized to ~targetMB,
// with frameSize large enough for one (possibly GRO-coalesced) packet at the
// given snaplen. Standard gopacket helper; frameSize must divide blockSize
// and blockSize must be a multiple of the page size.
func afpacketComputeSize(targetMB, snaplen, pageSize int) (frameSize, blockSize, numBlocks int) {
	if snaplen < pageSize {
		frameSize = pageSize / (pageSize / snaplen)
	} else {
		frameSize = (snaplen/pageSize + 1) * pageSize
	}
	blockSize = frameSize * 128
	numBlocks = (targetMB * 1024 * 1024) / blockSize
	if numBlocks == 0 {
		numBlocks = 1
	}
	return frameSize, blockSize, numBlocks
}

func newLive(iface string, snaplen int, ports Ports) (PacketSource, error) {
	frameSize, blockSize, numBlocks := afpacketComputeSize(48, snaplen, os.Getpagesize())
	opts := []interface{}{
		afpacket.OptFrameSize(frameSize),
		afpacket.OptBlockSize(blockSize),
		afpacket.OptNumBlocks(numBlocks),
		afpacket.OptBlockTimeout(64 * time.Millisecond),
		afpacket.OptPollTimeout(100 * time.Millisecond),
		afpacket.OptTPacketVersion(afpacket.TPacketVersion3),
	}
	if iface != "" && iface != "any" {
		opts = append(opts, afpacket.OptInterface(iface))
	}

	tp, err := afpacket.NewTPacket(opts...)
	if err != nil {
		return nil, err
	}
	// Load the in-kernel filter before we start reading so the ring only ever
	// holds dissectable traffic (see bpf.go: on a busy Cilium node the
	// "any"-interface firehose is hundreds of thousands to millions of
	// packets/sec, which instantly overruns the ring without this).
	filter, err := buildL7Filter(ports.TCP, ports.UDP)
	if err != nil {
		tp.Close()
		return nil, err
	}
	if err := tp.SetBPF(filter); err != nil {
		tp.Close()
		return nil, err
	}

	src := gopacket.NewPacketSource(tp, layers.LayerTypeEthernet)
	src.NoCopy = true
	return &liveSource{tp: tp, src: src}, nil
}

func (l *liveSource) Packets() <-chan gopacket.Packet { return l.src.Packets() }

// Stats reads the kernel's own cumulative packet/drop counters for this
// socket (TPACKET_V3's tp_packets/tp_drops via getsockopt). Unlike our
// userspace read loop, this sees drops the kernel made before the ring was
// ever handed to us.
func (l *liveSource) Stats() (RingStats, bool) {
	_, v3, err := l.tp.SocketStats()
	if err != nil {
		return RingStats{}, false
	}
	return RingStats{Packets: uint64(v3.Packets()), Drops: uint64(v3.Drops())}, true
}

func (l *liveSource) Close() error {
	l.tp.Close()
	return nil
}

//go:build linux

package capture

import (
	"os"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/net/bpf"
)

// l7Filter limits what the kernel delivers to our AF_PACKET ring to the
// traffic the dissectors can actually turn into entries. On a busy Cilium
// node the "any"-interface firehose is hundreds of thousands to millions of
// packets/sec (eth0 + cilium_vxlan + one lxc veth per pod, each packet seen
// several times), which instantly overruns the ring and stalls capture after
// the first burst. Filtering in-kernel cuts that by orders of magnitude and
// drops the VXLAN/overlay noise we can't dissect anyway.
//
// Compiled from this pcap expression (EN10MB link type, snaplen 65536):
//
//	(tcp port 80 or tcp port 8080 or tcp port 6379 or tcp port 5432 or tcp port 5672) or (udp port 53) or icmp
//
// To change the ports, regenerate with pcap.CompileBPFFilter(
// layers.LinkTypeEthernet, 65536, expr) and paste the RawInstructions here —
// we embed the compiled program so the runtime needs no libpcap.
var l7Filter = []bpf.RawInstruction{
	{Op: 0x0028, Jt: 0, Jf: 0, K: 0x0000000c},
	{Op: 0x0015, Jt: 0, Jf: 15, K: 0x000086dd},
	{Op: 0x0030, Jt: 0, Jf: 0, K: 0x00000014},
	{Op: 0x0015, Jt: 0, Jf: 8, K: 0x00000006},
	{Op: 0x0028, Jt: 0, Jf: 0, K: 0x00000036},
	{Op: 0x0015, Jt: 38, Jf: 0, K: 0x00000050},
	{Op: 0x0015, Jt: 37, Jf: 0, K: 0x00001f90},
	{Op: 0x0015, Jt: 36, Jf: 0, K: 0x000018eb},
	{Op: 0x0015, Jt: 35, Jf: 0, K: 0x00001538},
	{Op: 0x0015, Jt: 34, Jf: 0, K: 0x00001628},
	{Op: 0x0028, Jt: 0, Jf: 0, K: 0x00000038},
	{Op: 0x0015, Jt: 32, Jf: 19, K: 0x00000050},
	{Op: 0x0015, Jt: 0, Jf: 32, K: 0x00000011},
	{Op: 0x0028, Jt: 0, Jf: 0, K: 0x00000036},
	{Op: 0x0015, Jt: 29, Jf: 0, K: 0x00000035},
	{Op: 0x0028, Jt: 0, Jf: 0, K: 0x00000038},
	{Op: 0x0015, Jt: 27, Jf: 28, K: 0x00000035},
	{Op: 0x0015, Jt: 0, Jf: 27, K: 0x00000800},
	{Op: 0x0030, Jt: 0, Jf: 0, K: 0x00000017},
	{Op: 0x0015, Jt: 0, Jf: 15, K: 0x00000006},
	{Op: 0x0028, Jt: 0, Jf: 0, K: 0x00000014},
	{Op: 0x0045, Jt: 23, Jf: 0, K: 0x00001fff},
	{Op: 0x00b1, Jt: 0, Jf: 0, K: 0x0000000e},
	{Op: 0x0048, Jt: 0, Jf: 0, K: 0x0000000e},
	{Op: 0x0015, Jt: 19, Jf: 0, K: 0x00000050},
	{Op: 0x0015, Jt: 18, Jf: 0, K: 0x00001f90},
	{Op: 0x0015, Jt: 17, Jf: 0, K: 0x000018eb},
	{Op: 0x0015, Jt: 16, Jf: 0, K: 0x00001538},
	{Op: 0x0015, Jt: 15, Jf: 0, K: 0x00001628},
	{Op: 0x0048, Jt: 0, Jf: 0, K: 0x00000010},
	{Op: 0x0015, Jt: 13, Jf: 0, K: 0x00000050},
	{Op: 0x0015, Jt: 12, Jf: 0, K: 0x00001f90},
	{Op: 0x0015, Jt: 11, Jf: 0, K: 0x000018eb},
	{Op: 0x0015, Jt: 10, Jf: 0, K: 0x00001538},
	{Op: 0x0015, Jt: 9, Jf: 10, K: 0x00001628},
	{Op: 0x0015, Jt: 0, Jf: 7, K: 0x00000011},
	{Op: 0x0028, Jt: 0, Jf: 0, K: 0x00000014},
	{Op: 0x0045, Jt: 7, Jf: 0, K: 0x00001fff},
	{Op: 0x00b1, Jt: 0, Jf: 0, K: 0x0000000e},
	{Op: 0x0048, Jt: 0, Jf: 0, K: 0x0000000e},
	{Op: 0x0015, Jt: 3, Jf: 0, K: 0x00000035},
	{Op: 0x0048, Jt: 0, Jf: 0, K: 0x00000010},
	{Op: 0x0015, Jt: 1, Jf: 2, K: 0x00000035},
	{Op: 0x0015, Jt: 0, Jf: 1, K: 0x00000001},
	{Op: 0x0006, Jt: 0, Jf: 0, K: 0x00010000},
	{Op: 0x0006, Jt: 0, Jf: 0, K: 0x00000000},
}

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

func newLive(iface string, snaplen int) (PacketSource, error) {
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
	// holds dissectable traffic.
	if err := tp.SetBPF(l7Filter); err != nil {
		tp.Close()
		return nil, err
	}

	src := gopacket.NewPacketSource(tp, layers.LayerTypeEthernet)
	src.NoCopy = true
	return &liveSource{tp: tp, src: src}, nil
}

func (l *liveSource) Packets() <-chan gopacket.Packet { return l.src.Packets() }

func (l *liveSource) Close() error {
	l.tp.Close()
	return nil
}

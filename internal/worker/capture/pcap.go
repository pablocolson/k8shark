package capture

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/pcapgo"
)

// fileSource is a PacketSource backed by a pcap file (offline ingest, EXT-2):
// it replays a tcpdump/ksniff capture through the same route()/dissector path
// live traffic uses. Pure Go via pcapgo — no cgo/libpcap — so unlike live
// AF_PACKET capture it works on every platform (post-mortem analysis of a
// client-supplied pcap, dissector debugging on real bytes, a dev loop without
// a cluster or demo mode). Packets stream on a channel that closes at
// end-of-file, which captureLoop treats like any capture source ending.
type fileSource struct {
	f    *os.File
	ch   chan gopacket.Packet
	stop chan struct{}
	read atomic.Uint64 // packets read from the file (surfaced via Stats)
}

// NewFileSource opens a pcap file and streams its packets. The caller drives it
// exactly like a live source (Packets/Stats/Close). The classic pcap and the
// newer pcapng formats are both accepted (pcapgo autodetects).
func NewFileSource(path string) (PacketSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r, err := pcapgo.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: not a valid pcap file: %w", path, err)
	}
	s := &fileSource{f: f, ch: make(chan gopacket.Packet, 256), stop: make(chan struct{})}
	go s.run(r)
	return s, nil
}

func (s *fileSource) run(r *pcapgo.Reader) {
	defer close(s.ch)
	linkType := r.LinkType()
	for {
		data, ci, err := r.ReadPacketData()
		if err != nil {
			return // io.EOF, or a truncated/garbled record: stop cleanly
		}
		pkt := gopacket.NewPacket(data, linkType, gopacket.Lazy)
		m := pkt.Metadata()
		m.CaptureInfo = ci
		if m.Timestamp.IsZero() {
			m.Timestamp = time.Now()
		}
		s.read.Add(1)
		select {
		case s.ch <- pkt:
		case <-s.stop:
			return
		}
	}
}

func (s *fileSource) Packets() <-chan gopacket.Packet { return s.ch }

// Stats reports how many packets have been read from the file so far. There is
// no kernel ring behind a file, so Drops is always 0.
func (s *fileSource) Stats() (RingStats, bool) {
	return RingStats{Packets: s.read.Load()}, true
}

func (s *fileSource) Close() error {
	select {
	case <-s.stop:
		// already closed
	default:
		close(s.stop)
	}
	return s.f.Close()
}

package capture

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

// writePcap builds a classic pcap file at path holding n minimal
// Ethernet/IPv4/TCP packets and returns it.
func writePcap(t *testing.T, path string, n int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := pcapgo.NewWriter(f)
	if err := w.WriteFileHeader(65536, layers.LinkTypeEthernet); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		eth := &layers.Ethernet{
			SrcMAC:       []byte{2, 0, 0, 0, 0, 1},
			DstMAC:       []byte{2, 0, 0, 0, 0, 2},
			EthernetType: layers.EthernetTypeIPv4,
		}
		ip := &layers.IPv4{
			Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolTCP,
			SrcIP: []byte{10, 0, 0, 1}, DstIP: []byte{10, 0, 0, 2},
		}
		tcp := &layers.TCP{SrcPort: 40000, DstPort: 80, Seq: uint32(i)}
		_ = tcp.SetNetworkLayerForChecksum(ip)
		buf := gopacket.NewSerializeBuffer()
		if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, eth, ip, tcp); err != nil {
			t.Fatal(err)
		}
		data := buf.Bytes()
		ci := gopacket.CaptureInfo{Timestamp: time.Unix(int64(1700000000+i), 0), CaptureLength: len(data), Length: len(data)}
		if err := w.WritePacket(ci, data); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileSourceReplaysPackets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.pcap")
	writePcap(t, path, 3)

	src, err := NewFileSource(path)
	if err != nil {
		t.Fatalf("NewFileSource: %v", err)
	}
	defer src.Close()

	var got int
	deadline := time.After(3 * time.Second)
	for {
		select {
		case pkt, ok := <-src.Packets():
			if !ok {
				if got != 3 {
					t.Fatalf("got %d packets, want 3", got)
				}
				// timestamps must survive the round-trip
				if rs, ok := src.Stats(); !ok || rs.Packets != 3 {
					t.Fatalf("Stats() = %+v, want 3 packets read", rs)
				}
				return
			}
			if pkt.NetworkLayer() == nil || pkt.TransportLayer() == nil {
				t.Errorf("packet %d did not decode L3/L4: %v", got, pkt)
			}
			got++
		case <-deadline:
			t.Fatalf("timed out after %d packets", got)
		}
	}
}

func TestFileSourceRejectsNonPcap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.bin")
	if err := os.WriteFile(path, []byte("this is not a pcap"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSource(path); err == nil {
		t.Fatal("NewFileSource accepted a non-pcap file, want an error")
	}
	if _, err := NewFileSource(filepath.Join(t.TempDir(), "nope.pcap")); err == nil {
		t.Fatal("NewFileSource accepted a missing file, want an error")
	}
}

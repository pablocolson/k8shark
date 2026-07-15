package worker

import (
	"testing"
	"time"

	"github.com/google/gopacket/layers"
)

// mkTCP builds a *layers.TCP with the given flags/seq/window/payload for driving
// trackTCP directly.
func mkTCP(seq uint32, syn, ack, fin bool, window uint16, payload int, opts ...layers.TCPOption) *layers.TCP {
	t := &layers.TCP{Seq: seq, SYN: syn, ACK: ack, FIN: fin, Window: window, Options: opts}
	if payload > 0 {
		t.Payload = make([]byte, payload)
	}
	return t
}

func TestTrackTCPSnapshotL4(t *testing.T) {
	s := newSink("", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	// client 40000 -> server 80
	reqNet, reqTr, respNet, respTr := flows(40000, 80)

	mss := layers.TCPOption{OptionType: layers.TCPOptionKindMSS, OptionLength: 4, OptionData: []byte{0x05, 0xb4}} // 1460
	base := time.Unix(1_700_000_000, 0)
	meta := l4meta{srcMAC: "02:00:00:00:00:01", dstMAC: "02:00:00:00:00:02", ipVersion: 4, ttl: 64, ipFlags: "DF", headerHex: "hdr"}
	empty := l4meta{}

	// SYN (client) — carries the L3 meta, MSS and window.
	p.trackTCP(reqNet, reqTr, mkTCP(1000, true, false, false, 64240, 0, mss), 60, base, meta)
	// SYN-ACK (server), +10ms.
	p.trackTCP(respNet, respTr, mkTCP(5000, true, true, false, 64240, 0), 60, base.Add(10*time.Millisecond), empty)
	// Client data (100B payload), +11ms.
	p.trackTCP(reqNet, reqTr, mkTCP(1001, false, true, false, 64240, 100), 154, base.Add(11*time.Millisecond), empty)
	// Client retransmit — same seq, non-empty payload => retransmit++, +12ms.
	p.trackTCP(reqNet, reqTr, mkTCP(1001, false, true, false, 64240, 100), 154, base.Add(12*time.Millisecond), empty)
	// Server data (200B payload), +13ms.
	p.trackTCP(respNet, respTr, mkTCP(5001, false, true, false, 64240, 200), 254, base.Add(13*time.Millisecond), empty)

	info := p.snapshotL4(connKey(reqNet, reqTr))
	if info == nil {
		t.Fatal("snapshotL4 returned nil")
	}
	if info.RTTMs <= 0 {
		t.Errorf("RTTMs = %v, want > 0", info.RTTMs)
	}
	if info.MSS != 1460 {
		t.Errorf("MSS = %d, want 1460", info.MSS)
	}
	if info.SeqStart != 1000 || info.AckStart != 5000 {
		t.Errorf("SeqStart/AckStart = %d/%d, want 1000/5000", info.SeqStart, info.AckStart)
	}
	if info.Retransmits != 1 {
		t.Errorf("Retransmits = %d, want 1", info.Retransmits)
	}
	if info.ClientTCPFlags != "SYN,ACK" {
		t.Errorf("ClientTCPFlags = %q, want SYN,ACK", info.ClientTCPFlags)
	}
	// SYN + data + retransmit = 3 client packets; SYN-ACK + data = 2 server packets.
	if info.ClientPackets != 3 || info.ServerPackets != 2 {
		t.Errorf("packets client/server = %d/%d, want 3/2", info.ClientPackets, info.ServerPackets)
	}
	if info.ClientBytes != 60+154+154 || info.ServerBytes != 60+254 {
		t.Errorf("bytes client/server = %d/%d, want 368/314", info.ClientBytes, info.ServerBytes)
	}
	if info.SrcMAC != "02:00:00:00:00:01" || info.TTL != 64 || info.IPFlags != "DF" || info.HeaderHex != "hdr" {
		t.Errorf("L3 meta not copied: %+v", info)
	}
	if info.Window != 64240 {
		t.Errorf("Window = %d, want 64240", info.Window)
	}
}

// A FIN closes the flow and emits a generic L4 entry carrying L4Info.
func TestTrackTCPCloseEmitsL4(t *testing.T) {
	s := newSink("", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	reqNet, reqTr, _, _ := flows(41000, 443)
	base := time.Unix(1_700_000_000, 0)
	meta := l4meta{srcMAC: "02:00:00:00:00:0a", ipVersion: 4, ttl: 128}

	p.trackTCP(reqNet, reqTr, mkTCP(1, true, false, false, 1000, 0), 60, base, meta)
	p.trackTCP(reqNet, reqTr, mkTCP(2, false, true, true, 1000, 0), 60, base.Add(time.Millisecond), l4meta{})

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].L4 == nil {
		t.Fatal("emitted L4 flow has no L4Info")
	}
	if got[0].L4.TTL != 128 {
		t.Errorf("L4.TTL = %d, want 128", got[0].L4.TTL)
	}
}

package capture

import (
	"encoding/binary"
	"testing"

	"golang.org/x/net/bpf"
)

// Synthetic frame builders. The VM only reads fixed byte offsets — it never
// validates checksums or lengths — so these can be minimal and don't need to
// be wire-valid beyond the fields the filter actually inspects.

func ethHeader(ethertype uint16) []byte {
	b := make([]byte, 14)
	binary.BigEndian.PutUint16(b[12:], ethertype)
	return b
}

// ipv4Header builds a 20-byte IPv4 header (IHL=5, no options).
// fragField is the raw 16-bit flags+fragment-offset field (offset 6-7).
func ipv4Header(proto byte, fragField uint16) []byte {
	b := make([]byte, 20)
	b[0] = 0x45 // version 4, IHL 5
	binary.BigEndian.PutUint16(b[6:], fragField)
	b[9] = proto
	return b
}

func ipv6Header(nextHeader byte) []byte {
	b := make([]byte, 40)
	b[0] = 0x60 // version 6
	b[6] = nextHeader
	return b
}

func tcpSeg(srcPort, dstPort uint16) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b[0:], srcPort)
	binary.BigEndian.PutUint16(b[2:], dstPort)
	b[12] = 0x50 // data offset 5, no options
	return b
}

func udpSeg(srcPort, dstPort uint16) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b[0:], srcPort)
	binary.BigEndian.PutUint16(b[2:], dstPort)
	return b
}

func icmpSeg() []byte {
	return make([]byte, 8)
}

func ipv4Frame(proto byte, fragField uint16, l4 []byte) []byte {
	f := ethHeader(0x0800)
	f = append(f, ipv4Header(proto, fragField)...)
	f = append(f, l4...)
	return f
}

func ipv6Frame(nextHeader byte, l4 []byte) []byte {
	f := ethHeader(0x86dd)
	f = append(f, ipv6Header(nextHeader)...)
	f = append(f, l4...)
	return f
}

func runFilter(t *testing.T, tcpPorts, udpPorts []int, frame []byte) bool {
	t.Helper()
	// Also exercise buildL7Filter (the production entry point) to confirm the
	// program assembles cleanly, in addition to running it through the VM.
	if _, err := buildL7Filter(tcpPorts, udpPorts); err != nil {
		t.Fatalf("buildL7Filter: %v", err)
	}
	vm, err := bpf.NewVM(buildL7Program(tcpPorts, udpPorts))
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	n, err := vm.Run(frame)
	if err != nil {
		t.Fatalf("vm.Run: %v", err)
	}
	return n > 0
}

const (
	protoTCP   = 6
	protoUDP   = 17
	protoICMP  = 1
	protoICMP6 = 58
)

func TestBuildL7Filter_IPv4TCP(t *testing.T) {
	tcpPorts := []int{80, 6379}
	udpPorts := []int{53}

	cases := []struct {
		name       string
		srcP, dstP uint16
		want       bool
	}{
		{"dst port configured", 40000, 80, true},
		{"src port configured", 6379, 40000, true},
		{"neither port configured", 40000, 40001, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := ipv4Frame(protoTCP, 0, tcpSeg(c.srcP, c.dstP))
			if got := runFilter(t, tcpPorts, udpPorts, frame); got != c.want {
				t.Errorf("accepted = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBuildL7Filter_OperatorConfiguredPortsAccepted(t *testing.T) {
	// The whole point of CAP-1: a port the operator added (e.g. via
	// --http-ports=3000) must reach the kernel filter, not just userspace
	// dispatch.
	tcpPorts := []int{3000}
	frame := ipv4Frame(protoTCP, 0, tcpSeg(50000, 3000))
	if !runFilter(t, tcpPorts, nil, frame) {
		t.Error("operator-configured TCP port was rejected by the kernel filter")
	}
}

func TestBuildL7Filter_IPv4UDPDNS(t *testing.T) {
	frame := ipv4Frame(protoUDP, 0, udpSeg(51000, 53))
	if !runFilter(t, []int{80}, []int{53}, frame) {
		t.Error("DNS (udp/53) should be accepted")
	}
	frame = ipv4Frame(protoUDP, 0, udpSeg(51000, 5353))
	if runFilter(t, []int{80}, []int{53}, frame) {
		t.Error("unconfigured UDP port should be rejected")
	}
}

func TestBuildL7Filter_ICMPAlwaysAccepted(t *testing.T) {
	// ICMP carries no ports at all — must be accepted unconditionally,
	// regardless of the configured TCP/UDP port lists.
	frame := ipv4Frame(protoICMP, 0, icmpSeg())
	if !runFilter(t, nil, nil, frame) {
		t.Error("ICMP should always be accepted")
	}
}

func TestBuildL7Filter_IPv4FragmentRejected(t *testing.T) {
	// Non-first fragment (fragment offset != 0): no L4 header present, so
	// even a "matching" byte pattern at the port offset must not leak
	// through. 0x0001 sets a 1-fragment-unit offset.
	frame := ipv4Frame(protoTCP, 0x0001, tcpSeg(80, 80))
	if runFilter(t, []int{80}, nil, frame) {
		t.Error("non-first IP fragment should be rejected (no L4 header to match a port from)")
	}
}

func TestBuildL7Filter_IPv4FirstFragmentStillMatchesPorts(t *testing.T) {
	// First fragment (offset 0, MF flag set) does carry the real L4 header.
	frame := ipv4Frame(protoTCP, 0x2000 /* MF flag, offset 0 */, tcpSeg(80, 40000))
	if !runFilter(t, []int{80}, nil, frame) {
		t.Error("first fragment (offset 0) should still be matched on its real port")
	}
}

func TestBuildL7Filter_IPv6TCPAndUDP(t *testing.T) {
	tcpFrame := ipv6Frame(protoTCP, tcpSeg(40000, 6379))
	if !runFilter(t, []int{6379}, nil, tcpFrame) {
		t.Error("IPv6 TCP on configured port should be accepted")
	}
	udpFrame := ipv6Frame(protoUDP, udpSeg(40000, 53))
	if !runFilter(t, nil, []int{53}, udpFrame) {
		t.Error("IPv6 UDP/53 should be accepted")
	}
	rejectFrame := ipv6Frame(protoTCP, tcpSeg(40000, 40001))
	if runFilter(t, []int{6379}, nil, rejectFrame) {
		t.Error("IPv6 TCP on an unconfigured port should be rejected")
	}
}

func TestBuildL7Filter_ICMPv6AlwaysAccepted(t *testing.T) {
	frame := ipv6Frame(protoICMP6, icmpSeg())
	if !runFilter(t, nil, nil, frame) {
		t.Error("ICMPv6 should always be accepted")
	}
}

func TestBuildL7Filter_UnknownEthertypeRejected(t *testing.T) {
	frame := append(ethHeader(0x0806), make([]byte, 28)...) // ARP
	if runFilter(t, []int{80}, []int{53}, frame) {
		t.Error("non-IP ethertype should be rejected")
	}
}

func TestBuildL7Filter_EmptyPortListsStillAllowICMP(t *testing.T) {
	raw, err := buildL7Filter(nil, nil)
	if err != nil {
		t.Fatalf("buildL7Filter with no ports: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected a non-empty program even with no configured ports")
	}
}

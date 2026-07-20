package worker

import "testing"

// capturePorts is what feeds the kernel-level BPF filter (see
// internal/worker/capture/bpf.go) — a port missing here never reaches
// userspace at all, unlike buildRespPorts/buildAMQPPorts which only affect
// dispatch after the kernel has already let the packet through.
func TestCapturePortsDefaults(t *testing.T) {
	got := capturePorts(Options{})
	wantTCP := map[int]bool{80: true, 8080: true, redisPort: true, pgPort: true, amqpPort: true, dnsPort: true, mysqlPort: true, mongoPort: true}
	if len(got.TCP) != len(wantTCP) {
		t.Fatalf("TCP ports = %v, want the %d defaults", got.TCP, len(wantTCP))
	}
	for _, p := range got.TCP {
		if !wantTCP[p] {
			t.Errorf("unexpected default TCP port %d", p)
		}
	}
	if len(got.UDP) != 1 || got.UDP[0] != 53 {
		t.Errorf("UDP ports = %v, want [53]", got.UDP)
	}
}

func TestCapturePortsMergesOperatorOverrides(t *testing.T) {
	got := capturePorts(Options{
		RedisPorts:  []int{7000},
		ValkeyPorts: []int{7001},
		AMQPPorts:   []int{5673},
		HTTPPorts:   []int{3000, 8080}, // 8080 duplicates a default on purpose
	})
	want := map[int]bool{
		80: true, 8080: true, redisPort: true, pgPort: true, amqpPort: true, dnsPort: true,
		mysqlPort: true, mongoPort: true,
		7000: true, 7001: true, 5673: true, 3000: true,
	}
	if len(got.TCP) != len(want) {
		t.Fatalf("TCP ports = %v, want exactly %v (duplicates must not double up)", got.TCP, want)
	}
	for _, p := range got.TCP {
		if !want[p] {
			t.Errorf("unexpected TCP port %d", p)
		}
	}
}

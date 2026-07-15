//go:build linux

package ebpf

import (
	"encoding/binary"
	"testing"
)

// TestDecodeEventWithTuple verifies decodeEvent's hand-computed offsets against
// a crafted struct event, including the Phase 2b 4-tuple fields. The offsets
// must match bpf/tls.bpf.c's struct event layout exactly.
func TestDecodeEventWithTuple(t *testing.T) {
	data := []byte("hello")
	raw := make([]byte, eventOffData+len(data))
	binary.LittleEndian.PutUint32(raw[eventOffPID:], 100)
	binary.LittleEndian.PutUint32(raw[eventOffTID:], 7)
	binary.LittleEndian.PutUint64(raw[eventOffSSLCtx:], 42)
	copy(raw[eventOffSAddr:], []byte{10, 0, 0, 1}) // network-order IPv4 10.0.0.1
	copy(raw[eventOffDAddr:], []byte{10, 0, 0, 2}) // 10.0.0.2
	binary.LittleEndian.PutUint32(raw[eventOffDataLen:], uint32(len(data)))
	binary.LittleEndian.PutUint16(raw[eventOffSPort:], 54321)
	binary.LittleEndian.PutUint16(raw[eventOffDPort:], 5432)
	raw[eventOffDirection] = byte(TLSDirWrite)
	copy(raw[eventOffData:], data)

	rec, err := decodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rec.PID != 100 || rec.TID != 7 || rec.ConnID != 42 {
		t.Errorf("pid/tid/conn = %d/%d/%d, want 100/7/42", rec.PID, rec.TID, rec.ConnID)
	}
	if rec.Direction != TLSDirWrite {
		t.Errorf("direction = %d, want write", rec.Direction)
	}
	if rec.SrcIP != "10.0.0.1" || rec.DstIP != "10.0.0.2" {
		t.Errorf("ips = %s -> %s, want 10.0.0.1 -> 10.0.0.2", rec.SrcIP, rec.DstIP)
	}
	if rec.SrcPort != 54321 || rec.DstPort != 5432 {
		t.Errorf("ports = %d -> %d, want 54321 -> 5432", rec.SrcPort, rec.DstPort)
	}
	if string(rec.Data) != "hello" {
		t.Errorf("data = %q, want hello", rec.Data)
	}
}

// TestDecodeEventUnresolvedTuple: a zero 4-tuple (kprobe hasn't resolved the
// socket yet) leaves SrcIP/DstIP empty so the caller keeps the synthetic
// endpoint.
func TestDecodeEventUnresolvedTuple(t *testing.T) {
	raw := make([]byte, eventOffData)
	binary.LittleEndian.PutUint32(raw[eventOffPID:], 5)
	raw[eventOffDirection] = byte(TLSDirRead)
	rec, err := decodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SrcIP != "" || rec.DstIP != "" {
		t.Errorf("ips = %q/%q, want empty (unresolved)", rec.SrcIP, rec.DstIP)
	}
}

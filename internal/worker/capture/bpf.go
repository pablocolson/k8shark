package capture

import "golang.org/x/net/bpf"

// snapLen is the accept-return value used by RetConstant: any positive value
// tells the kernel to keep the whole captured frame (up to the socket's own
// snaplen), matching what the previously hardcoded filter returned.
const snapLen = 65536

// buildL7Filter compiles a cBPF program that accepts only: IPv4/IPv6 TCP
// packets whose source or destination port is in tcpPorts, IPv4/IPv6 UDP
// packets whose source or destination port is in udpPorts, and all ICMPv4/
// ICMPv6 packets. Everything else — including non-first IP fragments, which
// carry no L4 header to read a port from — is dropped in-kernel.
//
// This exists so the operator-configured ports (--redis-ports, --valkey-ports,
// --amqp-ports, --http-ports, ...) actually reach the kernel-level filter
// instead of only affecting userspace protocol dispatch: a port not present
// here is silently dropped by the kernel before any dissector ever sees it.
//
// IPv6 uses fixed header offsets (no extension-header walking) — same
// simplifying assumption as the rest of this package (see CAP-7 in
// docs/ROADMAP.md). A packet whose real L4 header is pushed past those fixed
// offsets by an extension header simply won't match its expected protocol
// number and gets rejected, which is a safe (if conservative) failure mode,
// not a new gap introduced here.
//
// This is deliberately hand-assembled via golang.org/x/net/bpf's symbolic
// builder rather than gopacket/pcap.CompileBPFFilter, which would pull in a
// runtime libpcap dependency this project intentionally avoids (see the
// comment on the old static l7Filter and build/k8shark.Dockerfile).
func buildL7Filter(tcpPorts, udpPorts []int) ([]bpf.RawInstruction, error) {
	return bpf.Assemble(buildL7Program(tcpPorts, udpPorts))
}

// buildL7Program returns the symbolic (unassembled) instruction list.
// Exposed separately from buildL7Filter so tests can run it directly through
// bpf.VM: NewVM requires the program's last instruction to be a concrete
// RetA/RetConstant value, which a round-trip through RawInstruction (as
// produced by bpf.Assemble) no longer satisfies.
func buildL7Program(tcpPorts, udpPorts []int) []bpf.Instruction {
	accept := bpf.RetConstant{Val: snapLen}
	reject := bpf.RetConstant{Val: 0}

	ipv4 := ipv4Block(tcpPorts, udpPorts, accept, reject)
	ipv6 := ipv6Block(tcpPorts, udpPorts, accept, reject)

	prog := []bpf.Instruction{
		bpf.LoadAbsolute{Off: 12, Size: 2}, // ethertype
	}
	prog = append(prog, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x0800, SkipTrue: 0, SkipFalse: uint8(len(ipv4))})
	prog = append(prog, ipv4...)
	prog = append(prog, bpf.LoadAbsolute{Off: 12, Size: 2}) // ethertype (A was clobbered above)
	prog = append(prog, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x86dd, SkipTrue: 0, SkipFalse: uint8(len(ipv6))})
	prog = append(prog, ipv6...)
	prog = append(prog, reject) // neither IPv4 nor IPv6 (ARP, etc.)

	return prog
}

// portMatchInstrs checks a loaded 16-bit port field (loaded fresh via loadSrc,
// then again via loadDst) against each candidate port, returning accept as
// soon as any one matches. Ports are matched src-or-dst, mirroring how the
// human-readable "tcp port N" pcap expression the old static filter was
// compiled from behaves.
func portMatchInstrs(loadSrc, loadDst bpf.Instruction, ports []int, accept bpf.Instruction) []bpf.Instruction {
	var out []bpf.Instruction
	for _, load := range []bpf.Instruction{loadSrc, loadDst} {
		out = append(out, load)
		for _, p := range ports {
			out = append(out,
				bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(p), SkipTrue: 0, SkipFalse: 1},
				accept,
			)
		}
	}
	return out
}

// ipv4Block assumes A holds nothing in particular on entry (it (re)loads what
// it needs) and packet[12:14] == 0x0800 has already been confirmed by the
// caller. It reads the protocol byte at a fixed offset (always inside the
// fixed 20-byte portion of the IPv4 header, options live after that), then
// for TCP/UDP computes the variable header length via LoadMemShift before
// reading ports.
func ipv4Block(tcpPorts, udpPorts []int, accept, reject bpf.Instruction) []bpf.Instruction {
	// Non-first fragments (fragment offset != 0) carry no L4 header at all —
	// reading a "port" from one would just be reading payload bytes. Guard
	// each of the TCP/UDP branches with this before touching the port fields.
	fragGuard := func() []bpf.Instruction {
		return []bpf.Instruction{
			bpf.LoadAbsolute{Off: 20, Size: 2}, // IP flags + fragment offset
			bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x1fff, SkipTrue: 0, SkipFalse: 1},
			reject,
		}
	}

	tcpHandle := append(fragGuard(),
		bpf.LoadMemShift{Off: 14}, // X = IPv4 header length (14 = start of IP header)
	)
	tcpHandle = append(tcpHandle, portMatchInstrs(
		bpf.LoadIndirect{Off: 14, Size: 2}, // src port at [X+14:X+16]
		bpf.LoadIndirect{Off: 16, Size: 2}, // dst port at [X+16:X+18]
		tcpPorts, accept,
	)...)

	udpHandle := append(fragGuard(),
		bpf.LoadMemShift{Off: 14},
	)
	udpHandle = append(udpHandle, portMatchInstrs(
		bpf.LoadIndirect{Off: 14, Size: 2},
		bpf.LoadIndirect{Off: 16, Size: 2},
		udpPorts, accept,
	)...)

	var out []bpf.Instruction
	out = append(out, bpf.LoadAbsolute{Off: 23, Size: 1}) // protocol
	out = append(out, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 6, SkipTrue: 0, SkipFalse: uint8(len(tcpHandle))})
	out = append(out, tcpHandle...)
	out = append(out, bpf.LoadAbsolute{Off: 23, Size: 1}) // protocol (A clobbered above)
	out = append(out, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 17, SkipTrue: 0, SkipFalse: uint8(len(udpHandle))})
	out = append(out, udpHandle...)
	out = append(out, bpf.LoadAbsolute{Off: 23, Size: 1}) // protocol, once more, for ICMP
	out = append(out, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 1, SkipTrue: 0, SkipFalse: 1})
	out = append(out, accept)
	out = append(out, reject)
	return out
}

// ipv6Block mirrors ipv4Block for the fixed 40-byte IPv6 base header
// (packet[12:14] == 0x86dd already confirmed by the caller). No fragment
// guard: IPv6 fragmentation uses a distinct Next Header value (44) carried in
// an extension header, so a fragmented flow simply won't read as
// NextHeader==6/17 here and falls through to reject — no separate check
// needed.
func ipv6Block(tcpPorts, udpPorts []int, accept, reject bpf.Instruction) []bpf.Instruction {
	tcpHandle := portMatchInstrs(
		bpf.LoadAbsolute{Off: 54, Size: 2}, // src port, fixed offset (14 eth + 40 ipv6 header)
		bpf.LoadAbsolute{Off: 56, Size: 2}, // dst port
		tcpPorts, accept,
	)
	udpHandle := portMatchInstrs(
		bpf.LoadAbsolute{Off: 54, Size: 2},
		bpf.LoadAbsolute{Off: 56, Size: 2},
		udpPorts, accept,
	)

	var out []bpf.Instruction
	out = append(out, bpf.LoadAbsolute{Off: 20, Size: 1}) // next header
	out = append(out, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 6, SkipTrue: 0, SkipFalse: uint8(len(tcpHandle))})
	out = append(out, tcpHandle...)
	out = append(out, bpf.LoadAbsolute{Off: 20, Size: 1})
	out = append(out, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 17, SkipTrue: 0, SkipFalse: uint8(len(udpHandle))})
	out = append(out, udpHandle...)
	out = append(out, bpf.LoadAbsolute{Off: 20, Size: 1})
	out = append(out, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x3a, SkipTrue: 0, SkipFalse: 1}) // ICMPv6
	out = append(out, accept)
	out = append(out, reject)
	return out
}

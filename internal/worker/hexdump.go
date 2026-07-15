package worker

import (
	"fmt"
	"strings"

	"github.com/google/gopacket/layers"
)

// hexDump renders b as an offset/hex/ascii block in the classic `hexdump -C`
// style, capped at cap bytes. It returns "" for empty input (or a non-positive
// cap). Output is bounded so it is safe to attach to an entry and ship over the
// wire.
func hexDump(b []byte, cap int) string {
	if cap <= 0 || len(b) == 0 {
		return ""
	}
	if len(b) > cap {
		b = b[:cap]
	}
	var sb strings.Builder
	for off := 0; off < len(b); off += 16 {
		end := off + 16
		if end > len(b) {
			end = len(b)
		}
		line := b[off:end]
		fmt.Fprintf(&sb, "%08x  ", off)
		// hex columns (two groups of 8, extra space between)
		for j := 0; j < 16; j++ {
			if j < len(line) {
				fmt.Fprintf(&sb, "%02x ", line[j])
			} else {
				sb.WriteString("   ")
			}
			if j == 7 {
				sb.WriteByte(' ')
			}
		}
		// ascii column
		sb.WriteString(" |")
		for _, c := range line {
			if c >= 0x20 && c < 0x7f {
				sb.WriteByte(c)
			} else {
				sb.WriteByte('.')
			}
		}
		sb.WriteString("|\n")
	}
	return sb.String()
}

// flagSet is a small bitset over the TCP control flags, used to union the flags
// seen per direction of a connection.
type flagSet uint8

const (
	flagSYN flagSet = 1 << iota
	flagACK
	flagFIN
	flagRST
	flagPSH
	flagURG
)

// String renders the set as "SYN,ACK,FIN" in a stable order.
func (f flagSet) String() string {
	var out []string
	for _, fl := range []struct {
		bit  flagSet
		name string
	}{
		{flagSYN, "SYN"}, {flagACK, "ACK"}, {flagFIN, "FIN"},
		{flagRST, "RST"}, {flagPSH, "PSH"}, {flagURG, "URG"},
	} {
		if f&fl.bit != 0 {
			out = append(out, fl.name)
		}
	}
	return strings.Join(out, ",")
}

// tcpFlagSet extracts the control-flag bitset from a TCP layer.
func tcpFlagSet(tcp *layers.TCP) flagSet {
	var f flagSet
	if tcp.SYN {
		f |= flagSYN
	}
	if tcp.ACK {
		f |= flagACK
	}
	if tcp.FIN {
		f |= flagFIN
	}
	if tcp.RST {
		f |= flagRST
	}
	if tcp.PSH {
		f |= flagPSH
	}
	if tcp.URG {
		f |= flagURG
	}
	return f
}

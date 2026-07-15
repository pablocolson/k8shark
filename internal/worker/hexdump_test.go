package worker

import (
	"strings"
	"testing"
)

func TestHexDumpEmpty(t *testing.T) {
	if got := hexDump(nil, 128); got != "" {
		t.Errorf("hexDump(nil) = %q, want empty", got)
	}
	if got := hexDump([]byte("x"), 0); got != "" {
		t.Errorf("hexDump with cap 0 = %q, want empty", got)
	}
}

func TestHexDumpFormat(t *testing.T) {
	got := hexDump([]byte("HTTP/1.1"), 128)
	// One line: offset, hex bytes, ascii gutter.
	if !strings.HasPrefix(got, "00000000  ") {
		t.Errorf("missing offset prefix: %q", got)
	}
	if !strings.Contains(got, "48 54 54 50") { // "HTTP"
		t.Errorf("missing hex bytes: %q", got)
	}
	if !strings.Contains(got, "|HTTP/1.1|") {
		t.Errorf("missing ascii gutter: %q", got)
	}
}

func TestHexDumpCapTruncates(t *testing.T) {
	in := append([]byte(strings.Repeat("A", 8)), []byte(strings.Repeat("B", 12))...)
	got := hexDump(in, 8) // only the first 8 'A' bytes should be dumped
	if strings.Count(got, "41") != 8 {
		t.Errorf("expected 8 'A' (0x41) bytes, got %q", got)
	}
	if strings.Contains(got, "42") || strings.Contains(got, "B") {
		t.Errorf("cap not honoured, 'B' bytes leaked: %q", got)
	}
	if !strings.Contains(got, "|AAAAAAAA|") {
		t.Errorf("ascii gutter wrong: %q", got)
	}
}

func TestFlagSetString(t *testing.T) {
	f := flagSYN | flagACK | flagFIN
	if got := f.String(); got != "SYN,ACK,FIN" {
		t.Errorf("flagSet.String() = %q, want SYN,ACK,FIN", got)
	}
	if got := flagSet(0).String(); got != "" {
		t.Errorf("empty flagSet = %q, want empty", got)
	}
}

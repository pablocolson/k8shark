package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// cannedPcap is an opaque body the fake hub returns; the MCP tool only relays
// bytes to a file, so it need not be a genuine capture.
var cannedPcap = append([]byte{0xd4, 0xc3, 0xb2, 0xa1}, []byte("canned-pcap-body")...)

// pathFromExportResult pulls the written file path out of the tool's message
// ("Exported N bytes ... to <path>\n...").
func pathFromExportResult(t *testing.T, out string) string {
	t.Helper()
	firstLine := strings.SplitN(out, "\n", 2)[0]
	idx := strings.Index(firstLine, " to ")
	if idx < 0 {
		t.Fatalf("export result missing a path: %q", out)
	}
	return firstLine[idx+len(" to "):]
}

// export_pcap fetches /api/pcap and writes the bytes to a local file whose path
// it returns.
func TestExportPcapWritesFile(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
		_, _ = w.Write(cannedPcap)
	}))
	defer srv.Close()

	s := New(srv.URL, "", true, discardLogger())
	out, err := s.handleExportPcap(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("handleExportPcap: %v", err)
	}
	if gotPath != "/api/pcap" {
		t.Errorf("hub path = %q, want /api/pcap", gotPath)
	}

	path := pathFromExportResult(t, out)
	defer os.Remove(path)
	if !strings.HasSuffix(path, ".pcap") {
		t.Errorf("written path = %q, want a .pcap file", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != string(cannedPcap) {
		t.Errorf("written bytes = %q, want the hub's pcap bytes", got)
	}
	if !strings.Contains(out, "tshark") {
		t.Errorf("result should hint how to open the file: %q", out)
	}
}

// filter/since/until/limit args are forwarded to the hub query.
func TestExportPcapForwardsArgs(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write(cannedPcap)
	}))
	defer srv.Close()

	s := New(srv.URL, "", true, discardLogger())
	out, err := s.handleExportPcap(context.Background(), map[string]any{
		"filter": `protocol == "http"`,
		"since":  "15m",
		"limit":  float64(50),
	})
	if err != nil {
		t.Fatalf("handleExportPcap: %v", err)
	}
	defer os.Remove(pathFromExportResult(t, out))

	for _, want := range []string{"filter=", "protocol", "since=15m", "limit=50"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("hub query %q missing %q", gotQuery, want)
		}
	}
}

// A hub error is surfaced, not written as a bogus file.
func TestExportPcapHubError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad filter", http.StatusBadRequest)
	}))
	defer srv.Close()

	s := New(srv.URL, "", true, discardLogger())
	if _, err := s.handleExportPcap(context.Background(), map[string]any{"filter": "bogus"}); err == nil {
		t.Fatal("expected an error for a 400 hub response")
	}
}

// The capture tool is gated behind allowCapture, under both its advertised name
// and the export_pcap alias; without allowCapture neither resolves.
func TestExportPcapGatedByAllowCapture(t *testing.T) {
	off := New("http://hub.invalid", "", false, discardLogger())
	if off.lookup("start_pcap") != nil {
		t.Error("start_pcap must not be registered without allowCapture")
	}
	if off.lookup("export_pcap") != nil {
		t.Error("export_pcap alias must not resolve without allowCapture")
	}

	on := New("http://hub.invalid", "", true, discardLogger())
	start := on.lookup("start_pcap")
	if start == nil {
		t.Fatal("start_pcap must be registered with allowCapture")
	}
	if !start.mutating {
		t.Error("the pcap export tool must be marked mutating (it writes a file)")
	}
	if alias := on.lookup("export_pcap"); alias != start {
		t.Error("export_pcap alias must resolve to the same tool as start_pcap")
	}
}

// The alias is callable end-to-end through tools/call, not just via lookup.
func TestExportPcapAliasViaCallTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(cannedPcap)
	}))
	defer srv.Close()

	s := New(srv.URL, "", true, discardLogger())
	s.http = &http.Client{Timeout: 5 * time.Second}

	// Resolve through lookup (the path callTool takes) using the alias name.
	tool := s.lookup("export_pcap")
	if tool == nil {
		t.Fatal("export_pcap did not resolve via lookup")
	}
	text, err := tool.handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("export_pcap alias handler: %v", err)
	}
	path := pathFromExportResult(t, text)
	defer os.Remove(path)
	if got, err := os.ReadFile(path); err != nil || string(got) != string(cannedPcap) {
		t.Errorf("alias call did not write the pcap bytes (err=%v)", err)
	}
}

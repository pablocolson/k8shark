package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pablocolson/k8shark/pkg/api"
)

// MCP-7: initialize returns a non-empty instructions field steering an agent
// toward the cheap aggregate tools before raw entries.
func TestInitializeIncludesInstructions(t *testing.T) {
	s := New("http://hub.invalid", "", false, discardLogger())
	resp := s.dispatch(context.Background(), rpcRequest{Method: "initialize", ID: json.RawMessage("1")})
	if resp.Error != nil {
		t.Fatalf("initialize returned an error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", resp.Result)
	}
	instructions, _ := result["instructions"].(string)
	if instructions == "" {
		t.Fatal("initialize result has no instructions")
	}
	if !strings.Contains(instructions, "get_stats") {
		t.Errorf("instructions = %q, want it to mention get_stats", instructions)
	}
}

// MCP-7: every tool is annotated readOnlyHint=true except start_pcap, the one
// tool that changes state.
func TestToolDefsReadOnlyHintExceptStartPcap(t *testing.T) {
	s := New("http://hub.invalid", "", true, discardLogger())
	defs := s.toolDefs()

	seenStartPcap := false
	for _, d := range defs {
		if d.Annotations == nil {
			t.Errorf("tool %q has no annotations", d.Name)
			continue
		}
		if d.Name == "start_pcap" {
			seenStartPcap = true
			if d.Annotations.ReadOnlyHint {
				t.Errorf("start_pcap readOnlyHint = true, want false (it mutates capture state)")
			}
			continue
		}
		if !d.Annotations.ReadOnlyHint {
			t.Errorf("tool %q readOnlyHint = false, want true", d.Name)
		}
	}
	if !seenStartPcap {
		t.Fatal("start_pcap not registered with allowCapture=true")
	}
}

// MCP-5: a full page (returned count == limit) hints the before_seq cursor to
// keep paging older; a partial page (fewer entries than limit, meaning the
// buffer's been exhausted) does not.
func TestHandleListEntriesBeforeSeqAndNextPageHint(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		entries := []api.Entry{
			{ID: "a", Seq: 10, Protocol: api.ProtocolHTTP},
			{ID: "b", Seq: 9, Protocol: api.ProtocolHTTP},
		}
		if limit < len(entries) {
			entries = entries[:limit]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer srv.Close()

	s := &Server{hubURL: srv.URL, http: &http.Client{Timeout: 5 * time.Second}, log: discardLogger()}

	out, err := s.handleListEntries(context.Background(), map[string]any{"limit": float64(2), "before_seq": float64(11)})
	if err != nil {
		t.Fatalf("handleListEntries: %v", err)
	}
	if !strings.Contains(gotQuery, "before_seq=11") {
		t.Errorf("hub query = %q, want it to carry before_seq=11", gotQuery)
	}
	if !strings.Contains(out, `"seq": 10`) {
		t.Errorf("output missing a seq field:\n%s", out)
	}
	if !strings.Contains(out, "next page: call again with before_seq=9") {
		t.Errorf("full page should hint the next before_seq:\n%s", out)
	}

	out, err = s.handleListEntries(context.Background(), map[string]any{"limit": float64(5)})
	if err != nil {
		t.Fatalf("handleListEntries: %v", err)
	}
	if strings.Contains(out, "next page") {
		t.Errorf("a partial page must not hint a next page:\n%s", out)
	}
}

// MCP-5: oversized tool output is capped with an explicit, non-silent notice
// rather than an unbounded (or silently mangled) result.
func TestCapText(t *testing.T) {
	short := "hello"
	if got := capText(short); got != short {
		t.Errorf("capText(short) = %q, want unchanged", got)
	}

	big := strings.Repeat("a", maxToolTextBytes+500)
	got := capText(big)
	if len(got) <= maxToolTextBytes {
		t.Fatalf("capped output should include the truncation notice, making it longer than the raw cap")
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxToolTextBytes)) {
		t.Error("capText should preserve the first maxToolTextBytes bytes verbatim")
	}
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "before_seq") {
		t.Errorf("capText output missing an explicit truncation notice: %q", got[len(got)-200:])
	}

	// A multi-byte rune sitting right at the cut point must not be split
	// into invalid UTF-8.
	multibyte := strings.Repeat("a", maxToolTextBytes-1) + "é" + "bbb" // é is 2 bytes in UTF-8
	got = capText(multibyte)
	notice := strings.Index(got, "\n\n... [truncated")
	if notice < 0 {
		t.Fatal("expected a truncation notice")
	}
	if !utf8.ValidString(got[:notice]) {
		t.Errorf("truncated prefix is not valid UTF-8: %q", got[:notice])
	}
}

package hub

// EXT-4: the export sink taps the ingest fan-out and ships entries to a
// rotating JSONL file and/or a batched webhook, strictly non-blocking. These
// tests cover both backends end to end, rotation, and the drop-on-backpressure
// policy that keeps export from ever slowing capture.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

// rawEntry returns the cached-JSON form of an entry, as the store would hand
// the exporter.
func rawEntry(id string) []byte {
	b, _ := json.Marshal(&api.Entry{ID: id, Protocol: api.ProtocolHTTP, Status: "success"})
	return b
}

// runExporter starts e.run on a fresh context and returns a stop func that
// cancels it and blocks until the final flush completes (e.done closed).
func runExporter(e *exporter) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go e.run(ctx)
	return func() {
		cancel()
		<-e.done
	}
}

func TestExportJSONLFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	e := newExporter(discardLogger(), ExportOptions{File: path})
	if e == nil {
		t.Fatal("newExporter returned nil for a configured file backend")
	}
	stop := runExporter(e)

	const n = 20
	for i := 0; i < n; i++ {
		e.export(rawEntry("e" + strconv.Itoa(i)))
	}
	stop() // drains + closes the file before we read it

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export file: %v", err)
	}
	var lines int
	for _, line := range splitLines(data) {
		var got api.Entry
		if err := json.Unmarshal(line, &got); err != nil {
			t.Fatalf("line %d is not valid entry JSON: %v (%q)", lines, err, line)
		}
		if got.ID == "" {
			t.Errorf("line %d decoded to an entry with no id", lines)
		}
		lines++
	}
	if lines != n {
		t.Fatalf("wrote %d JSONL lines, want %d", lines, n)
	}
}

func TestExportFileRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entries.jsonl")
	// A tiny cap forces a rotation after the first couple of records.
	e := newExporter(discardLogger(), ExportOptions{File: path, FileMaxBytes: 40})
	stop := runExporter(e)
	for i := 0; i < 20; i++ {
		e.export(rawEntry("entry-" + strconv.Itoa(i)))
	}
	stop()

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotated file %s.1: %v", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected a live file %s after rotation: %v", path, err)
	}
}

func TestExportWebhook(t *testing.T) {
	got := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("webhook Content-Type = %q, want application/json", ct)
		}
		buf, _ := io.ReadAll(r.Body)
		got <- buf
	}))
	defer srv.Close()

	e := newExporter(discardLogger(), ExportOptions{Webhook: srv.URL, WebhookInterval: 20 * time.Millisecond})
	stop := runExporter(e)
	for i := 0; i < 3; i++ {
		e.export(rawEntry("w" + strconv.Itoa(i)))
	}

	select {
	case body := <-got:
		var arr []api.Entry
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Fatalf("webhook body is not a JSON array of entries: %v (%q)", err, body)
		}
		if len(arr) != 3 {
			t.Fatalf("webhook received %d entries, want 3", len(arr))
		}
		if arr[0].ID != "w0" || arr[2].ID != "w2" {
			t.Errorf("webhook entries out of order: %v", []string{arr[0].ID, arr[2].ID})
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook never received a batch")
	}
	stop()
}

func TestExportDropsOnBackpressure(t *testing.T) {
	// Never start run(), so nothing drains the buffer: past its capacity every
	// further export() must drop and count, never block.
	e := newExporter(discardLogger(), ExportOptions{File: "/dev/null"})
	for i := 0; i < exportBufferSize+50; i++ {
		e.export(rawEntry("d" + strconv.Itoa(i)))
	}
	if d := e.drops(); d < 50 {
		t.Fatalf("drops = %d, want at least 50 after overflowing the %d-entry buffer", d, exportBufferSize)
	}
}

func TestNewExporterNilWhenUnconfigured(t *testing.T) {
	if e := newExporter(discardLogger(), ExportOptions{}); e != nil {
		t.Fatal("newExporter should return nil when no backend is set")
	}
	// The nil exporter's methods must be safe no-ops.
	var nilE *exporter
	nilE.export(rawEntry("x"))
	if nilE.drops() != 0 {
		t.Fatal("nil exporter drops() should be 0")
	}
}

// splitLines splits on '\n', dropping a trailing empty element.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

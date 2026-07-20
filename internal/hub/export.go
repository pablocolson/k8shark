package hub

// EXT-4: optional export sink. The hub's ring buffer is otherwise a dead end —
// besides the front WebSocket, nothing forwards the entry stream to a SIEM, a
// data lake or a log pipeline. This taps the same ingest fan-out (see
// handleWorker's MsgEntry case) and ships a copy out to one or both of:
//
//   - a rotating JSONL file (one api.Entry JSON per line), and
//   - a webhook (batched HTTP POST of a JSON array of entries).
//
// This is integration/fan-out, not local storage (distinct from the planned
// HUB-1 persistence). OTLP logs export is deferred to a later pass.
//
// Both backends are opt-in and strictly non-blocking: the export path must
// never slow or block ingestion. A single buffered channel absorbs bursts and
// a lone background goroutine drains it; on overflow the newest entry is
// dropped and counted (k8shark_hub_export_dropped_total), mirroring the worker
// sink's emit discipline (internal/worker/sink.go).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

const (
	// exportBufferSize bounds the in-memory hand-off between ingest and the
	// drain goroutine. Large enough to ride out a slow-webhook blip; past it,
	// export drops rather than blocking capture.
	exportBufferSize = 4096
	// defaultExportFileMaxBytes is the JSONL rotation threshold when unset.
	defaultExportFileMaxBytes int64 = 100 << 20 // 100 MiB
	// exportFileRotations bounds retained rotated files: <path>.1 … <path>.N.
	// The oldest is deleted on each rotation, so disk use stays bounded.
	exportFileRotations = 5
	// exportFilePerm is the mode for a freshly created JSONL file.
	exportFilePerm os.FileMode = 0o644
	// defaultWebhookInterval flushes a partial batch this often when unset.
	defaultWebhookInterval = 2 * time.Second
	// exportWebhookMaxBatch flushes early once this many entries accumulate,
	// whichever comes first with the interval.
	exportWebhookMaxBatch = 500
	// exportWebhookTimeout bounds a single POST (the in-flight limit is one
	// POST at a time, so a stuck sink can't wedge the drain past this).
	exportWebhookTimeout = 10 * time.Second
	// exportShutdownTimeout bounds how long Run waits for a final flush so a
	// wedged sink can't hold shutdown open.
	exportShutdownTimeout = 5 * time.Second
)

// newline terminates each JSONL record (raw entry JSON carries none).
var newline = []byte{'\n'}

// ExportOptions configures the export sink. Both backends are independent:
// either, both, or neither may be set. When neither is set newExporter returns
// nil and the hub pays nothing.
type ExportOptions struct {
	// File, when set, appends one api.Entry JSON per line to this path,
	// rotating to <path>.1 … once it grows past FileMaxBytes.
	File string
	// FileMaxBytes overrides the rotation threshold (0 = default).
	FileMaxBytes int64
	// Webhook, when set, POSTs batches of entries (a JSON array) to this URL.
	Webhook string
	// WebhookInterval overrides the partial-batch flush interval (0 = default).
	WebhookInterval time.Duration
}

// exporter fans captured entries out to the configured backends. Construct with
// newExporter, start the drain goroutine once with run, feed it entries with
// export (nil-safe), and read its drop counter with drops.
type exporter struct {
	log *slog.Logger

	// ch carries each entry's cached JSON (the store's immutable bytes) to the
	// drain goroutine. Buffered so export never blocks ingestion.
	ch      chan []byte
	dropped atomic.Uint64 // entries/batches lost to a full buffer or failed delivery
	done    chan struct{} // closed when run returns (shutdown flush barrier)

	// file backend (touched only by the drain goroutine).
	filePath     string
	fileMaxBytes int64
	file         *os.File
	fileSize     int64

	// webhook backend (touched only by the drain goroutine).
	webhookURL      string
	webhookInterval time.Duration
	client          *http.Client
	batch           [][]byte
}

// newExporter builds an exporter, or returns nil when no backend is configured
// (so the whole feature is a no-op cost on an unconfigured hub — export, drops
// and stopExporter are all nil-safe).
func newExporter(log *slog.Logger, opts ExportOptions) *exporter {
	if opts.File == "" && opts.Webhook == "" {
		return nil
	}
	e := &exporter{
		log:  log,
		ch:   make(chan []byte, exportBufferSize),
		done: make(chan struct{}),
	}
	if opts.File != "" {
		e.filePath = opts.File
		e.fileMaxBytes = opts.FileMaxBytes
		if e.fileMaxBytes <= 0 {
			e.fileMaxBytes = defaultExportFileMaxBytes
		}
	}
	if opts.Webhook != "" {
		e.webhookURL = opts.Webhook
		e.webhookInterval = opts.WebhookInterval
		if e.webhookInterval <= 0 {
			e.webhookInterval = defaultWebhookInterval
		}
		e.client = &http.Client{Timeout: exportWebhookTimeout}
	}
	return e
}

// export queues an entry's cached JSON for background delivery. Nil-safe (an
// unconfigured hub holds a nil exporter). Never blocks: a full buffer drops the
// newest entry and counts it, so export can never slow ingestion.
func (e *exporter) export(raw []byte) {
	if e == nil || raw == nil {
		return
	}
	select {
	case e.ch <- raw:
	default:
		if n := e.dropped.Add(1); n%1000 == 0 {
			e.log.Warn("export buffer full, dropping entries", "dropped", n)
		}
	}
}

// drops reports how many entries/batches the sink has dropped. Nil-safe.
func (e *exporter) drops() uint64 {
	if e == nil {
		return 0
	}
	return e.dropped.Load()
}

// run drains the buffer to the configured backends until ctx is cancelled,
// then flushes whatever remains and closes done. Start once, from Server.Run.
func (e *exporter) run(ctx context.Context) {
	defer close(e.done)

	if e.filePath != "" {
		if err := e.openFile(); err != nil {
			e.log.Error("export file open failed; file export disabled", "path", e.filePath, "err", err)
			e.filePath = ""
		}
	}
	defer e.closeFile()

	var tick <-chan time.Time
	if e.webhookURL != "" {
		t := time.NewTicker(e.webhookInterval)
		defer t.Stop()
		tick = t.C
	}

	for {
		select {
		case <-ctx.Done():
			// Clean shutdown: drain what's already buffered, then flush a final
			// webhook batch, so the last few entries aren't silently discarded.
			for {
				select {
				case raw := <-e.ch:
					e.consume(raw)
				default:
					e.flushWebhook()
					return
				}
			}
		case raw := <-e.ch:
			e.consume(raw)
		case <-tick:
			e.flushWebhook()
		}
	}
}

// consume writes one entry to every configured backend, flushing the webhook
// batch early once it fills.
func (e *exporter) consume(raw []byte) {
	e.writeFile(raw)
	if e.webhookURL != "" {
		e.batch = append(e.batch, raw)
		if len(e.batch) >= exportWebhookMaxBatch {
			e.flushWebhook()
		}
	}
}

// --- JSONL file backend ----------------------------------------------------

func (e *exporter) openFile() error {
	f, err := os.OpenFile(e.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, exportFilePerm)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	e.file = f
	e.fileSize = info.Size()
	return nil
}

// writeFile appends one JSONL record, rotating once the file grows past the
// size cap. A write error disables the file backend rather than wedging the
// drain (the webhook, if any, keeps working).
func (e *exporter) writeFile(raw []byte) {
	if e.file == nil {
		return
	}
	n, err := e.file.Write(raw)
	if err == nil {
		var m int
		m, err = e.file.Write(newline)
		n += m
	}
	if err != nil {
		e.log.Error("export file write failed; disabling file export", "err", err)
		e.closeFile()
		return
	}
	e.fileSize += int64(n)
	if e.fileSize >= e.fileMaxBytes {
		e.rotate()
	}
}

// rotate shifts <path>.(N-1) → <path>.N (dropping the oldest), moves the live
// file to <path>.1, and reopens a fresh live file. Best-effort: any failure
// disables the file backend instead of blocking the drain goroutine.
func (e *exporter) rotate() {
	e.closeFile()
	_ = os.Remove(fmt.Sprintf("%s.%d", e.filePath, exportFileRotations))
	for i := exportFileRotations - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", e.filePath, i), fmt.Sprintf("%s.%d", e.filePath, i+1))
	}
	if err := os.Rename(e.filePath, e.filePath+".1"); err != nil {
		e.log.Error("export file rotate failed; disabling file export", "err", err)
		return
	}
	if err := e.openFile(); err != nil {
		e.log.Error("export file reopen after rotate failed; disabling file export", "err", err)
	}
}

func (e *exporter) closeFile() {
	if e.file != nil {
		_ = e.file.Close()
		e.file = nil
	}
}

// --- webhook backend -------------------------------------------------------

// flushWebhook POSTs the accumulated batch as a JSON array and clears it. The
// batch is dropped (counted) if the request can't be built, the POST fails or
// times out, or the sink answers non-2xx — the export path must never wedge on
// an unreachable endpoint.
func (e *exporter) flushWebhook() {
	if e.webhookURL == "" || len(e.batch) == 0 {
		return
	}
	body := assembleEntryArray(e.batch)
	n := len(e.batch)
	e.batch = e.batch[:0]

	req, err := http.NewRequest(http.MethodPost, e.webhookURL, bytes.NewReader(body))
	if err != nil {
		e.countDrops(n)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		e.countDrops(n)
		e.log.Warn("export webhook POST failed; dropping batch", "entries", n, "err", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		e.countDrops(n)
		e.log.Warn("export webhook non-2xx; dropping batch", "status", resp.StatusCode, "entries", n)
	}
}

// countDrops records n dropped entries, logging on the same 1000-entry cadence
// as export so a persistently failing webhook stays visible.
func (e *exporter) countDrops(n int) {
	prev := e.dropped.Load()
	total := e.dropped.Add(uint64(n))
	if prev/1000 != total/1000 {
		e.log.Warn("export webhook dropping entries", "dropped", total)
	}
}

// assembleEntryArray splices cached per-entry JSON into a single JSON array,
// mirroring assembleBatch (server.go) but without the WebSocket envelope — the
// webhook body is a bare array of api.Entry objects.
func assembleEntryArray(raws [][]byte) []byte {
	size := 2 + len(raws) // '[' ']' plus one comma per element (overcounts by 1)
	for _, r := range raws {
		size += len(r)
	}
	b := make([]byte, 0, size)
	b = append(b, '[')
	for i, r := range raws {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, r...)
	}
	return append(b, ']')
}

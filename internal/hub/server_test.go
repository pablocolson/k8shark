package hub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

func TestHandleMetrics(t *testing.T) {
	s := New(slog.Default(), Options{})
	s.store.add(&api.Entry{ID: "x", Protocol: api.ProtocolHTTP, Status: "success", Timestamp: time.Now()})

	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	for _, want := range []string{
		"# HELP", "# TYPE",
		"k8shark_hub_entries_total",
		"k8shark_hub_front_clients",
		"k8shark_hub_workers",
		"k8shark_hub_broadcast_dropped_total",
		"k8shark_hub_entries_total 1",
		"k8shark_hub_entries_per_sec",
		"k8shark_hub_buffer_capacity",
		"k8shark_hub_buffer_entries 1",
		`k8shark_hub_entries_by_protocol_total{protocol="http"} 1`,
		`k8shark_hub_entries_by_status_total{status="success"} 1`,
		// Not in a cluster in this test, so the resolver is disabled and never
		// fails a refresh -- the counter must still be exposed at 0, not omitted.
		"k8shark_hub_k8s_enrich_failures_total 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n%s", want, body)
		}
	}

	// With a registered worker, per-node series appear.
	s.workerUpdate("node-a", func(wi *workerInfo) { wi.Connected = true; wi.Entries = 7; wi.Dropped = 3 })
	rec = httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body = rec.Body.String()
	for _, want := range []string{
		`k8shark_worker_entries_total{node="node-a"} 7`,
		`k8shark_worker_dropped_total{node="node-a"} 3`,
		`k8shark_worker_connected{node="node-a"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n%s", want, body)
		}
	}
}

// since/until narrow /api/entries to a time window.
func TestHandleEntriesTimeRange(t *testing.T) {
	s := New(slog.Default(), Options{})
	now := time.Now()
	s.store.add(&api.Entry{ID: "old", Protocol: api.ProtocolHTTP, Timestamp: now.Add(-time.Hour)})
	s.store.add(&api.Entry{ID: "new", Protocol: api.ProtocolHTTP, Timestamp: now})

	rec := httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?since=30m", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"new"`) || strings.Contains(body, `"old"`) {
		t.Errorf("since=30m returned wrong window:\n%s", body)
	}

	rec = httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?since=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("since=bogus status = %d, want 400", rec.Code)
	}
}

// ?before_seq= pages strictly-older entries by the numeric Seq the store
// assigns on ingest (HUB-3), and rejects a non-numeric value with 400.
func TestHandleEntriesBeforeSeq(t *testing.T) {
	s := New(slog.Default(), Options{})
	s.store.add(&api.Entry{ID: "a", Protocol: api.ProtocolHTTP})
	s.store.add(&api.Entry{ID: "b", Protocol: api.ProtocolHTTP})
	s.store.add(&api.Entry{ID: "c", Protocol: api.ProtocolHTTP})

	rec := httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?before_seq=2", nil))
	var got []api.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding entries: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("before_seq=2 = %+v, want just [a]", got)
	}

	rec = httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?before_seq=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("before_seq=bogus status = %d, want 400", rec.Code)
	}
}

// ?sort=&order= returns the top-N entries by numeric field value, and
// rejects a bad field/order with 400 instead of silently ignoring it.
func TestHandleEntriesSort(t *testing.T) {
	s := New(slog.Default(), Options{})
	s.store.add(&api.Entry{ID: "a", Protocol: api.ProtocolHTTP, ElapsedMs: 10})
	s.store.add(&api.Entry{ID: "b", Protocol: api.ProtocolHTTP, ElapsedMs: 90})
	s.store.add(&api.Entry{ID: "c", Protocol: api.ProtocolHTTP, ElapsedMs: 30})

	rec := httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?sort=elapsedMs&limit=2", nil))
	var got []api.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding entries: %v", err)
	}
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "c" {
		t.Fatalf("sort=elapsedMs (default desc) limit=2 = %+v, want [b(90), c(30)]", got)
	}

	rec = httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?sort=elapsedMs&order=asc&limit=2", nil))
	got = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding entries: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("sort=elapsedMs order=asc limit=2 = %+v, want [a(10), c(30)]", got)
	}

	rec = httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?sort=protocol", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("sort on a non-numeric field status = %d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?sort=elapsedMs&order=sideways", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad order status = %d, want 400", rec.Code)
	}
}

// An unknown filter field is a 400, not an empty result set.
func TestHandleEntriesUnknownFilterField(t *testing.T) {
	s := New(slog.Default(), Options{})
	rec := httptest.NewRecorder()
	s.handleEntries(rec, httptest.NewRequest(http.MethodGet, "/api/entries?filter=http.status_code+%3D%3D+500", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field status = %d, want 400", rec.Code)
	}
}

func TestWithAuth(t *testing.T) {
	s := New(slog.Default(), Options{APIToken: "sekret"})
	h := s.withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	get := func(path, header, query string) int {
		req := httptest.NewRequest(http.MethodGet, path+query, nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := get("/api/entries", "", ""); got != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", got)
	}
	if got := get("/api/entries", "Bearer wrong", ""); got != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", got)
	}
	if got := get("/api/entries", "Bearer sekret", ""); got != http.StatusOK {
		t.Errorf("bearer token = %d, want 200", got)
	}
	if got := get("/ws", "", "?token=sekret"); got != http.StatusOK {
		t.Errorf("query token = %d, want 200", got)
	}
	if got := get("/healthz", "", ""); got != http.StatusOK {
		t.Errorf("healthz should stay open, got %d", got)
	}

	// No token configured: everything stays open.
	open := New(slog.Default(), Options{}).withAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/entries", nil)
	rec := httptest.NewRecorder()
	open.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("open hub = %d, want 200", rec.Code)
	}
}

// The /api/workers registry keeps disconnected workers, with their counters.
func TestHandleWorkers(t *testing.T) {
	s := New(slog.Default(), Options{})
	s.workerUpdate("node-b", func(wi *workerInfo) { wi.Connected = true; wi.Entries = 5 })
	s.workerUpdate("node-a", func(wi *workerInfo) { wi.Connected = false; wi.Dropped = 9 })

	rec := httptest.NewRecorder()
	s.handleWorkers(rec, httptest.NewRequest(http.MethodGet, "/api/workers", nil))
	var out []workerInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding workers: %v", err)
	}
	if len(out) != 2 || out[0].Node != "node-a" || out[1].Node != "node-b" {
		t.Fatalf("workers = %+v, want node-a then node-b", out)
	}
	if out[0].Dropped != 9 || out[0].Connected {
		t.Errorf("node-a = %+v, want disconnected with 9 drops", out[0])
	}
	if out[1].Entries != 5 || !out[1].Connected {
		t.Errorf("node-b = %+v, want connected with 5 entries", out[1])
	}

	// Pre-hello connections are not tracked.
	s.workerUpdate("unknown", func(wi *workerInfo) { wi.Entries++ })
	if len(s.workerSnapshot()) != 2 {
		t.Error(`"unknown" node leaked into the registry`)
	}
}

// gcWorkers prunes a disconnected worker once it's aged past workerGCTTL, but
// never a still-connected one and never a disconnected-but-recent one.
func TestGCWorkers(t *testing.T) {
	s := New(slog.Default(), Options{})
	old := time.Now().Add(-workerGCTTL - time.Minute)
	recent := time.Now().Add(-time.Minute)

	s.workerUpdate("stale", func(wi *workerInfo) { wi.Connected = false; wi.LastSeen = old })
	s.workerUpdate("fresh", func(wi *workerInfo) { wi.Connected = false; wi.LastSeen = recent })
	s.workerUpdate("stuck-connected", func(wi *workerInfo) { wi.Connected = true; wi.LastSeen = old })

	s.gcWorkers()

	present := map[string]bool{}
	for _, wi := range s.workerSnapshot() {
		present[wi.Node] = true
	}
	if present["stale"] {
		t.Error("a disconnected worker past workerGCTTL should have been GC'd")
	}
	if !present["fresh"] {
		t.Error("a recently-disconnected worker should not have been GC'd")
	}
	if !present["stuck-connected"] {
		t.Error("a still-connected worker must never be GC'd regardless of LastSeen")
	}
}

// TestSendWorkerCommand exercises the hub -> worker delivery path directly
// (no real WebSocket needed: workerConns just holds a channel per connected
// node, which handleWorker registers on MsgHello — see server.go).
func TestSendWorkerCommand(t *testing.T) {
	s := New(slog.Default(), Options{})
	nodeA := make(chan []byte, 8)
	nodeB := make(chan []byte, 8)
	s.workerConns["node-a"] = nodeA
	s.workerConns["node-b"] = nodeB

	// Targeting one node reaches only that node.
	if sent := s.sendWorkerCommand("node-a", api.WorkerCommand{Paused: true}); sent != 1 {
		t.Fatalf("sendWorkerCommand(node-a) sent = %d, want 1", sent)
	}
	select {
	case b := <-nodeA:
		var env api.Envelope
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("decoding delivered command: %v", err)
		}
		if env.Type != api.MsgWorkerCommand || env.WorkerCommand == nil || !env.WorkerCommand.Paused {
			t.Errorf("delivered envelope = %+v, want workerCommand{paused:true}", env)
		}
	default:
		t.Fatal("node-a received nothing")
	}
	select {
	case <-nodeB:
		t.Error("node-b should not have received node-a's command")
	default:
	}

	// An empty node targets every connected worker.
	if sent := s.sendWorkerCommand("", api.WorkerCommand{Paused: false}); sent != 2 {
		t.Fatalf("sendWorkerCommand(\"\") sent = %d, want 2", sent)
	}

	// An unknown node is a silent no-op, not an error.
	if sent := s.sendWorkerCommand("node-c", api.WorkerCommand{Paused: true}); sent != 0 {
		t.Fatalf("sendWorkerCommand(node-c) sent = %d, want 0 (not connected)", sent)
	}
}

func TestHandleWorkerCapture(t *testing.T) {
	s := New(slog.Default(), Options{})
	ch := make(chan []byte, 8)
	s.workerConns["node-a"] = ch

	body := strings.NewReader(`{"node":"node-a","paused":true}`)
	rec := httptest.NewRecorder()
	s.handleWorkerCapture(rec, httptest.NewRequest(http.MethodPost, "/api/workers/capture", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct{ Sent int }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Sent != 1 {
		t.Errorf("sent = %d, want 1", resp.Sent)
	}
	select {
	case <-ch:
	default:
		t.Error("node-a's channel received nothing")
	}

	// GET is rejected — this is a control action, not a read.
	rec = httptest.NewRecorder()
	s.handleWorkerCapture(rec, httptest.NewRequest(http.MethodGet, "/api/workers/capture", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

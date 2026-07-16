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
	s.store.add(&api.Entry{ID: "x", Protocol: api.ProtocolHTTP, Timestamp: time.Now()})

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

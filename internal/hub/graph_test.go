package hub

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

func TestServiceGraphAggregatesAndSorts(t *testing.T) {
	now := time.Now()
	web := api.Endpoint{IP: "10.0.0.1", Name: "web-abc123", Namespace: "shop", Workload: "web"}
	pay := api.Endpoint{IP: "10.0.0.2", Name: "payment-def456", Namespace: "shop", Workload: "payment"}
	// No workload: falls back to the pod/service name; no name either: the IP.
	svc := api.Endpoint{IP: "10.0.0.3", Name: "redis-0", Namespace: "data"}
	bare := api.Endpoint{IP: "10.0.0.4"}
	entries := []*api.Entry{
		mkEntry(now, api.ProtocolHTTP, "success", 10, web, pay),
		mkEntry(now, api.ProtocolHTTP, "error", 200, web, pay),
		mkEntry(now, api.ProtocolHTTP, "warning", 30, web, pay),
		mkEntry(now, api.ProtocolRedis, "success", 5, pay, svc),
		mkEntry(now, api.ProtocolTCP, "success", 1, pay, bare),
		// Reverse direction is a distinct edge, not merged with web→payment.
		mkEntry(now, api.ProtocolHTTP, "success", 2, pay, web),
	}

	edges := serviceGraph(entries, "")
	if len(edges) != 4 {
		t.Fatalf("got %d edges, want 4: %+v", len(edges), edges)
	}
	// Busiest edge first.
	e := edges[0]
	if e.Src != "shop/web" || e.Dst != "shop/payment" {
		t.Fatalf("edges[0] = %s→%s, want shop/web→shop/payment (busiest)", e.Src, e.Dst)
	}
	if e.Count != 3 || e.Errors != 1 || e.Warnings != 1 {
		t.Errorf("shop/web→shop/payment = count %d errors %d warnings %d, want 3/1/1", e.Count, e.Errors, e.Warnings)
	}

	byPair := map[string]GraphEdge{}
	for _, e := range edges {
		byPair[e.Src+"→"+e.Dst] = e
	}
	if _, ok := byPair["shop/payment→data/redis-0"]; !ok {
		t.Errorf("missing name-fallback edge shop/payment→data/redis-0: %+v", edges)
	}
	if _, ok := byPair["shop/payment→10.0.0.4"]; !ok {
		t.Errorf("missing IP-fallback edge shop/payment→10.0.0.4: %+v", edges)
	}
	if rev, ok := byPair["shop/payment→shop/web"]; !ok || rev.Count != 1 {
		t.Errorf("reverse edge shop/payment→shop/web = %+v, want a distinct count-1 edge", rev)
	}
}

func TestServiceGraphPercentiles(t *testing.T) {
	now := time.Now()
	src := api.Endpoint{IP: "1", Workload: "a"}
	dst := api.Endpoint{IP: "2", Workload: "b"}
	var entries []*api.Entry
	for i := int64(1); i <= 100; i++ {
		entries = append(entries, mkEntry(now, api.ProtocolHTTP, "success", i, src, dst))
	}
	edges := serviceGraph(entries, "")
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	e := edges[0]
	if e.P50Ms != 50 || e.P95Ms != 95 || e.MaxMs != 100 {
		t.Errorf("p50/p95/max = %d/%d/%d, want 50/95/100", e.P50Ms, e.P95Ms, e.MaxMs)
	}
}

func TestServiceGraphFocus(t *testing.T) {
	now := time.Now()
	web := api.Endpoint{IP: "10.0.0.1", Name: "web-abc123", Namespace: "shop", Workload: "web"}
	pay := api.Endpoint{IP: "10.0.0.2", Name: "payment-def456", Namespace: "shop", Workload: "payment"}
	db := api.Endpoint{IP: "10.0.0.3", Name: "pg-0", Namespace: "data", Workload: "pg"}
	entries := []*api.Entry{
		mkEntry(now, api.ProtocolHTTP, "success", 10, web, pay),
		mkEntry(now, api.ProtocolPostgres, "success", 5, pay, db),
		mkEntry(now, api.ProtocolHTTP, "success", 2, web, web),
	}

	// The focused service keeps both its inbound and outbound edges — the
	// caller chain in both directions — whether named by bare workload, bare
	// pod/service name, or the qualified node label.
	for _, focus := range []string{"payment", "payment-def456", "shop/payment"} {
		edges := serviceGraph(entries, focus)
		if len(edges) != 2 {
			t.Fatalf("focus=%q got %d edges, want 2 (in + out): %+v", focus, len(edges), edges)
		}
		for _, e := range edges {
			if e.Src != "shop/payment" && e.Dst != "shop/payment" {
				t.Errorf("focus=%q kept unrelated edge %s→%s", focus, e.Src, e.Dst)
			}
		}
	}

	if edges := serviceGraph(entries, "no-such-service"); len(edges) != 0 {
		t.Errorf("unmatched focus kept %d edges, want 0: %+v", len(edges), edges)
	}
}

func TestHandleGraph(t *testing.T) {
	s := New(slog.Default(), Options{})
	now := time.Now()
	web := api.Endpoint{IP: "10.0.0.1", Namespace: "shop", Workload: "web"}
	pay := api.Endpoint{IP: "10.0.0.2", Namespace: "shop", Workload: "payment"}
	s.store.add(mkEntry(now, api.ProtocolHTTP, "success", 10, web, pay))
	s.store.add(mkEntry(now, api.ProtocolHTTP, "error", 90, web, pay))
	s.store.add(mkEntry(now, api.ProtocolRedis, "success", 5, pay, api.Endpoint{IP: "10.0.0.3"}))

	// Through the full route tree, so a missing /api/graph registration in
	// handler() fails here rather than only in-cluster.
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/graph", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Edges []GraphEdge `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding graph: %v", err)
	}
	if len(out.Edges) != 2 {
		t.Fatalf("got %d edges, want 2: %+v", len(out.Edges), out.Edges)
	}
	e := out.Edges[0]
	if e.Src != "shop/web" || e.Dst != "shop/payment" || e.Count != 2 || e.Errors != 1 {
		t.Errorf("edges[0] = %+v, want shop/web→shop/payment count 2 errors 1", e)
	}
	if e.P50Ms != 10 || e.P95Ms != 90 || e.MaxMs != 90 {
		t.Errorf("edges[0] p50/p95/max = %d/%d/%d, want 10/90/90", e.P50Ms, e.P95Ms, e.MaxMs)
	}
	if e := out.Edges[1]; e.Src != "shop/payment" || e.Dst != "10.0.0.3" {
		t.Errorf("edges[1] = %+v, want shop/payment→10.0.0.3 (IP fallback)", e)
	}
}

// ?focus= restricts to edges touching one service; ?filter= (IFL) and
// ?since= narrow the input entries, with the same 400-on-bad-input contract
// as /api/summary.
func TestHandleGraphFocusFilterAndErrors(t *testing.T) {
	s := New(slog.Default(), Options{})
	now := time.Now()
	web := api.Endpoint{IP: "10.0.0.1", Namespace: "shop", Workload: "web"}
	pay := api.Endpoint{IP: "10.0.0.2", Namespace: "shop", Workload: "payment"}
	db := api.Endpoint{IP: "10.0.0.3", Namespace: "data", Workload: "pg"}
	s.store.add(mkEntry(now, api.ProtocolHTTP, "success", 10, web, pay))
	s.store.add(mkEntry(now, api.ProtocolPostgres, "error", 50, pay, db))
	s.store.add(mkEntry(now.Add(-time.Hour), api.ProtocolHTTP, "success", 1, web, pay))

	get := func(query string) (int, []GraphEdge) {
		rec := httptest.NewRecorder()
		s.handleGraph(rec, httptest.NewRequest(http.MethodGet, "/api/graph"+query, nil))
		var out struct {
			Edges []GraphEdge `json:"edges"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decoding graph: %v", err)
			}
		}
		return rec.Code, out.Edges
	}

	if _, edges := get("?focus=pg"); len(edges) != 1 || edges[0].Dst != "data/pg" {
		t.Errorf("focus=pg edges = %+v, want just shop/payment→data/pg", edges)
	}
	if _, edges := get("?filter=" + "protocol%20%3D%3D%20%22postgres%22"); len(edges) != 1 || edges[0].Src != "shop/payment" {
		t.Errorf("filter=protocol==postgres edges = %+v, want just shop/payment→data/pg", edges)
	}
	if _, edges := get("?since=30m"); len(edges) != 2 {
		t.Errorf("since=30m edges = %+v, want the hour-old entry excluded but both edges present", edges)
	} else if edges[0].Count != 1 {
		t.Errorf("since=30m busiest edge count = %d, want 1 (old web→payment entry excluded)", edges[0].Count)
	}

	// An unknown filter field is a 400, not an empty graph.
	if code, _ := get("?filter=no.such.field%20%3D%3D%201"); code != http.StatusBadRequest {
		t.Errorf("unknown filter field status = %d, want 400", code)
	}
	if code, _ := get("?since=bogus"); code != http.StatusBadRequest {
		t.Errorf("since=bogus status = %d, want 400", code)
	}
}

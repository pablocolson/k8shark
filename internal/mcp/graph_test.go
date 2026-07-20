package mcp

// MCP-4: get_service_graph relays /api/graph — the hub-side src→dst call
// graph — so an agent can walk the dependency chain around a failing service.
// These tests pin the relay: the tool is registered, hits /api/graph,
// propagates its filter/since/until/focus args as query params, and renders
// the hub's edges.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func TestGetServiceGraphRelaysAndRendersEdges(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	s, ts := fakeHub(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"edges":[` +
			`{"src":"shop/web","dst":"shop/payment","count":12,"errors":3,"warnings":1,"p50Ms":8,"p95Ms":40,"maxMs":90},` +
			`{"src":"shop/payment","dst":"data/pg","count":5,"errors":0,"warnings":0,"p50Ms":2,"p95Ms":6,"maxMs":9}]}`))
	}, "")
	defer ts.Close()

	m, isErr := callToolResult(t, s, "get_service_graph", map[string]any{
		"filter": `protocol == "http"`,
		"since":  "15m",
		"until":  "5m",
		"focus":  "shop/payment",
	})
	if isErr {
		t.Fatalf("get_service_graph failed: %s", resultText(t, m))
	}

	if gotPath != "/api/graph" {
		t.Errorf("hub path = %q, want /api/graph", gotPath)
	}
	for arg, want := range map[string]string{
		"filter": `protocol == "http"`,
		"since":  "15m",
		"until":  "5m",
		"focus":  "shop/payment",
	} {
		if got := gotQuery.Get(arg); got != want {
			t.Errorf("hub query %s = %q, want %q", arg, got, want)
		}
	}

	var out struct {
		Edges []struct {
			Src    string `json:"src"`
			Dst    string `json:"dst"`
			Count  int64  `json:"count"`
			Errors int64  `json:"errors"`
			P95Ms  int64  `json:"p95Ms"`
		} `json:"edges"`
	}
	text := resultText(t, m)
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("rendered output is not the relayed JSON: %v\n%s", err, text)
	}
	if len(out.Edges) != 2 {
		t.Fatalf("rendered %d edges, want 2:\n%s", len(out.Edges), text)
	}
	e := out.Edges[0]
	if e.Src != "shop/web" || e.Dst != "shop/payment" || e.Count != 12 || e.Errors != 3 || e.P95Ms != 40 {
		t.Errorf("edges[0] = %+v, want the hub's shop/web→shop/payment row intact", e)
	}
}

// Omitted args must not leak as empty query params — the hub treats a present
// filter/since/until differently from an absent one.
func TestGetServiceGraphOmitsAbsentArgs(t *testing.T) {
	var gotQuery url.Values
	s, ts := fakeHub(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"edges":[]}`))
	}, "")
	defer ts.Close()

	if m, isErr := callToolResult(t, s, "get_service_graph", map[string]any{}); isErr {
		t.Fatalf("get_service_graph failed: %s", resultText(t, m))
	}
	if len(gotQuery) != 0 {
		t.Errorf("hub query = %v, want no params when no args are given", gotQuery)
	}
}

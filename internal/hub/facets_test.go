package hub

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pablocolson/k8shark/pkg/api"
)

func entryWithNamespace(ns string) *api.Entry {
	e := sample()
	e.Destination.Namespace = ns
	return e
}

func TestFacetIndexValuesSortedCountDescValueAscTiebreak(t *testing.T) {
	f := newFacetIndex()
	// "shop" observed 3x, "platform" 3x (tie -> value asc), "kube-system" 1x.
	for i := 0; i < 3; i++ {
		f.observe(entryWithNamespace("shop"))
	}
	for i := 0; i < 3; i++ {
		f.observe(entryWithNamespace("platform"))
	}
	f.observe(entryWithNamespace("kube-system"))

	vals, tracked := f.values("dst.namespace", "", 10)
	if !tracked {
		t.Fatalf("expected dst.namespace to be tracked")
	}
	want := []FieldValue{
		{Value: "platform", Count: 3},
		{Value: "shop", Count: 3},
		{Value: "kube-system", Count: 1},
	}
	if len(vals) != len(want) {
		t.Fatalf("got %d values, want %d: %+v", len(vals), len(want), vals)
	}
	for i, w := range want {
		if vals[i] != w {
			t.Errorf("values[%d] = %+v, want %+v", i, vals[i], w)
		}
	}
}

func TestFacetIndexEvictsLowestCountAtCap(t *testing.T) {
	f := newFacetIndex()

	// Fill "node" (tracked, string) to exactly facetTrackCap distinct values,
	// each observed a distinct number of times so the lowest is unambiguous.
	// node-0 observed once, node-1 observed twice, ... node-(cap-1) observed
	// cap times. node-0 is the unique minimum.
	for i := 0; i < facetTrackCap; i++ {
		name := "node-" + itoa(i)
		times := i + 1
		for j := 0; j < times; j++ {
			e := sample()
			e.Node = name
			f.observe(e)
		}
	}

	vals, tracked := f.values("node", "", facetTrackCap+10)
	if !tracked {
		t.Fatalf("expected node to be tracked")
	}
	if len(vals) != facetTrackCap {
		t.Fatalf("got %d distinct values, want %d", len(vals), facetTrackCap)
	}

	// Introduce one brand-new value: this must evict node-0 (count 1, the
	// unique minimum), not some arbitrary entry.
	e := sample()
	e.Node = "node-new"
	f.observe(e)

	vals, _ = f.values("node", "", facetTrackCap+10)
	if len(vals) != facetTrackCap {
		t.Fatalf("after eviction got %d distinct values, want %d", len(vals), facetTrackCap)
	}
	for _, v := range vals {
		if v.Value == "node-0" {
			t.Errorf("expected node-0 (lowest count) to be evicted, but it survived")
		}
	}
	foundNew := false
	for _, v := range vals {
		if v.Value == "node-new" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Errorf("expected node-new to be present after eviction")
	}
}

func TestFacetIndexUntrackedFieldReturnsFalse(t *testing.T) {
	f := newFacetIndex()
	f.observe(sample())

	if _, tracked := f.values("postgres.query", "", 10); tracked {
		t.Errorf("postgres.query is freetext/untracked, values() should report tracked=false")
	}
	if _, tracked := f.values("request.path", "", 10); tracked {
		t.Errorf("request.path is freetext/untracked, values() should report tracked=false")
	}
	if _, tracked := f.values("nonexistent.field", "", 10); tracked {
		t.Errorf("unknown field should report tracked=false")
	}
}

func TestFacetIndexPrefixCaseInsensitive(t *testing.T) {
	f := newFacetIndex()
	f.observe(entryWithNamespace("Shop"))
	f.observe(entryWithNamespace("platform"))

	vals, tracked := f.values("dst.namespace", "sh", 10)
	if !tracked {
		t.Fatalf("expected dst.namespace to be tracked")
	}
	if len(vals) != 1 || vals[0].Value != "Shop" {
		t.Errorf("prefix %q = %+v, want just [Shop]", "sh", vals)
	}

	vals, _ = f.values("dst.namespace", "SH", 10)
	if len(vals) != 1 || vals[0].Value != "Shop" {
		t.Errorf("prefix %q = %+v, want just [Shop]", "SH", vals)
	}
}

func TestFacetIndexSnapshotOmitsFreetextAndCapsTopN(t *testing.T) {
	f := newFacetIndex()
	f.observe(sample())

	snap := f.snapshot()
	if _, ok := snap["postgres.query"]; ok {
		t.Errorf("snapshot should not include untracked/freetext fields")
	}
	if _, ok := snap["dst.namespace"]; !ok {
		t.Errorf("snapshot should include tracked fields")
	}

	for i := 0; i < facetTopN+10; i++ {
		e := sample()
		e.Destination.Namespace = "ns-" + itoa(i)
		f.observe(e)
	}
	snap = f.snapshot()
	if len(snap["dst.namespace"]) > facetTopN {
		t.Errorf("snapshot()[dst.namespace] has %d entries, want <= %d", len(snap["dst.namespace"]), facetTopN)
	}
}

// itoa avoids pulling in strconv just for test fixture names.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// --- ServeMux smoke test -----------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestServeMuxRoutesDoNotShadowExisting(t *testing.T) {
	srv := New(discardLogger(), "")
	srv.store.add(sample())

	mux := http.NewServeMux()
	mux.HandleFunc("/api/entries", srv.handleEntries)
	mux.HandleFunc("/api/entry/", srv.handleEntry)
	mux.HandleFunc("/api/stats", srv.handleStats)
	mux.HandleFunc("/api/fields", srv.handleFields)
	mux.HandleFunc("/api/fields/", srv.handleFieldValues)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	cases := []struct {
		path       string
		wantStatus int
	}{
		{"/api/entries", http.StatusOK},
		{"/api/stats", http.StatusOK},
		{"/api/fields", http.StatusOK},
		{"/api/fields/protocol/values", http.StatusOK},
		{"/api/fields/request.path/values", http.StatusOK}, // known but untracked -> 200 (empty list), not 404
		{"/api/fields/nonexistent/values", http.StatusNotFound},
	}
	for _, c := range cases {
		resp, err := http.Get(ts.URL + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != c.wantStatus {
			t.Errorf("GET %s = %d, want %d", c.path, resp.StatusCode, c.wantStatus)
		}
	}
}

// TestFreshStoreStillOffersStaticEnumValues verifies that /api/fields
// surfaces protocol's static domain (e.g. "amqp"/"valkey") even with zero
// traffic observed so far.
func TestFreshStoreStillOffersStaticEnumValues(t *testing.T) {
	srv := New(discardLogger(), "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fields", nil)
	srv.handleFields(rec, req)

	var body struct {
		Fields []fieldMeta `json:"fields"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	var proto *fieldMeta
	for i := range body.Fields {
		if body.Fields[i].Name == "protocol" {
			proto = &body.Fields[i]
		}
	}
	if proto == nil {
		t.Fatalf("protocol field missing from /api/fields response")
	}
	seen := map[string]bool{}
	for _, v := range proto.Values {
		seen[v.Value] = true
	}
	for _, want := range []string{"amqp", "valkey", "http"} {
		if !seen[want] {
			t.Errorf("expected protocol values to include %q even with no traffic, got %+v", want, proto.Values)
		}
	}
}

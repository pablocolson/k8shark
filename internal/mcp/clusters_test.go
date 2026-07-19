package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

func errEntry(id string, protocol api.Protocol, dstWorkload, status string, code int, summary string, ts time.Time) api.Entry {
	return api.Entry{
		ID:          id,
		Protocol:    protocol,
		Timestamp:   ts,
		Status:      status,
		StatusCode:  code,
		Destination: api.Endpoint{Workload: dstWorkload},
		Response:    api.Payload{Summary: summary},
	}
}

func TestClusterErrorsGroupsByNormalizedSignature(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []api.Entry{
		errEntry("1", api.ProtocolHTTP, "shop/checkout", "error", 404, "user 42 not found", base),
		errEntry("2", api.ProtocolHTTP, "shop/checkout", "error", 404, "user 99 not found", base.Add(time.Minute)),
		errEntry("3", api.ProtocolHTTP, "shop/checkout", "error", 404, "user 7 not found", base.Add(2*time.Minute)),
	}
	clusters := clusterErrors(entries)
	if len(clusters) != 1 {
		t.Fatalf("got %d clusters, want 1 (all three should collapse to one signature)", len(clusters))
	}
	c := clusters[0]
	if c.Count != 3 {
		t.Errorf("Count = %d, want 3", c.Count)
	}
	if c.Summary != "user # not found" {
		t.Errorf("Summary = %q, want digits collapsed to \"user # not found\"", c.Summary)
	}
	if !c.FirstSeen.Equal(base) {
		t.Errorf("FirstSeen = %v, want %v", c.FirstSeen, base)
	}
	if !c.LastSeen.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("LastSeen = %v, want %v", c.LastSeen, base.Add(2*time.Minute))
	}
}

func TestClusterErrorsSeparatesDifferentSignatures(t *testing.T) {
	base := time.Now()
	entries := []api.Entry{
		errEntry("1", api.ProtocolHTTP, "shop/checkout", "error", 500, "internal error", base),
		errEntry("2", api.ProtocolHTTP, "shop/cart", "error", 500, "internal error", base),         // different dst workload
		errEntry("3", api.ProtocolHTTP, "shop/checkout", "error", 503, "internal error", base),     // different status code
		errEntry("4", api.ProtocolPostgres, "shop/checkout", "error", 500, "internal error", base), // different protocol
	}
	clusters := clusterErrors(entries)
	if len(clusters) != 4 {
		t.Fatalf("got %d clusters, want 4 (each dimension differs)", len(clusters))
	}
}

func TestClusterErrorsSortedByCountDescending(t *testing.T) {
	base := time.Now()
	var entries []api.Entry
	for i := 0; i < 5; i++ {
		entries = append(entries, errEntry("small-"+string(rune('a'+i)), api.ProtocolHTTP, "shop/rare", "error", 500, "rare error", base))
	}
	for i := 0; i < 20; i++ {
		entries = append(entries, errEntry("big-"+string(rune('a'+i)), api.ProtocolHTTP, "shop/common", "error", 500, "common error", base))
	}
	clusters := clusterErrors(entries)
	if len(clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(clusters))
	}
	if clusters[0].DstWorkload != "shop/common" || clusters[0].Count != 20 {
		t.Errorf("clusters[0] = %+v, want the 20-count \"common\" cluster first", clusters[0])
	}
	if clusters[1].DstWorkload != "shop/rare" || clusters[1].Count != 5 {
		t.Errorf("clusters[1] = %+v, want the 5-count \"rare\" cluster second", clusters[1])
	}
}

func TestClusterErrorsExampleIDsCapped(t *testing.T) {
	base := time.Now()
	var entries []api.Entry
	for i := 0; i < 10; i++ {
		entries = append(entries, errEntry("id-"+string(rune('a'+i)), api.ProtocolHTTP, "shop/x", "error", 500, "boom", base))
	}
	clusters := clusterErrors(entries)
	if len(clusters) != 1 {
		t.Fatalf("got %d clusters, want 1", len(clusters))
	}
	if len(clusters[0].ExampleIDs) != maxClusterExamples {
		t.Errorf("ExampleIDs len = %d, want capped at %d (out of 10 matching entries)", len(clusters[0].ExampleIDs), maxClusterExamples)
	}
}

func TestClusterErrorsEmptyInput(t *testing.T) {
	clusters := clusterErrors(nil)
	if len(clusters) != 0 {
		t.Errorf("got %d clusters from no entries, want 0", len(clusters))
	}
}

// --- handleFindErrorClusters end-to-end -------------------------------------

func TestHandleFindErrorClustersEndToEnd(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("filter")
		wantSubstr := `status == "error" or status == "warning"`
		if !strings.Contains(f, wantSubstr) {
			t.Errorf("filter = %q, want it to contain %q", f, wantSubstr)
		}
		entries := []api.Entry{
			errEntry("1", api.ProtocolHTTP, "shop/checkout", "error", 500, "boom 1", base),
			errEntry("2", api.ProtocolHTTP, "shop/checkout", "error", 500, "boom 2", base.Add(time.Second)),
			errEntry("3", api.ProtocolDNS, "kube-system/coredns", "warning", 0, "SERVFAIL", base),
		}
		json.NewEncoder(w).Encode(entries)
	}))
	defer ts.Close()

	s := New(ts.URL, "", false, discardLogger())
	text, err := s.handleFindErrorClusters(context.Background(), map[string]any{"since": "15m"})
	if err != nil {
		t.Fatalf("handleFindErrorClusters: %v", err)
	}

	var out struct {
		ScannedEntries int            `json:"scannedEntries"`
		ClusterCount   int            `json:"clusterCount"`
		Clusters       []errorCluster `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshaling result: %v\n%s", err, text)
	}
	if out.ScannedEntries != 3 {
		t.Errorf("scannedEntries = %d, want 3", out.ScannedEntries)
	}
	if out.ClusterCount != 2 {
		t.Fatalf("clusterCount = %d, want 2", out.ClusterCount)
	}
	// The HTTP 500 cluster (2 entries) should sort before the DNS SERVFAIL
	// cluster (1 entry).
	if out.Clusters[0].Count != 2 || out.Clusters[0].Protocol != "http" {
		t.Errorf("clusters[0] = %+v, want the 2-count http cluster first", out.Clusters[0])
	}
}

func TestHandleFindErrorClustersCombinesUserFilter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("filter")
		if !strings.Contains(f, `dst.namespace == "shop"`) || !strings.Contains(f, `status == "error"`) {
			t.Errorf("filter = %q, want both the user filter and the error/warning base filter", f)
		}
		json.NewEncoder(w).Encode([]api.Entry{})
	}))
	defer ts.Close()

	s := New(ts.URL, "", false, discardLogger())
	if _, err := s.handleFindErrorClusters(context.Background(), map[string]any{
		"filter": `dst.namespace == "shop"`,
	}); err != nil {
		t.Fatalf("handleFindErrorClusters: %v", err)
	}
}

func TestHandleFindErrorClustersRespectsLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := time.Now()
		var entries []api.Entry
		for i := 0; i < 5; i++ {
			entries = append(entries, errEntry("id-"+string(rune('a'+i)), api.ProtocolHTTP, "shop/svc"+string(rune('a'+i)), "error", 500, "boom", base))
		}
		json.NewEncoder(w).Encode(entries)
	}))
	defer ts.Close()

	s := New(ts.URL, "", false, discardLogger())
	text, err := s.handleFindErrorClusters(context.Background(), map[string]any{"limit": float64(2)})
	if err != nil {
		t.Fatalf("handleFindErrorClusters: %v", err)
	}
	var out struct {
		ClusterCount int  `json:"clusterCount"`
		MoreClusters bool `json:"moreClusters"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if out.ClusterCount != 2 {
		t.Errorf("clusterCount = %d, want the limit of 2", out.ClusterCount)
	}
	if !out.MoreClusters {
		t.Error("moreClusters = false, want true (5 clusters exist, only 2 returned)")
	}
}

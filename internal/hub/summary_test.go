package hub

import (
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

// mkEntry builds a minimal entry for aggregation tests.
func mkEntry(ts time.Time, proto api.Protocol, status string, elapsed int64, src, dst api.Endpoint) *api.Entry {
	return &api.Entry{
		Protocol:    proto,
		Timestamp:   ts,
		ElapsedMs:   elapsed,
		Status:      status,
		Source:      src,
		Destination: dst,
	}
}

func TestSummarizeByWorkload(t *testing.T) {
	now := time.Now()
	web := api.Endpoint{IP: "10.0.0.1", Name: "web-abc123", Namespace: "shop", Workload: "web"}
	pay := api.Endpoint{IP: "10.0.0.2", Name: "payment-def456", Namespace: "shop", Workload: "payment"}
	entries := []*api.Entry{
		mkEntry(now, api.ProtocolHTTP, "success", 10, web, pay),
		mkEntry(now, api.ProtocolHTTP, "error", 200, web, pay),
		mkEntry(now, api.ProtocolRedis, "success", 5, pay, api.Endpoint{IP: "10.0.0.3"}),
	}

	groups := summarize(entries, "workload")
	byKey := map[string]GroupSummary{}
	for _, g := range groups {
		byKey[g.Key] = g
	}

	p := byKey["shop/payment"]
	if p.Count != 3 || p.Errors != 1 {
		t.Errorf("shop/payment = count %d errors %d, want 3/1", p.Count, p.Errors)
	}
	if len(p.Protocols) != 2 { // http + redis
		t.Errorf("shop/payment protocols = %v, want http+redis", p.Protocols)
	}
	if w := byKey["shop/web"]; w.Count != 2 {
		t.Errorf("shop/web count = %d, want 2", w.Count)
	}
	// The bare-IP peer falls back to its IP as the key.
	if ip := byKey["10.0.0.3"]; ip.Count != 1 {
		t.Errorf("10.0.0.3 count = %d, want 1", ip.Count)
	}
	// Busiest group first.
	if groups[0].Key != "shop/payment" {
		t.Errorf("groups[0] = %q, want shop/payment (busiest)", groups[0].Key)
	}
}

func TestSummarizeByNamespaceDedupesSelfCalls(t *testing.T) {
	now := time.Now()
	a := api.Endpoint{IP: "1", Namespace: "shop"}
	b := api.Endpoint{IP: "2", Namespace: "shop"}
	entries := []*api.Entry{mkEntry(now, api.ProtocolHTTP, "success", 1, a, b)}
	groups := summarize(entries, "namespace")
	if len(groups) != 1 || groups[0].Key != "shop" || groups[0].Count != 1 {
		t.Fatalf("groups = %+v, want one shop group with count 1", groups)
	}
}

func TestSummarizePercentiles(t *testing.T) {
	now := time.Now()
	dst := api.Endpoint{IP: "1", Name: "svc"}
	var entries []*api.Entry
	for i := int64(1); i <= 100; i++ {
		entries = append(entries, mkEntry(now, api.ProtocolHTTP, "success", i, api.Endpoint{IP: "2"}, dst))
	}
	groups := summarize(entries, "dst.name")
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]
	if g.P50Ms != 50 || g.P95Ms != 95 || g.MaxMs != 100 {
		t.Errorf("p50/p95/max = %d/%d/%d, want 50/95/100", g.P50Ms, g.P95Ms, g.MaxMs)
	}
}

func TestSummarizeUnknownFieldYieldsNothing(t *testing.T) {
	if validGroupBy("no.such.field") {
		t.Error("validGroupBy(no.such.field) = true, want false")
	}
	if validGroupBy("workload") == false || validGroupBy("dst.name") == false {
		t.Error("workload / dst.name should be valid groupBy keys")
	}
}

func TestTimeline(t *testing.T) {
	base := time.Date(2026, 7, 15, 14, 0, 0, 0, time.UTC)
	e := func(offset time.Duration, status string) *api.Entry {
		return mkEntry(base.Add(offset), api.ProtocolHTTP, status, 1, api.Endpoint{IP: "1"}, api.Endpoint{IP: "2"})
	}
	entries := []*api.Entry{
		e(10*time.Second, "success"),
		e(20*time.Second, "error"),
		e(70*time.Second, "success"),
		// outside the range: dropped
		e(-time.Minute, "success"),
		e(10*time.Minute, "success"),
	}
	buckets := timeline(entries, base, base.Add(2*time.Minute), time.Minute)
	if len(buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(buckets))
	}
	if buckets[0].Entries != 2 || buckets[0].Errors != 1 {
		t.Errorf("bucket[0] = %d entries %d errors, want 2/1", buckets[0].Entries, buckets[0].Errors)
	}
	if buckets[1].Entries != 1 {
		t.Errorf("bucket[1] entries = %d, want 1", buckets[1].Entries)
	}
	if buckets[2].Entries != 0 {
		t.Errorf("bucket[2] entries = %d, want 0 (zero-filled)", buckets[2].Entries)
	}

	// A pathologically small bucket over a huge range is truncated, not OOM.
	huge := timeline(nil, base, base.Add(24*time.Hour), time.Second)
	if len(huge) != maxTimelineBuckets {
		t.Errorf("got %d buckets, want cap %d", len(huge), maxTimelineBuckets)
	}
}

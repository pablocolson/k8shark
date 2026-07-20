package hub

// TST-7: benchmarks for the hub's hot paths — the code that absorbs the whole
// cluster's throughput. Without these there is no way to objectify a perf
// regression on ingest (store.add), fan-out (broadcast to N filtered clients),
// or per-entry filter evaluation (run for every entry × every client). Run
// with `make bench`; b.ReportAllocs surfaces per-entry allocations, the thing
// that bites at scale.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/pkg/api"
)

// benchEntry is a representative HTTP entry reused across the hub benchmarks.
func benchEntry() *api.Entry {
	return &api.Entry{
		ID:          "bench-1",
		Protocol:    api.ProtocolHTTP,
		Timestamp:   time.Unix(1_700_000_000, 0),
		ElapsedMs:   42,
		Node:        "node-a",
		Source:      api.Endpoint{IP: "10.0.0.1", Port: 40000, Namespace: "shop", Workload: "web"},
		Destination: api.Endpoint{IP: "10.0.0.2", Port: 80, Namespace: "prod", Workload: "api"},
		Request:     api.Payload{Method: "GET", Path: "/api/v1/users", Host: "api", Summary: "GET /api/v1/users"},
		Response:    api.Payload{StatusCode: 200, Summary: "200 OK"},
		Status:      "success",
		StatusCode:  200,
	}
}

const benchFilter = `protocol == "http" and response.status < 500`

func BenchmarkCompileFilter(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := CompileFilter(benchFilter); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPredicate(b *testing.B) {
	pred, err := CompileFilter(benchFilter)
	if err != nil {
		b.Fatal(err)
	}
	e := benchEntry()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = pred(e)
	}
}

func BenchmarkStoreAdd(b *testing.B) {
	st := newStore(config.EntryBufferSize)
	e := benchEntry()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.add(e)
	}
}

// BenchmarkBroadcast measures the fan-out cost — queueing entries into the
// pending window plus the batched, cached-JSON dispatch to each connected
// client — at a few client counts, the hub's main point of contention. Each
// iteration queues one entry; the flush is forced explicitly so the benchmark
// captures the full path deterministically instead of timer-dependent.
func BenchmarkBroadcast(b *testing.B) {
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("clients=%d", n), func(b *testing.B) {
			s := New(discardLogger(), Options{})
			pred, err := CompileFilter(benchFilter)
			if err != nil {
				b.Fatal(err)
			}
			stop := make(chan struct{})
			for i := 0; i < n; i++ {
				c := &frontClient{send: make(chan []byte, 8), pred: pred}
				s.frontClients[c] = struct{}{}
				go func(ch chan []byte) {
					for {
						select {
						case <-ch:
						case <-stop:
							return
						}
					}
				}(c.send)
			}
			b.Cleanup(func() { close(stop) })

			e := benchEntry()
			raw, err := json.Marshal(e)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.broadcast(e, raw)
				if i%64 == 63 {
					s.flushBroadcast()
				}
			}
			s.flushBroadcast()
		})
	}
}

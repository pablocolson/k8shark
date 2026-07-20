package hub

import (
	"sort"

	"github.com/pablocolson/k8shark/pkg/api"
)

// GraphEdge is one row of /api/graph: all buffered traffic from one source
// node to one destination node, with latency percentiles from ElapsedMs.
type GraphEdge struct {
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Count    int64  `json:"count"`
	Errors   int64  `json:"errors"`
	Warnings int64  `json:"warnings"`
	P50Ms    int64  `json:"p50Ms"`
	P95Ms    int64  `json:"p95Ms"`
	MaxMs    int64  `json:"maxMs"`
}

// nodeLabel names a graph node: the stable workload, then the pod/service
// name, then the raw IP, namespace-qualified when both parts are known — so
// it degrades gracefully outside a cluster. Shared with the "workload"
// summary grouping (groupKeys), keeping graph nodes and summary keys aligned.
func nodeLabel(ep *api.Endpoint) string {
	label := ep.Workload
	if label == "" {
		label = ep.Name
	}
	if label == "" {
		label = ep.IP
	}
	if label != "" && ep.Namespace != "" {
		label = ep.Namespace + "/" + label
	}
	return label
}

// focusMatches reports whether focus names ep: its bare workload, its bare
// pod/service name, or its namespace-qualified node label (which also covers
// the IP fallback for unresolved endpoints).
func focusMatches(ep *api.Endpoint, focus string) bool {
	return focus == ep.Workload || focus == ep.Name || focus == nodeLabel(ep)
}

// serviceGraph aggregates entries into src→dst call-graph edges, sorted by
// count descending (errors, then src/dst, as tiebreaks) so the busiest edges
// come first. Unlike the summary's endpoint-union grouping, direction is kept:
// a→b and b→a are distinct edges, and a self-call is one a→a edge. focus,
// when non-empty, keeps only edges with a matching endpoint (see
// focusMatches) — the "who calls X, and what does X call" view.
func serviceGraph(entries []*api.Entry, focus string) []GraphEdge {
	type acc struct {
		GraphEdge
		elapsed []int64
	}
	edges := map[[2]string]*acc{}
	for _, e := range entries {
		src, dst := nodeLabel(&e.Source), nodeLabel(&e.Destination)
		if src == "" || dst == "" {
			continue // an edge with an unnameable end says nothing useful
		}
		if focus != "" && !focusMatches(&e.Source, focus) && !focusMatches(&e.Destination, focus) {
			continue
		}
		key := [2]string{src, dst}
		a := edges[key]
		if a == nil {
			a = &acc{GraphEdge: GraphEdge{Src: src, Dst: dst}}
			edges[key] = a
		}
		a.Count++
		switch e.Status {
		case "error":
			a.Errors++
		case "warning":
			a.Warnings++
		}
		a.elapsed = append(a.elapsed, e.ElapsedMs)
	}

	out := make([]GraphEdge, 0, len(edges))
	for _, a := range edges {
		sort.Slice(a.elapsed, func(i, j int) bool { return a.elapsed[i] < a.elapsed[j] })
		a.P50Ms = percentile(a.elapsed, 50)
		a.P95Ms = percentile(a.elapsed, 95)
		if n := len(a.elapsed); n > 0 {
			a.MaxMs = a.elapsed[n-1]
		}
		out = append(out, a.GraphEdge)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Errors != out[j].Errors {
			return out[i].Errors > out[j].Errors
		}
		if out[i].Src != out[j].Src {
			return out[i].Src < out[j].Src
		}
		return out[i].Dst < out[j].Dst
	})
	return out
}

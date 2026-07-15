package hub

import (
	"sort"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

// GroupSummary is one row of /api/summary: the buffered traffic aggregated
// over one value of the group-by key. Latency percentiles come from ElapsedMs.
type GroupSummary struct {
	Key       string    `json:"key"`
	Count     int64     `json:"count"`
	Errors    int64     `json:"errors"`
	Warnings  int64     `json:"warnings"`
	Protocols []string  `json:"protocols,omitempty"`
	P50Ms     int64     `json:"p50Ms"`
	P95Ms     int64     `json:"p95Ms"`
	MaxMs     int64     `json:"maxMs"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// TimelineBucket is one fixed-width slice of /api/timeline.
type TimelineBucket struct {
	Start    time.Time `json:"start"`
	Entries  int64     `json:"entries"`
	Errors   int64     `json:"errors"`
	Warnings int64     `json:"warnings"`
}

// maxTimelineBuckets bounds the /api/timeline response so a tiny bucket over a
// huge range can't build an enormous slice.
const maxTimelineBuckets = 1000

// validGroupBy reports whether key is usable with summarize: one of the
// endpoint-union pseudo-fields, or any real IFL field.
func validGroupBy(key string) bool {
	if key == "workload" || key == "namespace" {
		return true
	}
	return fieldGetter(key) != nil
}

// groupKeys returns the group key(s) an entry contributes to.
//
// Two pseudo-fields aggregate over both endpoints (deduped per entry, so a
// self-call counts once): "namespace" groups by the k8s namespaces the entry
// touches, and "workload" by "namespace/workload" with a workload -> pod/svc
// name -> IP fallback so it degrades gracefully outside a cluster. Everything
// else is a plain IFL field.
func groupKeys(e *api.Entry, groupBy string) []string {
	switch groupBy {
	case "namespace":
		return endpointUnion(e, func(ep *api.Endpoint) string { return ep.Namespace })
	case "workload":
		return endpointUnion(e, func(ep *api.Endpoint) string {
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
		})
	default:
		get := fieldGetter(groupBy)
		if get == nil {
			return nil
		}
		if v := get(e); v != "" {
			return []string{v}
		}
		return nil
	}
}

// endpointUnion applies key to both endpoints, deduping the pair.
func endpointUnion(e *api.Entry, key func(*api.Endpoint) string) []string {
	src, dst := key(&e.Source), key(&e.Destination)
	switch {
	case src == "" && dst == "":
		return nil
	case src == "" || src == dst:
		return []string{dst}
	case dst == "":
		return []string{src}
	default:
		return []string{src, dst}
	}
}

// summarize aggregates entries per groupBy key, sorted by count descending
// (errors, then key, as tiebreaks) so the busiest groups come first.
func summarize(entries []*api.Entry, groupBy string) []GroupSummary {
	type acc struct {
		GroupSummary
		protocols map[string]struct{}
		elapsed   []int64
	}
	groups := map[string]*acc{}
	for _, e := range entries {
		for _, key := range groupKeys(e, groupBy) {
			a := groups[key]
			if a == nil {
				a = &acc{GroupSummary: GroupSummary{Key: key}, protocols: map[string]struct{}{}}
				a.FirstSeen = e.Timestamp
				groups[key] = a
			}
			a.Count++
			switch e.Status {
			case "error":
				a.Errors++
			case "warning":
				a.Warnings++
			}
			a.protocols[string(e.Protocol)] = struct{}{}
			a.elapsed = append(a.elapsed, e.ElapsedMs)
			if e.Timestamp.Before(a.FirstSeen) {
				a.FirstSeen = e.Timestamp
			}
			if e.Timestamp.After(a.LastSeen) {
				a.LastSeen = e.Timestamp
			}
		}
	}

	out := make([]GroupSummary, 0, len(groups))
	for _, a := range groups {
		for p := range a.protocols {
			a.Protocols = append(a.Protocols, p)
		}
		sort.Strings(a.Protocols)
		sort.Slice(a.elapsed, func(i, j int) bool { return a.elapsed[i] < a.elapsed[j] })
		a.P50Ms = percentile(a.elapsed, 50)
		a.P95Ms = percentile(a.elapsed, 95)
		if n := len(a.elapsed); n > 0 {
			a.MaxMs = a.elapsed[n-1]
		}
		out = append(out, a.GroupSummary)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Errors != out[j].Errors {
			return out[i].Errors > out[j].Errors
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// percentile reads the p-th percentile (nearest-rank) from a sorted slice.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (len(sorted)*p + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// timeline buckets entries into fixed steps across [since, until], zero-filled
// so gaps are visible. The range is truncated (oldest first) if it would
// exceed maxTimelineBuckets.
func timeline(entries []*api.Entry, since, until time.Time, bucket time.Duration) []TimelineBucket {
	start := since.Truncate(bucket)
	n := int(until.Sub(start)/bucket) + 1
	if n < 1 {
		n = 1
	}
	if n > maxTimelineBuckets {
		start = start.Add(time.Duration(n-maxTimelineBuckets) * bucket)
		n = maxTimelineBuckets
	}
	buckets := make([]TimelineBucket, n)
	for i := range buckets {
		buckets[i].Start = start.Add(time.Duration(i) * bucket)
	}
	for _, e := range entries {
		if e.Timestamp.Before(start) || e.Timestamp.After(until) {
			continue
		}
		i := int(e.Timestamp.Sub(start) / bucket)
		if i < 0 || i >= n {
			continue
		}
		buckets[i].Entries++
		switch e.Status {
		case "error":
			buckets[i].Errors++
		case "warning":
			buckets[i].Warnings++
		}
	}
	return buckets
}

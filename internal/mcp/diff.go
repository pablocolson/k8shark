package mcp

import (
	"context"
	"fmt"
	"net/url"
	"sort"
)

// summaryGroup mirrors one row of the hub's /api/summary response
// (hub.GroupSummary). Redeclared locally rather than importing internal/hub:
// the MCP server only ever talks to the hub over its HTTP/JSON contract (it
// can point at a remote hub), so it never imports hub's Go types directly.
type summaryGroup struct {
	Key      string `json:"key"`
	Count    int64  `json:"count"`
	Errors   int64  `json:"errors"`
	Warnings int64  `json:"warnings"`
	P95Ms    int64  `json:"p95Ms"`
}

type summaryResponse struct {
	Total  int            `json:"total"`
	Groups []summaryGroup `json:"groups"`
}

// groupDiff is one group's baseline-vs-current comparison.
type groupDiff struct {
	Key    string `json:"key"`
	Status string `json:"status"` // "appeared", "disappeared", or "changed"

	BaselineCount int64 `json:"baselineCount"`
	CurrentCount  int64 `json:"currentCount"`
	CountDelta    int64 `json:"countDelta"`

	BaselineErrors int64 `json:"baselineErrors"`
	CurrentErrors  int64 `json:"currentErrors"`
	ErrorsDelta    int64 `json:"errorsDelta"`

	BaselineErrorRate float64 `json:"baselineErrorRate"`
	CurrentErrorRate  float64 `json:"currentErrorRate"`
	ErrorRateDelta    float64 `json:"errorRateDelta"`

	BaselineP95Ms int64 `json:"baselineP95Ms"`
	CurrentP95Ms  int64 `json:"currentP95Ms"`
	P95DeltaMs    int64 `json:"p95DeltaMs"`
}

// diffTraffic compares two /api/summary group sets keyed by group Key. A
// group missing from baseline is "appeared" (its baseline fields are the
// zero value, so its deltas equal the current values outright); one missing
// from current is "disappeared" (mirror image — deltas are negative of the
// baseline values). Sorted by the strongest regression first: error-rate
// increase, then p95 increase, then volume increase, each as a tiebreak for
// the one before it — a single scalar "regression score" would need an
// arbitrary weighting between error rate and latency, whereas this ordering
// is exact and lets the caller (an agent, or a human) reason about each
// dimension directly from the numbers.
func diffTraffic(baseline, current []summaryGroup) []groupDiff {
	baseByKey := make(map[string]summaryGroup, len(baseline))
	for _, g := range baseline {
		baseByKey[g.Key] = g
	}
	curByKey := make(map[string]summaryGroup, len(current))
	for _, g := range current {
		curByKey[g.Key] = g
	}
	keys := make(map[string]struct{}, len(baseByKey)+len(curByKey))
	for k := range baseByKey {
		keys[k] = struct{}{}
	}
	for k := range curByKey {
		keys[k] = struct{}{}
	}

	out := make([]groupDiff, 0, len(keys))
	for k := range keys {
		b, hasBaseline := baseByKey[k]
		c, hasCurrent := curByKey[k]
		d := groupDiff{
			Key:               k,
			BaselineCount:     b.Count,
			CurrentCount:      c.Count,
			CountDelta:        c.Count - b.Count,
			BaselineErrors:    b.Errors,
			CurrentErrors:     c.Errors,
			ErrorsDelta:       c.Errors - b.Errors,
			BaselineErrorRate: errorRate(b),
			CurrentErrorRate:  errorRate(c),
			BaselineP95Ms:     b.P95Ms,
			CurrentP95Ms:      c.P95Ms,
			P95DeltaMs:        c.P95Ms - b.P95Ms,
		}
		d.ErrorRateDelta = d.CurrentErrorRate - d.BaselineErrorRate
		switch {
		case !hasBaseline:
			d.Status = "appeared"
		case !hasCurrent:
			d.Status = "disappeared"
		default:
			d.Status = "changed"
		}
		out = append(out, d)
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ErrorRateDelta != b.ErrorRateDelta {
			return a.ErrorRateDelta > b.ErrorRateDelta
		}
		if a.P95DeltaMs != b.P95DeltaMs {
			return a.P95DeltaMs > b.P95DeltaMs
		}
		if a.CountDelta != b.CountDelta {
			return a.CountDelta > b.CountDelta
		}
		return a.Key < b.Key
	})
	return out
}

func errorRate(g summaryGroup) float64 {
	if g.Count == 0 {
		return 0
	}
	return float64(g.Errors) / float64(g.Count)
}

// handleDiffTraffic fetches /api/summary for the baseline and current
// windows (same group_by/filter, different since/until) and diffs them —
// entirely MCP-side, no hub changes needed since /api/summary already
// accepts since/until/groupBy/filter.
func (s *Server) handleDiffTraffic(ctx context.Context, args map[string]any) (string, error) {
	baselineSince := argString(args, "baseline_since")
	baselineUntil := argString(args, "baseline_until")
	currentSince := argString(args, "current_since")
	currentUntil := argString(args, "current_until")
	if baselineSince == "" || baselineUntil == "" || currentSince == "" || currentUntil == "" {
		return "", fmt.Errorf("baseline_since, baseline_until, current_since and current_until are all required")
	}

	groupBy := argString(args, "group_by")
	if groupBy == "" {
		groupBy = "workload"
	}
	filter := argString(args, "filter")

	baseline, err := s.fetchSummary(ctx, groupBy, filter, baselineSince, baselineUntil)
	if err != nil {
		return "", fmt.Errorf("fetching baseline window: %w", err)
	}
	current, err := s.fetchSummary(ctx, groupBy, filter, currentSince, currentUntil)
	if err != nil {
		return "", fmt.Errorf("fetching current window: %w", err)
	}

	diffs := diffTraffic(baseline.Groups, current.Groups)
	limit := argInt(args, "limit", 20)
	if limit < 1 {
		limit = 1
	}
	if len(diffs) > limit {
		diffs = diffs[:limit]
	}

	return marshalPretty(map[string]any{
		"groupBy":        groupBy,
		"baselineWindow": map[string]string{"since": baselineSince, "until": baselineUntil},
		"currentWindow":  map[string]string{"since": currentSince, "until": currentUntil},
		"baselineTotal":  baseline.Total,
		"currentTotal":   current.Total,
		"groups":         diffs,
	}, "diff")
}

// fetchSummary GETs /api/summary for one window. limit=1000 (well above the
// tool's own result limit) so the diff isn't skewed by /api/summary's own
// default 25-group truncation cutting off a group in one window but not the
// other.
func (s *Server) fetchSummary(ctx context.Context, groupBy, filter, since, until string) (*summaryResponse, error) {
	q := url.Values{}
	q.Set("groupBy", groupBy)
	if filter != "" {
		q.Set("filter", filter)
	}
	q.Set("since", since)
	q.Set("until", until)
	q.Set("limit", "1000")

	var resp summaryResponse
	if err := s.getJSON(ctx, "/api/summary?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

package mcp

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

// errorClusterFetchLimit bounds how many raw error/warning entries
// find_error_clusters pulls from /api/entries before clustering — an
// internal cap on the fetch, independent of the tool's own "limit" argument
// (which caps how many *clusters* come back).
const errorClusterFetchLimit = 2000

// maxClusterExamples bounds how many example entry IDs each cluster reports.
const maxClusterExamples = 3

// digitsRE collapses numeric runs (ids, ports, byte counts, ...) in a
// response summary so "user 42 not found" and "user 99 not found" cluster
// together instead of each getting their own one-off signature.
var digitsRE = regexp.MustCompile(`\d+`)

// errorCluster is one group of error/warning entries sharing a signature.
type errorCluster struct {
	Protocol    string    `json:"protocol"`
	DstWorkload string    `json:"dstWorkload,omitempty"`
	Status      string    `json:"status"`
	StatusCode  int       `json:"statusCode,omitempty"`
	Summary     string    `json:"summary"` // normalized (digits collapsed)
	Count       int       `json:"count"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	ExampleIDs  []string  `json:"exampleIds"`
}

// clusterErrors groups entries by (protocol, dst workload, status/code, a
// digit-normalized response summary), sorted by count descending — the
// busiest error/warning pattern first, since that's almost always the one
// worth investigating first.
func clusterErrors(entries []api.Entry) []errorCluster {
	byKey := map[string]*errorCluster{}
	var order []string

	for _, e := range entries {
		dstWorkload := e.Destination.Workload
		summary := digitsRE.ReplaceAllString(e.Response.Summary, "#")
		key := string(e.Protocol) + "|" + dstWorkload + "|" + e.Status + "|" +
			strconv.Itoa(e.StatusCode) + "|" + summary

		c, ok := byKey[key]
		if !ok {
			c = &errorCluster{
				Protocol:    string(e.Protocol),
				DstWorkload: dstWorkload,
				Status:      e.Status,
				StatusCode:  e.StatusCode,
				Summary:     summary,
				FirstSeen:   e.Timestamp,
				LastSeen:    e.Timestamp,
			}
			byKey[key] = c
			order = append(order, key)
		}
		c.Count++
		if e.Timestamp.Before(c.FirstSeen) {
			c.FirstSeen = e.Timestamp
		}
		if e.Timestamp.After(c.LastSeen) {
			c.LastSeen = e.Timestamp
		}
		if len(c.ExampleIDs) < maxClusterExamples {
			c.ExampleIDs = append(c.ExampleIDs, e.ID)
		}
	}

	out := make([]errorCluster, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Summary < out[j].Summary
	})
	return out
}

// handleFindErrorClusters fetches error/warning entries over the requested
// window (optionally narrowed by an extra IFL filter) and clusters them —
// entirely MCP-side, reusing /api/entries.
func (s *Server) handleFindErrorClusters(ctx context.Context, args map[string]any) (string, error) {
	const errorOrWarning = `status == "error" or status == "warning"`
	q := url.Values{}
	if f := argString(args, "filter"); f != "" {
		q.Set("filter", "("+f+") and ("+errorOrWarning+")")
	} else {
		q.Set("filter", errorOrWarning)
	}
	setTimeArgs(q, args)
	q.Set("limit", strconv.Itoa(errorClusterFetchLimit))

	var entries []api.Entry
	if err := s.getJSON(ctx, "/api/entries?"+q.Encode(), &entries); err != nil {
		return "", err
	}

	clusters := clusterErrors(entries)
	limit := argInt(args, "limit", 20)
	if limit < 1 {
		limit = 1
	}
	truncated := len(clusters) > limit
	if truncated {
		clusters = clusters[:limit]
	}

	return marshalPretty(map[string]any{
		"scannedEntries": len(entries),
		"scanTruncated":  len(entries) >= errorClusterFetchLimit,
		"clusterCount":   len(clusters),
		"moreClusters":   truncated,
		"clusters":       clusters,
	}, "clusters")
}

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDiffTrafficChangedGroup(t *testing.T) {
	baseline := []summaryGroup{{Key: "shop/checkout", Count: 100, Errors: 1, P95Ms: 50}}
	current := []summaryGroup{{Key: "shop/checkout", Count: 100, Errors: 20, P95Ms: 400}}

	diffs := diffTraffic(baseline, current)
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1", len(diffs))
	}
	d := diffs[0]
	if d.Status != "changed" {
		t.Errorf("status = %q, want %q", d.Status, "changed")
	}
	if d.ErrorsDelta != 19 {
		t.Errorf("ErrorsDelta = %d, want 19", d.ErrorsDelta)
	}
	if d.P95DeltaMs != 350 {
		t.Errorf("P95DeltaMs = %d, want 350", d.P95DeltaMs)
	}
	wantRate := 0.19 // (20/100) - (1/100)
	if diff := d.ErrorRateDelta - wantRate; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("ErrorRateDelta = %v, want %v", d.ErrorRateDelta, wantRate)
	}
}

func TestDiffTrafficAppearedAndDisappeared(t *testing.T) {
	baseline := []summaryGroup{{Key: "shop/legacy", Count: 50, Errors: 0, P95Ms: 20}}
	current := []summaryGroup{{Key: "shop/new-service", Count: 30, Errors: 5, P95Ms: 80}}

	diffs := diffTraffic(baseline, current)
	if len(diffs) != 2 {
		t.Fatalf("got %d diffs, want 2", len(diffs))
	}
	byKey := map[string]groupDiff{}
	for _, d := range diffs {
		byKey[d.Key] = d
	}

	appeared := byKey["shop/new-service"]
	if appeared.Status != "appeared" {
		t.Errorf("new-service status = %q, want %q", appeared.Status, "appeared")
	}
	if appeared.BaselineCount != 0 || appeared.CurrentCount != 30 || appeared.CountDelta != 30 {
		t.Errorf("new-service counts = %+v, want baseline 0 / current 30 / delta 30", appeared)
	}

	disappeared := byKey["shop/legacy"]
	if disappeared.Status != "disappeared" {
		t.Errorf("legacy status = %q, want %q", disappeared.Status, "disappeared")
	}
	if disappeared.BaselineCount != 50 || disappeared.CurrentCount != 0 || disappeared.CountDelta != -50 {
		t.Errorf("legacy counts = %+v, want baseline 50 / current 0 / delta -50", disappeared)
	}
}

func TestDiffTrafficSortedByStrongestRegressionFirst(t *testing.T) {
	baseline := []summaryGroup{
		{Key: "mild", Count: 100, Errors: 1, P95Ms: 50},
		{Key: "severe", Count: 100, Errors: 1, P95Ms: 50},
		{Key: "improved", Count: 100, Errors: 50, P95Ms: 500},
	}
	current := []summaryGroup{
		{Key: "mild", Count: 100, Errors: 5, P95Ms: 60},     // small error-rate increase
		{Key: "severe", Count: 100, Errors: 90, P95Ms: 900}, // large error-rate increase
		{Key: "improved", Count: 100, Errors: 0, P95Ms: 10}, // got better
	}

	diffs := diffTraffic(baseline, current)
	got := make([]string, len(diffs))
	for i, d := range diffs {
		got[i] = d.Key
	}
	want := []string{"severe", "mild", "improved"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (sorted by error-rate delta descending)", got, want)
		}
	}
}

func TestDiffTrafficZeroCountNoDivideByZero(t *testing.T) {
	// A group with zero traffic in a window must report a 0 error rate, not
	// NaN/Inf from a 0/0 division.
	baseline := []summaryGroup{{Key: "idle", Count: 0, Errors: 0, P95Ms: 0}}
	current := []summaryGroup{{Key: "idle", Count: 0, Errors: 0, P95Ms: 0}}
	diffs := diffTraffic(baseline, current)
	if len(diffs) != 1 || diffs[0].BaselineErrorRate != 0 || diffs[0].CurrentErrorRate != 0 {
		t.Fatalf("diffs = %+v, want a single zero-rate group", diffs)
	}
}

// --- handleDiffTraffic end-to-end -------------------------------------------

func TestHandleDiffTrafficRequiresAllFourWindowArgs(t *testing.T) {
	s := &Server{log: discardLogger(), http: &http.Client{}}
	_, err := s.handleDiffTraffic(context.Background(), map[string]any{
		"baseline_since": "1h", "baseline_until": "30m", "current_since": "30m",
		// current_until missing
	})
	if err == nil {
		t.Fatal("expected an error when a required window arg is missing")
	}
}

func TestHandleDiffTrafficEndToEnd(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("groupBy") != "workload" {
			t.Errorf("groupBy = %q, want %q", q.Get("groupBy"), "workload")
		}
		var resp summaryResponse
		switch q.Get("since") {
		case "baseline-start":
			resp = summaryResponse{Total: 100, Groups: []summaryGroup{
				{Key: "shop/checkout", Count: 100, Errors: 1, P95Ms: 40},
			}}
		case "current-start":
			resp = summaryResponse{Total: 120, Groups: []summaryGroup{
				{Key: "shop/checkout", Count: 100, Errors: 40, P95Ms: 400},
				{Key: "shop/new-endpoint", Count: 20, Errors: 0, P95Ms: 10},
			}}
		default:
			t.Fatalf("unexpected since=%q", q.Get("since"))
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	s := New(ts.URL, "", false, discardLogger())
	text, err := s.handleDiffTraffic(context.Background(), map[string]any{
		"baseline_since": "baseline-start",
		"baseline_until": "baseline-end",
		"current_since":  "current-start",
		"current_until":  "current-end",
	})
	if err != nil {
		t.Fatalf("handleDiffTraffic: %v", err)
	}

	var out struct {
		GroupBy       string      `json:"groupBy"`
		BaselineTotal int         `json:"baselineTotal"`
		CurrentTotal  int         `json:"currentTotal"`
		Groups        []groupDiff `json:"groups"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshaling result: %v\n%s", err, text)
	}
	if out.BaselineTotal != 100 || out.CurrentTotal != 120 {
		t.Errorf("totals = %d/%d, want 100/120", out.BaselineTotal, out.CurrentTotal)
	}
	if len(out.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(out.Groups))
	}
	// The regressed group (checkout's error rate jumped 1%->40%) must sort
	// before the appeared-but-error-free new-endpoint group.
	if out.Groups[0].Key != "shop/checkout" || out.Groups[0].Status != "changed" {
		t.Errorf("groups[0] = %+v, want the regressed checkout group first", out.Groups[0])
	}
	if out.Groups[1].Key != "shop/new-endpoint" || out.Groups[1].Status != "appeared" {
		t.Errorf("groups[1] = %+v, want the appeared new-endpoint group second", out.Groups[1])
	}
}

func TestHandleDiffTrafficRespectsLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var groups []summaryGroup
		for i := 0; i < 5; i++ {
			groups = append(groups, summaryGroup{Key: "g" + string(rune('a'+i)), Count: 10, Errors: int64(i)})
		}
		json.NewEncoder(w).Encode(summaryResponse{Total: 50, Groups: groups})
	}))
	defer ts.Close()

	s := New(ts.URL, "", false, discardLogger())
	text, err := s.handleDiffTraffic(context.Background(), map[string]any{
		"baseline_since": "1h", "baseline_until": "30m",
		"current_since": "30m", "current_until": "0m",
		"limit": float64(2),
	})
	if err != nil {
		t.Fatalf("handleDiffTraffic: %v", err)
	}
	var out struct {
		Groups []groupDiff `json:"groups"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Groups) != 2 {
		t.Errorf("got %d groups, want the limit of 2", len(out.Groups))
	}
}

func TestFetchSummaryPropagatesHubError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid since: bad format", http.StatusBadRequest)
	}))
	defer ts.Close()

	s := New(ts.URL, "", false, discardLogger())
	_, err := s.fetchSummary(context.Background(), "workload", "", "not-a-time", "also-not-a-time")
	if err == nil || !strings.Contains(err.Error(), "invalid since") {
		t.Fatalf("err = %v, want it to surface the hub's 400 body", err)
	}
}

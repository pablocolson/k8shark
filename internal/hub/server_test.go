package hub

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

func TestHandleMetrics(t *testing.T) {
	s := New(slog.Default(), "")
	s.store.add(&api.Entry{ID: "x", Protocol: api.ProtocolHTTP, Timestamp: time.Now()})

	rec := httptest.NewRecorder()
	s.handleMetrics(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	body := rec.Body.String()
	for _, want := range []string{
		"# HELP", "# TYPE",
		"k8shark_hub_entries_total",
		"k8shark_hub_front_clients",
		"k8shark_hub_workers",
		"k8shark_hub_broadcast_dropped_total",
		"k8shark_hub_entries_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n%s", want, body)
		}
	}
}

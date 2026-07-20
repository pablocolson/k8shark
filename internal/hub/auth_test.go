package hub

// SEC-5: token role separation. A single apiToken used to grant reads, the
// control plane (POST /api/workers/capture pauses capture cluster-wide) and
// the worker ingest channel (/ws/worker — forged-entry injection); worse, the
// front's nginx injects that token, so anyone who reached the dashboard also
// had control. These tests lock the split: workerToken alone opens /ws/worker,
// adminToken alone opens mutations (and also reads), and each falls back to
// apiToken when unset so single-token setups keep the old behavior.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// reqStatus issues method+path with a bearer token and returns the HTTP status.
func reqStatus(t *testing.T, base, method, path, token, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// dialStatus attempts a WS dial with a bearer token, returning (connected,
// http status of a failed handshake).
func dialStatus(t *testing.T, url, token string) (bool, int) {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": {"Bearer " + token}})
	if err == nil {
		c.Close()
		return true, 0
	}
	if resp == nil {
		t.Fatalf("dial %s failed without a response: %v", url, err)
	}
	return false, resp.StatusCode
}

func TestWorkerTokenSeparation(t *testing.T) {
	s := New(discardLogger(), Options{APIToken: "read-tok", WorkerToken: "worker-tok"})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	if ok, code := dialStatus(t, wsURL(ts.URL, "/ws/worker"), "read-tok"); ok || code != http.StatusUnauthorized {
		t.Fatalf("read token on /ws/worker = (%v, %d), want 401 (read token must not inject entries)", ok, code)
	}
	if ok, _ := dialStatus(t, wsURL(ts.URL, "/ws/worker"), "worker-tok"); !ok {
		t.Fatal("worker token rejected on /ws/worker")
	}

	if code := reqStatus(t, ts.URL, http.MethodGet, "/api/entries", "worker-tok", ""); code != http.StatusUnauthorized {
		t.Errorf("worker token on /api/entries = %d, want 401 (worker credential must not read traffic)", code)
	}
	if code := reqStatus(t, ts.URL, http.MethodGet, "/api/entries", "read-tok", ""); code != http.StatusOK {
		t.Errorf("read token on /api/entries = %d, want 200", code)
	}
	if ok, _ := dialStatus(t, wsURL(ts.URL, "/ws"), "read-tok"); !ok {
		t.Error("read token rejected on the front /ws")
	}
}

func TestAdminTokenSeparation(t *testing.T) {
	s := New(discardLogger(), Options{APIToken: "read-tok", AdminToken: "admin-tok"})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	const captureBody = `{"node":"n1","paused":true}`
	if code := reqStatus(t, ts.URL, http.MethodPost, "/api/workers/capture", "read-tok", captureBody); code != http.StatusUnauthorized {
		t.Errorf("read token on POST capture = %d, want 401 (dashboard users must not control capture)", code)
	}
	if code := reqStatus(t, ts.URL, http.MethodPost, "/api/workers/capture", "admin-tok", captureBody); code != http.StatusOK {
		t.Errorf("admin token on POST capture = %d, want 200", code)
	}
	if code := reqStatus(t, ts.URL, http.MethodGet, "/api/stats", "read-tok", ""); code != http.StatusOK {
		t.Errorf("read token on GET stats = %d, want 200", code)
	}
	if code := reqStatus(t, ts.URL, http.MethodGet, "/api/stats", "admin-tok", ""); code != http.StatusOK {
		t.Errorf("admin token on GET stats = %d, want 200 (admin grants reads)", code)
	}
	if code := reqStatus(t, ts.URL, http.MethodGet, "/api/stats", "wrong", ""); code != http.StatusUnauthorized {
		t.Errorf("wrong token on GET stats = %d, want 401", code)
	}
}

// With only apiToken set, all three classes fall back to it — the pre-split
// behavior (also exercised end-to-end by TestE2EWorkerToFrontRoundTrip).
func TestSingleTokenFallback(t *testing.T) {
	s := New(discardLogger(), Options{APIToken: "only-tok"})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	if ok, _ := dialStatus(t, wsURL(ts.URL, "/ws/worker"), "only-tok"); !ok {
		t.Error("api token rejected on /ws/worker without a worker token configured")
	}
	if code := reqStatus(t, ts.URL, http.MethodPost, "/api/workers/capture", "only-tok", `{"node":"n","paused":false}`); code != http.StatusOK {
		t.Errorf("api token on POST capture = %d, want 200 without an admin token configured", code)
	}
}

// workerToken alone (no apiToken): reads stay open, the worker channel is
// locked — the "protect ingest, keep the dashboard public" configuration.
func TestWorkerTokenOnlyKeepsReadsOpen(t *testing.T) {
	s := New(discardLogger(), Options{WorkerToken: "worker-tok"})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	if code := reqStatus(t, ts.URL, http.MethodGet, "/api/stats", "", ""); code != http.StatusOK {
		t.Errorf("unauthenticated GET stats = %d, want 200 (no api token = open reads)", code)
	}
	if ok, code := dialStatus(t, wsURL(ts.URL, "/ws/worker"), "nope"); ok || code != http.StatusUnauthorized {
		t.Fatalf("wrong token on /ws/worker = (%v, %d), want 401", ok, code)
	}
	if ok, _ := dialStatus(t, wsURL(ts.URL, "/ws/worker"), "worker-tok"); !ok {
		t.Fatal("worker token rejected on /ws/worker")
	}
}

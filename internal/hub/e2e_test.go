package hub

// TST-2: an end-to-end test over real WebSockets exercising the product's
// central path — a worker connects (/ws/worker, MsgHello), pushes entries, and
// a front client (/ws) receives them live, server-side filtered, plus the
// hub->worker command round-trip (pause capture). server_test.go covers each
// REST handler in isolation via httptest.NewRecorder; nothing else drives the
// WS fan-out, the live filter, or the Envelope contract between the three
// components. This locks that contract before Phase 3 refactors it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pablocolson/k8shark/pkg/api"
)

// wsURL rewrites an httptest http:// base into a ws:// URL for a given path.
func wsURL(base, path string) string {
	return strings.Replace(base, "http://", "ws://", 1) + path
}

// dialWS opens a WebSocket to url with optional headers, failing the test on
// error.
func dialWS(t *testing.T, url string, hdr http.Header) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, hdr)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

// readEnvelope reads one frame with a deadline and decodes it.
func readEnvelope(t *testing.T, c *websocket.Conn) api.Envelope {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var env api.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode frame %q: %v", data, err)
	}
	return env
}

func writeEnvelope(t *testing.T, c *websocket.Conn, env api.Envelope) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
}

func httpEntry(id string) *api.Entry {
	return &api.Entry{
		ID:        id,
		Protocol:  api.ProtocolHTTP,
		Timestamp: time.Now(),
		Status:    "success",
		Request:   api.Payload{Method: "GET", Path: "/" + id, Summary: "GET /" + id},
		Response:  api.Payload{StatusCode: 200, Summary: "200 OK"},
	}
}

// TestE2EWorkerToFrontRoundTrip drives worker -> hub -> front over real
// WebSockets: the front, subscribed with a server-side filter, must receive
// only the matching entries; the REST snapshot must show all of them; the
// registry must show the worker; and a POST /api/workers/capture command must
// reach the worker. Auth is enabled to also cover both token paths (Bearer
// header for the worker, ?token= for the browser-style front WS).
func TestE2EWorkerToFrontRoundTrip(t *testing.T) {
	const token = "e2e-secret"
	s := New(discardLogger(), Options{APIToken: token})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	bearer := http.Header{"Authorization": {"Bearer " + token}}

	// --- worker connects and identifies itself ---
	worker := dialWS(t, wsURL(ts.URL, "/ws/worker"), bearer)
	defer worker.Close()
	writeEnvelope(t, worker, api.Envelope{Type: api.MsgHello, Hello: &api.Hello{Node: "node-a", Version: "test"}})

	// The registry row is written on the hub's read goroutine; poll briefly.
	waitFor(t, func() bool {
		for _, wi := range s.workerSnapshot() {
			if wi.Node == "node-a" && wi.Connected {
				return true
			}
		}
		return false
	}, "worker to register as connected")

	// --- front subscribes with a server-side filter (http only) ---
	frontQ := url.Values{"filter": {`protocol == "http"`}, "token": {token}}
	front := dialWS(t, wsURL(ts.URL, "/ws?"+frontQ.Encode()), nil)
	defer front.Close()
	// On connect the front is sent a stats frame (history is empty here).
	if env := readEnvelope(t, front); env.Type != api.MsgStats {
		t.Fatalf("first front frame type = %q, want %q", env.Type, api.MsgStats)
	}

	// --- worker pushes three entries: two http (match), one redis (filtered) ---
	writeEnvelope(t, worker, api.Envelope{Type: api.MsgEntry, Entry: httpEntry("h1")})
	writeEnvelope(t, worker, api.Envelope{Type: api.MsgEntry, Entry: &api.Entry{
		ID: "r1", Protocol: api.ProtocolRedis, Timestamp: time.Now(), Status: "success",
		Request: api.Payload{Command: "GET k"},
	}})
	writeEnvelope(t, worker, api.Envelope{Type: api.MsgEntry, Entry: httpEntry("h2")})

	// The front must receive exactly the two http entries, in order, and never
	// the redis one (server-side filtered out before fan-out).
	var got []string
	for len(got) < 2 {
		env := readEnvelope(t, front)
		if env.Type != api.MsgEntry { // stats frames may interleave; ignore
			continue
		}
		if env.Entry.Protocol != api.ProtocolHTTP {
			t.Fatalf("front received a non-http entry despite the filter: %+v", env.Entry)
		}
		got = append(got, env.Entry.ID)
	}
	if got[0] != "h1" || got[1] != "h2" {
		t.Errorf("front entry IDs = %v, want [h1 h2]", got)
	}

	// --- REST snapshot sees all three (unfiltered), newest first ---
	all := getEntries(t, ts.URL, token, "")
	if len(all) != 3 {
		t.Fatalf("/api/entries returned %d entries, want 3", len(all))
	}
	// A filtered REST query must match the live filter's behavior.
	httpOnly := getEntries(t, ts.URL, token, `protocol == "http"`)
	if len(httpOnly) != 2 {
		t.Errorf("/api/entries?filter=http returned %d, want 2", len(httpOnly))
	}

	// --- hub -> worker command round-trip: pause capture ---
	body := strings.NewReader(`{"node":"node-a","paused":true}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/workers/capture", body)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/workers/capture: %v", err)
	}
	var sent struct {
		Sent int `json:"sent"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sent)
	resp.Body.Close()
	if sent.Sent != 1 {
		t.Errorf("capture command reached %d workers, want 1", sent.Sent)
	}
	// The worker must actually receive the command frame.
	cmd := readEnvelope(t, worker)
	if cmd.Type != api.MsgWorkerCommand || cmd.WorkerCommand == nil || !cmd.WorkerCommand.Paused {
		t.Errorf("worker command = %+v, want MsgWorkerCommand paused=true", cmd)
	}
}

// TestE2EBadTokenRejected confirms the WS endpoints enforce the API token: a
// wrong token fails the upgrade rather than silently exposing captured traffic.
func TestE2EBadTokenRejected(t *testing.T) {
	s := New(discardLogger(), Options{APIToken: "right"})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/worker"),
		http.Header{"Authorization": {"Bearer wrong"}})
	if err == nil {
		t.Fatal("dial with a wrong token succeeded, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token upgrade status = %v, want 401", resp)
	}
}

// getEntries GETs /api/entries with an optional filter and decodes the array.
func getEntries(t *testing.T, base, token, filter string) []api.Entry {
	t.Helper()
	u := base + "/api/entries"
	if filter != "" {
		u += "?filter=" + url.QueryEscape(filter)
	}
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", u, resp.StatusCode)
	}
	var entries []api.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	return entries
}

// waitFor polls cond up to ~2s, failing the test with what if it never holds.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

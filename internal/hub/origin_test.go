package hub

// SEC-6 / SEC-9: browser-facing hardening. The hub used to accept any Origin
// on its WebSockets and answer CORS with a wildcard — during a port-forward
// without a token (the default), any web page open in the operator's browser
// could read the cluster's captured traffic. And the only browser WS auth
// path was ?token=, which leaks into access logs, history and Referer. These
// tests lock the same-origin default, the --allow-origin list, and the
// `Sec-WebSocket-Protocol: bearer.<token>` auth path.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// corsGet issues a GET /api/stats with the given Origin and returns the
// Access-Control-Allow-Origin header of the response.
func corsGet(t *testing.T, base, origin string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/api/stats", nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Access-Control-Allow-Origin")
}

func TestCORSSameOriginDefault(t *testing.T) {
	s := New(discardLogger(), Options{})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	if acao := corsGet(t, ts.URL, ts.URL); acao != ts.URL {
		t.Errorf("same-origin ACAO = %q, want %q", acao, ts.URL)
	}
	if acao := corsGet(t, ts.URL, "http://evil.example"); acao != "" {
		t.Errorf("cross-origin ACAO = %q, want none (browser must not read responses)", acao)
	}
	if acao := corsGet(t, ts.URL, ""); acao != "" {
		t.Errorf("no-Origin ACAO = %q, want none", acao)
	}
}

func TestCORSAllowOriginList(t *testing.T) {
	s := New(discardLogger(), Options{AllowedOrigins: []string{"http://app.example"}})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	if acao := corsGet(t, ts.URL, "http://app.example"); acao != "http://app.example" {
		t.Errorf("allow-listed ACAO = %q, want echoed origin", acao)
	}
	if acao := corsGet(t, ts.URL, "http://other.example"); acao != "" {
		t.Errorf("unlisted ACAO = %q, want none", acao)
	}

	star := New(discardLogger(), Options{AllowedOrigins: []string{"*"}})
	ts2 := httptest.NewServer(star.handler())
	defer ts2.Close()
	if acao := corsGet(t, ts2.URL, "http://anything.example"); acao != "http://anything.example" {
		t.Errorf("wildcard ACAO = %q, want echoed origin", acao)
	}
}

// A cross-origin browser page must not be able to open the front WebSocket;
// a same-origin one still can. Non-browser clients (no Origin header — the
// worker sink, curl, the MCP) are covered by every other WS test in this
// package, none of which send an Origin.
func TestWSCrossOriginRejected(t *testing.T) {
	s := New(discardLogger(), Options{})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws"),
		http.Header{"Origin": {"http://evil.example"}})
	if err == nil {
		t.Fatal("cross-origin WS upgrade succeeded, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin upgrade status = %+v, want 403", resp)
	}

	c := dialWS(t, wsURL(ts.URL, "/ws"), http.Header{"Origin": {ts.URL}})
	c.Close()
}

// Browser clients can carry the API token as a `bearer.<token>` WebSocket
// subprotocol instead of ?token=; the hub must echo the entry as the selected
// subprotocol (browsers abort the connection otherwise) and reject a wrong
// token with 401.
func TestWSBearerSubprotocolAuth(t *testing.T) {
	const token = "sub-secret"
	s := New(discardLogger(), Options{APIToken: token})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	d := websocket.Dialer{Subprotocols: []string{"k8shark", "bearer." + token}}
	c, _, err := d.Dial(wsURL(ts.URL, "/ws"), nil)
	if err != nil {
		t.Fatalf("dial with bearer subprotocol: %v", err)
	}
	if got := c.Subprotocol(); got != "bearer."+token {
		t.Errorf("negotiated subprotocol = %q, want the echoed bearer entry", got)
	}
	c.Close()

	d = websocket.Dialer{Subprotocols: []string{"bearer.wrong"}}
	_, resp, err := d.Dial(wsURL(ts.URL, "/ws"), nil)
	if err == nil {
		t.Fatal("dial with a wrong subprotocol token succeeded, want rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token upgrade status = %+v, want 401", resp)
	}
}

func TestWSBearerProtocolParsing(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	if got := wsBearerProtocol(r); got != "" {
		t.Errorf("no header = %q, want empty", got)
	}
	r.Header.Set("Sec-WebSocket-Protocol", "k8shark,  bearer.tok-123 , other")
	if got := wsBearerProtocol(r); got != "bearer.tok-123" {
		t.Errorf("parsed = %q, want bearer.tok-123", got)
	}
	if !strings.HasPrefix("bearer.tok-123", wsBearerPrefix) {
		t.Fatal("wsBearerPrefix drifted from the wire format")
	}
}

package worker

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pablocolson/k8shark/pkg/api"
)

// TestSinkReaderPause exercises sink.reader() against a real WebSocket pair
// (a fake hub server + the sink's own client connection) since it needs an
// actual *websocket.Conn, not a fakeable interface: a MsgWorkerCommand frame
// sent from the "hub" side must flip s.paused(), and a resume must flip it
// back — the exact mechanism route()/consumeTLS/runDemo all gate on.
func TestSinkReaderPause(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var hubConn *websocket.Conn
	connected := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("hub-side upgrade: %v", err)
			return
		}
		hubConn = c
		close(connected)
	}))
	defer srv.Close()

	s := newSink("ws://"+srv.Listener.Addr().String(), "", "n", discardLogger())
	if err := s.connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	<-connected
	defer hubConn.Close()

	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	go s.reader(conn)

	if s.paused() {
		t.Fatal("sink starts paused, want capturing")
	}

	send := func(paused bool) {
		b, _ := json.Marshal(api.Envelope{Type: api.MsgWorkerCommand, WorkerCommand: &api.WorkerCommand{Paused: paused}})
		if err := hubConn.WriteMessage(websocket.TextMessage, b); err != nil {
			t.Fatalf("hub write: %v", err)
		}
	}
	waitFor := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if s.paused() == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("paused() = %v after deadline, want %v", s.paused(), want)
	}

	send(true)
	waitFor(true)

	send(false)
	waitFor(false)
}

// TestSetHubCA covers the SEC-7 CA loading paths: a bad path and a PEM
// without certificates must error; a valid CA installs a custom dialer.
func TestSetHubCA(t *testing.T) {
	s := newSink("wss://x", "", "n", discardLogger())
	if err := s.setHubCA("/no/such/file.pem"); err == nil {
		t.Error("missing file: want error")
	}
	junk := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(junk, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.setHubCA(junk); err == nil {
		t.Error("junk PEM: want error")
	}
	if s.dialer != websocket.DefaultDialer {
		t.Fatal("failed loads must leave the default dialer in place")
	}
}

// TestSinkConnectsWSSWithCustomCA dials a real TLS WebSocket server whose
// self-signed cert is trusted only via setHubCA — locking the worker's wss://
// path end to end: without the CA the dial must fail, with it it must succeed.
func TestSinkConnectsWSSWithCustomCA(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Consume the hello frame so connect()'s write lands.
		_, _, _ = c.ReadMessage()
		c.Close()
	}))
	defer srv.Close()
	wssURL := "wss://" + srv.Listener.Addr().String()

	noCA := newSink(wssURL, "", "n", discardLogger())
	if err := noCA.connect(); err == nil {
		t.Fatal("dial without the CA succeeded, want certificate verification failure")
	}

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, pem, 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSink(wssURL, "", "n", discardLogger())
	if err := s.setHubCA(caFile); err != nil {
		t.Fatalf("setHubCA: %v", err)
	}
	if err := s.connect(); err != nil {
		t.Fatalf("wss connect with custom CA: %v", err)
	}
}

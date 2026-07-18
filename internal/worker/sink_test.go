package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

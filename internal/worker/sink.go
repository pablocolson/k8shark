package worker

import (
	"encoding/json"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/pkg/api"
)

// sinkWriteTimeout bounds a single hub WriteMessage so a half-open connection
// can't wedge the pump for the OS TCP timeout (minutes) while the buffer
// silently drops all capture.
const sinkWriteTimeout = 10 * time.Second

// sink is a reconnecting WebSocket client that ships entries to the hub. Entries
// are buffered on a channel; if the hub is unreachable the buffer drops the
// newest (incoming) entry rather than blocking capture.
type sink struct {
	hubURL string
	node   string
	log    *slog.Logger

	ch      chan *api.Entry
	dropped atomic.Uint64 // entries dropped on a full buffer
	mu      sync.Mutex
	conn    *websocket.Conn
}

func newSink(hubURL, node string, log *slog.Logger) *sink {
	return &sink{
		hubURL: hubURL,
		node:   node,
		log:    log,
		ch:     make(chan *api.Entry, 1024),
	}
}

// emit queues an entry, dropping the incoming (newest) entry if the buffer is
// full so capture never blocks. Full-buffer drops are counted and logged every
// 1000 so they aren't completely invisible.
func (s *sink) emit(e *api.Entry) {
	select {
	case s.ch <- e:
	default:
		// buffer full — drop this (newest) entry to keep capture non-blocking
		if n := s.dropped.Add(1); n%1000 == 0 {
			s.log.Warn("hub sink buffer full, dropping entries", "dropped", n)
		}
	}
}

// run maintains the connection and drains the buffer until ctx-less stop.
func (s *sink) run() {
	for {
		if err := s.connect(); err != nil {
			s.log.Debug("hub connect failed, retrying", "url", s.hubURL, "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		s.log.Info("connected to hub", "url", s.hubURL)
		s.pump()
		s.log.Debug("hub connection lost, reconnecting")
		time.Sleep(1 * time.Second)
	}
}

func (s *sink) connect() error {
	u, err := url.Parse(s.hubURL)
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	hello, _ := json.Marshal(api.Envelope{
		Type:  api.MsgHello,
		Hello: &api.Hello{Node: s.node, Version: config.Ver()},
	})
	return conn.WriteMessage(websocket.TextMessage, hello)
}

// pump writes buffered entries to the current connection until it errors.
func (s *sink) pump() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	defer conn.Close()

	for e := range s.ch {
		b, err := json.Marshal(api.Envelope{Type: api.MsgEntry, Entry: e})
		if err != nil {
			continue
		}
		conn.SetWriteDeadline(time.Now().Add(sinkWriteTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			// Requeue the entry we failed to send, then bail to reconnect.
			s.emit(e)
			return
		}
	}
}

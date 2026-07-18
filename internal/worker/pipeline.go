package worker

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/tcpassembly"
	"github.com/google/gopacket/tcpassembly/tcpreader"
	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/pkg/api"
)

// Well-known service ports used to pick a TCP dissector.
const (
	redisPort = 6379
	pgPort    = 5432
	amqpPort  = 5672
)

// pipeline turns reassembled TCP streams and UDP datagrams into paired L7
// entries. It owns the request/response correlation state, which is protocol
// agnostic: each dissector enqueues a request and later completes it with a
// response, and the pipeline pairs them FIFO per connection.
type pipeline struct {
	sink *sink
	node string
	log  *slog.Logger

	seq atomic.Uint64

	mu    sync.Mutex
	conns map[string]*connState // request/response pairing, keyed by canonical conn
	dns   map[string]*dnsPending

	redisDB map[string]*redisDBState // per-connection Redis DB index (tracked from SELECT n), guarded by mu

	flowMu sync.Mutex
	flows  map[string]*flowState // generic L4 flow accounting, keyed by canonical conn

	// respPorts maps a RESP (Redis wire protocol) port to the protocol label it
	// should be emitted under (redis|valkey). Defaults to {6379: redis}.
	respPorts map[int]api.Protocol

	// amqpPorts is the set of ports carrying AMQP 0-9-1 (RabbitMQ). Defaults to
	// {5672: true}.
	amqpPorts map[int]bool

	// Capture-depth bounds (per direction). Defaults from config; overridden by
	// applyCaptureOpts from the worker Options.
	captureBodies bool
	bodyCap       int // max body bytes per direction
	rawCap        int // max raw hex bytes per direction (<0 disables raw capture)
	headerHexCap  int // max L2/L3/L4 header hexdump bytes

	// redactHeaders scrubs credential-bearing HTTP header values, query
	// params and RESP auth command arguments (the raw hex view is separate —
	// disable it via rawCap < 0 for full scrubbing).
	redactHeaders bool

	// redactPGParams replaces every Postgres Bind parameter value with
	// [REDACTED] (see pgResponses' 'E' case) when set. Unlike redactHeaders,
	// this defaults off: Bind params are positional wire values with no
	// name attached, so there's no way to redact just the sensitive ones —
	// only a blanket replace-everything is possible, which throws away
	// query-value visibility that's usually the whole point of watching
	// Postgres traffic. Opt in when queries in your cluster do carry
	// unparameterized secrets in bind values.
	redactPGParams bool
}

func newPipeline(s *sink, node string, log *slog.Logger) *pipeline {
	return &pipeline{
		sink:          s,
		node:          node,
		log:           log,
		conns:         map[string]*connState{},
		dns:           map[string]*dnsPending{},
		redisDB:       map[string]*redisDBState{},
		flows:         map[string]*flowState{},
		respPorts:     map[int]api.Protocol{redisPort: api.ProtocolRedis},
		amqpPorts:     map[int]bool{amqpPort: true},
		captureBodies: true,
		bodyCap:       config.DefaultBodyCaptureBytes,
		rawCap:        config.DefaultRawCaptureBytes,
		headerHexCap:  config.DefaultHeaderHexBytes,
	}
}

// applyCaptureOpts overrides the capture-depth bounds from the worker Options.
// A BodyBytes/RawBytes of 0 keeps the default; a negative RawBytes disables raw
// capture entirely.
func (p *pipeline) applyCaptureOpts(opts Options) {
	p.captureBodies = opts.CaptureBodies
	p.redactHeaders = opts.RedactHeaders
	p.redactPGParams = opts.RedactPGParams
	if opts.BodyBytes > 0 {
		p.bodyCap = opts.BodyBytes
	}
	switch {
	case opts.RawBytes < 0:
		p.rawCap = -1
	case opts.RawBytes > 0:
		p.rawCap = opts.RawBytes
	}
}

// connState holds requests awaiting their response on one TCP connection.
// HTTP/1.x, RESP and the Postgres protocol are all request-ordered per
// connection, so FIFO pairing is correct.
type connState struct {
	reqs []*pendingReq
}

type pendingReq struct {
	protocol api.Protocol
	req      api.Payload
	src, dst api.Endpoint
	ts       time.Time
}

// reqBacklogCap bounds the per-connection pending-request queue. FIFO pairing
// (see completeResponse) means a connection whose responses are never seen — or
// that desynced after a missed response — would otherwise grow this slice
// without limit; on overflow the oldest pending request is dropped.
const reqBacklogCap = 1024

// enqueueRequest records a request awaiting its response.
func (p *pipeline) enqueueRequest(key string, proto api.Protocol, req api.Payload, src, dst api.Endpoint) {
	pr := &pendingReq{protocol: proto, req: req, src: src, dst: dst, ts: time.Now()}
	p.mu.Lock()
	cs := p.conns[key]
	if cs == nil {
		cs = &connState{}
		p.conns[key] = cs
	}
	// PipelineDepth = requests already outstanding ahead of this one. Set through
	// the shared *RedisDetail pointer under the lock (before the pending request
	// becomes visible to the response goroutine) so it is race-free.
	if req.Redis != nil {
		req.Redis.PipelineDepth = len(cs.reqs)
	}
	cs.reqs = append(cs.reqs, pr)
	if len(cs.reqs) > reqBacklogCap {
		cs.reqs = cs.reqs[1:] // drop the oldest pending request to bound growth
	}
	p.mu.Unlock()

	// Flag this connection as L7-dissected so the generic L4 flow tracker does
	// not also emit a redundant flow entry for it.
	p.markL7(key)
}

// completeResponsePairRetries/Delay bound how long completeResponse waits for
// a request that hasn't been enqueued *yet*. AF_PACKET's request and response
// goroutines are ordered by real network causality (a response can't arrive
// before the request that produced it was fully sent), so the pending
// request is essentially always already there. The eBPF TLS path
// (tls_pipeline.go) has no such guarantee: consumeTLS feeds both directions
// back-to-back from in-memory records with no real network delay between
// them, so its two independently-scheduled per-direction goroutines can
// legitimately race — completeResponse observed before the matching
// enqueueRequest — dropping every entry on that connection. This bounded
// retry (worst case ~8ms) closes that race for both callers at negligible
// cost to the genuine "capture started mid-connection" case, which still
// correctly gives up and returns after the budget.
const (
	completeResponsePairRetries = 4
	completeResponsePairDelay   = 2 * time.Millisecond
)

// peekPendingMethod returns the HTTP method of the oldest pending request on
// key without consuming it (completeResponse does the actual pop once the
// full response is parsed). Returns "" if there is no pending request yet
// (e.g. capture started mid-response), in which case http.ReadResponse falls
// back to its nil-request/GET behavior, matching prior behavior.
func (p *pipeline) peekPendingMethod(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cs := p.conns[key]; cs != nil && len(cs.reqs) > 0 {
		return cs.reqs[0].req.Method
	}
	return ""
}

// isInterimStatus reports whether code is a 1xx informational response (100
// Continue, 103 Early Hints, ...). 101 Switching Protocols is excluded: it is
// the final response to its Upgrade request (and ends HTTP framing on the
// connection), not an interim one to be skipped.
func isInterimStatus(code int) bool {
	return code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols
}

// completeResponse pairs a response with the oldest pending request on the same
// connection and emits the finished entry.
//
// Known limitation: pairing is strict head-of-line FIFO with no request-id
// correlation, so a single missed response (capture started mid-response, a
// dropped segment, a dissector that failed to emit) shifts every subsequent
// response onto the wrong request for the rest of the connection, with no way
// to resync. The protocols dissected here are all strictly request-ordered per
// connection, so in the common lossless case FIFO is exact.
//
// firstByteTime is when the response's first byte was observed on the wire
// (used only to fill HTTPDetail.TTFBMs); pass the zero time.Time for
// protocols that don't populate resp.HTTP.
func (p *pipeline) completeResponse(key string, resp api.Payload, statusCode int, status string, firstByteTime time.Time) {
	var pr *pendingReq
	for attempt := 0; ; attempt++ {
		p.mu.Lock()
		cs := p.conns[key]
		if cs != nil && len(cs.reqs) > 0 {
			pr = cs.reqs[0]
			cs.reqs = cs.reqs[1:]
			p.mu.Unlock()
			break
		}
		p.mu.Unlock()
		if attempt >= completeResponsePairRetries {
			return // no request to pair with (capture started mid-connection)
		}
		time.Sleep(completeResponsePairDelay)
	}

	if resp.HTTP != nil && !firstByteTime.IsZero() {
		resp.HTTP.TTFBMs = firstByteTime.Sub(pr.ts).Milliseconds()
	}

	now := time.Now()
	entry := &api.Entry{
		ID:          p.node + "-" + strconv.FormatUint(p.seq.Add(1), 36),
		Protocol:    pr.protocol,
		Timestamp:   pr.ts,
		ElapsedMs:   now.Sub(pr.ts).Milliseconds(),
		Node:        p.node,
		Source:      pr.src,
		Destination: pr.dst,
		Request:     pr.req,
		Response:    resp,
		StatusCode:  statusCode,
		Status:      status,
	}
	// Snapshot L4 after p.mu is released (snapshotL4 takes only flowMu, never
	// nested inside mu). May be nil for a capture that started mid-connection.
	entry.L4 = p.snapshotL4(key)
	p.sink.emit(entry)
}

// --- TCP dispatch -----------------------------------------------------------

type tcpStreamFactory struct{ p *pipeline }

func (f *tcpStreamFactory) New(netFlow, transport gopacket.Flow) tcpassembly.Stream {
	r := tcpreader.NewReaderStream()
	go f.p.consumeStream(netFlow, transport, &r)
	return &r
}

// consumeStream routes one direction of an AF_PACKET-discovered TCP connection
// to the right dissector. It is a thin wrapper: consumeStreamID does the real
// work, keyed off connID rather than gopacket flows, so the same dispatch also
// serves eBPF-decrypted TLS streams (see tls_pipeline.go), which have no
// gopacket.Flow to offer.
func (p *pipeline) consumeStream(netFlow, transport gopacket.Flow, r io.Reader) {
	p.consumeStreamID(connIDFromFlows(netFlow, transport), r)
}

// consumeStreamID routes one direction of a TCP connection to the right
// dissector, chosen by the well-known server port (falling back to HTTP
// sniffing). Shared dispatch point for consumeStream (AF_PACKET) and
// consumeTLS (eBPF-decrypted plaintext).
func (p *pipeline) consumeStreamID(c connID, r io.Reader) {
	src, dst := c.srcPort, c.dstPort
	// Valkey and Redis are wire-identical (same RESP2/RESP3 protocol, usually the
	// same port), so the label emitted here is config-driven (an operator-supplied
	// port list, see Options.RedisPorts/ValkeyPorts in worker.go) rather than
	// something detected from the bytes on the wire.
	if proto, ok := p.respPorts[dst]; ok {
		p.consumeRedisID(c, r, true, proto)
		return
	}
	if proto, ok := p.respPorts[src]; ok {
		p.consumeRedisID(c, r, false, proto)
		return
	}
	if p.amqpPorts[dst] {
		p.consumeAMQPID(c, r, true)
		return
	}
	if p.amqpPorts[src] {
		p.consumeAMQPID(c, r, false)
		return
	}
	switch {
	case dst == pgPort || src == pgPort:
		p.consumePostgresID(c, r, dst == pgPort)
	default:
		p.consumeHTTPID(c, r)
	}
}

// --- HTTP over TCP ----------------------------------------------------------

// consumeHTTP reads one direction of an AF_PACKET-discovered TCP connection
// and parses HTTP messages. Thin wrapper over consumeHTTPID (see conn.go).
func (p *pipeline) consumeHTTP(netFlow, transport gopacket.Flow, r io.Reader) {
	p.consumeHTTPID(connIDFromFlows(netFlow, transport), r)
}

// consumeHTTPID reads one direction of a TCP connection (identified by c) and
// parses HTTP messages. The direction (request vs response) is auto-detected
// from the first bytes. Fed by both AF_PACKET (via consumeHTTP) and eBPF TLS
// uprobes (via consumeTLS/tls_pipeline.go), which is the whole point of the
// hybrid capture layer: decrypted HTTPS lands in the exact same dissector as
// plaintext HTTP.
func (p *pipeline) consumeHTTPID(c connID, r io.Reader) {
	r, cr := p.capture(r)
	br := bufio.NewReader(r)
	peek, err := br.Peek(5)
	if err != nil {
		io.Copy(io.Discard, br)
		return
	}
	key := c.key()

	if string(peek) == "HTTP/" {
		// Server -> client: a stream of responses.
		for {
			// Peek(1) blocks until the response's first byte is actually on
			// the wire without consuming it, so this timestamp reflects
			// first-byte arrival rather than whenever ReadResponse happens
			// to finish parsing headers.
			if _, err := br.Peek(1); err != nil {
				return
			}
			firstByte := time.Now()
			// The oldest pending request's method decides whether a body is
			// expected: net/http only skips a HEAD response's (possibly
			// non-zero) Content-Length when told the request method — a nil
			// req defaults to "a body may follow", which desyncs the rest of
			// the connection onto the wrong request/response pairs.
			method := p.peekPendingMethod(key)
			resp, err := http.ReadResponse(br, &http.Request{Method: method})
			if err != nil {
				return
			}
			if isInterimStatus(resp.StatusCode) {
				// 1xx informational responses (100 Continue, 103 Early
				// Hints, ...) precede the real response to the same
				// request; pairing them here would consume the pending
				// request early and shift every later response onto the
				// wrong request for the rest of the connection.
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				continue
			}
			body, truncated, full := p.drainBody(resp.Body)
			ct := resp.Header.Get("Content-Type")
			p.completeResponse(key, api.Payload{
				StatusCode:  resp.StatusCode,
				Headers:     p.flattenHeaders(resp.Header),
				Body:        body,
				Truncated:   truncated,
				Size:        full,
				ContentType: ct,
				Raw:         rawOf(cr),
				HTTP:        &api.HTTPDetail{Version: resp.Proto, ContentType: ct},
				Summary:     strconv.Itoa(resp.StatusCode) + " " + http.StatusText(resp.StatusCode),
			}, resp.StatusCode, classifyHTTP(resp.StatusCode), firstByte)
		}
	}
	// Client -> server: a stream of requests.
	src, dst := c.endpoints()
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		body, truncated, full := p.drainBody(req.Body)
		ct := req.Header.Get("Content-Type")
		p.enqueueRequest(key, api.ProtocolHTTP, api.Payload{
			Method:      req.Method,
			Path:        redactedRequestURI(req.URL, p.redactHeaders),
			Host:        req.Host,
			Headers:     p.flattenHeaders(req.Header),
			Body:        body,
			Truncated:   truncated,
			Size:        full,
			ContentType: ct,
			Raw:         rawOf(cr),
			HTTP:        &api.HTTPDetail{Version: req.Proto, ContentType: ct, Query: parseQuery(req.URL, p.redactHeaders)},
			Summary:     req.Method + " " + redactedRequestURI(req.URL, p.redactHeaders),
		}, src, dst)
	}
}

// --- DNS over UDP -----------------------------------------------------------

type dnsPending struct {
	question  string
	questions []api.DNSQuestion
	id        int
	src, dst  api.Endpoint
	ts        time.Time
	raw       *api.RawView
}

// handleDNS processes a single UDP packet that carries a DNS layer. raw is
// the undecoded DNS message bytes (gopacket Layer.LayerContents()) for the
// Raw tab — DNS has no bufio.Reader-based capture path like the TCP
// dissectors, so it's captured directly from the packet layer instead.
func (p *pipeline) handleDNS(netFlow gopacket.Flow, udp *layers.UDP, dns *layers.DNS, raw []byte) {
	srcIP, dstIP := netFlow.Src().String(), netFlow.Dst().String()
	if !dns.QR {
		// Query
		q := ""
		if len(dns.Questions) > 0 {
			q = string(dns.Questions[0].Name)
		}
		p.mu.Lock()
		p.dns[dnsKey(srcIP, dns.ID)] = &dnsPending{
			question:  q,
			questions: dnsQuestions(dns.Questions),
			id:        int(dns.ID),
			src:       api.Endpoint{IP: srcIP, Port: int(udp.SrcPort)},
			dst:       api.Endpoint{IP: dstIP, Port: int(udp.DstPort), Name: "dns"},
			ts:        time.Now(),
			raw:       rawViewFromBytes(raw, p.rawCap),
		}
		p.mu.Unlock()
		return
	}
	// Response: pair with the pending query (client is now the destination).
	p.mu.Lock()
	pend := p.dns[dnsKey(dstIP, dns.ID)]
	delete(p.dns, dnsKey(dstIP, dns.ID))
	p.mu.Unlock()
	if pend == nil {
		return
	}
	answer := ""
	for _, a := range dns.Answers {
		if a.IP != nil {
			answer = a.IP.String()
			break
		}
	}
	reqDetail := &api.DNSDetail{ID: pend.id, Questions: pend.questions}
	respDetail := &api.DNSDetail{
		ID:            int(dns.ID),
		Answers:       dnsRecords(dns.Answers),
		Authority:     dnsRecords(dns.Authorities),
		Additional:    dnsRecords(dns.Additionals),
		Rcode:         dnsRcodeName(dns.ResponseCode),
		Authoritative: dns.AA,
		RecursionAvl:  dns.RA,
	}
	now := time.Now()
	p.sink.emit(&api.Entry{
		ID:          p.node + "-dns-" + strconv.FormatUint(p.seq.Add(1), 36),
		Protocol:    api.ProtocolDNS,
		Timestamp:   pend.ts,
		ElapsedMs:   now.Sub(pend.ts).Milliseconds(),
		Node:        p.node,
		Source:      pend.src,
		Destination: pend.dst,
		Request:     api.Payload{Question: pend.question, Summary: "A? " + pend.question, DNS: reqDetail, Raw: pend.raw},
		Response:    api.Payload{Answer: answer, Summary: dnsRcode(dns.ResponseCode), DNS: respDetail, Raw: rawViewFromBytes(raw, p.rawCap)},
		Status:      dnsStatus(dns.ResponseCode),
	})
}

// gc drops stale pending state so a lossy capture can't leak memory.
func (p *pipeline) gc() {
	cutoff := time.Now().Add(-30 * time.Second)
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, d := range p.dns {
		if d.ts.Before(cutoff) {
			delete(p.dns, k)
		}
	}
	for k, cs := range p.conns {
		kept := cs.reqs[:0]
		for _, r := range cs.reqs {
			if !r.ts.Before(cutoff) {
				kept = append(kept, r)
			}
		}
		cs.reqs = kept
		if len(cs.reqs) == 0 {
			delete(p.conns, k)
		}
	}
	for k, r := range p.redisDB {
		if r.ts.Before(cutoff) {
			delete(p.redisDB, k)
		}
	}
}

// --- helpers ----------------------------------------------------------------

func connKey(netFlow, transport gopacket.Flow) string {
	a := netFlow.Src().String() + ":" + transport.Src().String()
	b := netFlow.Dst().String() + ":" + transport.Dst().String()
	if a < b {
		return a + "|" + b
	}
	return b + "|" + a
}

func flowEndpoints(netFlow, transport gopacket.Flow) (src, dst api.Endpoint) {
	src = api.Endpoint{IP: netFlow.Src().String(), Port: portOf(transport.Src().String())}
	dst = api.Endpoint{IP: netFlow.Dst().String(), Port: portOf(transport.Dst().String())}
	return
}

func dnsKey(clientIP string, id uint16) string {
	return clientIP + "/" + strconv.Itoa(int(id))
}

func portOf(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// drainBody reads up to p.bodyCap bytes of a message body, always consuming the
// rest so the stream advances. It returns the snippet, whether it was cut, and
// the full body length. When capture is disabled the body is discarded.
//
// A one-byte probe gates buffer allocation so an empty body (a GET request, a
// 204/304 response) allocates nothing, and the snippet buffer then grows only
// with the bytes actually present rather than a fixed p.bodyCap scratch buffer.
func (p *pipeline) drainBody(rc io.ReadCloser) (body string, truncated bool, full int) {
	if rc == nil {
		return "", false, 0
	}
	defer rc.Close()
	if !p.captureBodies || p.bodyCap <= 0 {
		io.Copy(io.Discard, rc)
		return "", false, 0
	}
	var first [1]byte
	if n, _ := io.ReadFull(rc, first[:]); n == 0 {
		return "", false, 0 // empty body — no allocation
	}
	var b bytes.Buffer
	b.WriteByte(first[0])
	n, _ := io.Copy(&b, io.LimitReader(rc, int64(p.bodyCap-1)))
	extra, _ := io.Copy(io.Discard, rc) // consume the rest so the stream advances
	return b.String(), extra > 0, 1 + int(n) + int(extra)
}

// capReader tees up to max bytes into buf while passing every read through. It
// records the first "connection head" bytes of one direction for the Raw view.
type capReader struct {
	r     io.Reader
	buf   []byte
	total int
	max   int
}

func newCapReader(r io.Reader, max int) *capReader {
	return &capReader{r: r, max: max, buf: make([]byte, 0, max)}
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.total += n
		if room := c.max - len(c.buf); room > 0 {
			take := n
			if take > room {
				take = room
			}
			c.buf = append(c.buf, p[:take]...)
		}
	}
	return n, err
}

func (c *capReader) raw() *api.RawView {
	if len(c.buf) == 0 {
		return nil
	}
	return &api.RawView{
		Hex:       hexDump(c.buf, c.max),
		Bytes:     c.total,
		Truncated: c.total > len(c.buf),
	}
}

// capture wraps a stream reader with a bounded recording tee (for the Raw view)
// unless raw capture is disabled (rawCap < 0), in which case cr is nil.
func (p *pipeline) capture(r io.Reader) (io.Reader, *capReader) {
	if p.rawCap < 0 {
		return r, nil
	}
	cr := newCapReader(r, p.rawCap)
	return cr, cr
}

// rawOf returns the RawView for a capReader, or nil when raw capture is off.
func rawOf(cr *capReader) *api.RawView {
	if cr == nil {
		return nil
	}
	return cr.raw()
}

// rawViewFromBytes builds a RawView from a single already-available byte
// slice — for capture paths with no bufio.Reader to tee (DNS, generic L4/ICMP
// flows), unlike the streaming capReader above. Returns nil when raw capture
// is disabled (cap < 0) or b is empty.
func rawViewFromBytes(b []byte, cap int) *api.RawView {
	if cap < 0 || len(b) == 0 {
		return nil
	}
	limit := len(b)
	if limit > cap {
		limit = cap
	}
	return &api.RawView{
		Hex:       hexDump(b[:limit], limit),
		Bytes:     len(b),
		Truncated: len(b) > limit,
	}
}

// sensitiveQueryParams are credential-bearing URL query parameter names
// whose values are scrubbed when redaction is on (names are kept so a
// scrubbed param's presence stays observable) — reuses redactHeaders rather
// than a dedicated flag, since both are the same "don't leak HTTP
// credentials into the capture" concern with the same safe default (on).
var sensitiveQueryParams = map[string]bool{
	"access_token":  true,
	"api_key":       true,
	"apikey":        true,
	"auth":          true,
	"client_secret": true,
	"password":      true,
	"refresh_token": true,
	"secret":        true,
	"session_token": true,
	"sig":           true,
	"signature":     true,
	"token":         true,
}

// parseQuery renders a URL's query parameters as a flat map (nil if none),
// scrubbing sensitiveQueryParams values when redact is on.
func parseQuery(u *url.URL, redact bool) map[string]string {
	if u == nil || u.RawQuery == "" {
		return nil
	}
	q := u.Query()
	if len(q) == 0 {
		return nil
	}
	out := make(map[string]string, len(q))
	for k, v := range q {
		if redact && sensitiveQueryParams[strings.ToLower(k)] {
			out[k] = redactedValue
			continue
		}
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// redactedRequestURI renders u's path+query the way url.URL.RequestURI()
// does, but with sensitiveQueryParams values scrubbed — used for Path and
// Summary, which otherwise embed the raw (unredacted) query string even
// when HTTP.Query itself is scrubbed via parseQuery. Falls back to the
// original RequestURI() when nothing needed scrubbing, since re-encoding an
// untouched query string (net/url.Values.Encode sorts keys and re-escapes)
// would otherwise change formatting for no reason.
func redactedRequestURI(u *url.URL, redact bool) string {
	if !redact || u.RawQuery == "" {
		return u.RequestURI()
	}
	q := u.Query()
	changed := false
	for k := range q {
		if sensitiveQueryParams[strings.ToLower(k)] {
			q[k] = []string{redactedValue}
			changed = true
		}
	}
	if !changed {
		return u.RequestURI()
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return path + "?" + q.Encode()
}

// sensitiveHeaders are credential-bearing headers whose values are scrubbed
// when redaction is on (keys are kept so their presence stays observable).
var sensitiveHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
	"x-auth-token":        true,
}

const redactedValue = "[REDACTED]"

func (p *pipeline) flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		lk := strings.ToLower(k)
		if p.redactHeaders && sensitiveHeaders[lk] {
			out[lk] = redactedValue
			continue
		}
		out[lk] = strings.Join(v, ", ")
	}
	return out
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func dnsRcode(c layers.DNSResponseCode) string {
	if c == layers.DNSResponseCodeNoErr {
		return "NOERROR"
	}
	return c.String()
}

// dnsRcodeName renders a response code with the canonical short DNS name (e.g.
// "NXDOMAIN"), used for the DNSDetail.Rcode field and the dns.rcode filter.
func dnsRcodeName(c layers.DNSResponseCode) string {
	switch c {
	case layers.DNSResponseCodeNoErr:
		return "NOERROR"
	case layers.DNSResponseCodeFormErr:
		return "FORMERR"
	case layers.DNSResponseCodeServFail:
		return "SERVFAIL"
	case layers.DNSResponseCodeNXDomain:
		return "NXDOMAIN"
	case layers.DNSResponseCodeNotImp:
		return "NOTIMP"
	case layers.DNSResponseCodeRefused:
		return "REFUSED"
	default:
		return c.String()
	}
}

// dnsQuestions renders the question section into wire DNSQuestion values.
func dnsQuestions(qs []layers.DNSQuestion) []api.DNSQuestion {
	if len(qs) == 0 {
		return nil
	}
	out := make([]api.DNSQuestion, 0, len(qs))
	for _, q := range qs {
		out = append(out, api.DNSQuestion{
			Name:  string(q.Name),
			Type:  q.Type.String(),
			Class: q.Class.String(),
		})
	}
	return out
}

// dnsRecords renders a resource-record section into wire DNSRecord values.
func dnsRecords(rrs []layers.DNSResourceRecord) []api.DNSRecord {
	if len(rrs) == 0 {
		return nil
	}
	out := make([]api.DNSRecord, 0, len(rrs))
	for _, rr := range rrs {
		out = append(out, api.DNSRecord{
			Name: string(rr.Name),
			Type: rr.Type.String(),
			TTL:  rr.TTL,
			Data: dnsRecordData(rr),
		})
	}
	return out
}

// dnsRecordData renders the rdata of a resource record to a printable string.
func dnsRecordData(rr layers.DNSResourceRecord) string {
	switch {
	case rr.IP != nil:
		return rr.IP.String()
	case len(rr.CNAME) > 0:
		return string(rr.CNAME)
	case len(rr.NS) > 0:
		return string(rr.NS)
	case len(rr.PTR) > 0:
		return string(rr.PTR)
	case len(rr.TXTs) > 0:
		parts := make([]string, len(rr.TXTs))
		for i, t := range rr.TXTs {
			parts[i] = string(t)
		}
		return strings.Join(parts, " ")
	case rr.SOA.MName != nil:
		return string(rr.SOA.MName) + " " + string(rr.SOA.RName)
	case rr.SRV.Name != nil:
		return string(rr.SRV.Name)
	default:
		return ""
	}
}

func dnsStatus(c layers.DNSResponseCode) string {
	if c == layers.DNSResponseCodeNoErr {
		return "success"
	}
	return "error"
}

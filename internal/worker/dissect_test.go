package worker

import (
	"bufio"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/pablocolson/k8shark/pkg/api"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// flows builds matching request/response gopacket flows for a client on
// clientPort talking to a server on serverPort.
func flows(clientPort, serverPort int) (reqNet, reqTr, respNet, respTr gopacket.Flow) {
	ipA := layers.NewIPEndpoint(net.IP{10, 0, 0, 1})
	ipB := layers.NewIPEndpoint(net.IP{10, 0, 0, 2})
	c := layers.NewTCPPortEndpoint(layers.TCPPort(clientPort))
	s := layers.NewTCPPortEndpoint(layers.TCPPort(serverPort))
	reqNet, _ = gopacket.FlowFromEndpoints(ipA, ipB)
	reqTr, _ = gopacket.FlowFromEndpoints(c, s)
	respNet, _ = gopacket.FlowFromEndpoints(ipB, ipA)
	respTr, _ = gopacket.FlowFromEndpoints(s, c)
	return
}

// drain collects all entries currently buffered on the sink.
func drain(s *sink) []*api.Entry {
	var out []*api.Entry
	for {
		select {
		case e := <-s.ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// --- Redis ------------------------------------------------------------------

func TestRedisBinaryValueRendering(t *testing.T) {
	// A SET of a gzip-compressed value: the command must render safely (key
	// stays readable, the binary value becomes a bounded hex preview) rather
	// than dumping raw bytes.
	gz := "\x1f\x8b\x08\x00" + strings.Repeat("\x00\xff\xfe", 40) // clearly non-printable, >32 bytes
	req := "*3\r\n$3\r\nSET\r\n$3\r\nkey\r\n$" + strconv.Itoa(len(gz)) + "\r\n" + gz + "\r\n"
	v, err := parseRESP(bufio.NewReader(strings.NewReader(req)))
	if err != nil {
		t.Fatal(err)
	}
	got := renderRedisCommand(v)
	if !strings.HasPrefix(got, "SET key \\x1f8b0800") {
		t.Errorf("command = %q, want it to start with the SET key + hex preview", got)
	}
	if strings.ContainsRune(got, 0x00) || strings.Contains(got, "\xff") {
		t.Errorf("command still contains raw binary bytes: %q", got)
	}
	if !strings.Contains(got, "bytes)") {
		t.Errorf("command %q should note the true byte length", got)
	}

	// A printable value passes through untouched.
	if d := redisDisplay("hello world"); d != "hello world" {
		t.Errorf("redisDisplay(printable) = %q, want unchanged", d)
	}
}

func TestRedisRESPRendering(t *testing.T) {
	cmd, err := parseRESP(bufio.NewReader(strings.NewReader("*2\r\n$3\r\nGET\r\n$5\r\nmykey\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if got := renderRedisCommand(cmd); got != "GET mykey" {
		t.Errorf("command = %q, want %q", got, "GET mykey")
	}

	cases := []struct {
		in      string
		summary string
		isErr   bool
	}{
		{"+OK\r\n", "OK", false},
		{"-ERR bad command\r\n", "ERR bad command", true},
		{":42\r\n", "(integer) 42", false},
		{"$3\r\nabc\r\n", "abc", false},
		{"$-1\r\n", "(nil)", false},
	}
	for _, c := range cases {
		v, err := parseRESP(bufio.NewReader(strings.NewReader(c.in)))
		if err != nil {
			t.Fatalf("parse %q: %v", c.in, err)
		}
		summary, _, isErr := renderRedisReply(v)
		if summary != c.summary || isErr != c.isErr {
			t.Errorf("reply %q = (%q,%v), want (%q,%v)", c.in, summary, isErr, c.summary, c.isErr)
		}
	}
}

func TestRedisPairingEndToEnd(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40000, redisPort)

	req := "*2\r\n$3\r\nGET\r\n$5\r\nmykey\r\n" + "*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n"
	resp := "$5\r\nvalue\r\n" + "+OK\r\n"

	p.consumeRedis(rNet, rTr, strings.NewReader(req), true, api.ProtocolRedis)
	p.consumeRedis(sNet, sTr, strings.NewReader(resp), false, api.ProtocolRedis)

	got := drain(s)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Protocol != api.ProtocolRedis || got[0].Request.Command != "GET mykey" || got[0].Response.Summary != "value" {
		t.Errorf("entry0 = %+v", got[0])
	}
	if got[1].Request.Command != "SET foo bar" || got[1].Response.Summary != "OK" {
		t.Errorf("entry1 = %+v", got[1])
	}
}

// --- Valkey / RESP port labelling ---------------------------------------------

// Default configuration (no RedisPorts/ValkeyPorts overrides) must keep
// emitting ProtocolRedis on port 6379, proving the Valkey feature is
// backward-compatible and opt-in only.
func TestConsumeStreamDefaultPortIsRedis(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40020, redisPort)

	req := "*1\r\n$4\r\nPING\r\n"
	resp := "+PONG\r\n"

	p.consumeStream(rNet, rTr, strings.NewReader(req))
	p.consumeStream(sNet, sTr, strings.NewReader(resp))

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Protocol != api.ProtocolRedis {
		t.Errorf("protocol = %q, want %q (default port 6379 must stay redis)", got[0].Protocol, api.ProtocolRedis)
	}
}

// A port configured (via respPorts, as buildRespPorts would populate it from
// --valkey-ports) as Valkey must emit ProtocolValkey entries, even though the
// bytes on the wire are indistinguishable from Redis.
func TestConsumeStreamConfiguredPortIsValkey(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	const valkeyPort = 16379
	p.respPorts = buildRespPorts(nil, []int{valkeyPort})
	rNet, rTr, sNet, sTr := flows(40021, valkeyPort)

	req := "*1\r\n$4\r\nPING\r\n"
	resp := "+PONG\r\n"

	p.consumeStream(rNet, rTr, strings.NewReader(req))
	p.consumeStream(sNet, sTr, strings.NewReader(resp))

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Protocol != api.ProtocolValkey {
		t.Errorf("protocol = %q, want %q", got[0].Protocol, api.ProtocolValkey)
	}
}

// buildRespPorts: Valkey wins when a port is listed in both RedisPorts and
// ValkeyPorts, and the 6379 default stays redis unless overridden.
func TestBuildRespPorts(t *testing.T) {
	rp := buildRespPorts(nil, nil)
	if rp[redisPort] != api.ProtocolRedis {
		t.Errorf("default respPorts[%d] = %q, want redis", redisPort, rp[redisPort])
	}

	rp = buildRespPorts([]int{7000}, []int{7000, 7001})
	if rp[7000] != api.ProtocolValkey {
		t.Errorf("overlapping port 7000 = %q, want valkey to win", rp[7000])
	}
	if rp[7001] != api.ProtocolValkey {
		t.Errorf("port 7001 = %q, want valkey", rp[7001])
	}
	if rp[redisPort] != api.ProtocolRedis {
		t.Errorf("default port %d = %q, want redis to remain unaffected", redisPort, rp[redisPort])
	}
}

// --- Postgres ---------------------------------------------------------------

func pgMsg(typ byte, payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:], uint32(4+len(payload)))
	copy(buf[5:], payload)
	return buf
}

func pgStartup() []byte { // SSLRequest: len=8, code=80877103
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:], 8)
	binary.BigEndian.PutUint32(b[4:], 80877103)
	return b
}

func TestPostgresPairingEndToEnd(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40001, pgPort)

	// Requests: startup (skipped) + Simple Query + extended Parse/Execute.
	var req []byte
	req = append(req, pgStartup()...)
	req = append(req, pgMsg('Q', []byte("SELECT id FROM t\x00"))...)
	// Parse: stmt "s1", query, then int16 numParams(0).
	req = append(req, pgMsg('P', []byte("s1\x00UPDATE t SET x=1\x00\x00\x00"))...)
	// Execute: empty portal cstr + int32 maxRows(0).
	req = append(req, pgMsg('E', []byte("\x00\x00\x00\x00\x00"))...)

	// Responses: two DataRows + CommandComplete for the SELECT, then a
	// CommandComplete for the UPDATE.
	var resp []byte
	resp = append(resp, pgMsg('D', []byte{0, 1})...)
	resp = append(resp, pgMsg('D', []byte{0, 1})...)
	resp = append(resp, pgMsg('C', []byte("SELECT 2\x00"))...)
	resp = append(resp, pgMsg('C', []byte("UPDATE 3\x00"))...)

	p.consumePostgres(rNet, rTr, strings.NewReader(string(req)), true)
	p.consumePostgres(sNet, sTr, strings.NewReader(string(resp)), false)

	got := drain(s)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Protocol != api.ProtocolPostgres || got[0].Request.Query != "SELECT id FROM t" {
		t.Errorf("entry0 request = %+v", got[0].Request)
	}
	if got[0].Response.Summary != "SELECT 2 (2 rows)" || got[0].Response.RowCount != 2 {
		t.Errorf("entry0 response = %+v", got[0].Response)
	}
	if got[1].Request.Query != "UPDATE t SET x=1" || got[1].Response.Summary != "UPDATE 3" {
		t.Errorf("entry1 = req %q resp %q", got[1].Request.Query, got[1].Response.Summary)
	}
}

func TestPostgresError(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40002, pgPort)

	req := pgMsg('Q', []byte("SELECT * FROM nope\x00"))
	// ErrorResponse: S=ERROR, C=42P01, M=relation "nope" does not exist, then 0.
	errPayload := []byte("SERROR\x00C42P01\x00Mrelation \"nope\" does not exist\x00\x00")
	resp := pgMsg('E', errPayload)

	p.consumePostgres(rNet, rTr, strings.NewReader(string(req)), true)
	p.consumePostgres(sNet, sTr, strings.NewReader(string(resp)), false)

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Status != "error" {
		t.Errorf("status = %q, want error", got[0].Status)
	}
	if !strings.Contains(got[0].Response.Summary, "does not exist") || !strings.Contains(got[0].Response.Summary, "42P01") {
		t.Errorf("summary = %q", got[0].Response.Summary)
	}
}

// --- regression tests for issues found by the adversarial review ------------

// Deeply nested RESP arrays must be rejected, not overflow the stack.
func TestRESPDepthGuard(t *testing.T) {
	in := strings.Repeat("*1\r\n", 100000) + "+OK\r\n"
	if _, err := parseRESP(bufio.NewReader(strings.NewReader(in))); err == nil {
		t.Fatal("expected error for over-deep RESP nesting, got nil")
	}
}

// A RESP3 map is ONE reply value, not several — otherwise it desyncs pairing.
func TestRESP3MapSingleReply(t *testing.T) {
	br := bufio.NewReader(strings.NewReader("%1\r\n$6\r\nserver\r\n$5\r\nredis\r\n"))
	n := 0
	for {
		if _, err := parseRESP(br); err != nil {
			break
		}
		n++
	}
	if n != 1 {
		t.Fatalf("RESP3 map parsed as %d values, want 1", n)
	}
}

// An unsolicited pub/sub message must not steal a pending request's response.
func TestRedisPubSubNotMispaired(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40010, redisPort)

	req := "*2\r\n$9\r\nSUBSCRIBE\r\n$4\r\nnews\r\n" + "*2\r\n$3\r\nGET\r\n$3\r\nfoo\r\n"
	resp := "*3\r\n$9\r\nsubscribe\r\n$4\r\nnews\r\n:1\r\n" + // reply to SUBSCRIBE
		"*3\r\n$7\r\nmessage\r\n$4\r\nnews\r\n$5\r\nhello\r\n" + // unsolicited push
		"$3\r\nbar\r\n" // reply to GET

	p.consumeRedis(rNet, rTr, strings.NewReader(req), true, api.ProtocolRedis)
	p.consumeRedis(sNet, sTr, strings.NewReader(resp), false, api.ProtocolRedis)

	var getEntry *api.Entry
	for _, e := range drain(s) {
		if e.Request.Command == "GET foo" {
			getEntry = e
		}
	}
	if getEntry == nil {
		t.Fatal("GET foo entry missing")
	}
	if getEntry.Response.Summary != "bar" {
		t.Errorf("GET foo paired with %q, want \"bar\" (pub/sub push mispaired it)", getEntry.Response.Summary)
	}
}

// startupMessage builds a plaintext libpq StartupMessage (protocol 3.0).
func startupMessage() []byte {
	params := []byte("user\x00postgres\x00\x00")
	body := make([]byte, 4+len(params))
	binary.BigEndian.PutUint32(body[0:], 0x00030000)
	copy(body[4:], params)
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out[0:], uint32(4+len(body)))
	copy(out[4:], body)
	return out
}

// --- WS3 richer extraction: sub-object population ---------------------------

func appendU16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }
func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

type pgCol struct {
	name string
	oid  uint32
}

// pgBindPayload builds a Bind message payload with text-format params.
func pgBindPayload(portal, stmt string, params []string) []byte {
	var b []byte
	b = append(append(b, []byte(portal)...), 0)
	b = append(append(b, []byte(stmt)...), 0)
	b = appendU16(b, 1) // one param format code
	b = appendU16(b, 0) // text
	b = appendU16(b, uint16(len(params)))
	for _, pv := range params {
		b = appendU32(b, uint32(len(pv)))
		b = append(b, []byte(pv)...)
	}
	return appendU16(b, 0) // no result format codes
}

// pgRowDesc builds a RowDescription payload for the given columns.
func pgRowDesc(cols ...pgCol) []byte {
	b := appendU16(nil, uint16(len(cols)))
	for _, c := range cols {
		b = append(append(b, []byte(c.name)...), 0)
		b = appendU32(b, 0)          // tableOID
		b = appendU16(b, 0)          // colAttr
		b = appendU32(b, c.oid)      // typeOID
		b = appendU16(b, 0xffff)     // typeSize (-1)
		b = appendU32(b, 0xffffffff) // typeMod (-1)
		b = appendU16(b, 0)          // format
	}
	return b
}

// Extended-query Parse+Bind+Execute -> typed params/columns/tag on the entry.
func TestPostgresExtendedDetail(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40030, pgPort)

	var req []byte
	req = append(req, pgStartup()...)
	req = append(req, pgMsg('P', []byte("s1\x00SELECT id,email FROM users WHERE id=$1 AND name=$2\x00\x00\x00"))...)
	req = append(req, pgMsg('B', pgBindPayload("", "s1", []string{"42", "bob"}))...)
	req = append(req, pgMsg('E', []byte("\x00\x00\x00\x00\x00"))...)

	var resp []byte
	resp = append(resp, pgMsg('T', pgRowDesc(pgCol{"id", 23}, pgCol{"email", 1043}))...)
	resp = append(resp, pgMsg('D', []byte{0, 2})...)
	resp = append(resp, pgMsg('C', []byte("SELECT 1\x00"))...)

	p.consumePostgres(rNet, rTr, strings.NewReader(string(req)), true)
	p.consumePostgres(sNet, sTr, strings.NewReader(string(resp)), false)

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Request.Postgres == nil || e.Request.Postgres.StatementName != "s1" {
		t.Fatalf("statement name = %+v", e.Request.Postgres)
	}
	if got := e.Request.Postgres.Params; len(got) != 2 || got[0] != "42" || got[1] != "bob" {
		t.Errorf("params = %v, want [42 bob]", got)
	}
	if e.Response.Postgres == nil || e.Response.Postgres.Tag != "SELECT 1" {
		t.Fatalf("tag = %+v", e.Response.Postgres)
	}
	cols := e.Response.Postgres.Columns
	if len(cols) != 2 || cols[0].Name != "id" || cols[0].Type != "int4" || cols[1].Type != "varchar" {
		t.Errorf("columns = %+v", cols)
	}
}

// ErrorResponse -> typed PGError fields.
func TestPostgresErrorDetail(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40031, pgPort)

	req := pgMsg('Q', []byte("SELECT * FROM nope\x00"))
	errPayload := []byte("SERROR\x00VERROR\x00C42P01\x00Mrelation \"nope\" does not exist\x00Htry again\x00\x00")
	resp := pgMsg('E', errPayload)

	p.consumePostgres(rNet, rTr, strings.NewReader(string(req)), true)
	p.consumePostgres(sNet, sTr, strings.NewReader(string(resp)), false)

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	pe := got[0].Response.Postgres
	if pe == nil || pe.Error == nil {
		t.Fatalf("no PGError: %+v", got[0].Response.Postgres)
	}
	if pe.Error.Code != "42P01" || pe.Error.Severity != "ERROR" || pe.Error.Hint != "try again" {
		t.Errorf("error = %+v", pe.Error)
	}
	if !strings.Contains(pe.Error.Message, "does not exist") {
		t.Errorf("message = %q", pe.Error.Message)
	}
}

// SELECT n + pipelined GET + RESP3 attribute reply -> DBIndex/PipelineDepth/
// ReplyType/Attributes.
func TestRedisDetail(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40040, redisPort)

	req := "*2\r\n$6\r\nSELECT\r\n$1\r\n2\r\n" + "*2\r\n$3\r\nGET\r\n$3\r\nfoo\r\n"
	resp := "+OK\r\n" + "|1\r\n$3\r\nttl\r\n:60\r\n$3\r\nbar\r\n"

	p.consumeRedis(rNet, rTr, strings.NewReader(req), true, api.ProtocolRedis)
	p.consumeRedis(sNet, sTr, strings.NewReader(resp), false, api.ProtocolRedis)

	var get *api.Entry
	for _, e := range drain(s) {
		if e.Request.Command == "GET foo" {
			get = e
		}
	}
	if get == nil {
		t.Fatal("GET foo entry missing")
	}
	if get.Request.Redis == nil || get.Request.Redis.DBIndex != 2 {
		t.Errorf("DBIndex = %+v, want 2", get.Request.Redis)
	}
	if get.Request.Redis.PipelineDepth != 1 {
		t.Errorf("PipelineDepth = %d, want 1", get.Request.Redis.PipelineDepth)
	}
	if get.Response.Redis == nil || get.Response.Redis.Reply != "bar" || get.Response.Redis.ReplyType != "string" {
		t.Errorf("reply = %+v", get.Response.Redis)
	}
	if get.Response.Redis.Attributes["ttl"] != "60" {
		t.Errorf("attributes = %+v, want ttl=60", get.Response.Redis.Attributes)
	}
}

func TestRESP3MapReplyType(t *testing.T) {
	v, err := parseRESP(bufio.NewReader(strings.NewReader("%1\r\n$1\r\na\r\n$1\r\nb\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if got := respTypeName(v.typ); got != "map" {
		t.Errorf("replyType = %q, want map", got)
	}
}

// A NOERROR response with multiple A answers -> DNSDetail.Answers/Rcode.
func TestDNSAnswerDetail(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	reqNet, _, respNet, _ := flows(50000, 53)

	q := &layers.DNS{ID: 7, Questions: []layers.DNSQuestion{{Name: []byte("svc.local"), Type: layers.DNSTypeA, Class: layers.DNSClassIN}}}
	p.handleDNS(reqNet, &layers.UDP{SrcPort: 50000, DstPort: 53}, q)

	resp := &layers.DNS{
		ID: 7, QR: true, AA: true, RA: true, ResponseCode: layers.DNSResponseCodeNoErr,
		Answers: []layers.DNSResourceRecord{
			{Name: []byte("svc.local"), Type: layers.DNSTypeA, TTL: 30, IP: net.IP{1, 2, 3, 4}},
			{Name: []byte("svc.local"), Type: layers.DNSTypeA, TTL: 30, IP: net.IP{5, 6, 7, 8}},
		},
	}
	p.handleDNS(respNet, &layers.UDP{SrcPort: 53, DstPort: 50000}, resp)

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Request.DNS == nil || len(e.Request.DNS.Questions) != 1 || e.Request.DNS.Questions[0].Type != "A" {
		t.Errorf("question detail = %+v", e.Request.DNS)
	}
	if e.Response.DNS == nil || e.Response.DNS.Rcode != "NOERROR" || !e.Response.DNS.Authoritative {
		t.Fatalf("response detail = %+v", e.Response.DNS)
	}
	if a := e.Response.DNS.Answers; len(a) != 2 || a[0].Data != "1.2.3.4" || a[1].Data != "5.6.7.8" {
		t.Errorf("answers = %+v", e.Response.DNS.Answers)
	}
}

func TestDNSNXDomainDetail(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	reqNet, _, respNet, _ := flows(50001, 53)

	q := &layers.DNS{ID: 8, Questions: []layers.DNSQuestion{{Name: []byte("nope.local"), Type: layers.DNSTypeA, Class: layers.DNSClassIN}}}
	p.handleDNS(reqNet, &layers.UDP{SrcPort: 50001, DstPort: 53}, q)

	resp := &layers.DNS{
		ID: 8, QR: true, RA: true, ResponseCode: layers.DNSResponseCodeNXDomain,
		Authorities: []layers.DNSResourceRecord{
			{Name: []byte("local"), Type: layers.DNSTypeSOA, TTL: 30, SOA: layers.DNSSOA{MName: []byte("ns.local"), RName: []byte("hostmaster.local")}},
		},
	}
	p.handleDNS(respNet, &layers.UDP{SrcPort: 53, DstPort: 50001}, resp)

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Response.DNS == nil || e.Response.DNS.Rcode != "NXDOMAIN" {
		t.Errorf("rcode = %+v, want NXDOMAIN", e.Response.DNS)
	}
	if len(e.Response.DNS.Authority) != 1 {
		t.Errorf("authority = %+v", e.Response.DNS.Authority)
	}
	if e.Status != "error" {
		t.Errorf("status = %q, want error", e.Status)
	}
}

// --- WS5: AMQP (RabbitMQ 0-9-1) ---------------------------------------------

func amqpFrame(ftype byte, channel uint16, payload []byte) []byte {
	b := make([]byte, 7+len(payload)+1)
	b[0] = ftype
	binary.BigEndian.PutUint16(b[1:], channel)
	binary.BigEndian.PutUint32(b[3:], uint32(len(payload)))
	copy(b[7:], payload)
	b[7+len(payload)] = 0xCE
	return b
}

func amqpMethod(class, method uint16, args []byte) []byte {
	p := make([]byte, 4+len(args))
	binary.BigEndian.PutUint16(p[0:], class)
	binary.BigEndian.PutUint16(p[2:], method)
	copy(p[4:], args)
	return p
}

func amqpShortStrBytes(s string) []byte { return append([]byte{byte(len(s))}, s...) }

const amqpHeader = "AMQP\x00\x00\x09\x01"

// Basic.Publish + content header + body -> one entry with exchange/rk/body.
func TestAMQPBasicPublish(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, _, _ := flows(40100, amqpPort)

	// Basic.Publish args: reserved short(0) + exchange + routing-key + bits(1).
	var args []byte
	args = appendU16(args, 0)
	args = append(args, amqpShortStrBytes("orders")...)
	args = append(args, amqpShortStrBytes("new")...)
	args = append(args, 0x00) // mandatory/immediate bits
	publish := amqpFrame(amqpFrameMethod, 1, amqpMethod(amqpClassBasic, 40, args))

	// Content header: class(2) weight(2) body-size(8)=5 flags(2).
	var hdr []byte
	hdr = appendU16(hdr, amqpClassBasic)
	hdr = appendU16(hdr, 0)
	hdr = append(hdr, 0, 0, 0, 0, 0, 0, 0, 5) // body-size = 5
	hdr = appendU16(hdr, 0)
	header := amqpFrame(amqpFrameHeader, 1, hdr)
	body := amqpFrame(amqpFrameBody, 1, []byte("hello"))

	stream := amqpHeader + string(publish) + string(header) + string(body)
	p.consumeStream(rNet, rTr, strings.NewReader(stream))

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Protocol != api.ProtocolAMQP {
		t.Errorf("protocol = %q, want amqp", e.Protocol)
	}
	if e.Request.Exchange != "orders" || e.Request.RoutingKey != "new" {
		t.Errorf("exchange/rk = %q/%q, want orders/new", e.Request.Exchange, e.Request.RoutingKey)
	}
	if e.Request.Body != "hello" {
		t.Errorf("body = %q, want hello", e.Request.Body)
	}
	if !strings.Contains(e.Request.Summary, "PUBLISH orders/new") {
		t.Errorf("summary = %q", e.Request.Summary)
	}
}

// Queue.Declare surfaces without any content frames.
func TestAMQPQueueDeclare(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, _, _ := flows(40101, amqpPort)

	var args []byte
	args = appendU16(args, 0) // reserved
	args = append(args, amqpShortStrBytes("payments")...)
	frame := amqpFrame(amqpFrameMethod, 1, amqpMethod(amqpClassQueue, 10, args))

	p.consumeStream(rNet, rTr, strings.NewReader(amqpHeader+string(frame)))

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Request.Queue != "payments" {
		t.Errorf("queue = %q, want payments", got[0].Request.Queue)
	}
	if !strings.Contains(got[0].Request.Summary, "QUEUE.DECLARE payments") {
		t.Errorf("summary = %q", got[0].Request.Summary)
	}
}

// Connection.Close with a >=400 reply-code is an error entry.
func TestAMQPConnectionCloseIsError(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, _, _ := flows(40102, amqpPort)

	var args []byte
	args = appendU16(args, 320) // reply-code CONNECTION_FORCED
	args = append(args, amqpShortStrBytes("CONNECTION_FORCED")...)
	args = appendU16(args, 0) // class
	args = appendU16(args, 0) // method
	frame := amqpFrame(amqpFrameMethod, 0, amqpMethod(amqpClassConnection, 50, args))

	p.consumeStream(rNet, rTr, strings.NewReader(amqpHeader+string(frame)))

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Status != "error" {
		t.Errorf("status = %q, want error", got[0].Status)
	}
}

// A frame whose frame-end byte != 0xCE (garbled/TLS) must bail without emitting.
func TestAMQPFramingGuard(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, _, _ := flows(40103, amqpPort)

	frame := amqpFrame(amqpFrameMethod, 1, amqpMethod(amqpClassQueue, 10, append(appendU16(nil, 0), amqpShortStrBytes("q")...)))
	frame[len(frame)-1] = 0x00 // corrupt the frame-end sentinel

	p.consumeStream(rNet, rTr, strings.NewReader(amqpHeader+string(frame)))

	if got := drain(s); len(got) != 0 {
		t.Fatalf("got %d entries, want 0 on framing error", len(got))
	}
}

// An AMQP 1.0 protocol header must be detected and skipped (no entries, no panic).
func TestAMQP10Skipped(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, _, _ := flows(40104, amqpPort)

	stream := "AMQP\x00\x01\x00\x00" + "some 1.0 performative junk that must not parse"
	p.consumeStream(rNet, rTr, strings.NewReader(stream))

	if got := drain(s); len(got) != 0 {
		t.Fatalf("got %d entries, want 0 for AMQP 1.0", len(got))
	}
}

// A port configured as Valkey emits ProtocolValkey entries (label-only relabel).
func TestValkeyLabel(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	p.respPorts = buildRespPorts(nil, []int{6380})
	rNet, rTr, sNet, sTr := flows(40105, 6380)

	p.consumeStream(rNet, rTr, strings.NewReader("*1\r\n$4\r\nPING\r\n"))
	p.consumeStream(sNet, sTr, strings.NewReader("+PONG\r\n"))

	got := drain(s)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Protocol != api.ProtocolValkey || got[0].Request.Command != "PING" {
		t.Errorf("entry = %q %q, want valkey PING", got[0].Protocol, got[0].Request.Command)
	}
}

// sslmode=prefer against a non-TLS server sends SSLRequest THEN a plaintext
// StartupMessage — two consecutive untyped messages. Both must be skipped.
func TestPostgresSSLPreferDoubleUntyped(t *testing.T) {
	s := newSink("", "", "n", discardLogger())
	p := newPipeline(s, "n", discardLogger())
	rNet, rTr, sNet, sTr := flows(40011, pgPort)

	var req []byte
	req = append(req, pgStartup()...)      // untyped #1: SSLRequest
	req = append(req, startupMessage()...) // untyped #2: plaintext StartupMessage
	req = append(req, pgMsg('Q', []byte("SELECT 1\x00"))...)
	resp := pgMsg('C', []byte("SELECT 1\x00"))

	p.consumePostgres(rNet, rTr, strings.NewReader(string(req)), true)
	p.consumePostgres(sNet, sTr, strings.NewReader(string(resp)), false)

	got := drain(s)
	if len(got) != 1 || got[0].Request.Query != "SELECT 1" {
		t.Fatalf("expected 1 paired SELECT after skipping both untyped msgs, got %d: %+v", len(got), got)
	}
}

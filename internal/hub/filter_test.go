package hub

import (
	"strings"
	"testing"

	"github.com/pablocolson/k8shark/pkg/api"
)

func sample() *api.Entry {
	return &api.Entry{
		Protocol:    api.ProtocolHTTP,
		Node:        "node-1",
		Status:      "error",
		StatusCode:  503,
		Source:      api.Endpoint{IP: "10.0.1.10", Port: 34567, Name: "frontend", Namespace: "shop"},
		Destination: api.Endpoint{IP: "10.0.1.14", Port: 8080, Name: "payment", Namespace: "shop"},
		Request:     api.Payload{Method: "POST", Path: "/api/checkout", Host: "payment.shop"},
		Response:    api.Payload{StatusCode: 503},
	}
}

func TestCompileFilter(t *testing.T) {
	e := sample()
	cases := []struct {
		expr string
		want bool
	}{
		{"", true},
		{`protocol == "http"`, true},
		{`protocol == "dns"`, false},
		{`protocol != "dns"`, true},
		{"response.status >= 500", true},
		{"response.status > 503", false},
		{"response.status >= 500 and http.method == \"POST\"", true},
		{"response.status < 500 or dst.name == \"payment\"", true},
		{`http.method == "GET"`, false},
		{`request.path contains "checkout"`, true},
		{`request.path contains "cart"`, false},
		{`not (protocol == "dns")`, true},
		{`dst.namespace == "shop" and src.name == "frontend"`, true},
		{`status == "error"`, true},
		{"checkout", true},           // full-text
		{"nonexistent-token", false}, // full-text miss
		{`dst.port == 8080`, true},
	}
	for _, c := range cases {
		pred, err := CompileFilter(c.expr)
		if err != nil {
			t.Errorf("CompileFilter(%q) error: %v", c.expr, err)
			continue
		}
		if got := pred(e); got != c.want {
			t.Errorf("filter %q = %v, want %v", c.expr, got, c.want)
		}
	}
}

// TestNamespaceFilter exercises the bare "namespace"/"ns" field, which
// matches either src or dst rather than a single struct field — sample()
// isn't useful here since both sides share the same namespace, so this uses
// an entry with two distinct ones.
func TestNamespaceFilter(t *testing.T) {
	e := &api.Entry{
		Source:      api.Endpoint{Namespace: "shop"},
		Destination: api.Endpoint{Namespace: "platform"},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{`namespace == "shop"`, true},       // matches src
		{`namespace == "platform"`, true},   // matches dst
		{`namespace == "kube-system"`, false}, // matches neither
		{`ns == "shop"`, true},              // alias
		{`namespace contains "plat"`, true}, // substring on dst
		// != means "neither side matches" (exclude noise), not the De
		// Morgan-literal "either side differs" (which response.status-style
		// != would make true for nearly every entry here).
		{`namespace != "shop"`, false},        // src does match "shop"
		{`namespace != "kube-system"`, true},  // neither side is kube-system
	}
	for _, c := range cases {
		pred, err := CompileFilter(c.expr)
		if err != nil {
			t.Errorf("CompileFilter(%q) error: %v", c.expr, err)
			continue
		}
		if got := pred(e); got != c.want {
			t.Errorf("filter %q = %v, want %v", c.expr, got, c.want)
		}
	}
}

// richEntry exercises the WS3 sub-object filter fields.
func richEntry() *api.Entry {
	return &api.Entry{
		Protocol: api.ProtocolPostgres,
		Status:   "error",
		Request: api.Payload{
			Size:     12,
			HTTP:     &api.HTTPDetail{Version: "HTTP/2.0", ContentType: "application/grpc"},
			Redis:    &api.RedisDetail{DBIndex: 3, PipelineDepth: 2},
			Postgres: &api.PGDetail{StatementName: "stmt_s1", Portal: "p1"},
			DNS:      &api.DNSDetail{Questions: []api.DNSQuestion{{Name: "x", Type: "AAAA"}}},
		},
		Response: api.Payload{
			ContentType: "application/json",
			Size:        99,
			RowCount:    7,
			DNS: &api.DNSDetail{
				Rcode: "NXDOMAIN", Answers: []api.DNSRecord{{Data: "1.2.3.4"}},
				Authoritative: true, RecursionAvl: true,
			},
			Redis:    &api.RedisDetail{Reply: "OK"},
			Postgres: &api.PGDetail{Error: &api.PGError{Code: "40P01"}, TxStatus: "E"},
			HTTP:     &api.HTTPDetail{TTFBMs: 42},
		},
		L4: &api.L4Info{
			TTL: 64, Retransmits: 2, MSS: 1460, Window: 64240, RTTMs: 1.5, ClientBytes: 100,
			SrcMAC: "aa:bb:cc:dd:ee:ff", DstMAC: "11:22:33:44:55:66", IPVersion: 4, IPFlags: "DF",
			ClientTCPFlags: "SYN,ACK", ServerTCPFlags: "SYN,ACK,FIN", SeqStart: 1000, AckStart: 2000,
			DurationMs: 250, ClientPackets: 5, ServerPackets: 7,
			TLS: &api.TLSInfo{SNI: "api.example.com"},
		},
	}
}

func TestCompileFilterRichFields(t *testing.T) {
	e := richEntry()
	cases := []struct {
		expr string
		want bool
	}{
		{`l4.retransmits > 0`, true},
		{`l4.retransmits == 2`, true},
		{`l4.ttl == 64`, true},
		{`l4.mss >= 1400`, true},
		{`dns.rcode == "NXDOMAIN"`, true},
		{`dns.rcode == "NOERROR"`, false},
		{`dns.type == "AAAA"`, true},
		{`postgres.statement contains "s1"`, true},
		{`postgres.error == "40P01"`, true},
		{`pg.code == "40P01"`, true},
		{`postgres.txstatus == "E"`, true},
		{`http.version == "HTTP/2.0"`, true},
		{`response.contenttype contains "json"`, true},
		{`redis.db == 3`, true},
		{`redis.reply == "OK"`, true},
		{`tls.sni contains "example.com"`, true},
		{`tls.sni == "other"`, false},
		{"api.example.com", true}, // full-text via SNI
		{"1.2.3.4", true},         // full-text via DNS answer data

		// Previously display-only fields, now filterable.
		{`redis.pipelinedepth == 2`, true},
		{`redis.pipelinedepth > 5`, false},
		{`postgres.portal == "p1"`, true},
		{`dns.authoritative == "true"`, true},
		{`dns.recursionavailable == "true"`, true},
		{`dns.recursionavl == "true"`, true},
		{`request.size == 12`, true},
		{`response.size > 50`, true},
		{`postgres.rowcount == 7`, true},
		{`rowcount == 7`, true},
		{`http.ttfbms == 42`, true},

		// Remaining L4Info fields.
		{`l4.srcmac == "aa:bb:cc:dd:ee:ff"`, true},
		{`l4.dstmac contains "55:66"`, true},
		{`l4.ipversion == 4`, true},
		{`l4.ipflags == "DF"`, true},
		{`l4.clienttcpflags contains "SYN"`, true},
		{`l4.servertcpflags == "SYN,ACK,FIN"`, true},
		{`l4.seqstart == 1000`, true},
		{`l4.ackstart == 2000`, true},
		{`l4.durationms >= 250`, true},
		{`l4.clientpackets == 5`, true},
		{`l4.serverpackets == 7`, true},
	}
	for _, c := range cases {
		pred, err := CompileFilter(c.expr)
		if err != nil {
			t.Errorf("CompileFilter(%q) error: %v", c.expr, err)
			continue
		}
		if got := pred(e); got != c.want {
			t.Errorf("filter %q = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestCompileFilterAMQP(t *testing.T) {
	e := &api.Entry{
		Protocol: api.ProtocolAMQP,
		Status:   "success",
		Request: api.Payload{
			Class: "Basic", Method: "Publish", Exchange: "orders", RoutingKey: "new",
			Queue: "payments", DeliveryTag: 42, Summary: "PUBLISH orders/new",
		},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{`protocol == "amqp"`, true},
		{`protocol == "amqp" and amqp.exchange == "orders"`, true},
		{`amqp.routingkey == "new"`, true},
		{`amqp.routing-key == "new"`, true},
		{`amqp.queue == "payments"`, true},
		{`amqp.deliverytag == 42`, true},
		{`amqp.class == "Basic"`, true},
		{`amqp.method == "Publish"`, true},
		{`amqp.exchange == "other"`, false},
		{"orders", true}, // full-text via exchange
	}
	for _, c := range cases {
		pred, err := CompileFilter(c.expr)
		if err != nil {
			t.Errorf("CompileFilter(%q) error: %v", c.expr, err)
			continue
		}
		if got := pred(e); got != c.want {
			t.Errorf("filter %q = %v, want %v", c.expr, got, c.want)
		}
	}

	// amqp.method must not leak HTTP verbs (Method is a shared field).
	http := &api.Entry{Protocol: api.ProtocolHTTP, Request: api.Payload{Method: "POST"}}
	pred, _ := CompileFilter(`amqp.method == "POST"`)
	if pred(http) {
		t.Error("amqp.method matched an HTTP POST (should be AMQP-scoped)")
	}
}

// Numeric L4 fields must not spuriously match when L4 is absent (missing => ""
// not "0").
func TestL4FieldMissingIsEmpty(t *testing.T) {
	e := &api.Entry{Protocol: api.ProtocolHTTP}
	pred, err := CompileFilter(`l4.retransmits == 0`)
	if err != nil {
		t.Fatal(err)
	}
	if pred(e) {
		t.Error(`l4.retransmits == 0 matched an entry with no L4 (want false)`)
	}
}

func TestCompileFilterErrors(t *testing.T) {
	for _, expr := range []string{`protocol == `, `( protocol == "http"`, `"unterminated`} {
		if _, err := CompileFilter(expr); err == nil {
			t.Errorf("expected error for %q, got nil", expr)
		}
	}
}

// Latency and workload are first-class filter fields — the two most common
// debugging pivots ("what's slow?", "which service?").
func TestCompileFilterLatencyAndWorkload(t *testing.T) {
	e := sample()
	e.ElapsedMs = 750
	e.Source.Workload = "frontend"
	e.Destination.Workload = "payment"
	e.L4 = &api.L4Info{DurationMs: 1200}
	cases := []struct {
		expr string
		want bool
	}{
		{`elapsedMs > 500`, true},
		{`elapsedms > 500`, true}, // case-insensitive
		{`latency > 500`, true},   // alias
		{`elapsedMs > 800`, false},
		{`elapsedMs <= 750`, true},
		{`src.workload == "frontend"`, true},
		{`dst.workload == "payment"`, true},
		{`dst.workload == "checkout"`, false},
		{`l4.durationms >= 1000`, true},
	}
	for _, c := range cases {
		pred, err := CompileFilter(c.expr)
		if err != nil {
			t.Errorf("CompileFilter(%q) error: %v", c.expr, err)
			continue
		}
		if got := pred(e); got != c.want {
			t.Errorf("filter %q = %v, want %v", c.expr, got, c.want)
		}
	}

	// l4.durationms must not match when L4 is absent.
	pred, _ := CompileFilter(`l4.durationms == 0`)
	if pred(sample()) {
		t.Error("l4.durationms == 0 matched an entry with no L4 (want false)")
	}
}

// An unknown field in a comparison must be a compile error, not a silent
// match-nothing — a typo would otherwise read as "no matching traffic".
func TestCompileFilterUnknownField(t *testing.T) {
	for _, expr := range []string{
		`http.status_code == 500`, // typo of response.status
		`namespcae == "shop"`,
		`elapsed_ms > 100`,
	} {
		if _, err := CompileFilter(expr); err == nil {
			t.Errorf("expected unknown-field error for %q, got nil", expr)
		}
	}
	// Bare tokens (full-text) still compile — only `field op value` is strict.
	if _, err := CompileFilter("checkout"); err != nil {
		t.Errorf("bare token should compile, got %v", err)
	}
}

// Every catalog entry must resolve through fieldGetter, so the /api/fields
// autocomplete never advertises a field the filter would then reject.
func TestFieldCatalogMatchesGetter(t *testing.T) {
	for _, spec := range fieldCatalog {
		if fieldGetter(spec.Name) == nil {
			t.Errorf("catalog field %q has no fieldGetter case", spec.Name)
		}
	}
}

// TestCompileFilterDoS guards the unauthenticated ?filter= surface: a
// pathologically nested or oversized expression must return an error, never a
// panic/stack-overflow crash.
func TestCompileFilterDoS(t *testing.T) {
	// Thousands of leading '(' — would recurse into a stack overflow without the
	// depth guard.
	if _, err := CompileFilter(strings.Repeat("(", 5000)); err == nil {
		t.Error("5000 '(' should error, got nil")
	}
	// A long "not not not ..." chain — same deep-recursion risk via parseUnary.
	if _, err := CompileFilter(strings.Repeat("not ", 5000) + "checkout"); err == nil {
		t.Error("long 'not' chain should error, got nil")
	}
	// An oversized (but well-formed-looking) input is rejected before parsing.
	if _, err := CompileFilter(strings.Repeat("a", maxFilterLen+1)); err == nil {
		t.Error("oversized filter should error, got nil")
	}

	// A normal, moderately-nested filter still compiles and evaluates correctly.
	pred, err := CompileFilter(`not (protocol == "dns" or (dst.namespace == "shop" and http.method == "POST"))`)
	if err != nil {
		t.Fatalf("moderately-nested filter errored: %v", err)
	}
	if pred(sample()) {
		t.Error("expected the sample (http POST in shop) to be excluded by the negation")
	}
}

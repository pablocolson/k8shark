package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A fully-populated Entry must round-trip and still carry the canonical scalar
// fields the IFL filter reads.
func TestEntryRoundTripFull(t *testing.T) {
	in := &Entry{
		ID:          "n-1",
		Protocol:    ProtocolHTTP,
		Timestamp:   time.Unix(1_700_000_000, 0).UTC(),
		Node:        "node-1",
		StatusCode:  200,
		Status:      "success",
		Source:      Endpoint{IP: "10.0.0.1", Port: 5000, Name: "a"},
		Destination: Endpoint{IP: "10.0.0.2", Port: 80, Name: "b"},
		Request: Payload{
			Method: "GET", Path: "/x", Host: "b", ContentType: "text/plain",
			HTTP:     &HTTPDetail{Version: "HTTP/1.1", Query: map[string]string{"q": "1"}},
			Redis:    &RedisDetail{Args: []string{"GET", "k"}, DBIndex: 2},
			Postgres: &PGDetail{StatementName: "s1", Params: []string{"42"}},
			DNS:      &DNSDetail{Questions: []DNSQuestion{{Name: "x", Type: "A"}}},
			Raw:      &RawView{Hex: "0000  ...", Bytes: 4, Truncated: true},
		},
		Response: Payload{
			StatusCode: 200, Body: "hi", ContentType: "application/json",
			Postgres: &PGDetail{Columns: []PGColumn{{Name: "id", TypeOID: 23, Type: "int4"}}, Tag: "SELECT 1"},
		},
		L4: &L4Info{
			SrcMAC: "02:00:00:00:00:01", IPVersion: 4, TTL: 64, MSS: 1460,
			ClientTCPFlags: "SYN,ACK,FIN", ClientBytes: 100, ServerBytes: 200,
			TLS: &TLSInfo{SNI: "example.com"},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Entry
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Request.Method != "GET" || out.Request.Path != "/x" {
		t.Errorf("canonical scalars lost: %+v", out.Request)
	}
	if out.Request.HTTP == nil || out.Request.HTTP.Version != "HTTP/1.1" {
		t.Errorf("HTTP sub-object lost: %+v", out.Request.HTTP)
	}
	if out.L4 == nil || out.L4.TLS == nil || out.L4.TLS.SNI != "example.com" {
		t.Errorf("L4/TLS sub-object lost: %+v", out.L4)
	}
	if out.Response.Postgres == nil || len(out.Response.Postgres.Columns) != 1 {
		t.Errorf("PG columns lost: %+v", out.Response.Postgres)
	}
}

// An Entry with no sub-objects must not emit any of the new keys (omitempty),
// so an old front-end sees exactly the pre-change JSON shape.
func TestEntryRoundTripAdditive(t *testing.T) {
	in := &Entry{
		ID:       "n-2",
		Protocol: ProtocolTCP,
		Status:   "success",
		Request:  Payload{Summary: "tcp"},
		Response: Payload{Summary: "idle"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, key := range []string{`"l4"`, `"raw"`, `"http"`, `"dns"`, `"redis"`, `"postgres"`, `"contentType"`, `"exchange"`} {
		if strings.Contains(js, key) {
			t.Errorf("nil sub-object leaked key %s into JSON: %s", key, js)
		}
	}
}

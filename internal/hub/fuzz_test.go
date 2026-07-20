package hub

// TST-3: CompileFilter parses attacker-influenced input — every /api/*?filter=
// and every front-end filter frame flows through it. It is depth- and
// length-bounded, but never fuzzed. This target asserts two things across
// arbitrary input: compilation never panics, and a successfully compiled
// predicate never panics when evaluated against a fully-populated entry. The
// seed corpus (valid and deliberately malformed expressions) runs on every
// `go test`; `go test -fuzz` (see `make fuzz`) explores further.

import (
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

func FuzzCompileFilter(f *testing.F) {
	seeds := []string{
		"",
		`protocol == "http"`,
		`elapsedMs > 100 and dst.namespace == "shop"`,
		`not (response.status >= 500 or protocol == "redis")`,
		`request.path matches "^/api/v[0-9]+/"`,
		`dst.namespace in ("prod", "staging")`,
		`request.host startswith "api."`,
		`request.header.user-agent contains "curl"`,
		"free text match",
		`unbalanced ( paren`,
		`protocol ==`,
		`nosuchfield == "x"`,
		`((((((((((a))))))))))`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// A representative entry touching the fields the getters read, so an
	// evaluated predicate exercises real extraction rather than nil access.
	entry := &api.Entry{
		ID:          "e1",
		Protocol:    api.ProtocolHTTP,
		Timestamp:   time.Unix(1_700_000_000, 0),
		ElapsedMs:   123,
		Node:        "node-a",
		Source:      api.Endpoint{IP: "10.0.0.1", Port: 40000, Name: "client", Namespace: "shop", Workload: "web"},
		Destination: api.Endpoint{IP: "10.0.0.2", Port: 80, Name: "api-0", Namespace: "prod", Workload: "api"},
		Request: api.Payload{
			Method: "GET", Path: "/api/v1/x", Host: "api.example.com",
			Headers: map[string]string{"user-agent": "curl/8", "content-type": "application/json"},
			Summary: "GET /api/v1/x",
		},
		Response:   api.Payload{StatusCode: 200, Summary: "200 OK"},
		Status:     "success",
		StatusCode: 200,
	}

	f.Fuzz(func(t *testing.T, expr string) {
		pred, err := CompileFilter(expr)
		if err != nil {
			return // a rejected filter is a valid outcome; it must just not panic
		}
		// Empty/whitespace expressions compile to a nil (match-all) predicate;
		// only evaluate a real one.
		if pred != nil {
			_ = pred(entry)
		}
	})
}

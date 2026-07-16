package worker

import (
	"net/http"
	"testing"
)

// Redaction scrubs credential-bearing header values while keeping the keys
// (so their presence stays observable) and leaving everything else intact.
func TestFlattenHeadersRedaction(t *testing.T) {
	h := http.Header{
		"Authorization": {"Bearer abc123"},
		"Cookie":        {"session=deadbeef"},
		"X-Api-Key":     {"k-42"},
		"Content-Type":  {"application/json"},
	}

	p := newPipeline(newSink("", "", "n", discardLogger()), "n", discardLogger())
	p.redactHeaders = true
	out := p.flattenHeaders(h)
	for _, k := range []string{"authorization", "cookie", "x-api-key"} {
		if out[k] != redactedValue {
			t.Errorf("%s = %q, want %q", k, out[k], redactedValue)
		}
	}
	if out["content-type"] != "application/json" {
		t.Errorf("content-type = %q, want the original value", out["content-type"])
	}

	// Redaction off: values pass through untouched.
	p.redactHeaders = false
	out = p.flattenHeaders(h)
	if out["authorization"] != "Bearer abc123" {
		t.Errorf("with redaction off, authorization = %q, want the original", out["authorization"])
	}
}

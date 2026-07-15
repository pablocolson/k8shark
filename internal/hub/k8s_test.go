package hub

import (
	"net/http"
	"testing"

	"github.com/pablocolson/k8shark/pkg/api"
)

// TestResolverEnrich fills endpoint Name/Namespace from the IP map, skips
// unknown IPs, and never overwrites an already-set Name.
func TestResolverEnrich(t *testing.T) {
	r := &resolver{
		client: &http.Client{}, // non-nil => enabled()
		byIP: map[string]ref{
			"10.0.0.1": {name: "web-abc", namespace: "shop"},
			"10.0.0.2": {name: "pg-0", namespace: "db"},
		},
	}

	e := &api.Entry{
		Source:      api.Endpoint{IP: "10.0.0.1", Port: 5000},
		Destination: api.Endpoint{IP: "10.0.0.2", Port: 5432},
	}
	r.enrich(e)
	if e.Source.Name != "web-abc" || e.Source.Namespace != "shop" {
		t.Errorf("source = %q/%q, want web-abc/shop", e.Source.Name, e.Source.Namespace)
	}
	if e.Destination.Name != "pg-0" || e.Destination.Namespace != "db" {
		t.Errorf("dest = %q/%q, want pg-0/db", e.Destination.Name, e.Destination.Namespace)
	}

	// Unknown IP stays bare; an already-named endpoint is left untouched.
	e2 := &api.Entry{
		Source:      api.Endpoint{IP: "10.9.9.9"},
		Destination: api.Endpoint{IP: "10.0.0.1", Name: "keep"},
	}
	r.enrich(e2)
	if e2.Source.Name != "" {
		t.Errorf("unknown source got name %q, want empty", e2.Source.Name)
	}
	if e2.Destination.Name != "keep" {
		t.Errorf("named dest overwritten to %q, want keep", e2.Destination.Name)
	}
}

// TestResolverDisabledNoop: an off-cluster resolver (nil client) enriches
// nothing, even with a populated map.
func TestResolverDisabledNoop(t *testing.T) {
	r := &resolver{byIP: map[string]ref{"10.0.0.1": {name: "x"}}}
	e := &api.Entry{Source: api.Endpoint{IP: "10.0.0.1"}}
	r.enrich(e)
	if e.Source.Name != "" {
		t.Errorf("disabled resolver enriched %q, want no-op", e.Source.Name)
	}
}

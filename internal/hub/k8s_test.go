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

// The workload label (stable across pod restarts) is filled alongside the pod
// name, including on endpoints whose Name was already set by the worker.
func TestResolverEnrichWorkload(t *testing.T) {
	r := &resolver{
		client: &http.Client{},
		byIP: map[string]ref{
			"10.0.0.1": {name: "web-7d9f4b-x2x4v", namespace: "shop", workload: "web"},
		},
	}
	e := &api.Entry{Source: api.Endpoint{IP: "10.0.0.1"}, Destination: api.Endpoint{IP: "10.0.0.1", Name: "keep"}}
	r.enrich(e)
	if e.Source.Workload != "web" {
		t.Errorf("source workload = %q, want web", e.Source.Workload)
	}
	// Name already set (worker-supplied) still gains the workload.
	if e.Destination.Name != "keep" || e.Destination.Workload != "web" {
		t.Errorf("dest = %q/%q, want keep/web", e.Destination.Name, e.Destination.Workload)
	}
}

// workloadOf resolves pod owners: ReplicaSet -> Deployment (via the RS map,
// or by stripping the pod-template hash when the map is missing), direct
// controllers by name, bare pods to themselves.
func TestWorkloadOf(t *testing.T) {
	owner := func(kind, name string) objMeta {
		m := objMeta{Name: "pod-x", Namespace: "shop"}
		m.OwnerReferences = append(m.OwnerReferences, struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}{kind, name})
		return m
	}
	rsOwners := map[string]string{"shop/web-7d9f4b": "web"}

	if got := workloadOf(owner("ReplicaSet", "web-7d9f4b"), rsOwners); got != "web" {
		t.Errorf("RS via map = %q, want web", got)
	}
	if got := workloadOf(owner("ReplicaSet", "api-5c9d8e"), nil); got != "api" {
		t.Errorf("RS via hash-strip = %q, want api", got)
	}
	if got := workloadOf(owner("StatefulSet", "pg"), nil); got != "pg" {
		t.Errorf("StatefulSet = %q, want pg", got)
	}
	if got := workloadOf(owner("DaemonSet", "fluentd"), nil); got != "fluentd" {
		t.Errorf("DaemonSet = %q, want fluentd", got)
	}
	if got := workloadOf(objMeta{Name: "one-off", Namespace: "shop"}, nil); got != "one-off" {
		t.Errorf("bare pod = %q, want one-off", got)
	}
}

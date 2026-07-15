package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

const (
	saTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	saCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	// enrichInterval is how often the IP -> identity map is rebuilt.
	enrichInterval = 20 * time.Second
)

// ref is the resolved k8s identity behind an IP (a pod or a service).
type ref struct {
	name      string
	namespace string
}

// resolver maps pod/service IPs to their k8s identity by periodically listing
// the in-cluster API, so captured endpoints show a workload name instead of a
// bare IP. It is dependency-free (no client-go): it reads the mounted
// service-account token/CA and polls the REST API (the same hand-rolled style
// as internal/mcp). When not running in a cluster (no token) it is a no-op, so
// the hub still runs in local dev — endpoints simply keep their ip:port.
type resolver struct {
	log    *slog.Logger
	api    string
	token  string
	client *http.Client

	mu   sync.RWMutex
	byIP map[string]ref
}

func newResolver(log *slog.Logger) *resolver {
	r := &resolver{log: log, byIP: map[string]ref{}}
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	token, err := os.ReadFile(saTokenPath)
	if host == "" || err != nil {
		return r // not in-cluster: enrichment stays disabled
	}
	if port == "" {
		port = "443"
	}
	pool := x509.NewCertPool()
	if ca, caErr := os.ReadFile(saCAPath); caErr == nil {
		pool.AppendCertsFromPEM(ca)
	}
	r.api = "https://" + net.JoinHostPort(host, port)
	r.token = string(token)
	r.client = &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	return r
}

func (r *resolver) enabled() bool { return r.client != nil }

// run rebuilds the IP map every enrichInterval until ctx is cancelled. A no-op
// when the resolver is disabled (not in a cluster).
func (r *resolver) run(ctx context.Context) {
	if !r.enabled() {
		r.log.Info("k8s endpoint enrichment disabled (not running in-cluster)")
		return
	}
	r.log.Info("k8s endpoint enrichment enabled")
	r.refresh(ctx)
	t := time.NewTicker(enrichInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.refresh(ctx)
		}
	}
}

// refresh rebuilds byIP from a pods list plus a services list. On a failed or
// empty refresh it keeps the previous map rather than blanking enrichment.
func (r *resolver) refresh(ctx context.Context) {
	m := r.listPods(ctx)
	if m == nil {
		return // request failed; keep the previous map
	}
	// Services fill ClusterIP gaps; pods win on any overlap (disjoint in practice).
	for ip, rf := range r.listServices(ctx) {
		if _, ok := m[ip]; !ok {
			m[ip] = rf
		}
	}
	r.mu.Lock()
	r.byIP = m
	r.mu.Unlock()
	r.log.Debug("k8s enrichment refreshed", "endpoints", len(m))
}

// get performs an authenticated GET and decodes the JSON body into out.
func (r *resolver) get(ctx context.Context, path string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.api+path, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		r.log.Warn("k8s enrichment request failed", "path", path, "err", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.log.Warn("k8s enrichment non-200", "path", path, "status", resp.StatusCode)
		return false
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		r.log.Warn("k8s enrichment decode failed", "path", path, "err", err)
		return false
	}
	return true
}

func (r *resolver) listPods(ctx context.Context) map[string]ref {
	var list struct {
		Items []struct {
			Metadata struct{ Name, Namespace string } `json:"metadata"`
			Spec     struct {
				HostNetwork bool `json:"hostNetwork"`
			} `json:"spec"`
			Status struct {
				PodIP  string `json:"podIP"`
				PodIPs []struct {
					IP string `json:"ip"`
				} `json:"podIPs"`
			} `json:"status"`
		} `json:"items"`
	}
	if !r.get(ctx, "/api/v1/pods", &list) {
		return nil
	}
	m := make(map[string]ref, len(list.Items))
	for _, p := range list.Items {
		if p.Spec.HostNetwork {
			continue // shares the node IP — ambiguous, skip
		}
		rf := ref{name: p.Metadata.Name, namespace: p.Metadata.Namespace}
		if p.Status.PodIP != "" {
			m[p.Status.PodIP] = rf
		}
		for _, ip := range p.Status.PodIPs {
			if ip.IP != "" {
				m[ip.IP] = rf
			}
		}
	}
	return m
}

func (r *resolver) listServices(ctx context.Context) map[string]ref {
	var list struct {
		Items []struct {
			Metadata struct{ Name, Namespace string } `json:"metadata"`
			Spec     struct {
				ClusterIP  string   `json:"clusterIP"`
				ClusterIPs []string `json:"clusterIPs"`
			} `json:"spec"`
		} `json:"items"`
	}
	if !r.get(ctx, "/api/v1/services", &list) {
		return nil
	}
	m := make(map[string]ref, len(list.Items))
	for _, s := range list.Items {
		rf := ref{name: s.Metadata.Name, namespace: s.Metadata.Namespace}
		for _, ip := range append(s.Spec.ClusterIPs, s.Spec.ClusterIP) {
			if ip != "" && ip != "None" {
				m[ip] = rf
			}
		}
	}
	return m
}

// enrich fills Name/Namespace on the entry's endpoints from the IP map, when
// known and not already set. Safe to call when disabled (no-op).
func (r *resolver) enrich(e *api.Entry) {
	if e == nil || !r.enabled() {
		return
	}
	r.enrichEndpoint(&e.Source)
	r.enrichEndpoint(&e.Destination)
}

func (r *resolver) enrichEndpoint(ep *api.Endpoint) {
	if ep.Name != "" || ep.IP == "" {
		return
	}
	r.mu.RLock()
	rf, ok := r.byIP[ep.IP]
	r.mu.RUnlock()
	if ok {
		ep.Name, ep.Namespace = rf.name, rf.namespace
	}
}

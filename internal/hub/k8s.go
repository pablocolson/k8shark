package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	// workload is the owning controller (Deployment/StatefulSet/...): a stable
	// label where name churns with every pod restart. For services it is the
	// service name itself.
	workload string
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
	m := r.listPods(ctx, r.listReplicaSetOwners(ctx))
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

// listPageSize bounds each list request so a big cluster is fetched in pages
// instead of one giant response.
const listPageSize = 500

// objMeta is the metadata subset the resolver reads off any listed object.
type objMeta struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	OwnerReferences []struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"ownerReferences"`
}

// listAll GETs a paginated list endpoint, following the continue token, and
// returns every item. ok is false when any page failed (callers then keep
// their previous state rather than working from a partial list).
func listAll[T any](ctx context.Context, r *resolver, path string) (items []T, ok bool) {
	var page struct {
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
		Items []T `json:"items"`
	}
	cont := ""
	for {
		u := path + "?limit=" + strconv.Itoa(listPageSize)
		if cont != "" {
			u += "&continue=" + url.QueryEscape(cont)
		}
		page.Metadata.Continue = ""
		page.Items = nil
		if !r.get(ctx, u, &page) {
			return nil, false
		}
		items = append(items, page.Items...)
		if page.Metadata.Continue == "" {
			return items, true
		}
		cont = page.Metadata.Continue
	}
}

// listReplicaSetOwners maps "namespace/replicaset-name" to the owning
// Deployment's name, so pods resolve to the stable Deployment rather than a
// hash-suffixed ReplicaSet. Best effort: on failure workloadOf falls back to
// stripping the pod-template hash off the ReplicaSet name.
func (r *resolver) listReplicaSetOwners(ctx context.Context) map[string]string {
	type rs struct {
		Metadata objMeta `json:"metadata"`
	}
	items, ok := listAll[rs](ctx, r, "/apis/apps/v1/replicasets")
	if !ok {
		return nil
	}
	m := make(map[string]string, len(items))
	for _, it := range items {
		for _, o := range it.Metadata.OwnerReferences {
			if o.Kind == "Deployment" {
				m[it.Metadata.Namespace+"/"+it.Metadata.Name] = o.Name
				break
			}
		}
	}
	return m
}

// workloadOf resolves a pod's owning controller name. rsOwners may be nil.
func workloadOf(meta objMeta, rsOwners map[string]string) string {
	for _, o := range meta.OwnerReferences {
		switch o.Kind {
		case "ReplicaSet":
			if d, ok := rsOwners[meta.Namespace+"/"+o.Name]; ok {
				return d
			}
			// ReplicaSet names are "<deployment>-<pod-template-hash>"; stripping
			// the last segment recovers the Deployment when the RS list failed.
			if i := strings.LastIndexByte(o.Name, '-'); i > 0 {
				return o.Name[:i]
			}
			return o.Name
		case "StatefulSet", "DaemonSet", "Job":
			return o.Name
		}
	}
	return meta.Name // bare pod: the pod is its own workload
}

func (r *resolver) listPods(ctx context.Context, rsOwners map[string]string) map[string]ref {
	type pod struct {
		Metadata objMeta `json:"metadata"`
		Spec     struct {
			HostNetwork bool `json:"hostNetwork"`
		} `json:"spec"`
		Status struct {
			PodIP  string `json:"podIP"`
			PodIPs []struct {
				IP string `json:"ip"`
			} `json:"podIPs"`
		} `json:"status"`
	}
	items, ok := listAll[pod](ctx, r, "/api/v1/pods")
	if !ok {
		return nil
	}
	m := make(map[string]ref, len(items))
	for _, p := range items {
		if p.Spec.HostNetwork {
			continue // shares the node IP — ambiguous, skip
		}
		rf := ref{
			name:      p.Metadata.Name,
			namespace: p.Metadata.Namespace,
			workload:  workloadOf(p.Metadata, rsOwners),
		}
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
	type svc struct {
		Metadata objMeta `json:"metadata"`
		Spec     struct {
			ClusterIP  string   `json:"clusterIP"`
			ClusterIPs []string `json:"clusterIPs"`
		} `json:"spec"`
	}
	items, ok := listAll[svc](ctx, r, "/api/v1/services")
	if !ok {
		return nil
	}
	m := make(map[string]ref, len(items))
	for _, s := range items {
		rf := ref{name: s.Metadata.Name, namespace: s.Metadata.Namespace, workload: s.Metadata.Name}
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
	if ep.IP == "" {
		return
	}
	r.mu.RLock()
	rf, ok := r.byIP[ep.IP]
	r.mu.RUnlock()
	if !ok {
		return
	}
	if ep.Name == "" {
		ep.Name, ep.Namespace = rf.name, rf.namespace
	}
	if ep.Workload == "" {
		ep.Workload = rf.workload
	}
}

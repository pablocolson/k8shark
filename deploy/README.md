# k8shark — all-in-one Kubernetes deploy

This directory holds a single-file, no-Helm deployment of k8shark
(`deploy/k8shark.yaml`), rendered from the canonical chart at
`helm/k8shark`. Use it when you want a plain `kubectl apply` workflow
instead of Helm. It is hardened for a **Talos Linux + Cilium** cluster.

`deploy/k8shark.yaml` contains two literal placeholder tokens that are
**not** resolved by any live templating engine:

- `__REGISTRY__` — the container registry/repo prefix
- `__TAG__` — the image tag

You must substitute both before applying, via one of the two paths below.

**Images are built by CI** (`.github/workflows/ci.yml`): every push to `main`
and every tag pushes `ghcr.io/pablocolson/k8shark` and
`ghcr.io/pablocolson/k8shark-front` tagged `latest`, the commit short-SHA, and
(on a tag) the tag itself — no manual `docker build`/`docker push` needed for
a normal deploy. `make docker-build`/`docker-push` still exist for local
iteration.

## Path A — kustomize

1. Edit `deploy/kustomization.yaml` and set `images[].newName` /
   `images[].newTag` to your registry and tag (defaults point at
   `ghcr.io/pablocolson/k8shark:v0.1.0` and
   `ghcr.io/pablocolson/k8shark-front:v0.1.0`).
2. Apply:

   ```sh
   kubectl apply -k deploy/
   ```

## Path B — sed

Substitute the placeholders yourself and pipe straight into `kubectl apply`:

```sh
sed 's|__REGISTRY__|<registry>|g; s|__TAG__|<tag>|g' deploy/k8shark.yaml | kubectl apply -f -
```

Replace `<registry>` and `<tag>` with your actual registry/repo prefix and
image tag (e.g. `ghcr.io/pablocolson`
and `v0.1.0`).

## Private registry / imagePullSecrets

If `__REGISTRY__` points at a private registry, create the pull secret
out-of-band (it is intentionally not part of this manifest):

```sh
kubectl -n k8shark create secret docker-registry k8shark-registry \
  --docker-server=<host> \
  --docker-username=<user> \
  --docker-password=<token>
```

Then reference it from each workload's pod spec:

```yaml
imagePullSecrets:
  - name: k8shark-registry
```

Add this either to the `hub`, `worker`, and `front` pod specs directly, or
once to each of the `k8shark-hub` / `k8shark-worker` / `k8shark-front`
ServiceAccounts (`imagePullSecrets` on a ServiceAccount is picked up
automatically by any pod using it).

(When installing via the Helm chart instead of these manifests, set the
chart's top-level `imagePullSecrets` value — it wires the reference into all
three pod specs for you.)

## Verification checklist

1. Pods are up, and the worker DaemonSet has one pod per node — including
   Talos control-plane nodes, since its `tolerations` is `operator: Exists`:

   ```sh
   kubectl -n k8shark get pods -o wide
   ```

2. Hub logs look healthy:

   ```sh
   kubectl -n k8shark logs -l app.kubernetes.io/name=k8shark-hub
   ```

3. Port-forward the front end and open it in a browser:

   ```sh
   kubectl -n k8shark port-forward svc/k8shark-front 8899:80
   ```

   then open <http://localhost:8899>.

## Note on the worker's eBPF mounts/capabilities

The worker DaemonSet's `BPF`/`PERFMON`/`SYS_ADMIN`/etc. capabilities and its
`/sys/fs/bpf`, debugfs, and hostproc mounts back the eBPF TLS uprobe capture
layer (OpenSSL/boringssl `SSL_read`/`SSL_write` uprobes, see
`internal/worker/ebpf/`). They are only rendered/applied when
`worker.tls.enabled=true` (Helm) — the default install stays on AF_PACKET
only and does not request them.

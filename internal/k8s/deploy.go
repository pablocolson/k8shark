// Package k8s drives cluster operations by shelling out to helm and kubectl,
// which keeps the binary light and reuses whatever kubeconfig/context the user
// already has. The Helm chart is extracted from the embedded FS at runtime.
package k8s

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pablocolson/k8shark/helm"
)

// extractChart writes the embedded chart to a temp dir and returns its path and
// a cleanup func.
func extractChart() (string, func(), error) {
	tmp, err := os.MkdirTemp("", "k8shark-chart-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	err = fs.WalkDir(helm.Chart, "k8shark", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := helm.Chart.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	return filepath.Join(tmp, "k8shark"), cleanup, nil
}

// namespaceManifest renders the target namespace with the privileged Pod
// Security Admission labels. A privileged, hostNetwork worker DaemonSet is
// rejected on a PSA-enforcing cluster (e.g. Talos, which enforces "baseline"/
// "restricted" by default) unless its namespace opts into the privileged
// profile. The labels match deploy/k8shark.yaml (the static manifest) exactly.
func namespaceManifest(namespace string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    app.kubernetes.io/part-of: k8shark
    pod-security.kubernetes.io/enforce: privileged
    pod-security.kubernetes.io/enforce-version: latest
    pod-security.kubernetes.io/warn: privileged
    pod-security.kubernetes.io/audit: privileged
`, namespace)
}

// ensureNamespace creates (or updates) the target namespace with the PSA labels
// before Helm runs. We do this with `kubectl apply` rather than letting Helm own
// the namespace because a chart cannot own the very namespace it installs into
// (Helm writes its release record there first) — and because the labels must be
// present *before* the worker pods are admitted.
func ensureNamespace(ctx context.Context, log *slog.Logger, namespace string) error {
	log.Info("ensuring namespace with privileged PSA labels", "namespace", namespace)
	return runStdin(ctx, log, namespaceManifest(namespace), "kubectl", "apply", "-f", "-")
}

// Install runs `helm upgrade --install` for the embedded chart, after ensuring
// the namespace exists with the privileged PSA labels.
func Install(ctx context.Context, log *slog.Logger, release, namespace string, sets []string) error {
	if err := ensureNamespace(ctx, log, namespace); err != nil {
		return fmt.Errorf("ensure namespace: %w", err)
	}

	chartDir, cleanup, err := extractChart()
	if err != nil {
		return fmt.Errorf("extract chart: %w", err)
	}
	defer cleanup()

	// No --create-namespace: ensureNamespace already created it with the PSA
	// labels (Helm's --create-namespace makes an unlabelled namespace).
	args := []string{
		"upgrade", "--install", release, chartDir,
		"--namespace", namespace,
		"--wait", "--timeout", "3m",
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	log.Info("installing chart", "release", release, "namespace", namespace)
	return run(ctx, log, "helm", args...)
}

// Uninstall removes the release, then the namespace — unless keepNamespace is
// set or the namespace holds resources k8shark didn't create. ensureNamespace
// (see Install) applies k8shark's PSA labels with `kubectl apply` even to a
// namespace that already existed, so the presence of those labels alone can't
// tell "k8shark's own namespace" apart from "a shared namespace k8shark was
// installed into" — `tap -n monitoring` must not delete `monitoring` and
// everything else running in it. Checking for foreign resources instead is a
// real (if best-effort) safety net for that case.
func Uninstall(ctx context.Context, log *slog.Logger, release, namespace string, keepNamespace bool) error {
	_ = run(ctx, log, "helm", "uninstall", release, "--namespace", namespace)
	if keepNamespace {
		log.Info("keeping namespace (--keep-namespace)", "namespace", namespace)
		return nil
	}
	foreign, err := namespaceHasForeignResources(ctx, namespace)
	if err != nil {
		log.Warn("could not verify the namespace is safe to delete, leaving it in place",
			"namespace", namespace, "err", err,
			"hint", "delete it yourself with kubectl if that's safe, or re-run with --keep-namespace to silence this")
		return nil
	}
	if foreign {
		log.Warn("namespace has resources k8shark did not create, leaving it in place",
			"namespace", namespace,
			"hint", "remove the unrelated resources and re-run clean, or pass --keep-namespace to always skip this")
		return nil
	}
	return run(ctx, log, "kubectl", "delete", "namespace", namespace, "--ignore-not-found")
}

// namespaceHasForeignResources reports whether namespace contains any of the
// common workload/networking kinds (`kubectl get all`) NOT carrying
// k8shark's app.kubernetes.io/part-of label — i.e. something k8shark didn't
// put there itself. This is best-effort, not exhaustive (bare ConfigMaps,
// Secrets and PVCs from another tool would slip through), so it's a safety
// net layered on top of --keep-namespace, not a substitute for it. A
// not-found namespace (already gone) is reported as having nothing foreign,
// since there's nothing left to protect.
func namespaceHasForeignResources(ctx context.Context, namespace string) (bool, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "all",
		"--namespace", namespace,
		"-l", "app.kubernetes.io/part-of!=k8shark",
		"-o", "name")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "NotFound") {
			return false, nil
		}
		return false, fmt.Errorf("kubectl get all -n %s: %w: %s", namespace, err, strings.TrimSpace(string(out)))
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// PortForward starts `kubectl port-forward` for a service. The returned command
// is already started; call Wait or Process.Kill to stop it.
func PortForward(ctx context.Context, namespace, svc string, local, remote int) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "port-forward",
		"--namespace", namespace,
		"svc/"+svc, fmt.Sprintf("%d:%d", local, remote))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd, cmd.Start()
}

// Logs streams logs from a component (by app.kubernetes.io/name label).
func Logs(ctx context.Context, namespace, component string, follow bool) error {
	args := []string{"logs", "--namespace", namespace,
		"-l", "app.kubernetes.io/name=k8shark-" + component,
		"--all-containers", "--max-log-requests", "20", "--tail", "100"}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// OpenBrowser best-effort opens url in the default browser.
func OpenBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, url)...).Start()
}

func run(ctx context.Context, log *slog.Logger, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

// runStdin runs a command feeding stdin from a string (used to `kubectl apply -f -`).
func runStdin(ctx context.Context, log *slog.Logger, stdin, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Stdin = strings.NewReader(stdin)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

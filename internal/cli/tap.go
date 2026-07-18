package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/internal/k8s"
	"github.com/spf13/cobra"
)

func tapCmd() *cobra.Command {
	var (
		namespace string
		release   string
		frontPort int
		demo      bool
		noBrowser bool
		noAuth    bool
		sets      []string
	)
	cmd := &cobra.Command{
		Use:   "tap",
		Short: "Deploy k8shark to the current cluster and open the dashboard",
		Long: "Installs k8shark (hub + worker DaemonSet + front) into the cluster,\n" +
			"port-forwards the dashboard to localhost and opens it in your browser.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if demo {
				sets = append(sets, "worker.demo=true")
			}

			// Without hub.apiToken, anyone who can reach the hub Service reads
			// every captured request/response cluster-wide. Generate one
			// unless the caller opted out or already set it explicitly.
			token := ""
			if !noAuth && !hasSetValue(sets, "hub.apiToken") {
				var err error
				token, err = generateAPIToken()
				if err != nil {
					return fmt.Errorf("generate API token: %w", err)
				}
				sets = append(sets, "hub.apiToken="+token)
			}

			if err := k8s.Install(ctx, log, release, namespace, sets); err != nil {
				return err
			}

			pf, err := k8s.PortForward(ctx, namespace, "k8shark-front", frontPort, config.DefaultFrontPort)
			if err != nil {
				return fmt.Errorf("port-forward: %w", err)
			}
			url := fmt.Sprintf("http://localhost:%d", frontPort)
			log.Info("dashboard ready", "url", url)
			fmt.Printf("\n  k8shark is running.\n  Dashboard: %s\n", url)
			if token != "" {
				fmt.Printf("  API token (the dashboard above works without it; needed for direct /api, /ws or MCP access): %s\n", token)
				fmt.Printf("  Retrieve it again later with: kubectl -n %s get secret k8shark-api-token -o jsonpath='{.data.token}' | base64 -d\n", namespace)
			}
			fmt.Printf("  Press Ctrl-C to stop the port-forward (the install stays up; use `k8shark clean` to remove).\n\n")
			if !noBrowser {
				k8s.OpenBrowser(url)
			}

			<-ctx.Done()
			_ = pf.Process.Kill()
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", config.DefaultNamespace, "namespace to install into")
	cmd.Flags().StringVar(&release, "release", "k8shark", "helm release name")
	cmd.Flags().IntVar(&frontPort, "port", 8899, "local port for the dashboard")
	cmd.Flags().BoolVar(&demo, "demo", false, "run workers in demo mode (synthetic traffic)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser")
	cmd.Flags().BoolVar(&noAuth, "no-auth", false, "skip generating an API token, leaving the hub API/WebSocket unauthenticated (not recommended)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "extra helm --set values (repeatable)")
	return cmd
}

// hasSetValue reports whether sets already assigns key (e.g. "hub.apiToken"),
// so tap doesn't override a value the caller passed explicitly via --set.
func hasSetValue(sets []string, key string) bool {
	prefix := key + "="
	for _, s := range sets {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// generateAPIToken returns a random hex-encoded bearer token for hub.apiToken.
func generateAPIToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

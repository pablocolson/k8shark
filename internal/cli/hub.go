package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/internal/hub"
	"github.com/spf13/cobra"
)

func hubCmd() *cobra.Command {
	var port int
	var uiDir string
	var apiToken string
	var workerToken string
	var adminToken string
	var bufferSize int
	var allowOrigins []string
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Run the hub server (aggregates worker traffic, serves the API)",
		Long: "Runs the central hub that receives entries from workers and streams\n" +
			"them to front-end clients. This is what the hub container runs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiToken == "" {
				apiToken = os.Getenv("K8SHARK_API_TOKEN")
			}
			if workerToken == "" {
				workerToken = os.Getenv("K8SHARK_WORKER_TOKEN")
			}
			if adminToken == "" {
				adminToken = os.Getenv("K8SHARK_ADMIN_TOKEN")
			}
			s := hub.New(log, hub.Options{
				UIDir:          uiDir,
				APIToken:       apiToken,
				WorkerToken:    workerToken,
				AdminToken:     adminToken,
				BufferSize:     bufferSize,
				AllowedOrigins: allowOrigins,
			})
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return s.Run(ctx, fmt.Sprintf(":%d", port))
		},
	}
	cmd.Flags().IntVar(&port, "port", config.DefaultHubPort, "listen port")
	cmd.Flags().StringVar(&uiDir, "serve-ui", "", "serve a built front from this directory (local dev)")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "require this bearer token on /api and WebSocket endpoints (default $K8SHARK_API_TOKEN; empty disables auth)")
	cmd.Flags().StringVar(&workerToken, "worker-token", "", "distinct bearer token required on /ws/worker (default $K8SHARK_WORKER_TOKEN; empty falls back to the API token)")
	cmd.Flags().StringVar(&adminToken, "admin-token", "", "distinct bearer token required on mutating /api calls, also grants reads (default $K8SHARK_ADMIN_TOKEN; empty falls back to the API token)")
	cmd.Flags().IntVar(&bufferSize, "buffer", 0, "in-memory entry buffer size (0 = default 10000)")
	cmd.Flags().StringArrayVar(&allowOrigins, "allow-origin", nil, "extra browser Origin allowed on the API and WebSockets, repeatable (default: same-origin only; \"*\" allows any)")
	return cmd
}

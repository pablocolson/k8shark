package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/pablocolson/k8shark/internal/mcp"
	"github.com/spf13/cobra"
)

func mcpCmd() *cobra.Command {
	var hubURL string
	var hubToken string
	var allowCapture bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server exposing captured traffic to AI agents",
		Long: "Runs a Model Context Protocol server over stdio. It relays the hub's\n" +
			"REST API to an AI agent as read-only tools (stats, entries, summaries,\n" +
			"timelines, workers, namespaces, workloads). stdout carries the JSON-RPC\n" +
			"protocol; all logs go to stderr.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// stdout is the MCP protocol channel, so keep a dedicated stderr
			// logger rather than risk anything writing to stdout.
			logger := newLogger(logLevel)

			if hubToken == "" {
				hubToken = os.Getenv("K8SHARK_API_TOKEN")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return mcp.New(hubURL, hubToken, allowCapture, logger).ServeStdio(ctx)
		},
	}
	cmd.Flags().StringVar(&hubURL, "hub", "http://localhost:8898", "hub base URL")
	cmd.Flags().StringVar(&hubToken, "hub-token", "", "bearer token for the hub API (default $K8SHARK_API_TOKEN)")
	cmd.Flags().BoolVar(&allowCapture, "allow-capture", false, "register the (placeholder) PCAP capture tool")
	return cmd
}

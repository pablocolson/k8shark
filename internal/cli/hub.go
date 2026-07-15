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
	cmd := &cobra.Command{
		Use:   "hub",
		Short: "Run the hub server (aggregates worker traffic, serves the API)",
		Long: "Runs the central hub that receives entries from workers and streams\n" +
			"them to front-end clients. This is what the hub container runs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			s := hub.New(log, uiDir)
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return s.Run(ctx, fmt.Sprintf(":%d", port))
		},
	}
	cmd.Flags().IntVar(&port, "port", config.DefaultHubPort, "listen port")
	cmd.Flags().StringVar(&uiDir, "serve-ui", "", "serve a built front from this directory (local dev)")
	return cmd
}

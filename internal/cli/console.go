package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/internal/k8s"
	"github.com/spf13/cobra"
)

func consoleCmd() *cobra.Command {
	var namespace, component string
	var follow bool
	cmd := &cobra.Command{
		Use:   "console",
		Short: "Stream logs from k8shark components",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return k8s.Logs(ctx, namespace, component, follow)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", config.DefaultNamespace, "namespace of the install")
	cmd.Flags().StringVar(&component, "component", "hub", "component: hub|worker|front")
	cmd.Flags().BoolVarP(&follow, "follow", "f", true, "follow the log stream")
	return cmd
}

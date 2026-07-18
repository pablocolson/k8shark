package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/internal/k8s"
	"github.com/spf13/cobra"
)

func cleanCmd() *cobra.Command {
	var namespace, release string
	var keepNamespace bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Remove k8shark from the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return k8s.Uninstall(ctx, log, release, namespace, keepNamespace)
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", config.DefaultNamespace, "namespace to remove")
	cmd.Flags().StringVar(&release, "release", "k8shark", "helm release name")
	cmd.Flags().BoolVar(&keepNamespace, "keep-namespace", false,
		"never delete the namespace, only the helm release (use when -n targets a namespace k8shark doesn't own exclusively)")
	return cmd
}

package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/internal/k8s"
	"github.com/spf13/cobra"
)

func proxyCmd() *cobra.Command {
	var namespace string
	var frontPort int
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Port-forward the dashboard of an existing install",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			pf, err := k8s.PortForward(ctx, namespace, "k8shark-front", frontPort, config.DefaultFrontPort)
			if err != nil {
				return err
			}
			url := fmt.Sprintf("http://localhost:%d", frontPort)
			fmt.Printf("\n  Dashboard: %s\n  Press Ctrl-C to stop.\n\n", url)
			if !noBrowser {
				k8s.OpenBrowser(url)
			}
			<-ctx.Done()
			_ = pf.Process.Kill()
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", config.DefaultNamespace, "namespace of the install")
	cmd.Flags().IntVar(&frontPort, "port", 8899, "local port for the dashboard")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser")
	return cmd
}

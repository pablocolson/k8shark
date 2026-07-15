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

func tapCmd() *cobra.Command {
	var (
		namespace string
		release   string
		frontPort int
		demo      bool
		noBrowser bool
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
			if err := k8s.Install(ctx, log, release, namespace, sets); err != nil {
				return err
			}

			pf, err := k8s.PortForward(ctx, namespace, "k8shark-front", frontPort, config.DefaultFrontPort)
			if err != nil {
				return fmt.Errorf("port-forward: %w", err)
			}
			url := fmt.Sprintf("http://localhost:%d", frontPort)
			log.Info("dashboard ready", "url", url)
			fmt.Printf("\n  k8shark is running.\n  Dashboard: %s\n  Press Ctrl-C to stop the port-forward (the install stays up; use `k8shark clean` to remove).\n\n", url)
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
	cmd.Flags().StringArrayVar(&sets, "set", nil, "extra helm --set values (repeatable)")
	return cmd
}

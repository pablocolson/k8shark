// Package cli implements the k8shark command tree. The same binary is the
// control-plane (tap/clean/proxy/console), the hub server (hub) and the node
// agent (worker); the sub-command chooses the role.
package cli

import (
	"log/slog"
	"os"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/spf13/cobra"
)

var (
	logLevel string
	log      *slog.Logger
)

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "k8shark",
		Short: "k8shark — cluster-wide network observability for Kubernetes",
		Long: "k8shark captures and reconstructs L7 traffic across a Kubernetes\n" +
			"cluster and streams it to a real-time dashboard.\n\n" +
			"Quick start:  k8shark tap",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			log = newLogger(logLevel)
			slog.SetDefault(log)
		},
		Version: config.Ver(),
	}
	cmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")

	cmd.AddCommand(
		tapCmd(),
		cleanCmd(),
		proxyCmd(),
		consoleCmd(),
		versionCmd(),
		hubCmd(),
		workerCmd(),
		mcpCmd(),
	)
	return cmd
}

// Execute runs the root command.
func Execute() error {
	return rootCmd().Execute()
}

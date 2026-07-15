package cli

import (
	"fmt"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/spf13/cobra"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the k8shark version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s %s\n", config.Name, config.Ver())
		},
	}
}

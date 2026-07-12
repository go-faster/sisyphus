package cmdutil

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/go-faster/sisyphus/internal/cliversion"
)

// ConfigureVersion sets the built-in --version output for a root command.
func ConfigureVersion(cmd *cobra.Command, info cliversion.Info) {
	cmd.Version = info.Short()
	cmd.SetVersionTemplate("{{.Name}} version {{.Version}}\n")
}

// NewVersionCmd prints full build information.
func NewVersionCmd(name string, info cliversion.Info) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", name, info)
			return err
		},
	}
}

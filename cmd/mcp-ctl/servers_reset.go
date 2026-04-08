package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newServersResetCircuitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset-circuit <name>",
		Short: "Reset the circuit breaker for a backend server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := validateServerName(args[0])
			if err != nil {
				return err
			}
			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			if err := client.ResetCircuit(cmd.Context(), name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Circuit breaker for %q reset.\n", name)
			return nil
		},
	}
}

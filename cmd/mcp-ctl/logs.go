package main

import (
	"fmt"
	"os"
	"os/signal"

	"mcp-gateway/internal/ctlclient"

	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Stream backend logs",
		Long:  "Stream live log output from a backend server via SSE.",
		Args:  cobra.ExactArgs(1),
		RunE:  runLogs,
	}

	cmd.Flags().Bool("no-reconnect", false, "Exit on disconnect instead of reconnecting")

	return cmd
}

func runLogs(cmd *cobra.Command, args []string) error {
	name, err := validateServerName(args[0])
	if err != nil {
		return err
	}
	noReconnect, _ := cmd.Flags().GetBool("no-reconnect")

	client, err := getClient(cmd)
	if err != nil {
		return err
	}

	// Wire Ctrl+C to context cancellation.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	opts := &ctlclient.StreamLogsOptions{
		Reconnect: !noReconnect,
	}

	err = client.StreamLogs(ctx, name, func(line string) {
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}, opts)

	if err != nil {
		return err
	}
	return nil
}

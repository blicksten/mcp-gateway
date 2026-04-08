package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/spf13/cobra"
)

// removeOptions holds injectable dependencies for the remove command.
// Production uses real stdin/TTY; tests override.
type removeOptions struct {
	isTTY       func() bool
	stdinReader io.Reader
}

func defaultRemoveOptions() removeOptions {
	return removeOptions{
		isTTY:       func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		stdinReader: os.Stdin,
	}
}

func newServersRemoveCmd() *cobra.Command {
	return newServersRemoveCmdWithOpts(defaultRemoveOptions())
}

func newServersRemoveCmdWithOpts(opts removeOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a backend server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := validateServerName(args[0])
			if err != nil {
				return err
			}
			force, _ := cmd.Flags().GetBool("force")

			if !force {
				if !opts.isTTY() {
					return fmt.Errorf("use --force to remove in non-interactive environments")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Remove server %q? [y/N] ", name)
				scanner := bufio.NewScanner(opts.stdinReader)
				if !scanner.Scan() {
					if err := scanner.Err(); err != nil {
						return fmt.Errorf("reading confirmation: %w", err)
					}
					return fmt.Errorf("aborted")
				}
				answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(cmd.ErrOrStderr(), "Aborted.")
					return nil
				}
			}

			client, err := getClient(cmd)
			if err != nil {
				return err
			}
			if err := client.RemoveServer(cmd.Context(), name); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Server %q removed.\n", name)
			return nil
		},
	}

	cmd.Flags().Bool("force", false, "Skip confirmation prompt")
	return cmd
}

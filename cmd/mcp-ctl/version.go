package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build info",
		Args:  cobra.NoArgs,
		RunE:  runVersion,
	}
}

func runVersion(cmd *cobra.Command, _ []string) error {
	// Prefer ldflags-injected values (set during release builds).
	if version != "dev" {
		fmt.Fprintf(cmd.OutOrStdout(), "mcp-ctl version %s (commit: %s, built: %s)\n", version, commit, date)
		return nil
	}

	// Fallback: extract from debug.ReadBuildInfo() for local dev builds.
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(cmd.OutOrStdout(), "mcp-ctl version: dev")
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "mcp-ctl version: dev\n")
	fmt.Fprintf(cmd.OutOrStdout(), "go: %s\n", info.GoVersion)

	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			fmt.Fprintf(cmd.OutOrStdout(), "commit: %s\n", rev)
		case "vcs.time":
			fmt.Fprintf(cmd.OutOrStdout(), "built: %s\n", s.Value)
		case "vcs.modified":
			if s.Value == "true" {
				fmt.Fprintln(cmd.OutOrStdout(), "dirty: true")
			}
		}
	}
	return nil
}

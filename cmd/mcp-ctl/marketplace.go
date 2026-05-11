// Spike 2026-05-11 FM 6 — marketplace `.bak` cleanup.
//
// Background: the mcp-gateway plugin installer creates ~/.claude/plugins/
// marketplaces/<name>.bak/ snapshots when it overwrites an existing
// marketplace tree. Today's investigation found these shadows accumulating
// indefinitely — at the time of the spike the live filesystem had both
// `claude-plugins-official/` and `claude-plugins-official.bak/`, each with
// 17 external_plugins and 19 .mcp.json files. Claude Code scans every
// marketplace tree at session start, so duplicate trees double the
// scan work and double the .mcp.json file count visible to the plugin
// loader. None of the .bak content is reachable from any live config.
//
// This subcommand removes .bak directories under the marketplaces root
// whose mtime is older than --max-age (default 7 days). 7 days is a
// deliberate safety margin so an installer mistake is recoverable from
// a still-on-disk shadow during the same week.

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newMarketplaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "marketplace",
		Short: "Manage Claude Code plugin marketplaces",
		Long: "Group of marketplace-level utilities. Currently only " +
			"`cleanup` is implemented (see `mcp-ctl marketplace cleanup --help`).",
	}
	cmd.Annotations = map[string]string{skipClientAnnotation: "true"}
	cmd.AddCommand(newMarketplaceCleanupCmd())
	return cmd
}

func newMarketplaceCleanupCmd() *cobra.Command {
	var (
		dryRun      bool
		maxAgeDays  int
		pluginsRoot string
	)
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove stale *.bak marketplace directories",
		Long: "Removes directories under ~/.claude/plugins/marketplaces/ " +
			"whose name ends in .bak and whose mtime is older than --max-age. " +
			"These are shadow copies left over from a previous installer run; " +
			"keeping them accumulates duplicate .mcp.json scanning work for " +
			"Claude Code on every session start.\n\n" +
			"Use --dry-run to preview without deleting. Use --root to override " +
			"the marketplaces directory path (defaults to ~/.claude/plugins/" +
			"marketplaces).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := pluginsRoot
			if root == "" {
				root = defaultMarketplacesRoot()
			}
			cutoff := time.Now().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
			out := cmd.OutOrStdout()
			report, err := scanStaleBak(root, cutoff)
			if err != nil {
				return err
			}
			if len(report) == 0 {
				fmt.Fprintf(out, "No stale .bak directories under %s\n", root)
				return nil
			}
			action := "Would remove"
			if !dryRun {
				action = "Removing"
			}
			fmt.Fprintf(out, "%s %d stale .bak %s under %s (cutoff: %s, max-age: %dd):\n",
				action, len(report),
				plural(len(report), "directory", "directories"),
				root, cutoff.UTC().Format(time.RFC3339), maxAgeDays)
			var firstErr error
			removed := 0
			for _, entry := range report {
				fmt.Fprintf(out, "  %s  (mtime %s)\n",
					entry.path, entry.mtime.UTC().Format(time.RFC3339))
				if dryRun {
					continue
				}
				if err := os.RemoveAll(entry.path); err != nil {
					fmt.Fprintf(out, "    ERROR: %v\n", err)
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				removed++
			}
			if dryRun {
				fmt.Fprintf(out, "Dry run — no files were touched.\n")
				return nil
			}
			fmt.Fprintf(out, "Removed %d of %d.\n", removed, len(report))
			if firstErr != nil {
				return fmt.Errorf("at least one removal failed: %w", firstErr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Preview what would be removed without deleting")
	cmd.Flags().IntVar(&maxAgeDays, "max-age", 7,
		"Only remove .bak directories whose mtime is older than this many days")
	cmd.Flags().StringVar(&pluginsRoot, "root", "",
		"Override marketplaces directory (default: ~/.claude/plugins/marketplaces)")
	return cmd
}

// staleBakEntry is one cleanup candidate, captured during scan so the
// dry-run path can print without re-stat-ing.
type staleBakEntry struct {
	path  string
	mtime time.Time
}

// scanStaleBak enumerates directories directly under root whose name ends
// in ".bak" and whose mtime is at or before cutoff. Returns entries sorted
// by path for deterministic output (test stability).
//
// Errors from individual file stats are tolerated and skipped — a single
// permission-denied dir must not abort the whole cleanup. A missing root
// directory is NOT an error (treated as "nothing to clean").
func scanStaleBak(root string, cutoff time.Time) ([]staleBakEntry, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read marketplaces dir %q: %w", root, err)
	}
	var out []staleBakEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".bak") {
			continue
		}
		full := filepath.Join(root, e.Name())
		info, err := e.Info()
		if err != nil {
			continue // best-effort; skip rather than abort the cleanup
		}
		if info.ModTime().After(cutoff) {
			continue // too young — leave alone
		}
		out = append(out, staleBakEntry{path: full, mtime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func defaultMarketplacesRoot() string {
	// os.UserHomeDir errors only when both $HOME and the Windows USERPROFILE
	// are missing — extremely rare for an interactive CLI session. Fall back
	// to a sentinel that scanStaleBak will report as missing.
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "marketplaces")
}

func plural(n int, singular, pluralWord string) string {
	if n == 1 {
		return singular
	}
	return pluralWord
}

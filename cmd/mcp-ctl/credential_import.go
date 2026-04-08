package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"mcp-gateway/internal/keepass"
	"mcp-gateway/internal/models"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newCredentialImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import credentials from a KeePass KDBX file",
		Long: `Import credentials from a KeePass KDBX file into .env file or apply via PATCH API.

Modes:
  --to-env-file <path>  Write KEY=VALUE lines to env file (default mode)
  --to-server           Apply credentials via PATCH API (loopback only)

Entry mapping:
  Title     → server name
  Password  → {SERVER}_PASSWORD env var
  UserName  → {SERVER}_USER env var
  Custom    → {SERVER}_{FIELD} env var
  HDR_*     → HTTP headers (--to-server) or env vars with warning (--to-env-file)

In --to-server mode, failures are non-transactional: already-applied
servers are NOT rolled back on partial failure.`,
		RunE: runCredentialImport,
	}

	cmd.Flags().String("keepass", "", "Path to KDBX database file (required)")
	cmd.Flags().String("password-file", "", "Path to file containing the master password")
	cmd.Flags().String("key-file", "", "Path to key file for KDBX authentication")
	cmd.Flags().String("to-env-file", "", "Write credentials to .env file (default mode)")
	cmd.Flags().Bool("to-server", false, "Apply credentials via PATCH API")
	cmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	cmd.Flags().String("group", "", "Only import entries from this KeePass group")

	_ = cmd.MarkFlagRequired("keepass")

	return cmd
}

func runCredentialImport(cmd *cobra.Command, _ []string) error {
	kdbxPath, _ := cmd.Flags().GetString("keepass")
	passwordFile, _ := cmd.Flags().GetString("password-file")
	keyFile, _ := cmd.Flags().GetString("key-file")
	envFilePath, _ := cmd.Flags().GetString("to-env-file")
	toServer, _ := cmd.Flags().GetBool("to-server")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	groupFilter, _ := cmd.Flags().GetString("group")

	// Validate mutually exclusive flags.
	if toServer && envFilePath != "" {
		return fmt.Errorf("--to-server and --to-env-file are mutually exclusive")
	}

	// Default mode: env file.
	if !toServer && envFilePath == "" {
		envFilePath = ".env"
	}

	// Read password.
	password, err := readPassword(cmd, passwordFile, keyFile)
	if err != nil {
		return err
	}
	defer zeroBytes(password)

	// Open and decode KDBX.
	db, err := keepass.OpenDatabase(kdbxPath, password, keyFile)
	if err != nil {
		return fmt.Errorf("open KDBX: %w", err)
	}

	// Extract entries, then release key material from db.
	entries := keepass.ExtractEntries(db, groupFilter)
	db.Credentials = nil
	db = nil
	if len(entries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No entries found in KDBX database.")
		return nil
	}
	defer func() {
		for i := range entries {
			entries[i].ZeroPassword()
		}
	}()

	// Map to credentials.
	creds, err := keepass.MapToCredentials(entries)
	if err != nil {
		return err
	}

	// Print summary.
	fmt.Fprintf(cmd.OutOrStdout(), "Found %d server(s) in KDBX:\n", len(creds))
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tENV VARS\tHEADERS")
	for _, sc := range creds {
		fmt.Fprintf(tw, "%s\t%d\t%d\n", sc.ServerName, len(sc.EnvVars), len(sc.Headers))
	}
	tw.Flush()

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "\n(dry-run: no changes made)")
		return nil
	}

	// Apply credentials.
	if toServer {
		return applyToServer(cmd, creds)
	}
	return applyToEnvFile(cmd, envFilePath, creds)
}

func readPassword(cmd *cobra.Command, passwordFile, keyFile string) ([]byte, error) {
	if passwordFile != "" {
		return keepass.ReadPasswordFile(passwordFile)
	}

	// If key-file-only auth, no password needed.
	if keyFile != "" {
		return nil, nil
	}

	// Interactive prompt.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("no --password-file and no --key-file provided, and stdin is not a terminal")
	}

	fmt.Fprint(cmd.OutOrStderr(), "Master password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(cmd.OutOrStderr()) // newline after password
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	return pw, nil
}

func applyToEnvFile(cmd *cobra.Command, path string, creds []keepass.ServerCredentials) error {
	if err := keepass.WriteEnvFile(path, creds); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nCredentials written to %s\n", path)
	return nil
}

func applyToServer(cmd *cobra.Command, creds []keepass.ServerCredentials) error {
	client, err := getClient(cmd)
	if err != nil {
		return fmt.Errorf("--to-server requires a gateway connection: %w", err)
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "\nSERVER\tSTATUS\tDETAIL")

	for _, sc := range creds {
		if err := models.ValidateServerName(sc.ServerName); err != nil {
			fmt.Fprintf(tw, "%s\tSKIPPED\tinvalid server name: %s\n", sc.ServerName, err)
			continue
		}

		var addEnv []string
		for k, v := range sc.EnvVars {
			addEnv = append(addEnv, k+"="+v)
		}

		err := client.PatchServerEnv(cmd.Context(), sc.ServerName, addEnv, nil)
		if err != nil {
			fmt.Fprintf(tw, "%s\tFAILED\t%s\n", sc.ServerName, err)
			continue
		}

		if len(sc.Headers) > 0 {
			err = client.PatchServerHeaders(cmd.Context(), sc.ServerName, sc.Headers, nil)
			if err != nil {
				fmt.Fprintf(tw, "%s\tPARTIAL\tenv OK, headers failed: %s\n", sc.ServerName, err)
				continue
			}
		}

		fmt.Fprintf(tw, "%s\tOK\t%d env, %d headers\n", sc.ServerName, len(sc.EnvVars), len(sc.Headers))
	}

	tw.Flush()
	return nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

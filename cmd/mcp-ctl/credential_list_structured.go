package main

import (
	"encoding/json"
	"fmt"

	"mcp-gateway/internal/sapcreds"
	"mcp-gateway/internal/saplandscape"

	"github.com/spf13/cobra"
)

// structuredRow is the JSON shape emitted by `credential list-structured`.
// It mirrors sapcreds.Row with snake_case JSON tags for external consumers.
type structuredRow struct {
	SID       string `json:"sid"`
	Client    string `json:"client"`
	User      string `json:"user"`
	KPMissing bool   `json:"kpMissing"`
}

func newCredentialListStructuredCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-structured",
		Short: "List SAP credentials as structured JSON (landscape ∪ KeePass intersection)",
		Long: `Reads a SAPUILandscape.xml file and a KeePass vault, intersects them,
and emits a JSON array of rows — one per landscape SID. Each row carries the
SID, client, user, and a kpMissing flag indicating whether no matching KeePass
entry was found.

Password sources (mutually exclusive):
  --password-stdin    read master password from stdin (preferred for non-TTY
                      callers such as the VS Code extension; password is never
                      visible in process listings).
  --password-file P   read master password from file P.
  --password PW       pass master password directly (DEPRECATED — visible in
                      process listings and shell history; kept only for
                      backwards compatibility).
  (none)              interactive TTY prompt.`,
		RunE: runCredentialListStructured,
	}

	cmd.Flags().String("kdbx", "", "Path to the KeePass KDBX vault (required)")
	cmd.Flags().String("password", "", "Master password (DEPRECATED — prefer --password-stdin)")
	cmd.Flags().String("password-file", "", "Path to file containing the master password")
	cmd.Flags().Bool("password-stdin", false,
		"Read master password from stdin (mutually exclusive with --password / --password-file). "+
			"Mirrors `credential import --password-stdin` so the extension can pipe the password "+
			"without it appearing on the command line.")
	cmd.Flags().String("keyfile", "", "Path to the KeePass key file (optional)")
	cmd.Flags().String("landscape", "", "Path to SAPUILandscape.xml (required)")

	_ = cmd.MarkFlagRequired("kdbx")
	_ = cmd.MarkFlagRequired("landscape")

	return cmd
}

func runCredentialListStructured(cmd *cobra.Command, _ []string) error {
	kdbxPath, _ := cmd.Flags().GetString("kdbx")
	passwordFlag, _ := cmd.Flags().GetString("password")
	passwordFile, _ := cmd.Flags().GetString("password-file")
	passwordStdin, _ := cmd.Flags().GetBool("password-stdin")
	keyfile, _ := cmd.Flags().GetString("keyfile")
	landscapePath, _ := cmd.Flags().GetString("landscape")

	// Mutual-exclusivity guards mirror credential_import.go so misuse is
	// reported the same way to callers (VS Code extension, ops scripts).
	if passwordStdin && passwordFile != "" {
		return fmt.Errorf("--password-stdin and --password-file are mutually exclusive")
	}
	if passwordStdin && passwordFlag != "" {
		return fmt.Errorf("--password-stdin and --password are mutually exclusive")
	}
	if passwordFile != "" && passwordFlag != "" {
		return fmt.Errorf("--password-file and --password are mutually exclusive")
	}

	// Resolve password from one of the four sources. The legacy --password
	// flag short-circuits the shared readPassword helper so deprecation
	// stays additive (no behaviour change for existing callers).
	var password []byte
	if passwordFlag != "" {
		password = []byte(passwordFlag)
	} else {
		pw, err := readPassword(cmd, passwordFile, passwordStdin, keyfile)
		if err != nil {
			return err
		}
		password = pw
	}
	defer zeroBytes(password)

	landscape, err := saplandscape.Parse(landscapePath)
	if err != nil {
		return err
	}

	// Pass password as []byte directly to mirror credential_import's known-
	// good path. The earlier string round-trip via ListEntries showed an
	// HMAC-SHA256 mismatch on the operator's KDBX (Cyrillic master, KDBX4,
	// 2026-05-27); ListEntriesBytes skips the conversion to rule that out.
	kpEntries, err := sapcreds.ListEntriesBytes(kdbxPath, password, keyfile)
	if err != nil {
		return err
	}

	rows, warnings := sapcreds.Hybrid(landscape, kpEntries)

	// Surface KP-duplicate warnings on stderr so the JSON-on-stdout contract
	// stays clean for tooling that pipes the output.
	for _, msg := range warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
	}

	out := make([]structuredRow, len(rows))
	for i, r := range rows {
		out[i] = structuredRow{
			SID:       r.SID,
			Client:    r.Client,
			User:      r.User,
			KPMissing: r.KPMissing,
		}
	}

	return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
}

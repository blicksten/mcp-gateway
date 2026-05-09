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
entry was found.`,
		RunE: runCredentialListStructured,
	}

	cmd.Flags().String("kdbx", "", "Path to the KeePass KDBX vault (required)")
	cmd.Flags().String("password", "", "Master password for the vault")
	cmd.Flags().String("keyfile", "", "Path to the KeePass key file (optional)")
	cmd.Flags().String("landscape", "", "Path to SAPUILandscape.xml (required)")

	_ = cmd.MarkFlagRequired("kdbx")
	_ = cmd.MarkFlagRequired("landscape")

	return cmd
}

func runCredentialListStructured(cmd *cobra.Command, _ []string) error {
	kdbxPath, _ := cmd.Flags().GetString("kdbx")
	password, _ := cmd.Flags().GetString("password")
	keyfile, _ := cmd.Flags().GetString("keyfile")
	landscapePath, _ := cmd.Flags().GetString("landscape")

	landscape, err := saplandscape.Parse(landscapePath)
	if err != nil {
		return err
	}

	kpEntries, err := sapcreds.ListEntries(kdbxPath, password, keyfile)
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

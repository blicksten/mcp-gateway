package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"mcp-gateway/internal/keepass"
	"mcp-gateway/internal/models"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// CredentialImportJSONVersion is the stable JSON output contract version
// consumed by the VS Code extension's keepass-importer.ts (T12B.1).
// Bump only on breaking schema changes.
const CredentialImportJSONVersion = 1

// credentialImportJSON is the wire format produced by `mcp-ctl credential
// import --json`. Stable across minor releases per ADR-0003 consumer
// contract. Extension parses this verbatim.
type credentialImportJSON struct {
	Version int                        `json:"version"`
	Mode    string                     `json:"mode"` // "dry-run" | "to-env-file" | "to-server"
	Found   int                        `json:"found"`
	Servers []credentialImportServer   `json:"servers"`
	Results []credentialImportResult   `json:"results,omitempty"` // only for to-server mode
}

// credentialImportServer is one KeePass-sourced server entry.
// SECURITY: EnvVars / Headers contain plaintext credentials. Callers must
// never log this struct. The extension writes values into SecretStorage.
type credentialImportServer struct {
	Name    string            `json:"name"`
	EnvVars map[string]string `json:"env_vars"`
	Headers map[string]string `json:"headers"`
}

// credentialImportResult is per-server application outcome (to-server mode).
type credentialImportResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "ok" | "skipped" | "failed" | "partial"
	Detail string `json:"detail,omitempty"`
}

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
	cmd.Flags().Bool("password-stdin", false, "Read master password from stdin (exclusive with --password-file; enables non-TTY exec, e.g. child process from VS Code extension)")
	cmd.Flags().String("key-file", "", "Path to key file for KDBX authentication")
	cmd.Flags().String("to-env-file", "", "Write credentials to .env file (default mode)")
	cmd.Flags().Bool("to-server", false, "Apply credentials via PATCH API")
	cmd.Flags().Bool("dry-run", false, "Show what would be done without making changes")
	cmd.Flags().String("group", "", "Only import entries from this KeePass group")
	cmd.Flags().Bool("json", false, "Output a stable JSON contract to stdout (for programmatic consumption; values are plaintext — do NOT log)")

	_ = cmd.MarkFlagRequired("keepass")

	return cmd
}

func runCredentialImport(cmd *cobra.Command, _ []string) error {
	kdbxPath, _ := cmd.Flags().GetString("keepass")
	passwordFile, _ := cmd.Flags().GetString("password-file")
	passwordStdin, _ := cmd.Flags().GetBool("password-stdin")
	keyFile, _ := cmd.Flags().GetString("key-file")
	envFilePath, _ := cmd.Flags().GetString("to-env-file")
	toServer, _ := cmd.Flags().GetBool("to-server")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	groupFilter, _ := cmd.Flags().GetString("group")
	jsonOut, _ := cmd.Flags().GetBool("json")

	// Validate mutually exclusive flags.
	if toServer && envFilePath != "" {
		return fmt.Errorf("--to-server and --to-env-file are mutually exclusive")
	}
	if passwordStdin && passwordFile != "" {
		return fmt.Errorf("--password-stdin and --password-file are mutually exclusive")
	}

	// --json forces dry-run semantics when no explicit destination is
	// given: programmatic callers (VS Code extension) want to see the
	// structured contents first, then they decide what to do.
	if jsonOut && !toServer && envFilePath == "" {
		dryRun = true
	}

	// Default mode: env file (only when not in --json mode).
	if !toServer && envFilePath == "" && !jsonOut {
		envFilePath = ".env"
	}

	// Read password.
	password, err := readPassword(cmd, passwordFile, passwordStdin, keyFile)
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
		if jsonOut {
			// PAL CRITICAL fix: --json must return valid JSON for every
			// exit path so programmatic consumers (keepass-importer.ts)
			// never have to handle a human-text stream.
			return emitJSONAndMaybeApply(cmd, []keepass.ServerCredentials{}, envFilePath, toServer, dryRun)
		}
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

	// JSON output path — structured for programmatic consumers.
	if jsonOut {
		return emitJSONAndMaybeApply(cmd, creds, envFilePath, toServer, dryRun)
	}

	// Human-readable summary.
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

// emitJSONAndMaybeApply renders the JSON contract and optionally applies
// credentials (to-server). Values in the emitted JSON are plaintext —
// stdout MUST NOT be logged by programmatic consumers. The extension's
// keepass-importer.ts enforces this invariant.
func emitJSONAndMaybeApply(cmd *cobra.Command, creds []keepass.ServerCredentials, envFilePath string, toServer, dryRun bool) error {
	mode := "dry-run"
	switch {
	case toServer:
		mode = "to-server"
	case envFilePath != "":
		mode = "to-env-file"
	}

	out := credentialImportJSON{
		Version: CredentialImportJSONVersion,
		Mode:    mode,
		Found:   len(creds),
		Servers: make([]credentialImportServer, 0, len(creds)),
	}
	for _, sc := range creds {
		out.Servers = append(out.Servers, credentialImportServer{
			Name:    sc.ServerName,
			EnvVars: copyMap(sc.EnvVars),
			Headers: copyMap(sc.Headers),
		})
	}

	if !dryRun {
		switch {
		case toServer:
			out.Results = applyToServerJSON(cmd, creds)
		case envFilePath != "":
			if err := keepass.WriteEnvFile(envFilePath, creds); err != nil {
				return writeJSONAndError(cmd, out, err)
			}
		}
	}

	// Use an Encoder with SetEscapeHTML(false) so Bearer tokens with `&`
	// or `<` in SAP-generated secrets round-trip intact.
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// writeJSONAndError writes the partial JSON to stdout then returns the
// error. Keeps stdout a valid JSON object even on failure.
func writeJSONAndError(cmd *cobra.Command, out credentialImportJSON, err error) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return err
}

func copyMap(src map[string]string) map[string]string {
	if src == nil {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// applyToServerJSON applies credentials via PATCH API and returns a
// per-server result slice suitable for JSON serialization (no human
// text output). Mirrors applyToServer but without tabwriter.
func applyToServerJSON(cmd *cobra.Command, creds []keepass.ServerCredentials) []credentialImportResult {
	client, err := getClient(cmd)
	if err != nil {
		// Connection-level failure — every server is unreachable.
		results := make([]credentialImportResult, 0, len(creds))
		for _, sc := range creds {
			results = append(results, credentialImportResult{
				Name:   sc.ServerName,
				Status: "failed",
				Detail: "no gateway connection: " + err.Error(),
			})
		}
		return results
	}

	results := make([]credentialImportResult, 0, len(creds))
	for _, sc := range creds {
		if err := models.ValidateServerName(sc.ServerName); err != nil {
			results = append(results, credentialImportResult{
				Name:   sc.ServerName,
				Status: "skipped",
				Detail: "invalid server name: " + err.Error(),
			})
			continue
		}

		var addEnv []string
		for k, v := range sc.EnvVars {
			addEnv = append(addEnv, k+"="+v)
		}

		if err := client.PatchServerEnv(cmd.Context(), sc.ServerName, addEnv, nil); err != nil {
			results = append(results, credentialImportResult{
				Name: sc.ServerName, Status: "failed", Detail: err.Error(),
			})
			continue
		}

		if len(sc.Headers) > 0 {
			if err := client.PatchServerHeaders(cmd.Context(), sc.ServerName, sc.Headers, nil); err != nil {
				results = append(results, credentialImportResult{
					Name: sc.ServerName, Status: "partial",
					Detail: "env OK, headers failed: " + err.Error(),
				})
				continue
			}
		}

		results = append(results, credentialImportResult{
			Name: sc.ServerName, Status: "ok",
		})
	}
	return results
}

func readPassword(cmd *cobra.Command, passwordFile string, passwordStdin bool, keyFile string) ([]byte, error) {
	// T12B.2: --password-stdin enables non-TTY password supply. The
	// extension pipes the master password into mcp-ctl's stdin so
	// there is no TTY prompt when running as a child process. First
	// line only, CR/LF trimmed, stdin closed on return.
	if passwordStdin {
		return readPasswordStdin(cmd.InOrStdin())
	}

	if passwordFile != "" {
		return keepass.ReadPasswordFile(passwordFile)
	}

	// If key-file-only auth, no password needed.
	if keyFile != "" {
		return nil, nil
	}

	// Interactive prompt.
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("no --password-file, --password-stdin, or --key-file provided, and stdin is not a terminal")
	}

	fmt.Fprint(cmd.OutOrStderr(), "Master password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(cmd.OutOrStderr()) // newline after password
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	return pw, nil
}

// maxStdinPasswordBytes caps the stdin read to prevent memory blow-up
// if a caller mistakenly pipes a huge blob without a newline.
const maxStdinPasswordBytes = 4096

// readPasswordStdin reads a single line from stdin as the master
// password. CR/LF are trimmed. The password NEVER transits a Go
// `string` — strings are immutable and cannot be zeroed, so a string
// copy would leave the secret in memory until GC. Instead we operate
// on []byte throughout and zero both the original buffer and any
// intermediate copies on every exit path. (PAL HIGH + MEDIUM fix.)
func readPasswordStdin(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, maxStdinPasswordBytes+1))
	buf, err := br.ReadBytes('\n')
	// On EOF without newline, ReadBytes returns the data plus io.EOF.
	if err != nil && err != io.EOF {
		zeroBytes(buf)
		return nil, fmt.Errorf("read password from stdin: %w", err)
	}
	if len(buf) > maxStdinPasswordBytes {
		zeroBytes(buf)
		return nil, fmt.Errorf("--password-stdin input exceeds %d byte cap", maxStdinPasswordBytes)
	}

	// Trim trailing \n and any bare \r (Windows piping) IN PLACE on []byte.
	trimEnd := len(buf)
	for trimEnd > 0 && (buf[trimEnd-1] == '\n' || buf[trimEnd-1] == '\r') {
		trimEnd--
	}
	if trimEnd == 0 {
		zeroBytes(buf)
		return nil, fmt.Errorf("--password-stdin provided but stdin was empty")
	}

	// Copy the trimmed bytes into a new slice so the bufio-owned buffer
	// can be zeroed. Caller zeroes the returned slice after use.
	pw := make([]byte, trimEnd)
	copy(pw, buf[:trimEnd])
	zeroBytes(buf)
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

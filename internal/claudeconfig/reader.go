package claudeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

// Snapshot is the result of reading one of the three Claude Code config
// sources. It carries enough state to drive the import-snapshot and
// import-apply REST endpoints AND to drive a subsequent CAS write that
// preserves top-level fields outside mcpServers.
type Snapshot struct {
	// Source is the logical identifier (cc_global / cc_project / desktop).
	Source Source `json:"source"`

	// Path is the resolved absolute filesystem path.
	Path string `json:"path"`

	// Exists reports whether the file was present at read time.
	// When false, Entries is nil and ModTimeNS is 0; the caller
	// should treat this as "no entries to import" (not an error).
	Exists bool `json:"exists"`

	// ModTimeNS is the file's modification time as Unix nanos at the
	// point Read returned a stable bytes/mtime pair (post mtime-CAS
	// retry). 0 when Exists is false. Used by callers that perform
	// a CAS write later.
	ModTimeNS int64 `json:"mod_time_ns"`

	// Raw is the complete raw bytes of the file (for callers that
	// want to splice via RawRoot). nil when Exists is false.
	Raw []byte `json:"-"`

	// Entries is the parsed mcpServers map, keyed by server name.
	// Empty map (not nil) when the file exists but has no mcpServers
	// or mcpServers is empty.
	Entries map[string]EntryRaw `json:"entries"`

	// Warnings carries non-fatal anomalies surfaced during the read
	// (e.g. unrecognised fields preserved verbatim).
	Warnings []string `json:"warnings,omitempty"`
}

// EntryRaw is one mcpServer entry as it appeared on disk.
//
// We keep the raw JSON bytes verbatim under Raw because Claude Code's
// schema is open-ended (transport-specific fields like url/headers for
// http transport, command/args/env for stdio); mapping every variant to
// a typed struct would either lose unrecognised keys (the property we
// want to preserve) or require painful interface{} fields throughout.
//
// Type, Command, Args, URL are extracted for convenience (e.g. UI
// preview) but the round-trip uses Raw, so unrecognised keys survive.
type EntryRaw struct {
	Name string          `json:"name"`
	Raw  json.RawMessage `json:"-"`

	// Convenience fields extracted for snapshot UI consumption.
	// Not used by the round-trip path.
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// readErr wraps a typed error for callers that want to distinguish
// "file missing" from "parse failure" without comparing strings.
type readErr struct {
	op   string
	err  error
	path string
}

func (e *readErr) Error() string { return fmt.Sprintf("claudeconfig: %s %q: %v", e.op, e.path, e.err) }
func (e *readErr) Unwrap() error { return e.err }

// IsNotExist reports whether err signals a non-existent file. Wraps
// errors.Is + os.IsNotExist for the typed error.
func IsNotExist(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err)
}

// readAttempt holds one mtime+bytes pair from a single read.
type readAttempt struct {
	mtimeNS int64
	body    []byte
}

// readFileWithMtimeCAS reads path and verifies the file's mtime did not
// change during the read. Up to maxRetries on mtime mismatch; raises
// after that.
//
// This is the R-08 mtime-CAS sequence required for safe reads of files
// that other processes (Claude Code itself, `claude mcp add`) may write
// concurrently. The lockfile (acquired by callers, not here) protects
// the WRITE path; mtime-CAS protects the READ path.
//
// Returns (body, mtimeNS, error). On a non-existent file, returns
// errors satisfying IsNotExist.
func readFileWithMtimeCAS(path string, maxRetries int) ([]byte, int64, error) {
	if maxRetries < 1 {
		maxRetries = 3
	}
	for attempt := 0; attempt < maxRetries; attempt++ {
		st1, err := os.Stat(path)
		if err != nil {
			return nil, 0, &readErr{op: "stat", err: err, path: path}
		}
		mtime1 := st1.ModTime().UnixNano()
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, &readErr{op: "read", err: err, path: path}
		}
		st2, err := os.Stat(path)
		if err != nil {
			return nil, 0, &readErr{op: "stat-recheck", err: err, path: path}
		}
		mtime2 := st2.ModTime().UnixNano()
		if mtime1 == mtime2 {
			return body, mtime1, nil
		}
	}
	return nil, 0, &readErr{op: "mtime-cas", err: errors.New("mtime changed during read"), path: path}
}

// Read parses the snapshot of source src. projectRoot is honoured only
// for SourceCCProject. Read is the unified entry point used by all three
// readers (cc_global, cc_project, desktop) — they differ only in path
// resolution + which warning prefixes apply.
//
// File-missing is NOT an error: returns Snapshot{Exists:false}. Parse
// failures, mtime-CAS exhaustion, and lockfile failures ARE errors.
func Read(src Source, projectRoot string) (*Snapshot, error) {
	path, err := ResolvePath(src, projectRoot)
	if err != nil {
		return nil, err
	}

	body, mtimeNS, err := readFileWithMtimeCAS(path, 3)
	if err != nil {
		if IsNotExist(err) {
			return &Snapshot{Source: src, Path: path, Exists: false}, nil
		}
		return nil, err
	}

	snap := &Snapshot{
		Source:    src,
		Path:      path,
		Exists:    true,
		ModTimeNS: mtimeNS,
		Raw:       body,
		Entries:   map[string]EntryRaw{},
	}

	// Empty file → exists with zero entries (treat as "no servers").
	if len(body) == 0 {
		return snap, nil
	}

	rr, err := ParseRawRoot(body)
	if err != nil {
		return nil, &readErr{op: "parse", err: err, path: path}
	}
	if !rr.HasMcpServers() {
		return snap, nil
	}

	// Parse mcpServers as an object of {name: server config}.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.McpServersBytes(), &raw); err != nil {
		return nil, &readErr{op: "decode-mcpServers", err: err, path: path}
	}

	// Sort keys so subsequent processing is deterministic across runs.
	names := make([]string, 0, len(raw))
	for k := range raw {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		entry := EntryRaw{
			Name: name,
			Raw:  append(json.RawMessage(nil), raw[name]...),
		}
		decodeEntryConvenience(raw[name], &entry)
		snap.Entries[name] = entry
	}

	return snap, nil
}

// decodeEntryConvenience populates the convenience fields of EntryRaw
// (Type, Command, Args, URL, Env) on a best-effort basis. Unknown
// fields are silently ignored at this layer — the round-trip uses the
// full Raw bytes, so unrecognised fields are preserved by Apply.
func decodeEntryConvenience(raw json.RawMessage, out *EntryRaw) {
	var conv struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		URL     string            `json:"url"`
		Env     map[string]string `json:"env"`
	}
	// Errors are expected on schemas we don't recognise; leave the
	// out fields as their zero values in that case. The full Raw
	// bytes still round-trip.
	_ = json.Unmarshal(raw, &conv)
	out.Type = conv.Type
	out.Command = conv.Command
	out.Args = conv.Args
	out.URL = conv.URL
	out.Env = conv.Env
}

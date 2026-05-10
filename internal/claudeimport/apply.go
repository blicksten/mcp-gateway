package claudeimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"mcp-gateway/internal/claudeconfig"
	"mcp-gateway/internal/models"
)

// Action enumerates the import-apply operation kinds. Per spike §4.2:
// only `copy` and `move` are supported; `duplicate` was explicitly
// scoped out (R-25).
type Action string

const (
	ActionCopy Action = "copy"
	ActionMove Action = "move"
)

// ConflictPolicy controls behaviour when the gateway already has an
// entry with the candidate name.
type ConflictPolicy string

const (
	ConflictSkip      ConflictPolicy = "skip"
	ConflictOverwrite ConflictPolicy = "overwrite"
)

// Op is one row in an Apply request.
type Op struct {
	// Source identifies the origin file (cc_global / cc_project /
	// desktop). The Apply call resolves the path via
	// claudeconfig.ResolvePath.
	Source claudeconfig.Source `json:"source"`

	// ProjectRoot is the workspace path used only by SourceCCProject.
	ProjectRoot string `json:"project_root,omitempty"`

	// Name is the entry name in the source file. The gateway side
	// uses the same name unless the operator renamed via DestName.
	Name string `json:"name"`

	// DestName is the entry name on the gateway side. Empty → reuse
	// Name. Set this when the operator renamed during the picker UX.
	DestName string `json:"dest_name,omitempty"`

	// Action is "copy" or "move".
	Action Action `json:"action"`

	// Conflict is the policy for the gateway-already-has-this-name
	// case. Mandatory.
	Conflict ConflictPolicy `json:"conflict"`

	// Override carries an operator-edited ServerConfig that replaces
	// the source-file config. Used when the picker UI lets the
	// operator tweak args/env before apply. nil → use source bytes
	// as-is (translated via TranslateEntry).
	Override *models.ServerConfig `json:"override,omitempty"`
}

// Status captures the outcome of one Op.
type Status string

const (
	StatusApplied  Status = "applied"
	StatusSkipped  Status = "skipped"
	StatusConflict Status = "conflict"
	StatusError    Status = "error"
)

// OpResult is one row in an Apply response.
type OpResult struct {
	Name              string   `json:"name"`
	DestName          string   `json:"dest_name"`
	Action            Action   `json:"action"`
	Status            Status   `json:"status"`
	Reason            string   `json:"reason,omitempty"`
	ResolvedCommand   string   `json:"resolved_command,omitempty"`
	DriftFields       []string `json:"drift_fields,omitempty"`
	SourceUpdated     bool     `json:"source_updated"`
	SourceUpdatedAt   string   `json:"source_updated_at,omitempty"`
	// ProvenanceWarning surfaces a non-fatal error from the sidecar
	// write (e.g. Windows Rename access-denied due to AV scanner).
	// The operation itself succeeded but the next snapshot's
	// "previously imported" badge will not show this import
	// (F-07 fix — was a silent discard).
	ProvenanceWarning string `json:"provenance_warning,omitempty"`
}

// AddOpts mirrors api.AddOpts (avoiding a circular import). The api
// package's addServerInProcess accepts a parallel struct.
type AddOpts struct {
	SuppressPluginRegen bool
	SkipAutoStart       bool
}

// RemoveOpts mirrors api.RemoveOpts.
type RemoveOpts struct {
	SuppressPluginRegen bool
}

// RemoveResult mirrors lifecycle.RemoveResult.
type RemoveResult struct {
	Orphan  bool
	StopErr error
}

// Adder is the callback the api package supplies to drive the
// addServerInProcess critical section. We can't import api here
// (apply.go would create a cycle).
type Adder func(ctx context.Context, name string, sc *models.ServerConfig, opts AddOpts) error

// Remover is the callback for removeServerInProcess.
type Remover func(ctx context.Context, name string, opts RemoveOpts) (RemoveResult, error)

// GatewaySnapshot is supplied by the caller — it represents the
// daemon's current entries at the point Apply began. Used for
// conflict detection.
type GatewaySnapshot struct {
	Entries map[string]json.RawMessage
}

// Dependencies bundles the callbacks Apply needs.
type Dependencies struct {
	Adder            Adder
	Remover          Remover
	GatewaySnapshot  GatewaySnapshot
	SidecarPath      string // empty → use DefaultSidecarPath
	BeforeSourceWrite func(path string)             // optional hook (T-D.4 reflector pause)
	AfterSourceWrite  func(path string, success bool) // optional hook (T-D.4 reflector resume)
}

// sourceLocks guards the source-file write critical section against
// in-process concurrent moves on the same file. Refcounted: entries
// are deleted from the map once the last waiter releases, so the map
// cardinality is O(active concurrent paths), not O(total paths ever
// seen) (F-02 fix — was unbounded).
type refcountedMutex struct {
	mu       sync.Mutex
	waiters  int // protected by sourceLocksMu, not mu itself
}

var (
	sourceLocksMu sync.Mutex
	sourceLocks   = map[string]*refcountedMutex{}
)

// lockSource returns a refcounted mutex for path with the waiter
// count already incremented. Callers must call releaseSourceLock to
// decrement; lockSource + Lock must be matched by Unlock + release.
func lockSource(path string) *refcountedMutex {
	sourceLocksMu.Lock()
	defer sourceLocksMu.Unlock()
	if rm, ok := sourceLocks[path]; ok {
		rm.waiters++
		return rm
	}
	rm := &refcountedMutex{waiters: 1}
	sourceLocks[path] = rm
	return rm
}

// releaseSourceLock decrements the waiter count for path. When it
// drops to zero, the entry is removed from the global map so the
// map does not grow forever.
func releaseSourceLock(path string, rm *refcountedMutex) {
	sourceLocksMu.Lock()
	defer sourceLocksMu.Unlock()
	rm.waiters--
	if rm.waiters <= 0 {
		delete(sourceLocks, path)
	}
}

// Apply executes ops sequentially against the gateway state via deps,
// returning per-op results. Sequential rather than parallel because
// each op may mutate the same source file (move semantics on the same
// origin); the contention model is "1 source-file lock per file" so
// parallelism would only help across distinct files. Sequential keeps
// the failure attribution simple and matches the picker UX which
// shows one row at a time advancing.
//
// Apply does NOT acquire deps.Adder/Remover under any internal lock —
// those callbacks own their own concurrency model (the api package's
// cfgMu).
func Apply(ctx context.Context, ops []Op, deps Dependencies) []OpResult {
	results := make([]OpResult, len(ops))
	for i, op := range ops {
		results[i] = applyOne(ctx, op, deps)
	}
	return results
}

// applyOne is the core per-row workhorse.
func applyOne(ctx context.Context, op Op, deps Dependencies) OpResult {
	res := OpResult{
		Name:     op.Name,
		DestName: destNameOf(op),
		Action:   op.Action,
	}

	if err := validateOp(&op); err != nil {
		res.Status = StatusError
		res.Reason = err.Error()
		return res
	}

	// Read source.
	snap, err := claudeconfig.Read(op.Source, op.ProjectRoot)
	if err != nil {
		res.Status = StatusError
		res.Reason = fmt.Sprintf("read source: %v", err)
		return res
	}
	if !snap.Exists {
		res.Status = StatusError
		res.Reason = fmt.Sprintf("source file does not exist: %s", snap.Path)
		return res
	}
	entry, ok := snap.Entries[op.Name]
	if !ok {
		res.Status = StatusError
		res.Reason = fmt.Sprintf("entry %q not found in source", op.Name)
		return res
	}

	// Build the daemon-side ServerConfig.
	sc, resolution, err := translateEntry(entry, op.Override)
	if err != nil {
		res.Status = StatusError
		res.Reason = fmt.Sprintf("translate entry: %v", err)
		return res
	}
	res.ResolvedCommand = resolution.AbsPath

	// Conflict detection.
	if existing, exists := deps.GatewaySnapshot.Entries[res.DestName]; exists {
		drift := DriftFields(json.RawMessage(entry.Raw), GatewayState{Entries: deps.GatewaySnapshot.Entries}, res.DestName)
		res.DriftFields = drift
		switch op.Conflict {
		case ConflictSkip:
			res.Status = StatusSkipped
			res.Reason = fmt.Sprintf("entry %q already exists; skip per conflict policy", res.DestName)
			return res
		case ConflictOverwrite:
			// Remove first; the Adder below will re-add.
			if _, err := deps.Remover(ctx, res.DestName, RemoveOpts{SuppressPluginRegen: true}); err != nil {
				res.Status = StatusError
				res.Reason = fmt.Sprintf("remove existing %q before overwrite: %v", res.DestName, err)
				return res
			}
			_ = existing // referenced for side-effect (clarity)
		default:
			res.Status = StatusError
			res.Reason = fmt.Sprintf("invalid conflict policy %q", op.Conflict)
			return res
		}
	}

	// Add to gateway. Suppress plugin regen — caller's batch handler
	// will fire one regen + RebuildTools at end of batch.
	if err := deps.Adder(ctx, res.DestName, sc, AddOpts{SuppressPluginRegen: true}); err != nil {
		res.Status = StatusError
		res.Reason = fmt.Sprintf("add %q: %v", res.DestName, err)
		return res
	}

	// Move semantics: delete from source.
	if op.Action == ActionMove {
		if err := mutateSourceRemove(snap, op.Name, op.ProjectRoot, deps); err != nil {
			// Gateway has the entry but the source delete aborted
			// (typically mtime-CAS catching a concurrent writer).
			// Status stays Applied — partial-success contract is:
			// SourceUpdated=false + non-empty Reason. The provenance
			// record is still written below so future snapshots show
			// the badge and the operator's retry path is informed.
			res.Reason = fmt.Sprintf("source delete failed: %v", err)
		} else {
			res.SourceUpdated = true
			res.SourceUpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}

	// Provenance sidecar.
	sidecarPath := deps.SidecarPath
	if sidecarPath == "" {
		p, perr := DefaultSidecarPath()
		if perr == nil {
			sidecarPath = p
		}
	}
	if sidecarPath != "" {
		if err := AppendProvenance(sidecarPath, ProvenanceRecord{
			Source:     string(op.Source),
			SourcePath: snap.Path,
			Name:       op.Name,
			Action:     string(op.Action),
		}); err != nil {
			// Surface the warning via Reason — the operation
			// itself succeeded but the snapshot's
			// "previously imported" badge will not show this
			// import on the next read (F-07 fix — was a silent
			// discard via _ =).
			res.ProvenanceWarning = err.Error()
		}
	}

	res.Status = StatusApplied
	return res
}

// destNameOf returns the gateway-side name for op.
func destNameOf(op Op) string {
	if op.DestName != "" {
		return op.DestName
	}
	return op.Name
}

// validateOp surfaces early errors before any I/O.
func validateOp(op *Op) error {
	if op.Source == "" {
		return errors.New("source is required")
	}
	if op.Name == "" {
		return errors.New("name is required")
	}
	switch op.Action {
	case ActionCopy, ActionMove:
	default:
		return fmt.Errorf("invalid action %q", op.Action)
	}
	switch op.Conflict {
	case ConflictSkip, ConflictOverwrite:
	default:
		return fmt.Errorf("invalid conflict policy %q", op.Conflict)
	}
	return nil
}

// translateEntry maps a Claude Code config entry into a
// models.ServerConfig the daemon's lifecycle Manager understands.
//
// override (when non-nil) replaces the translated value entirely; the
// resolved-command resolution is still attempted on override.Command
// for visibility in the response.
func translateEntry(entry claudeconfig.EntryRaw, override *models.ServerConfig) (*models.ServerConfig, CommandResolution, error) {
	if override != nil {
		// Defensive copy — handing back the caller's pointer would
		// let a later mutation by the caller corrupt the value the
		// daemon stored via Adder (F-04 fix).
		copied := *override
		// Slices and maps still alias — but the caller-mutation
		// scenario the audit caught is replacing whole-struct
		// fields (Command/URL); the slice/map aliasing is bounded
		// by lifecycle.Manager which deep-copies on AddServer.
		return &copied, ResolveCommand(copied.Command), nil
	}

	// Decode the raw entry into a permissive struct.
	var perm struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Cwd     string            `json:"cwd"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(entry.Raw, &perm); err != nil {
		return nil, CommandResolution{}, fmt.Errorf("decode entry: %w", err)
	}

	resolution := CommandResolution{}
	sc := &models.ServerConfig{}

	switch {
	case perm.Command != "":
		// stdio entry.
		resolution = ResolveCommand(perm.Command)
		if resolution.Resolved {
			sc.Command = resolution.AbsPath
		} else {
			sc.Command = perm.Command
		}
		sc.Args = append([]string(nil), perm.Args...)
		sc.Cwd = perm.Cwd
		sc.Env = mapToEnvSlice(perm.Env)
	case perm.URL != "":
		// http / sse entry.
		sc.URL = perm.URL
		sc.Headers = copyStringMap(perm.Headers)
	default:
		// Empty entry — caller may still want to apply via override.
		// Without override this is invalid; surface the error.
		return nil, CommandResolution{}, errors.New("entry has neither command nor url")
	}
	return sc, resolution, nil
}

// mapToEnvSlice flattens map[string]string into the daemon's
// ServerConfig.Env shape ([]string with KEY=VAL entries).
//
// Output is sorted by key for deterministic round-trips.
func mapToEnvSlice(in map[string]string) []string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(in))
	for _, k := range keys {
		out = append(out, k+"="+in[k])
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// mutateSourceRemove performs the move-semantics "delete from source"
// step: read the source, splice mcpServers without the named entry,
// atomic-rename write. Acquires an in-process lock on the source
// file path; mtime-CAS is performed by the underlying readFileWith…
// helper plus an explicit pre-write mtime check here.
//
// projectRoot must be the same workspace path the original snapshot
// was read with (only used for SourceCCProject; ignored otherwise).
// Threading it through is the F-01 fix — without it cc_project moves
// always fail at the re-read step with ErrEmptyProjectRoot.
//
// deps.BeforeSourceWrite/AfterSourceWrite provide the T-D.4 hook
// where the TS-side claudeConfigSync reflector can inhibit its own
// write-back during the critical section.
func mutateSourceRemove(snap *claudeconfig.Snapshot, name, projectRoot string, deps Dependencies) error {
	if !snap.Exists {
		return errors.New("source not present")
	}
	lock := lockSource(snap.Path)
	lock.mu.Lock()
	defer func() {
		lock.mu.Unlock()
		releaseSourceLock(snap.Path, lock)
	}()

	// Re-read under the lock to defeat a TOCTOU between Apply's
	// initial read and the write below.
	fresh, err := claudeconfig.Read(snap.Source, projectRoot)
	if err != nil {
		return fmt.Errorf("re-read for write: %w", err)
	}

	rr, err := claudeconfig.ParseRawRoot(fresh.Raw)
	if err != nil {
		return fmt.Errorf("parse for write: %w", err)
	}
	// Build new mcpServers value without the named entry.
	var existing map[string]json.RawMessage
	if rr.HasMcpServers() {
		if err := json.Unmarshal(rr.McpServersBytes(), &existing); err != nil {
			return fmt.Errorf("decode existing mcpServers: %w", err)
		}
	} else {
		existing = map[string]json.RawMessage{}
	}
	if _, present := existing[name]; !present {
		// Already gone — treat as success (idempotent).
		return nil
	}
	delete(existing, name)
	newServers, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal new mcpServers: %w", err)
	}

	out, err := rr.ReplaceMcpServers(newServers)
	if err != nil {
		return fmt.Errorf("splice mcpServers: %w", err)
	}

	// T-D.4 reflector pause hook — inhibit the TS sync between read
	// and write so a reflector tick does not write back the deleted
	// entry. The hook is best-effort: a missing one falls through
	// to the existing CAS protection in the reflector.
	if deps.BeforeSourceWrite != nil {
		deps.BeforeSourceWrite(snap.Path)
	}
	defer func() {
		if deps.AfterSourceWrite != nil {
			deps.AfterSourceWrite(snap.Path, true)
		}
	}()

	// Atomic write: tempfile in same directory + rename.
	dir := filepath.Dir(snap.Path)
	tmp, err := os.CreateTemp(dir, ".claude-config.*.tmp")
	if err != nil {
		return fmt.Errorf("CreateTemp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}

	// mtime CAS check: if the source file's mtime changed between
	// our re-read and now, abort the write — another writer beat us
	// and our spliced bytes would lose their work. The atomic-rename
	// step is where we'd be racing the most.
	st, err := os.Stat(snap.Path)
	if err == nil && st.ModTime().UnixNano() != fresh.ModTimeNS {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("mtime changed after re-read; abort write")
	}

	if err := os.Rename(tmpPath, snap.Path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

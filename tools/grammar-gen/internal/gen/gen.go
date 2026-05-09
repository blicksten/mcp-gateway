// Package gen renders the Go and TypeScript SAP server-name parsers from
// the YAML grammar source-of-truth.
//
// It is internal because both the codegen entrypoint (./tools/grammar-gen)
// and the staleness checker (./tools/grammar-gen/check) consume it; no
// other package should import this.
package gen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// Paths describes where the generator reads and writes. Resolved by
// DefaultPaths() to absolute paths anchored at the repo root.
type Paths struct {
	GrammarYAML string // input
	GoOut       string // output: generated Go file
	TSOut       string // output: generated TS file
	GoTmpl      string // input: Go template
	TSTmpl      string // input: TS template
}

// DefaultPaths returns the canonical repo-relative paths resolved against
// the location of this source file. Computing from runtime.Caller keeps
// `go run ./tools/grammar-gen` working from any CWD inside the repo.
func DefaultPaths() Paths {
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile = <repo>/tools/grammar-gen/internal/gen/gen.go
	// repoRoot = four levels up.
	repoRoot := filepath.Clean(filepath.Join(thisFile, "..", "..", "..", "..", ".."))
	tmplDir := filepath.Join(repoRoot, "tools", "grammar-gen", "templates")
	return Paths{
		GrammarYAML: filepath.Join(repoRoot, "docs", "grammar", "sap-server-name.yaml"),
		GoOut:       filepath.Join(repoRoot, "internal", "sapname", "grammar_gen.go"),
		TSOut:       filepath.Join(repoRoot, "vscode", "mcp-gateway-dashboard", "src", "sap-name-grammar.gen.ts"),
		GoTmpl:      filepath.Join(tmplDir, "grammar_gen.go.tmpl"),
		TSTmpl:      filepath.Join(tmplDir, "sap-name-grammar.gen.ts.tmpl"),
	}
}

// Generate reads the YAML at p.GrammarYAML, renders both parsers, and
// writes them to p.GoOut / p.TSOut. When dryRun is true, no files are
// written; the function still validates the templates by rendering them
// in memory and returns an error on any rendering failure.
func Generate(p Paths, dryRun bool) error {
	rendered, err := Render(p)
	if err != nil {
		return err
	}
	if dryRun {
		fmt.Fprintf(os.Stderr, "would write %s (%d bytes)\n", p.GoOut, len(rendered.Go))
		fmt.Fprintf(os.Stderr, "would write %s (%d bytes)\n", p.TSOut, len(rendered.TS))
		return nil
	}
	if err := writeIfChanged(p.GoOut, rendered.Go); err != nil {
		return fmt.Errorf("write go: %w", err)
	}
	if err := writeIfChanged(p.TSOut, rendered.TS); err != nil {
		return fmt.Errorf("write ts: %w", err)
	}
	return nil
}

// Rendered holds the byte payload that would be written to each output.
// Used by the staleness checker to compare against on-disk content
// without touching the filesystem.
type Rendered struct {
	Go []byte
	TS []byte
}

// Render returns the byte payload for the Go and TS parsers without
// writing anything. Errors propagate unchanged so callers (Generate +
// the check tool) can wrap them with their own context.
func Render(p Paths) (Rendered, error) {
	yamlBytes, err := os.ReadFile(p.GrammarYAML)
	if err != nil {
		return Rendered{}, fmt.Errorf("read grammar yaml: %w", err)
	}
	var raw rawGrammar
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return Rendered{}, fmt.Errorf("parse grammar yaml: %w", err)
	}
	model, err := raw.toModel()
	if err != nil {
		return Rendered{}, fmt.Errorf("validate grammar: %w", err)
	}

	goTmpl, err := template.ParseFiles(p.GoTmpl)
	if err != nil {
		return Rendered{}, fmt.Errorf("parse go template: %w", err)
	}
	tsTmpl, err := template.ParseFiles(p.TSTmpl)
	if err != nil {
		return Rendered{}, fmt.Errorf("parse ts template: %w", err)
	}

	var goBuf, tsBuf bytes.Buffer
	if err := goTmpl.Execute(&goBuf, model); err != nil {
		return Rendered{}, fmt.Errorf("render go: %w", err)
	}
	if err := tsTmpl.Execute(&tsBuf, model); err != nil {
		return Rendered{}, fmt.Errorf("render ts: %w", err)
	}

	// Run the Go output through go/format so the generator's output is
	// byte-identical to `gofmt -w` over the same content. Without this,
	// the staleness check would report STALE every time a developer ran
	// gofmt on the generated file (gofmt's spacing rules differ from a
	// raw template's).
	formatted, err := format.Source(goBuf.Bytes())
	if err != nil {
		return Rendered{}, fmt.Errorf("gofmt go: %w (rendered output:\n%s)", err, goBuf.String())
	}
	return Rendered{Go: formatted, TS: tsBuf.Bytes()}, nil
}

// writeIfChanged writes b to path only when the existing content differs.
// Skipping no-op writes keeps mtimes stable, which matters for build
// caches and IDE file-change watchers.
func writeIfChanged(path string, b []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, b) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// rawGrammar mirrors the YAML schema. Public fields exist solely to be
// populated by yaml.Unmarshal; consumers should call toModel() instead.
type rawGrammar struct {
	Version int          `yaml:"version"`
	Kinds   []rawKind    `yaml:"kinds"`
	SID     rawCharField `yaml:"sid"`
	Client  rawCharField `yaml:"client"`
}

type rawKind struct {
	Name   string `yaml:"name"`
	Prefix string `yaml:"prefix"`
}

type rawCharField struct {
	Length    int             `yaml:"length"`
	Charset   []rawCharsetRng `yaml:"charset"`
	Separator string          `yaml:"separator,omitempty"`
}

type rawCharsetRng struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// toModel validates the raw YAML and produces the template-friendly
// model. Validation is intentionally strict so a malformed YAML produces
// a fail-loud diagnostic at codegen time, not a silently broken parser.
func (r rawGrammar) toModel() (templateModel, error) {
	if r.Version != 1 {
		return templateModel{}, fmt.Errorf("unsupported grammar version: %d (only 1 is supported)", r.Version)
	}
	if len(r.Kinds) == 0 {
		return templateModel{}, fmt.Errorf("grammar must declare at least one kind")
	}

	kinds := make([]kindModel, 0, len(r.Kinds))
	for _, k := range r.Kinds {
		if k.Name == "" || k.Prefix == "" {
			return templateModel{}, fmt.Errorf("kind requires non-empty name and prefix (got %+v)", k)
		}
		kinds = append(kinds, kindModel{
			Name:      k.Name,
			Prefix:    k.Prefix,
			PrefixLen: len(k.Prefix),
			GoIdent:   toGoIdent(k.Name),
			JsIdent:   toJsIdent(k.Name),
			JsConst:   toJsConst(k.Name),
		})
	}

	// kindsLongestFirst is used in the prefix-dispatch chain so that e.g.
	// "sap-gui-" is checked before "sap-" (if such a prefix were ever
	// added). With the current grammar both prefixes are mutually
	// exclusive in practice, but the longest-first ordering removes the
	// ordering hazard for future grammar edits.
	longestFirst := make([]kindModel, len(kinds))
	copy(longestFirst, kinds)
	sort.SliceStable(longestFirst, func(i, j int) bool {
		return len(longestFirst[i].Prefix) > len(longestFirst[j].Prefix)
	})

	sid, err := buildCharFieldModel("sid", r.SID, false)
	if err != nil {
		return templateModel{}, err
	}
	cli, err := buildCharFieldModel("client", r.Client, true)
	if err != nil {
		return templateModel{}, err
	}

	return templateModel{
		Version:           r.Version,
		Kinds:             kinds,
		KindsLongestFirst: longestFirst,
		Sid:               sid,
		Client:            cli,
	}, nil
}

func buildCharFieldModel(name string, raw rawCharField, requireSeparator bool) (charFieldModel, error) {
	if raw.Length <= 0 {
		return charFieldModel{}, fmt.Errorf("%s length must be > 0", name)
	}
	if len(raw.Charset) == 0 {
		return charFieldModel{}, fmt.Errorf("%s charset must declare at least one range", name)
	}
	if requireSeparator && raw.Separator == "" {
		return charFieldModel{}, fmt.Errorf("%s separator is required", name)
	}

	var goRanges, tsRanges []string
	var descParts []string
	for _, r := range raw.Charset {
		if len(r.From) != 1 || len(r.To) != 1 {
			return charFieldModel{}, fmt.Errorf("%s charset range %q-%q must use single-byte ASCII bounds", name, r.From, r.To)
		}
		if r.From > r.To {
			return charFieldModel{}, fmt.Errorf("%s charset range from=%q > to=%q", name, r.From, r.To)
		}
		goRanges = append(goRanges, fmt.Sprintf("(c >= '%s' && c <= '%s')", goEscapeByte(r.From[0]), goEscapeByte(r.To[0])))
		tsRanges = append(tsRanges, fmt.Sprintf("(c >= %d && c <= %d)", r.From[0], r.To[0]))
		descParts = append(descParts, fmt.Sprintf("%s-%s", r.From, r.To))
	}

	return charFieldModel{
		Length:             raw.Length,
		Separator:          raw.Separator,
		CharCheckExpr:      strings.Join(goRanges, " || "),
		TsCharCheckExpr:    strings.Join(tsRanges, " || "),
		CharsetDescription: strings.Join(descParts, ", "),
	}, nil
}

// goEscapeByte renders b as a Go rune literal body. The grammar's
// charset is ASCII-only so we cover the small set of bytes that need
// escaping inside a single-quoted Go literal: \ and ' (the alphanumeric
// ranges in our grammar do not include either, but defensive escaping
// keeps the codegen safe if the YAML grows).
func goEscapeByte(b byte) string {
	switch b {
	case '\\':
		return `\\`
	case '\'':
		return `\'`
	}
	return string([]byte{b})
}

// toGoIdent maps a kind name like "sap-gui" to the Go identifier
// "SAPGUI" used in generated constants and helper functions.
// "vsp" → "VSP".
func toGoIdent(name string) string {
	parts := strings.Split(name, "-")
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(strings.ToUpper(p))
	}
	return b.String()
}

// toJsIdent maps "sap-gui" → "SAPGUI" for the camel-style helper names
// like isSAPGUI in the generated TS file. Same logic as toGoIdent today
// but kept as a separate function so the two output styles can diverge
// later (e.g. if Go wanted PascalCase and TS wanted camelCase).
func toJsIdent(name string) string {
	return toGoIdent(name)
}

// toJsConst maps "sap-gui" → "SAP_GUI" for the SCREAMING_SNAKE_CASE
// constant identifier (KIND_VSP, KIND_SAP_GUI).
func toJsConst(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// templateModel is the data root passed to both templates.
type templateModel struct {
	Version           int
	Kinds             []kindModel
	KindsLongestFirst []kindModel
	Sid               charFieldModel
	Client            charFieldModel
}

type kindModel struct {
	Name      string // wire identifier, e.g. "vsp"
	Prefix    string // string prefix that signals this kind
	PrefixLen int    // byte length of Prefix
	GoIdent   string // Go identifier suffix, e.g. "VSP", "SAPGUI"
	JsIdent   string // TS function-name suffix, e.g. "VSP", "SAPGUI"
	JsConst   string // TS constant name, e.g. "VSP", "SAP_GUI"
}

type charFieldModel struct {
	Length             int
	Separator          string // "" for SID
	CharCheckExpr      string // Go expression evaluating the range check on `c byte`
	TsCharCheckExpr    string // TS expression evaluating the range check on `c` (charCode)
	CharsetDescription string // human-readable, e.g. "A-Z, 0-9"
}


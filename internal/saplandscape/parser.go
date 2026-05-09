// Package saplandscape parses SAPUILandscape.xml files into a flat list of
// SAP service entries. It handles recursive <Include> chains, URL
// normalisation, and cycle detection without using any regular expressions.
package saplandscape

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxIncludeDepth = 8

// ParseError is returned when the XML in a landscape file is syntactically
// invalid. It carries the file path and the byte offset (from xml.SyntaxError).
type ParseError struct {
	Path   string
	Offset int64
	Err    error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error in %q at offset %d: %v", e.Path, e.Offset, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// Service represents a single <Service> element inside a landscape file.
type Service struct {
	Name   string `xml:"name,attr"`
	SID    string `xml:"systemid,attr"`
	Client string `xml:"client,attr"`
	Type   string `xml:"type,attr"`
	Server string `xml:"server,attr"`
}

// Landscape is the aggregated result of parsing one or more landscape files.
type Landscape struct {
	Services []Service
	Warnings []string
}

// xmlWorkspace mirrors the top-level <Workspace> element.
type xmlWorkspace struct {
	XMLName  xml.Name     `xml:"Workspace"`
	Services []xmlService `xml:"Services>Service"`
	Includes []xmlInclude `xml:"Includes>Include"`
}

type xmlService struct {
	Name   string `xml:"name,attr"`
	SID    string `xml:"systemid,attr"`
	Client string `xml:"client,attr"`
	Type   string `xml:"type,attr"`
	Server string `xml:"server,attr"`
}

type xmlInclude struct {
	URL string `xml:"url,attr"`
}

// Parse opens the file at path and parses the landscape XML.
func Parse(path string) (*Landscape, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path %q: %w", path, err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open landscape file: %w", err)
	}
	defer f.Close()
	return ParseReader(f, abs)
}

// ParseReader parses XML from r, resolving relative <Include> URLs relative
// to basePath (which must be the absolute path of the file r was opened from).
func ParseReader(r io.Reader, basePath string) (*Landscape, error) {
	result := &Landscape{}
	visited := make(map[string]struct{})
	visited[basePath] = struct{}{}

	ws, err := decodeWorkspace(r, basePath)
	if err != nil {
		return nil, err
	}

	for _, xs := range ws.Services {
		result.Services = append(result.Services, xmlServiceToService(xs))
	}

	baseDir := filepath.Dir(basePath)
	for _, inc := range ws.Includes {
		parseInclude(result, inc.URL, baseDir, visited, 1)
	}

	return result, nil
}

// decodeWorkspace decodes the XML stream into xmlWorkspace. Returns
// *ParseError for syntax errors. io.EOF on empty input is intentionally
// mapped to a zero-value workspace + nil error — empty landscape == zero
// services is the documented contract (F-06 — TestParse_EmptyFile pins
// this behaviour).
func decodeWorkspace(r io.Reader, filePath string) (*xmlWorkspace, error) {
	var ws xmlWorkspace
	dec := xml.NewDecoder(r)
	if err := dec.Decode(&ws); err != nil {
		if synErr, ok := err.(*xml.SyntaxError); ok {
			return nil, &ParseError{Path: filePath, Offset: int64(synErr.Line), Err: synErr}
		}
		if err != io.EOF {
			return nil, &ParseError{Path: filePath, Offset: 0, Err: err}
		}
	}
	return &ws, nil
}

// parseInclude resolves and parses a single <Include> URL recursively.
// Errors (missing file, cycle, depth limit) are recorded as Warnings on
// result — the root file's services are always returned.
func parseInclude(result *Landscape, rawURL, baseDir string, visited map[string]struct{}, depth int) {
	if depth >= maxIncludeDepth {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("include depth limit (%d) exceeded for URL %q — skipping", maxIncludeDepth, rawURL))
		return
	}

	normalised := normalizeURL(rawURL)
	resolved := resolveIncludePath(normalised, baseDir)

	abs, err := filepath.Abs(resolved)
	if err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("cannot resolve include %q: %v", rawURL, err))
		return
	}

	if _, seen := visited[abs]; seen {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("cycle detected: %q has already been visited — skipping", abs))
		return
	}
	visited[abs] = struct{}{}

	f, err := os.Open(abs)
	if err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("cannot open include %q: %v", abs, err))
		return
	}
	defer f.Close()

	ws, err := decodeWorkspace(f, abs)
	if err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("cannot parse include %q: %v", abs, err))
		return
	}

	for _, xs := range ws.Services {
		result.Services = append(result.Services, xmlServiceToService(xs))
	}

	incBaseDir := filepath.Dir(abs)
	for _, inc := range ws.Includes {
		parseInclude(result, inc.URL, incBaseDir, visited, depth+1)
	}
}

// resolveIncludePath resolves an include URL/path relative to baseDir.
// UNC paths and absolute paths are returned unchanged.
func resolveIncludePath(normalised, baseDir string) string {
	if filepath.IsAbs(normalised) {
		return normalised
	}
	if strings.HasPrefix(normalised, `\\`) {
		return normalised
	}
	return filepath.Join(baseDir, normalised)
}

// normalizeURL converts landscape URL forms to local filesystem paths.
//
// Rules (in order, no regex):
//  1. %VAR% Windows-style env vars → expand via os.ExpandEnv
//  2. file:///C:/path → C:\path
//  3. file://server/share/… → \\server\share\…
//  4. \\?\ long-path prefix → returned as-is
//  5. Anything else returned as-is (relative paths, plain paths)
func normalizeURL(u string) string {
	// Step 1: expand %VAR% by rewriting to ${VAR} first (os.ExpandEnv only
	// handles $VAR / ${VAR} syntax, not Windows %VAR% syntax).
	u = expandWindowsEnvVars(u)
	u = os.ExpandEnv(u)

	// Step 4: \\?\ long-path passthrough — must be checked before other
	// prefix checks so we don't accidentally strip the prefix.
	if strings.HasPrefix(u, `\\?\`) {
		return u
	}

	// Step 2: file:///C:/path (local file with drive letter).
	// file:/// has three slashes; the fourth character starts the drive letter.
	if strings.HasPrefix(u, "file:///") {
		rest := u[len("file:///"):]
		// rest is e.g. "C:/path/to/file.xml" — convert forward slashes to backslashes.
		return strings.ReplaceAll(rest, "/", `\`)
	}

	// Step 3: file://server/share/path (UNC).
	if strings.HasPrefix(u, "file://") {
		rest := u[len("file://"):]
		return `\\` + strings.ReplaceAll(rest, "/", `\`)
	}

	return u
}

// expandWindowsEnvVars rewrites %VAR% tokens to ${VAR} so that
// os.ExpandEnv can process them. Written without regex using index scanning.
func expandWindowsEnvVars(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '%' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// Found opening '%' — scan for closing '%'.
		j := i + 1
		for j < len(s) && s[j] != '%' {
			j++
		}
		if j >= len(s) {
			// No closing '%' — emit literally.
			b.WriteByte('%')
			i++
			continue
		}
		// Found a matching pair: s[i]='%', s[j]='%', token is s[i+1:j].
		varName := s[i+1 : j]
		if len(varName) == 0 {
			// %% — emit a literal percent.
			b.WriteByte('%')
			i = j + 1
			continue
		}
		b.WriteString("${")
		b.WriteString(varName)
		b.WriteString("}")
		i = j + 1
	}
	return b.String()
}

// xmlServiceToService converts the XML struct to the exported Service type.
func xmlServiceToService(xs xmlService) Service {
	return Service{
		Name:   xs.Name,
		SID:    xs.SID,
		Client: xs.Client,
		Type:   xs.Type,
		Server: xs.Server,
	}
}

// isValidSID returns true when s is a valid 3-character SAP SID consisting
// only of uppercase letters (A-Z) and digits (0-9). No regex — charcode
// range comparisons only.
func isValidSID(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		c := s[i]
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// Package claudeconfig provides readers and a top-level-preserving raw-bytes
// splicer for Claude Code's three configuration sources:
//
//   - cc_global  — ~/.claude.json (top-level mcpServers + many other keys)
//   - cc_project — <workspace>/.mcp.json (mcpServers only, but co-tenanted)
//   - desktop    — Claude Desktop's claude_desktop_config.json
//
// RawRoot is the splicer. It finds the byte range of the top-level
// "mcpServers" value in a JSON document, lets a caller mutate just that
// value, and produces a new document where every byte outside the
// mcpServers value is identical to the input. This preserves:
//
//   - top-level field order (Claude Code orders keys lexically by insertion;
//     re-encoding via the standard library would alphabetise them);
//   - whitespace and indentation between unrelated fields;
//   - unrecognised top-level fields (cachedGrowthBookFeatures, projects,
//     oauthAccount, customApiKeyResponses, ...) — preserved byte-identical;
//   - duplicate keys at the top level — rejected with ErrDuplicateKey
//     (per the T-D.0 PoC decision: no ambiguity tolerated).
//
// PoC decision (docs/spikes/2026-05-08-rawroot-poc.md, R-02):
// raw-bytes splice is preferred over iancoleman/orderedmap because:
//  1. zero new dependency,
//  2. preserves whitespace verbatim (orderedmap re-encodes with default
//     indent and would change every byte),
//  3. handles unrecognised top-level fields by NOT looking inside them.
package claudeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrDuplicateKey signals two top-level "mcpServers" keys in the input.
// This is rejected because either could be authoritative and the splicer
// would silently drop one — that ambiguity is a bug-on-disk we surface
// as an error rather than masking.
var ErrDuplicateKey = errors.New("claudeconfig: duplicate top-level mcpServers key")

// ErrNotObject signals the top-level JSON value is not an object. The
// splicer only operates on object roots — arrays / scalars are out of
// scope for Claude Code config files.
var ErrNotObject = errors.New("claudeconfig: top-level value is not an object")

// RawRoot wraps the original file bytes plus the byte range of the
// top-level "mcpServers" value, if any.
//
// Use ReadRawRoot to construct one from an io.Reader. Use ReplaceMcpServers
// to splice in a new mcpServers value while preserving the rest of the
// document byte-identically. Use Bytes to get the spliced output.
type RawRoot struct {
	src []byte

	// mcpServersStart..mcpServersEnd is the byte range of the value
	// associated with the top-level "mcpServers" key.
	//
	// When the key is absent, both are -1 and ReplaceMcpServers will
	// inject a fresh "mcpServers": <value> entry just before the
	// closing brace of the root object.
	mcpServersStart int
	mcpServersEnd   int

	// rootCloseAt is the byte offset of the root object's closing '}'.
	rootCloseAt int
}

// ReadRawRoot scans a JSON document, locates the top-level "mcpServers"
// value's byte range, and returns a RawRoot ready for splice.
//
// Scanning is byte-level (no encoding/json.Decoder coupling) so we get
// exact byte offsets and can preserve whitespace verbatim.
//
// On a non-object root, ErrNotObject is returned. On duplicate top-level
// "mcpServers" keys, ErrDuplicateKey. Other parse errors are wrapped.
func ReadRawRoot(r io.Reader) (*RawRoot, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("claudeconfig: read: %w", err)
	}
	return ParseRawRoot(src)
}

// ParseRawRoot is the byte-slice form of ReadRawRoot. Useful when callers
// already have the file bytes (e.g. from an mtime-CAS read loop).
func ParseRawRoot(src []byte) (*RawRoot, error) {
	rr := &RawRoot{
		src:             append([]byte(nil), src...),
		mcpServersStart: -1,
		mcpServersEnd:   -1,
		rootCloseAt:     -1,
	}

	// Validate input parses as JSON; gives us a typed error for
	// malformed documents instead of silent wrong-offset behaviour.
	if !json.Valid(src) {
		return nil, errors.New("claudeconfig: input is not valid JSON")
	}

	// Find the root object's '{'. Skip leading whitespace.
	i := skipSpace(src, 0)
	if i >= len(src) || src[i] != '{' {
		return nil, ErrNotObject
	}
	rootOpen := i
	// rootEnd = "one byte past root close brace" (scanContainerEnd's
	// return contract). rootCloseAt = byte index OF the '}' itself.
	// Renamed from prior `rootClose` to disambiguate the two semantics
	// (F-05 fix — was confusingly named).
	rootEnd := scanContainerEnd(src, rootOpen)
	if rootEnd <= rootOpen {
		return nil, errors.New("claudeconfig: unterminated root object")
	}
	rr.rootCloseAt = rootEnd - 1
	rootCloseIdx := rr.rootCloseAt

	// Walk top-level keys. The body lies in (rootOpen, rootCloseIdx).
	i = rootOpen + 1
	for i < rootCloseIdx {
		i = skipSpace(src, i)
		if i >= rootCloseIdx {
			break
		}
		// Optional trailing comma between members.
		if src[i] == ',' {
			i++
			continue
		}
		if src[i] != '"' {
			return nil, fmt.Errorf("claudeconfig: expected key at offset %d, got %q", i, src[i])
		}
		keyStart := i
		keyEnd := scanStringEnd(src, keyStart)
		if keyEnd <= keyStart {
			return nil, fmt.Errorf("claudeconfig: malformed key at offset %d", keyStart)
		}
		// keyStart..keyEnd contains the quoted string. Decode via
		// json.Unmarshal to interpret escapes consistently.
		var key string
		if err := json.Unmarshal(src[keyStart:keyEnd], &key); err != nil {
			return nil, fmt.Errorf("claudeconfig: decode key at offset %d: %w", keyStart, err)
		}

		// Advance past key, optional whitespace, ':', whitespace.
		i = keyEnd
		i = skipSpace(src, i)
		if i >= rootCloseIdx || src[i] != ':' {
			return nil, fmt.Errorf("claudeconfig: expected colon after key %q", key)
		}
		i++
		i = skipSpace(src, i)
		if i >= rootCloseIdx {
			return nil, fmt.Errorf("claudeconfig: missing value for key %q", key)
		}

		valueStart := i
		valueEnd, err := scanValueEnd(src, valueStart)
		if err != nil {
			return nil, fmt.Errorf("claudeconfig: scan value for key %q: %w", key, err)
		}

		if key == "mcpServers" {
			if rr.mcpServersStart != -1 {
				return nil, ErrDuplicateKey
			}
			rr.mcpServersStart = valueStart
			rr.mcpServersEnd = valueEnd
		}

		i = valueEnd
	}

	return rr, nil
}

// HasMcpServers reports whether the source had a top-level "mcpServers"
// key. When false, ReplaceMcpServers will INSERT one before the root close.
func (rr *RawRoot) HasMcpServers() bool {
	return rr.mcpServersStart != -1
}

// McpServersBytes returns the raw bytes of the existing mcpServers value
// (slice into rr.src). nil when HasMcpServers is false.
//
// The returned slice aliases the internal buffer; callers MUST NOT modify
// it. Use append([]byte(nil), v...) if you need an owned copy.
func (rr *RawRoot) McpServersBytes() []byte {
	if rr.mcpServersStart == -1 {
		return nil
	}
	return rr.src[rr.mcpServersStart:rr.mcpServersEnd]
}

// Bytes returns a copy of the source bytes verbatim. Useful before any
// splice has been applied.
func (rr *RawRoot) Bytes() []byte {
	return append([]byte(nil), rr.src...)
}

// ReplaceMcpServers splices in a new mcpServers value, returning the new
// document bytes.
//
// newValue MUST be valid JSON (typically the marshalled form of a Go
// map[string]ServerConfig or json.RawMessage). It is inserted verbatim,
// so caller-controlled indentation is preserved.
//
// When HasMcpServers is true: the existing value's byte range is replaced.
// When HasMcpServers is false: a new "mcpServers": <newValue> entry is
// inserted just before the root close, with a leading comma if the root
// has any other keys.
//
// rr is not mutated; this returns a fresh slice.
func (rr *RawRoot) ReplaceMcpServers(newValue []byte) ([]byte, error) {
	if !json.Valid(newValue) {
		return nil, errors.New("claudeconfig: newValue is not valid JSON")
	}

	if rr.mcpServersStart != -1 {
		out := make([]byte, 0, len(rr.src)+len(newValue))
		out = append(out, rr.src[:rr.mcpServersStart]...)
		out = append(out, newValue...)
		out = append(out, rr.src[rr.mcpServersEnd:]...)
		return out, nil
	}

	close := rr.rootCloseAt
	if close == -1 {
		return nil, errors.New("claudeconfig: root close offset unknown")
	}
	last := close - 1
	for last >= 0 && isJSONSpace(rr.src[last]) {
		last--
	}
	rootEmpty := last >= 0 && rr.src[last] == '{'

	insert := make([]byte, 0, 32+len(newValue))
	if !rootEmpty {
		insert = append(insert, ',')
	}
	insert = append(insert, '"', 'm', 'c', 'p', 'S', 'e', 'r', 'v', 'e', 'r', 's', '"', ':', ' ')
	insert = append(insert, newValue...)

	out := make([]byte, 0, len(rr.src)+len(insert))
	out = append(out, rr.src[:close]...)
	out = append(out, insert...)
	out = append(out, rr.src[close:]...)
	return out, nil
}

// scanValueEnd scans src starting at valueStart (which must point at the
// first byte of a JSON value) and returns the byte offset just past the
// value's last byte.
//
// Handles all JSON value types: object, array, string, number, true,
// false, null. Tracks string escapes and nested containers. No regex.
func scanValueEnd(src []byte, valueStart int) (int, error) {
	if valueStart >= len(src) {
		return 0, errors.New("claudeconfig: value start past end of input")
	}
	switch src[valueStart] {
	case '{', '[':
		return scanContainerEnd(src, valueStart), nil
	case '"':
		return scanStringEnd(src, valueStart), nil
	case 't':
		return matchLiteral(src, valueStart, "true")
	case 'f':
		return matchLiteral(src, valueStart, "false")
	case 'n':
		return matchLiteral(src, valueStart, "null")
	}
	// number
	return scanNumberEnd(src, valueStart), nil
}

// scanContainerEnd scans an object or array starting at src[start] (which
// must be '{' or '[') and returns the offset just past the matching close.
//
// Strings inside the container are skipped via scanStringEnd to avoid
// counting braces/brackets inside string literals.
func scanContainerEnd(src []byte, start int) int {
	open := src[start]
	var close byte
	if open == '{' {
		close = '}'
	} else {
		close = ']'
	}
	depth := 0
	i := start
	for i < len(src) {
		c := src[i]
		switch c {
		case '"':
			i = scanStringEnd(src, i)
			continue
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return i
}

// scanStringEnd assumes src[start] == '"' and returns the offset just past
// the closing quote, accounting for backslash escapes.
func scanStringEnd(src []byte, start int) int {
	i := start + 1
	for i < len(src) {
		c := src[i]
		if c == '\\' {
			i += 2
			continue
		}
		if c == '"' {
			return i + 1
		}
		i++
	}
	return i
}

// scanNumberEnd scans a JSON number starting at start. Numbers are: an
// optional minus, then digits, optional '.digits', optional 'eN' / 'EN'
// with sign. We accept the chars JSON permits and stop at the first
// non-number char.
func scanNumberEnd(src []byte, start int) int {
	i := start
	if i < len(src) && src[i] == '-' {
		i++
	}
	for i < len(src) {
		c := src[i]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			i++
			continue
		}
		break
	}
	return i
}

// matchLiteral verifies src[start:start+len(lit)] == lit and returns the
// offset just past it.
func matchLiteral(src []byte, start int, lit string) (int, error) {
	end := start + len(lit)
	if end > len(src) {
		return 0, fmt.Errorf("claudeconfig: truncated literal %q", lit)
	}
	if string(src[start:end]) != lit {
		return 0, fmt.Errorf("claudeconfig: expected literal %q", lit)
	}
	return end, nil
}

// skipSpace returns the next index >= off where src[i] is not JSON
// whitespace.
func skipSpace(src []byte, off int) int {
	for off < len(src) && isJSONSpace(src[off]) {
		off++
	}
	return off
}

// isJSONSpace reports whether c is a whitespace byte recognised by JSON.
func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

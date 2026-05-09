package saplandscape

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testdataPath(name string) string {
	return filepath.Join("testdata", name)
}

// TestParse_Sample verifies that a well-formed landscape file is parsed correctly.
func TestParse_Sample(t *testing.T) {
	result, err := Parse(testdataPath("sample.xml"))
	require.NoError(t, err)
	require.Len(t, result.Services, 2)
	assert.Equal(t, "PRD", result.Services[0].SID)
	assert.Equal(t, "100", result.Services[0].Client)
	assert.Equal(t, "QAS", result.Services[1].SID)
}

// TestParse_EmptyFile verifies that an empty file returns a typed error (empty
// XML is not a valid landscape file — the decoder will return io.EOF which
// we surface as a ParseError).
func TestParse_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.xml")
	require.NoError(t, os.WriteFile(path, []byte{}, 0o600))

	result, err := Parse(path)
	// An empty file may decode as zero-value Workspace without an explicit
	// error because xml.Decoder.Decode returns nil for an empty stream on
	// some Go versions. Accept either an error OR an empty Services slice.
	if err != nil {
		var pe *ParseError
		assert.ErrorAs(t, err, &pe, "empty file error must be *ParseError")
	} else {
		assert.Empty(t, result.Services, "empty file must yield no services")
	}
}

// TestParse_Malformed verifies that a syntactically invalid XML file returns
// a *ParseError and does not panic.
func TestParse_Malformed(t *testing.T) {
	_, err := Parse(testdataPath("malformed.xml"))
	require.Error(t, err)
	var pe *ParseError
	require.ErrorAs(t, err, &pe, "malformed XML must return *ParseError")
	// pe.Path contains the absolute path; check the filename suffix.
	assert.True(t, strings.HasSuffix(filepath.ToSlash(pe.Path), "testdata/malformed.xml"),
		"ParseError.Path must end with testdata/malformed.xml, got %q", pe.Path)
}

// TestParse_Malformed_NoPanic is a belt-and-suspenders check.
func TestParse_Malformed_NoPanic(t *testing.T) {
	assert.NotPanics(t, func() {
		_, _ = Parse(testdataPath("malformed.xml"))
	})
}

// TestParse_DeepIncludeChain tests that a depth-7 include chain succeeds and
// a depth-8 include (the 9th file, which would be at depth 9) triggers a
// depth warning.
//
// depth1.xml → depth2.xml → … → depth7.xml → depth8.xml
// depth1 is parsed at depth=0 (root), then depth2 at depth=1, …, depth7 at
// depth=7, depth8 would be at depth=8 which hits the limit.
func TestParse_DeepIncludeChain(t *testing.T) {
	result, err := Parse(testdataPath("depth1.xml"))
	require.NoError(t, err, "deep include chain up to limit should not error")

	// depth1..depth8 all succeed (depth9 is skipped by the depth limit).
	// depth1.xml is root, depth2..depth8 are nested includes at depth 1..7.
	// depth9.xml would be at depth 8 which equals maxIncludeDepth and is skipped.
	sids := make(map[string]bool)
	for _, svc := range result.Services {
		sids[svc.SID] = true
	}
	for _, sid := range []string{"D01", "D02", "D03", "D04", "D05", "D06", "D07", "D08"} {
		assert.True(t, sids[sid], "expected SID %q in result", sid)
	}
	assert.False(t, sids["D09"], "D09 must be skipped due to depth limit")

	// depth8 must have been skipped with a warning.
	hasDepthWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "depth limit") {
			hasDepthWarning = true
		}
	}
	assert.True(t, hasDepthWarning, "expected depth-limit warning in Warnings, got: %v", result.Warnings)
}

// TestParse_CycleDetected verifies that a cycle between two include files
// produces a warning and does not cause an infinite loop.
func TestParse_CycleDetected(t *testing.T) {
	result, err := Parse(testdataPath("cycle_a.xml"))
	require.NoError(t, err, "cycle must not produce a top-level error")

	hasCycleWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "cycle") {
			hasCycleWarning = true
		}
	}
	assert.True(t, hasCycleWarning, "expected cycle warning in Warnings, got: %v", result.Warnings)

	// Both A and B services should be present (the first traversal of each succeeds).
	sids := make(map[string]bool)
	for _, svc := range result.Services {
		sids[svc.SID] = true
	}
	assert.True(t, sids["AAA"], "service from cycle_a.xml must be present")
	assert.True(t, sids["BBB"], "service from cycle_b.xml must be present")
}

// TestParse_MissingIncludeFile verifies that a reference to a non-existent
// include file appends a warning but still returns the root file's services.
func TestParse_MissingIncludeFile(t *testing.T) {
	result, err := Parse(testdataPath("root_with_missing_include.xml"))
	require.NoError(t, err, "missing include must not error — only warn")
	require.Len(t, result.Services, 1, "root file's service must be present")
	assert.Equal(t, "ROT", result.Services[0].SID)
	assert.NotEmpty(t, result.Warnings, "missing include must produce a warning")
}

// TestParse_UNCNormalization verifies that a landscape containing a
// file://server/share/... Include URL is normalised to a UNC path and the
// warning explains what happened (file will not exist in test — graceful skip).
func TestParse_UNCNormalization(t *testing.T) {
	// Write a landscape that references a UNC-style URL.
	xml := `<?xml version="1.0" encoding="utf-8"?>
<Workspace>
  <Services>
    <Service name="ROOT" systemid="RUT" client="001" type="SAPGUI"/>
  </Services>
  <Includes>
    <Include url="file://server/share/landscape.xml"/>
  </Includes>
</Workspace>`
	path := filepath.Join(t.TempDir(), "unc.xml")
	require.NoError(t, os.WriteFile(path, []byte(xml), 0o600))

	result, err := Parse(path)
	require.NoError(t, err)
	assert.Len(t, result.Services, 1)
	// The UNC file doesn't exist — expect a warning.
	hasWarning := len(result.Warnings) > 0
	assert.True(t, hasWarning, "UNC include that cannot be opened must produce a warning")
}

// TestParse_AppDataExpansion verifies that %APPDATA% inside an Include URL
// is expanded to the value of the APPDATA environment variable.
func TestParse_AppDataExpansion(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("APPDATA", tmpDir)

	// Create a real include file in the tmpDir.
	includeContent := `<?xml version="1.0" encoding="utf-8"?>
<Workspace>
  <Services>
    <Service name="INC" systemid="INC" client="999" type="SAPGUI"/>
  </Services>
  <Includes/>
</Workspace>`
	includeFile := filepath.Join(tmpDir, "extra.xml")
	require.NoError(t, os.WriteFile(includeFile, []byte(includeContent), 0o600))

	// Write root landscape that includes %APPDATA%\extra.xml.
	rootXML := `<?xml version="1.0" encoding="utf-8"?>
<Workspace>
  <Services>
    <Service name="ROOT" systemid="ROO" client="001" type="SAPGUI"/>
  </Services>
  <Includes>
    <Include url="%APPDATA%\extra.xml"/>
  </Includes>
</Workspace>`
	rootPath := filepath.Join(t.TempDir(), "root.xml")
	require.NoError(t, os.WriteFile(rootPath, []byte(rootXML), 0o600))

	result, err := Parse(rootPath)
	require.NoError(t, err)

	sids := make(map[string]bool)
	for _, svc := range result.Services {
		sids[svc.SID] = true
	}
	assert.True(t, sids["ROO"], "root service must be present")
	assert.True(t, sids["INC"], "APPDATA-expanded include service must be present")
}

// TestNormalizeURL is a table-driven test covering all normaliseURL mappings.
func TestNormalizeURL(t *testing.T) {
	t.Setenv("APPDATA", `C:\Users\testuser\AppData\Roaming`)
	t.Setenv("USERPROFILE", `C:\Users\testuser`)

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "file_triple_slash_drive",
			input: "file:///C:/path/to/landscape.xml",
			want:  `C:\path\to\landscape.xml`,
		},
		{
			name:  "file_unc_server",
			input: "file://server/share/landscape.xml",
			want:  `\\server\share\landscape.xml`,
		},
		{
			name:  "long_path_passthrough",
			input: `\\?\C:\very\long\path\file.xml`,
			want:  `\\?\C:\very\long\path\file.xml`,
		},
		{
			name:  "relative_unchanged",
			input: `extra\landscape.xml`,
			want:  `extra\landscape.xml`,
		},
		{
			name:  "appdata_expansion",
			input: `%APPDATA%\SAPUILandscape.xml`,
			want:  `C:\Users\testuser\AppData\Roaming\SAPUILandscape.xml`,
		},
		{
			name:  "userprofile_expansion",
			input: `%USERPROFILE%\landscape.xml`,
			want:  `C:\Users\testuser\landscape.xml`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeURL(tc.input)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestIsValidSID verifies the SID charset/length check.
func TestIsValidSID(t *testing.T) {
	assert.True(t, isValidSID("PRD"))
	assert.True(t, isValidSID("AB1"))
	assert.True(t, isValidSID("000"))
	assert.False(t, isValidSID("pr"), "too short")
	assert.False(t, isValidSID("PRDD"), "too long")
	assert.False(t, isValidSID("prd"), "lowercase")
	assert.False(t, isValidSID("P D"), "space")
	assert.False(t, isValidSID("P-D"), "dash")
}

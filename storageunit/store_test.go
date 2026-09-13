package storageunit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shape is a contract with every other suite program - and with a user
// editing the file by hand - so it is asserted literally rather than only
// round-tripped through this package's own parser.
func TestFileShape(t *testing.T) {
	m, root := newTestManager(t)
	first := addDir(t, m, root, "Fast SSD")
	addDir(t, m, root, "Archive NAS")

	body, err := os.ReadFile(m.File())
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	for _, want := range []string{
		"\"version\": 1",
		"\"storageUnits\": [",
		"\"id\": \"" + first.ID + "\"",
		"\"name\": \"Fast SSD\"",
		"\"path\": \"" + first.Path + "\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file is missing %s:\n%s", want, got)
		}
	}
	// Computed fields have no persisted representation at all.
	for _, forbidden := range []string{"freeBytes", "totalBytes", "unreachable"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("%s must never be serialized:\n%s", forbidden, got)
		}
	}
	if !strings.HasSuffix(got, "}\n") {
		t.Errorf("file should end with a newline:\n%q", got)
	}
	// Order in the array is priority order, so the first unit added must appear
	// before the second in the raw bytes.
	if strings.Index(got, "Fast SSD") > strings.Index(got, "Archive NAS") {
		t.Error("array order must be priority order")
	}
}

// The file's keyed body exists to leave room for future suite settings beside
// the list. That room is only real if a program that does not know about a key
// preserves it instead of dropping it on the next write.
func TestUnknownKeysArePreserved(t *testing.T) {
	dir := t.TempDir()
	existing := `{
  "version": 1,
  "storageUnits": [
    {
      "id": "aabbccddeeff",
      "name": "Archive NAS",
      "path": "/mnt/nas/MediaItems",
      "somethingNewer": {"nested": [1, 2, {"deep": true}]}
    }
  ],
  "futureSuiteSetting": {"enabled": true},
  "anotherKey": "kept"
}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Any mutation rewrites the whole file, which is when preservation matters.
	if err := m.Rename("aabbccddeeff", "Renamed"); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(m.File())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version      int `json:"version"`
		StorageUnits []struct {
			ID             string          `json:"id"`
			Name           string          `json:"name"`
			Path           string          `json:"path"`
			SomethingNewer json.RawMessage `json:"somethingNewer"`
		} `json:"storageUnits"`
		FutureSuiteSetting struct {
			Enabled bool `json:"enabled"`
		} `json:"futureSuiteSetting"`
		AnotherKey string `json:"anotherKey"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("rewritten file is not valid JSON: %v\n%s", err, body)
	}

	if !doc.FutureSuiteSetting.Enabled {
		t.Errorf("an unknown top-level key was dropped on write:\n%s", body)
	}
	if doc.AnotherKey != "kept" {
		t.Errorf("an unknown top-level key was dropped on write:\n%s", body)
	}
	if len(doc.StorageUnits) != 1 {
		t.Fatalf("unit count = %d, want 1", len(doc.StorageUnits))
	}
	u := doc.StorageUnits[0]
	if u.Name != "Renamed" || u.Path != "/mnt/nas/MediaItems" {
		t.Errorf("known fields wrong after rewrite: %+v", u)
	}
	var nested struct {
		Nested []json.RawMessage `json:"nested"`
	}
	if err := json.Unmarshal(u.SomethingNewer, &nested); err != nil {
		t.Fatalf("an unknown per-unit key was dropped or mangled: %v\n%s", err, body)
	}
	if len(nested.Nested) != 3 {
		t.Errorf("nested unknown value was not preserved intact:\n%s", body)
	}
}

// Rewriting a file nothing has changed must produce the same bytes, or every
// launch shows up as a diff in a config folder someone keeps in git.
func TestWritesAreStable(t *testing.T) {
	dir := t.TempDir()
	existing := `{"version":1,"storageUnits":[
	  {"id":"aabbccddeeff","name":"One","path":"/one","zeta":1,"alpha":2},
	  {"id":"112233445566","name":"Two","path":"/two"}
	],"zzz":"last","aaa":"first"}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Rename("aabbccddeeff", "One"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(m.File())
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := m.Rename("aabbccddeeff", "One"); err != nil {
			t.Fatal(err)
		}
		again, err := os.ReadFile(m.File())
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("repeated writes are not byte-stable:\n%s\n---\n%s", first, again)
		}
	}
	// The file listed zzz before aaa and zeta before alpha; both orders are the
	// author's, and both must survive the rewrite.
	got := string(first)
	if strings.Index(got, "zzz") > strings.Index(got, "aaa") {
		t.Errorf("preserved top-level keys were reordered:\n%s", got)
	}
	if strings.Index(got, "zeta") > strings.Index(got, "alpha") {
		t.Errorf("preserved keys inside a unit were reordered:\n%s", got)
	}
}

// The standard is meant to be hand-writable, and a person or an unrelated tool
// may well write this file. Opening the app must not rearrange their fields.
func TestAuthorsFieldOrderSurvives(t *testing.T) {
	dir := t.TempDir()
	// Deliberately not our order: path first, id last, and a settings key ahead
	// of the list.
	existing := `{
  "aTrailingSetting": true,
  "storageUnits": [
    {
      "path": "/mnt/nas/MediaItems",
      "name": "Archive NAS",
      "notes": "the one in the cupboard",
      "id": "aabbccddeeff"
    }
  ],
  "version": 1
}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Rename("aabbccddeeff", "Archive NAS"); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(m.File())
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	for _, pair := range [][2]string{
		{"aTrailingSetting", "storageUnits"},
		{"storageUnits", "version"},
		{"\"path\"", "\"name\""},
		{"\"name\"", "notes"},
		{"notes", "\"id\""},
	} {
		if strings.Index(got, pair[0]) > strings.Index(got, pair[1]) {
			t.Errorf("%s should still come before %s:\n%s", pair[0], pair[1], got)
		}
	}
}

func TestMalformedFileIsReportedNotSilentlyEmptied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(); err == nil {
		t.Fatal("a malformed file must be an error; silently reading it as an empty list would let the next write erase the user's units")
	}
}

func TestEmptyFileReadsAsEmptyList(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	units, err := m.List()
	if err != nil {
		t.Fatalf("a zero-length file should read as an empty list: %v", err)
	}
	if len(units) != 0 {
		t.Errorf("got %d units", len(units))
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	m, root := newTestManager(t)
	addDir(t, m, root, "media")

	entries, err := os.ReadDir(m.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
}

// The file is meant to be read and edited by a person, so a path containing an
// ampersand or an angle bracket must appear as the user typed it. Go's default
// JSON encoder escapes those for safe embedding in HTML, which this file is not.
func TestPathsAreNotHTMLEscaped(t *testing.T) {
	m, root := newTestManager(t)
	awkward := "Rock & Roll <2001>"
	if err := os.MkdirAll(filepath.Join(root, awkward), 0o755); err != nil {
		t.Skipf("cannot create %q here: %v", awkward, err)
	}
	if _, err := m.Add(filepath.Join(root, awkward)); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(m.File())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), awkward) {
		t.Errorf("the name was escaped rather than written literally:\n%s", body)
	}
	for _, escape := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(string(body), escape) {
			t.Errorf("found the HTML escape %s in the file:\n%s", escape, body)
		}
	}
}

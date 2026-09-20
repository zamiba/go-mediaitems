package jsonfile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeKeepsDocumentOrder(t *testing.T) {
	obj, err := Decode([]byte(`{"zeta": 1, "alpha": {"deep": [1, {"x": 2}]}, "mid": "m"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(obj.Order, ","); got != "zeta,alpha,mid" {
		t.Errorf("Order = %s, want zeta,alpha,mid", got)
	}
	if string(obj.Values["mid"]) != `"m"` {
		t.Errorf("Values[mid] = %s", obj.Values["mid"])
	}
	// A nested value is consumed whole; the walk stays on the top level.
	var nested struct{ Deep []json.RawMessage }
	if err := json.Unmarshal(obj.Values["alpha"], &nested); err != nil || len(nested.Deep) != 2 {
		t.Errorf("nested value mangled: %s (%v)", obj.Values["alpha"], err)
	}
}

func TestDecodeRepeatedKeyKeepsFirstPositionLastValue(t *testing.T) {
	obj, err := Decode([]byte(`{"a": 1, "b": 2, "a": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(obj.Order, ","); got != "a,b" {
		t.Errorf("Order = %s, want a,b", got)
	}
	if string(obj.Values["a"]) != "3" {
		t.Errorf("Values[a] = %s, want 3", obj.Values["a"])
	}
}

func TestDecodeRejectsNonObjects(t *testing.T) {
	for _, in := range []string{`[1,2]`, `"s"`, `42`, `{not json`, `{"a":1} trailing`, ``} {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("Decode(%q) should fail", in)
		}
	}
}

func TestEncodeOrderRules(t *testing.T) {
	// Read a file whose author chose their own order, add two known keys (one
	// already present, one new) and one unknown key, and check placement.
	obj, err := Decode([]byte(`{"custom": true, "name": "x", "another": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	obj.SetString("name", "y")               // existing: keeps its position
	obj.Set("version", json.RawMessage("1")) // canonical but new: appended after the file's keys
	obj.Set("zzz", json.RawMessage("0"))     // neither: sorted tail
	obj.Set("aaa", json.RawMessage("0"))

	out, err := obj.Compact("version", "name")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `{"custom":true,"name":"y","another":1,"version":1,"aaa":0,"zzz":0}`; got != want {
		t.Errorf("Compact =\n%s\nwant\n%s", got, want)
	}
}

func TestEncodeOfANewObjectUsesCanonicalOrder(t *testing.T) {
	var obj Object
	obj.Set("storageUnits", json.RawMessage("[]"))
	obj.Set("version", json.RawMessage("1"))
	out, err := obj.Encode("version", "storageUnits")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "{\n  \"version\": 1,\n  \"storageUnits\": []\n}\n"; got != want {
		t.Errorf("Encode =\n%q\nwant\n%q", got, want)
	}
}

func TestEncodeIsByteStableAndReindentsCarriedValues(t *testing.T) {
	messy := []byte("{\"a\":1,\"kept\":  {  \"nested\" :\n\n [1,   2] }  ,\"z\":2}")
	obj, err := Decode(messy)
	if err != nil {
		t.Fatal(err)
	}
	first, err := obj.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "\"nested\": [\n      1,\n      2\n    ]") {
		t.Errorf("carried value not re-indented:\n%s", first)
	}
	again, _ := Decode(first)
	second, _ := again.Encode()
	if string(first) != string(second) {
		t.Errorf("not byte-stable:\n%s\n---\n%s", first, second)
	}
}

func TestStringDoesNotHTMLEscape(t *testing.T) {
	raw, err := String(`Rock & Roll <2001> "quoted" \ back`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `"Rock & Roll <2001> \"quoted\" \\ back"`; got != want {
		t.Errorf("String = %s, want %s", got, want)
	}
	var back string
	if err := json.Unmarshal(raw, &back); err != nil || back != `Rock & Roll <2001> "quoted" \ back` {
		t.Errorf("does not round-trip: %q (%v)", back, err)
	}
}

func TestWriteAtomicAndCreateExclusive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")

	if err := CreateExclusive(path, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if err := CreateExclusive(path, []byte("{}\n")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second create: err = %v, want fs.ErrExist", err)
	}

	if err := WriteAtomic(path, []byte("{\"v\":2}\n")); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "{\"v\":2}\n" {
		t.Errorf("after WriteAtomic: %s", body)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 0644", info.Mode().Perm())
	}
}

// Object holds a map, so a struct copy shares it. Clone is the way to take a
// copy a caller can set keys on without altering the original.
func TestCloneSharesNothing(t *testing.T) {
	orig, _ := Decode([]byte(`{"a": 1}`))
	c := orig.Clone()
	c.Set("b", json.RawMessage("2"))
	c.Order = append(c.Order, "b")
	if _, leaked := orig.Values["b"]; leaked {
		t.Error("Set on the clone altered the original's map")
	}
	if len(orig.Order) != 1 {
		t.Error("appending to the clone's Order altered the original's")
	}
}

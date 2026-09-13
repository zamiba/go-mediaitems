package checksum

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Digests of the ASCII bytes "The quick brown fox jumps over the lazy dog",
// which are widely published, so a change in this package's output shows up as a
// mismatch against outside references rather than only against itself.
const fox = "The quick brown fox jumps over the lazy dog"

var foxSet = Set{
	CRC32:  "414fa339",
	MD5:    "9e107d9d372bb6826bd81d3542a419d6",
	SHA1:   "2fd4e1c67a2d28fced849ee1bb76e7391b93eb12",
	SHA256: "d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592",
	SHA512: "07e547d9586f6a73f73fbac0435ed76951218fb7d0c8d788a309d785436bbb642e93a252a954f23912547d1e8a3b5ed6e1bfd7097821233fa0538f3db854fee6",
}

func TestComputeMatchesPublishedDigests(t *testing.T) {
	got, err := Compute(strings.NewReader(fox))
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if got != foxSet {
		t.Errorf("Compute:\n got %+v\nwant %+v", got, foxSet)
	}
}

func TestComputeEmptyInput(t *testing.T) {
	got, err := Compute(strings.NewReader(""))
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	// The empty string has digests; it is not the same as "not computed".
	if got.CRC32 != "00000000" {
		t.Errorf("CRC32 of empty = %q, want %q (zero-padded to eight digits)", got.CRC32, "00000000")
	}
	if got.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("SHA256 of empty = %q", got.SHA256)
	}
	if got.Empty() {
		t.Error("a computed set is not Empty, whatever it hashed")
	}
}

func TestSelectedAlgorithms(t *testing.T) {
	got, err := ComputeSelected(strings.NewReader(fox), CRC32|SHA1)
	if err != nil {
		t.Fatal(err)
	}
	if got.CRC32 != foxSet.CRC32 || got.SHA1 != foxSet.SHA1 {
		t.Errorf("selected digests are wrong: %+v", got)
	}
	if got.MD5 != "" || got.SHA256 != "" || got.SHA512 != "" {
		t.Errorf("unselected algorithms must stay empty: %+v", got)
	}

	// A zero Algo is a caller who did not choose, not a caller who chose none.
	all, err := ComputeSelected(strings.NewReader(fox), 0)
	if err != nil {
		t.Fatal(err)
	}
	if all != foxSet {
		t.Errorf("a zero Algo should mean All:\n got %+v\nwant %+v", all, foxSet)
	}
}

// The Hasher exists so a program already moving bytes can checksum without a
// second read pass; hashing in chunks must equal hashing in one go.
func TestHasherIsIncremental(t *testing.T) {
	h := NewHasher(All)
	for _, chunk := range []string{"The quick brown fox ", "jumps over ", "the lazy dog"} {
		if _, err := h.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.Sum(); got != foxSet {
		t.Errorf("chunked hashing:\n got %+v\nwant %+v", got, foxSet)
	}
	// Sum does not consume the Hasher.
	if got := h.Sum(); got != foxSet {
		t.Errorf("a second Sum differs: %+v", got)
	}
}

func TestComputeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fox.txt")
	if err := os.WriteFile(path, []byte(fox), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ComputeFile(path)
	if err != nil {
		t.Fatalf("ComputeFile: %v", err)
	}
	if got != foxSet {
		t.Errorf("ComputeFile:\n got %+v\nwant %+v", got, foxSet)
	}
	if _, err := ComputeFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("hashing a missing file must error")
	}
}

func TestMatch(t *testing.T) {
	catalogue := Set{SHA1: strings.ToUpper(foxSet.SHA1)} // as pasted from No-Intro
	computed := foxSet

	ok, compared := computed.Match(catalogue)
	if !ok || compared != 1 {
		t.Errorf("uppercase catalogue value should match: ok=%v compared=%d", ok, compared)
	}

	ok, compared = computed.Match(foxSet)
	if !ok || compared != 5 {
		t.Errorf("identical sets: ok=%v compared=%d, want true/5", ok, compared)
	}

	wrong := foxSet
	wrong.SHA256 = strings.Repeat("0", 64)
	if ok, _ := computed.Match(wrong); ok {
		t.Error("a differing digest must not match")
	}

	// No algorithm in common is not a match, and the count is how a caller
	// knows: nothing contradicted anything because nothing was compared.
	ok, compared = Set{MD5: foxSet.MD5}.Match(Set{SHA512: foxSet.SHA512})
	if !ok {
		t.Error("disjoint sets have nothing to disagree about, so ok should be true")
	}
	if compared != 0 {
		t.Errorf("compared = %d, want 0 for disjoint sets", compared)
	}
	if ok, compared := (Set{}).Match(Set{}); compared != 0 || !ok {
		t.Errorf("two empty sets: ok=%v compared=%d", ok, compared)
	}
}

func TestNormalizeAndStrongest(t *testing.T) {
	messy := Set{SHA256: "  " + strings.ToUpper(foxSet.SHA256) + " ", MD5: strings.ToUpper(foxSet.MD5)}
	clean := messy.Normalize()
	if clean.SHA256 != foxSet.SHA256 || clean.MD5 != foxSet.MD5 {
		t.Errorf("Normalize: %+v", clean)
	}

	field, value := messy.Strongest()
	if field != "sha256Checksum" || value != foxSet.SHA256 {
		t.Errorf("Strongest = (%q, %q), want the canonical sha256", field, value)
	}
	if field, value := foxSet.Strongest(); field != "sha512Checksum" || value != foxSet.SHA512 {
		t.Errorf("Strongest should prefer sha512: (%q, %q)", field, value)
	}
	if field, value := (Set{CRC32: "414FA339"}).Strongest(); field != "crc32Checksum" || value != "414fa339" {
		t.Errorf("Strongest of a crc32-only set = (%q, %q)", field, value)
	}
	if field, value := (Set{}).Strongest(); field != "" || value != "" {
		t.Errorf("Strongest of an empty set = (%q, %q), want empty", field, value)
	}
	if !(Set{}).Empty() {
		t.Error("a zero Set is Empty")
	}
}

// The field names are the wire format of .mediaitem.json, shared with the JS
// side of the ecosystem. They are asserted literally because a typo in a struct
// tag is invisible in Go and fatal across programs.
func TestJSONFieldNames(t *testing.T) {
	body, err := json.Marshal(struct {
		Name string `json:"name"`
		Set
	}{Name: "Super Mario 64 (USA).z64", Set: foxSet})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"crc32Checksum":  foxSet.CRC32,
		"md5Checksum":    foxSet.MD5,
		"sha1Checksum":   foxSet.SHA1,
		"sha256Checksum": foxSet.SHA256,
		"sha512Checksum": foxSet.SHA512,
	} {
		if got[field] != want {
			t.Errorf("%s = %v, want %s", field, got[field], want)
		}
	}
	if got["name"] != "Super Mario 64 (USA).z64" {
		t.Error("embedding a Set must not disturb the item's own fields")
	}

	// An uncomputed checksum is absent, not an empty string: the standard's
	// examples carry empty strings as placeholders, but writing one back would
	// claim a digest that was never computed.
	partial, err := json.Marshal(Set{SHA1: foxSet.SHA1})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(partial); s != `{"sha1Checksum":"`+foxSet.SHA1+`"}` {
		t.Errorf("partial set marshalled as %s", s)
	}

	// And a placeholder read back from an item is not mistaken for a digest.
	var parsed Set
	if err := json.Unmarshal([]byte(`{"sha256Checksum":"","crc32Checksum":"414FA339"}`), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.SHA256 != "" {
		t.Error("an empty placeholder should parse as empty")
	}
	if field, _ := parsed.Strongest(); field != "crc32Checksum" {
		t.Errorf("Strongest picked %q over the only real digest", field)
	}
}

package itemtitle

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// The cases live in testdata/itemtitle.json rather than in this file, because
// three implementations have to agree on them: Omnidex's TypeScript runs the
// same file. A case that exists only in Go is a case the others cannot check
// themselves against.
type table struct {
	SanitizeValue []struct {
		Input, Want, Note string
	} `json:"sanitizeValue"`
	SanitizeToEmpty []struct {
		Input, Note string
	} `json:"sanitizeToEmpty"`
	Compose []struct {
		Values     []string
		Want, Note string
	} `json:"compose"`
	ComposeError []struct {
		Values []string
		Note   string
	} `json:"composeError"`
	Valid []struct {
		Input string
		Want  bool
		Note  string
	} `json:"valid"`
	ValidFolderName []struct {
		Input string
		Want  bool
		Note  string
	} `json:"validFolderName"`
	ValidSafe []struct {
		Input string
		Want  bool
		Note  string
	} `json:"validSafe"`
	SameName []struct {
		A, B string
		Want bool
		Note string
	} `json:"sameName"`
	Reserved []struct {
		Input string
		Want  bool
		Note  string
	} `json:"reserved"`
	Warnings []struct {
		Input string
		Want  []string
		Note  string
	} `json:"warnings"`
	PathSafe []struct {
		Input string
		Want  bool
		Note  string
	} `json:"pathSafe"`
}

func load(t *testing.T) table {
	t.Helper()
	body, err := os.ReadFile("testdata/itemtitle.json")
	if err != nil {
		t.Fatal(err)
	}
	var tb table
	if err := json.Unmarshal(body, &tb); err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestSanitizeValue(t *testing.T) {
	for _, c := range load(t).SanitizeValue {
		if got := SanitizeValue(c.Input); got != c.Want {
			t.Errorf("SanitizeValue(%q) = %q, want %q\n  %s", c.Input, got, c.Want, c.Note)
		}
	}
}

// Sanitizing twice must change nothing, or a consumer that applies the rule
// defensively produces a different folder than the composer intended.
func TestSanitizeValueIsIdempotent(t *testing.T) {
	for _, c := range load(t).SanitizeValue {
		once := SanitizeValue(c.Input)
		if twice := SanitizeValue(once); twice != once {
			t.Errorf("SanitizeValue(%q) = %q, sanitized again = %q", c.Input, once, twice)
		}
	}
}

// Anything that survives sanitizing must be a valid _itemTitle component, or
// the two halves of the rule disagree and the composed name is rejected by
// the validator that was meant to accept it.
func TestSanitizedValuesAreValid(t *testing.T) {
	for _, c := range load(t).SanitizeValue {
		got := SanitizeValue(c.Input)
		if got == "" {
			continue
		}
		if !Valid(got) {
			t.Errorf("SanitizeValue(%q) = %q, which Valid rejects", c.Input, got)
		}
	}
}

func TestSanitizeToEmpty(t *testing.T) {
	for _, c := range load(t).SanitizeToEmpty {
		if got := SanitizeValue(c.Input); got != "" {
			t.Errorf("SanitizeValue(%q) = %q, want empty\n  %s", c.Input, got, c.Note)
		}
	}
}

func TestCompose(t *testing.T) {
	tb := load(t)
	for _, c := range tb.Compose {
		got, err := Compose(c.Values...)
		if err != nil {
			t.Errorf("Compose(%q): %v", c.Values, err)
			continue
		}
		if got != c.Want {
			t.Errorf("Compose(%q) = %q, want %q\n  %s", c.Values, got, c.Want, c.Note)
		}
		if !Valid(got) {
			t.Errorf("Compose(%q) = %q, which Valid rejects", c.Values, got)
		}
	}
	for _, c := range tb.ComposeError {
		if _, err := Compose(c.Values...); err == nil {
			t.Errorf("Compose(%q) = no error, want one\n  %s", c.Values, c.Note)
		}
	}
}

// The error a cataloguer sees has to name the fix, or it says only that
// something they wrote is unacceptable.
func TestComposeNamesSafeTitle(t *testing.T) {
	_, err := Compose("***", "2001")
	if !errors.Is(err, ErrEmptyValue) {
		t.Fatalf("err = %v, want ErrEmptyValue", err)
	}
	if !contains(err.Error(), "safeTitle") {
		t.Errorf("error does not mention safeTitle: %v", err)
	}
	if !contains(err.Error(), "***") {
		t.Errorf("error does not quote the offending value: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestValid(t *testing.T) {
	for _, c := range load(t).Valid {
		if got := Valid(c.Input); got != c.Want {
			t.Errorf("Valid(%q) = %v, want %v\n  %s", c.Input, got, c.Want, c.Note)
		}
	}
}

func TestValidFolderName(t *testing.T) {
	for _, c := range load(t).ValidFolderName {
		if got := ValidFolderName(c.Input); got != c.Want {
			t.Errorf("ValidFolderName(%q) = %v, want %v\n  %s", c.Input, got, c.Want, c.Note)
		}
	}
}

func TestValidSafe(t *testing.T) {
	for _, c := range load(t).ValidSafe {
		if got := ValidSafe(c.Input); got != c.Want {
			t.Errorf("ValidSafe(%q) = %v, want %v\n  %s", c.Input, got, c.Want, c.Note)
		}
	}
}

// A safeTitle is written by a person, so the rule is that sanitizing one
// changes nothing - which is what lets the validator report a bad one as an
// error instead of silently rewriting it.
func TestAcceptedSafeTitlesAreAlreadySanitized(t *testing.T) {
	for _, c := range load(t).ValidSafe {
		if !c.Want {
			continue
		}
		if got := SanitizeValue(c.Input); got != c.Input {
			t.Errorf("ValidSafe accepts %q but SanitizeValue turns it into %q", c.Input, got)
		}
	}
}

// ValidSafe is the narrow check. Applying it to an ordinary title would
// reject most of the world's films, so the two must not be interchangeable.
func TestValidSafeIsNarrowerThanValid(t *testing.T) {
	ordinary := "Lord of the Rings The Fellowship of the Ring, The · 2001"
	if !Valid(ordinary) {
		t.Fatalf("Valid(%q) = false", ordinary)
	}
	if ValidSafe(ordinary) {
		t.Errorf("ValidSafe(%q) = true; the separator is not allowed in a field value", ordinary)
	}
}

func TestSameName(t *testing.T) {
	for _, c := range load(t).SameName {
		if got := SameName(c.A, c.B); got != c.Want {
			t.Errorf("SameName(%q, %q) = %v, want %v\n  %s", c.A, c.B, got, c.Want, c.Note)
		}
	}
}

func TestReserved(t *testing.T) {
	for _, c := range load(t).Reserved {
		if got := Reserved(c.Input); got != c.Want {
			t.Errorf("Reserved(%q) = %v, want %v\n  %s", c.Input, got, c.Want, c.Note)
		}
	}
}

func TestWarnings(t *testing.T) {
	for _, c := range load(t).Warnings {
		var got []string
		for _, w := range Warnings(c.Input) {
			got = append(got, string(w.Code))
			if w.Message == "" {
				t.Errorf("Warnings(%q): %s has no message", c.Input, w.Code)
			}
		}
		if len(got) != len(c.Want) {
			t.Errorf("Warnings(%q) = %v, want %v\n  %s", c.Input, got, c.Want, c.Note)
			continue
		}
		for i := range got {
			if got[i] != c.Want[i] {
				t.Errorf("Warnings(%q) = %v, want %v\n  %s", c.Input, got, c.Want, c.Note)
				break
			}
		}
	}
}

// A warning must never be the reason something is rejected: the two answer
// different questions, and a validator that conflated them would refuse items
// the standard accepts.
func TestWarningsDoNotDecideValidity(t *testing.T) {
	if !Valid("CON") {
		t.Error("Valid(\"CON\") = false; a reserved name is a warning at the identifier level")
	}
	if ValidFolderName("CON") {
		t.Error("ValidFolderName(\"CON\") = true; the folder cannot exist on Windows")
	}
	if len(Warnings("CON")) == 0 {
		t.Error("Warnings(\"CON\") is empty")
	}
}

// The table is the shared reference, so it must contain no literal non-ASCII:
// a file full of combining accents and invisible characters is exactly the one
// an editor silently normalises, which would make it agree with itself and
// with nothing else. The rule cannot be left to whoever edits it next.
func TestTableIsPureASCII(t *testing.T) {
	body, err := os.ReadFile("testdata/itemtitle.json")
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range body {
		if b > 127 {
			t.Fatalf("testdata/itemtitle.json byte %d is 0x%02X; non-ASCII must be written as an escape", i, b)
		}
	}
}

func TestPathSafe(t *testing.T) {
	for _, c := range load(t).PathSafe {
		if got := PathSafe(c.Input); got != c.Want {
			t.Errorf("PathSafe(%q) = %v, want %v\n  %s", c.Input, got, c.Want, c.Note)
		}
	}
}

// PathSafe is the floor, not the check. Anything ValidFolderName accepts must
// be path-safe, or the fuller check would be letting something through that
// the floor would have caught.
func TestValidFolderNameImpliesPathSafe(t *testing.T) {
	tb := load(t)
	for _, c := range tb.ValidFolderName {
		if c.Want && !PathSafe(c.Input) {
			t.Errorf("ValidFolderName(%q) is true but PathSafe is false", c.Input)
		}
	}
	for _, c := range tb.Valid {
		if c.Want && !PathSafe(c.Input) {
			t.Errorf("Valid(%q) is true but PathSafe is false", c.Input)
		}
	}
}

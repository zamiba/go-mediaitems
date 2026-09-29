// Package itemtitle implements the MediaItem standard's rules for turning
// field values into an _itemTitle, and for checking one it is handed.
//
// The rules live here rather than in each program because they must produce
// byte-identical results everywhere: an _itemTitle names the folder an item
// lives in on a storage unit, and the same folder inside every profile. Two
// implementations that disagree by one character do not fail - they quietly
// file the same item in two places.
//
// The standard splits this into two operations that must not be confused,
// and the split is the reason for the function names here:
//
//   - SanitizeValue transforms a *field value*, before it is composed into an
//     _itemTitle. Compose does that composition.
//   - Valid, ValidFolderName and ValidSafe *check* a finished string. A
//     finished _itemTitle can never be repaired by sanitizing it again,
//     because that would remove its separators. It can only be accepted or
//     rejected.
//
// This package knows nothing about ItemTypes, so it cannot check that a title
// matches the format its type defines. It checks characters and structure;
// per-type formats belong to whoever knows the type.
package itemtitle

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Separator joins the field values of an _itemTitle: "Sam's Film · 2026".
//
// It is U+00B7 with a space on each side. Because SanitizeValue replaces a
// "·" inside a field value with "-", every "·" in a finished _itemTitle is a
// separator and nothing else.
const Separator = " · "

// The zero-width joiners, which survive sanitizing although they are format
// characters like the rest of Unicode category C.
//
// They are kept because they carry meaning: ZWNJ is linguistically
// significant in Persian and Arabic, ZWJ forces conjunct forms in Indic
// scripts, and emoji sequences are held together by ZWJ - so stripping it
// turns one glyph into several. Every other format character is removed,
// including the bidirectional overrides that let a name display in a
// different order than it is stored.
const (
	zwnj = '‌'
	zwj  = '‍'
)

// replaced characters stand between words, so removing them would weld the
// words together: Face/Off is Face-Off, not FaceOff. "_" and "·" are the
// standard's own punctuation - the suffix separator and the _itemTitle
// separator - which is why a field value may not contain them.
const replaced = `/\|_·`

// removed characters decorate or end a word rather than dividing it, so
// M*A*S*H is MASH.
const removed = `:*?"<>`

// forbidden is what a finished _itemTitle may not contain. It is the replaced
// set without "·", because a composed title is full of separators - that is
// what composing means - while a field value may hold neither the separator
// nor the suffix's "_".
const forbidden = `/\|_` + removed

// Errors returned by Compose.
var (
	// ErrEmptyValue means a field value held nothing but punctuation, so
	// sanitizing left nothing behind. The item has no human-readable
	// identifier and needs a safeTitle.
	ErrEmptyValue = errors.New("itemtitle: field value is empty after sanitizing; the item needs a safeTitle")

	// ErrNoValues means Compose was called with nothing to compose.
	ErrNoValues = errors.New("itemtitle: no values to compose")
)

// SanitizeValue turns one field value into a form that is safe as part of a
// folder name, on every filesystem the suite has to survive being copied
// between.
//
// It is applied to each field value *before* composing, never to a finished
// _itemTitle: sanitizing a composed one would remove its separators and
// silently produce a different folder than the composer intended.
//
// The result may be empty - "***" holds nothing that survives - which is why
// Compose returns an error rather than composing it. Sanitizing an already
// sanitized value changes nothing, so a composer may apply it defensively.
func SanitizeValue(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range norm.NFC.String(v) {
		switch {
		case r == zwnj || r == zwj:
			b.WriteRune(r)
		case unicode.IsSpace(r):
			// Every kind of space becomes a plain one *before* runs are
			// collapsed. Without this, a non-breaking space or an ideographic
			// space is a space to one implementation and an ordinary
			// character to another, and the two produce different folders.
			// This also catches the whitespace control characters - tab,
			// newline - which category C would otherwise remove, welding the
			// words on either side together.
			b.WriteByte(' ')
		case r == utf8.RuneError:
			// The replacement character is kept, although it is never a
			// character somebody chose: it is what a decoder writes when it
			// hits bytes it cannot interpret, so "Bj\uFFFDrn" is a "Björn"
			// whose ö was already destroyed upstream.
			//
			// Dropping it would give "Bjrn", which looks like a name somebody
			// might plausibly have - it would launder the corruption into
			// something that no longer looks corrupt, in the one place that
			// outlives the conversation about it. Kept, the damage stays
			// visible and the person can fix the data. Warnings reports it.
			b.WriteRune(r)
		case unicode.Is(unicode.C, r):
			// Control, format, private-use and surrogate characters. This is
			// where the bidirectional overrides go, and the zero-width
			// characters that make two different folder names look identical.
			// Unassigned code points are not in Go's tables and so survive;
			// nothing can be done about that from here.
		case strings.ContainsRune(replaced, r):
			b.WriteByte('-')
		case strings.ContainsRune(removed, r):
		default:
			b.WriteRune(r)
		}
	}
	return trimEnds(collapseSpaces(b.String()))
}

// Compose builds an _itemTitle from field values, sanitizing each and joining
// them with Separator. It is the only correct way to build one: sanitizing
// the finished string instead would eat the separators.
//
// A value that sanitizes to nothing is an error rather than an empty
// component, because a title made only of punctuation has no human-readable
// identifier - which is what an _itemTitle is for. The fix is a safeTitle,
// and the error says so.
func Compose(values ...string) (string, error) {
	if len(values) == 0 {
		return "", ErrNoValues
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = SanitizeValue(v)
		if parts[i] == "" {
			return "", fmt.Errorf("%w: %q", ErrEmptyValue, v)
		}
	}
	return strings.Join(parts, Separator), nil
}

// Valid reports whether s is a well-formed _itemTitle: one that this package
// could have composed, and that names a folder on any of the three
// filesystems.
//
// It accepts "·", because a finished _itemTitle contains separators, and it
// rejects "_", because the suffix separator is the only "_" a folder name may
// hold and an _itemTitle is the part before it. Use ValidFolderName for a
// name that may carry a suffix.
//
// A leading "-" is accepted. The standard reserves that prefix for ItemType
// folders inside a MediaItem folder, which is a different place in the tree:
// every entry inside an ItemType folder is a MediaItem folder, so there is
// nothing there for the prefix to be confused with.
func Valid(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if s != norm.NFC.String(s) {
		return false
	}
	if strings.ContainsAny(s, forbidden) {
		return false
	}
	// A leading "." hides the folder from ls, from Finder and from shell
	// globs - and .mediaitem.json and .artwork are themselves dotfiles, so a
	// walker that is right to skip those would lose the whole item. A
	// trailing "." or space is silently dropped by Windows, leaving a folder
	// that no longer matches its own _itemTitle.
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	if strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") || strings.Contains(s, "  ") {
		return false
	}
	for _, r := range s {
		if r == zwnj || r == zwj {
			continue
		}
		if unicode.Is(unicode.C, r) {
			return false
		}
		if unicode.IsSpace(r) && r != ' ' {
			return false
		}
	}
	return true
}

// ValidFolderName reports whether name may be used as a MediaItem folder:
// "[_itemTitle]" or "[_itemTitle]_[field value]", where the suffix
// disambiguates two items that would otherwise produce the same folder.
//
// Two checks, and only one of them is the standard's:
//
//   - Conformance, on the part before the "_" only. Everything from the
//     suffix onward is ignored, because a suffix value may be any field the
//     cataloguer thought distinguishing.
//   - Path safety, on the whole string, always. A name arrives from a
//     .mediaitem.json somebody else wrote, so ignoring the suffix for
//     conformance must never mean joining it onto a path unchecked.
func ValidFolderName(name string) bool {
	if !PathSafe(name) {
		return false
	}
	// A name Windows reserves cannot be a folder there at all, and the whole
	// point of the character rules is that a storage unit survives being
	// copied between filesystems. So this is an error here, although Valid
	// leaves it as a warning: Valid asks whether the identifier conforms,
	// which is the standard's question, and this function asks whether the
	// folder can exist. The suffix is irrelevant - Windows reserves the whole
	// name, so "Movie · 2001_CON" is fine.
	if Reserved(name) {
		return false
	}
	head := name
	if i := strings.IndexByte(name, '_'); i >= 0 {
		head = name[:i]
	}
	return Valid(head)
}

// PathSafe reports whether s can only ever name one folder directly inside
// another: not empty, not "." or "..", no separator of either kind, and no
// control characters. It is what stands between a name somebody else wrote and
// a path outside the folder it was meant for.
//
// **This is not the check you want for an item title.** It says nothing about
// whether a name conforms to the standard, whether it can exist on Windows, or
// whether two devices would spell it the same way - ValidFolderName asks all
// of that and this as well. PathSafe is for the names the standard does not
// define, which need the security floor and must not be held to the rest:
// a profile slug is the example, because it is deliberately reductive and
// already exists in folders on people's machines, so refusing one would make
// an existing profile unreachable rather than prevent a bad one.
//
// Reach for it only when you can say why the fuller check is wrong. If you
// cannot, ValidFolderName is the answer.
func PathSafe(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			return false
		}
	}
	return true
}

// ValidSafe reports whether s is acceptable as a safeTitle or safeSortTitle.
//
// Those fields exist for the cases where the mechanical rule produces
// something safe but wrong - ".hack" sanitizes to "hack", where the franchise
// itself is written "dothack" - so they are written by a person, rarely, and
// are constrained by what may appear rather than by what may not. A positive
// set covers the characters nobody has thought of yet.
//
// This is *tighter* than Valid, which has to accept the accents, CJK and "·"
// that ordinary titles are full of. Applying this one to an ordinary
// _itemTitle would reject most of the world's films.
func ValidSafe(s string) bool {
	if s == "" {
		return false
	}
	if s != norm.NFC.String(s) {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	if strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") || strings.Contains(s, "  ") {
		return false
	}
	for _, r := range s {
		switch {
		case r == zwnj || r == zwj:
		case r == ' ':
		case strings.ContainsRune(`-',.()&`, r):
		case unicode.IsLetter(r), unicode.Is(unicode.M, r), unicode.IsDigit(r):
		default:
			return false
		}
	}
	return true
}

// SameName reports whether two folder names collide - the test for whether a
// disambiguating suffix is needed.
//
// Windows and macOS treat "Alien · 1979" and "ALIEN · 1979" as one folder
// where Linux treats them as two, so a collision has to be judged
// case-insensitively or a storage unit loses an item the first time it is
// copied. strings.EqualFold is simple Unicode case folding, which is what the
// standard asks for: a locale-aware folding would make the answer depend on
// the machine.
func SameName(a, b string) bool {
	return strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
}

// Reserved reports whether name is one Windows refuses whatever the
// extension, so that a validator can warn about it. It is deliberately not
// part of Valid: the standard makes this a warning, since a title that
// sanitizes down to exactly "CON" is vanishingly rare and refusing it outright
// would be worse than the problem.
func Reserved(name string) bool {
	// Windows ignores an extension and any trailing dots or spaces when it
	// matches these, so "nul.txt", "CON." and "AUX " are all refused.
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimRight(name, " ")
	switch strings.ToUpper(name) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	if len(name) == 4 && (strings.EqualFold(name[:3], "COM") || strings.EqualFold(name[:3], "LPT")) {
		return name[3] >= '1' && name[3] <= '9'
	}
	return false
}

// collapseSpaces reduces runs of spaces to one. Only spaces: a run of "-"
// left by two replaced characters is kept, because a title may legitimately
// contain "--" and collapsing it would mangle one.
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// trimEnds removes leading and trailing spaces and dots. A leading "-" is
// left alone; see Valid for why the two ends differ.
func trimEnds(s string) string {
	return strings.Trim(s, ". ")
}

// A WarningCode names a kind of warning. They are stable strings so that a
// caller can act on one - suppress it, translate it, or link to an
// explanation - without matching on the message text.
type WarningCode string

const (
	// WarnReplacementCharacter: the value holds U+FFFD, which means some
	// earlier step could not decode the bytes it was given. The character
	// that was there is already lost.
	WarnReplacementCharacter WarningCode = "replacement-character"

	// WarnReservedName: Windows refuses this name whatever the extension.
	// Valid accepts it, because the standard makes it a warning rather than
	// an error; ValidFolderName does not, because the folder cannot exist.
	WarnReservedName WarningCode = "reserved-name"

	// WarnFullwidthPunctuation: the value holds a full-width or CJK form of a
	// character the rules remove, such as U+FF1A "：" beside an ordinary ":".
	// These are legal on every filesystem and are correct punctuation in
	// Japanese, so they are left alone - but two titles differing only by
	// which one was typed give two folders that look identical.
	WarnFullwidthPunctuation WarningCode = "fullwidth-punctuation"

	// WarnStrayJoiner: a zero-width joiner sits next to a Latin letter. The
	// joiners are kept because they are meaningful in Persian, Arabic and the
	// Indic scripts, where they decide whether letters join up. Beside Latin
	// text they do nothing visible, which is exactly what makes two different
	// names look like one.
	WarnStrayJoiner WarningCode = "stray-joiner"
)

// A Warning is something acceptable but worth a person's attention. Message
// is one sentence, written to be shown as it is.
type Warning struct {
	Code    WarningCode
	Message string
}

// lookalikes are full-width and CJK characters that resemble ones the rules
// remove or reserve. The list is deliberately short and openly incomplete:
// folding them properly would need NFKC, which also folds half-width katakana,
// the "ﬁ" ligature, "①" and the full-width Latin that Japanese typography uses
// on purpose. Warning about a few real confusions beats mangling correct text.
const lookalikes = "／＼｜：＊？＂＜＞＿・"

// Warnings reports what is acceptable but worth a person's attention. It says
// nothing about whether s is valid - ask Valid or ValidFolderName for that -
// and it is meant for a validator, an import report, or anywhere a person is
// about to commit to a name.
//
// It takes either a field value or a finished _itemTitle: every check here is
// about characters rather than about structure.
func Warnings(s string) []Warning {
	var out []Warning
	if strings.ContainsRune(s, utf8.RuneError) {
		out = append(out, Warning{WarnReplacementCharacter,
			"This text contains a replacement character (�), which means some earlier step could not read the original bytes. The character that was there is already lost, so the data needs fixing at its source rather than here."})
	}
	if Reserved(s) {
		out = append(out, Warning{WarnReservedName,
			"Windows reserves this name for a device and will not allow a folder to be called it, whatever the extension."})
	}
	if strings.ContainsAny(s, lookalikes) {
		out = append(out, Warning{WarnFullwidthPunctuation,
			"This text contains a full-width or CJK character that resembles one the naming rules remove, so it is kept as it is. Check it was meant, since a name using it looks almost identical to one that does not."})
	}
	if strayJoiner(s) {
		out = append(out, Warning{WarnStrayJoiner,
			"This text contains an invisible joining character next to Latin letters, where it has no visible effect. Two names differing only by one of these look identical."})
	}
	return out
}

// strayJoiner reports whether a joiner sits directly beside a Latin letter,
// which is where it does nothing that can be seen. Between two Arabic letters
// or inside an emoji sequence it is doing its job, and no warning is given.
func strayJoiner(s string) bool {
	runes := []rune(s)
	for i, r := range runes {
		if r != zwnj && r != zwj {
			continue
		}
		if i > 0 && unicode.Is(unicode.Latin, runes[i-1]) {
			return true
		}
		if i+1 < len(runes) && unicode.Is(unicode.Latin, runes[i+1]) {
			return true
		}
	}
	return false
}

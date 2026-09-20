// Package jsonfile is how the MediaItem suite reads and writes a JSON file that
// people and several programs share.
//
// It is not a JSON library. encoding/json does the parsing and the formatting;
// this package holds the handful of decisions layered on top that decide what
// bytes land on disk, so that every file the suite writes - storage-units.json,
// profile.json, and whatever comes next - behaves the same way:
//
//   - **The file's own key order is kept.** A file written by a person or by
//     another tool comes back with its fields where they put them. Anything new
//     is appended. encoding/json cannot do this on its own: unmarshalling into
//     a map loses the order and into a struct loses undeclared keys.
//   - **Unknown keys survive.** A program that does not recognise a key another
//     program wrote preserves it rather than deleting it on the next write.
//   - **Strings are not HTML-escaped.** Go escapes &, < and > by default so JSON
//     can be embedded in a web page. These files are not web pages, and a
//     folder called "Rock & Roll" should read as itself.
//   - **Writes are atomic and byte-stable.** A reader never sees a torn file,
//     and rewriting unchanged content produces identical bytes, so a config
//     folder kept in git does not churn.
//
// These primitives existed as copies in two packages before this one, and the
// second copy had already drifted from the first on the escaping rule. That is
// the reason this package exists: the file layer is a contract between
// programs, and a contract with two implementations is two contracts.
package jsonfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Object is one JSON object with its keys in document order.
//
// Order is the order the keys were read in, and is what Encode writes them
// back in. Values holds each key's raw, still-encoded bytes. A key may be in
// Values without being in Order - that is a key added since the file was
// read, and Encode appends it.
type Object struct {
	Order  []string
	Values map[string]json.RawMessage
}

// Decode parses one JSON object, keeping its keys in the order they appear.
//
// It walks the token stream rather than unmarshalling, because that is the only
// way encoding/json exposes key order. Decode consumes a whole value however
// deeply nested, so the walk stays on the object's own keys. A repeated key
// keeps its first position and its last value, as json.Unmarshal would.
//
// Anything other than a single object - an array, a bare value, trailing
// content - is an error.
func Decode(data []byte) (Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return Object{}, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return Object{}, fmt.Errorf("jsonfile: expected a JSON object, found %v", tok)
	}

	obj := Object{Values: make(map[string]json.RawMessage)}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return Object{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return Object{}, fmt.Errorf("jsonfile: expected an object key, found %v", keyTok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return Object{}, err
		}
		if _, seen := obj.Values[key]; !seen {
			obj.Order = append(obj.Order, key)
		}
		obj.Values[key] = raw
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return Object{}, err
	}
	if dec.More() {
		return Object{}, errors.New("jsonfile: unexpected content after the object")
	}
	return obj, nil
}

// Clone returns a copy that shares nothing with o. Object holds a map, and a
// plain assignment copies the struct but not the map behind it - so a caller
// that wants to set keys for one write without altering the object it read
// clones first.
func (o Object) Clone() Object {
	c := Object{Order: append([]string(nil), o.Order...), Values: make(map[string]json.RawMessage, len(o.Values)+2)}
	for k, v := range o.Values {
		c.Values[k] = v
	}
	return c
}

// Set stores a value under key. It does not change Order: a key the file
// already had keeps its position, and a new key is placed by Encode.
func (o *Object) Set(key string, value json.RawMessage) {
	if o.Values == nil {
		o.Values = make(map[string]json.RawMessage)
	}
	o.Values[key] = value
}

// SetString is Set with a string value, encoded without HTML escaping.
func (o *Object) SetString(key, value string) error {
	raw, err := String(value)
	if err != nil {
		return err
	}
	o.Set(key, raw)
	return nil
}

// Compact renders the object on one line, with keys in this order: the file's
// own order first, then any key named in canonical that the file did not have,
// then anything else sorted by name.
//
// New keys go on the end rather than into the middle because appending is the
// only placement that confines an existing file's diff to the lines that
// actually changed. canonical is the order a brand-new file gets, and the order
// new known keys are appended in. The sorted tail is what keeps the output
// independent of map iteration order.
func (o Object) Compact(canonical ...string) ([]byte, error) {
	order := o.finalOrder(canonical)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range order {
		if i > 0 {
			buf.WriteByte(',')
		}
		encoded, err := String(key)
		if err != nil {
			return nil, fmt.Errorf("jsonfile: encoding key %q: %w", key, err)
		}
		buf.Write(encoded)
		buf.WriteByte(':')
		buf.Write(o.Values[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Encode renders the object as it is written to disk: Compact's ordering,
// two-space indentation, and a trailing newline. Values carried over from the
// file are re-indented in the same pass, so content someone wrote on one line
// and content we wrote come out looking the same.
func (o Object) Encode(canonical ...string) ([]byte, error) {
	compact, err := o.Compact(canonical...)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return nil, fmt.Errorf("jsonfile: formatting: %w", err)
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func (o Object) finalOrder(canonical []string) []string {
	out := make([]string, 0, len(o.Values))
	seen := make(map[string]bool, len(o.Values))
	take := func(key string) {
		if _, ok := o.Values[key]; ok && !seen[key] {
			out = append(out, key)
			seen[key] = true
		}
	}
	for _, key := range o.Order {
		take(key)
	}
	for _, key := range canonical {
		take(key)
	}
	rest := make([]string, 0, len(o.Values)-len(out))
	for key := range o.Values {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// String encodes s as a JSON string without HTML escaping.
func String(s string) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// WriteAtomic replaces the file at path with data: a temp file in the same
// directory, flushed to disk, then renamed over the target. A concurrent
// reader sees the whole old file or the whole new one, never a torn one, and a
// power cut between the flush and the rename loses nothing.
//
// The temp name is unique rather than fixed, so a stale temp left by a crashed
// process is inert rather than something the next write silently adopts.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("jsonfile: creating a temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("jsonfile: writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("jsonfile: syncing %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o644); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return fmt.Errorf("jsonfile: setting permissions on %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("jsonfile: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("jsonfile: replacing %s: %w", path, err)
	}
	return nil
}

// CreateExclusive writes data to a file that must not exist yet. If it does,
// the error satisfies errors.Is(err, fs.ErrExist) and nothing is written -
// which is how two programs creating the same file at the same moment are
// prevented from both believing they made it.
func CreateExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err // fs.ErrExist passes through for the caller to test
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("jsonfile: writing %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("jsonfile: syncing %s: %w", path, err)
	}
	return f.Close()
}

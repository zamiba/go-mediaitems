package storageunit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// store is the file half of the package: it reads and writes storage-units.json
// and knows nothing about what the units mean.
type store struct {
	dir      string
	file     string
	lockFile string
}

func newStore(dir string) *store {
	return &store{
		dir:      dir,
		file:     filepath.Join(dir, FileName),
		lockFile: filepath.Join(dir, LockFileName),
	}
}

// document is one parsed storage-units.json.
//
// It keeps every top-level key it did not recognise in extra, and each unit
// keeps its own unrecognised keys, so a program writing this file never drops
// data a newer sibling program wrote. The file's shape was chosen to leave room
// for future suite settings beside the list; that room is only real if a round
// trip preserves them.
type document struct {
	version int
	units   []persistedUnit
	extra   map[string]json.RawMessage

	// keyOrder is the order the keys appeared in when the file was read, so a
	// file written by a person or another tool comes back in the order they
	// chose rather than in ours.
	keyOrder []string
}

// persistedUnit is the on-disk form of a unit: the three fields that persist,
// and nothing else. Space figures and the unreachable flag have no
// representation here, so they cannot be written to the file by accident.
type persistedUnit struct {
	ID       string
	Name     string
	Path     string
	extra    map[string]json.RawMessage
	keyOrder []string
}

var unitKnownKeys = map[string]bool{"id": true, "name": true, "path": true}

func (p *persistedUnit) UnmarshalJSON(b []byte) error {
	order, raw, err := decodeObject(b)
	if err != nil {
		return err
	}
	p.keyOrder = order
	for key, dst := range map[string]*string{"id": &p.ID, "name": &p.Name, "path": &p.Path} {
		if v, ok := raw[key]; ok {
			if err := json.Unmarshal(v, dst); err != nil {
				return fmt.Errorf("storage unit %s: %w", key, err)
			}
		}
	}
	for key, v := range raw {
		if unitKnownKeys[key] {
			continue
		}
		if p.extra == nil {
			p.extra = make(map[string]json.RawMessage, 1)
		}
		p.extra[key] = v
	}
	return nil
}

// ensure creates the config folder and, if the file is absent, an empty list.
//
// Creation is exclusive: when two programs first run at the same moment one
// wins and the other reads the winner's file. An existing file is never
// touched, including a deliberately empty one - a list the user has emptied
// stays empty, and a program with no reachable units shows its empty state
// rather than re-seeding folders.
func (s *store) ensure() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("storageunit: creating %s: %w", s.dir, err)
	}
	f, err := os.OpenFile(s.file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil // someone got there first, which is the point of O_EXCL
		}
		return fmt.Errorf("storageunit: creating %s: %w", s.file, err)
	}
	defer f.Close()
	empty := &document{version: SchemaVersion}
	body, err := empty.marshal()
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("storageunit: writing %s: %w", s.file, err)
	}
	return f.Sync()
}

// load reads the file. A missing file reads as an empty list rather than an
// error: it is the same state as a file holding an empty array, and a caller
// that removed the file between ensure and load should see an empty list, not a
// failure.
func (s *store) load() (*document, error) {
	body, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &document{version: SchemaVersion}, nil
		}
		return nil, fmt.Errorf("storageunit: reading %s: %w", s.file, err)
	}
	return parse(body, s.file)
}

func parse(body []byte, name string) (*document, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return &document{version: SchemaVersion}, nil
	}
	order, raw, err := decodeObject(body)
	if err != nil {
		return nil, fmt.Errorf("storageunit: %s is not valid JSON: %w", name, err)
	}

	doc := &document{version: SchemaVersion, keyOrder: order}
	if v, ok := raw["version"]; ok {
		if err := json.Unmarshal(v, &doc.version); err != nil {
			return nil, fmt.Errorf("storageunit: %s has a non-numeric version: %w", name, err)
		}
	}
	if v, ok := raw["storageUnits"]; ok {
		if err := json.Unmarshal(v, &doc.units); err != nil {
			return nil, fmt.Errorf("storageunit: %s has a malformed storageUnits array: %w", name, err)
		}
	}
	for key, v := range raw {
		if key == "version" || key == "storageUnits" {
			continue
		}
		if doc.extra == nil {
			doc.extra = make(map[string]json.RawMessage, 1)
		}
		doc.extra[key] = v
	}
	return doc, nil
}

// save writes the document atomically: a temp file in the same directory, then
// a rename over the target. A concurrent reader sees either the whole old file
// or the whole new one, never a torn one.
func (s *store) save(doc *document) error {
	body, err := doc.marshal()
	if err != nil {
		return err
	}
	// A unique temp name rather than a fixed storage-units.json.tmp: the lock
	// makes a collision unlikely, but a stale temp left by a crashed process is
	// then inert rather than something the next write silently adopts.
	tmp, err := os.CreateTemp(s.dir, FileName+".tmp*")
	if err != nil {
		return fmt.Errorf("storageunit: creating a temp file in %s: %w", s.dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("storageunit: writing %s: %w", tmpName, err)
	}
	// Flush before the rename: the rename is atomic with respect to other
	// readers, but not with respect to a power cut that lands between them.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("storageunit: syncing %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o644); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return fmt.Errorf("storageunit: setting permissions on %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storageunit: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.file); err != nil {
		return fmt.Errorf("storageunit: replacing %s: %w", s.file, err)
	}
	return nil
}

// marshal renders the document as the JSON written to disk.
//
// Three properties matter, and each is a rule about respecting what was already
// in the file:
//
//   - **Key order is the file's own.** A file written by a person or by another
//     tool comes back with its fields in the order they chose. Anything new is
//     appended rather than woven in. The alternative - imposing our own order -
//     turns opening the app into a diff nobody asked for, and the MediaItem
//     standard is explicitly meant to be hand-writable.
//   - **Unrecognised keys survive**, at the top level and inside each unit, so a
//     program that does not know about a key a newer sibling wrote preserves it
//     instead of deleting it.
//   - **Output is byte-stable**: rewriting an unchanged list produces identical
//     bytes, so a config folder kept in git does not churn on every launch.
//
// The document is assembled compactly in the right order and then handed to
// json.Indent once, which is what keeps the formatting logic out of this file -
// no prefix threading, no per-level indentation arithmetic. Values carried over
// from the file are re-indented by that same pass, so a unit someone wrote on
// one line and a unit we wrote come out looking the same.
func (d *document) marshal() ([]byte, error) {
	version := d.version
	if version == 0 {
		version = SchemaVersion
	}
	versionRaw, err := json.Marshal(version)
	if err != nil {
		return nil, fmt.Errorf("storageunit: encoding version: %w", err)
	}
	unitsRaw, err := marshalUnits(d.units)
	if err != nil {
		return nil, err
	}

	values := make(map[string]json.RawMessage, len(d.extra)+2)
	for key, value := range d.extra {
		values[key] = value
	}
	// Written after the preserved keys so a stray "version" in a file could
	// never displace the real one.
	values["version"] = versionRaw
	values["storageUnits"] = unitsRaw

	var compact bytes.Buffer
	if err := writeObject(&compact, finalOrder(d.keyOrder, values, "version", "storageUnits"), values); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, fmt.Errorf("storageunit: formatting %s: %w", FileName, err)
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func marshalUnits(units []persistedUnit) (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, u := range units {
		if i > 0 {
			buf.WriteByte(',')
		}
		values := make(map[string]json.RawMessage, len(u.extra)+3)
		for key, value := range u.extra {
			values[key] = value
		}
		for key, value := range map[string]string{"id": u.ID, "name": u.Name, "path": u.Path} {
			encoded, err := jsonString(value)
			if err != nil {
				return nil, fmt.Errorf("storageunit: encoding unit %s: %w", key, err)
			}
			values[key] = encoded
		}
		if err := writeObject(&buf, finalOrder(u.keyOrder, values, "id", "name", "path"), values); err != nil {
			return nil, err
		}
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// decodeObject returns a JSON object's keys in the order the document had them,
// along with each raw value.
//
// encoding/json's usual entry points cannot answer this: unmarshalling into a
// map loses the order, and unmarshalling into a struct loses the keys the struct
// does not declare. Walking the token stream keeps both. Decode consumes a whole
// value however deeply nested it is, so the walk stays on the object's own keys.
func decodeObject(data []byte) ([]string, map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("expected a JSON object, found %v", tok)
	}

	var order []string
	values := make(map[string]json.RawMessage)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil, fmt.Errorf("expected an object key, found %v", keyTok)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = raw // a repeated key keeps its first position, last value
	}
	return order, values, nil
}

// finalOrder is the order to write values in: the order the file had, then
// anything new.
//
// New keys go on the end rather than into the middle, because appending is the
// only placement that leaves an existing file's diff to the lines that actually
// changed. Keys named in canonical come first among the new ones, and the rest
// are sorted, so the result never depends on map iteration order.
func finalOrder(recorded []string, values map[string]json.RawMessage, canonical ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	take := func(key string) {
		if _, ok := values[key]; ok && !seen[key] {
			out = append(out, key)
			seen[key] = true
		}
	}
	for _, key := range recorded {
		take(key)
	}
	for _, key := range canonical {
		take(key)
	}
	rest := make([]string, 0, len(values)-len(out))
	for key := range values {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func writeObject(buf *bytes.Buffer, order []string, values map[string]json.RawMessage) error {
	buf.WriteByte('{')
	for i, key := range order {
		if i > 0 {
			buf.WriteByte(',')
		}
		encoded, err := jsonString(key)
		if err != nil {
			return fmt.Errorf("storageunit: encoding key %q: %w", key, err)
		}
		buf.Write(encoded)
		buf.WriteByte(':')
		buf.Write(values[key])
	}
	buf.WriteByte('}')
	return nil
}

// jsonString encodes a Go string as a JSON string without HTML escaping.
//
// json.Marshal turns &, < and > into \u0026-style escapes so that JSON can be
// embedded safely inside an HTML page. This file is not an HTML page, and a
// folder called "Rock & Roll" should read as itself when the user opens it.
func jsonString(s string) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

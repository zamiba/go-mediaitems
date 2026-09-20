package storageunit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zamiba/go-mediaitems/jsonfile"
)

// store is the file half of the package: it reads and writes storage-units.json
// and knows nothing about what the units mean. How the bytes are read and
// written - key order, unknown keys, escaping, atomicity - is jsonfile's job,
// shared with every other file the suite writes.
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

// The keys this package owns, and the order a brand-new file gets. An existing
// file keeps its own order; see jsonfile.Object.
const (
	keyVersion = "version"
	keyUnits   = "storageUnits"
)

var canonicalOrder = []string{keyVersion, keyUnits}

// document is one parsed storage-units.json.
//
// The two keys this package understands are typed; everything else in the
// file rides along in obj so a program writing this file never drops data a
// newer sibling program wrote. The file's shape was chosen to leave room for
// future suite settings beside the list; that room is only real if a round
// trip preserves them.
type document struct {
	version int
	units   []persistedUnit
	obj     jsonfile.Object // the file as read, minus the two typed keys
}

// persistedUnit is the on-disk form of a unit: the three fields that persist,
// and nothing else. Space figures and the unreachable flag have no
// representation here, so they cannot be written to the file by accident.
type persistedUnit struct {
	ID   string
	Name string
	Path string
	obj  jsonfile.Object // the unit as read, minus the three typed keys
}

const (
	unitKeyID   = "id"
	unitKeyName = "name"
	unitKeyPath = "path"
)

var unitCanonicalOrder = []string{unitKeyID, unitKeyName, unitKeyPath}

func (p *persistedUnit) UnmarshalJSON(b []byte) error {
	obj, err := jsonfile.Decode(b)
	if err != nil {
		return err
	}
	for key, dst := range map[string]*string{unitKeyID: &p.ID, unitKeyName: &p.Name, unitKeyPath: &p.Path} {
		if v, ok := obj.Values[key]; ok {
			if err := json.Unmarshal(v, dst); err != nil {
				return fmt.Errorf("storage unit %s: %w", key, err)
			}
			delete(obj.Values, key)
		}
	}
	p.obj = obj
	return nil
}

func (p persistedUnit) compact() ([]byte, error) {
	obj := p.obj.Clone()
	for key, value := range map[string]string{unitKeyID: p.ID, unitKeyName: p.Name, unitKeyPath: p.Path} {
		if err := obj.SetString(key, value); err != nil {
			return nil, fmt.Errorf("storageunit: encoding unit %s: %w", key, err)
		}
	}
	return obj.Compact(unitCanonicalOrder...)
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
	empty := &document{version: SchemaVersion}
	body, err := empty.marshal()
	if err != nil {
		return err
	}
	if err := jsonfile.CreateExclusive(s.file, body); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil // someone got there first, which is the point of O_EXCL
		}
		return fmt.Errorf("storageunit: creating %s: %w", s.file, err)
	}
	return nil
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
	obj, err := jsonfile.Decode(body)
	if err != nil {
		return nil, fmt.Errorf("storageunit: %s is not valid JSON: %w", name, err)
	}

	doc := &document{version: SchemaVersion}
	if v, ok := obj.Values[keyVersion]; ok {
		if err := json.Unmarshal(v, &doc.version); err != nil {
			return nil, fmt.Errorf("storageunit: %s has a non-numeric version: %w", name, err)
		}
		delete(obj.Values, keyVersion)
	}
	if v, ok := obj.Values[keyUnits]; ok {
		if err := json.Unmarshal(v, &doc.units); err != nil {
			return nil, fmt.Errorf("storageunit: %s has a malformed storageUnits array: %w", name, err)
		}
		delete(obj.Values, keyUnits)
	}
	doc.obj = obj
	return doc, nil
}

// save writes the document atomically, so a concurrent reader sees either the
// whole old file or the whole new one.
func (s *store) save(doc *document) error {
	body, err := doc.marshal()
	if err != nil {
		return err
	}
	if err := jsonfile.WriteAtomic(s.file, body); err != nil {
		return fmt.Errorf("storageunit: %w", err)
	}
	return nil
}

// marshal renders the document as the JSON written to disk. The two keys this
// package owns are set last so a stray copy of either in the file could never
// displace the real one; jsonfile keeps the file's order, preserves everything
// else, and formats.
func (d *document) marshal() ([]byte, error) {
	version := d.version
	if version == 0 {
		version = SchemaVersion
	}
	versionRaw, err := json.Marshal(version)
	if err != nil {
		return nil, fmt.Errorf("storageunit: encoding version: %w", err)
	}

	var units bytes.Buffer
	units.WriteByte('[')
	for i, u := range d.units {
		if i > 0 {
			units.WriteByte(',')
		}
		compact, err := u.compact()
		if err != nil {
			return nil, err
		}
		units.Write(compact)
	}
	units.WriteByte(']')

	obj := d.obj.Clone()
	obj.Set(keyVersion, versionRaw)
	obj.Set(keyUnits, units.Bytes())
	out, err := obj.Encode(canonicalOrder...)
	if err != nil {
		return nil, fmt.Errorf("storageunit: %w", err)
	}
	return out, nil
}

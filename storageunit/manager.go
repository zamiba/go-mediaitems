package storageunit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Options configures a Manager.
type Options struct {
	// Dir is the folder holding storage-units.json. Empty means the shared
	// location, os.UserConfigDir()/MediaItem - which is what every suite
	// program should use. Set it only for tests, or for a program offering an
	// explicit "use this config folder instead" flag.
	Dir string
}

// Manager is the entry point to the shared list. It is safe for concurrent use,
// and safe against other processes: every mutation takes an advisory lock on the
// sibling lock file for the whole read-modify-write, so two suite programs
// adding a unit at the same moment produce a list with both.
//
// A Manager holds no copy of the list. Every operation reads the file, so a
// change another program made a second ago is already visible and correctness
// never depends on being notified. A program wanting live UI freshness can
// re-read on window focus or watch the file with fsnotify; neither is required.
type Manager struct {
	// mu serialises callers within this process. The file lock alone would do
	// it, but taking a cheap mutex first avoids sending every goroutine through
	// a syscall to queue.
	mu    sync.Mutex
	store *store
}

// New opens the shared list, creating the config folder and an empty list file
// if they do not exist yet.
//
// Creating the file is not seeding it. A new file holds an empty storageUnits
// array, and it stays empty until something calls Add or Seed.
func New(opts Options) (*Manager, error) {
	dir := opts.Dir
	if dir == "" {
		var err error
		if dir, err = DefaultDir(); err != nil {
			return nil, err
		}
	}
	s := newStore(dir)
	if err := s.ensure(); err != nil {
		return nil, err
	}
	return &Manager{store: s}, nil
}

// Open is New with default options: the shared list in the user's config folder.
func Open() (*Manager, error) { return New(Options{}) }

// DefaultDir returns the shared configuration folder,
// os.UserConfigDir()/MediaItem.
//
// Every suite program resolves it through os.UserConfigDir rather than a
// hard-coded ~/.config, so all of them land in the same place on every
// platform: ${XDG_CONFIG_HOME:-~/.config}/MediaItem on Linux,
// ~/Library/Application Support/MediaItem on macOS, %AppData%\MediaItem on
// Windows.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("storageunit: locating the user config folder: %w", err)
	}
	return filepath.Join(base, DirName), nil
}

// Dir returns the folder holding the list. Suite data that belongs beside the
// list - the shared item index - goes here, and nowhere else: never in a cache
// folder, and never on a storage unit, which would be unreadable in exactly the
// situation it exists to describe.
func (m *Manager) Dir() string { return m.store.dir }

// File returns the full path of storage-units.json.
func (m *Manager) File() string { return m.store.file }

// List returns every configured unit in priority order, each with live space
// figures, or Unreachable set if its path could not be read.
//
// Unreachable units are included. A program that owns persistent content must
// read across all of them: content on a disconnected drive is unavailable,
// which is a different answer than missing, and the user is owed the
// difference.
func (m *Manager) List() ([]StorageUnit, error) {
	doc, err := m.store.load()
	if err != nil {
		return nil, err
	}
	return withSpace(doc.units), nil
}

// Add appends a folder to the list as the lowest-priority unit.
//
// New units go to the bottom, never the top: a folder the user just picked must
// not silently capture output that was going somewhere else. The path is
// resolved to an absolute path and must be a readable directory; a folder
// already in the list is rejected with ErrDuplicatePath rather than duplicated.
//
// "Already in the list" means the same folder, not the same spelling of it - see
// sameFolder. Nothing in this package ever adds a location on the user's behalf,
// so every unit in the list is one the user chose deliberately.
func (m *Manager) Add(path string) (StorageUnit, error) {
	abs, err := resolveDir(path)
	if err != nil {
		return StorageUnit{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return StorageUnit{}, fmt.Errorf("%w: %s: %w", ErrUnreadablePath, abs, err)
	}

	var added StorageUnit
	err = m.mutate(func(doc *document) error {
		for _, u := range doc.units {
			if sameFolder(u.Path, abs, info) {
				return fmt.Errorf("%w: %s", ErrDuplicatePath, abs)
			}
		}
		unit := persistedUnit{ID: newID(doc.units), Name: defaultName(abs), Path: abs}
		doc.units = append(doc.units, unit)
		added = live(unit)
		return nil
	})
	if err != nil {
		return StorageUnit{}, err
	}
	return added, nil
}

// Remove drops a unit from the list by id, and does nothing else.
//
// It forgets a location; it never touches what is inside it. The folder and its
// contents are left exactly as they were, and a program that had content there
// reports it as unavailable, precisely as it would for an unplugged drive.
// Removing a unit is a statement about the user's configuration, not an
// instruction to delete anything.
func (m *Manager) Remove(id string) error {
	return m.mutate(func(doc *document) error {
		for i, u := range doc.units {
			if u.ID == id {
				doc.units = append(doc.units[:i], doc.units[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	})
}

// Reorder replaces the priority order, taking the full list of every id exactly
// once. A call whose id set does not match the current one - wrong length,
// unknown id, or a repeat - is rejected with ErrReorderMismatch and changes
// nothing, so a client working from a stale list cannot drop or invent a unit
// through a reorder.
//
// Reordering moves no content. Priority governs where new output is placed and
// nothing else; if dragging a row in a settings list relocated a library, it
// would move gigabytes and invalidate every path already recorded elsewhere.
// Migration between units is a separate, explicit, user-initiated operation.
func (m *Manager) Reorder(ids []string) error {
	return m.mutate(func(doc *document) error {
		if len(ids) != len(doc.units) {
			return fmt.Errorf("%w: got %d of %d", ErrReorderMismatch, len(ids), len(doc.units))
		}
		remaining := make(map[string]persistedUnit, len(doc.units))
		for _, u := range doc.units {
			remaining[u.ID] = u
		}
		next := make([]persistedUnit, 0, len(ids))
		for _, id := range ids {
			u, ok := remaining[id]
			if !ok {
				// Either an id that was never in the list, or one listed twice -
				// the delete below makes the second look like the first, which
				// is the right answer for both.
				return fmt.Errorf("%w: unknown or repeated id %s", ErrReorderMismatch, id)
			}
			delete(remaining, id)
			next = append(next, u)
		}
		doc.units = next
		return nil
	})
}

// Rename changes a unit's display label. The label defaults to the folder's base
// name and is the user's to edit; the id, not the name, is what other data
// refers to a unit by, so renaming is purely cosmetic and breaks nothing.
func (m *Manager) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}
	return m.mutate(func(doc *document) error {
		for i := range doc.units {
			if doc.units[i].ID == id {
				doc.units[i].Name = name
				return nil
			}
		}
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	})
}

// Destination returns the highest-priority reachable unit with at least need
// bytes free. This is the one call a job scheduler makes before writing output.
//
// The edges, stated so no caller has to guess them:
//
//   - need == 0 means "the top reachable unit" and always returns the first
//     reachable entry.
//   - The comparison is inclusive: a unit with exactly need bytes free
//     qualifies.
//   - Unreachable units are skipped.
//   - If no unit qualifies, the error is ErrNoDestination. An empty list
//     returns that same error rather than a zero unit, so a caller has one
//     failure path to handle rather than two, and "no save location has room"
//     is surfaced rather than silently doing nothing.
func (m *Manager) Destination(need uint64) (StorageUnit, error) {
	units, err := m.List()
	if err != nil {
		return StorageUnit{}, err
	}
	for _, u := range units {
		if u.Unreachable {
			continue
		}
		if u.FreeBytes >= need {
			return u, nil
		}
	}
	return StorageUnit{}, ErrNoDestination
}

// mutate runs fn against the current file contents under both the in-process
// mutex and the cross-process advisory lock, then writes the result atomically.
//
// Every mutation is a read-modify-write - Add appends to the current list,
// Reorder replaces it - so the read and the write must be inside one lock. Plain
// last-writer-wins would silently lose a concurrent Add, which is a correctness
// bug rather than a torn read.
func (m *Manager) mutate(fn func(*document) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	release, err := acquireLock(m.store.lockFile)
	if err != nil {
		return err
	}
	defer release()

	doc, err := m.store.load()
	if err != nil {
		return err
	}
	if err := fn(doc); err != nil {
		return err
	}
	return m.store.save(doc)
}

// withSpace turns persisted units into the live view callers see.
func withSpace(units []persistedUnit) []StorageUnit {
	out := make([]StorageUnit, len(units))
	for i, u := range units {
		out[i] = live(u)
	}
	return out
}

func live(u persistedUnit) StorageUnit {
	unit := StorageUnit{ID: u.ID, Name: u.Name, Path: u.Path}
	free, total, err := diskSpace(u.Path)
	if err != nil {
		// Unreachable, not dropped: the user configured this deliberately and
		// it may well come back.
		unit.Unreachable = true
		return unit
	}
	unit.FreeBytes, unit.TotalBytes = free, total
	return unit
}

// sameFolder reports whether an existing unit's path and a candidate path are
// the same folder on disk - not merely the same string.
//
// The string comparison alone is not enough. A symlink, a second mount of the
// same filesystem, or a Windows path differing only in case all name one folder
// by two spellings, and adding it twice would give one folder two ids, two rows
// in the settings list, and two entries in anything keyed by unit id.
//
// os.SameFile is the real test: it compares the identity the filesystem itself
// reports (device and inode on Unix, volume serial and file index on Windows),
// so every one of those routes resolves to the same answer. It needs both paths
// to be readable, which the candidate always is - Add has just stat'd it - and
// the existing unit usually is. When the existing unit is unreachable, an
// unplugged drive cannot be stat'd and the comparison falls back to the string
// form: two spellings of one offline folder would be accepted as two units,
// which is a cosmetic problem the user can fix, and the alternative - refusing
// to add a folder because an unrelated offline unit might turn out to be it -
// is worse.
func sameFolder(existing, candidate string, candidateInfo os.FileInfo) bool {
	if samePath(existing, candidate) {
		return true
	}
	existingInfo, err := os.Stat(existing)
	if err != nil {
		return false // unreachable: the string comparison above was the only test available
	}
	return os.SameFile(existingInfo, candidateInfo)
}

// resolveDir turns a user-supplied path into the absolute path stored in the
// list, rejecting anything that is not a readable directory.
func resolveDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrUnreadablePath, path, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrUnreadablePath, abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a folder", ErrUnreadablePath, abs)
	}
	if _, _, err := diskSpace(abs); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrUnreadablePath, abs, err)
	}
	return abs, nil
}

// defaultName is the label a newly added unit starts with. filepath.Base of a
// filesystem root is a separator, which makes a poor label, so a root falls back
// to the path itself.
func defaultName(abs string) string {
	base := filepath.Base(abs)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return abs
	}
	return base
}

// newID generates the stable opaque id for a unit: six random bytes, hex. Short
// enough to read in a config file, and re-rolled in the vanishingly unlikely
// event of a collision with an existing unit.
func newID(existing []persistedUnit) string {
	for {
		b := make([]byte, 6)
		if _, err := rand.Read(b); err != nil {
			// crypto/rand does not fail on any supported platform; if it ever
			// did, a duplicate id would be worse than a panic here.
			panic(fmt.Sprintf("storageunit: reading random bytes: %v", err))
		}
		id := hex.EncodeToString(b)
		taken := false
		for _, u := range existing {
			if u.ID == id {
				taken = true
				break
			}
		}
		if !taken {
			return id
		}
	}
}

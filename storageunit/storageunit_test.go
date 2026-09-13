package storageunit

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newTestManager returns a Manager over a temp config folder, plus that folder.
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, dir
}

// addDir makes a folder under the temp root and adds it as a unit.
func addDir(t *testing.T, m *Manager, root, name string) StorageUnit {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	u, err := m.Add(path)
	if err != nil {
		t.Fatalf("Add(%s): %v", path, err)
	}
	return u
}

func ids(units []StorageUnit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u.ID
	}
	return out
}

func TestNewCreatesEmptyList(t *testing.T) {
	m, dir := newTestManager(t)

	body, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("reading the created file: %v", err)
	}
	var doc struct {
		Version      int               `json:"version"`
		StorageUnits []json.RawMessage `json:"storageUnits"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("created file is not valid JSON: %v\n%s", err, body)
	}
	if doc.Version != SchemaVersion {
		t.Errorf("version = %d, want %d", doc.Version, SchemaVersion)
	}
	if doc.StorageUnits == nil {
		t.Error("storageUnits key is missing; a new file must carry an empty array")
	}
	if len(doc.StorageUnits) != 0 {
		t.Errorf("a new file must be empty, got %d units", len(doc.StorageUnits))
	}

	units, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(units) != 0 {
		t.Errorf("List of a new file = %d units, want 0", len(units))
	}
}

// A file the user has emptied is a deliberate empty list, and opening it again
// must not restore anything.
func TestNewNeverOverwritesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	existing := `{"version":1,"storageUnits":[{"id":"aabbccddeeff","name":"Kept","path":"/nowhere"}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	units, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(units) != 1 || units[0].ID != "aabbccddeeff" {
		t.Fatalf("New rewrote an existing list: %+v", units)
	}
	if !units[0].Unreachable {
		t.Error("a unit whose path does not exist must be Unreachable, not dropped")
	}
}

func TestAddAppendsAtLowestPriority(t *testing.T) {
	m, root := newTestManager(t)
	first := addDir(t, m, root, "first")
	second := addDir(t, m, root, "second")

	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(units), []string{first.ID, second.ID}; !equalStrings(got, want) {
		t.Errorf("order = %v, want %v: new units go to the bottom", got, want)
	}
	if units[0].Name != "first" {
		t.Errorf("default name = %q, want the folder's base name", units[0].Name)
	}
	if !filepath.IsAbs(units[0].Path) {
		t.Errorf("stored path %q is not absolute", units[0].Path)
	}
	if units[0].TotalBytes == 0 {
		t.Error("List must attach live space figures")
	}
}

func TestAddRejectsDuplicatesAndUnreadablePaths(t *testing.T) {
	m, root := newTestManager(t)
	unit := addDir(t, m, root, "media")

	if _, err := m.Add(unit.Path); !errors.Is(err, ErrDuplicatePath) {
		t.Errorf("adding the same path twice: err = %v, want ErrDuplicatePath", err)
	}
	// The same folder reached by an unclean path is the same folder.
	if _, err := m.Add(filepath.Join(root, "media", ".", "..", "media")); !errors.Is(err, ErrDuplicatePath) {
		t.Errorf("adding an unclean form of an existing path: err = %v, want ErrDuplicatePath", err)
	}
	if _, err := m.Add(filepath.Join(root, "does-not-exist")); !errors.Is(err, ErrUnreadablePath) {
		t.Errorf("adding a missing folder: err = %v, want ErrUnreadablePath", err)
	}

	file := filepath.Join(root, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add(file); !errors.Is(err, ErrUnreadablePath) {
		t.Errorf("adding a file: err = %v, want ErrUnreadablePath", err)
	}

	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Errorf("a rejected Add must not change the list; got %d units", len(units))
	}
}

func TestRemove(t *testing.T) {
	m, root := newTestManager(t)
	first := addDir(t, m, root, "first")
	second := addDir(t, m, root, "second")

	// Remove forgets a location and never touches its contents.
	marker := filepath.Join(first.Path, "keep-me")
	if err := os.WriteFile(marker, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(first.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("Remove deleted content: %v", err)
	}

	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(units), []string{second.ID}; !equalStrings(got, want) {
		t.Errorf("after Remove: %v, want %v", got, want)
	}
	if err := m.Remove("no-such-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove of an unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestReorder(t *testing.T) {
	m, root := newTestManager(t)
	a := addDir(t, m, root, "a")
	b := addDir(t, m, root, "b")
	c := addDir(t, m, root, "c")

	if err := m.Reorder([]string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(units), []string{c.ID, a.ID, b.ID}; !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	// Every rejection must leave the order exactly as it was.
	for name, call := range map[string][]string{
		"short list":  {c.ID, a.ID},
		"long list":   {c.ID, a.ID, b.ID, a.ID},
		"unknown id":  {c.ID, a.ID, "ffffffffffff"},
		"repeated id": {c.ID, a.ID, a.ID},
	} {
		if err := m.Reorder(call); !errors.Is(err, ErrReorderMismatch) {
			t.Errorf("Reorder with a %s: err = %v, want ErrReorderMismatch", name, err)
		}
		units, err := m.List()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := ids(units), []string{c.ID, a.ID, b.ID}; !equalStrings(got, want) {
			t.Errorf("a rejected Reorder (%s) changed the order: %v, want %v", name, got, want)
		}
	}
}

func TestRename(t *testing.T) {
	m, root := newTestManager(t)
	unit := addDir(t, m, root, "media")

	if err := m.Rename(unit.ID, "  Fast SSD  "); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if units[0].Name != "Fast SSD" {
		t.Errorf("name = %q, want %q", units[0].Name, "Fast SSD")
	}
	if units[0].ID != unit.ID {
		t.Error("Rename must not change the id")
	}
	if err := m.Rename(unit.ID, "   "); !errors.Is(err, ErrEmptyName) {
		t.Errorf("Rename to blank: err = %v, want ErrEmptyName", err)
	}
	if err := m.Rename("no-such-id", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Rename of an unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestDestination(t *testing.T) {
	m, root := newTestManager(t)

	// An empty list is the same failure as "nothing qualifies", not a nil unit.
	if _, err := m.Destination(0); !errors.Is(err, ErrNoDestination) {
		t.Errorf("Destination on an empty list: err = %v, want ErrNoDestination", err)
	}

	gone := addDir(t, m, root, "gone")
	reachable := addDir(t, m, root, "live")
	if err := os.Remove(gone.Path); err != nil {
		t.Fatal(err)
	}

	// The top unit is unreachable, so placement falls through to the next one.
	got, err := m.Destination(0)
	if err != nil {
		t.Fatalf("Destination(0): %v", err)
	}
	if got.ID != reachable.ID {
		t.Errorf("Destination(0) = %s, want the first reachable unit %s", got.ID, reachable.ID)
	}

	// The comparison is inclusive: exactly the free figure still qualifies.
	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if !units[0].Unreachable {
		t.Fatal("the deleted folder should read as unreachable")
	}
	free := units[1].FreeBytes
	if got, err := m.Destination(free); err != nil {
		t.Errorf("Destination(exactly free) errored: %v", err)
	} else if got.ID != reachable.ID {
		t.Errorf("Destination(exactly free) = %s, want %s", got.ID, reachable.ID)
	}

	if _, err := m.Destination(math.MaxUint64); !errors.Is(err, ErrNoDestination) {
		t.Errorf("Destination beyond capacity: err = %v, want ErrNoDestination", err)
	}
}

// Every operation reads the file, so a change another program made is visible
// without any notification.
func TestManagersShareTheFile(t *testing.T) {
	dir := t.TempDir()
	programA, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	programB, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}

	unit := addDir(t, programA, dir, "output")
	units, err := programB.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].ID != unit.ID {
		t.Fatalf("the second manager did not see the first's Add: %+v", units)
	}

	if err := programB.Remove(unit.ID); err != nil {
		t.Fatal(err)
	}
	units, err = programA.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Fatalf("the first manager did not see the second's Remove: %+v", units)
	}
}

// The lock exists because every mutation is a read-modify-write: without it,
// concurrent Adds silently lose each other.
func TestConcurrentAddsAllSurvive(t *testing.T) {
	dir := t.TempDir()
	const n = 8

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m, err := New(Options{Dir: dir})
			if err != nil {
				errs <- err
				return
			}
			path := filepath.Join(dir, "unit"+string(rune('a'+i)))
			if err := os.MkdirAll(path, 0o755); err != nil {
				errs <- err
				return
			}
			if _, err := m.Add(path); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Add: %v", err)
	}

	m, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != n {
		t.Fatalf("after %d concurrent Adds the list holds %d units; the lock is not holding", n, len(units))
	}
	seen := map[string]bool{}
	for _, u := range units {
		if seen[u.ID] {
			t.Errorf("duplicate id %s", u.ID)
		}
		seen[u.ID] = true
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Lock files are an implementation detail of the writes, but their location is
// not: the lock must be the sibling, never the file the rename replaces.
func TestLockIsTakenOnTheSibling(t *testing.T) {
	m, root := newTestManager(t)
	addDir(t, m, root, "media")

	if _, err := os.Stat(filepath.Join(m.Dir(), LockFileName)); err != nil {
		t.Errorf("expected the sibling lock file to exist: %v", err)
	}
	if !strings.HasSuffix(m.File(), FileName) {
		t.Errorf("File() = %q, want a path ending in %s", m.File(), FileName)
	}
}

// A folder reached by a second route is the same folder, and must not become a
// second unit with its own id. Nothing in the package adds a location on the
// user's behalf, so every duplicate reaching Add is one the user typed.
func TestAddRejectsTheSameFolderByAnotherRoute(t *testing.T) {
	m, root := newTestManager(t)
	unit := addDir(t, m, root, "media")

	link := filepath.Join(root, "media-link")
	if err := os.Symlink(unit.Path, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if _, err := m.Add(link); !errors.Is(err, ErrDuplicatePath) {
		t.Errorf("adding a symlink to an existing unit: err = %v, want ErrDuplicatePath", err)
	}

	// A genuinely different folder is still addable - the check must not be so
	// eager that it rejects everything.
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add(other); err != nil {
		t.Errorf("adding an unrelated folder: %v", err)
	}

	units, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Errorf("list holds %d units, want 2 (the folder and the unrelated one)", len(units))
	}
}

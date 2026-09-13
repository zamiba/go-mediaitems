// Package storageunit implements the shared StorageUnit list: the ordered set of
// folders a MediaItem suite program writes its output to, and reads its content
// back from.
//
// Every suite program reads and writes one file, so a folder receives the same
// output whichever program placed it. That is the whole point of the package:
// the behaviour is specified for the suite as a whole rather than chosen here,
// and this is its reference implementation. Two rules govern everything below
// and are stated first because both look like details worth simplifying, and
// neither is:
//
//   - Free space is never persisted. It is read live on every List and every
//     Destination, using the space available to an unprivileged caller. Stored
//     figures go stale in seconds, and two units on the same volume correctly
//     report identical numbers.
//
//   - A unit whose path cannot be read is marked Unreachable and kept. An
//     unmounted NAS or a pulled USB drive is not a reason to forget a location
//     the user deliberately configured.
//
// Priority is array order: the first entry is the highest-priority unit. There
// is no position field, which removes a whole class of index-drift bugs.
//
// # Placement versus lookup
//
// The list serves two different jobs and only one of them is about writing.
// Destination answers "where does new output go" - it walks the list in priority
// order and skips unreachable units. List answers "what locations exist" and is
// the search path for a program that owns persistent content, which must read
// across every unit including the unreachable ones: content on a disconnected
// drive is unavailable, which is a different state from missing.
//
// Nothing in this package moves content. Reordering, adding and removing change
// where new output is placed and nothing else; Remove forgets a location and
// never touches what is inside it.
package storageunit

// Layout constants for the shared configuration. The file lives at
// DefaultDir()/FileName, with the advisory lock on its sibling LockFileName.
const (
	// DirName is the folder created inside os.UserConfigDir().
	DirName = "MediaItem"

	// FileName is the shared list. Its top-level object carries a storageUnits
	// array whose order is priority order.
	FileName = "storage-units.json"

	// LockFileName is the sibling advisory lock taken for the whole
	// read-modify-write of FileName.
	//
	// The lock is on the sibling and never on FileName itself: writes replace
	// the target's inode via rename, so a lock taken on the target is a lock on
	// a file that no longer exists by the time the write lands. This is not a
	// stylistic choice and must not be "simplified" away.
	LockFileName = "storage-units.lock"

	// SchemaVersion is the version written to the file's version field.
	SchemaVersion = 1
)

// StorageUnit is one configured output folder, as seen by a caller.
//
// ID, Name and Path persist. FreeBytes, TotalBytes and Unreachable are computed
// on every read and never stored - the persisted form is a separate type, so
// writing a stale space figure to disk is not something a caller can do by
// accident.
type StorageUnit struct {
	// ID is a stable opaque identifier, generated once when the unit is added.
	// It survives renames and reordering, and is what other suite data (the
	// item index) refers to a unit by.
	ID string `json:"id"`

	// Name is the display label. It defaults to the folder's base name and is
	// user-editable through Rename.
	Name string `json:"name"`

	// Path is the absolute path to the folder.
	Path string `json:"path"`

	// FreeBytes is the space an unprivileged caller can actually write, read
	// live. Zero when Unreachable.
	FreeBytes uint64 `json:"freeBytes"`

	// TotalBytes is the capacity of the filesystem holding Path, read live.
	// Zero when Unreachable.
	TotalBytes uint64 `json:"totalBytes"`

	// Unreachable reports that Path could not be read at the moment of the
	// call - an unmounted share, a disconnected drive, a deleted folder. The
	// unit stays in the list and is skipped for placement.
	Unreachable bool `json:"unreachable"`
}

package storageunit

import "errors"

// Errors returned by the five operations. They are sentinels so a caller can
// branch on the case rather than match on message text - Seed in particular
// depends on telling ErrDuplicatePath apart from a genuine failure.
var (
	// ErrDuplicatePath is returned by Add when the list already holds the
	// resolved absolute path. Adding a folder twice is rejected, never
	// silently duplicated.
	ErrDuplicatePath = errors.New("storageunit: that folder is already a storage unit")

	// ErrUnreadablePath is returned by Add when the folder does not exist, is
	// not a directory, or its free space cannot be probed.
	ErrUnreadablePath = errors.New("storageunit: that folder cannot be read")

	// ErrNotFound is returned by Remove and Rename for an unknown id.
	ErrNotFound = errors.New("storageunit: no such storage unit")

	// ErrReorderMismatch is returned by Reorder when the given ids are not
	// exactly the current set, each once - a wrong length, an unknown id, or a
	// repeat. The whole order is replaced or nothing is, so a stale client
	// cannot drop or invent a unit through a reorder.
	ErrReorderMismatch = errors.New("storageunit: reorder must list every storage unit exactly once")

	// ErrNoDestination is returned by Destination when no reachable unit has
	// room for the requested size. An empty list returns this same error rather
	// than a zero unit, so callers have one failure path rather than two.
	ErrNoDestination = errors.New("storageunit: no storage unit has enough free space")

	// ErrEmptyName is returned by Rename for a blank label.
	ErrEmptyName = errors.New("storageunit: name cannot be empty")
)

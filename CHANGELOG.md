# Changelog

All notable changes to `github.com/zamiba/go-mediaitems`.

The module is `v0.x` while the API settles: a minor bump may break, and the
entries below say when. Both consumers should pin the same version and move
together.

## v0.4.0 — 2026-09-29

### Added

- **`itemtitle.PathSafe(s) bool`** — the path-safety half of
  `ValidFolderName`, on its own. Purely additive; nothing else changed.

  It exists because a few names in the suite are not `_itemTitle`s and must not
  be held to the standard's rule. A profile slug is the case: it is
  deliberately reductive, and it is an identity that already exists in folders
  on people's machines, so rejecting one would make an existing profile
  unreachable rather than prevent a bad one. Before this,
  `go-mediaitems-profiles` kept its own `isFolderName` for exactly that — a
  second implementation of "is this one safe folder name", which is the thing
  this module exists to stop.

  It is the security floor and nothing more: `PathSafe("CON")` and
  `PathSafe(".hack")` are both true. A test asserts that everything `Valid` or
  `ValidFolderName` accepts is also path-safe, so the floor can never become
  the looser of the two.

## v0.3.0 — 2026-09-29

### Added

- **`itemtitle`** — the MediaItem standard's `_itemTitle` rules, in one place
  because they must produce byte-identical results in every program: a title
  names the folder an item lives in on a StorageUnit *and* the same folder
  inside every profile, so two implementations that disagree by one character
  quietly file one item in two places.
  - `SanitizeValue(v)` and `Compose(values...)` — the transformation, applied
    to each field value **before** composing.
  - `Valid`, `ValidFolderName`, `ValidSafe` — the checks, applied to a
    *finished* string, which can only be accepted or rejected. A finished
    `_itemTitle` cannot be repaired by sanitizing it again, because that would
    remove its separators.
  - `SameName` (case-folded collision test) and `Reserved` (names Windows
    refuses for devices: `CON`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9`, whatever
    the extension). `ValidFolderName` rejects these, because the folder cannot
    exist on Windows; `Valid` accepts them, because the standard makes a
    reserved name a warning at the identifier level.
  - `Warnings(s) []Warning` — acceptable but worth a person's attention:
    a replacement character, a reserved name, full-width punctuation that
    resembles a character the rules remove, and a zero-width joiner beside
    Latin letters where it does nothing visible. A warning never decides
    validity.
  - `ErrEmptyValue` — a value that sanitizes to nothing, naming `safeTitle` as
    the fix.
- **`itemtitle/testdata/itemtitle.json`** — the suite's shared test table, 111
  cases as data rather than as Go test code, so Omnidex's TypeScript runs the
  same file instead of keeping a second set. Non-ASCII is written as escapes,
  enforced by a test.

### Changed

- **New dependency: `golang.org/x/text` v0.41.0**, for Unicode NFC. There is no
  NFC in the standard library, and the rule is unimplementable without it. It is
  pinned to the go-1.25 generation like everything else here, and the `go`
  directive is unchanged at 1.25.0. PortForge already had it via
  `go-mediaitems-profiles`; Digitalizer gains it.

## v0.2.0 — 2026-09-20

### Added

- **`jsonfile`** — the file layer that `storageunit` had carried privately,
  exported so dependent modules (`go-mediaitems-profiles`) use the same code
  rather than a copy: `Decode` preserving key order, `Object.Encode` with the
  file's-order-then-canonical-then-sorted rule, `String` without HTML
  escaping, `WriteAtomic`, `CreateExclusive`, `Clone`.

### Changed

- `storageunit` now uses `jsonfile`. Behaviour is identical — its own tests
  pass unchanged — and `store.go` shrank from 390 lines to 215.

## v0.1.0 — 2026-09-13

### Added

- **`storageunit`** — the shared StorageUnit list: the ordered set of output
  folders every suite program reads and writes, in one file.
  - `StorageUnit` with the three persisted fields and three live ones, where the
    live figures have no on-disk representation at all rather than being omitted
    by convention.
  - `Manager` with `List`, `Add`, `Remove`, `Reorder`, `Destination`, plus
    `Rename` (`name` is specified as user-editable). Nothing adds a location on
    the user's behalf: a new list is empty until the user picks a folder.
  - `storage-units.json` in `os.UserConfigDir()/MediaItem/`, written atomically
    via temp+rename under an advisory lock (`flock` / `LockFileEx`) on the
    sibling `storage-units.lock`.
  - Cross-platform free-space probe: `statfs` `Bavail` on Unix and
    `GetDiskFreeSpaceEx` available-to-caller on Windows, each ported from the
    consumer application that had already written that half.
  - The file's own field order is preserved, at the top level and inside each
    unit, so a hand-written or externally generated file is not rearranged by
    opening the app; new keys are appended. Unknown keys are preserved for the
    same reason, making the file's room for future suite settings real rather
    than nominal. Output is byte-stable and written with HTML escaping off, so a
    path containing `&` or `<` appears as the user typed it.
  - Duplicate detection is by filesystem identity (`os.SameFile`), so a symlink,
    a second mount, or a case-different Windows path cannot make one folder into
    two units.
  - A malformed file is an error, never silently read as an empty list.
- **`checksum`** — the standard's five checksum fields as an embeddable `Set`,
  a one-pass `Hasher` (an `io.Writer`, so a program already moving bytes hashes
  for free), `Compute*` helpers, and `Match` / `Normalize` / `Strongest`.
  Canonical form is lowercase hex; comparison is case-insensitive.

### Notes for consumers

`storageunit` is ported from an existing application implementation and its
behaviour is preserved, with three deliberate differences to check at your call
sites:

- `List` now returns an error, because it reads the file.
- The manager keeps **no in-memory copy** of the list — every operation reads
  fresh, which is what makes a second program writing the file safe.
- There is no pluggable store and no event bus. Persistence is the shared JSON
  file; publish your own change event after a mutation returns.

The free-space probe returns `(free, total)`, which is the opposite order from
one of the two donor implementations.

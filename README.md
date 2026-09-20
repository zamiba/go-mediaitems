# `go-mediaitems`

The shared Go module for the MediaItem suite: the code that has to behave
identically in every program, so that "both programs must do the same thing"
becomes "both programs call the same function."

```
import "github.com/zamiba/go-mediaitems/storageunit"
import "github.com/zamiba/go-mediaitems/checksum"
```

Consumers are the suite's Go applications. Programs in other languages are out
of scope for this module, but everything here serializes to exactly the JSON the
standard defines, so every side of the ecosystem stays wire-compatible.

**This module implements the MediaItem standard and the suite's shared
conventions; it does not invent semantics.** Nothing here is a preference — every
behaviour was specified before it was written, and a change in behaviour is a
change to the standard first and to this module second. The reasoning is carried
inline in the doc comments, so read them before changing anything that looks
redundant: several of the details most worth deleting are the ones holding a
correctness property up.

---

## What is in it

| Package | Status | What it covers |
|---|---|---|
| `storageunit` | complete | The shared StorageUnit list: the type, the five operations, `storage-units.json` with atomic writes and cross-process locking, and the cross-platform free-space probe. |
| `checksum` | complete | The standard's five checksum fields as a struct, a one-pass multi-algorithm hasher, and the matching rules. |
| `jsonfile` | complete | How the suite reads and writes a shared JSON file: the author's key order kept, unknown keys preserved, no HTML escaping, atomic and byte-stable writes. Used by `storageunit` and by dependent modules. |

Planned, in order: the shared item index, `.mediaitem.json` (de)serialization
and the MediaItem type graph, and ItemType name constants.

Anything that reads hardware, drives a subprocess, or paints a screen stays in
the application. Shared *data shapes* and the *storage/identity* logic that must
be byte-identical across programs live here.

---

## `storageunit`

The ordered list of folders a program writes output to and reads content back
from. One file, `os.UserConfigDir()/MediaItem/storage-units.json`, read and
written by every suite program.

```go
mgr, err := storageunit.Open()          // the shared list, created if absent
units, err := mgr.List()                // priority order, live space figures
dest, err := mgr.Destination(needBytes) // where this rip/install goes
```

### Public API

```go
const (
    DirName       = "MediaItem"
    FileName      = "storage-units.json"
    LockFileName  = "storage-units.lock"
    SchemaVersion = 1
)

type StorageUnit struct {
    ID   string `json:"id"`   // stable, opaque, generated once
    Name string `json:"name"` // display label, user-editable
    Path string `json:"path"` // absolute

    FreeBytes   uint64 `json:"freeBytes"`   // live, never persisted
    TotalBytes  uint64 `json:"totalBytes"`  // live, never persisted
    Unreachable bool   `json:"unreachable"` // computed, never persisted
}

type Options struct { Dir string } // empty means DefaultDir()

func DefaultDir() (string, error)
func New(opts Options) (*Manager, error)
func Open() (*Manager, error)             // New(Options{})

func (m *Manager) List() ([]StorageUnit, error)
func (m *Manager) Add(path string) (StorageUnit, error)
func (m *Manager) Remove(id string) error
func (m *Manager) Reorder(ids []string) error
func (m *Manager) Destination(need uint64) (StorageUnit, error)
func (m *Manager) Rename(id, name string) error
func (m *Manager) Dir() string
func (m *Manager) File() string

var (
    ErrDuplicatePath
    ErrUnreadablePath
    ErrNotFound
    ErrReorderMismatch
    ErrNoDestination
    ErrEmptyName
)
```

`Rename` is the one addition beyond the specified five operations: `name` is
specified as user-editable, and nothing else would let a settings screen edit it.

### The behaviour worth knowing before you call it

**A `Manager` holds no copy of the list.** Every operation reads the file, so a
change another program made a second ago is already visible and correctness never
depends on being notified. For live UI freshness, re-read on window focus or
watch the file with `fsnotify` — neither is required.

**Free space is read live, on every `List` and every `Destination`.** It is the
space available to an *unprivileged* caller: `statfs` `Bavail` on Unix (not
`Bfree`, which counts root-reserved blocks), `GetDiskFreeSpaceEx`'s
available-to-caller figure on Windows. Two units on the same volume correctly
report identical numbers.

**A unit that will not read is `Unreachable`, and stays in the list.** An
unmounted NAS or a pulled USB drive is not a reason to forget a location the user
configured. `Destination` skips unreachable units; `List` returns them, because a
program that owns persistent content must read across all of them — content on a
disconnected drive is *unavailable*, which is a different answer than *missing*,
and the user is owed the difference.

**Nothing here moves or deletes content.** Priority governs where *new* output
goes and nothing else. `Reorder` never relocates a library — if dragging a row in
a settings list moved gigabytes, it would invalidate every path already recorded
elsewhere. `Remove` forgets a location and leaves the folder and its contents
exactly as they were.

**`Destination(need)` edges**, so no caller has to guess:

- `need == 0` means "the top reachable unit" and always returns the first
  reachable entry.
- The comparison is inclusive: exactly `need` bytes free qualifies.
- Nothing qualifying is `ErrNoDestination`, never a silent no-op — the caller
  surfaces "no save location has room".
- An **empty list returns that same error**, not a zero unit, so there is one
  failure path rather than two.

**`Add` appends at the bottom.** New units are lowest priority, never highest: a
folder the user just picked must not silently capture output that was going
somewhere else. The path is resolved absolute and must be a readable directory;
a path already in the list is `ErrDuplicatePath`. Comparison is case-sensitive on
Unix and case-insensitive on Windows.

**"Already in the list" means the same folder, not the same spelling.** A
symlink, a second mount of the same filesystem, or a Windows path differing only
in case all name one folder by two routes, and `os.SameFile` catches every one of
them by comparing the identity the filesystem itself reports. One folder can
therefore never become two units with two ids. The exception is an existing unit
that is currently unreachable: an unplugged drive cannot be examined, so the
comparison falls back to matching the path text.

**`Reorder` takes every id exactly once.** A wrong length, an unknown id or a
repeat is `ErrReorderMismatch` and changes nothing, so a client working from a
stale list cannot drop or invent a unit through a reorder.

### Nothing is ever added on the user's behalf

There is no seeding, by design: a program does not contribute a default folder,
and a new list is empty until the user picks a location. The list is a record of
deliberate choices, so a program with no units shows its empty state and asks,
rather than guessing somewhere to write and being right by accident.

This matters more with several programs sharing one file than it would for one
alone. A folder auto-added by one program becomes a destination every other
program will happily write to — a choice the user never made, arriving in a
program they may not have opened yet.

### Concurrency and the file

Two programs write this file, so every mutation takes an exclusive advisory lock
(`flock` / `LockFileEx`) on the sibling `storage-units.lock` for the whole
read-modify-write, then writes to a temp file in the same directory and renames
over the target. Reads take no lock — the rename is atomic, so a reader sees the
whole old file or the whole new one.

The lock is on the **sibling**, never on `storage-units.json` itself: the atomic
write replaces the target's inode, so a lock on the target is a lock on a file
that no longer exists by the time the write lands. This is load-bearing, not
stylistic.

Two further properties, neither of which is required but both of which cost
little and prevent a class of data loss:

- **Unknown keys are preserved.** The file's keyed body exists to leave room for
  future suite settings beside the list. If a program that did not know about a
  key dropped it on the next write, that room would be fictional — so unrecognised
  top-level keys and unrecognised per-unit keys survive a rewrite intact.
- **A malformed file is an error, not an empty list.** Reading unparseable JSON
  as zero units would let the next write erase the user's configuration.

Writes are byte-stable: rewriting an unchanged list produces identical bytes, so
a config folder kept in git does not churn.

**The file's own field order is kept.** A file written by a person or by another
tool comes back with its fields in the order they chose — at the top level and
inside each unit — and anything new is appended rather than woven in. Imposing
our order instead would turn merely opening the app into a diff nobody asked for.
Paths are written literally, so a folder called `Rock & Roll` reads as itself
rather than as `Rock \u0026 Roll`.

---

## `jsonfile`

The handful of decisions layered on `encoding/json` that decide what bytes land
on disk when the suite writes a JSON file people and several programs share.
Not a JSON library; about 200 lines including the reasons.

```go
type Object struct { Order []string; Values map[string]json.RawMessage }

func Decode(data []byte) (Object, error)            // keys in document order
func (o *Object) Set(key string, value json.RawMessage)
func (o *Object) SetString(key, value string) error  // no HTML escaping
func (o Object) Clone() Object
func (o Object) Compact(canonical ...string) ([]byte, error)
func (o Object) Encode(canonical ...string) ([]byte, error)  // indented, trailing newline

func String(s string) (json.RawMessage, error)
func WriteAtomic(path string, data []byte) error
func CreateExclusive(path string, data []byte) error  // fs.ErrExist if present
```

The ordering rule, which is the reason the package exists: **a file's own key
order is kept**; keys in `canonical` that the file lacked are appended in that
order; anything else is appended sorted. A brand-new file therefore gets the
canonical order, and an existing file is never rearranged by a program that
merely read it and wrote back one field.

`Object` holds a map, so a struct copy shares it — `Clone` before setting keys
on an object you also want to keep as read.

These primitives existed as copies in two packages before this one, and the
second copy had already drifted from the first. A file format is a contract
between programs, and a contract with two implementations is two contracts.

---

## `checksum`

The standard's flat checksum fields, and the hashing that fills them in.
One program *generates* these — from physical media, from a build output — while
another *matches* a user's files against catalogue values. A shared codec is what
stops that becoming guesswork.

```go
type Set struct {
    CRC32  string `json:"crc32Checksum,omitempty"`
    MD5    string `json:"md5Checksum,omitempty"`
    SHA1   string `json:"sha1Checksum,omitempty"`
    SHA256 string `json:"sha256Checksum,omitempty"`
    SHA512 string `json:"sha512Checksum,omitempty"`
}

type Algo uint8
const (CRC32; MD5; SHA1; SHA256; SHA512)
const All = CRC32 | MD5 | SHA1 | SHA256 | SHA512

func NewHasher(algos Algo) *Hasher            // implements io.Writer
func (h *Hasher) Sum() Set

func Compute(r io.Reader) (Set, error)
func ComputeSelected(r io.Reader, algos Algo) (Set, error)
func ComputeFile(path string) (Set, error)
func ComputeFileSelected(path string, algos Algo) (Set, error)

func (s Set) Match(other Set) (ok bool, compared int)
func (s Set) Normalize() Set
func (s Set) Strongest() (field, value string)
func (s Set) Empty() bool
```

`Set` embeds into an item struct and round-trips through `.mediaitem.json`
unchanged. Empty fields are omitted: a missing checksum means "not computed",
which is not the same as a checksum of the empty string.

**Formatting is a contract.** Values are lowercase hex, no separators, CRC32
zero-padded to eight digits. Comparison is case-insensitive and ignores
surrounding whitespace, because catalogue data pasted from No-Intro or Redump
commonly arrives uppercase and a case difference is never an identity one.

**`Match` returns a count, and you must not ignore it.** It compares only the
algorithms both sets carry — a catalogue entry listing a SHA-1 and a file hashed
for SHA-256 have nothing to disagree about. `compared == 0` means the two sets had
no algorithm in common and `ok` is true only because nothing contradicted
anything. That is not a match:

```go
if ok, n := candidate.Match(want); ok && n > 0 {
    // this is the file
}
```

**Hash once, not twice.** `Hasher` is an `io.Writer`, so a program already moving
the bytes can tee them rather than reading the file again:

```go
h := checksum.NewHasher(checksum.All)
if _, err := io.Copy(io.MultiWriter(dst, h), src); err != nil { return err }
set := h.Sum()
```

SHA-512 over a 40 GB disc image costs real minutes, so `ComputeSelected` lets a
program with a reason compute less. Anything writing a catalogue entry should use
`All`.

For a track inside a media container, the checksum is computed over the **raw
elementary stream** with no container framing, so a track keeps one identity
across every mux it appears in. Extracting that stream is the caller's job; this
package hashes the bytes it is given.

---

## Versioning and how to depend on this

**Semver tags, consumed as an ordinary dependency.** No `replace` directives in
any committed `go.mod`.

```
require github.com/zamiba/go-mediaitems v0.1.0
```

The module is `v0.x` while the API settles, so a minor bump may break — the
CHANGELOG says when. Both consumers should pin the same version and move
together. `v1.0.0` waits until the type graph lands, since that is the piece most
likely to force a rename.

This is deliberately not how a sibling module in this org is consumed: that one
is untagged behind a `replace`, which works because it is developed alongside its
single application and has never had an outside consumer. This module has two
consumers on day one, in repositories that do not share a parent directory, and
inheriting that arrangement would put three repos on local-path pins and stop
anyone building outside one machine.

### Local development

To edit the module and a consumer in one pass, use a **`go.work`** in the
consumer's checkout — it overrides the `require` without touching any committed
file:

```
cd /path/to/consumer
go work init .
go work use /path/to/go-mediaitems
```

`go.work` and `go.work.sum` are gitignored on both sides. See
[`go.work.example`](./go.work.example). When you are done, `rm go.work` and the
build goes back to the tagged version — nothing to remember to strip before a
release.

**Cutting a release:** tag the module repo `vX.Y.Z`, then bump the `require` in
each consumer.

---

## Development

```
go test ./...             # unit tests
go vet ./...
GOOS=windows go build ./...   # the Windows half compiles nowhere else
GOOS=darwin  go build ./...
```

The Windows and macOS halves of the free-space probe and the file lock cannot be
exercised on Linux, so **cross-compiling all three is part of the test loop**, not
an afterthought. `go test -race ./...` needs a C toolchain.

Supported platforms: Linux, macOS, the BSDs (`//go:build unix`) and Windows.
Suite programs run on all of them, so a change to a platform file means building
for every one.

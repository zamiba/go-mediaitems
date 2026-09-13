// Package checksum implements the MediaItem standard's checksum fields: the flat
// crc32Checksum / md5Checksum / sha1Checksum / sha256Checksum / sha512Checksum
// set carried by ItemFile items, and the hashing that produces and compares them.
//
// Checksums are to ItemFiles what remote IDs are to everything else - the
// identifier that lets two independently catalogued copies of a file be
// recognised as the same file. That only works if every program in the suite
// writes them the same way, which is the reason this lives in the shared module
// rather than in each program: one program generates these while another matches
// a user's files against them, and a disagreement about digest formatting would
// show up as a library that never matches anything.
//
// Two conventions make that agreement concrete:
//
//   - Values are lowercase hex with no separators, CRC32 zero-padded to eight
//     digits. This is what Compute writes.
//   - Comparison is case-insensitive. Catalogue data pasted from No-Intro or
//     Redump commonly arrives uppercase, and a case difference is a formatting
//     difference, never an identity one.
//
// For a track inside a media container, the checksum is computed over the raw
// elementary stream as extracted, with no container framing, so a track keeps
// one identity across every mux it appears in. Feeding that stream to a Hasher
// is the caller's job; this package hashes whatever bytes it is given.
package checksum

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"strings"
)

// Algo is a bit set selecting which digests to compute.
//
// Hashing is one pass over the data whichever algorithms are chosen, but not
// every algorithm is free: SHA-512 over a 40 GB disc image costs real minutes.
// A program with a reason to compute less can say so; All is the default and
// what anything writing a catalogue entry should use.
type Algo uint8

const (
	CRC32 Algo = 1 << iota
	MD5
	SHA1
	SHA256
	SHA512
)

// All selects every algorithm the standard defines a field for.
const All = CRC32 | MD5 | SHA1 | SHA256 | SHA512

// Set is the checksum field group as it appears on an ItemFile.
//
// The fields are flat and individually queryable, exactly as the standard
// defines them, so a Set embeds cleanly into an item struct and round-trips
// through .mediaitem.json unchanged. Empty fields are omitted on marshal: a
// missing checksum means "not computed", which is different from a checksum of
// the empty string.
type Set struct {
	CRC32  string `json:"crc32Checksum,omitempty"`
	MD5    string `json:"md5Checksum,omitempty"`
	SHA1   string `json:"sha1Checksum,omitempty"`
	SHA256 string `json:"sha256Checksum,omitempty"`
	SHA512 string `json:"sha512Checksum,omitempty"`
}

// Hasher computes several digests in a single pass. It implements io.Writer, so
// a program already moving the bytes - writing a rip to disk, copying a ROM into
// place - can tee them through a Hasher and checksum for free rather than
// reading the file a second time:
//
//	h := checksum.NewHasher(checksum.All)
//	if _, err := io.Copy(io.MultiWriter(dst, h), src); err != nil {
//	    return err
//	}
//	set := h.Sum()
type Hasher struct {
	algos  Algo
	hashes map[Algo]hash.Hash
	w      io.Writer
}

// NewHasher returns a Hasher computing the selected algorithms. A zero Algo
// selects All, so a caller that forgets to choose gets the complete set rather
// than silently hashing nothing.
func NewHasher(algos Algo) *Hasher {
	if algos == 0 {
		algos = All
	}
	h := &Hasher{algos: algos, hashes: make(map[Algo]hash.Hash, 5)}
	for _, a := range []struct {
		algo Algo
		new  func() hash.Hash
	}{
		{CRC32, func() hash.Hash { return crc32.NewIEEE() }},
		{MD5, md5.New},
		{SHA1, sha1.New},
		{SHA256, sha256.New},
		{SHA512, sha512.New},
	} {
		if algos&a.algo != 0 {
			h.hashes[a.algo] = a.new()
		}
	}
	writers := make([]io.Writer, 0, len(h.hashes))
	for _, a := range []Algo{CRC32, MD5, SHA1, SHA256, SHA512} {
		if hh, ok := h.hashes[a]; ok {
			writers = append(writers, hh)
		}
	}
	h.w = io.MultiWriter(writers...)
	return h
}

// Write feeds bytes to every selected digest. It never returns an error: the
// underlying hashes cannot fail.
func (h *Hasher) Write(p []byte) (int, error) { return h.w.Write(p) }

// Sum returns the digests computed so far, lowercase hex, with unselected
// algorithms left empty. It does not reset the Hasher, so a caller may Sum
// mid-stream for a progress display and Sum again at the end.
func (h *Hasher) Sum() Set {
	var s Set
	for algo, dst := range map[Algo]*string{
		CRC32: &s.CRC32, MD5: &s.MD5, SHA1: &s.SHA1, SHA256: &s.SHA256, SHA512: &s.SHA512,
	} {
		if hh, ok := h.hashes[algo]; ok {
			*dst = hex.EncodeToString(hh.Sum(nil))
		}
	}
	return s
}

// copyBufferSize is sized for the media this suite handles - disc images and
// ROM dumps rather than config files - where the default 32 KB copy buffer turns
// a hash into a syscall benchmark.
const copyBufferSize = 1 << 20

// Compute reads r to EOF and returns every checksum the standard defines.
func Compute(r io.Reader) (Set, error) { return ComputeSelected(r, All) }

// ComputeSelected reads r to EOF and returns the selected checksums.
func ComputeSelected(r io.Reader, algos Algo) (Set, error) {
	h := NewHasher(algos)
	if _, err := io.CopyBuffer(h, r, make([]byte, copyBufferSize)); err != nil {
		return Set{}, fmt.Errorf("checksum: reading input: %w", err)
	}
	return h.Sum(), nil
}

// ComputeFile is Compute over the contents of a file.
func ComputeFile(path string) (Set, error) { return ComputeFileSelected(path, All) }

// ComputeFileSelected is ComputeSelected over the contents of a file.
func ComputeFileSelected(path string, algos Algo) (Set, error) {
	f, err := os.Open(path)
	if err != nil {
		return Set{}, fmt.Errorf("checksum: opening %s: %w", path, err)
	}
	defer f.Close()
	set, err := ComputeSelected(f, algos)
	if err != nil {
		return Set{}, fmt.Errorf("checksum: hashing %s: %w", path, err)
	}
	return set, nil
}

// Normalize returns the set with every value trimmed and lowercased, which is
// the canonical form Compute produces. Use it on checksums parsed from a
// .mediaitem.json before storing or indexing them, so an entry hand-edited in
// uppercase indexes under the same key as a computed one.
func (s Set) Normalize() Set {
	return Set{
		CRC32:  canonical(s.CRC32),
		MD5:    canonical(s.MD5),
		SHA1:   canonical(s.SHA1),
		SHA256: canonical(s.SHA256),
		SHA512: canonical(s.SHA512),
	}
}

func canonical(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// Empty reports whether the set carries no checksum at all.
func (s Set) Empty() bool {
	return s.CRC32 == "" && s.MD5 == "" && s.SHA1 == "" && s.SHA256 == "" && s.SHA512 == ""
}

// Match reports whether two sets identify the same file, and how many
// algorithms that answer is based on.
//
// It compares only the algorithms both sets carry, because a catalogue entry
// listing a SHA-1 and a file hashed for SHA-256 have nothing to disagree about.
// The count is the part callers must not ignore: compared == 0 means the two
// sets had no algorithm in common, and ok is true only because nothing
// contradicted anything. That is not a match, and treating it as one would make
// every unhashed file identical to every catalogue entry.
//
//	if ok, n := candidate.Match(want); ok && n > 0 {
//	    // this is the file
//	}
//
// Comparison is case-insensitive and ignores surrounding whitespace, so
// catalogue values pasted in uppercase match computed ones.
func (s Set) Match(other Set) (ok bool, compared int) {
	pairs := [][2]string{
		{s.CRC32, other.CRC32},
		{s.MD5, other.MD5},
		{s.SHA1, other.SHA1},
		{s.SHA256, other.SHA256},
		{s.SHA512, other.SHA512},
	}
	for _, p := range pairs {
		a, b := canonical(p[0]), canonical(p[1])
		if a == "" || b == "" {
			continue
		}
		if a != b {
			return false, compared
		}
		compared++
	}
	return true, compared
}

// Strongest returns the strongest checksum the set carries, as the standard's
// field name and its canonical value - "sha256Checksum", "a1b2…". It is the
// value to key an index by when one identifier per file is wanted.
//
// Strength order is SHA-512, SHA-256, SHA-1, MD5, CRC32. An empty set returns
// two empty strings.
func (s Set) Strongest() (field, value string) {
	for _, c := range []struct{ field, value string }{
		{"sha512Checksum", s.SHA512},
		{"sha256Checksum", s.SHA256},
		{"sha1Checksum", s.SHA1},
		{"md5Checksum", s.MD5},
		{"crc32Checksum", s.CRC32},
	} {
		if v := canonical(c.value); v != "" {
			return c.field, v
		}
	}
	return "", ""
}

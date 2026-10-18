package xet

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

const (
	// MaxShardSize bounds a shard a client uploads.
	MaxShardSize = 64 << 20

	shardHeaderVersion = 2
	shardEntrySize     = 48
	shardHeaderSize    = 48

	fileFlagWithVerification = 1 << 31
	fileFlagWithMetadataExt  = 1 << 30
)

var shardHeaderTag = [HashSize]byte{
	'H',
	'F',
	'R',
	'e',
	'p',
	'o',
	'M',
	'e',
	't',
	'a',
	'D',
	'a',
	't',
	'a',
	0,
	85,
	105,
	103,
	69,
	106,
	123,
	129,
	87,
	131,
	165,
	189,
	217,
	92,
	205,
	209,
	74,
	169,
}

var bookend = func() Hash {
	var h Hash
	for i := range h {
		h[i] = 0xff
	}

	return h
}()

// Term is a range of chunks of a xorb, part of a file.
type Term struct {
	Xorb Hash
	// Size is the length of the term's data.
	Size uint32
	// Start and End are the chunk range, End excluded.
	Start uint32
	End   uint32
	// Verification is the term's verification hash, zero when the shard
	// has none.
	Verification Hash
}

// File is a file a shard registers: its hash and how to rebuild it.
type File struct {
	Hash         Hash
	Terms        []Term
	Verification bool
	// SHA256 is the file's SHA-256 in hex, empty when the shard has none.
	SHA256 string
}

// Size is the length of the file.
func (f File) Size() uint64 {
	var total uint64
	for _, t := range f.Terms {
		total += uint64(t.Size)
	}

	return total
}

// XorbEntry is a xorb a shard lists, with its chunks.
type XorbEntry struct {
	Hash   Hash
	Chunks []Chunk
}

// Shard is a parsed shard.
type Shard struct {
	Files []File
	Xorbs []XorbEntry
}

type shardReader struct {
	src   *bufio.Reader
	entry [shardEntrySize]byte
}

func (s *shardReader) next(what string) ([]byte, error) {
	if _, err := io.ReadFull(s.src, s.entry[:]); err != nil {
		return nil, truncated(err, what)
	}

	return s.entry[:], nil
}

func entryHash(entry []byte) Hash {
	var h Hash

	copy(h[:], entry)

	return h
}

// ParseShard reads a shard, its footer optional.
func ParseShard(src io.Reader) (Shard, error) {
	s := &shardReader{
		src: bufio.NewReader(src),
	}

	header, err := s.next("shard header")
	if err != nil {
		return Shard{}, err
	}

	if !bytes.Equal(header[:HashSize], shardHeaderTag[:]) {
		return Shard{}, fmt.Errorf("%w: not a shard", ErrInvalid)
	}

	if v := binary.LittleEndian.Uint64(header[32:]); v != shardHeaderVersion {
		return Shard{}, fmt.Errorf("%w: shard version %d", ErrInvalid, v)
	}

	footerSize := binary.LittleEndian.Uint64(header[40:])

	var shard Shard

	if shard.Files, err = s.files(); err != nil {
		return Shard{}, err
	}

	if shard.Xorbs, err = s.xorbs(); err != nil {
		return Shard{}, err
	}

	rest, err := io.Copy(io.Discard, io.LimitReader(s.src, MaxShardSize))
	if err != nil {
		return Shard{}, err
	}

	if rest != 0 && (footerSize > MaxShardSize || rest != int64(footerSize)) {
		return Shard{}, fmt.Errorf("%w: %d bytes after the shard's sections", ErrInvalid, rest)
	}

	return shard, nil
}

func (s *shardReader) files() ([]File, error) {
	var files []File

	for {
		entry, err := s.next("file header")
		if err != nil {
			return nil, err
		}

		f := File{
			Hash: entryHash(entry),
		}
		if f.Hash == bookend {
			return files, nil
		}

		flags := binary.LittleEndian.Uint32(entry[32:])
		count := binary.LittleEndian.Uint32(entry[36:])

		if count == 0 {
			return nil, fmt.Errorf("%w: file %s has no terms", ErrInvalid, f.Hash)
		}

		for range count {
			entry, err := s.next("file term")
			if err != nil {
				return nil, err
			}

			t := Term{
				Xorb:  entryHash(entry),
				Size:  binary.LittleEndian.Uint32(entry[36:]),
				Start: binary.LittleEndian.Uint32(entry[40:]),
				End:   binary.LittleEndian.Uint32(entry[44:]),
			}
			if t.End <= t.Start {
				return nil, fmt.Errorf("%w: file %s has an empty chunk range", ErrInvalid, f.Hash)
			}

			f.Terms = append(f.Terms, t)
		}

		if flags&fileFlagWithVerification != 0 {
			f.Verification = true

			for i := range f.Terms {
				entry, err := s.next("file verification")
				if err != nil {
					return nil, err
				}

				f.Terms[i].Verification = entryHash(entry)
			}
		}

		if flags&fileFlagWithMetadataExt != 0 {
			entry, err := s.next("file metadata")
			if err != nil {
				return nil, err
			}

			f.SHA256 = hex.EncodeToString(entry[:HashSize])
		}

		files = append(files, f)
	}
}

func (s *shardReader) xorbs() ([]XorbEntry, error) {
	var xorbs []XorbEntry

	for {
		entry, err := s.next("xorb header")
		if err != nil {
			return nil, err
		}

		x := XorbEntry{
			Hash: entryHash(entry),
		}
		if x.Hash == bookend {
			return xorbs, nil
		}

		count := binary.LittleEndian.Uint32(entry[36:])
		if count > MaxXorbChunks {
			return nil, fmt.Errorf("%w: xorb %s lists %d chunks", ErrInvalid, x.Hash, count)
		}

		for range count {
			entry, err := s.next("xorb chunk")
			if err != nil {
				return nil, err
			}

			x.Chunks = append(x.Chunks, Chunk{
				Hash: entryHash(entry),
				Size: uint64(binary.LittleEndian.Uint32(entry[36:])),
			})
		}

		xorbs = append(xorbs, x)
	}
}

// MarshalBinary serializes the shard without a footer, as clients upload
// it. Every file carries verification hashes; xorb chunk offsets are left
// zero.
func (s Shard) MarshalBinary() ([]byte, error) {
	var out []byte

	out = append(out, shardHeaderTag[:]...)
	out = binary.LittleEndian.AppendUint64(out, shardHeaderVersion)
	out = binary.LittleEndian.AppendUint64(out, 0)

	for _, f := range s.Files {
		flags := uint32(fileFlagWithVerification)
		if f.SHA256 != "" {
			flags |= fileFlagWithMetadataExt
		}

		out = appendEntry(out, f.Hash, flags, uint32(len(f.Terms))) //nolint:gosec // test shards are small

		for _, t := range f.Terms {
			out = appendEntry(out, t.Xorb, 0, t.Size, t.Start, t.End)
		}

		for _, t := range f.Terms {
			out = appendEntry(out, t.Verification)
		}

		if f.SHA256 != "" {
			sum, err := hex.DecodeString(f.SHA256)
			if err != nil || len(sum) != HashSize {
				return nil, fmt.Errorf("%w: sha256 %q", ErrInvalid, f.SHA256)
			}

			out = appendEntry(out, Hash(sum))
		}
	}

	out = appendEntry(out, bookend)

	for _, x := range s.Xorbs {
		var total uint64
		for _, c := range x.Chunks {
			total += c.Size
		}

		//nolint:gosec // bounded by the xorb limits
		out = appendEntry(
			out,
			x.Hash,
			0,
			uint32(len(x.Chunks)),
			uint32(total),
		)

		for _, c := range x.Chunks {
			out = appendEntry(out, c.Hash, 0, uint32(c.Size)) //nolint:gosec // bounded by MaxChunkSize
		}
	}

	return appendEntry(out, bookend), nil
}

// appendEntry appends a 48-byte shard entry: a hash, then up to four
// 32-bit fields, zero-padded.
func appendEntry(out []byte, h Hash, fields ...uint32) []byte {
	start := len(out)

	out = append(out, h[:]...)
	for _, field := range fields {
		out = binary.LittleEndian.AppendUint32(out, field)
	}

	return append(out, make([]byte, shardEntrySize-(len(out)-start))...)
}

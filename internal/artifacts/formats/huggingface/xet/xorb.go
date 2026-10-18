package xet

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/pierrec/lz4/v4"
)

const (
	// MaxChunkSize bounds a chunk's data.
	MaxChunkSize = 128 << 10
	// MaxXorbSize bounds a serialized xorb.
	MaxXorbSize = 64 << 20
	// MaxXorbChunks bounds the chunks of a xorb.
	MaxXorbChunks = 8 << 10

	chunkHeaderSize   = 8
	chunkVersion      = 0
	maxCompressedSize = 2 * MaxChunkSize
	indexEntrySize    = HashSize + 8
)

// Compression is how a chunk's data is stored in a xorb.
type Compression byte

// The chunk compression schemes.
const (
	CompressionNone Compression = iota
	CompressionLZ4
	CompressionByteGrouping4LZ4
)

var xorbFooterIdent = []byte("XETBLOB")

// IndexEntry locates a chunk in a serialized xorb.
type IndexEntry struct {
	Hash Hash
	// End is the offset just past the chunk, header included.
	End uint32
	// Size is the length of the chunk's data.
	Size uint32
}

// Index lists a xorb's chunks in order.
type Index []IndexEntry

// Chunks are the index's chunks as the Merkle hashes see them.
func (x Index) Chunks() []Chunk {
	out := make([]Chunk, len(x))
	for i, e := range x {
		out[i] = Chunk{
			Hash: e.Hash,
			Size: uint64(e.Size),
		}
	}

	return out
}

// Start is the offset of chunk i in the xorb.
func (x Index) Start(i int) uint32 {
	if i == 0 {
		return 0
	}

	return x[i-1].End
}

// MarshalBinary encodes the index: per chunk, its hash, end and size.
func (x Index) MarshalBinary() ([]byte, error) {
	out := make([]byte, 0, len(x)*indexEntrySize)
	for _, e := range x {
		out = append(out, e.Hash[:]...)
		out = binary.LittleEndian.AppendUint32(out, e.End)
		out = binary.LittleEndian.AppendUint32(out, e.Size)
	}

	return out, nil
}

// ParseIndex decodes an index MarshalBinary encoded.
func ParseIndex(encoded []byte) (Index, error) {
	if len(encoded)%indexEntrySize != 0 {
		return nil, fmt.Errorf("%w: xorb index of %d bytes", ErrInvalid, len(encoded))
	}

	out := make(Index, len(encoded)/indexEntrySize)
	for i := range out {
		entry := encoded[i*indexEntrySize:]
		copy(out[i].Hash[:], entry)
		out[i].End = binary.LittleEndian.Uint32(entry[HashSize:])
		out[i].Size = binary.LittleEndian.Uint32(entry[HashSize+4:])
	}

	return out, nil
}

// XorbReader passes a serialized xorb through while checking it: every
// chunk must decode, and the chunks must hash to the expected xorb hash.
// Its Read fails instead of returning io.EOF when they do not.
type XorbReader struct {
	src      *bufio.Reader
	expected Hash
	pending  []byte
	read     int64
	index    Index
	footer   bool
	err      error
}

// NewXorbReader returns a reader checking the xorb in src against expected.
func NewXorbReader(src io.Reader, expected Hash) *XorbReader {
	return &XorbReader{
		src:      bufio.NewReaderSize(src, chunkHeaderSize+maxCompressedSize),
		expected: expected,
	}
}

// Index is the xorb's chunk index, complete once Read returned io.EOF.
func (x *XorbReader) Index() Index { return x.index }

// Read implements io.Reader.
func (x *XorbReader) Read(p []byte) (int, error) {
	for len(x.pending) == 0 {
		if x.err != nil {
			return 0, x.err
		}

		x.err = x.next()
	}

	n := copy(p, x.pending)
	x.pending = x.pending[n:]

	return n, nil
}

func (x *XorbReader) next() error {
	if x.footer {
		return x.passFooter()
	}

	peek, err := x.src.Peek(1)
	if errors.Is(err, io.EOF) {
		return x.finish()
	}

	if err != nil {
		return err
	}

	if peek[0] != chunkVersion {
		ident, _ := x.src.Peek(len(xorbFooterIdent))
		if !bytes.HasPrefix(xorbFooterIdent, ident) {
			return fmt.Errorf("%w: chunk version %d", ErrInvalid, peek[0])
		}

		x.footer = true

		return x.passFooter()
	}

	raw, entry, err := x.readChunk()
	if err != nil {
		return err
	}

	x.index = append(x.index, entry)
	x.pending = raw

	return nil
}

func (x *XorbReader) readChunk() ([]byte, IndexEntry, error) {
	if len(x.index) >= MaxXorbChunks {
		return nil, IndexEntry{}, fmt.Errorf("%w: a xorb holds at most %d chunks", ErrInvalid, MaxXorbChunks)
	}

	header := make([]byte, chunkHeaderSize)
	if _, err := io.ReadFull(x.src, header); err != nil {
		return nil, IndexEntry{}, truncated(err, "chunk header")
	}

	compressed, scheme, size, err := parseChunkHeader(header)
	if err != nil {
		return nil, IndexEntry{}, err
	}

	raw := make([]byte, chunkHeaderSize+compressed)
	copy(raw, header)

	if _, err := io.ReadFull(x.src, raw[chunkHeaderSize:]); err != nil {
		return nil, IndexEntry{}, truncated(err, "chunk data")
	}

	data, err := decompress(scheme, raw[chunkHeaderSize:], size)
	if err != nil {
		return nil, IndexEntry{}, err
	}

	x.read += int64(len(raw))
	if x.read > MaxXorbSize {
		return nil, IndexEntry{}, fmt.Errorf("%w: a xorb holds at most %d bytes", ErrInvalid, MaxXorbSize)
	}

	return raw, IndexEntry{
		Hash: ChunkHash(data),
		End:  uint32(x.read), //nolint:gosec // bounded by MaxXorbSize
		Size: uint32(size),   //nolint:gosec // bounded by MaxChunkSize
	}, nil
}

func (x *XorbReader) passFooter() error {
	buf := make([]byte, 32<<10)

	n, err := x.src.Read(buf)
	x.read += int64(n)

	if x.read > MaxXorbSize {
		return fmt.Errorf("%w: a xorb holds at most %d bytes", ErrInvalid, MaxXorbSize)
	}

	if n > 0 {
		x.pending = buf[:n]
		return nil
	}

	if errors.Is(err, io.EOF) {
		return x.finish()
	}

	if err == nil {
		return io.ErrNoProgress
	}

	return err
}

func (x *XorbReader) finish() error {
	if len(x.index) == 0 {
		return fmt.Errorf("%w: the xorb holds no chunks", ErrInvalid)
	}

	if got := XorbHash(x.index.Chunks()); got != x.expected {
		return fmt.Errorf("%w: the chunks hash to xorb %s, not %s", ErrInvalid, got, x.expected)
	}

	return io.EOF
}

func truncated(err error, what string) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: truncated %s", ErrInvalid, what)
	}

	return err
}

func parseChunkHeader(header []byte) (compressed int, scheme Compression, size int, err error) {
	if header[0] != chunkVersion {
		return 0, 0, 0, fmt.Errorf("%w: chunk version %d", ErrInvalid, header[0])
	}

	compressed = int(header[1]) | int(header[2])<<8 | int(header[3])<<16
	scheme = Compression(header[4])
	size = int(header[5]) | int(header[6])<<8 | int(header[7])<<16

	switch {
	case scheme > CompressionByteGrouping4LZ4:
		return 0, 0, 0, fmt.Errorf("%w: chunk compression %d", ErrInvalid, scheme)
	case size == 0 || size > MaxChunkSize:
		return 0, 0, 0, fmt.Errorf("%w: chunk of %d bytes", ErrInvalid, size)
	case compressed == 0 || compressed > maxCompressedSize:
		return 0, 0, 0, fmt.Errorf("%w: compressed chunk of %d bytes", ErrInvalid, compressed)
	}

	return compressed, scheme, size, nil
}

func decompress(scheme Compression, stored []byte, size int) ([]byte, error) {
	if scheme == CompressionNone {
		if len(stored) != size {
			return nil, fmt.Errorf("%w: uncompressed chunk of %d bytes claims %d", ErrInvalid, len(stored), size)
		}

		return stored, nil
	}

	data := make([]byte, size+1)

	n, err := io.ReadFull(lz4.NewReader(bytes.NewReader(stored)), data)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("%w: chunk does not decompress: %w", ErrInvalid, err)
	}

	if n != size {
		return nil, fmt.Errorf("%w: chunk decompresses to %d bytes, not %d", ErrInvalid, n, size)
	}

	data = data[:n]
	if scheme == CompressionByteGrouping4LZ4 {
		data = byteUngroup4(data)
	}

	return data, nil
}

// byteGroupSizes are the lengths of the four groups byte grouping splits n
// bytes into.
func byteGroupSizes(n int) [4]int {
	split, rem := n/4, n%4

	return [4]int{
		split + min(1, rem),
		split + min(1, max(rem-1, 0)),
		split + min(1, max(rem-2, 0)),
		split,
	}
}

func byteUngroup4(grouped []byte) []byte {
	sizes := byteGroupSizes(len(grouped))
	out := make([]byte, len(grouped))

	offset := 0

	for group, size := range sizes {
		for i := range size {
			out[4*i+group] = grouped[offset+i]
		}

		offset += size
	}

	return out
}

func byteGroup4(data []byte) []byte {
	sizes := byteGroupSizes(len(data))
	out := make([]byte, len(data))

	offset := 0

	for group, size := range sizes {
		for i := range size {
			out[offset+i] = data[4*i+group]
		}

		offset += size
	}

	return out
}

// ChunkReader decodes the chunks of a range of a serialized xorb.
type ChunkReader struct {
	src *bufio.Reader
}

// NewChunkReader reads chunks from src, which starts at a chunk header.
func NewChunkReader(src io.Reader) *ChunkReader {
	return &ChunkReader{
		src: bufio.NewReaderSize(src, chunkHeaderSize+maxCompressedSize),
	}
}

// Next returns the next chunk's data.
func (c *ChunkReader) Next() ([]byte, error) {
	header := make([]byte, chunkHeaderSize)
	if _, err := io.ReadFull(c.src, header); err != nil {
		return nil, truncated(err, "chunk header")
	}

	compressed, scheme, size, err := parseChunkHeader(header)
	if err != nil {
		return nil, err
	}

	stored := make([]byte, compressed)
	if _, err := io.ReadFull(c.src, stored); err != nil {
		return nil, truncated(err, "chunk data")
	}

	return decompress(scheme, stored, size)
}

// AppendChunk serializes data as a chunk stored with scheme, appended to
// xorb.
func AppendChunk(xorb, data []byte, scheme Compression) ([]byte, error) {
	stored := data

	if scheme != CompressionNone {
		source := data
		if scheme == CompressionByteGrouping4LZ4 {
			source = byteGroup4(data)
		}

		var buf bytes.Buffer

		w := lz4.NewWriter(&buf)
		if _, err := w.Write(source); err != nil {
			return nil, fmt.Errorf("compress chunk: %w", err)
		}

		if err := w.Close(); err != nil {
			return nil, fmt.Errorf("compress chunk: %w", err)
		}

		stored = buf.Bytes()
	}

	if len(data) == 0 || len(data) > MaxChunkSize || len(stored) > maxCompressedSize {
		return nil, fmt.Errorf("%w: chunk of %d bytes", ErrInvalid, len(data))
	}

	xorb = append(xorb, chunkVersion)
	xorb = appendUint24(xorb, len(stored))
	xorb = append(xorb, byte(scheme))
	xorb = appendUint24(xorb, len(data))

	return append(xorb, stored...), nil
}

// appendUint24 appends v, below 1<<24, as three little-endian bytes.
func appendUint24(out []byte, v int) []byte {
	//nolint:gosec // callers bound v below 1<<24
	return binary.LittleEndian.AppendUint32(out, uint32(v))[:len(out)+3]
}

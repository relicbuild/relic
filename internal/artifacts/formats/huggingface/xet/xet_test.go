package xet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"testing"
)

const (
	referenceFileHash  = "118a53328412787fee04011dcf82fdc4acf3a4a1eddec341c910d30a306aaf97"
	referenceXorbHash  = "eea25d6ee393ccae385820daed127b96ef0ea034dfb7cf6da3a950ce334b7632"
	referenceRangeHash = "d81c11b1fc9bc2a25587108c675bbfe65ca2e5d350b0cd92c58329fcc8444178"
	referenceChunkHash = "099cb228194fe640e36a6c7d274ee5ed3a714ccd557a0951d9b6b43a7292b5d1"
)

func TestHashText(t *testing.T) {
	t.Parallel()

	var h Hash
	for i := range h {
		h[i] = byte(i)
	}

	const want = "07060504030201000f0e0d0c0b0a090817161514131211101f1e1d1c1b1a1918"
	if got := h.String(); got != want {
		t.Fatalf("String() = %s, want %s", got, want)
	}

	parsed, err := ParseHash(want)
	if err != nil || parsed != h {
		t.Fatalf("ParseHash(%s) = %v, %v; want %v", want, parsed, err, h)
	}

	for _, bad := range []string{
		"",
		"07060504",
		"07060504030201000F0E0D0C0B0A090817161514131211101F1E1D1C1B1A1918",
		"g7060504030201000f0e0d0c0b0a090817161514131211101f1e1d1c1b1a1918",
	} {
		if _, err := ParseHash(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseHash(%q) error = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestChunkHashMatchesTheReference(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("testdata/" + referenceChunkHash + ".chunk")
	if err != nil {
		t.Fatal(err)
	}

	if got := ChunkHash(data).String(); got != referenceChunkHash {
		t.Fatalf("ChunkHash = %s, want %s", got, referenceChunkHash)
	}
}

func referenceShard(t *testing.T) Shard {
	t.Helper()

	f, err := os.Open("testdata/reference.shard")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = f.Close()
	})

	shard, err := ParseShard(f)
	if err != nil {
		t.Fatal(err)
	}

	return shard
}

func TestReferenceShardHashes(t *testing.T) {
	t.Parallel()

	shard := referenceShard(t)
	if len(shard.Files) != 1 || len(shard.Xorbs) != 1 {
		t.Fatalf("shard has %d files and %d xorbs, want 1 and 1", len(shard.Files), len(shard.Xorbs))
	}

	file, xorb := shard.Files[0], shard.Xorbs[0]

	tests := []struct {
		name string
		got  Hash
		want string
	}{
		{
			"file hash in the shard",
			file.Hash,
			referenceFileHash,
		},
		{
			"xorb hash in the shard",
			xorb.Hash,
			referenceXorbHash,
		},
		{
			"computed xorb hash",
			XorbHash(xorb.Chunks),
			referenceXorbHash,
		},
		{
			"computed file hash",
			FileHash(xorb.Chunks),
			referenceFileHash,
		},
		{
			"computed range hash",
			VerificationHash(chunkHashes(xorb.Chunks)),
			referenceRangeHash,
		},
	}
	for _, tt := range tests {
		if tt.got.String() != tt.want {
			t.Errorf("%s = %s, want %s", tt.name, tt.got, tt.want)
		}
	}

	if !file.Verification {
		t.Fatal("the file has no verification hashes")
	}

	for _, term := range file.Terms {
		want := VerificationHash(chunkHashes(xorb.Chunks[term.Start:term.End]))
		if term.Verification != want {
			t.Errorf("term [%d, %d) verification = %s, want %s", term.Start, term.End, term.Verification, want)
		}
	}

	if file.Size() != 63527244 {
		t.Errorf("file size = %d, want 63527244", file.Size())
	}
}

func chunkHashes(chunks []Chunk) []Hash {
	out := make([]Hash, len(chunks))
	for i, c := range chunks {
		out[i] = c.Hash
	}

	return out
}

func TestShardRoundTrip(t *testing.T) {
	t.Parallel()

	shard := referenceShard(t)
	shard.Files[0].SHA256 = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

	encoded, err := shard.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseShard(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}

	if parsed.Files[0].SHA256 != shard.Files[0].SHA256 || len(parsed.Files[0].Terms) != len(shard.Files[0].Terms) {
		t.Fatalf("round trip lost the file: %+v", parsed.Files[0])
	}

	if XorbHash(parsed.Xorbs[0].Chunks).String() != referenceXorbHash {
		t.Fatal("round trip lost the xorb's chunks")
	}
}

func TestParseShardRejectsMalformedShards(t *testing.T) {
	t.Parallel()

	valid, err := os.ReadFile("testdata/reference.shard")
	if err != nil {
		t.Fatal(err)
	}

	wrongVersion := bytes.Clone(valid)
	wrongVersion[32] = 3

	tests := []struct {
		name  string
		shard []byte
	}{
		{
			"empty",
			nil,
		},
		{
			"wrong tag",
			append([]byte("not a shard at all"), valid[18:]...),
		},
		{
			"wrong version",
			wrongVersion,
		},
		{
			"truncated",
			valid[:len(valid)-10],
		},
		{
			"trailing bytes",
			append(bytes.Clone(valid), 1, 2, 3),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := ParseShard(bytes.NewReader(tt.shard)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ParseShard error = %v, want ErrInvalid", err)
			}
		})
	}
}

func testChunks() [][]byte {
	text := bytes.Repeat([]byte("relic xet chunk "), 4000)

	floats := make([]byte, 0, 4*20001)
	for i := range 20001 {
		floats = binary.LittleEndian.AppendUint32(floats, math.Float32bits(float32(i)*0.25))
	}

	noise := make([]byte, MaxChunkSize)
	_, _ = rand.NewChaCha8([32]byte{
		1,
	}).Read(noise)

	return [][]byte{
		text,
		floats[:len(floats)-1],
		noise,
		[]byte("x"),
	}
}

func testXorb(t *testing.T, schemes ...Compression) ([]byte, Hash, Index) {
	t.Helper()

	var (
		xorb []byte
		err  error
	)

	chunks := make([]Chunk, 0, len(testChunks()))
	index := make(Index, 0, len(testChunks()))

	for i, data := range testChunks() {
		xorb, err = AppendChunk(xorb, data, schemes[i%len(schemes)])
		if err != nil {
			t.Fatal(err)
		}

		chunks = append(chunks, Chunk{
			Hash: ChunkHash(data),
			Size: uint64(len(data)),
		})
		index = append(index, IndexEntry{
			Hash: ChunkHash(data),
			End:  uint32(len(xorb)), //nolint:gosec // small test xorb
			Size: uint32(len(data)), //nolint:gosec // small test chunk
		})
	}

	return xorb, XorbHash(chunks), index
}

func TestXorbReaderChecksEveryCompression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schemes []Compression
	}{
		{
			"none",
			[]Compression{
				CompressionNone,
			},
		},
		{
			"lz4",
			[]Compression{
				CompressionLZ4,
			},
		},
		{
			"byte grouping and lz4",
			[]Compression{
				CompressionByteGrouping4LZ4,
			},
		},
		{
			"mixed",
			[]Compression{
				CompressionNone,
				CompressionLZ4,
				CompressionByteGrouping4LZ4,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			xorb, hash, index := testXorb(t, tt.schemes...)

			reader := NewXorbReader(bytes.NewReader(xorb), hash)

			passed, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(passed, xorb) {
				t.Fatal("the reader changed the xorb")
			}

			if !slicesEqual(reader.Index(), index) {
				t.Fatalf("index = %+v, want %+v", reader.Index(), index)
			}

			encoded, err := reader.Index().MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}

			parsed, err := ParseIndex(encoded)
			if err != nil || !slicesEqual(parsed, index) {
				t.Fatalf("ParseIndex = %+v, %v", parsed, err)
			}

			chunks := NewChunkReader(bytes.NewReader(xorb[index.Start(1):]))
			for _, want := range testChunks()[1:] {
				got, err := chunks.Next()
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("ChunkReader.Next = %d bytes, %v; want %d bytes", len(got), err, len(want))
				}
			}
		})
	}
}

func slicesEqual(a, b Index) bool {
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

func TestXorbReaderAcceptsAFooter(t *testing.T) {
	t.Parallel()

	xorb, hash, _ := testXorb(t, CompressionLZ4)
	xorb = append(xorb, "XETBLOB footer bytes"...)

	passed, err := io.ReadAll(NewXorbReader(bytes.NewReader(xorb), hash))
	if err != nil || !bytes.Equal(passed, xorb) {
		t.Fatalf("ReadAll = %d bytes, %v", len(passed), err)
	}
}

func TestXorbReaderRejectsMalformedXorbs(t *testing.T) {
	t.Parallel()

	xorb, hash, _ := testXorb(t, CompressionLZ4)

	badVersion := bytes.Clone(xorb)
	badVersion[0] = 1

	badScheme := bytes.Clone(xorb)
	badScheme[4] = 9

	corrupt := bytes.Clone(xorb)
	corrupt[len(corrupt)-1] ^= 0xff

	oversized, err := AppendChunk(nil, []byte("small"), CompressionNone)
	if err != nil {
		t.Fatal(err)
	}

	oversized[5], oversized[6], oversized[7] = 0, 0, 4

	tests := []struct {
		name string
		xorb []byte
		hash Hash
	}{
		{
			"empty",
			nil,
			hash,
		},
		{
			"wrong hash",
			xorb,
			Hash{
				1,
			},
		},
		{
			"truncated",
			xorb[:len(xorb)-3],
			hash,
		},
		{
			"bad chunk version",
			badVersion,
			hash,
		},
		{
			"bad compression",
			badScheme,
			hash,
		},
		{
			"corrupt data",
			corrupt,
			hash,
		},
		{
			"chunk over the size limit",
			oversized,
			hash,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := io.ReadAll(NewXorbReader(bytes.NewReader(tt.xorb), tt.hash)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ReadAll error = %v, want ErrInvalid", err)
			}
		})
	}
}

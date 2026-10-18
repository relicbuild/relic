package xet

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
	"os"
	"slices"
	"testing"
)

// clientFile is the file hf_xet uploaded to make testdata/client.xorb and
// testdata/client.shard.
func clientFile() []byte {
	line := []byte("relic xet reference file, made by the real hf_xet client\n")
	text := bytes.Repeat(line, 11<<20/len(line)+1)[:11<<20]

	floats := make([]byte, 0, 4*65536)
	for i := range 65536 {
		floats = binary.LittleEndian.AppendUint32(floats, math.Float32bits(float32(i%4096)*0.125))
	}

	return append(text, floats...)
}

func TestClientUploadRebuildsTheFile(t *testing.T) {
	t.Parallel()

	shardFile, err := os.Open("testdata/client.shard")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = shardFile.Close()
	}()

	shard, err := ParseShard(shardFile)
	if err != nil {
		t.Fatal(err)
	}

	if len(shard.Files) != 1 || len(shard.Xorbs) != 1 {
		t.Fatalf("shard has %d files and %d xorbs, want 1 and 1", len(shard.Files), len(shard.Xorbs))
	}

	xorb, err := os.ReadFile("testdata/client.xorb")
	if err != nil {
		t.Fatal(err)
	}

	reader := NewXorbReader(bytes.NewReader(xorb), shard.Xorbs[0].Hash)
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatal(err)
	}

	index := reader.Index()
	if !slices.Equal(index.Chunks(), shard.Xorbs[0].Chunks) {
		t.Fatal("the xorb's chunks differ from the ones the shard lists")
	}

	file := shard.Files[0]

	var (
		rebuilt []byte
		chunks  []Chunk
	)

	for _, term := range file.Terms {
		taken := index[term.Start:term.End]
		if VerificationHash(chunkHashes(taken.Chunks())) != term.Verification {
			t.Fatalf("term [%d, %d) has a wrong verification hash", term.Start, term.End)
		}

		chunks = append(chunks, taken.Chunks()...)

		decoder := NewChunkReader(bytes.NewReader(xorb[index.Start(int(term.Start)):]))
		for range taken {
			data, err := decoder.Next()
			if err != nil {
				t.Fatal(err)
			}

			rebuilt = append(rebuilt, data...)
		}
	}

	if FileHash(chunks) != file.Hash {
		t.Fatal("the file's chunks do not hash to its file hash")
	}

	want := clientFile()
	if !bytes.Equal(rebuilt, want) {
		t.Fatalf("rebuilt %d bytes differing from the %d uploaded", len(rebuilt), len(want))
	}

	sum := sha256.Sum256(want)
	if file.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("shard SHA-256 = %s, want %x", file.SHA256, sum)
	}
}

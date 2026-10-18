// Package xettest builds Xet uploads for tests: a xorb holding files'
// chunks and a shard registering the files.
package xettest

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"relic/internal/artifacts/formats/huggingface/xet"
)

// ChunkSize is the length of the chunks Build cuts files into.
const ChunkSize = 64 << 10

// Upload is what a client uploads for some files.
type Upload struct {
	Xorb     []byte
	XorbHash xet.Hash
	Shard    xet.Shard
}

// ShardBytes is the serialized shard.
func (u Upload) ShardBytes(t testing.TB) []byte {
	t.Helper()

	encoded, err := u.Shard.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

// Build packs files into one xorb, their chunks compressed with LZ4, and
// registers each file in the shard with one term.
func Build(t testing.TB, files ...[]byte) Upload {
	t.Helper()

	var (
		upload Upload
		err    error
	)

	chunks := make([]xet.Chunk, 0, len(files))

	for _, data := range files {
		start := len(chunks)

		var fileChunks []xet.Chunk

		for offset := 0; offset < len(data); offset += ChunkSize {
			piece := data[offset:min(offset+ChunkSize, len(data))]

			upload.Xorb, err = xet.AppendChunk(upload.Xorb, piece, xet.CompressionLZ4)
			if err != nil {
				t.Fatal(err)
			}

			fileChunks = append(fileChunks, xet.Chunk{
				Hash: xet.ChunkHash(piece),
				Size: uint64(len(piece)),
			})
		}

		chunks = append(chunks, fileChunks...)

		hashes := make([]xet.Hash, len(fileChunks))
		for i, c := range fileChunks {
			hashes[i] = c.Hash
		}

		sum := sha256.Sum256(data)
		upload.Shard.Files = append(upload.Shard.Files, xet.File{
			Hash: xet.FileHash(fileChunks),
			Terms: []xet.Term{
				{
					Size:         uint32(len(data)),   //nolint:gosec // small test files
					Start:        uint32(start),       //nolint:gosec // small test files
					End:          uint32(len(chunks)), //nolint:gosec // small test files
					Verification: xet.VerificationHash(hashes),
				},
			},
			Verification: true,
			SHA256:       hex.EncodeToString(sum[:]),
		})
	}

	upload.XorbHash = xet.XorbHash(chunks)
	for i := range upload.Shard.Files {
		upload.Shard.Files[i].Terms[0].Xorb = upload.XorbHash
	}

	upload.Shard.Xorbs = []xet.XorbEntry{
		{
			Hash:   upload.XorbHash,
			Chunks: chunks,
		},
	}

	return upload
}

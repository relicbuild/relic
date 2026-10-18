// Package xet implements the binary formats and hashes of Hugging Face's
// Xet storage protocol: chunk, xorb, file and term verification hashes,
// serialized xorbs and shards.
package xet

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"lukechampine.com/blake3"
)

// HashSize is the length of every Xet hash in bytes.
const HashSize = 32

// Hash is a Xet hash. Its text form writes each of its four 8-byte words as
// a little-endian 64-bit number in hex.
type Hash [HashSize]byte

// ErrInvalid marks data that does not follow the Xet formats.
var ErrInvalid = errors.New("invalid xet data")

var (
	dataKey = [HashSize]byte{
		102,
		151,
		245,
		119,
		91,
		149,
		80,
		222,
		49,
		53,
		203,
		172,
		165,
		151,
		24,
		28,
		157,
		228,
		33,
		16,
		155,
		235,
		43,
		88,
		180,
		208,
		176,
		75,
		147,
		173,
		242,
		41,
	}
	internalNodeKey = [HashSize]byte{
		1,
		126,
		197,
		199,
		165,
		71,
		41,
		150,
		253,
		148,
		102,
		102,
		180,
		138,
		2,
		230,
		93,
		221,
		83,
		111,
		55,
		199,
		109,
		210,
		248,
		99,
		82,
		230,
		74,
		83,
		113,
		63,
	}
	verificationKey = [HashSize]byte{
		127,
		24,
		87,
		214,
		206,
		86,
		237,
		102,
		18,
		127,
		249,
		19,
		231,
		165,
		195,
		243,
		164,
		205,
		38,
		213,
		181,
		219,
		73,
		230,
		65,
		36,
		152,
		127,
		40,
		251,
		148,
		195,
	}
	fileKey = [HashSize]byte{}
)

// String is the hash's text form, used in API paths and headers.
func (h Hash) String() string {
	var out [HashSize * 2]byte

	for word := range 4 {
		value := binary.LittleEndian.Uint64(h[word*8:])
		hex.Encode(out[word*16:], binary.BigEndian.AppendUint64(nil, value))
	}

	return string(out[:])
}

// ParseHash reads a hash's text form: 64 lowercase hex digits.
func ParseHash(text string) (Hash, error) {
	var h Hash

	if len(text) != HashSize*2 {
		return h, fmt.Errorf("%w: hash %q is not 64 hex digits", ErrInvalid, text)
	}

	for word := range 4 {
		digits := text[word*16 : word*16+16]
		for _, c := range digits {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return h, fmt.Errorf("%w: hash %q is not lowercase hex", ErrInvalid, text)
			}
		}

		value, err := strconv.ParseUint(digits, 16, 64)
		if err != nil {
			return h, fmt.Errorf("%w: hash %q: %w", ErrInvalid, text, err)
		}

		binary.LittleEndian.PutUint64(h[word*8:], value)
	}

	return h, nil
}

func keyedHash(key *[HashSize]byte, data []byte) Hash {
	hasher := blake3.New(HashSize, key[:])
	_, _ = hasher.Write(data)

	var h Hash

	copy(h[:], hasher.Sum(nil))

	return h
}

// ChunkHash is the hash of a chunk's data.
func ChunkHash(data []byte) Hash { return keyedHash(&dataKey, data) }

// Chunk is a chunk as the Merkle hashes see it: its hash and its length.
type Chunk struct {
	Hash Hash
	Size uint64
}

const meanBranchingFactor = 4

// nextMergeCut is how many of nodes the next internal node takes: at least
// two, at most nine, ending at the first node whose hash is a multiple of
// the branching factor.
func nextMergeCut(nodes []Chunk) int {
	if len(nodes) <= 2 {
		return len(nodes)
	}

	end := min(2*meanBranchingFactor+1, len(nodes))
	for i := 2; i < end; i++ {
		if binary.LittleEndian.Uint64(nodes[i].Hash[24:])%meanBranchingFactor == 0 {
			return i + 1
		}
	}

	return end
}

func mergedNode(nodes []Chunk) Chunk {
	text := make([]byte, 0, len(nodes)*90)

	var total uint64

	for _, n := range nodes {
		text = append(text, n.Hash.String()...)
		text = append(text, " : "...)
		text = strconv.AppendUint(text, n.Size, 10)
		text = append(text, '\n')
		total += n.Size
	}

	return Chunk{
		Hash: keyedHash(&internalNodeKey, text),
		Size: total,
	}
}

func merkleRoot(chunks []Chunk) Hash {
	if len(chunks) == 0 {
		return Hash{}
	}

	level := chunks
	for len(level) > 1 {
		next := make([]Chunk, 0, len(level)/2+1)

		for start := 0; start < len(level); {
			end := start + nextMergeCut(level[start:])
			next = append(next, mergedNode(level[start:end]))
			start = end
		}

		level = next
	}

	return level[0].Hash
}

// XorbHash is the hash of a xorb holding chunks, in order.
func XorbHash(chunks []Chunk) Hash { return merkleRoot(chunks) }

// FileHash is the hash of a file made of chunks, in order: the file id.
func FileHash(chunks []Chunk) Hash {
	if len(chunks) == 0 {
		return Hash{}
	}

	root := merkleRoot(chunks)

	return keyedHash(&fileKey, root[:])
}

// VerificationHash is the hash a shard gives a term: over the hashes of the
// chunks the term takes from its xorb.
func VerificationHash(chunkHashes []Hash) Hash {
	joined := make([]byte, 0, len(chunkHashes)*HashSize)
	for _, h := range chunkHashes {
		joined = append(joined, h[:]...)
	}

	return keyedHash(&verificationKey, joined)
}

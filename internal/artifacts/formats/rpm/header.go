package rpm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

const (
	leadSize       = 96
	introSize      = 16
	indexEntrySize = 16
)

const (
	typeNull = iota
	typeChar
	typeInt8
	typeInt16
	typeInt32
	typeInt64
	typeString
	typeBinary
	typeStringArray
	typeI18NString
)

var (
	leadMagic = []byte{
		0xed,
		0xab,
		0xee,
		0xdb,
	}
	headerMagic = []byte{
		0x8e,
		0xad,
		0xe8,
	}
)

var errTruncated = errors.New("truncated")

// header is one header structure of an RPM file: the signature or the main header.
type header struct {
	numbers map[uint32][]int64
	strings map[uint32][]string
	size    int
}

func (h header) number(id uint32) int64 {
	if v := h.numbers[id]; len(v) > 0 {
		return v[0]
	}

	return 0
}

func (h header) text(id uint32) string {
	if v := h.strings[id]; len(v) > 0 {
		return v[0]
	}

	return ""
}

func readLead(r io.Reader) error {
	lead := make([]byte, leadSize)
	if _, err := io.ReadFull(r, lead); err != nil {
		return fmt.Errorf("lead: %w", errTruncated)
	}

	if !bytes.Equal(lead[:4], leadMagic) {
		return errors.New("not an rpm file")
	}

	if major := lead[4]; major < 3 || major > 4 {
		return fmt.Errorf("unsupported rpm version %d", major)
	}

	return nil
}

func readHeader(r io.Reader, padded bool) (header, error) {
	intro := make([]byte, introSize)
	if _, err := io.ReadFull(r, intro); err != nil {
		return header{}, fmt.Errorf("header: %w", errTruncated)
	}

	if !bytes.Equal(intro[:3], headerMagic) {
		return header{}, errors.New("no header magic")
	}

	entries := int64(binary.BigEndian.Uint32(intro[8:12]))
	storeSize := int64(binary.BigEndian.Uint32(intro[12:16]))

	if entries*indexEntrySize+storeSize > maxHeaderBytes {
		return header{}, fmt.Errorf("header of %d entries and %d bytes is too large", entries, storeSize)
	}

	index := make([]byte, entries*indexEntrySize)
	if _, err := io.ReadFull(r, index); err != nil {
		return header{}, fmt.Errorf("header index: %w", errTruncated)
	}

	store := make([]byte, storeSize)
	if _, err := io.ReadFull(r, store); err != nil {
		return header{}, fmt.Errorf("header store: %w", errTruncated)
	}

	h := header{
		numbers: map[uint32][]int64{},
		strings: map[uint32][]string{},
		size:    introSize + len(index) + len(store),
	}

	for entry := range slices.Chunk(index, indexEntrySize) {
		if err := h.add(entry, store); err != nil {
			return header{}, err
		}
	}

	if padded {
		padding := (8 - storeSize%8) % 8
		if _, err := io.CopyN(io.Discard, r, padding); err != nil {
			return header{}, fmt.Errorf("header padding: %w", errTruncated)
		}

		h.size += int(padding)
	}

	return h, nil
}

func (h header) add(entry, store []byte) error {
	id := binary.BigEndian.Uint32(entry[0:4])
	kind := binary.BigEndian.Uint32(entry[4:8])
	offset := int64(binary.BigEndian.Uint32(entry[8:12]))
	count := int64(binary.BigEndian.Uint32(entry[12:16]))

	if count < 1 || offset >= int64(len(store)) {
		return fmt.Errorf("tag %d: %d values at offset %d of a %d-byte store", id, count, offset, len(store))
	}

	switch kind {
	case typeNull, typeChar, typeInt8, typeBinary:
		if offset+count > int64(len(store)) {
			return fmt.Errorf("tag %d: %w", id, errTruncated)
		}
	case typeInt16, typeInt32, typeInt64:
		values, err := numberValues(store[offset:], kind, count)
		if err != nil {
			return fmt.Errorf("tag %d: %w", id, err)
		}

		h.numbers[id] = values
	case typeString, typeStringArray, typeI18NString:
		values, err := stringValues(store[offset:], count)
		if err != nil {
			return fmt.Errorf("tag %d: %w", id, err)
		}

		h.strings[id] = values
	default:
		return fmt.Errorf("tag %d: unknown type %d", id, kind)
	}

	return nil
}

func numberValues(store []byte, kind uint32, count int64) ([]int64, error) {
	width := map[uint32]int64{
		typeInt16: 2,
		typeInt32: 4,
		typeInt64: 8,
	}[kind]

	if count*width > int64(len(store)) {
		return nil, errTruncated
	}

	values := make([]int64, count)
	for i := range values {
		value := store[int64(i)*width:]

		switch width {
		case 2:
			values[i] = int64(binary.BigEndian.Uint16(value))
		case 4:
			values[i] = int64(binary.BigEndian.Uint32(value))
		default:
			//nolint:gosec // RPM stores sizes and times, well below 2^63
			values[i] = int64(binary.BigEndian.Uint64(value))
		}
	}

	return values, nil
}

func stringValues(store []byte, count int64) ([]string, error) {
	if count > int64(len(store)) {
		return nil, errTruncated
	}

	values := make([]string, 0, count)
	for range count {
		end := bytes.IndexByte(store, 0)
		if end < 0 {
			return nil, errTruncated
		}

		values = append(values, string(store[:end]))
		store = store[end+1:]
	}

	return values, nil
}

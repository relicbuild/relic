package nuget

import (
	"encoding/binary"
	"errors"
	"testing"
)

func portablePDB(id []byte) []byte {
	const (
		versionLength = 12
		tablesStart   = 60
		pdbStart      = 64
		pdbSize       = 32
	)

	pdb := binary.LittleEndian.AppendUint32(nil, 0x424A5342)
	pdb = binary.LittleEndian.AppendUint16(pdb, 1)
	pdb = binary.LittleEndian.AppendUint16(pdb, 1)
	pdb = binary.LittleEndian.AppendUint32(pdb, 0)
	pdb = binary.LittleEndian.AppendUint32(pdb, versionLength)
	pdb = append(pdb, "PDB v1.0\x00\x00\x00\x00"...)
	pdb = binary.LittleEndian.AppendUint16(pdb, 0)
	pdb = binary.LittleEndian.AppendUint16(pdb, 2)
	pdb = binary.LittleEndian.AppendUint32(pdb, tablesStart)
	pdb = binary.LittleEndian.AppendUint32(pdb, 4)
	pdb = append(pdb, "#~\x00\x00"...)
	pdb = binary.LittleEndian.AppendUint32(pdb, pdbStart)
	pdb = binary.LittleEndian.AppendUint32(pdb, pdbSize)
	pdb = append(pdb, "#Pdb\x00\x00\x00\x00"...)
	pdb = append(pdb, 0, 0, 0, 0)
	pdb = append(pdb, id...)

	return append(pdb, make([]byte, pdbSize-len(id))...)
}

func TestSymbolKey(t *testing.T) {
	t.Parallel()

	id := []byte{
		0x33,
		0x22,
		0x11,
		0x00,
		0x55,
		0x44,
		0x77,
		0x66,
		0x88,
		0x99,
		0xaa,
		0xbb,
		0xcc,
		0xdd,
		0xee,
		0xff,
		0x01,
		0x02,
		0x03,
		0x04,
	}

	for _, tc := range []struct {
		name    string
		file    string
		pdb     []byte
		want    string
		wantErr error
	}{
		{
			name: "a portable PDB",
			file: "Acme.Lib.pdb",
			pdb:  portablePDB(id),
			want: "acme.lib.pdb/00112233445566778899aabbccddeeffffffffff",
		},
		{
			name:    "a Windows PDB",
			file:    "Acme.Lib.pdb",
			pdb:     []byte("Microsoft C/C++ MSF 7.00\r\n\x1aDS\x00\x00\x00"),
			wantErr: ErrNotPortablePDB,
		},
		{
			name:    "a truncated portable PDB",
			file:    "Acme.Lib.pdb",
			pdb:     portablePDB(id)[:40],
			wantErr: ErrNotPortablePDB,
		},
		{
			name:    "an empty file",
			file:    "Acme.Lib.pdb",
			wantErr: ErrNotPortablePDB,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := SymbolKey(tc.file, tc.pdb)
			if got != tc.want || !errors.Is(err, tc.wantErr) {
				t.Errorf("SymbolKey = %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func FuzzSymbolKey(f *testing.F) {
	f.Add(portablePDB(make([]byte, 20)))
	f.Fuzz(func(_ *testing.T, pdb []byte) {
		_, _ = SymbolKey("a.pdb", pdb)
	})
}

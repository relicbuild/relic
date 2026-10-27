package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"relic/internal/artifacts/repository"
)

const symbolKeyProperty = "nuget.symbol.key"

const maxSymbolPackageBytes = 512 << 20

// ErrNotPortablePDB is returned for a .pdb that is not a portable PDB.
var ErrNotPortablePDB = errors.New("not a portable PDB")

// SymbolsPath is where a pushed symbol package is stored in a local repository.
func SymbolsPath(id, version string) string {
	return id + "." + normalizeVersion(version) + ".snupkg"
}

// SymbolsPackage reports whether the manifest declares a symbol package (.snupkg).
func (n Nuspec) SymbolsPackage() bool {
	for _, t := range n.Metadata.PackageTypes.Types {
		if strings.EqualFold(t.Name, "SymbolsPackage") {
			return true
		}
	}

	return false
}

// SymbolKey is the symbol server key of a portable PDB named name:
// <name>/<GUID as 32 hex digits>FFFFFFFF, lower-cased.
func SymbolKey(name string, pdb []byte) (string, error) {
	id, err := portablePDBID(pdb)
	if err != nil {
		return "", err
	}

	guid := make([]byte, 0, 16)
	guid = binary.BigEndian.AppendUint32(guid, binary.LittleEndian.Uint32(id[0:4]))
	guid = binary.BigEndian.AppendUint16(guid, binary.LittleEndian.Uint16(id[4:6]))
	guid = binary.BigEndian.AppendUint16(guid, binary.LittleEndian.Uint16(id[6:8]))
	guid = append(guid, id[8:16]...)

	return strings.ToLower(name + "/" + hex.EncodeToString(guid) + "ffffffff"), nil
}

func portablePDBID(pdb []byte) ([]byte, error) {
	const headerSize = 16

	if len(pdb) < headerSize || binary.LittleEndian.Uint32(pdb) != 0x424A5342 {
		return nil, ErrNotPortablePDB
	}

	versionLength := int(binary.LittleEndian.Uint32(pdb[12:16]))

	offset := headerSize + versionLength
	if versionLength < 0 || versionLength > len(pdb) || offset+4 > len(pdb) {
		return nil, ErrNotPortablePDB
	}

	streams := int(binary.LittleEndian.Uint16(pdb[offset+2:]))
	offset += 4

	for range streams {
		if offset+8 > len(pdb) {
			return nil, ErrNotPortablePDB
		}

		start := int(binary.LittleEndian.Uint32(pdb[offset:]))
		size := int(binary.LittleEndian.Uint32(pdb[offset+4:]))
		offset += 8

		end := bytes.IndexByte(pdb[offset:], 0)
		if end < 0 {
			return nil, ErrNotPortablePDB
		}

		name := string(pdb[offset : offset+end])
		offset += (end + 4) &^ 3

		if name == "#Pdb" {
			if size < 20 || start < 0 || start > len(pdb)-20 {
				return nil, ErrNotPortablePDB
			}

			return pdb[start : start+20], nil
		}
	}

	return nil, ErrNotPortablePDB
}

func symbolKeys(file *os.File, size int64) ([]string, error) {
	zr, err := zip.NewReader(file, size)
	if err != nil {
		return nil, fmt.Errorf("%w: not a zip archive", ErrInvalid)
	}

	var keys []string

	for _, f := range zr.File {
		if !strings.EqualFold(path.Ext(f.Name), ".pdb") {
			continue
		}

		pdb, err := readZipFile(f)
		if err != nil {
			return nil, err
		}

		key, err := SymbolKey(path.Base(f.Name), pdb)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, f.Name, err)
		}

		keys = append(keys, key)
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: a symbol package with no portable PDB", ErrInvalid)
	}

	return keys, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalid, f.Name, err)
	}

	defer func() {
		_ = rc.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(rc, maxSymbolPackageBytes+1))
	if err != nil || len(body) > maxSymbolPackageBytes {
		return nil, fmt.Errorf("%w: %s unreadable or too large", ErrInvalid, f.Name)
	}

	return body, nil
}

func (s *Service) pushSymbols(ctx context.Context, repoKey string, n Nuspec, file *os.File, user string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}

	keys, err := symbolKeys(file, info.Size())
	if err != nil {
		return err
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	id, version := n.Metadata.ID, normalizeVersion(n.Metadata.Version)
	p := SymbolsPath(id, version)
	lock := "nuget-push:" + repoKey + "\x00" + strings.ToLower(p)

	return s.repos.WithLock(ctx, lock, func() error {
		if _, err := s.repos.Stat(ctx, repoKey, p); err == nil {
			return fmt.Errorf("%w: symbols of %s %s", ErrExists, id, version)
		}

		_, err := s.repos.Deploy(ctx, repository.DeployInput{
			Repository: repoKey,
			Path:       p,
			Body:       file,
			User:       user,
			Props: repository.Properties{
				"nuget.symbols.id": {
					id,
				},
				"nuget.symbols.version": {
					version,
				},
				symbolKeyProperty: keys,
			},
		})

		return err
	})
}

// Symbol finds the file key names (<name>/<id>, as SymbolKey gives it, with
// file the name again) in the symbol packages stored in r's members, and
// returns the package and the file's content.
func (s *Service) Symbol(
	ctx context.Context,
	r repository.Repository,
	key, file string,
) (repository.Item, []byte, error) {
	key = strings.ToLower(key)
	if !strings.EqualFold(path.Dir(key), file) || strings.ContainsAny(file, "/\\") {
		return repository.Item{}, nil, ErrNotFound
	}

	for _, m := range s.members(ctx, r, 0) {
		items, err := s.repos.FilesWithProperty(ctx, m.Name, symbolKeyProperty, key)
		if err != nil {
			return repository.Item{}, nil, err
		}

		for _, item := range items {
			body, err := s.symbolFile(ctx, item, key)
			if errors.Is(err, ErrNotFound) {
				continue
			}

			return item, body, err
		}
	}

	return repository.Item{}, nil, ErrNotFound
}

func (s *Service) symbolFile(ctx context.Context, item repository.Item, key string) ([]byte, error) {
	zr, err := zipArchive(s.repos.ReaderAt(ctx, item), item.Size)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", item.Path(), err)
	}

	for _, f := range zr.File {
		if !strings.EqualFold(path.Base(f.Name), path.Dir(key)) {
			continue
		}

		pdb, err := readZipFile(f)
		if err != nil {
			return nil, err
		}

		if got, err := SymbolKey(path.Base(f.Name), pdb); err == nil && got == key {
			return pdb, nil
		}
	}

	return nil, ErrNotFound
}

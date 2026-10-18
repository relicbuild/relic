package rpm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
)

type testTag struct {
	id    uint32
	kind  uint32
	count uint32
	value []byte
}

func stringsTag(id uint32, values ...string) testTag {
	var value []byte
	for _, v := range values {
		value = append(value, v...)
		value = append(value, 0)
	}

	kind := uint32(typeStringArray)
	if id == tagName || id == tagVersion || id == tagRelease {
		kind = typeString
	}

	return testTag{
		id:    id,
		kind:  kind,
		count: uint32(len(values)), //nolint:gosec // a test header holds a few values
		value: value,
	}
}

func int32Tag(id uint32, values ...uint32) testTag {
	value := make([]byte, 0, 4*len(values))
	for _, v := range values {
		value = binary.BigEndian.AppendUint32(value, v)
	}

	return testTag{
		id:    id,
		kind:  typeInt32,
		count: uint32(len(values)), //nolint:gosec // a test header holds a few values
		value: value,
	}
}

func headerBytes(tags []testTag, padded bool) []byte {
	var index, store []byte

	for _, tag := range tags {
		index = binary.BigEndian.AppendUint32(index, tag.id)
		index = binary.BigEndian.AppendUint32(index, tag.kind)
		index = binary.BigEndian.AppendUint32(index, uint32(len(store))) //nolint:gosec // a test header is small
		index = binary.BigEndian.AppendUint32(index, tag.count)
		store = append(store, tag.value...)
	}

	out := []byte{
		0x8e,
		0xad,
		0xe8,
		0x01,
		0,
		0,
		0,
		0,
	}
	out = binary.BigEndian.AppendUint32(out, uint32(len(tags)))  //nolint:gosec // a test header is small
	out = binary.BigEndian.AppendUint32(out, uint32(len(store))) //nolint:gosec // a test header is small
	out = append(out, index...)
	out = append(out, store...)

	if padded {
		out = append(out, make([]byte, (8-len(store)%8)%8)...)
	}

	return out
}

func rawRPM(tags ...testTag) []byte {
	var lead [leadSize]byte
	copy(lead[:], []byte{
		0xed,
		0xab,
		0xee,
		0xdb,
		3,
		0,
	})

	out := append(lead[:], headerBytes([]testTag{
		int32Tag(1000, 0),
	}, true)...)

	return append(out, headerBytes(tags, false)...)
}

func identityTags() []testTag {
	return []testTag{
		stringsTag(tagName, "hello"),
		stringsTag(tagVersion, "1.0"),
		stringsTag(tagRelease, "1"),
	}
}

func TestParseRefusesMalformedHeaders(t *testing.T) {
	t.Parallel()

	files := func(dirIndexes ...uint32) []testTag {
		return append(
			identityTags(),
			stringsTag(tagBaseNames, "a", "b"),
			stringsTag(tagDirNames, "/usr/"),
			int32Tag(tagDirIndexes, dirIndexes...),
			int32Tag(tagFileModes, 0o100644, 0o100644),
			int32Tag(tagFileFlags, 0, 0),
		)
	}

	valid := rawRPM(identityTags()...)

	badMagic := slices.Clone(valid)
	badMagic[leadSize+len(headerBytes([]testTag{
		int32Tag(1000, 0),
	}, true))] = 0

	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "a directory index past the directory names",
			raw:  rawRPM(files(0, 1)...),
		},
		{
			name: "fewer directory indexes than files",
			raw:  rawRPM(files(0)...),
		},
		{
			name: "fewer dependency flags than names",
			raw: rawRPM(append(
				identityTags(),
				stringsTag(tagRequireName, "a", "b"),
				int32Tag(tagRequireFlags, 0),
				stringsTag(tagRequireVersion, "", ""),
			)...),
		},
		{
			name: "a string past the end of the store",
			raw: rawRPM(append(
				identityTags(),
				testTag{
					id:    tagSummary,
					kind:  typeI18NString,
					count: 3,
					value: []byte("x\x00"),
				},
			)...),
		},
		{
			name: "numbers past the end of the store",
			raw: rawRPM(append(
				identityTags(),
				testTag{
					id:    tagBuildTime,
					kind:  typeInt32,
					count: 2,
					value: []byte{
						0,
						0,
						0,
						1,
					},
				},
			)...),
		},
		{
			name: "a header without the header magic",
			raw:  badMagic,
		},
		{
			name: "a truncated header",
			raw:  valid[:len(valid)-3],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse(bytes.NewReader(tt.raw)); !errors.Is(err, ErrInvalid) {
				t.Errorf("Parse = %v, want ErrInvalid", err)
			}
		})
	}

	if _, err := Parse(bytes.NewReader(valid)); err != nil {
		t.Errorf("Parse of the valid package = %v", err)
	}
}

func TestParseReadsWeakDependencies(t *testing.T) {
	t.Parallel()

	const greaterOrEqual = senseGreater | senseEqual

	weak := []struct {
		name     string
		tags     [3]uint32
		packaged func(Package) []Dependency
	}{
		{
			name: "recommends",
			tags: [3]uint32{
				5046,
				5048,
				5047,
			},
			packaged: func(p Package) []Dependency {
				return p.Recommends
			},
		},
		{
			name: "suggests",
			tags: [3]uint32{
				5049,
				5051,
				5050,
			},
			packaged: func(p Package) []Dependency {
				return p.Suggests
			},
		},
		{
			name: "supplements",
			tags: [3]uint32{
				5052,
				5054,
				5053,
			},
			packaged: func(p Package) []Dependency {
				return p.Supplements
			},
		},
		{
			name: "enhances",
			tags: [3]uint32{
				5055,
				5057,
				5056,
			},
			packaged: func(p Package) []Dependency {
				return p.Enhances
			},
		},
	}
	for _, tt := range weak {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := Parse(bytes.NewReader(rawRPM(append(
				identityTags(),
				stringsTag(tt.tags[0], "other"),
				int32Tag(tt.tags[1], greaterOrEqual),
				stringsTag(tt.tags[2], "1:2.0-3"),
			)...)))
			if err != nil {
				t.Fatal(err)
			}

			want := []Dependency{
				{
					Name:    "other",
					Flags:   "GE",
					Epoch:   1,
					Version: "2.0",
					Release: "3",
				},
			}
			if got := tt.packaged(p); !slices.Equal(got, want) {
				t.Errorf("%s = %+v, want %+v", tt.name, got, want)
			}
		})
	}
}

func TestParseReadsOldFileNames(t *testing.T) {
	t.Parallel()

	p, err := Parse(bytes.NewReader(rawRPM(append(
		identityTags(),
		stringsTag(tagOldFileNames, "/etc/hello.conf", "/etc/hello.d"),
		int32Tag(tagFileModes, 0o100644, 0o40755),
		int32Tag(tagFileFlags, 0, 0),
	)...)))
	if err != nil {
		t.Fatal(err)
	}

	want := []File{
		{
			Path: "/etc/hello.conf",
		},
		{
			Path: "/etc/hello.d",
			Type: "dir",
		},
	}
	if !slices.Equal(p.Files, want) {
		t.Errorf("files = %+v, want %+v", p.Files, want)
	}
}

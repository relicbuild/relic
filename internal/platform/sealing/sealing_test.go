package sealing

import (
	"bytes"
	"errors"
	"testing"
)

func testBox(t *testing.T, fill byte) *Box {
	t.Helper()

	b, err := New(bytes.Repeat([]byte{
		fill,
	}, 32))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestSealOpen(t *testing.T) {
	t.Parallel()
	b := testBox(t, 1)

	sealed, err := b.Seal([]byte("seed"), "signing_keys:a")
	if err != nil {
		t.Fatal(err)
	}

	if !Sealed(sealed) || bytes.Contains(sealed, []byte("seed")) {
		t.Fatalf("sealed = %q", sealed)
	}

	tests := []struct {
		name  string
		box   *Box
		where string
		want  string
		err   error
	}{
		{
			"same key and place",
			b,
			"signing_keys:a",
			"seed",
			nil,
		},
		{
			"another place",
			b,
			"signing_keys:b",
			"",
			ErrOpen,
		},
		{
			"another key",
			testBox(t, 2),
			"signing_keys:a",
			"",
			ErrOpen,
		},
		{
			"no key",
			&Box{},
			"signing_keys:a",
			"",
			ErrNoKey,
		},
		{
			"nil box",
			nil,
			"signing_keys:a",
			"",
			ErrNoKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.box.Open(sealed, tt.where)
			if !errors.Is(err, tt.err) || string(got) != tt.want {
				t.Errorf("Open = %q, %v; want %q, %v", got, err, tt.want, tt.err)
			}
		})
	}
}

func TestPassThrough(t *testing.T) {
	t.Parallel()

	var none *Box
	if got, _ := none.Seal([]byte("x"), "w"); string(got) != "x" {
		t.Errorf("Seal without key = %q", got)
	}

	if got, err := testBox(t, 1).Open([]byte("legacy"), "w"); err != nil || string(got) != "legacy" {
		t.Errorf("Open(unsealed) = %q, %v", got, err)
	}
}

func TestSealString(t *testing.T) {
	t.Parallel()
	b := testBox(t, 1)

	sealed, err := b.SealString("hunter2", "repositories:r")
	if err != nil || !SealedString(sealed) {
		t.Fatalf("SealString = %q, %v", sealed, err)
	}

	if again, _ := b.SealString(sealed, "repositories:r"); again != sealed {
		t.Error("sealing a sealed string changed it")
	}

	if got, err := b.OpenString(sealed, "repositories:r"); err != nil || got != "hunter2" {
		t.Errorf("OpenString = %q, %v", got, err)
	}

	if _, err := b.OpenString(sealed, "repositories:other"); !errors.Is(err, ErrOpen) {
		t.Errorf("OpenString elsewhere err = %v", err)
	}

	if got, _ := b.SealString("", "w"); got != "" {
		t.Errorf("empty sealed to %q", got)
	}

	if got, _ := b.OpenString("plain", "w"); got != "plain" {
		t.Errorf("OpenString(plain) = %q", got)
	}
}

func TestParseKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, in string
		ok       bool
	}{
		{
			"std base64",
			"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
			true,
		},
		{
			"raw std base64",
			"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE",
			true,
		},
		{
			"url base64",
			"-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_s=",
			true,
		},
		{
			"raw url base64",
			"-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_v7-_s",
			true,
		},
		{
			"too short",
			"AQEB",
			false,
		},
		{
			"not base64",
			"not a key!",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := ParseKey(tt.in); (err == nil) != tt.ok {
				t.Errorf("ParseKey(%q) err = %v", tt.in, err)
			}
		})
	}
}

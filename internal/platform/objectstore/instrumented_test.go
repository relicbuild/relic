package objectstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
	"relic/internal/platform/telemetry/telemetrytest"
)

func TestInstrumentedStore(t *testing.T) {
	t.Parallel()

	s := objectstore.Instrument(objectstoretest.NewMemory())
	ctx := t.Context()
	ops := func(op, result string) float64 {
		return telemetrytest.Value(t, "relic_object_storage_ops_total", map[string]string{
			"op":     op,
			"result": result,
		})
	}
	bytesOf := func(op string) float64 {
		return telemetrytest.Value(t, "relic_object_storage_bytes_total", map[string]string{
			"op": op,
		})
	}
	putOK, putBytes, getBytes := ops("put", "success"), bytesOf("put"), bytesOf("get")
	headMiss, getMiss, headOK := ops("head", "miss"), ops("get", "miss"), ops("head", "success")

	if err := s.Put(ctx, "sha256/aa/bb/x", io.MultiReader(strings.NewReader("hello")), -1); err != nil {
		t.Fatal(err)
	}

	if err := s.Put(ctx, "sha256/aa/bb/y", bytes.NewReader([]byte("world!")), 6); err != nil {
		t.Fatal(err)
	}

	rc, _, err := s.Get(ctx, "sha256/aa/bb/x", nil)
	if err != nil {
		t.Fatal(err)
	}

	if b, _ := io.ReadAll(rc); string(b) != "hello" {
		t.Fatalf("read %q", b)
	}

	_ = rc.Close()

	if _, err := s.Head(ctx, "sha256/aa/bb/x"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Head(ctx, "readyz/probe"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("Head missing = %v", err)
	}

	if _, _, err := s.Get(ctx, "nope", nil); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("Get missing = %v", err)
	}

	for _, c := range []struct {
		name      string
		got, want float64
	}{
		{
			"put success",
			ops("put", "success") - putOK,
			2,
		},
		{
			"head success",
			ops("head", "success") - headOK,
			1,
		},
		{
			"head miss",
			ops("head", "miss") - headMiss,
			1,
		},
		{
			"get miss",
			ops("get", "miss") - getMiss,
			1,
		},
		{
			"put bytes",
			bytesOf("put") - putBytes,
			11,
		},
		{
			"get bytes",
			bytesOf("get") - getBytes,
			5,
		},
	} {
		if c.got < c.want {
			t.Errorf("%s moved %v, want at least %v", c.name, c.got, c.want)
		}
	}

	if telemetrytest.Value(t, "relic_object_storage_op_duration_seconds", map[string]string{
		"op": "put",
	}) < 2 {
		t.Error("put durations not observed")
	}
}

type failingPut struct {
	objectstore.Store
}

func (failingPut) Put(_ context.Context, _ string, r io.Reader, _ int64) error {
	_, _ = io.Copy(io.Discard, r)

	return errors.New("put failed")
}

func TestInstrumentedStoreCountsBytesOfSuccessfulPutsOnly(t *testing.T) {
	s := objectstore.Instrument(failingPut{
		Store: objectstoretest.NewMemory(),
	})
	putBytes := func() float64 {
		return telemetrytest.Value(t, "relic_object_storage_bytes_total", map[string]string{
			"op": "put",
		})
	}
	before := putBytes()

	for name, body := range map[string]io.Reader{
		"a reader that seeks":   bytes.NewReader([]byte("hello")),
		"a reader that streams": io.MultiReader(strings.NewReader("hello")),
	} {
		if err := s.Put(t.Context(), "sha256/aa/bb/x", body, 5); err == nil {
			t.Fatalf("%s: the put succeeded", name)
		}
	}

	if got := putBytes() - before; got != 0 {
		t.Errorf("failed puts counted %v bytes, want none", got)
	}
}

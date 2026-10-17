package objectstoretest_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"relic/internal/platform/objectstore"
	"relic/internal/platform/objectstore/objectstoretest"
)

func TestFaultyFailsMatchingCallsOnly(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	f := objectstoretest.NewFaulty(objectstoretest.NewMemory())
	f.Inject(objectstoretest.Fault{
		Op:        objectstoretest.OpPut,
		KeyPrefix: "sha256/",
		Times:     1,
	})

	if err := f.Put(ctx, "tmp/a", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("Put outside the prefix: %v", err)
	}

	err := f.Put(ctx, "sha256/a", bytes.NewReader([]byte("x")), 1)
	if !errors.Is(err, objectstoretest.ErrInjected) {
		t.Fatalf("first Put under the prefix = %v, want ErrInjected", err)
	}

	if err := f.Put(ctx, "sha256/a", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("second Put after Times ran out: %v", err)
	}

	if got := f.Calls(objectstoretest.OpPut); got != 3 {
		t.Errorf("Calls(Put) = %d, want 3", got)
	}
}

func TestFaultyCustomErrorAndClear(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	f := objectstoretest.NewFaulty(objectstoretest.NewMemory())
	f.Inject(objectstoretest.Fault{
		Op:  objectstoretest.OpHead,
		Err: objectstore.ErrNotFound,
	})

	for range 2 {
		if _, err := f.Head(ctx, "k"); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Head = %v, want ErrNotFound", err)
		}
	}

	f.Clear()

	if err := f.Put(ctx, "k", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Head(ctx, "k"); err != nil {
		t.Fatalf("Head after Clear: %v", err)
	}
}

func TestFaultyGetShortReadAndCorruption(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	content := []byte("0123456789")

	tests := []struct {
		name    string
		fault   objectstoretest.Fault
		want    []byte
		wantErr bool
	}{
		{
			name: "short read",
			fault: objectstoretest.Fault{
				Op:        objectstoretest.OpGet,
				ShortRead: 4,
			},
			want:    []byte("0123"),
			wantErr: true,
		},
		{
			name: "corrupt",
			fault: objectstoretest.Fault{
				Op:      objectstoretest.OpGet,
				Corrupt: true,
			},
			want: append(
				[]byte{
					'0' ^ 0xff,
				},
				content[1:]...,
			),
		},
		{
			name: "failed open",
			fault: objectstoretest.Fault{
				Op: objectstoretest.OpGet,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := objectstoretest.NewFaulty(objectstoretest.NewMemory())
			if err := f.Put(ctx, "k", bytes.NewReader(content), int64(len(content))); err != nil {
				t.Fatal(err)
			}

			f.Inject(tt.fault)

			body, _, err := f.Get(ctx, "k", nil)
			if err != nil {
				if !tt.wantErr || tt.want != nil {
					t.Fatalf("Get: %v", err)
				}

				return
			}

			t.Cleanup(func() {
				_ = body.Close()
			})

			got, err := io.ReadAll(body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("read error = %v, want error %v", err, tt.wantErr)
			}

			if !bytes.Equal(got, tt.want) {
				t.Errorf("read %q, want %q", got, tt.want)
			}
		})
	}
}

//go:build integration

package huggingface

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"relic/internal/artifacts/formats/huggingface/xet"
	"relic/internal/artifacts/formats/huggingface/xet/xettest"
	"relic/internal/artifacts/repository"
)

func randomBytes(n int, seed byte) []byte {
	out := make([]byte, n)
	_, _ = rand.NewChaCha8([32]byte{
		seed,
	}).Read(out)

	return out
}

func storeXorb(t *testing.T, service *Service, upload xettest.Upload) {
	t.Helper()

	inserted, err := service.StoreXorb(t.Context(), "models", upload.XorbHash, bytes.NewReader(upload.Xorb), "admin")
	if err != nil || !inserted {
		t.Fatalf("StoreXorb = %v, %v; want true, nil", inserted, err)
	}
}

func stagedContent(t *testing.T, service *Service, sha256 string) []byte {
	t.Helper()

	item, err := service.repos.Stat(t.Context(), "models", lfsStaging+"/"+sha256)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := service.repos.Open(t.Context(), item, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = rc.Close()
	}()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestXetUploadRebuildsFilesForACommit(t *testing.T) {
	t.Parallel()

	service := newFrozenService(t)
	weights := randomBytes(300_000, 1)
	config := []byte(`{"model_type": "relic"}`)
	upload := xettest.Build(t, weights, config)

	storeXorb(t, service, upload)

	again, err := service.StoreXorb(t.Context(), "models", upload.XorbHash, bytes.NewReader(upload.Xorb), "admin")
	if err != nil || again {
		t.Fatalf("second StoreXorb = %v, %v; want false, nil", again, err)
	}

	if err := service.RegisterShard(t.Context(), "models", bytes.NewReader(upload.ShardBytes(t)), "admin"); err != nil {
		t.Fatal(err)
	}

	for i, want := range [][]byte{
		weights,
		config,
	} {
		if got := stagedContent(t, service, upload.Shard.Files[i].SHA256); !bytes.Equal(got, want) {
			t.Fatalf("file %d rebuilt as %d bytes, want %d", i, len(got), len(want))
		}
	}

	var op Operation

	op.Key = "lfsFile"
	op.Value.Path = "weights.bin"
	op.Value.Oid = upload.Shard.Files[0].SHA256
	op.Value.Size = int64(len(weights))

	c, err := service.CreateCommit(t.Context(), "models", "models", "org/model", "main", []Operation{
		op,
	}, "admin")
	if err != nil {
		t.Fatal(err)
	}

	item, err := service.repos.Stat(t.Context(), "models", c.Dir()+"/weights.bin")
	if err != nil || item.Checksums.SHA256 != op.Value.Oid {
		t.Fatalf("committed file = %+v, %v", item, err)
	}

	if err := service.RegisterShard(t.Context(), "models", bytes.NewReader(upload.ShardBytes(t)), "admin"); err != nil {
		t.Fatalf("registering the shard again: %v", err)
	}
}

func TestStoreXorbRejectsAXorbNotMatchingItsHash(t *testing.T) {
	t.Parallel()

	service := newFrozenService(t)
	upload := xettest.Build(t, randomBytes(100_000, 2))

	wrong := upload.XorbHash
	wrong[0] ^= 1

	if _, err := service.StoreXorb(t.Context(), "models", wrong, bytes.NewReader(upload.Xorb), "admin"); !errors.Is(
		err,
		xet.ErrInvalid,
	) {
		t.Fatalf("StoreXorb error = %v, want ErrInvalid", err)
	}

	if service.XorbStored(t.Context(), "models", wrong) {
		t.Fatal("the rejected xorb was stored")
	}

	if _, err := service.repos.Stat(t.Context(), "models", xorbPath(wrong)); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("Stat of the rejected xorb = %v, want ErrNotFound", err)
	}
}

func TestRegisterShardRejectsShardsItCannotTrust(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		store  bool
		mutate func(*xet.Shard)
	}{
		{
			"xorb never uploaded",
			false,
			func(*xet.Shard) {},
		},
		{
			"no verification hashes",
			true,
			func(s *xet.Shard) {
				s.Files[0].Verification = false
			},
		},
		{
			"wrong verification hash",
			true,
			func(s *xet.Shard) {
				s.Files[0].Terms[0].Verification[0] ^= 1
			},
		},
		{
			"wrong file hash",
			true,
			func(s *xet.Shard) {
				s.Files[0].Hash[0] ^= 1
			},
		},
		{
			"wrong term size",
			true,
			func(s *xet.Shard) {
				s.Files[0].Terms[0].Size++
			},
		},
		{
			"chunks past the xorb's end",
			true,
			func(s *xet.Shard) {
				s.Files[0].Terms[0].End += 5
			},
		},
		{
			"no SHA-256",
			true,
			func(s *xet.Shard) {
				s.Files[0].SHA256 = ""
			},
		},
		{
			"wrong SHA-256",
			true,
			func(s *xet.Shard) {
				s.Files[0].SHA256 = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := newFrozenService(t)
			upload := xettest.Build(t, randomBytes(200_000, 3))

			if tt.store {
				storeXorb(t, service, upload)
			}

			tt.mutate(&upload.Shard)

			shard, err := upload.Shard.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}

			if err := service.RegisterShard(t.Context(), "models", bytes.NewReader(shard), "admin"); !errors.Is(
				err,
				xet.ErrInvalid,
			) {
				t.Fatalf("RegisterShard error = %v, want ErrInvalid", err)
			}

			if service.StagedLFS(t.Context(), "models", upload.Shard.Files[0].SHA256) {
				t.Fatal("a file of the rejected shard was staged")
			}
		})
	}
}

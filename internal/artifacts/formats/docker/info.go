package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
)

// ErrNotImage is returned for folders that hold no manifest.
var ErrNotImage = errors.New("not an image")

// ImageInfo describes the image a tag folder holds (the UI's Docker tab).
type ImageInfo struct {
	Digest       string            `json:"digest"`
	MediaType    string            `json:"mediaType"`
	OS           string            `json:"os,omitempty"`
	Architecture string            `json:"architecture,omitempty"`
	Created      *time.Time        `json:"created,omitempty"`
	TotalSize    int64             `json:"totalSize"`
	Labels       map[string]string `json:"labels"`
	Layers       []LayerInfo       `json:"layers"`
	Platforms    []PlatformInfo    `json:"platforms,omitempty"`
}

// LayerInfo is one layer with the build step that made it.
type LayerInfo struct {
	Digest      string     `json:"digest"`
	Size        int64      `json:"size"`
	MediaType   string     `json:"mediaType"`
	Instruction string     `json:"instruction,omitempty"`
	Created     *time.Time `json:"created,omitempty"`
}

// PlatformInfo is one image of an index.
type PlatformInfo struct {
	Digest       string `json:"digest"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
	Size         int64  `json:"size"`
}

type imageConfig struct {
	OS           string     `json:"os"`
	Architecture string     `json:"architecture"`
	Created      *time.Time `json:"created"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
	History []struct {
		Created    *time.Time `json:"created"`
		CreatedBy  string     `json:"created_by"`
		EmptyLayer bool       `json:"empty_layer"`
	} `json:"history"`
}

const maxConfigBytes = 4 << 20

// ImageInfo reads the image held by a tag folder (<image>/<tag>).
func (s *Service) ImageInfo(ctx context.Context, repoKey, folder string) (ImageInfo, error) {
	var (
		item repository.Item
		err  error
	)
	for _, name := range []string{
		ManifestFile,
		ListFile,
	} {
		item, err = s.repos.Stat(ctx, repoKey, path.Join(folder, name))
		if err == nil || !errors.Is(err, repository.ErrNotFound) {
			break
		}
	}

	if errors.Is(err, repository.ErrNotFound) {
		return ImageInfo{}, ErrNotImage
	}

	if err != nil {
		return ImageInfo{}, err
	}

	doc, err := s.readManifest(ctx, item)
	if err != nil {
		return ImageInfo{}, err
	}

	info := ImageInfo{
		Digest:    "sha256:" + item.Checksums.SHA256,
		MediaType: mediaTypeOf("", doc),
		Labels:    map[string]string{},
		Layers:    []LayerInfo{},
	}
	if len(doc.Manifests) > 0 {
		for _, m := range doc.Manifests {
			p := PlatformInfo{
				Digest: m.Digest,
				Size:   m.Size,
			}
			if m.Platform != nil {
				p.OS, p.Architecture, p.Variant = m.Platform.OS, m.Platform.Architecture, m.Platform.Variant
			}

			info.Platforms = append(info.Platforms, p)
		}

		return info, nil
	}

	var cfg imageConfig

	if doc.Config != nil {
		info.TotalSize += doc.Config.Size
		if cfg, err = s.readConfig(ctx, repoKey, folder, doc.Config.Digest); err != nil {
			s.log.WarnContext(ctx, "read image config", "repo", repoKey, "folder", folder, "error", err)
		}
	}

	info.OS, info.Architecture, info.Created = cfg.OS, cfg.Architecture, cfg.Created
	maps.Copy(info.Labels, cfg.Config.Labels)

	steps := cfg.History[:0:0]
	for _, h := range cfg.History {
		if !h.EmptyLayer {
			steps = append(steps, h)
		}
	}

	for i, l := range doc.Layers {
		info.TotalSize += l.Size

		li := LayerInfo{
			Digest:    l.Digest,
			Size:      l.Size,
			MediaType: l.MediaType,
		}
		if i < len(steps) && len(steps) == len(doc.Layers) {
			li.Instruction, li.Created = cleanInstruction(steps[i].CreatedBy), steps[i].Created
		}

		info.Layers = append(info.Layers, li)
	}

	return info, nil
}

func (s *Service) readConfig(ctx context.Context, repoKey, folder, digest string) (imageConfig, error) {
	h, ok := ParseDigest(digest)
	if !ok {
		return imageConfig{}, fmt.Errorf("config digest %q", digest)
	}

	item, err := s.repos.Stat(ctx, repoKey, path.Join(folder, LayerName(h)))
	if err != nil {
		return imageConfig{}, err
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return imageConfig{}, err
	}

	defer func() {
		_ = rc.Close()
	}()

	var cfg imageConfig
	if err := json.NewDecoder(io.LimitReader(rc, maxConfigBytes)).Decode(&cfg); err != nil {
		return imageConfig{}, fmt.Errorf("image config: %w", err)
	}

	return cfg, nil
}

func cleanInstruction(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "/bin/sh -c #(nop)"); ok {
		return strings.TrimSpace(rest)
	}

	if rest, ok := strings.CutPrefix(s, "/bin/sh -c "); ok {
		return "RUN " + strings.TrimSpace(rest)
	}

	return s
}

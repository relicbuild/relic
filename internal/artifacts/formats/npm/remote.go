package npm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/telemetry"
)

const (
	packumentIndexCacheBytes    = 32 << 20
	packumentDocumentCacheBytes = 128 << 20
)

type packumentIndex struct {
	versions []indexedVersion
	times    map[string]string
	tags     map[string]string
}

type indexedVersion struct {
	version string
	tarball string
	shasum  string
}

func (x packumentIndex) bytes() int64 {
	const overhead = 64

	n := int64(overhead)
	for _, v := range x.versions {
		n += int64(len(v.version)+len(v.tarball)+len(v.shasum)) + overhead
	}

	for _, m := range []map[string]string{
		x.times,
		x.tags,
	} {
		for k, v := range m {
			n += int64(len(k)+len(v)) + overhead
		}
	}

	return n
}

type remotePackument struct {
	item        repository.Item
	name        string
	upstream    string
	tarballBase string
	kept        map[string]bool
	tags        map[string]string
	restricted  bool
}

func packumentDecoder(r io.Reader) *jsontext.Decoder {
	return jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
}

func (s *Service) remotePackument(
	ctx context.Context,
	r repository.Repository,
	name, tarballBase string,
) (*remotePackument, error) {
	cfg := r.Upstream()
	if cfg.URL == "" {
		return nil, ErrNotFound
	}

	upstream := strings.TrimSuffix(cfg.URL, "/")

	item, err := s.repos.ResolveURL(ctx, r, upstream+"/"+escapeName(name), packumentCachePath(name))
	if errors.Is(err, repository.ErrNotFound) {
		return nil, ErrNotFound
	}

	if err != nil {
		return nil, err
	}

	index, err := s.packumentIndex(ctx, item)
	if err != nil {
		return nil, err
	}

	p := &remotePackument{
		item:        item,
		name:        name,
		upstream:    upstream,
		tarballBase: tarballBase,
		kept:        make(map[string]bool, len(index.versions)),
		tags:        maps.Clone(index.tags),
		restricted:  repository.Restricted(ctx, r.Name),
	}

	paths := make(map[string]string, len(index.versions))

	for _, v := range index.versions {
		p.kept[v.version] = true

		if v.tarball == "" {
			continue
		}

		rel, ok := relativeTarball(v.tarball, upstream)
		if !ok {
			continue
		}

		if !repository.Visible(ctx, r.Name, rel) {
			delete(p.kept, v.version)
			continue
		}

		paths[v.version] = rel
	}

	if cooldown := cfg.Cooldown(); cooldown > 0 {
		cutoff := s.now().Add(-cooldown)

		dropVersions(p.kept, index.times, p.tags, func(version string) bool {
			published, ok := publishTime(index.times, version)
			return ok && published.After(cutoff)
		})
	}

	offered := make([]repository.Version, 0, len(p.kept))
	for _, version := range slices.Sorted(maps.Keys(p.kept)) {
		offered = append(offered, repository.Version{
			Version: version,
			Path:    paths[version],
		})
	}

	refused, err := s.repos.RefusedVersions(ctx, r, "npm", name, offered)
	if err != nil {
		return nil, err
	}

	if len(refused) > 0 {
		dropVersions(p.kept, index.times, p.tags, func(version string) bool {
			return refused[version]
		})
	}

	if p.restricted {
		if len(p.kept) == 0 {
			return nil, ErrNotFound
		}

		maps.DeleteFunc(p.tags, func(_, version string) bool {
			return !p.kept[version]
		})
	}

	return p, nil
}

func packumentCachePath(name string) string {
	return ".npm/" + name + packumentCacheSuffix
}

func relativeTarball(tarball, upstream string) (string, bool) {
	if rel, ok := strings.CutPrefix(tarball, upstream+"/"); ok {
		return rel, true
	}

	u, err := url.Parse(tarball)
	if err != nil {
		return "", false
	}

	return strings.TrimPrefix(u.Path, "/"), true
}

func (p *remotePackument) key() string {
	h := sha256.New()

	field := func(s string) {
		_, _ = fmt.Fprintf(h, "%d:%s", len(s), s)
	}

	field(p.item.Checksums.SHA256)
	field(p.name)
	field(p.upstream)
	field(p.tarballBase)
	field(fmt.Sprint(p.restricted))

	for _, version := range slices.Sorted(maps.Keys(p.kept)) {
		field(version)
	}

	field("dist-tags")

	if p.tags == nil {
		field("none")
	}

	for _, tag := range slices.Sorted(maps.Keys(p.tags)) {
		field(tag)
		field(p.tags[tag])
	}

	return hex.EncodeToString(h.Sum(nil))
}

func (s *Service) packumentIndex(ctx context.Context, item repository.Item) (packumentIndex, error) {
	return s.indexes.get(ctx, item.Checksums.SHA256, func(ctx context.Context) (packumentIndex, error) {
		return s.readIndex(ctx, item)
	})
}

func (s *Service) readIndex(ctx context.Context, item repository.Item) (index packumentIndex, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "npm.ReadPackumentIndex")
	defer func() {
		telemetry.End(span, err)
	}()

	s.indexReads.Add(1)

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return packumentIndex{}, err
	}

	defer func() {
		_ = rc.Close()
	}()

	index, err = readPackumentIndex(io.LimitReader(rc, maxPackumentBytes))
	if err != nil {
		return packumentIndex{}, fmt.Errorf("%w: invalid packument: %w", repository.ErrUpstream, err)
	}

	return index, nil
}

func readPackumentIndex(r io.Reader) (packumentIndex, error) {
	dec := packumentDecoder(r)

	var index packumentIndex

	err := eachMember(dec, func(key string) error {
		var err error

		switch key {
		case "versions":
			index.versions, err = readIndexedVersions(dec)
		case "time":
			index.times, err = readStrings(dec)
		case "dist-tags":
			index.tags, err = readStrings(dec)
		default:
			err = dec.SkipValue()
		}

		return err
	})

	return index, err
}

func eachMember(dec *jsontext.Decoder, member func(name string) error) error {
	begin, err := dec.ReadToken()
	if err != nil {
		return err
	}

	if begin.Kind() != '{' {
		return fmt.Errorf("want an object, got %v", begin.Kind())
	}

	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return err
		}

		if err := member(name.String()); err != nil {
			return err
		}
	}

	_, err = dec.ReadToken()

	return err
}

func readIndexedVersions(dec *jsontext.Decoder) ([]indexedVersion, error) {
	if dec.PeekKind() != '{' {
		return nil, dec.SkipValue()
	}

	var versions []indexedVersion

	err := eachMember(dec, func(version string) error {
		value, err := dec.ReadValue()
		if err != nil {
			return err
		}

		var manifest struct {
			Dist struct {
				Tarball string `json:"tarball"`
				Shasum  string `json:"shasum"`
			} `json:"dist"`
		}

		_ = json.Unmarshal(value, &manifest)

		versions = append(versions, indexedVersion{
			version: version,
			tarball: manifest.Dist.Tarball,
			shasum:  manifest.Dist.Shasum,
		})

		return nil
	})

	return versions, err
}

func readStrings(dec *jsontext.Decoder) (map[string]string, error) {
	value, err := dec.ReadValue()
	if err != nil || value.Kind() != '{' {
		return nil, err
	}

	var raw map[string]any

	_ = json.Unmarshal(value, &raw)

	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}

	return out, nil
}

func (s *Service) remoteDocument(ctx context.Context, p *remotePackument) ([]byte, error) {
	return s.documents.get(ctx, p.key(), func(ctx context.Context) ([]byte, error) {
		return s.renderDocument(ctx, p)
	})
}

func (s *Service) renderDocument(ctx context.Context, p *remotePackument) (body []byte, err error) {
	ctx, span := telemetry.Tracer().Start(ctx, "npm.RenderPackument")
	defer func() {
		telemetry.End(span, err)
	}()

	s.renders.Add(1)

	rc, err := s.repos.Open(ctx, p.item, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = rc.Close()
	}()

	var out bytes.Buffer

	out.Grow(int(min(p.item.Size+p.item.Size/8, maxPackumentBytes)))

	// jsontext doubles a *bytes.Buffer it writes to; behind a plain
	// io.Writer it fills the capacity grown above.
	writer := struct {
		io.Writer
	}{
		&out,
	}
	if err := p.render(writer, io.LimitReader(rc, maxPackumentBytes)); err != nil {
		return nil, fmt.Errorf("%w: invalid packument: %w", repository.ErrUpstream, err)
	}

	return out.Bytes(), nil
}

func (p *remotePackument) render(w io.Writer, r io.Reader) error {
	dec := packumentDecoder(r)
	enc := jsontext.NewEncoder(w, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))

	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	err := eachMember(dec, func(key string) error {
		if err := enc.WriteToken(jsontext.String(key)); err != nil {
			return err
		}

		switch {
		case key == "versions":
			return p.renderVersions(dec, enc)
		case key == "dist-tags" && p.tags != nil:
			if err := dec.SkipValue(); err != nil {
				return err
			}

			return writeStrings(enc, p.tags)
		case key == "time" && p.restricted && dec.PeekKind() == '{':
			return p.renderTimes(dec, enc)
		default:
			return copyValue(dec, enc)
		}
	})
	if err != nil {
		return err
	}

	return enc.WriteToken(jsontext.EndObject)
}

func (p *remotePackument) renderVersions(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	if dec.PeekKind() != '{' {
		return copyValue(dec, enc)
	}

	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	err := eachMember(dec, func(version string) error {
		if !p.kept[version] {
			return dec.SkipValue()
		}

		if err := enc.WriteToken(jsontext.String(version)); err != nil {
			return err
		}

		return p.renderVersion(dec, enc, version)
	})
	if err != nil {
		return err
	}

	return enc.WriteToken(jsontext.EndObject)
}

func (p *remotePackument) renderVersion(dec *jsontext.Decoder, enc *jsontext.Encoder, version string) error {
	if dec.PeekKind() != '{' {
		return copyValue(dec, enc)
	}

	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	err := eachMember(dec, func(key string) error {
		if err := enc.WriteToken(jsontext.String(key)); err != nil {
			return err
		}

		if key == "dist" {
			return p.renderDist(dec, enc, version)
		}

		return copyValue(dec, enc)
	})
	if err != nil {
		return err
	}

	return enc.WriteToken(jsontext.EndObject)
}

func (p *remotePackument) renderDist(dec *jsontext.Decoder, enc *jsontext.Encoder, version string) error {
	if dec.PeekKind() != '{' {
		return copyValue(dec, enc)
	}

	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	err := eachMember(dec, func(key string) error {
		if err := enc.WriteToken(jsontext.String(key)); err != nil {
			return err
		}

		switch {
		case key == "tarball" && dec.PeekKind() == '"':
			return p.renderTarball(dec, enc)
		case key == "attestations" && dec.PeekKind() == '{':
			return p.renderAttestations(dec, enc, version)
		default:
			return copyValue(dec, enc)
		}
	})
	if err != nil {
		return err
	}

	return enc.WriteToken(jsontext.EndObject)
}

func (p *remotePackument) renderTarball(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	token, err := dec.ReadToken()
	if err != nil {
		return err
	}

	tarball := token.String()
	if rel, ok := relativeTarball(tarball, p.upstream); ok && tarball != "" {
		tarball = p.tarballBase + "/" + rel
	}

	return enc.WriteToken(jsontext.String(tarball))
}

func (p *remotePackument) renderAttestations(dec *jsontext.Decoder, enc *jsontext.Encoder, version string) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	err := eachMember(dec, func(key string) error {
		if key == "url" {
			return dec.SkipValue()
		}

		if err := enc.WriteToken(jsontext.String(key)); err != nil {
			return err
		}

		return copyValue(dec, enc)
	})
	if err != nil {
		return err
	}

	for _, token := range []jsontext.Token{
		jsontext.String("url"),
		jsontext.String(p.tarballBase + "/-/npm/v1/attestations/" + escapeName(p.name) + "@" + version),
		jsontext.EndObject,
	} {
		if err := enc.WriteToken(token); err != nil {
			return err
		}
	}

	return nil
}

func (p *remotePackument) renderTimes(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	err := eachMember(dec, func(key string) error {
		if key != "created" && key != "modified" && !p.kept[key] {
			return dec.SkipValue()
		}

		if err := enc.WriteToken(jsontext.String(key)); err != nil {
			return err
		}

		return copyValue(dec, enc)
	})
	if err != nil {
		return err
	}

	return enc.WriteToken(jsontext.EndObject)
}

func copyValue(dec *jsontext.Decoder, enc *jsontext.Encoder) error {
	value, err := dec.ReadValue()
	if err != nil {
		return err
	}

	return enc.WriteValue(value)
}

func writeStrings(enc *jsontext.Encoder, m map[string]string) error {
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	for _, k := range slices.Sorted(maps.Keys(m)) {
		for _, token := range []jsontext.Token{
			jsontext.String(k),
			jsontext.String(m[k]),
		} {
			if err := enc.WriteToken(token); err != nil {
				return err
			}
		}
	}

	return enc.WriteToken(jsontext.EndObject)
}

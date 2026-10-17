// Package docker implements the OCI distribution API (Docker Registry v2) on
// Relic repositories, in this storage layout:
//
//	<image>/<tag>/manifest.json          an image manifest pushed by tag
//	<image>/<tag>/list.manifest.json     an index (multi-arch) pushed by tag
//	<image>/<tag>/sha256__<hex>          the config and layers it references
//	<image>/sha256__<hex>/manifest.json  a manifest pushed by digest (the
//	                                     platform manifests of an index)
//	<image>/_uploads/sha256__<hex>       a finished upload not yet referenced
package docker

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/objectstore"
)

// Format is the repository format this package serves.
const Format = repository.FormatDocker

// Serves reports whether repositories of format f are OCI registries:
// docker, helm (clients push OCI charts to helm repositories, contract
// helm.md §5), helmoci and oci.
func Serves(f repository.Format) bool {
	switch f {
	case Format, repository.FormatHelm, repository.FormatHelmOCI, repository.FormatOCI:
		return true
	default:
		return false
	}
}

// Media types of manifests Relic distinguishes.
const (
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
)

// File names of the layout.
const (
	ManifestFile = "manifest.json"
	ListFile     = "list.manifest.json"
	uploadsDir   = "_uploads"
)

// MaxManifestBytes bounds a manifest body (distribution recommends 4 MiB).
const MaxManifestBytes = 4 << 20

// Errors, mapped to registry error codes by the API layer.
var (
	ErrManifestUnknown = errors.New("manifest unknown")
	ErrBlobUnknown     = errors.New("blob unknown to registry")
	ErrManifestInvalid = errors.New("manifest invalid")
	ErrDigestInvalid   = errors.New("provided digest did not match uploaded content")
	ErrUploadUnknown   = errors.New("blob upload unknown to registry")
)

// BlobUnknownError names a blob a manifest references but the repository
// does not hold.
type BlobUnknownError struct {
	Digest string
}

func (e BlobUnknownError) Error() string { return "blob unknown to registry: " + e.Digest }
func (e BlobUnknownError) Unwrap() error { return ErrBlobUnknown }

var (
	imageName = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagName   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestHex = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// ValidImage reports whether name is a valid image name (without the
// repository key).
func ValidImage(name string) bool { return len(name) <= 255 && imageName.MatchString(name) }

// ValidTag reports whether tag is a valid tag whose folder is not one the
// layout reserves for uploads or manifests pushed by digest.
func ValidTag(tag string) bool {
	return tagName.MatchString(tag) && tag != uploadsDir && !strings.HasPrefix(tag, "sha256__")
}

// ParseDigest returns the hex of a "sha256:<hex>" digest.
func ParseDigest(d string) (string, bool) {
	h, ok := strings.CutPrefix(d, "sha256:")
	return h, ok && digestHex.MatchString(h)
}

// IsIndex reports whether mediaType is a manifest list or image index.
func IsIndex(mediaType string) bool {
	return mediaType == MediaTypeDockerList || mediaType == MediaTypeOCIIndex
}

// Service implements the registry on top of repo.Service.
type Service struct {
	repos           *repository.Service
	blobs           objectstore.Store
	log             *slog.Logger
	tokens          tokenCache
	manifestBlobs   manifestBlobsCache
	manifestFetches repository.Coalescer[Manifest]
	blobFetches     repository.Coalescer[repository.Item]
}

// NewService returns a Service.
func NewService(repos *repository.Service, blobs objectstore.Store, log *slog.Logger) *Service {
	return &Service{
		repos: repos,
		blobs: blobs,
		log:   log,
	}
}

// Manifest is a stored manifest.
type Manifest struct {
	Item      repository.Item
	MediaType string
	Digest    string
}

type descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	URLs      []string  `json:"urls,omitempty"`
	Platform  *platform `json:"platform,omitempty"`
}

type platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type manifestDoc struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        *descriptor       `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Manifests     []descriptor      `json:"manifests"`
	Subject       *descriptor       `json:"subject,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

const (
	propSubject      = "oci.subject"
	propArtifactType = "oci.artifactType"
	propAnnotations  = "oci.annotations"
	propSize         = "oci.size"
)

func mediaTypeOf(contentType string, doc manifestDoc) string {
	ct, _, _ := strings.Cut(contentType, ";")
	switch ct = strings.TrimSpace(ct); ct {
	case MediaTypeDockerManifest, MediaTypeDockerList, MediaTypeOCIManifest, MediaTypeOCIIndex:
		return ct
	}

	switch {
	case doc.MediaType != "":
		return doc.MediaType
	case doc.Manifests != nil:
		return MediaTypeOCIIndex
	default:
		return MediaTypeOCIManifest
	}
}

func tagFolder(image, ref string) string {
	if h, ok := ParseDigest(ref); ok {
		return image + "/" + LayerName(h)
	}

	return image + "/" + ref
}

// LayerName is the file name of a blob inside a tag folder: the digest's
// algorithm and hex joined by "__", since ":" is not safe in a path.
func LayerName(hexDigest string) string { return "sha256__" + hexDigest }

// PutManifest stores a manifest pushed to image under ref (a tag or a
// digest) and links the blobs it references into its folder.
func (s *Service) PutManifest(
	ctx context.Context,
	repoKey, image, ref, contentType string,
	body []byte,
	user string,
	canRead func(repository.Item) bool,
	from ...repository.Repository,
) (string, error) {
	return s.putManifest(ctx, repoKey, image, ref, contentType, body, user, false, canRead, from)
}

func (s *Service) putManifest(
	ctx context.Context,
	repoKey, image, ref, contentType string,
	body []byte,
	user string,
	cache bool,
	canRead func(repository.Item) bool,
	from []repository.Repository,
) (string, error) {
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])

	digest := "sha256:" + hexSum
	if h, ok := ParseDigest(ref); ok && h != hexSum {
		return "", ErrDigestInvalid
	}

	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil || doc.SchemaVersion != 2 {
		return "", ErrManifestInvalid
	}

	mediaType := mediaTypeOf(contentType, doc)
	folder := tagFolder(image, ref)

	name, other := ManifestFile, ListFile
	if IsIndex(mediaType) {
		name, other = ListFile, ManifestFile
	}

	var refs []descriptor

	if !IsIndex(mediaType) && !cache {
		if doc.Config != nil {
			refs = append(refs, *doc.Config)
		}

		refs = append(refs, doc.Layers...)
	}

	keep := map[string]bool{
		name: true,
	}

	for _, d := range refs {
		if len(d.URLs) > 0 {
			continue
		}

		h, ok := ParseDigest(d.Digest)
		if !ok {
			return "", ErrManifestInvalid
		}

		keep[LayerName(h)] = true
		if err := s.link(ctx, repoKey, image, folder, h, user, false, canRead, from); err != nil {
			return "", err
		}
	}

	tagValue := ref
	props := repository.Properties{
		"docker.manifest": {
			tagValue,
		},
		"docker.repoName": {
			image,
		},
		"docker.manifest.digest": {
			digest,
		},
		"docker.manifest.type": {
			mediaType,
		},
		"sha256": {
			hexSum,
		},
	}

	if doc.Subject != nil {
		if _, ok := ParseDigest(doc.Subject.Digest); !ok {
			return "", ErrManifestInvalid
		}

		artifactType := doc.ArtifactType
		if artifactType == "" && doc.Config != nil {
			artifactType = doc.Config.MediaType
		}

		annotations, err := json.Marshal(doc.Annotations)
		if err != nil {
			return "", err
		}

		props[propSubject] = []string{
			doc.Subject.Digest,
		}
		props[propArtifactType] = []string{
			artifactType,
		}
		props[propAnnotations] = []string{
			string(annotations),
		}
		props[propSize] = []string{
			strconv.Itoa(len(body)),
		}
	}

	in := repository.DeployInput{
		Repository: repoKey,
		Path:       folder + "/" + name,
		Body:       bytes.NewReader(body),
		User:       user,
		Props:      props,
		Expected: repository.Checksums{
			SHA256: hexSum,
		},
	}

	deploy := s.repos.Deploy
	if cache {
		deploy = s.repos.Cache
	}

	if _, err := deploy(ctx, in); err != nil {
		return "", err
	}

	if cache {
		s.prune(ctx, repoKey, folder, nil, other)
		return digest, nil
	}

	s.prune(ctx, repoKey, folder, keep, other)

	for h := range keep {
		if after, ok := strings.CutPrefix(h, "sha256__"); ok {
			s.dropUpload(ctx, repoKey, image, after)
		}
	}

	return digest, nil
}

func (s *Service) link(
	ctx context.Context,
	repoKey, image, folder, h, user string,
	cache bool,
	canRead func(repository.Item) bool,
	from []repository.Repository,
) error {
	target := folder + "/" + LayerName(h)
	if it, err := s.repos.Stat(ctx, repoKey, target); err == nil && it.Checksums.SHA256 == h {
		return nil
	}

	it, err := s.Blob(ctx, repoKey, image, h)
	if err == nil && canRead != nil && !canRead(it) {
		err = ErrBlobUnknown
	}

	if err != nil {
		if !errors.Is(err, ErrBlobUnknown) {
			return err
		}

		if !slices.ContainsFunc(from, func(m repository.Repository) bool {
			_, err := s.BlobIn(ctx, m, image, h)
			return err == nil
		}) {
			return BlobUnknownError{
				Digest: "sha256:" + h,
			}
		}
	}

	byChecksum := s.repos.DeployByChecksum
	if cache {
		byChecksum = s.repos.CacheByChecksum
	}

	_, err = byChecksum(ctx, repository.DeployInput{
		Repository: repoKey,
		Path:       target,
		User:       user,
		Expected: repository.Checksums{
			SHA256: h,
		},
		Props: repository.Properties{
			"sha256": {
				h,
			},
		},
	})
	if errors.Is(err, repository.ErrChecksumUnknown) {
		return BlobUnknownError{
			Digest: "sha256:" + h,
		}
	}

	return err
}

func (s *Service) prune(ctx context.Context, repoKey, folder string, keep map[string]bool, other string) {
	dir, err := s.repos.Stat(ctx, repoKey, folder)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			s.log.WarnContext(ctx, "find tag folder", "repo", repoKey, "folder", folder, "error", err)
		}

		return
	}

	children, err := s.repos.Children(ctx, dir)
	if err != nil {
		s.log.WarnContext(ctx, "list tag folder", "repo", repoKey, "folder", folder, "error", err)
		return
	}

	for _, c := range children {
		stale := c.Name == other || (keep != nil && strings.HasPrefix(c.Name, "sha256__") && !keep[c.Name])
		if !c.IsFolder() && stale {
			if err := s.repos.Delete(ctx, repoKey, c.Path()); err != nil && !errors.Is(err, repository.ErrNotFound) {
				s.log.WarnContext(ctx, "prune tag folder", "repo", repoKey, "path", c.Path(), "error", err)
			}
		}
	}
}

func (s *Service) dropUpload(ctx context.Context, repoKey, image, h string) {
	err := s.repos.Delete(ctx, repoKey, image+"/"+uploadsDir+"/"+LayerName(h))
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		s.log.WarnContext(ctx, "drop finished upload", "repo", repoKey, "image", image, "error", err)
	}
}

// GetManifest finds the manifest of image by tag or digest.
func (s *Service) GetManifest(ctx context.Context, repoKey, image, ref string) (Manifest, error) {
	var item repository.Item

	if h, ok := ParseDigest(ref); ok {
		items, err := s.manifestsWithDigest(ctx, repoKey, image, h)
		if err != nil {
			return Manifest{}, err
		}

		if len(items) == 0 {
			return Manifest{}, ErrManifestUnknown
		}

		item = items[0]
	} else {
		var err error
		for _, name := range []string{
			ListFile,
			ManifestFile,
		} {
			item, err = s.repos.Stat(ctx, repoKey, image+"/"+ref+"/"+name)
			if err == nil {
				break
			}

			if !errors.Is(err, repository.ErrNotFound) {
				return Manifest{}, err
			}
		}

		if err != nil {
			return Manifest{}, ErrManifestUnknown
		}
	}

	m := Manifest{
		Item:   item,
		Digest: "sha256:" + item.Checksums.SHA256,
	}

	props, err := s.repos.Properties(ctx, item)
	if err != nil {
		return Manifest{}, err
	}

	if t := props["docker.manifest.type"]; len(t) > 0 {
		m.MediaType = t[0]
		return m, nil
	}

	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return Manifest{}, err
	}

	defer func() {
		_ = rc.Close()
	}()

	var doc manifestDoc
	if err := json.NewDecoder(io.LimitReader(rc, MaxManifestBytes)).Decode(&doc); err != nil {
		return Manifest{}, fmt.Errorf("%w: %s/%s", ErrManifestInvalid, repoKey, item.Path())
	}

	m.MediaType = mediaTypeOf("", doc)

	return m, nil
}

// DeleteManifest removes a tag, or every tag of a digest.
func (s *Service) DeleteManifest(ctx context.Context, repoKey, image, ref string) error {
	if _, ok := ParseDigest(ref); !ok {
		if _, err := s.GetManifest(ctx, repoKey, image, ref); err != nil {
			return err
		}

		return s.deleteTagFolder(ctx, repoKey, image+"/"+ref)
	}

	h, _ := ParseDigest(ref)

	items, err := s.manifestsWithDigest(ctx, repoKey, image, h)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		return ErrManifestUnknown
	}

	for _, it := range items {
		err := s.deleteTagFolder(ctx, repoKey, it.Parent)
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return err
		}
	}

	return nil
}

// manifestsWithDigest returns image's manifests with the hex digest h,
// leaving out those of the images nested under image.
func (s *Service) manifestsWithDigest(ctx context.Context, repoKey, image, h string) ([]repository.Item, error) {
	items, err := s.repos.FindBySHA256(ctx, repoKey, image, h)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(items, func(it repository.Item) bool {
		return path.Dir(it.Parent) != image || it.Name != ManifestFile && it.Name != ListFile
	}), nil
}

// deleteTagFolder deletes the manifest and blobs of a tag folder, which is
// also the folder of the image named after the tag.
func (s *Service) deleteTagFolder(ctx context.Context, repoKey, folder string) error {
	dir, err := s.repos.Stat(ctx, repoKey, folder)
	if err != nil {
		return err
	}

	children, err := s.repos.Children(ctx, dir)
	if err != nil {
		return err
	}

	if !slices.ContainsFunc(children, repository.Item.IsFolder) {
		return s.repos.Delete(ctx, repoKey, folder)
	}

	for _, c := range children {
		if c.IsFolder() {
			continue
		}

		if err := s.repos.Delete(ctx, repoKey, c.Path()); err != nil && !errors.Is(err, repository.ErrNotFound) {
			return err
		}
	}

	return nil
}

// tagFolderCopies copies or moves a tag folder without the images nested
// in it.
func (s *Service) tagFolderCopies(
	ctx context.Context,
	src repository.Item,
	destinationRepository, destinationPath string,
	move bool,
	user string,
) ([]repository.CopyInput, error) {
	children, err := s.repos.Children(ctx, src)
	if err != nil {
		return nil, err
	}

	if !slices.ContainsFunc(children, repository.Item.IsFolder) {
		return []repository.CopyInput{
			{
				Source:                src,
				DestinationRepository: destinationRepository,
				DestinationPath:       destinationPath,
				Move:                  move,
				User:                  user,
			},
		}, nil
	}

	var copies []repository.CopyInput

	for _, c := range children {
		if c.IsFolder() {
			continue
		}

		copies = append(copies, repository.CopyInput{
			Source:                c,
			DestinationRepository: destinationRepository,
			DestinationPath:       destinationPath + "/" + c.Name,
			Move:                  move,
			User:                  user,
		})
	}

	return copies, nil
}

// Blob finds a stored blob by hex digest, preferring copies under image.
func (s *Service) Blob(ctx context.Context, repoKey, image, h string) (repository.Item, error) {
	items, err := s.repos.FindBySHA256(ctx, repoKey, "", h)
	if err != nil {
		return repository.Item{}, err
	}

	if len(items) == 0 {
		return repository.Item{}, ErrBlobUnknown
	}

	for _, it := range items {
		if it.Parent == image || strings.HasPrefix(it.Parent, image+"/") {
			return it, nil
		}
	}

	return items[0], nil
}

// Tags lists image's tags in lexical order.
func (s *Service) Tags(ctx context.Context, repoKey, image string) ([]string, error) {
	items, err := s.repos.FilesNamed(ctx, repoKey, image, ManifestFile, ListFile)
	if err != nil {
		return nil, err
	}

	var tags []string

	for _, it := range items {
		dir, tag := path.Split(it.Parent)
		if strings.TrimSuffix(dir, "/") == image && ValidTag(tag) {
			tags = append(tags, tag)
		}
	}

	slices.Sort(tags)

	return slices.Compact(tags), nil
}

// Referrer is a manifest that names another as its subject.
type Referrer struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// Referrers lists an image's manifests whose subject is the digest,
// optionally only those of an artifact type.
func (s *Service) Referrers(ctx context.Context, repoKey, image, digest, artifactType string) ([]Referrer, error) {
	items, err := s.repos.FilesNamed(ctx, repoKey, image, ManifestFile, ListFile)
	if err != nil {
		return nil, err
	}

	out := []Referrer{}
	seen := map[string]bool{}

	for _, it := range items {
		if path.Dir(it.Parent) != image {
			continue
		}

		props, err := s.repos.Properties(ctx, it)
		if err != nil {
			return nil, err
		}

		if first(props[propSubject]) != digest ||
			(artifactType != "" && first(props[propArtifactType]) != artifactType) {
			continue
		}

		d := first(props["docker.manifest.digest"])
		if seen[d] {
			continue
		}

		seen[d] = true
		size, _ := strconv.ParseInt(first(props[propSize]), 10, 64)
		r := Referrer{
			MediaType:    first(props["docker.manifest.type"]),
			Digest:       d,
			Size:         size,
			ArtifactType: first(props[propArtifactType]),
		}
		_ = json.Unmarshal([]byte(first(props[propAnnotations])), &r.Annotations)
		out = append(out, r)
	}

	slices.SortFunc(out, func(a, b Referrer) int {
		return strings.Compare(a.Digest, b.Digest)
	})

	return out, nil
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}

	return v[0]
}

// Images lists the images in a repository in lexical order.
func (s *Service) Images(ctx context.Context, repoKey string) ([]string, error) {
	items, err := s.repos.FilesNamed(ctx, repoKey, "", ManifestFile, ListFile)
	if err != nil {
		return nil, err
	}

	var images []string

	for _, it := range items {
		if image := path.Dir(it.Parent); image != "." && ValidImage(image) {
			images = append(images, image)
		}
	}

	slices.Sort(images)

	return slices.Compact(images), nil
}

func uploadKey(id string, i int) string {
	return fmt.Sprintf("%sdocker-uploads/%s/%d", objectstore.TemporaryKeyPrefix, id, i)
}

func uploadStartKey(id string) string {
	return fmt.Sprintf("%sdocker-uploads/%s/started", objectstore.TemporaryKeyPrefix, id)
}

// StartUpload opens upload session id.
func (s *Service) StartUpload(ctx context.Context, id string) error {
	if err := s.blobs.Put(ctx, uploadStartKey(id), strings.NewReader(""), 0); err != nil {
		return fmt.Errorf("start upload: %w", err)
	}

	return nil
}

// FindUpload returns ErrUploadUnknown unless upload session id was started
// and is neither finished nor cancelled.
func (s *Service) FindUpload(ctx context.Context, id string) error {
	for _, key := range []string{
		uploadKey(id, 0),
		uploadStartKey(id),
	} {
		_, err := s.blobs.Head(ctx, key)
		if err == nil {
			return nil
		}

		if !errors.Is(err, objectstore.ErrNotFound) {
			return fmt.Errorf("find upload: %w", err)
		}
	}

	return ErrUploadUnknown
}

// AppendChunk stages chunk i of upload session id, of size bytes (-1 when
// unknown), and returns its size.
func (s *Service) AppendChunk(ctx context.Context, id string, i int, r io.Reader, size int64) (int64, error) {
	cr := &countingReader{
		r: r,
	}
	if err := s.blobs.Put(ctx, uploadKey(id, i), cr, size); err != nil {
		return 0, fmt.Errorf("stage upload chunk: %w", err)
	}

	return cr.n, nil
}

// UploadState is an upload session's staged chunks and bytes, from a known
// state forward: the state a client's upload URL carries may be stale (an
// earlier URL used again), so the chunks staged since are counted.
func (s *Service) UploadState(ctx context.Context, id string, chunks int, offset int64) (int, int64, error) {
	for chunks < MaxUploadChunks {
		info, err := s.blobs.Head(ctx, uploadKey(id, chunks))
		if errors.Is(err, objectstore.ErrNotFound) {
			break
		}

		if err != nil {
			return 0, 0, fmt.Errorf("read upload state: %w", err)
		}

		chunks++
		offset += info.Size
	}

	return chunks, offset, nil
}

// MaxUploadChunks bounds an upload session's chunks.
const MaxUploadChunks = 10000

// CancelUpload closes an upload session and discards its staged chunks.
func (s *Service) CancelUpload(ctx context.Context, id string, chunks int) {
	keys := make([]string, 0, chunks+1)

	keys = append(keys, uploadStartKey(id))
	for i := range chunks {
		keys = append(keys, uploadKey(id, i))
	}

	for _, key := range keys {
		if err := s.blobs.Delete(ctx, key); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			s.log.WarnContext(ctx, "delete upload chunk", "upload", id, "error", err)
		}
	}
}

// FinishUpload completes an upload session: the staged chunks followed by
// final must hash to h. size is the blob's expected length, -1 when unknown.
func (s *Service) FinishUpload(
	ctx context.Context,
	repoKey, image, id string,
	chunks int,
	final io.Reader,
	size int64,
	h, user string,
) (repository.Item, error) {
	body := &chunkReader{
		ctx:   ctx,
		s:     s,
		id:    id,
		n:     chunks,
		final: final,
	}
	defer body.close()

	item, err := s.repos.Deploy(ctx, repository.DeployInput{
		Repository: repoKey,
		Path:       image + "/" + uploadsDir + "/" + LayerName(h),
		Body:       body,
		Size:       size,
		User:       user,
		Expected: repository.Checksums{
			SHA256: h,
		},
		Props: repository.Properties{
			"sha256": {
				h,
			},
		},
	})
	if errors.Is(err, repository.ErrChecksumMismatch) {
		err = ErrDigestInvalid
	}

	if errors.Is(err, objectstore.ErrNotFound) {
		err = ErrUploadUnknown
	}

	if err == nil {
		s.CancelUpload(context.WithoutCancel(ctx), id, chunks)
	}

	return item, err
}

type chunkReader struct {
	ctx   context.Context
	s     *Service
	id    string
	n, i  int
	cur   io.ReadCloser
	final io.Reader
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for {
		if c.cur == nil {
			if c.i >= c.n {
				if c.final == nil {
					return 0, io.EOF
				}

				return c.final.Read(p)
			}

			rc, _, err := c.s.blobs.Get(c.ctx, uploadKey(c.id, c.i), nil)
			if err != nil {
				return 0, fmt.Errorf("open upload chunk: %w", err)
			}

			c.cur, c.i = rc, c.i+1
		}

		n, err := c.cur.Read(p)
		if errors.Is(err, io.EOF) {
			_ = c.cur.Close()
			c.cur, err = nil, nil

			if n == 0 {
				continue
			}
		}

		return n, err
	}
}

func (c *chunkReader) close() {
	if c.cur != nil {
		_ = c.cur.Close()
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}

// PromoteInput is an api/docker/<repo>/v2/promote request (contract §4.1).
type PromoteInput struct {
	SourceRepository, TargetRepository string
	Image, TargetImage                 string
	Tag, TargetTag                     string
	Copy                               bool
	User                               string
}

// Promote copies or moves a tag (or, with no tag, every tag) of an image to
// another repository, rewriting the manifests' docker.* properties.
func (s *Service) Promote(ctx context.Context, in PromoteInput) error {
	if in.TargetImage == "" {
		in.TargetImage = in.Image
	}

	tags := []string{
		in.Tag,
	}
	if in.Tag == "" {
		var err error
		if tags, err = s.Tags(ctx, in.SourceRepository, in.Image); err != nil {
			return err
		}

		if len(tags) == 0 {
			return ErrManifestUnknown
		}

		in.TargetTag = ""
	}

	for _, tag := range tags {
		m, err := s.GetManifest(ctx, in.SourceRepository, in.Image, tag)
		if err != nil {
			return err
		}

		targetTag := cmp.Or(in.TargetTag, tag)
		folders := []promotedFolder{
			{
				source: in.Image + "/" + tag,
				target: in.TargetImage + "/" + targetTag,
				move:   !in.Copy,
			},
		}

		if IsIndex(m.MediaType) {
			children, err := s.childFolders(ctx, m.Item)
			if err != nil {
				return err
			}

			kept := map[string]bool{}
			if !in.Copy {
				if kept, err = s.childFoldersOfOtherTags(ctx, in.SourceRepository, in.Image, tag); err != nil {
					return err
				}
			}

			for _, c := range children {
				folders = append(folders, promotedFolder{
					source: in.Image + "/" + c,
					target: in.TargetImage + "/" + c,
					move:   !in.Copy && !kept[c],
				})
			}
		}

		var (
			copies       []repository.CopyInput
			childTargets []string
		)

		for i, f := range folders {
			src, err := s.repos.Stat(ctx, in.SourceRepository, f.source)
			if errors.Is(err, repository.ErrNotFound) && i > 0 {
				continue
			}

			if err != nil {
				return err
			}

			if i > 0 {
				childTargets = append(childTargets, f.target)
			}

			folderCopies, err := s.tagFolderCopies(ctx, src, in.TargetRepository, f.target, f.move, in.User)
			if err != nil {
				return err
			}

			copies = append(copies, folderCopies...)
		}

		if err := s.repos.CopyAll(ctx, copies); err != nil {
			return err
		}

		moved, err := s.GetManifest(ctx, in.TargetRepository, in.TargetImage, targetTag)
		if err != nil {
			return err
		}

		props := repository.Properties{
			"docker.repoName": {
				in.TargetImage,
			},
			"docker.manifest": {
				targetTag,
			},
		}
		if err := s.repos.SetProperties(ctx, moved.Item, props, false); err != nil {
			return err
		}

		for _, target := range childTargets {
			child, err := s.repos.Stat(ctx, in.TargetRepository, target+"/"+ManifestFile)
			if errors.Is(err, repository.ErrNotFound) {
				continue
			}

			if err != nil {
				return err
			}

			if err := s.repos.SetProperties(ctx, child, repository.Properties{
				"docker.repoName": {
					in.TargetImage,
				},
			}, false); err != nil {
				return err
			}
		}
	}

	return nil
}

type promotedFolder struct {
	source string
	target string
	move   bool
}

// childFoldersOfOtherTags returns the platform manifest folders that the
// indexes of image's other tags and digests reference: a move of tag leaves
// them in place.
func (s *Service) childFoldersOfOtherTags(ctx context.Context, repoKey, image, tag string) (map[string]bool, error) {
	indexes, err := s.repos.FilesNamed(ctx, repoKey, image, ListFile)
	if err != nil {
		return nil, err
	}

	folders := map[string]bool{}

	for _, index := range indexes {
		if path.Dir(index.Parent) != image || index.Parent == image+"/"+tag {
			continue
		}

		children, err := s.childFolders(ctx, index)
		if errors.Is(err, ErrManifestInvalid) {
			continue
		}

		if err != nil {
			return nil, err
		}

		for _, c := range children {
			folders[c] = true
		}
	}

	return folders, nil
}

func (s *Service) childFolders(ctx context.Context, index repository.Item) ([]string, error) {
	rc, err := s.repos.Open(ctx, index, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()

	var doc manifestDoc
	if err := json.NewDecoder(io.LimitReader(rc, MaxManifestBytes)).Decode(&doc); err != nil {
		return nil, ErrManifestInvalid
	}

	var out []string

	for _, d := range doc.Manifests {
		if h, ok := ParseDigest(d.Digest); ok {
			out = append(out, LayerName(h))
		}
	}

	return out, nil
}

// UploadTTL is how long a pushed blob that no manifest references is kept.
const UploadTTL = 24 * time.Hour

// ExpireUploads deletes pushed blobs that no manifest took in within
// olderThan (UploadTTL), and returns how many it deleted.
func ExpireUploads(ctx context.Context, repos *repository.Service, olderThan time.Duration) (int, error) {
	return repos.DeleteStaleFiles(ctx, uploadsDir, []repository.Format{
		Format,
		repository.FormatHelm,
		repository.FormatHelmOCI,
		repository.FormatOCI,
	}, olderThan)
}

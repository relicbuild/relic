// Package evidence stores signed evidence about artifacts, builds and
// releases.
package evidence

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"uuid"

	"relic/internal/artifacts/repository"
	"relic/internal/platform/database"
)

// Domain errors.
var (
	ErrInvalid     = errors.New("invalid evidence")
	ErrKeyNotFound = errors.New("signing key not found")
	ErrKeyExists   = errors.New("key alias already exists")
)

// Repository holds the evidence files.
const Repository = "relic-evidence"

const maxEnvelope = 16 << 20

// Attachment is a file attached to an evidence statement, and where it is
// stored when the envelope said (Repository and Path).
type Attachment struct {
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	Type       string `json:"type"`
	Repository string `json:"repository,omitempty"`
	Path       string `json:"path,omitempty"`
}

// Evidence is one stored evidence file.
type Evidence struct {
	ID                uuid.UUID
	SubjectRepository string
	SubjectPath       string // "" at the repository root
	SubjectName       string
	SubjectSHA256     string
	PredicateType     string
	PredicateSlug     string
	PredicateCategory string
	Predicate         json.RawMessage
	ProviderID        string
	KeyAlias          string
	PublicKey         string // the trusted key's PEM, when it still exists
	Verified          bool
	Attachments       []Attachment
	ItemPath          string // path in Repository
	ItemSHA256        string
	CreatedBy         string
	Created           time.Time
}

// Service stores and finds evidence.
type Service struct {
	db    *database.DB
	repos *repository.Service
	log   *slog.Logger
	now   func() time.Time
}

// New returns a Service.
func New(db *database.DB, repos *repository.Service, log *slog.Logger) *Service {
	return &Service{
		db:    db,
		repos: repos,
		log:   log,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

type statement struct {
	Subject []struct {
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	PredicateType string          `json:"predicateType"`
	Predicate     json.RawMessage `json:"predicate"`
	Attachments   []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Type   string `json:"type"`
	} `json:"attachments"`
}

// Input is evidence to store.
type Input struct {
	// Repository and Path name the subject file.
	Repository string
	Path       string
	// Envelope is the DSSE envelope, stored as it was sent.
	Envelope []byte
	// Attachments say where the statement's attachments are stored.
	Attachments []AttachmentPlace
	// Provider names what produced the evidence.
	Provider string
	User     string
}

// Create stores the envelope as evidence for the subject file.
func (s *Service) Create(ctx context.Context, in Input) (Evidence, error) {
	subjectRepo, subjectPath, body := in.Repository, in.Path, in.Envelope
	if len(body) > maxEnvelope {
		return Evidence{}, fmt.Errorf("%w: envelope too large", ErrInvalid)
	}

	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Evidence{}, fmt.Errorf("%w: not a DSSE envelope: %w", ErrInvalid, err)
	}

	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil || env.PayloadType == "" || len(env.Signatures) == 0 {
		return Evidence{}, fmt.Errorf("%w: envelope needs a payload, a payloadType and a signature", ErrInvalid)
	}

	var st statement
	if err := json.Unmarshal(payload, &st); err != nil || st.PredicateType == "" {
		return Evidence{}, fmt.Errorf("%w: payload is not an in-toto statement", ErrInvalid)
	}

	subject, err := s.repos.Stat(ctx, subjectRepo, subjectPath)
	if err != nil {
		return Evidence{}, err
	}

	if !subjectDigestMatches(st, subject.Checksums.SHA256) {
		return Evidence{}, fmt.Errorf(
			"%w: the statement's subject digest does not match %s/%s",
			ErrInvalid,
			subjectRepo,
			subjectPath,
		)
	}

	alias, err := s.verifyEnvelope(ctx, env, payload)
	if err != nil {
		return Evidence{}, err
	}

	return s.store(ctx, in, subject, st, alias, true)
}

func (s *Service) verifyEnvelope(ctx context.Context, env Envelope, payload []byte) (string, error) {
	var (
		unverifiedAlias string
		missing         error
	)

	for _, sig := range env.Signatures {
		if sig.KeyID == "" {
			continue
		}

		key, err := s.trustedKey(ctx, sig.KeyID)
		if errors.Is(err, ErrKeyNotFound) {
			missing = cmp.Or(missing, err)
			continue
		}

		if err != nil {
			return "", err
		}

		if verifySignature(key, env.PayloadType, payload, sig.Sig) {
			return sig.KeyID, nil
		}

		unverifiedAlias = cmp.Or(unverifiedAlias, sig.KeyID)
	}

	switch {
	case unverifiedAlias != "":
		return "", fmt.Errorf("%w: the signature does not verify with the trusted key %s", ErrInvalid, unverifiedAlias)
	case missing != nil:
		return "", missing
	}

	return "", fmt.Errorf("%w: no signature names a trusted key in its keyid", ErrInvalid)
}

// Record stores an unsigned statement Relic makes about the subject file.
func (s *Service) Record(
	ctx context.Context,
	subjectRepo, subjectPath, predicateType string,
	predicate any,
	user string,
) (Evidence, error) {
	subject, err := s.repos.Stat(ctx, subjectRepo, subjectPath)
	if err != nil {
		return Evidence{}, err
	}

	pred, err := json.Marshal(predicate)
	if err != nil {
		return Evidence{}, fmt.Errorf("encode predicate: %w", err)
	}

	payload, err := json.Marshal(map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []map[string]any{
			{
				"name": subject.Name,
				"digest": map[string]string{
					"sha256": subject.Checksums.SHA256,
				},
			},
		},
		"predicateType": predicateType,
		"predicate":     json.RawMessage(pred),
	})
	if err != nil {
		return Evidence{}, fmt.Errorf("encode statement: %w", err)
	}

	body, err := json.Marshal(Envelope{
		PayloadType: "application/vnd.in-toto+json",
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{},
	})
	if err != nil {
		return Evidence{}, fmt.Errorf("encode envelope: %w", err)
	}

	st := statement{
		PredicateType: predicateType,
		Predicate:     pred,
	}

	return s.store(ctx, Input{
		Repository: subjectRepo,
		Path:       subjectPath,
		Envelope:   body,
		Provider:   "relic",
		User:       user,
	}, subject, st, "", false)
}

func (s *Service) store(
	ctx context.Context,
	in Input,
	subject repository.Item,
	st statement,
	alias string,
	verified bool,
) (Evidence, error) {
	subjectRepo, body, providerID, user := in.Repository, in.Envelope, in.Provider, in.User

	if err := s.ensureRepo(ctx); err != nil {
		return Evidence{}, err
	}

	e := Evidence{
		ID:                uuid.NewV7(),
		SubjectRepository: subjectRepo,
		SubjectName:       subject.Name,
		SubjectSHA256:     subject.Checksums.SHA256,
		PredicateType:     st.PredicateType,
		PredicateSlug:     slug(st.PredicateType),
		PredicateCategory: "custom",
		ProviderID:        providerID,
		KeyAlias:          alias,
		Verified:          verified,
		CreatedBy:         user,
		Created:           s.now(),
	}
	if subject.Parent != repository.RootPath {
		e.SubjectPath = subject.Parent
	}

	if len(st.Predicate) > 0 && st.Predicate[0] == '{' {
		e.Predicate = st.Predicate
	}

	for _, a := range st.Attachments {
		att := Attachment{
			Name:   a.Name,
			SHA256: a.SHA256,
			Type:   a.Type,
		}
		for _, p := range in.Attachments {
			if strings.EqualFold(p.SHA256, a.SHA256) {
				att.Repository, att.Path = p.Repository, p.Path
			}
		}

		e.Attachments = append(e.Attachments, att)
	}

	e.ItemPath = subjectRepo + "/" + subject.Path() + "/" + e.PredicateSlug + "-" + e.ID.String() + ".json"

	item, err := s.repos.Deploy(
		ctx,
		repository.DeployInput{
			Repository: Repository,
			Path:       e.ItemPath,
			Body:       bytes.NewReader(body),
			User:       user,
		},
	)
	if err != nil {
		return Evidence{}, fmt.Errorf("store evidence: %w", err)
	}

	e.ItemSHA256 = item.Checksums.SHA256
	if err := s.insert(ctx, e); err != nil {
		if derr := s.repos.Delete(ctx, Repository, e.ItemPath); derr != nil {
			s.log.WarnContext(ctx, "clean up evidence file", "path", e.ItemPath, "error", derr)
		}

		return Evidence{}, err
	}

	return e, nil
}

func subjectDigestMatches(st statement, sha string) bool {
	for _, sub := range st.Subject {
		if strings.EqualFold(sub.Digest["sha256"], sha) {
			return true
		}
	}

	return false
}

func slug(predicateType string) string {
	parts := strings.FieldsFunc(
		predicateType,
		func(r rune) bool {
			return r == '/' || r == ':' || r == '#' || r == '?'
		},
	)
	for _, part := range slices.Backward(parts) {
		p := strings.ToLower(part)
		if p == "" || (p[0] == 'v' && len(p) > 1 && p[1] >= '0' && p[1] <= '9') || (p[0] >= '0' && p[0] <= '9') {
			continue
		}

		var b strings.Builder

		for _, r := range p {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
				b.WriteRune(r)
			}
		}

		if b.Len() > 0 {
			return b.String()
		}
	}

	return "evidence"
}

func (s *Service) insert(ctx context.Context, e Evidence) error {
	atts, err := json.Marshal(nonNil(e.Attachments))
	if err != nil {
		return fmt.Errorf("encode attachments: %w", err)
	}

	var predicate []byte
	if len(e.Predicate) > 0 {
		predicate = e.Predicate
	}

	err = s.db.CreateEvidence(ctx, database.CreateEvidenceParams{
		ID:                e.ID,
		SubjectRepo:       e.SubjectRepository,
		SubjectPath:       e.SubjectPath,
		SubjectName:       e.SubjectName,
		SubjectSha256:     e.SubjectSHA256,
		PredicateType:     e.PredicateType,
		PredicateSlug:     e.PredicateSlug,
		PredicateCategory: e.PredicateCategory,
		Predicate:         predicate,
		ProviderID:        e.ProviderID,
		KeyAlias:          e.KeyAlias,
		Verified:          e.Verified,
		Attachments:       atts,
		ItemPath:          e.ItemPath,
		ItemSha256:        e.ItemSHA256,
		CreatedBy:         e.CreatedBy,
		Created:           e.Created,
	})
	if err != nil {
		return fmt.Errorf("record evidence: %w", err)
	}

	return nil
}

// ForSubject lists the evidence for a file, newest first.
func (s *Service) ForSubject(ctx context.Context, repoKey, path, name string) ([]Evidence, error) {
	return s.subjectEvidence(ctx, repoKey, path, name, false)
}

// ForCurrentContent lists the evidence about a file's current content, newest
// first.
func (s *Service) ForCurrentContent(ctx context.Context, repoKey, path, name string) ([]Evidence, error) {
	return s.subjectEvidence(ctx, repoKey, path, name, true)
}

func (s *Service) subjectEvidence(
	ctx context.Context,
	repoKey, path, name string,
	currentContent bool,
) ([]Evidence, error) {
	subject, err := s.repos.Stat(ctx, repoKey, repository.JoinPath(path, name))
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrRepositoryNotFound) {
		return []Evidence{}, nil
	}

	if err != nil {
		return nil, err
	}

	rows, err := s.db.ListEvidenceForSubject(
		ctx,
		database.ListEvidenceForSubjectParams{
			SubjectRepo: repoKey,
			SubjectPath: path,
			SubjectName: name,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list evidence: %w", err)
	}

	keys := map[string]string{}

	out := make([]Evidence, 0, len(rows))
	for _, r := range rows {
		current := r.SubjectSha256 == "" || strings.EqualFold(r.SubjectSha256, subject.Checksums.SHA256)
		if r.Created.Before(subject.Created) || (currentContent && !current) {
			continue
		}

		e := Evidence{
			ID:                r.ID,
			SubjectRepository: r.SubjectRepo,
			SubjectPath:       r.SubjectPath,
			SubjectName:       r.SubjectName,
			SubjectSHA256:     r.SubjectSha256,
			PredicateType:     r.PredicateType,
			PredicateSlug:     r.PredicateSlug,
			PredicateCategory: r.PredicateCategory,
			Predicate:         r.Predicate,
			ProviderID:        r.ProviderID,
			KeyAlias:          r.KeyAlias,
			Verified:          r.Verified && current,
			ItemPath:          r.ItemPath,
			ItemSHA256:        r.ItemSha256,
			CreatedBy:         r.CreatedBy,
			Created:           r.Created,
		}
		if err := json.Unmarshal(r.Attachments, &e.Attachments); err != nil {
			return nil, fmt.Errorf("decode attachments: %w", err)
		}

		if e.KeyAlias != "" {
			pk, ok := keys[e.KeyAlias]
			if !ok {
				if k, err := s.db.GetTrustedKeyByAlias(ctx, e.KeyAlias); err == nil {
					pk = k.PublicKey
				}

				keys[e.KeyAlias] = pk
			}

			e.PublicKey = pk
		}

		out = append(out, e)
	}

	return out, nil
}

func (s *Service) ensureRepo(ctx context.Context) error {
	if _, err := s.repos.GetRepository(ctx, Repository); err == nil {
		return nil
	}

	r := repository.Repository{
		Name:   Repository,
		Class:  repository.ClassLocal,
		Format: repository.FormatEvidence,
	}
	if err := s.repos.CreateRepository(ctx, r); err != nil && !errors.Is(err, repository.ErrRepositoryExists) {
		return err
	}

	return nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}

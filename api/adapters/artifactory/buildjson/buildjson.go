// Package buildjson translates the adapter's build documents.
package buildjson

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"relic/internal/delivery/buildinfo"

	"github.com/jfrog/build-info-go/entities"
)

// TimeFormat is build-info's timestamp layout.
const TimeFormat = "2006-01-02T15:04:05.000-0700"

// Format reads build-info JSON.
type Format struct{}

var _ buildinfo.Format = Format{}

// Parse reads a build-info document.
func (Format) Parse(raw []byte) (buildinfo.Document, error) {
	var bi entities.BuildInfo
	if err := json.Unmarshal(raw, &bi); err != nil {
		return buildinfo.Document{}, fmt.Errorf("decode build info: %w", err)
	}

	doc := buildinfo.Document{
		Name:       bi.Name,
		Number:     bi.Number,
		Properties: bi.Properties,
	}

	started, err := ParseTime(bi.Started)
	if err != nil {
		return buildinfo.Document{}, fmt.Errorf("started: %w", err)
	}

	doc.Started = started
	if bi.Agent != nil {
		doc.Agent = buildinfo.Agent{
			Name:    bi.Agent.Name,
			Version: bi.Agent.Version,
		}
	}

	for _, v := range bi.VcsList {
		doc.VCS = append(doc.VCS, buildinfo.VCS{
			URL:      v.Url,
			Revision: v.Revision,
			Branch:   v.Branch,
		})
	}

	for _, m := range bi.Modules {
		mod := buildinfo.Module{
			ID:   m.Id,
			Type: string(m.Type),
		}
		for _, a := range m.Artifacts {
			mod.Artifacts = append(mod.Artifacts, buildinfo.Artifact{
				Name:   a.Name,
				Type:   a.Type,
				Path:   a.Path,
				SHA1:   a.Sha1,
				SHA256: a.Sha256,
				MD5:    a.Md5,
			})
		}

		for _, d := range m.Dependencies {
			mod.Dependencies = append(mod.Dependencies, buildinfo.Dependency{
				ID:     d.Id,
				Type:   d.Type,
				Scopes: d.Scopes,
				SHA1:   d.Sha1,
				SHA256: d.Sha256,
				MD5:    d.Md5,
			})
		}

		doc.Modules = append(doc.Modules, mod)
	}

	return doc, nil
}

// Render writes doc as build-info JSON.
func (Format) Render(doc buildinfo.Document) ([]byte, error) {
	bi := entities.BuildInfo{
		Name:       doc.Name,
		Number:     doc.Number,
		Started:    doc.Started.Format(TimeFormat),
		Properties: doc.Properties,
	}
	if doc.Agent != (buildinfo.Agent{}) {
		bi.Agent = &entities.Agent{
			Name:    doc.Agent.Name,
			Version: doc.Agent.Version,
		}
	}

	for _, v := range doc.VCS {
		bi.VcsList = append(bi.VcsList, entities.Vcs{
			Url:      v.URL,
			Revision: v.Revision,
			Branch:   v.Branch,
		})
	}

	for _, m := range doc.Modules {
		mod := entities.Module{
			Id:   m.ID,
			Type: entities.ModuleType(m.Type),
		}
		for _, a := range m.Artifacts {
			mod.Artifacts = append(mod.Artifacts, entities.Artifact{
				Name:   a.Name,
				Type:   a.Type,
				Path:   a.Path,
				Sha1:   a.SHA1,
				Sha256: a.SHA256,
				Md5:    a.MD5,
			})
		}

		for _, d := range m.Dependencies {
			mod.Dependencies = append(mod.Dependencies, entities.Dependency{
				Id:     d.ID,
				Type:   d.Type,
				Scopes: d.Scopes,
				Sha1:   d.SHA1,
				Sha256: d.SHA256,
				Md5:    d.MD5,
			})
		}

		bi.Modules = append(bi.Modules, mod)
	}

	return json.Marshal(bi)
}

// ParseTime reads a build-info time.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeFormat, s)
	if err != nil {
		if t, err = time.Parse(time.RFC3339Nano, s); err != nil {
			return time.Time{}, fmt.Errorf("%q is not %s", s, TimeFormat)
		}
	}

	return t, nil
}

// Promotion is a build promotion body.
type Promotion struct {
	Comment             string              `json:"comment,omitempty"`
	SourceRepo          string              `json:"sourceRepo,omitempty"`
	TargetRepo          string              `json:"targetRepo,omitempty"`
	Status              string              `json:"status,omitempty"`
	IncludeDependencies bool                `json:"dependencies"`
	Copy                bool                `json:"copy"`
	FailFast            bool                `json:"failFast"`
	DryRun              bool                `json:"dryRun"`
	Properties          map[string][]string `json:"properties,omitempty"`
}

// Native returns Relic's promotion.
func (p Promotion) Native() buildinfo.Promotion {
	return buildinfo.Promotion{
		Status:           p.Status,
		Comment:          p.Comment,
		SourceRepository: p.SourceRepo,
		TargetRepository: p.TargetRepo,
		Dependencies:     p.IncludeDependencies,
		Copy:             p.Copy,
		FailFast:         p.FailFast,
		DryRun:           p.DryRun,
		Properties:       p.Properties,
	}
}

// PromotionOf returns the body of a promotion.
func PromotionOf(p buildinfo.Promotion) Promotion {
	return Promotion{
		Comment:             p.Comment,
		SourceRepo:          p.SourceRepository,
		TargetRepo:          p.TargetRepository,
		Status:              p.Status,
		IncludeDependencies: p.Dependencies,
		Copy:                p.Copy,
		FailFast:            p.FailFast,
		DryRun:              p.DryRun,
		Properties:          p.Properties,
	}
}

// Message is a line of a promotion report ({"level":"WARNING",...}).
type Message struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Messages renders a promotion report; never nil, so the JSON is an array.
func Messages(list []buildinfo.Message) []Message {
	out := make([]Message, 0, len(list))
	for _, m := range list {
		out = append(out, Message{
			Level:   strings.ToUpper(string(m.Level)),
			Message: m.Text,
		})
	}

	return out
}

// Retention is a build retention body.
type Retention struct {
	MinimumBuildDate string   `json:"minimumBuildDate,omitempty"`
	Count            string   `json:"count,omitempty"`
	ExcludeBuilds    []string `json:"buildNumbersNotToBeDiscarded,omitempty"`
	DeleteArtifacts  bool     `json:"deleteBuildArtifacts"`
}

// Native returns Relic's retention.
func (r Retention) Native() (buildinfo.Retention, error) {
	out := buildinfo.Retention{
		Keep:            r.ExcludeBuilds,
		DeleteArtifacts: r.DeleteArtifacts,
	}
	if r.Count != "" {
		n, err := strconv.Atoi(r.Count)
		if err != nil || n < 1 {
			return buildinfo.Retention{}, fmt.Errorf(
				"%w: Max count retention needs to be a positive number",
				buildinfo.ErrInvalid,
			)
		}

		out.MaxRuns = n
	}

	if r.MinimumBuildDate != "" {
		t, err := time.Parse(TimeFormat, r.MinimumBuildDate)
		if err != nil {
			return buildinfo.Retention{}, fmt.Errorf(
				"%w: minimumBuildDate %q",
				buildinfo.ErrInvalid,
				r.MinimumBuildDate,
			)
		}

		out.MinStarted = t
	}

	return out, nil
}

// RetentionOf returns the body of a retention.
func RetentionOf(r buildinfo.Retention) Retention {
	out := Retention{
		ExcludeBuilds:   r.Keep,
		DeleteArtifacts: r.DeleteArtifacts,
	}
	if r.MaxRuns > 0 {
		out.Count = strconv.Itoa(r.MaxRuns)
	}

	if !r.MinStarted.IsZero() {
		out.MinimumBuildDate = r.MinStarted.Format(TimeFormat)
	}

	return out
}

type status struct {
	Status        string `json:"status"`
	Comment       string `json:"comment,omitempty"`
	Repository    string `json:"repository,omitempty"`
	Timestamp     string `json:"timestamp"`
	User          string `json:"user"`
	TimestampDate int64  `json:"timestampDate"`
}

// View returns the run's document as published, with its promotions added
// as "statuses".
func View(run buildinfo.BuildRun) (json.RawMessage, error) {
	if len(run.Statuses) == 0 {
		return run.Raw, nil
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(run.Raw, &doc); err != nil {
		return nil, fmt.Errorf("decode stored build: %w", err)
	}

	statuses := make([]status, len(run.Statuses))
	for i, s := range run.Statuses {
		statuses[i] = status{
			Status:        s.Status,
			Comment:       s.Comment,
			Repository:    s.Repository,
			Timestamp:     s.Created.Format(TimeFormat),
			User:          s.User,
			TimestampDate: s.Created.UnixMilli(),
		}
	}

	var err error
	if doc["statuses"], err = json.Marshal(statuses); err != nil {
		return nil, fmt.Errorf("encode statuses: %w", err)
	}

	return json.Marshal(doc)
}

const schemaVersion = "1.0.1"

// Prepare adds the fields set on publish.
func (Format) Prepare(
	ctx context.Context,
	raw []byte,
	sha256Of func(ctx context.Context, sha1 string) (string, bool, error),
) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}

	changed := false
	if _, ok := doc["version"]; !ok {
		doc["version"], changed = schemaVersion, true
	}

	modules, _ := doc["modules"].([]any)
	for _, m := range modules {
		mod, _ := m.(map[string]any)
		for _, key := range []string{
			"artifacts",
			"dependencies",
		} {
			list, _ := mod[key].([]any)
			for _, e := range list {
				entry, _ := e.(map[string]any)

				sha1, _ := entry["sha1"].(string)
				if _, has := entry["sha256"]; has || sha1 == "" {
					continue
				}

				sha256, ok, err := sha256Of(ctx, sha1)
				if err != nil {
					return nil, err
				}

				if ok {
					entry["sha256"], changed = sha256, true
				}
			}
		}
	}

	if !changed {
		return raw, nil
	}

	return json.Marshal(doc)
}

var specialChars = map[rune]string{
	'/':  "%2F",
	'\\': "%5C",
	'|':  "%7C",
	'*':  "%2A",
	'?':  "%3F",
	'\'': "%22",
	':':  "%3A",
	';':  "%3B",
	'%':  "%25",
}

// EncodeName encodes a build name or number for the build-info repository.
func EncodeName(v string) string {
	var b strings.Builder

	for _, r := range v {
		if unicode.IsSpace(r) {
			r = ' '
		}

		if enc, ok := specialChars[r]; ok {
			b.WriteString(enc)
		} else {
			b.WriteRune(r)
		}
	}

	s := strings.ReplaceAll(b.String(), "%2F ", "%2F%20")

	return strings.ReplaceAll(s, " %2F", "%20%2F")
}

// Path is the build-info file's path.
func (Format) Path(name, number string, started time.Time) string {
	return EncodeName(name) + "/" + EncodeName(number) + "-" + strconv.FormatInt(started.UnixMilli(), 10) + ".json"
}

// Package repoconfig translates the adapter's repository configuration
// documents (api/repositories) to and from Relic's repositories.
package repoconfig

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"relic/internal/artifacts/repository"
)

// Extension is the repo.Config extension that holds the fields Relic does
// not model.
const Extension = "artifactory"

const (
	defaultCachePeriodSecs = 7200
	defaultTimeoutMillis   = 15000
)

// FromArtifactory reads a repository configuration document sent for key.
func FromArtifactory(key string, doc []byte) (repository.Repository, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return repository.Repository{}, fmt.Errorf("%w: %w", repository.ErrInvalidRepository, err)
	}

	d := &document{
		fields: fields,
	}
	if k := d.str("key"); k != "" && k != key {
		return repository.Repository{}, fmt.Errorf(
			"%w: key %q does not match URL key %q",
			repository.ErrInvalidRepository,
			k,
			key,
		)
	}

	r := repository.Repository{
		Name:    key,
		Class:   repository.Class(d.str("rclass")),
		Project: d.str("projectKey"),
	}
	if r.Class == "federated" {
		return repository.Repository{}, fmt.Errorf(
			"%w: federated repositories are not supported",
			repository.ErrInvalidRepository,
		)
	}

	t := d.str("packageType")
	if t != "" {
		f, ok := FormatOf(t)
		if !ok {
			return repository.Repository{}, fmt.Errorf(
				"%w: unsupported package type %q",
				repository.ErrInvalidRepository,
				t,
			)
		}

		r.Format = f
	}

	d.take("key", "rclass")

	if _, alias := aliases[strings.ToLower(strings.TrimSpace(t))]; !alias {
		d.take("packageType")
	}

	c := &r.Config
	c.Description, c.Notes = d.str("description"), d.str("notes")
	c.SigningKey = d.str("primaryKeyPairRef")

	var err error
	if c.Environments, err = d.list("environments"); err != nil {
		return repository.Repository{}, err
	}

	if c.Architectures, err = d.list("debianDefaultArchitectures"); err != nil {
		return repository.Repository{}, err
	}

	if len(c.Architectures) > 0 {
		d.take("debianDefaultArchitectures")
	} else {
		if c.Architectures, err = d.list("debianArchitectures"); err != nil {
			return repository.Repository{}, err
		}

		d.take("debianArchitectures")
	}

	d.take("description", "notes", "environments")
	d.takeSet("projectKey", "primaryKeyPairRef")

	if r.Format == repository.FormatRPM && d.has("yumRootDepth") {
		depth, err := d.integer("yumRootDepth", 0)
		if err != nil {
			return repository.Repository{}, err
		}

		if depth < 0 || depth > 16 {
			return repository.Repository{}, fmt.Errorf(
				"%w: yumRootDepth %d is not between 0 and 16",
				repository.ErrInvalidRepository,
				depth,
			)
		}

		c.MetadataDepth = int32(depth)

		d.take("yumRootDepth")
	}

	switch r.Class {
	case repository.ClassRemote:
		if c.Remote, err = remoteFrom(d, r.Format); err != nil {
			return repository.Repository{}, err
		}
	case repository.ClassVirtual:
		members, err := d.list("repositories")
		if err != nil {
			return repository.Repository{}, err
		}

		c.Virtual = &repository.VirtualConfig{
			Members:  members,
			DeployTo: d.str("defaultDeploymentRepo"),
		}
		d.take("repositories")
		d.takeSet("defaultDeploymentRepo")
	default:
	}

	if err := d.err; err != nil {
		return repository.Repository{}, err
	}

	if err := r.Validate(); err != nil {
		return repository.Repository{}, err
	}

	if len(d.fields) > 0 {
		ext, err := json.Marshal(d.fields)
		if err != nil {
			return repository.Repository{}, fmt.Errorf("encode repository %s: %w", key, err)
		}

		c.Extensions = map[string]json.RawMessage{
			Extension: ext,
		}
	}

	return r, nil
}

func remoteFrom(d *document, format repository.Format) (*repository.RemoteConfig, error) {
	rc := &repository.RemoteConfig{
		URL:                 d.str("url"),
		Username:            d.str("username"),
		Password:            d.str("password"),
		CachePeriodSeconds:  defaultCachePeriodSecs,
		TimeoutMilliseconds: defaultTimeoutMillis,
	}

	var err error
	if rc.Offline, err = d.boolean("offline", false); err != nil {
		return nil, err
	}

	store, err := d.boolean("storeArtifactsLocally", true)
	if err != nil {
		return nil, err
	}

	rc.DisableCache = !store
	if rc.CachePeriodSeconds, err = d.integer("retrievalCachePeriodSecs", rc.CachePeriodSeconds); err != nil {
		return nil, err
	}

	if rc.TimeoutMilliseconds, err = d.integer("socketTimeoutMillis", rc.TimeoutMilliseconds); err != nil {
		return nil, err
	}

	if d.has("missedRetrievalCachePeriodSecs") {
		missed, err := d.integer("missedRetrievalCachePeriodSecs", 0)
		if err != nil {
			return nil, err
		}

		rc.MissedRetrievalCachePeriodSeconds = &missed
	}

	d.take(
		"url",
		"username",
		"password",
		"offline",
		"storeArtifactsLocally",
		"retrievalCachePeriodSecs",
		"socketTimeoutMillis",
		"missedRetrievalCachePeriodSecs",
	)

	switch format {
	case repository.FormatPyPI:
		rc.IndexURL = d.str("pyPIRegistryUrl")
		d.take("pyPIRegistryUrl")
	case repository.FormatNuGet:
		rc.IndexURL = d.str("v3FeedUrl")
		d.takeSet("v3FeedUrl")
	default:
	}

	return rc, nil
}

// ToArtifactory renders r as a repository configuration document.
// The password is never included: it reads back empty.
func ToArtifactory(r repository.Repository) ([]byte, error) {
	out := map[string]any{}

	if raw := r.Config.Extensions[Extension]; len(raw) > 0 {
		var ext map[string]json.RawMessage
		if err := json.Unmarshal(raw, &ext); err != nil {
			return nil, fmt.Errorf("decode %s extension of %s: %w", Extension, r.Name, err)
		}

		for k, v := range ext {
			out[k] = v
		}
	}

	c := r.Config
	out["key"], out["rclass"], out["packageType"] = r.Name, r.Class, WirePackageType(r)
	out["description"], out["notes"] = c.Description, c.Notes
	out["environments"] = nonNil(c.Environments)
	setIf(out, "projectKey", r.Project)
	setIf(out, "primaryKeyPairRef", r.KeyPairRef())
	setIf(out, "debianDefaultArchitectures", strings.Join(c.Architectures, ","))

	if r.Format == repository.FormatRPM {
		out["yumRootDepth"] = c.MetadataDepth
	}

	if rc := c.Remote; rc != nil {
		out["url"], out["username"], out["password"] = rc.URL, rc.Username, ""
		out["offline"], out["storeArtifactsLocally"] = rc.Offline, !rc.DisableCache
		out["retrievalCachePeriodSecs"], out["socketTimeoutMillis"] = rc.CachePeriodSeconds, rc.TimeoutMilliseconds
		out["missedRetrievalCachePeriodSecs"] = int64(rc.MissedRetrievalPeriod() / time.Second)

		switch r.Format {
		case repository.FormatPyPI:
			out["pyPIRegistryUrl"] = rc.IndexURL
		case repository.FormatNuGet:
			setIf(out, "v3FeedUrl", rc.IndexURL)
		default:
		}
	}

	if vc := c.Virtual; vc != nil {
		out["repositories"] = nonNil(vc.Members)
		setIf(out, "defaultDeploymentRepo", vc.DeployTo)
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode repository %s: %w", r.Name, err)
	}

	return b, nil
}

// Merge applies an update document to existing.
// A remote's "url" and "password" are also kept when sent empty.
func Merge(existing repository.Repository, patch []byte) (repository.Repository, error) {
	var next map[string]json.RawMessage
	if err := json.Unmarshal(patch, &next); err != nil {
		return repository.Repository{}, fmt.Errorf("%w: %w", repository.ErrInvalidRepository, err)
	}

	current, err := ToArtifactory(existing)
	if err != nil {
		return repository.Repository{}, err
	}

	var merged map[string]json.RawMessage
	if err := json.Unmarshal(current, &merged); err != nil {
		return repository.Repository{}, fmt.Errorf("decode stored repository %s: %w", existing.Name, err)
	}

	for k, v := range next {
		if (k == "url" || k == "password") && (string(v) == `""` || string(v) == "null") {
			continue
		}

		merged[k] = v
	}

	doc, err := json.Marshal(merged)
	if err != nil {
		return repository.Repository{}, fmt.Errorf("encode repository %s: %w", existing.Name, err)
	}

	r, err := FromArtifactory(existing.Name, doc)
	if err != nil {
		return repository.Repository{}, err
	}

	if r.Class != existing.Class {
		return repository.Repository{}, fmt.Errorf(
			"%w: cannot change rclass from %s to %s",
			repository.ErrInvalidRepository,
			existing.Class,
			r.Class,
		)
	}

	if r.Format != existing.Format {
		return repository.Repository{}, fmt.Errorf(
			"%w: cannot change packageType from %s to %s",
			repository.ErrInvalidRepository,
			existing.Format,
			r.Format,
		)
	}

	if r.Config.Remote != nil && r.Config.Remote.Password == "" && existing.Config.Remote != nil &&
		repository.SameOrigin(existing.Config.Remote.URL, r.Config.Remote.URL) {
		r.Config.Remote.Password = existing.Config.Remote.Password
	}

	return r, nil
}

func setIf(out map[string]any, k, v string) {
	if v != "" {
		out[k] = v
	}
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}

	return l
}

type document struct {
	fields map[string]json.RawMessage
	err    error
}

func (d *document) take(keys ...string) {
	for _, k := range keys {
		delete(d.fields, k)
	}
}

func (d *document) takeSet(keys ...string) {
	for _, k := range keys {
		if d.str(k) != "" {
			delete(d.fields, k)
		}
	}
}

func (d *document) has(k string) bool {
	_, ok := d.fields[k]
	return ok
}

func (d *document) value(k string) any {
	raw, ok := d.fields[k]
	if !ok {
		return nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil && d.err == nil {
		d.err = fmt.Errorf("%w: %s: %w", repository.ErrInvalidRepository, k, err)
	}

	return v
}

func (d *document) str(k string) string {
	s, _ := d.value(k).(string)
	return s
}

func (d *document) boolean(k string, def bool) (bool, error) {
	switch v := d.value(k).(type) {
	case bool:
		return v, nil
	case string:
		if v == "" {
			return def, nil
		}

		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("%w: %s: invalid boolean %q", repository.ErrInvalidRepository, k, v)
		}

		return b, nil
	}

	return def, nil
}

func (d *document) integer(k string, def int64) (int64, error) {
	switch v := d.value(k).(type) {
	case float64:
		return int64(v), nil
	case string:
		if v == "" {
			return def, nil
		}

		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %s: invalid integer %q", repository.ErrInvalidRepository, k, v)
		}

		return n, nil
	}

	return def, nil
}

func (d *document) list(k string) ([]string, error) {
	var out []string

	switch v := d.value(k).(type) {
	case string:
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	case nil:
	default:
		return nil, fmt.Errorf("%w: %s must be a list", repository.ErrInvalidRepository, k)
	}

	return out, nil
}

var renamed = map[string]repository.Format{
	"gems":           repository.FormatRubyGems,
	"huggingfaceml":  repository.FormatHuggingFace,
	"go":             repository.FormatGoModules,
	"releasebundles": repository.FormatRelease,
	"buildinfo":      repository.FormatBuild,
}

var aliases = map[string]repository.Format{
	"yum": repository.FormatRPM,
}

// FormatOf returns Relic's format for a package type, which is
// case-insensitive.
func FormatOf(packageType string) (repository.Format, bool) {
	t := strings.ToLower(strings.TrimSpace(packageType))
	if f, ok := renamed[t]; ok {
		return f, true
	}

	if f, ok := aliases[t]; ok {
		return f, true
	}

	f := repository.Format(t)
	if _, renamed := packageTypes[f]; renamed || !f.Known() {
		return "", false
	}

	return f, true
}

var packageTypes = func() map[repository.Format]string {
	m := make(map[repository.Format]string, len(renamed))
	for t, f := range renamed {
		m[f] = t
	}

	return m
}()

// WirePackageType returns the package type r was created with: an alias,
// such as yum for an RPM repository, or its format's package type.
func WirePackageType(r repository.Repository) string {
	if raw := r.Config.Extensions[Extension]; len(raw) > 0 {
		var ext struct {
			PackageType string `json:"packageType"`
		}

		if json.Unmarshal(raw, &ext) == nil {
			if f, ok := aliases[strings.ToLower(ext.PackageType)]; ok && f == r.Format {
				return ext.PackageType
			}
		}
	}

	return PackageType(r.Format)
}

// PackageType returns the package type of a format.
func PackageType(f repository.Format) string {
	if t, ok := packageTypes[f]; ok {
		return t
	}

	return string(f)
}

package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"relic/internal/artifacts/repository"
)

// PackageInfo is what the UI's npm tab shows from a tarball's package.json.
type PackageInfo struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Description      string            `json:"description,omitempty"`
	License          string            `json:"license,omitempty"`
	Homepage         string            `json:"homepage,omitempty"`
	Repository       string            `json:"repository,omitempty"`
	Keywords         []string          `json:"keywords"`
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
}

// PackageInfo reads package.json from the tarball item.
func (s *Service) PackageInfo(ctx context.Context, item repository.Item) (PackageInfo, error) {
	rc, err := s.repos.Open(ctx, item, nil)
	if err != nil {
		return PackageInfo{}, err
	}
	defer func() {
		_ = rc.Close()
	}()

	raw, err := readManifest(rc)
	if err != nil {
		return PackageInfo{}, err
	}

	var doc struct {
		Name, Version, Description, Homepage string
		License, Repository, Keywords        json.RawMessage
		Dependencies                         map[string]string
		DevDependencies                      map[string]string `json:"devDependencies"`
		PeerDependencies                     map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return PackageInfo{}, fmt.Errorf("%w: package.json: %w", ErrInvalid, err)
	}

	info := PackageInfo{
		Name:             doc.Name,
		Version:          doc.Version,
		Description:      doc.Description,
		Homepage:         doc.Homepage,
		License:          stringOrField(doc.License, "type"),
		Repository:       stringOrField(doc.Repository, "url"),
		Keywords:         parseKeywords(doc.Keywords),
		Dependencies:     nonNilMap(doc.Dependencies),
		DevDependencies:  nonNilMap(doc.DevDependencies),
		PeerDependencies: nonNilMap(doc.PeerDependencies),
	}

	return info, nil
}

func stringOrField(raw json.RawMessage, key string) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		if v, ok := obj[key].(string); ok {
			return v
		}
	}

	return ""
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}

	return m
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

func parseKeywords(raw json.RawMessage) []string {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return nonNilSlice(list)
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.FieldsFunc(text, func(r rune) bool {
			return r == ',' || unicode.IsSpace(r)
		})
	}

	return []string{}
}

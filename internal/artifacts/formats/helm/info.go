package helm

import (
	"context"

	"relic/internal/artifacts/repository"
)

// ChartInfo is what the UI's Helm tab shows from Chart.yaml.
type ChartInfo struct {
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	AppVersion   string              `json:"appVersion,omitempty"`
	Description  string              `json:"description,omitempty"`
	Type         string              `json:"type,omitempty"`
	APIVersion   string              `json:"apiVersion,omitempty"`
	KubeVersion  string              `json:"kubeVersion,omitempty"`
	Home         string              `json:"home,omitempty"`
	Icon         string              `json:"icon,omitempty"`
	Deprecated   bool                `json:"deprecated,omitempty"`
	Keywords     []string            `json:"keywords"`
	Sources      []string            `json:"sources"`
	Maintainers  []map[string]string `json:"maintainers"`
	Dependencies []map[string]string `json:"dependencies"`
}

// ChartInfo reads Chart.yaml from a chart archive item.
func (s *Service) ChartInfo(ctx context.Context, item repository.Item) (ChartInfo, error) {
	meta, err := s.chartMeta(ctx, item)
	if err != nil {
		return ChartInfo{}, err
	}

	info := ChartInfo{
		Name:         str(meta["name"]),
		Version:      str(meta["version"]),
		AppVersion:   str(meta["appVersion"]),
		Description:  str(meta["description"]),
		Type:         str(meta["type"]),
		APIVersion:   str(meta["apiVersion"]),
		KubeVersion:  str(meta["kubeVersion"]),
		Home:         str(meta["home"]),
		Icon:         str(meta["icon"]),
		Keywords:     stringList(meta["keywords"]),
		Sources:      stringList(meta["sources"]),
		Maintainers:  records(meta["maintainers"], "name", "email", "url"),
		Dependencies: records(meta["dependencies"], "name", "version", "repository", "condition", "alias"),
	}
	info.Deprecated, _ = meta["deprecated"].(bool)

	return info, nil
}

func stringList(v any) []string {
	out := []string{}

	list, _ := v.([]any)
	for _, e := range list {
		if s := str(e); s != "" {
			out = append(out, s)
		}
	}

	return out
}

func records(v any, keys ...string) []map[string]string {
	out := []map[string]string{}

	list, _ := v.([]any)
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}

		rec := map[string]string{}

		for _, k := range keys {
			if s := str(m[k]); s != "" {
				rec[k] = s
			}
		}

		out = append(out, rec)
	}

	return out
}

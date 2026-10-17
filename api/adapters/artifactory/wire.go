package artifactory

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"relic/api/packages"
	"relic/internal/artifacts/repository"
)

const timeFormat = "2006-01-02T15:04:05.000Z07:00"

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(timeFormat)
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}

	return scheme + "://" + r.Host + "/artifactory"
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}

	return strings.Join(parts, "/")
}

type checksumsJSON struct {
	SHA1   string `json:"sha1,omitempty"`
	MD5    string `json:"md5,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type fileInfo struct {
	URI               string        `json:"uri"`
	DownloadURI       string        `json:"downloadUri"`
	Repo              string        `json:"repo"`
	Path              string        `json:"path"`
	Created           string        `json:"created"`
	CreatedBy         string        `json:"createdBy"`
	LastModified      string        `json:"lastModified"`
	ModifiedBy        string        `json:"modifiedBy"`
	LastUpdated       string        `json:"lastUpdated"`
	Size              string        `json:"size"`
	MimeType          string        `json:"mimeType"`
	Checksums         checksumsJSON `json:"checksums"`
	OriginalChecksums checksumsJSON `json:"originalChecksums"`
}

type folderInfo struct {
	URI          string        `json:"uri"`
	Repo         string        `json:"repo"`
	Path         string        `json:"path"`
	Created      string        `json:"created"`
	CreatedBy    string        `json:"createdBy"`
	LastModified string        `json:"lastModified"`
	ModifiedBy   string        `json:"modifiedBy"`
	LastUpdated  string        `json:"lastUpdated"`
	Children     []folderChild `json:"children"`
}

type folderChild struct {
	URI    string `json:"uri"`
	Folder bool   `json:"folder"`
}

func storagePath(item repository.Item) string {
	if item.Name == repository.RootPath {
		return "/"
	}

	return "/" + item.Path()
}

func newFileInfo(r *http.Request, item repository.Item) fileInfo {
	base := baseURL(r)
	sums := checksumsJSON{
		SHA1:   item.Checksums.SHA1,
		MD5:    item.Checksums.MD5,
		SHA256: item.Checksums.SHA256,
	}

	return fileInfo{
		URI:               base + "/api/storage/" + escapePath(item.Repository+"/"+item.Path()),
		DownloadURI:       base + "/" + escapePath(item.Repository+"/"+item.Path()),
		Repo:              item.Repository,
		Path:              storagePath(item),
		Created:           formatTime(item.Created),
		CreatedBy:         item.CreatedBy,
		LastModified:      formatTime(item.Modified),
		ModifiedBy:        item.ModifiedBy,
		LastUpdated:       formatTime(item.Updated),
		Size:              fmt.Sprint(item.Size),
		MimeType:          packages.MimeType(item.Name),
		Checksums:         sums,
		OriginalChecksums: sums,
	}
}

func newFolderInfo(r *http.Request, item repository.Item, children []repository.Item) folderInfo {
	info := folderInfo{
		URI: baseURL(r) + "/api/storage/" +
			escapePath(strings.TrimSuffix(item.Repository+"/"+strings.TrimPrefix(storagePath(item), "/"), "/")),
		Repo:         item.Repository,
		Path:         storagePath(item),
		Created:      formatTime(item.Created),
		CreatedBy:    item.CreatedBy,
		LastModified: formatTime(item.Modified),
		ModifiedBy:   item.ModifiedBy,
		LastUpdated:  formatTime(item.Updated),
		Children:     make([]folderChild, 0, len(children)),
	}
	for _, c := range children {
		info.Children = append(info.Children, folderChild{
			URI:    "/" + c.Name,
			Folder: c.IsFolder(),
		})
	}

	return info
}

func rawQuery(raw string) map[string]string {
	out := map[string]string{}

	for pair := range strings.SplitSeq(raw, "&") {
		if pair == "" {
			continue
		}

		k, v, _ := strings.Cut(pair, "=")
		if key, err := url.QueryUnescape(k); err == nil {
			out[key] = v
		}
	}

	return out
}

func queryValue(q map[string]string, key string) string {
	v, err := url.QueryUnescape(q[key])
	if err != nil {
		return ""
	}

	return v
}

func splitValues(v string) []string {
	var (
		out []string
		cur strings.Builder
	)

	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '\\' && i+1 < len(v) && v[i+1] == ',':
			cur.WriteByte(',')

			i++
		case v[i] == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(v[i])
		}
	}

	return append(out, cur.String())
}

func parseProps(pairs []string) (repository.Properties, error) {
	props := repository.Properties{}

	for _, pair := range pairs {
		if pair == "" {
			continue
		}

		rawK, rawV, _ := strings.Cut(pair, "=")

		k, err := url.QueryUnescape(rawK)
		if err != nil || k == "" {
			return nil, fmt.Errorf("invalid property %q", pair)
		}

		if !repository.ValidPropertyKey(k) {
			return nil, fmt.Errorf("invalid property key %q: %s", k, repository.PropertyKeyRule)
		}

		v, err := url.QueryUnescape(rawV)
		if err != nil {
			return nil, fmt.Errorf("invalid property %q", pair)
		}

		for _, val := range splitValues(v) {
			if !containsValue(props[k], val) {
				props[k] = append(props[k], val)
			}
		}
	}

	return props, nil
}

func containsValue(vals []string, v string) bool {
	return slices.Contains(vals, v)
}

package sdk

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
)

var excluded = map[string]string{}

const openAPIPath = "../docs/routes.openapi.json"

func artifactoryRoutes() ([]string, error) {
	b, err := os.ReadFile(openAPIPath)
	if err != nil {
		return nil, err
	}

	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", openAPIPath, err)
	}

	var out []string

	for p, ops := range doc.Paths {
		if !strings.HasPrefix(p, "/artifactory/api/") {
			continue
		}

		for m := range ops {
			out = append(out, strings.ToUpper(m)+" "+p)
		}
	}

	sort.Strings(out)

	return out, nil
}

var lastWildcard = regexp.MustCompile(`\{(\w+)\}$`)

func routeMux(routes []string) (*http.ServeMux, map[string]string) {
	mux := http.NewServeMux()
	patternOf := map[string]string{}

	for _, r := range routes {
		pattern := lastWildcard.ReplaceAllString(r, "{$1...}")
		if !register(mux, pattern) {
			pattern = r
			if !register(mux, pattern) {
				continue
			}
		}

		patternOf[pattern] = r
	}

	return mux, patternOf
}

func register(mux *http.ServeMux, pattern string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})

	return true
}

func uncovered(routes []string, reqs []*http.Request) []string {
	mux, patternOf := routeMux(routes)
	hit := map[string]bool{}

	for _, r := range reqs {
		if _, p := mux.Handler(r); p != "" {
			hit[patternOf[p]] = true
		}
	}

	var out []string

	for _, r := range routes {
		if !hit[r] && excluded[r] == "" {
			out = append(out, r)
		}
	}

	return out
}

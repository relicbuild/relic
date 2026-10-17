package evidence

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"relic/api/adapters/artifactory/layout"
	"relic/api/adapters/artifactory/repoconfig"
	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/release"

	evd "relic/internal/delivery/evidence"
)

var (
	argPattern       = regexp.MustCompile(`(\w+)\s*:\s*"((?:[^"\\]|\\.)*)"`)
	predicatePattern = regexp.MustCompile(`[\s{]predicate[\s}]`)
)

type graphqlQuery struct {
	kind             string
	args             map[string]string
	includePredicate bool
}

var errUnsupportedQuery = errors.New(
	"unsupported query: Relic answers evidence.searchEvidence(where:{hasSubjectWith:{repositoryKey,path,name}}) and releaseBundleVersion.getVersion",
)

func indexOutsideStrings(s string, c byte) int {
	quoted := false

	for i := 0; i < len(s); i++ {
		switch {
		case quoted && s[i] == '\\':
			i++
		case s[i] == '"':
			quoted = !quoted
		case !quoted && s[i] == c:
			return i
		}
	}

	return -1
}

func parseQuery(q string) (graphqlQuery, error) {
	out := graphqlQuery{
		args:             map[string]string{},
		includePredicate: predicatePattern.MatchString(q),
	}

	var from string

	switch {
	case strings.Contains(q, "releaseBundleVersion") && strings.Contains(q, "getVersion("):
		out.kind = "releaseBundleVersion"
		from = q[strings.Index(q, "getVersion("):]

		end := indexOutsideStrings(from, ')')
		if end < 0 {
			return out, errUnsupportedQuery
		}

		from = from[:end+1]
	case strings.Contains(q, "searchEvidence") && strings.Contains(q, "hasSubjectWith"):
		out.kind = "searchEvidence"

		from = q[strings.Index(q, "hasSubjectWith"):]
		if end := indexOutsideStrings(from, '}'); end >= 0 {
			from = from[:end]
		}
	default:
		return out, errUnsupportedQuery
	}

	for _, m := range argPattern.FindAllStringSubmatch(from, -1) {
		var v string
		if err := json.Unmarshal([]byte(`"`+m[2]+`"`), &v); err != nil {
			return out, errors.New("invalid string argument")
		}

		out.args[m[1]] = v
	}

	if _, ok := out.args["entityType"]; ok {
		return out, errors.New("entity evidence is not supported")
	}

	return out, nil
}

func (h *Handler) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		graphqlError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	q, err := parseQuery(req.Query)
	if err != nil {
		graphqlError(w, http.StatusBadRequest, err.Error())
		return
	}

	var data any

	switch q.kind {
	case "searchEvidence":
		data, err = h.searchEvidence(r, q)
	default:
		data, err = h.bundleVersion(r, q)
	}

	if err != nil {
		h.log.ErrorContext(r.Context(), "graphql query failed", "error", err)
		graphqlError(w, http.StatusInternalServerError, "internal server error")

		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]any{
		"data": data,
	})
}

func graphqlError(w http.ResponseWriter, status int, msg string) {
	wire.WriteJSON(w, status, map[string]any{
		"errors": []map[string]string{
			{
				"message": msg,
			},
		},
	})
}

func (h *Handler) connection(r *http.Request, repoKey, path, name string, withPredicate bool) (map[string]any, error) {
	ok, err := h.can(r, repoKey, repository.JoinPath(path, name), auth.ActionRead)
	if errors.Is(err, repository.ErrRepositoryNotFound) || err == nil && !ok {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	list, err := h.evidence.ForSubject(r.Context(), repoKey, path, name)
	if err != nil {
		return nil, err
	}

	edges := make([]map[string]any, 0, len(list))
	for _, e := range list {
		edges = append(
			edges,
			map[string]any{
				"cursor": base64.StdEncoding.EncodeToString([]byte("evidence:" + e.ID.String())),
				"node":   node(e, withPredicate),
			},
		)
	}

	return map[string]any{
		"totalCount": len(edges),
		"edges":      edges,
	}, nil
}

func node(e evd.Evidence, withPredicate bool) map[string]any {
	atts := make([]map[string]string, 0, len(e.Attachments))
	for _, a := range e.Attachments {
		atts = append(
			atts,
			map[string]string{
				"name":         a.Name,
				"sha256":       a.SHA256,
				"type":         a.Type,
				"downloadPath": attachmentPath(a),
			},
		)
	}

	n := map[string]any{
		"predicateSlug": e.PredicateSlug,
		"predicateType": e.PredicateType,
		"downloadPath":  downloadPath(e),
		"verified":      e.Verified,
		"signingKey": map[string]string{
			"alias":     e.KeyAlias,
			"publicKey": e.PublicKey,
		},
		"createdBy": e.CreatedBy,
		"createdAt": formatTime(e.Created),
		"subject": map[string]string{
			"sha256": e.SubjectSHA256,
		},
		"attachments": atts,
	}
	if withPredicate && len(e.Predicate) > 0 {
		n["predicate"] = e.Predicate
	}

	return n
}

func (h *Handler) searchEvidence(r *http.Request, q graphqlQuery) (any, error) {
	conn, err := h.connection(r, q.args["repositoryKey"], q.args["path"], q.args["name"], q.includePredicate)
	if err != nil {
		return nil, err
	}

	var search any
	if conn != nil {
		search = conn
	}

	return map[string]any{
		"evidence": map[string]any{
			"searchEvidence": search,
		},
	}, nil
}

func (h *Handler) bundleVersion(r *http.Request, q graphqlQuery) (any, error) {
	repoKey, name, version := q.args["repositoryKey"], q.args["name"], q.args["version"]

	project, _ := strings.CutSuffix(repoKey, "-release-bundles-v2")
	if repoKey == layout.ReleaseBundles.Repository("") {
		project = ""
	}

	b, err := h.bundles.Get(r.Context(), project, name, version)
	if errors.Is(err, release.ErrNotFound) {
		return map[string]any{
			"releaseBundleVersion": map[string]any{
				"getVersion": nil,
			},
		}, nil
	}

	if err != nil {
		return nil, err
	}

	manifest, err := h.connection(r, b.Repository, b.Folder(), layout.ReleaseBundles.ManifestName, q.includePredicate)
	if err != nil {
		return nil, err
	}

	if manifest == nil {
		return map[string]any{
			"releaseBundleVersion": map[string]any{
				"getVersion": nil,
			},
		}, nil
	}

	arts := []map[string]any{}
	types := map[string]string{}

	for _, a := range b.Artifacts {
		dir, file := repository.SplitPath(a.Path)
		if dir == repository.RootPath {
			dir = ""
		}

		conn, err := h.connection(r, a.Repository, dir, file, q.includePredicate)
		if err != nil {
			return nil, err
		}

		if conn == nil || conn["totalCount"] == 0 {
			continue
		}

		pt, ok := types[a.Repository]
		if !ok {
			rp, err := h.repos.GetRepository(r.Context(), a.Repository)

			switch {
			case err == nil:
				pt = repoconfig.PackageType(rp.Format)
			case !errors.Is(err, repository.ErrRepositoryNotFound):
				return nil, err
			}

			types[a.Repository] = pt
		}

		arts = append(arts, map[string]any{
			"node": map[string]any{
				"sourceRepositoryPath": a.Repository + "/" + a.Path,
				"packageType":          pt,
				"evidenceConnection":   conn,
			},
		})
	}

	return map[string]any{
		"releaseBundleVersion": map[string]any{
			"getVersion": map[string]any{
				"evidenceConnection": manifest,
				"artifactsConnection": map[string]any{
					"totalCount": len(arts),
					"edges":      arts,
				},
				"fromBuilds": []any{},
			},
		},
	}, nil
}

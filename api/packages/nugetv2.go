package packages

import (
	"encoding/xml"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/nuget"
	"relic/internal/artifacts/repository"
)

var (
	v2PackageRe = regexp.MustCompile(`^Packages\(Id='([^']+)',Version='([^']+)'\)$`)
	v2QuotedRe  = regexp.MustCompile(`^'(.*)'$`)
)

type v2Feed struct {
	XMLName xml.Name  `xml:"feed"`
	Base    string    `xml:"xml:base,attr"`
	Xmlns   string    `xml:"xmlns,attr"`
	D       string    `xml:"xmlns:d,attr"`
	M       string    `xml:"xmlns:m,attr"`
	ID      string    `xml:"id"`
	Title   v2Text    `xml:"title"`
	Updated string    `xml:"updated"`
	Entries []v2Entry `xml:"entry"`
}

type v2Text struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type v2Entry struct {
	XMLName xml.Name `xml:"entry"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	D       string   `xml:"xmlns:d,attr,omitempty"`
	M       string   `xml:"xmlns:m,attr,omitempty"`
	ID      string   `xml:"id"`
	Title   v2Text   `xml:"title"`
	Updated string   `xml:"updated"`
	Author  struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Content struct {
		Type string `xml:"type,attr"`
		Src  string `xml:"src,attr"`
	} `xml:"content"`
	Properties v2Properties `xml:"m:properties"`
}

type v2Typed struct {
	Type  string `xml:"m:type,attr,omitempty"`
	Value string `xml:",chardata"`
}

type v2Properties struct {
	ID                      v2Typed `xml:"d:Id"`
	Version                 v2Typed `xml:"d:Version"`
	NormalizedVersion       v2Typed `xml:"d:NormalizedVersion"`
	Description             v2Typed `xml:"d:Description"`
	Authors                 v2Typed `xml:"d:Authors"`
	Dependencies            v2Typed `xml:"d:Dependencies"`
	IsPrerelease            v2Typed `xml:"d:IsPrerelease"`
	IsLatestVersion         v2Typed `xml:"d:IsLatestVersion"`
	IsAbsoluteLatestVersion v2Typed `xml:"d:IsAbsoluteLatestVersion"`
	Listed                  v2Typed `xml:"d:Listed"`
	PackageSize             v2Typed `xml:"d:PackageSize"`
	Published               v2Typed `xml:"d:Published"`
}

const (
	atomNS = "http://www.w3.org/2005/Atom"
	dNS    = "http://schemas.microsoft.com/ado/2007/08/dataservices"
	mNS    = "http://schemas.microsoft.com/ado/2007/08/dataservices/metadata"
)

func (h *Handler) nugetV2(w http.ResponseWriter, r *http.Request, m Mount, rp repository.Repository, rest string) bool {
	base := m.base(r) + "/" + rp.Name

	switch {
	case rest == "" || rest == "/":
		h.v2Service(w, base)
	case rest == "$metadata":
		writeV2XML(w, "application/xml", v2Metadata)
	case rest == "FindPackagesById()":
		id := unquote(r.URL.Query().Get("id"))
		if !nuget.ValidID(id) || !h.RequireRepo(w, r, RepoCheck{
			rp.Name,
			id,
			auth.ActionRead,
		}) {
			if !nuget.ValidID(id) {
				http.NotFound(w, r)
			}

			return true
		}

		entries, err := h.nuget.V2Entries(r.Context(), rp, id)
		if err != nil && !errors.Is(err, nuget.ErrNotFound) {
			h.nugetFail(w, r, err)
			return true
		}

		h.writeV2Feed(w, base, "FindPackagesById", entries, entries, false)
	case v2PackageRe.MatchString(rest):
		m := v2PackageRe.FindStringSubmatch(rest)
		if !h.RequireRepo(w, r, RepoCheck{
			rp.Name,
			m[1],
			auth.ActionRead,
		}) {
			return true
		}

		entries, err := h.nuget.V2Entries(r.Context(), rp, m[1])
		if h.nugetFail(w, r, err) {
			return true
		}

		for _, e := range entries {
			if strings.EqualFold(e.Version, m[2]) {
				h.writeV2Feed(w, base, "Packages", []nuget.V2Entry{
					e,
				}, entries, true)

				return true
			}
		}

		http.NotFound(w, r)
	case rest == "Search()" || rest == "Search()/$count" || rest == "Packages()" || rest == "Packages":
		if !h.RequireRepo(w, r, RepoCheck{
			rp.Name,
			"",
			auth.ActionRead,
		}) {
			return true
		}

		q := r.URL.Query()
		skip, _ := strconv.Atoi(q.Get("$skip"))

		take, _ := strconv.Atoi(q.Get("$top"))
		if take <= 0 || take > 100 {
			take = 30
		}

		results, total, err := h.nuget.Search(r.Context(), rp, unquote(q.Get("searchTerm")), max(skip, 0), take)
		if h.nugetFail(w, r, err) {
			return true
		}

		if strings.HasSuffix(rest, "$count") {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(strconv.Itoa(total)))

			return true
		}

		var entries, versions []nuget.V2Entry

		for _, res := range results {
			for _, v := range res.Versions {
				versions = append(
					versions,
					nuget.V2Entry{
						ID:         res.ID,
						Version:    v,
						Prerelease: strings.Contains(v, "-"),
					},
				)
			}

			if len(res.Versions) > 0 {
				entries = append(entries, versions[len(versions)-1])
			}
		}

		h.writeV2Feed(w, base, "Search", entries, versions, false)
	case strings.HasPrefix(rest, "Download/"):
		parts := strings.Split(strings.TrimPrefix(rest, "Download/"), "/")
		if len(parts) != 2 || !nuget.ValidID(parts[0]) || !nuget.ValidVersion(parts[1]) {
			http.NotFound(w, r)
			return true
		}

		if !h.RequireRepo(w, r, RepoCheck{
			rp.Name,
			parts[0],
			auth.ActionRead,
		}) {
			return true
		}

		h.nugetDownload(w, r, rp, parts[0], parts[1])
	default:
		return false
	}

	return true
}

func (h *Handler) v2Service(w http.ResponseWriter, base string) {
	doc := `<?xml version="1.0" encoding="utf-8"?>` +
		`<service xml:base="` + xmlEscape(base) + `/" xmlns="http://www.w3.org/2007/app" xmlns:atom="http://www.w3.org/2005/Atom">` +
		`<workspace><atom:title type="text">Default</atom:title><collection href="Packages"><atom:title type="text">Packages</atom:title></collection></workspace></service>`
	writeV2XML(w, "application/xml", doc)
}

// versions must hold every version of the entries' packages, in ascending order.
func (h *Handler) writeV2Feed(
	w http.ResponseWriter,
	base, title string,
	entries, versions []nuget.V2Entry,
	single bool,
) {
	now := time.Now().UTC().Format(time.RFC3339)
	latest, absoluteLatest := map[string]string{}, map[string]string{}

	for _, e := range versions {
		absoluteLatest[strings.ToLower(e.ID)] = e.Version
		if !e.Prerelease {
			latest[strings.ToLower(e.ID)] = e.Version
		}
	}

	out := make([]v2Entry, 0, len(entries))
	for _, e := range entries {
		ve := v2Entry{
			ID: base + "/Packages(Id='" + e.ID + "',Version='" + e.Version + "')",
			Title: v2Text{
				Type:  "text",
				Value: e.ID,
			},
			Updated: now,
		}
		ve.Author.Name = e.Authors
		ve.Content.Type, ve.Content.Src = "application/zip", base+"/Download/"+e.ID+"/"+e.Version

		published := e.Published
		if published.IsZero() {
			published = time.Unix(0, 0)
		}

		ve.Properties = v2Properties{
			ID: v2Typed{
				Value: e.ID,
			},
			Version: v2Typed{
				Value: e.Version,
			},
			NormalizedVersion: v2Typed{
				Value: e.Version,
			},
			Description: v2Typed{
				Value: e.Description,
			},
			Authors: v2Typed{
				Value: e.Authors,
			},
			Dependencies: v2Typed{
				Value: e.Dependencies,
			},
			IsPrerelease: v2Typed{
				Type:  "Edm.Boolean",
				Value: strconv.FormatBool(e.Prerelease),
			},
			IsLatestVersion: v2Typed{
				Type:  "Edm.Boolean",
				Value: strconv.FormatBool(latest[strings.ToLower(e.ID)] == e.Version),
			},
			IsAbsoluteLatestVersion: v2Typed{
				Type:  "Edm.Boolean",
				Value: strconv.FormatBool(absoluteLatest[strings.ToLower(e.ID)] == e.Version),
			},
			Listed: v2Typed{
				Type:  "Edm.Boolean",
				Value: "true",
			},
			PackageSize: v2Typed{
				Type:  "Edm.Int64",
				Value: strconv.FormatInt(e.Size, 10),
			},
			Published: v2Typed{
				Type:  "Edm.DateTime",
				Value: published.UTC().Format("2006-01-02T15:04:05.000"),
			},
		}
		out = append(out, ve)
	}

	var (
		body []byte
		err  error
	)

	if single && len(out) == 1 {
		out[0].Xmlns, out[0].D, out[0].M = atomNS, dNS, mNS
		body, err = xml.Marshal(out[0])
	} else {
		body, err = xml.Marshal(
			v2Feed{
				Base:  base + "/",
				Xmlns: atomNS,
				D:     dNS,
				M:     mNS,
				ID:    base + "/" + title,
				Title: v2Text{
					Type:  "text",
					Value: title,
				},
				Updated: now,
				Entries: out,
			},
		)
	}

	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	writeV2XML(w, "application/atom+xml;type=feed;charset=utf-8", xml.Header+string(body))
}

func writeV2XML(w http.ResponseWriter, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("DataServiceVersion", "2.0")
	_, _ = w.Write(
		[]byte(body),
	) //nolint:gosec // G705: XML built with encoding/xml escaping, served with an XML content type and nosniff
}

func unquote(s string) string {
	if m := v2QuotedRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return strings.ReplaceAll(m[1], "''", "'")
	}

	return strings.TrimSpace(s)
}

func xmlEscape(s string) string {
	var b strings.Builder

	_ = xml.EscapeText(&b, []byte(s))

	return b.String()
}

const v2Metadata = `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="1.0" xmlns:edmx="http://schemas.microsoft.com/ado/2007/06/edmx">
<edmx:DataServices xmlns:m="http://schemas.microsoft.com/ado/2007/08/dataservices/metadata" m:DataServiceVersion="2.0">
<Schema Namespace="NuGetGallery" xmlns="http://schemas.microsoft.com/ado/2006/04/edm">
<EntityType Name="V2FeedPackage" m:HasStream="true"><Key><PropertyRef Name="Id"/><PropertyRef Name="Version"/></Key>
<Property Name="Id" Type="Edm.String" Nullable="false"/><Property Name="Version" Type="Edm.String" Nullable="false"/>
<Property Name="NormalizedVersion" Type="Edm.String"/><Property Name="Description" Type="Edm.String"/><Property Name="Authors" Type="Edm.String"/>
<Property Name="Dependencies" Type="Edm.String"/><Property Name="IsPrerelease" Type="Edm.Boolean" Nullable="false"/>
<Property Name="IsLatestVersion" Type="Edm.Boolean" Nullable="false"/><Property Name="IsAbsoluteLatestVersion" Type="Edm.Boolean" Nullable="false"/>
<Property Name="Listed" Type="Edm.Boolean" Nullable="false"/><Property Name="PackageSize" Type="Edm.Int64" Nullable="false"/>
<Property Name="Published" Type="Edm.DateTime" Nullable="false"/></EntityType>
<EntityContainer Name="FeedContext_x0060_1" m:IsDefaultEntityContainer="true"><EntitySet Name="Packages" EntityType="NuGetGallery.V2FeedPackage"/>
<FunctionImport Name="Search" EntitySet="Packages" ReturnType="Collection(NuGetGallery.V2FeedPackage)" m:HttpMethod="GET">
<Parameter Name="searchTerm" Type="Edm.String" FixedLength="false" Unicode="false"/><Parameter Name="targetFramework" Type="Edm.String" FixedLength="false" Unicode="false"/>
<Parameter Name="includePrerelease" Type="Edm.Boolean" Nullable="false"/></FunctionImport>
<FunctionImport Name="FindPackagesById" EntitySet="Packages" ReturnType="Collection(NuGetGallery.V2FeedPackage)" m:HttpMethod="GET">
<Parameter Name="id" Type="Edm.String" FixedLength="false" Unicode="false"/></FunctionImport>
</EntityContainer></Schema></edmx:DataServices></edmx:Edmx>`

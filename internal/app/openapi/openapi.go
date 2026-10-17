// Package openapi builds Relic's OpenAPI 3.1 description from the route
// table and per-route documentation.
package openapi

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/platform/httpserver"
)

// Doc documents one route.
type Doc struct {
	Summary     string
	Description string
	Request     any
	Response    any
	Query       []Param
}

// Param is a query parameter.
type Param struct {
	Name, Description string
	Required          bool
}

// Info describes the API.
type Info struct {
	Title, Version, Description string
	// Tag groups the routes outside the Relic API (an API mount's);
	// nil or "" puts them under "System".
	Tag func(pattern string) string
}

// Build returns the OpenAPI document for routes, using docs keyed by route
// pattern ("GET /api/v1/files/{repo}/{path...}").
func Build(routes []httpserver.Route, docs map[string]Doc, info Info) map[string]any {
	g := &generator{
		schemas: map[string]any{},
		names:   map[reflect.Type]string{},
	}
	paths := map[string]map[string]any{}

	for _, r := range routes {
		method, pattern, ok := strings.Cut(r.Pattern, " ")
		if !ok {
			continue
		}

		p, params := pathOf(pattern)

		op := map[string]any{
			"operationId": operationID(method, pattern),
			"tags": []string{
				tagOf(pattern, info.Tag),
			},
			"x-relic-access": r.Access.String(),
			"responses":      map[string]any{},
		}
		if r.Access == auth.Public {
			op["security"] = []any{}
		}

		d := docs[r.Pattern]

		op["summary"] = d.Summary
		if d.Summary == "" {
			op["summary"] = method + " " + pattern
		}

		if d.Description != "" {
			op["description"] = d.Description
		}

		for _, q := range d.Query {
			params = append(
				params,
				map[string]any{
					"name":        q.Name,
					"in":          "query",
					"required":    q.Required,
					"description": q.Description,
					"schema": map[string]any{
						"type": "string",
					},
				},
			)
		}

		if len(params) > 0 {
			op["parameters"] = params
		}

		if d.Request != nil {
			op["requestBody"] = map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": g.schema(reflect.TypeOf(d.Request)),
					},
				},
			}
		}

		ok200 := map[string]any{
			"description": "OK",
		}
		if d.Response != nil {
			ok200["content"] = map[string]any{
				"application/json": map[string]any{
					"schema": g.schema(reflect.TypeOf(d.Response)),
				},
			}
		}

		op["responses"] = map[string]any{
			"200": ok200,
		}

		if paths[p] == nil {
			paths[p] = map[string]any{}
		}

		paths[p][strings.ToLower(method)] = op
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       info.Title,
			"version":     info.Version,
			"description": info.Description,
		},
		"paths": paths,
		"components": map[string]any{
			"schemas": g.schemas,
			"securitySchemes": map[string]any{
				"basic": map[string]any{
					"type":   "http",
					"scheme": "basic",
				},
				"bearer": map[string]any{
					"type":        "http",
					"scheme":      "bearer",
					"description": "An access token (command-line clients, CI).",
				},
				"session": map[string]any{
					"type":        "apiKey",
					"in":          "cookie",
					"name":        auth.SessionCookie,
					"description": "The web UI's session; honoured under /api/ only.",
				},
			},
		},
		"security": []any{
			map[string]any{
				"basic": []any{},
			},
			map[string]any{
				"bearer": []any{},
			},
			map[string]any{
				"session": []any{},
			},
		},
	}
}

// Marshal renders a document as indented JSON with a trailing newline.
func Marshal(doc map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(b, '\n'), nil
}

var wildcard = regexp.MustCompile(`\{([A-Za-z_]+)(\.\.\.)?\}`)

func pathOf(pattern string) (string, []any) {
	var params []any

	p := wildcard.ReplaceAllStringFunc(pattern, func(m string) string {
		sub := wildcard.FindStringSubmatch(m)

		desc := ""
		if sub[2] != "" {
			desc = "The rest of the path; may contain slashes."
		}

		params = append(
			params,
			map[string]any{
				"name":        sub[1],
				"in":          "path",
				"required":    true,
				"description": desc,
				"schema": map[string]any{
					"type": "string",
				},
			},
		)

		return "{" + sub[1] + "}"
	})

	return strings.TrimSuffix(p, "{$}"), params
}

func operationID(method, pattern string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))

	for _, part := range strings.FieldsFunc(pattern, func(r rune) bool {
		return r == '/' || r == '{' || r == '}' || r == '.' || r == '-' || r == '$'
	}) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}

	return b.String()
}

func tagOf(pattern string, other func(string) string) string {
	segs := strings.Split(strings.Trim(pattern, "/"), "/")
	if segs[0] == "api" {
		resource := ""
		if len(segs) > 2 && !strings.HasPrefix(segs[2], "{") {
			resource = segs[2]
		}

		return "Relic API: " + resource
	}

	if other != nil {
		if t := other(pattern); t != "" {
			return t
		}
	}

	return "System"
}

type generator struct {
	schemas map[string]any
	names   map[reflect.Type]string
}

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
)

func (g *generator) schema(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t {
	case timeType:
		return map[string]any{
			"type":   "string",
			"format": "date-time",
		}
	case rawType:
		return map[string]any{}
	}

	switch t.Kind() { //nolint:exhaustive // interfaces, funcs and the like get the empty (any) schema below
	case reflect.String:
		return map[string]any{
			"type": "string",
		}
	case reflect.Bool:
		return map[string]any{
			"type": "boolean",
		}
	case reflect.Int,
		reflect.Int8,
		reflect.Int16,
		reflect.Int32,
		reflect.Int64,
		reflect.Uint,
		reflect.Uint8,
		reflect.Uint16,
		reflect.Uint32,
		reflect.Uint64:
		return map[string]any{
			"type": "integer",
		}
	case reflect.Float32, reflect.Float64:
		return map[string]any{
			"type": "number",
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{
				"type":            "string",
				"contentEncoding": "base64",
			}
		}

		return map[string]any{
			"type":  "array",
			"items": g.schema(t.Elem()),
		}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": g.schema(t.Elem()),
		}
	case reflect.Struct:
		return g.ref(t)
	}

	return map[string]any{}
}

func (g *generator) ref(t reflect.Type) map[string]any {
	if t.Name() == "" {
		return g.object(t)
	}

	name, ok := g.names[t]
	if !ok {
		name = t.Name()
		for n := 2; slices.ContainsFunc(sortedValues(g.names), func(v string) bool {
			return v == name
		}); n++ {
			name = t.Name() + strings.Repeat("_", n-1)
		}

		g.names[t] = name
		g.schemas[name] = map[string]any{}
		g.schemas[name] = g.object(t)
	}

	return map[string]any{
		"$ref": "#/components/schemas/" + name,
	}
}

func (g *generator) object(t reflect.Type) map[string]any {
	props := map[string]any{}

	var (
		required []string
		walk     func(reflect.Type)
	)

	walk = func(t reflect.Type) {
		for f := range t.Fields() {
			if !f.IsExported() {
				continue
			}

			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}

			name, opts, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" {
				ft := f.Type
				for ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}

				if ft.Kind() == reflect.Struct {
					walk(ft)
					continue
				}
			}

			if name == "" {
				name = f.Name
			}

			props[name] = g.schema(f.Type)
			if !strings.Contains(opts, "omitempty") && !strings.Contains(opts, "omitzero") &&
				f.Type.Kind() != reflect.Pointer {
				required = append(required, name)
			}
		}
	}
	walk(t)

	obj := map[string]any{
		"type":       "object",
		"properties": props,
	}

	if len(required) > 0 {
		slices.Sort(required)
		obj["required"] = required
	}

	return obj
}

func sortedValues(m map[reflect.Type]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}

	slices.Sort(out)

	return out
}

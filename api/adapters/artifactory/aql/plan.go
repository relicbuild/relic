package aql

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"relic/internal/artifacts/itemquery"
)

// ErrUnsupported is returned for valid AQL that Relic does not answer.
var ErrUnsupported = errors.New("aql: unsupported")

var itemFields = map[string]itemquery.Field{
	"repo":          itemquery.FieldRepository,
	"path":          itemquery.FieldPath,
	"name":          itemquery.FieldName,
	"type":          itemquery.FieldType,
	"size":          itemquery.FieldSize,
	"created":       itemquery.FieldCreated,
	"created_by":    itemquery.FieldCreatedBy,
	"modified":      itemquery.FieldModified,
	"modified_by":   itemquery.FieldModifiedBy,
	"updated":       itemquery.FieldUpdated,
	"actual_sha1":   itemquery.FieldSHA1,
	"original_sha1": itemquery.FieldSHA1,
	"actual_md5":    itemquery.FieldMD5,
	"original_md5":  itemquery.FieldMD5,
	"sha256":        itemquery.FieldSHA256,
	"depth":         itemquery.FieldDepth,
}

var ops = map[string]itemquery.Op{
	OpEq:     itemquery.OpEqual,
	OpNe:     itemquery.OpNotEqual,
	OpGt:     itemquery.OpGreater,
	OpGte:    itemquery.OpGreaterOrEqual,
	OpLt:     itemquery.OpLess,
	OpLte:    itemquery.OpLessOrEqual,
	OpMatch:  itemquery.OpMatch,
	OpNmatch: itemquery.OpNotMatch,
}

var buildRelations = map[string]itemquery.BuildRelation{
	"artifact.module.build.":   itemquery.BuildArtifact,
	"dependency.module.build.": itemquery.BuildDependency,
}

var buildFields = map[string]itemquery.BuildField{
	"name":   itemquery.BuildName,
	"number": itemquery.BuildNumber,
}

// DefaultItemFields are returned when a query has no include().
var DefaultItemFields = []string{
	"repo",
	"path",
	"name",
	"type",
	"size",
	"created",
	"created_by",
	"modified",
	"modified_by",
	"updated",
}

// Plan is an AQL items query translated to Relic's item query, with what
// the AQL response shows.
type Plan struct {
	Query itemquery.Query
	// Fields are the AQL item fields to output, in order.
	Fields []string
	// Properties reports whether results carry a "properties" array.
	Properties bool
	Offset     int64
	Limit      *int64
	// ShowRepo, when set, maps a stored repository name to the name results
	// report (a remote's items belong to "<remote>-cache").
	ShowRepo func(name string) string

	compiled *itemquery.Plan
}

// PlanItems translates an items-domain query and compiles it.
func PlanItems(q *Query) (*Plan, error) {
	if q.Domain != "items" {
		return nil, fmt.Errorf("%w: domain %q", ErrUnsupported, q.Domain)
	}

	plan := &Plan{
		Limit: q.Limit,
	}
	if err := plan.setFields(q.Include); err != nil {
		return nil, err
	}

	iq, err := ItemQuery(q)
	if err != nil {
		return nil, err
	}

	iq.Properties = plan.Properties
	if q.Offset != nil {
		plan.Offset = *q.Offset
	}

	if plan.compiled, err = itemquery.Compile(iq); err != nil {
		return nil, relicError(err)
	}

	plan.Query = iq

	return plan, nil
}

// ItemQuery translates an items-domain query's criteria, sort, offset and
// limit.
func ItemQuery(q *Query) (itemquery.Query, error) {
	var out itemquery.Query

	if q.Criteria != nil {
		n, err := translate(q.Criteria)
		if err != nil {
			return out, err
		}

		out.Criteria = n
	}

	if !mentionsType(q.Criteria) {
		files := itemquery.Compare{
			Field: itemquery.FieldType,
			Op:    itemquery.OpEqual,
			Value: "file",
		}
		if out.Criteria == nil {
			out.Criteria = files
		} else {
			out.Criteria = itemquery.All{
				out.Criteria,
				files,
			}
		}
	}

	if q.Sort != nil {
		out.Sort = &itemquery.Sort{
			Desc: q.Sort.Desc,
		}
		for _, f := range q.Sort.Fields {
			field, ok := itemFields[f]
			if !ok {
				return out, fmt.Errorf("%w: cannot sort by %q", ErrSyntax, f)
			}

			out.Sort.Fields = append(out.Sort.Fields, field)
		}
	}

	out.Offset, out.Limit = q.Offset, q.Limit

	return out, nil
}

func relicError(err error) error {
	if errors.Is(err, itemquery.ErrUnsupported) {
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	}

	return fmt.Errorf("%w: %w", ErrSyntax, err)
}

func (plan *Plan) setFields(include []string) error {
	if len(include) == 0 {
		plan.Fields = DefaultItemFields
		return nil
	}

	for _, f := range include {
		switch {
		case f == "*":
			for _, name := range slices.Concat(DefaultItemFields, []string{
				"actual_sha1",
				"actual_md5",
				"original_sha1",
				"original_md5",
				"sha256",
				"depth",
			}) {
				if !slices.Contains(plan.Fields, name) {
					plan.Fields = append(plan.Fields, name)
				}
			}
		case f == "property" || f == "property.*" || f == "property.key" || f == "property.value":
			plan.Properties = true
		case strings.HasPrefix(f, "@"):
			plan.Properties = true
		default:
			if _, ok := itemFields[f]; !ok {
				return fmt.Errorf("%w: cannot include %q", ErrUnsupported, f)
			}

			if !slices.Contains(plan.Fields, f) {
				plan.Fields = append(plan.Fields, f)
			}
		}
	}

	return nil
}

func mentionsType(n Node) bool {
	switch t := n.(type) {
	case Compare:
		return t.Field == "type"
	case And:
		return slices.ContainsFunc(t, mentionsType)
	case Or:
		return slices.ContainsFunc(t, mentionsType)
	}

	return false
}

func translate(n Node) (itemquery.Node, error) {
	switch t := n.(type) {
	case And:
		return translateAll(t, func(c []itemquery.Node) itemquery.Node {
			return itemquery.All(c)
		})
	case Or:
		return translateAll(t, func(c []itemquery.Node) itemquery.Node {
			return itemquery.Any(c)
		})
	case MatchSingleProperty:
		out := make(itemquery.SameProperty, 0, len(t))
		for _, c := range t {
			p, err := property(c)
			if err != nil {
				return nil, err
			}

			if p == nil {
				return nil, fmt.Errorf("%w: %q inside $msp", ErrUnsupported, c.Field)
			}

			out = append(out, p)
		}

		return out, nil
	case Compare:
		return compare(t)
	}

	return nil, fmt.Errorf("%w: node %T", ErrUnsupported, n)
}

func translateAll(nodes []Node, wrap func([]itemquery.Node) itemquery.Node) (itemquery.Node, error) {
	out := make([]itemquery.Node, 0, len(nodes))
	for _, c := range nodes {
		n, err := translate(c)
		if err != nil {
			return nil, err
		}

		out = append(out, n)
	}

	return wrap(out), nil
}

func op(o string) (itemquery.Op, error) {
	if r, ok := ops[o]; ok {
		return r, nil
	}

	return "", fmt.Errorf("%w: operator %q", ErrSyntax, o)
}

func property(c Compare) (itemquery.Node, error) {
	o, err := op(c.Op)
	if err != nil {
		return nil, err
	}

	switch {
	case strings.HasPrefix(c.Field, "@"):
		return itemquery.PropertyValue{
			Key:   strings.TrimPrefix(c.Field, "@"),
			Op:    o,
			Value: c.Value,
		}, nil
	case c.Field == "property.key":
		return itemquery.PropertyKey{
			Op:    o,
			Value: c.Value,
		}, nil
	case c.Field == "property.value":
		return itemquery.PropertyValue{
			Op:    o,
			Value: c.Value,
		}, nil
	}

	return nil, nil
}

func compare(c Compare) (itemquery.Node, error) {
	o, err := op(c.Op)
	if err != nil {
		return nil, err
	}

	for prefix, rel := range buildRelations {
		if rest, ok := strings.CutPrefix(c.Field, prefix); ok {
			f, ok := buildFields[rest]
			if !ok {
				return nil, fmt.Errorf("%w: field %q", ErrUnsupported, c.Field)
			}

			return itemquery.Build{
				Relation: rel,
				Field:    f,
				Op:       o,
				Value:    c.Value,
			}, nil
		}
	}

	if p, err := property(c); err != nil || p != nil {
		return p, err
	}

	if c.Field == "type" && c.Op == OpEq && c.Value == "any" {
		return itemquery.All{}, nil
	}

	field, ok := itemFields[c.Field]
	if !ok {
		return nil, fmt.Errorf("%w: field %q", ErrUnsupported, c.Field)
	}

	value := c.Value
	if s, ok := value.(string); ok && o != itemquery.OpMatch && o != itemquery.OpNotMatch &&
		(field == itemquery.FieldCreated || field == itemquery.FieldModified || field == itemquery.FieldUpdated) {
		if t, err := time.Parse(TimeFormat, s); err == nil {
			value = t
		} else if t, err := time.Parse("2006-01-02T15:04:05.000-0700", s); err == nil {
			value = t
		}
	}

	return itemquery.Compare{
		Field: field,
		Op:    o,
		Value: value,
	}, nil
}

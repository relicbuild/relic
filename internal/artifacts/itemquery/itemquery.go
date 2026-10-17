// Package itemquery finds items (files and folders) by their fields, their
// properties and the builds that produced or used them, and plans such a
// query to parameterized SQL.
package itemquery

import (
	"errors"
	"time"
)

// Errors.
var (
	// ErrInvalid is a query that cannot run as written (a value of the
	// wrong kind, an unknown field).
	ErrInvalid = errors.New("itemquery: invalid query")
	// ErrUnsupported is a query Relic does not answer (yet).
	ErrUnsupported = errors.New("itemquery: unsupported")
)

// Query selects items.
type Query struct {
	Criteria   Node
	Properties bool
	Sort       *Sort
	Offset     *int64
	Limit      *int64
}

// Sort orders results by Fields, all ascending or all descending.
type Sort struct {
	Desc   bool
	Fields []Field
}

// Field is an item field.
type Field string

// Item fields.
const (
	FieldRepository Field = "repository"
	FieldPath       Field = "path"
	FieldName       Field = "name"
	FieldType       Field = "type"
	FieldSize       Field = "size"
	FieldCreated    Field = "created"
	FieldCreatedBy  Field = "created_by"
	FieldModified   Field = "modified"
	FieldModifiedBy Field = "modified_by"
	FieldUpdated    Field = "updated"
	FieldSHA1       Field = "sha1"
	FieldMD5        Field = "md5"
	FieldSHA256     Field = "sha256"
	// FieldDepth counts path segments: 1 for items at the repository root.
	FieldDepth Field = "depth"
)

// Op is a comparison.
type Op string

// Comparisons.
const (
	OpEqual          Op = "eq"
	OpNotEqual       Op = "ne"
	OpGreater        Op = "gt"
	OpGreaterOrEqual Op = "gte"
	OpLess           Op = "lt"
	OpLessOrEqual    Op = "lte"
	OpMatch          Op = "match"
	OpNotMatch       Op = "nmatch"
)

// Value is a literal: a string, an int64, a float64, a bool, a time.Time
// or nil (matching a missing value with OpEqual and OpNotEqual).
type Value = any

// Node is a criterion: All, Any, Compare, PropertyValue, PropertyKey,
// SameProperty or Build.
type Node interface{ node() }

// All matches when every child matches (and when there are none).
type All []Node

// Any matches when some child matches.
type Any []Node

// Compare applies Op and Value to an item field.
type Compare struct {
	Field Field
	Op    Op
	Value Value
}

// PropertyValue matches items with a property named Key (any property when
// Key is empty) whose value satisfies Op and Value.
type PropertyValue struct {
	Key   string
	Op    Op
	Value Value
}

// PropertyKey matches items with a property whose key satisfies Op and
// Value.
type PropertyKey struct {
	Op    Op
	Value Value
}

// SameProperty matches items with one property satisfying every child at
// once; its children are PropertyValue and PropertyKey.
type SameProperty []Node

// BuildRelation is how an item relates to a build.
type BuildRelation string

// Build relations: an item is an artifact a build produced, or a
// dependency it used, matched by SHA-1.
const (
	BuildArtifact   BuildRelation = "artifact"
	BuildDependency BuildRelation = "dependency"
)

// BuildField is a field of a build run.
type BuildField string

// Build fields.
const (
	BuildName   BuildField = "name"
	BuildNumber BuildField = "number"
)

// Build matches items related to a build run whose Field satisfies Op and
// Value.
type Build struct {
	Relation BuildRelation
	Field    BuildField
	Op       Op
	Value    Value
}

func (All) node()           {}
func (Any) node()           {}
func (Compare) node()       {}
func (PropertyValue) node() {}
func (PropertyKey) node()   {}
func (SameProperty) node()  {}
func (Build) node()         {}

// Mentions reports whether n compares field anywhere.
func Mentions(n Node, field Field) bool {
	switch t := n.(type) {
	case Compare:
		return t.Field == field
	case All:
		for _, c := range t {
			if Mentions(c, field) {
				return true
			}
		}
	case Any:
		for _, c := range t {
			if Mentions(c, field) {
				return true
			}
		}
	}

	return false
}

// ExpandRepositories rewrites every repository equality through expand,
// which maps a repository name to the names whose items it covers (a
// virtual repository's members).
func ExpandRepositories(n Node, expand func(name string) []string) Node {
	switch t := n.(type) {
	case All:
		out := make(All, len(t))
		for i, c := range t {
			out[i] = ExpandRepositories(c, expand)
		}

		return out
	case Any:
		out := make(Any, len(t))
		for i, c := range t {
			out[i] = ExpandRepositories(c, expand)
		}

		return out
	case Compare:
		name, ok := t.Value.(string)
		if t.Field != FieldRepository || t.Op != OpEqual || !ok {
			return t
		}

		names := expand(name)
		if len(names) == 1 && names[0] == name {
			return t
		}

		if len(names) == 0 {
			return t
		}

		out := make(Any, 0, len(names))
		for _, k := range names {
			out = append(out, Compare{
				Field: FieldRepository,
				Op:    OpEqual,
				Value: k,
			})
		}

		return out
	default:
		return n
	}
}

// Row is one item a query found.
type Row struct {
	Repository, Path, Name, Type string
	Size                         int64
	Created, Modified, Updated   time.Time
	CreatedBy, ModifiedBy        string
	SHA1, MD5, SHA256            *string
	Properties                   []Property
}

// Property is one key and value of an item's properties.
type Property struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Depth is the row's depth (FieldDepth).
func (r Row) Depth() int {
	if r.Path == "." {
		return 1
	}

	n := 2

	for _, c := range r.Path {
		if c == '/' {
			n++
		}
	}

	return n
}

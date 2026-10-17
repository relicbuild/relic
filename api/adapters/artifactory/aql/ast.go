// Package aql parses AQL, the query language of the adapter's search API.
package aql

// Query is a parsed AQL statement.
type Query struct {
	Domain   string
	Criteria Node // nil matches everything
	Include  []string
	Sort     *Sort
	Offset   *int64
	Limit    *int64
	Distinct *bool
}

// Sort orders results by Fields, all ascending or all descending.
type Sort struct {
	Desc   bool
	Fields []string
}

// Node is a criteria tree node: And, Or, MatchSingleProperty or Compare.
type Node interface{ node() }

// And matches when every child matches.
type And []Node

// Or matches when any child matches.
type Or []Node

// MatchSingleProperty ($msp) matches when one property row satisfies every
// child comparison at once.
type MatchSingleProperty []Compare

// Compare applies Op to Field and Value.
type Compare struct {
	Field string
	Op    string
	Value Value
}

// Comparison operators.
const (
	OpEq     = "$eq"
	OpNe     = "$ne"
	OpGt     = "$gt"
	OpGte    = "$gte"
	OpLt     = "$lt"
	OpLte    = "$lte"
	OpMatch  = "$match"
	OpNmatch = "$nmatch"
)

// Value is a literal: string, float64, bool or nil.
type Value = any

func (And) node()                 {}
func (Or) node()                  {}
func (MatchSingleProperty) node() {}
func (Compare) node()             {}

// ExpandRepos rewrites every "repo" $eq criterion through expand, which
// maps a repository key to the keys whose items it covers (a virtual
// repository's members).
func ExpandRepos(n Node, expand func(key string) []string) Node {
	switch n := n.(type) {
	case And:
		out := make(And, len(n))
		for i, c := range n {
			out[i] = ExpandRepos(c, expand)
		}

		return out
	case Or:
		out := make(Or, len(n))
		for i, c := range n {
			out[i] = ExpandRepos(c, expand)
		}

		return out
	case Compare:
		key, ok := n.Value.(string)
		if n.Field != "repo" || n.Op != OpEq || !ok {
			return n
		}

		keys := expand(key)
		if len(keys) == 1 && keys[0] == key {
			return n
		}

		or := make(Or, 0, len(keys))
		for _, k := range keys {
			or = append(or, Compare{
				Field: "repo",
				Op:    OpEq,
				Value: k,
			})
		}

		if len(or) == 0 {
			return n
		}

		return or
	default:
		return n
	}
}

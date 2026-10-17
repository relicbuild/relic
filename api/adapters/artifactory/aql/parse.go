package aql

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrSyntax wraps every parse error.
var ErrSyntax = errors.New("aql syntax error")

// maxDepth bounds criteria nesting so hostile input cannot exhaust the stack.
const maxDepth = 64

var operators = []string{
	OpEq,
	OpNe,
	OpGt,
	OpGte,
	OpLt,
	OpLte,
	OpMatch,
	OpNmatch,
}

// Parse parses one AQL statement.
func Parse(src string) (*Query, error) {
	p := &parser{
		src: src,
	}

	q, err := p.query()
	if err != nil {
		return nil, fmt.Errorf("%w: %w (at offset %d)", ErrSyntax, err, p.pos)
	}

	return q, nil
}

type parser struct {
	src string
	pos int
}

func (p *parser) query() (*Query, error) {
	q := &Query{}

	p.skipSpace()

	q.Domain = p.ident()
	if q.Domain == "" {
		return nil, errors.New("expected a domain such as items")
	}

	if err := p.expectWord(".find"); err != nil {
		return nil, err
	}

	if err := p.expect('('); err != nil {
		return nil, err
	}

	p.skipSpace()

	if !p.peek(')') {
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}

		obj, ok := v.(object)
		if !ok {
			return nil, errors.New("find() takes an object")
		}

		if q.Criteria, err = criteria(obj, 0); err != nil {
			return nil, err
		}
	}

	if err := p.expect(')'); err != nil {
		return nil, err
	}

	for {
		p.skipSpace()

		if p.pos == len(p.src) {
			return q, nil
		}

		if err := p.expect('.'); err != nil {
			return nil, err
		}

		method := p.ident()

		args, err := p.args()
		if err != nil {
			return nil, err
		}

		if err := q.apply(method, args); err != nil {
			return nil, err
		}
	}
}

func (q *Query) apply(method string, args []any) error {
	switch method {
	case "include":
		for _, a := range args {
			s, ok := a.(string)
			if !ok {
				return errors.New("include() takes field names")
			}

			q.Include = append(q.Include, s)
		}
	case "sort":
		if len(args) != 1 {
			return errors.New(`sort() takes one object like {"$asc":["name"]}`)
		}

		obj, ok := args[0].(object)
		if !ok || len(obj) != 1 || (obj[0].key != "$asc" && obj[0].key != "$desc") {
			return errors.New(`sort() takes {"$asc":[...]} or {"$desc":[...]}`)
		}

		fields, err := stringArray(obj[0].value)
		if err != nil {
			return fmt.Errorf("sort: %w", err)
		}

		q.Sort = &Sort{
			Desc:   obj[0].key == "$desc",
			Fields: fields,
		}
	case "offset", "limit":
		n, err := oneInt(args)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}

		if method == "offset" {
			q.Offset = &n
		} else {
			q.Limit = &n
		}
	case "distinct":
		if len(args) != 1 {
			return errors.New("distinct() takes a boolean")
		}

		b, ok := args[0].(bool)
		if !ok {
			return errors.New("distinct() takes a boolean")
		}

		q.Distinct = &b
	case "transitive":
		// Searches of a virtual repository always cover all of its members.
		if len(args) != 0 {
			return errors.New("transitive() takes no arguments")
		}
	default:
		return fmt.Errorf("unknown method %q", method)
	}

	return nil
}

func oneInt(args []any) (int64, error) {
	if len(args) != 1 {
		return 0, errors.New("takes one number")
	}

	f, ok := args[0].(float64)
	if !ok || f < 0 || f != float64(int64(f)) {
		return 0, errors.New("takes a non-negative integer")
	}

	return int64(f), nil
}

func stringArray(v any) ([]string, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, errors.New("expected an array of field names")
	}

	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, errors.New("expected an array of field names")
		}

		out = append(out, s)
	}

	return out, nil
}

// object keeps members in source order; duplicate keys are allowed.
type object []member

type member struct {
	key   string
	value any
}

func criteria(obj object, depth int) (Node, error) {
	if depth > maxDepth {
		return nil, errors.New("criteria nested too deeply")
	}

	and := And{}

	for _, m := range obj {
		switch m.key {
		case "$and", "$or":
			children, err := criteriaList(m.value, depth)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", m.key, err)
			}

			if m.key == "$and" {
				and = append(and, And(children))
			} else {
				and = append(and, Or(children))
			}
		case "$msp":
			children, err := criteriaList(m.value, depth)
			if err != nil {
				return nil, fmt.Errorf("$msp: %w", err)
			}

			msp := MatchSingleProperty{}

			for _, c := range flatten(children) {
				cmp, ok := c.(Compare)
				if !ok {
					return nil, errors.New("$msp takes comparisons only")
				}

				msp = append(msp, cmp)
			}

			and = append(and, msp)
		default:
			if strings.HasPrefix(m.key, "$") {
				return nil, fmt.Errorf("unknown operator %q", m.key)
			}

			cmps, err := comparisons(m.key, m.value)
			if err != nil {
				return nil, err
			}

			and = append(and, cmps...)
		}
	}

	if len(and) == 1 {
		return and[0], nil
	}

	return and, nil
}

func criteriaList(v any, depth int) ([]Node, error) {
	var items []any

	switch t := v.(type) {
	case []any:
		items = t
	case object:
		items = []any{
			t,
		}
	default:
		return nil, errors.New("expected an array of criteria")
	}

	var nodes []Node

	for _, it := range items {
		obj, ok := it.(object)
		if !ok {
			return nil, errors.New("expected an array of criteria objects")
		}

		for _, m := range obj {
			n, err := criteria(object{
				m,
			}, depth+1)
			if err != nil {
				return nil, err
			}

			nodes = append(nodes, n)
		}
	}

	return nodes, nil
}

func flatten(nodes []Node) []Node {
	var out []Node

	for _, n := range nodes {
		if a, ok := n.(And); ok {
			out = append(out, flatten(a)...)
		} else {
			out = append(out, n)
		}
	}

	return out
}

func comparisons(field string, v any) ([]Node, error) {
	obj, ok := v.(object)
	if !ok {
		if _, isArr := v.([]any); isArr {
			return nil, fmt.Errorf("field %q: arrays are not comparable", field)
		}

		return []Node{
			Compare{
				Field: field,
				Op:    OpEq,
				Value: v,
			},
		}, nil
	}

	out := make([]Node, 0, len(obj))
	for _, m := range obj {
		if !slices.Contains(operators, m.key) {
			return nil, fmt.Errorf("field %q: unknown operator %q", field, m.key)
		}

		op, value := m.key, m.value

		switch nested := m.value.(type) {
		case object:
			var err error
			if op, value, err = nestedMatch(field, m.key, nested); err != nil {
				return nil, err
			}
		case []any:
			return nil, fmt.Errorf("field %q: %s takes a literal", field, m.key)
		}

		out = append(out, Compare{
			Field: field,
			Op:    op,
			Value: value,
		})
	}

	return out, nil
}

// nestedMatch folds $eq or $ne of a {"$match":pattern} or {"$nmatch":pattern} object into one match operator.
func nestedMatch(field, outer string, nested object) (string, Value, error) {
	if len(nested) != 1 || (outer != OpEq && outer != OpNe) ||
		(nested[0].key != OpMatch && nested[0].key != OpNmatch) {
		return "", nil, fmt.Errorf("field %q: %s takes a literal", field, outer)
	}

	pattern, ok := nested[0].value.(string)
	if !ok {
		return "", nil, fmt.Errorf("field %q: %s takes a pattern", field, nested[0].key)
	}

	if (outer == OpNe) != (nested[0].key == OpNmatch) {
		return OpNmatch, pattern, nil
	}

	return OpMatch, pattern, nil
}

func (p *parser) skipSpace() {
	for p.pos < len(p.src) {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if !unicode.IsSpace(r) {
			return
		}

		p.pos += size
	}
}

func (p *parser) peek(c byte) bool { return p.pos < len(p.src) && p.src[p.pos] == c }

func (p *parser) expect(c byte) error {
	p.skipSpace()

	if !p.peek(c) {
		return fmt.Errorf("expected %q", c)
	}

	p.pos++

	return nil
}

func (p *parser) expectWord(w string) error {
	p.skipSpace()

	if !strings.HasPrefix(p.src[p.pos:], w) {
		return fmt.Errorf("expected %q", w)
	}

	p.pos += len(w)

	return nil
}

func (p *parser) ident() string {
	p.skipSpace()

	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' && p.pos > start {
			p.pos++
			continue
		}

		break
	}

	return p.src[start:p.pos]
}

func (p *parser) args() ([]any, error) {
	if err := p.expect('('); err != nil {
		return nil, err
	}

	var args []any

	p.skipSpace()

	if p.peek(')') {
		p.pos++
		return args, nil
	}

	for {
		v, err := p.value(0)
		if err != nil {
			return nil, err
		}

		args = append(args, v)

		p.skipSpace()

		if p.peek(',') {
			p.pos++
			continue
		}

		return args, p.expect(')')
	}
}

func (p *parser) value(depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("value nested too deeply")
	}

	p.skipSpace()

	if p.pos >= len(p.src) {
		return nil, errors.New("unexpected end of query")
	}

	switch c := p.src[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"' || c == '\'':
		return p.string()
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	default:
		switch w := p.ident(); w {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected %q", string(c))
		}
	}
}

func (p *parser) object(depth int) (object, error) {
	p.pos++
	obj := object{}

	p.skipSpace()

	if p.peek('}') {
		p.pos++
		return obj, nil
	}

	for {
		p.skipSpace()

		if !p.peek('"') && !p.peek('\'') {
			return nil, errors.New("expected a quoted key")
		}

		key, err := p.string()
		if err != nil {
			return nil, err
		}

		if err := p.expect(':'); err != nil {
			return nil, err
		}

		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}

		obj = append(obj, member{
			key:   key,
			value: v,
		})

		p.skipSpace()

		if p.peek(',') {
			p.pos++
			continue
		}

		return obj, p.expect('}')
	}
}

func (p *parser) array(depth int) ([]any, error) {
	p.pos++
	arr := []any{}

	p.skipSpace()

	if p.peek(']') {
		p.pos++
		return arr, nil
	}

	for {
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}

		arr = append(arr, v)

		p.skipSpace()

		if p.peek(',') {
			p.pos++
			continue
		}

		return arr, p.expect(']')
	}
}

func (p *parser) string() (string, error) {
	quote := p.src[p.pos]
	p.pos++

	var b strings.Builder

	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case quote:
			p.pos++
			return b.String(), nil
		case '\\':
			if p.pos+1 >= len(p.src) {
				return "", errors.New("unterminated escape")
			}

			e := p.src[p.pos+1]
			p.pos += 2

			switch e {
			case '"', '\'', '\\', '/':
				b.WriteByte(e)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				if p.pos+4 > len(p.src) {
					return "", errors.New(`short \u escape`)
				}

				n, err := strconv.ParseUint(p.src[p.pos:p.pos+4], 16, 16)
				if err != nil {
					return "", errors.New(`invalid \u escape`)
				}

				r := rune(n)
				p.pos += 4

				if utf16.IsSurrogate(r) && strings.HasPrefix(p.src[p.pos:], `\u`) && p.pos+6 <= len(p.src) {
					low, err := strconv.ParseUint(p.src[p.pos+2:p.pos+6], 16, 16)
					if pair := utf16.DecodeRune(r, rune(low)); err == nil && pair != utf8.RuneError {
						r = pair
						p.pos += 6
					}
				}

				b.WriteRune(r)
			default:
				return "", fmt.Errorf("invalid escape \\%c", e)
			}
		default:
			b.WriteByte(c)

			p.pos++
		}
	}

	return "", errors.New("unterminated string")
}

func (p *parser) number() (float64, error) {
	start := p.pos
	if p.peek('-') {
		p.pos++
	}

	for p.pos < len(p.src) && strings.IndexByte("0123456789.eE+-", p.src[p.pos]) >= 0 {
		p.pos++
	}

	f, err := strconv.ParseFloat(p.src[start:p.pos], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", p.src[start:p.pos])
	}

	return f, nil
}

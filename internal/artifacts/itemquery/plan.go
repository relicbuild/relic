package itemquery

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"relic/internal/platform/database"
)

type fieldKind int

const (
	kindText fieldKind = iota
	kindNumber
	kindTime
)

type column struct {
	expr string
	kind fieldKind
}

var columns = map[Field]column{
	FieldRepository: {
		"i.repo_key",
		kindText,
	},
	FieldPath: {
		"i.path",
		kindText,
	},
	FieldName: {
		"i.name",
		kindText,
	},
	FieldType: {
		"i.type",
		kindText,
	},
	FieldSize: {
		"i.size",
		kindNumber,
	},
	FieldCreated: {
		"i.created",
		kindTime,
	},
	FieldCreatedBy: {
		"i.created_by",
		kindText,
	},
	FieldModified: {
		"i.modified",
		kindTime,
	},
	FieldModifiedBy: {
		"i.modified_by",
		kindText,
	},
	FieldUpdated: {
		"i.updated",
		kindTime,
	},
	FieldSHA1: {
		"i.sha1",
		kindText,
	},
	FieldMD5: {
		"i.md5",
		kindText,
	},
	FieldSHA256: {
		"i.sha256",
		kindText,
	},
	FieldDepth: {
		"(CASE WHEN i.path = '.' THEN 1 ELSE array_length(string_to_array(i.path, '/'), 1) + 1 END)",
		kindNumber,
	},
}

var buildTables = map[BuildRelation]string{
	BuildArtifact:   "build_artifacts x JOIN builds b ON b.id = x.build_id",
	BuildDependency: "build_dependencies x JOIN builds b ON b.id = x.build_id",
}

var buildColumns = map[BuildField]string{
	BuildName:   "b.name",
	BuildNumber: "b.number",
}

const itemColumns = "i.repo_key, i.path, i.name, i.type, i.size, i.created, i.created_by, i.modified, i.modified_by, i.updated, i.sha1, i.md5, i.sha256"

// Plan is a query compiled to SQL.
type Plan struct {
	sql        string
	args       []any
	properties bool
}

const propertiesColumn = "/*properties*/"

const (
	postgresqlProperties = ", (SELECT coalesce(json_agg(json_build_object('key', p.key, 'value', p.value) " +
		"ORDER BY p.key, p.value), '[]'::json) FROM item_properties p WHERE p.item_id = i.id)"
	sqliteProperties = ", (SELECT json_group_array(json_object('key', p.key, 'value', p.value) " +
		"ORDER BY p.key, p.value) FROM item_properties p WHERE p.item_id = i.id)"
)

type planner struct {
	args []any
}

func (p *planner) param(v any) string {
	p.args = append(p.args, v)
	return "$" + strconv.Itoa(len(p.args))
}

// Compile plans q.
func Compile(q Query) (*Plan, error) {
	p := &planner{}
	where := "TRUE"

	if q.Criteria != nil {
		var err error
		if where, err = p.node(q.Criteria); err != nil {
			return nil, err
		}
	}

	var sql strings.Builder
	sql.WriteString("SELECT " + itemColumns)

	if q.Properties {
		sql.WriteString(propertiesColumn)
	}

	sql.WriteString(" FROM items i WHERE " + where)

	if q.Sort != nil && len(q.Sort.Fields) > 0 {
		order := make([]string, 0, len(q.Sort.Fields))
		for _, f := range q.Sort.Fields {
			c, ok := columns[f]
			if !ok {
				return nil, fmt.Errorf("%w: cannot sort by %q", ErrInvalid, f)
			}

			dir := " ASC NULLS LAST"
			if q.Sort.Desc {
				dir = " DESC NULLS FIRST"
			}

			order = append(order, c.expr+dir)
		}

		sql.WriteString(" ORDER BY " + strings.Join(order, ", "))
	}

	// LIMIT before OFFSET, which SQLite requires; an offset without a
	// limit takes the largest one, as SQLite has no OFFSET alone.
	switch {
	case q.Limit != nil:
		sql.WriteString(" LIMIT " + p.param(*q.Limit))
	case q.Offset != nil:
		sql.WriteString(" LIMIT " + p.param(int64(math.MaxInt64)))
	}

	if q.Offset != nil {
		sql.WriteString(" OFFSET " + p.param(*q.Offset))
	}

	return &Plan{
		sql:        sql.String(),
		args:       p.args,
		properties: q.Properties,
	}, nil
}

func (p *planner) node(n Node) (string, error) {
	switch t := n.(type) {
	case All:
		return p.all(t)
	case Any:
		return p.join(t, " OR ", "FALSE")
	case SameProperty:
		if len(t) == 0 {
			return "", fmt.Errorf("%w: same property holds no comparison", ErrInvalid)
		}

		conds := make([]string, 0, len(t))
		for _, c := range t {
			cond, err := p.propertyRow(c, false)
			if err != nil {
				return "", err
			}

			conds = append(conds, cond)
		}

		return propertyExists(strings.Join(conds, " AND ")), nil
	case PropertyValue, PropertyKey:
		cond, err := p.propertyRow(t, true)
		if err != nil {
			return "", err
		}

		if negated(opOf(t)) {
			return "NOT " + propertyExists(cond), nil
		}

		return propertyExists(cond), nil
	case Build:
		return p.buildExists(t.Relation, []Build{
			t,
		})
	case Compare:
		c, ok := columns[t.Field]
		if !ok {
			return "", fmt.Errorf("%w: field %q", ErrInvalid, t.Field)
		}

		return p.condition(c.expr, c.kind, t.Op, t.Value)
	}

	return "", fmt.Errorf("%w: criterion %T", ErrUnsupported, n)
}

func propertyExists(cond string) string {
	return "EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND " + cond + ")"
}

func (p *planner) join(nodes []Node, sep, empty string) (string, error) {
	if len(nodes) == 0 {
		return empty, nil
	}

	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		s, err := p.node(n)
		if err != nil {
			return "", err
		}

		parts = append(parts, s)
	}

	return "(" + strings.Join(parts, sep) + ")", nil
}

func (p *planner) all(nodes All) (string, error) {
	groups := map[BuildRelation][]Build{}

	var (
		order []BuildRelation
		rest  []Node
	)

	for _, n := range nodes {
		if b, ok := n.(Build); ok {
			if _, seen := groups[b.Relation]; !seen {
				order = append(order, b.Relation)
			}

			groups[b.Relation] = append(groups[b.Relation], b)

			continue
		}

		rest = append(rest, n)
	}

	if len(order) == 0 {
		return p.join(rest, " AND ", "TRUE")
	}

	parts := make([]string, 0, len(rest)+len(order))
	for _, n := range rest {
		s, err := p.node(n)
		if err != nil {
			return "", err
		}

		parts = append(parts, s)
	}

	for _, rel := range order {
		s, err := p.buildExists(rel, groups[rel])
		if err != nil {
			return "", err
		}

		parts = append(parts, s)
	}

	return "(" + strings.Join(parts, " AND ") + ")", nil
}

func (p *planner) buildExists(rel BuildRelation, builds []Build) (string, error) {
	from, ok := buildTables[rel]
	if !ok {
		return "", fmt.Errorf("%w: build relation %q", ErrInvalid, rel)
	}

	conds := make([]string, 0, len(builds))
	for _, b := range builds {
		col, ok := buildColumns[b.Field]
		if !ok {
			return "", fmt.Errorf("%w: build field %q", ErrInvalid, b.Field)
		}

		cond, err := p.condition(col, kindText, b.Op, b.Value)
		if err != nil {
			return "", err
		}

		conds = append(conds, cond)
	}

	return "EXISTS (SELECT 1 FROM " + from + " WHERE x.sha1 = i.sha1 AND " + strings.Join(conds, " AND ") + ")", nil
}

func (p *planner) propertyRow(n Node, asPositive bool) (string, error) {
	op := opOf(n)
	if asPositive {
		op = positive(op)
	}

	switch t := n.(type) {
	case PropertyKey:
		return p.condition("p.key", kindText, op, t.Value)
	case PropertyValue:
		cond, err := p.condition("p.value", kindText, op, t.Value)
		if err != nil || t.Key == "" {
			return cond, err
		}

		return "p.key = " + p.param(t.Key) + " AND " + cond, nil
	}

	return "", fmt.Errorf("%w: %T inside SameProperty", ErrInvalid, n)
}

func opOf(n Node) Op {
	switch t := n.(type) {
	case PropertyKey:
		return t.Op
	case PropertyValue:
		return t.Op
	}

	return ""
}

func negated(op Op) bool { return op == OpNotEqual || op == OpNotMatch }

func positive(op Op) Op {
	switch op {
	case OpNotEqual:
		return OpEqual
	case OpNotMatch:
		return OpMatch
	default:
		return op
	}
}

var sqlOps = map[Op]string{
	OpEqual:          "=",
	OpNotEqual:       "<>",
	OpGreater:        ">",
	OpGreaterOrEqual: ">=",
	OpLess:           "<",
	OpLessOrEqual:    "<=",
}

func (p *planner) condition(col string, kind fieldKind, op Op, v Value) (string, error) {
	if v == nil {
		switch op {
		case OpEqual:
			return col + " IS NULL", nil
		case OpNotEqual:
			return col + " IS NOT NULL", nil
		default:
			return "", fmt.Errorf("%w: %s null", ErrInvalid, op)
		}
	}

	switch op { //nolint:exhaustive // the other operators compare below
	case OpMatch, OpNotMatch:
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%w: %s takes a string", ErrInvalid, op)
		}

		pattern := p.param(WildcardToLike(s))
		if op == OpNotMatch {
			return "(" + col + " IS NULL OR CAST(" + col + " AS TEXT) NOT LIKE " + pattern + ` ESCAPE '\')`, nil
		}

		return "CAST(" + col + " AS TEXT) LIKE " + pattern + ` ESCAPE '\'`, nil
	}

	sqlOp, ok := sqlOps[op]
	if !ok {
		return "", fmt.Errorf("%w: operator %q", ErrInvalid, op)
	}

	lit, err := literal(kind, v)
	if err != nil {
		return "", err
	}

	if op == OpNotEqual {
		return "(" + col + " IS NULL OR " + col + " <> " + p.param(lit) + ")", nil
	}

	return col + " " + sqlOp + " " + p.param(lit), nil
}

func literal(kind fieldKind, v Value) (any, error) {
	switch kind {
	case kindNumber:
		switch t := v.(type) {
		case int64:
			return t, nil
		case int:
			return int64(t), nil
		case float64:
			if t != math.Trunc(t) {
				return nil, fmt.Errorf("%w: %v is not a whole number", ErrInvalid, t)
			}

			if t < math.MinInt64 || t >= math.MaxInt64 {
				return nil, fmt.Errorf("%w: %v is out of range", ErrInvalid, t)
			}

			return int64(t), nil
		case string:
			n, err := strconv.ParseInt(t, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: %q is not a number", ErrInvalid, t)
			}

			return n, nil
		}
	case kindTime:
		switch t := v.(type) {
		case time.Time:
			return t, nil
		case string:
			for _, layout := range []string{
				time.RFC3339Nano,
				"2006-01-02",
			} {
				if parsed, err := time.Parse(layout, t); err == nil {
					return parsed, nil
				}
			}

			return nil, fmt.Errorf("%w: %q is not a date", ErrInvalid, t)
		}
	case kindText:
		switch t := v.(type) {
		case string:
			return t, nil
		case int64:
			return strconv.FormatInt(t, 10), nil
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64), nil
		case bool:
			return strconv.FormatBool(t), nil
		}
	}

	return nil, fmt.Errorf("%w: unexpected value %v", ErrInvalid, v)
}

// WildcardToLike converts a match pattern (* and ?) to a LIKE pattern with
// backslash escapes.
func WildcardToLike(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch r {
		case '*':
			b.WriteByte('%')
		case '?':
			b.WriteByte('_')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

// Querier runs planned SQL.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (database.Rows, error)
	SQLite() bool
}

// Run runs plan and calls fn for each row, stopping at the first error.
func Run(ctx context.Context, db Querier, plan *Plan, fn func(Row) error) error {
	properties := postgresqlProperties
	if db.SQLite() {
		properties = sqliteProperties
	}

	rows, err := db.Query(ctx, strings.Replace(plan.sql, propertiesColumn, properties, 1), plan.args...)
	if err != nil {
		return fmt.Errorf("item query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r Row

		dest := []any{
			&r.Repository,
			&r.Path,
			&r.Name,
			&r.Type,
			&r.Size,
			&r.Created,
			&r.CreatedBy,
			&r.Modified,
			&r.ModifiedBy,
			&r.Updated,
			&r.SHA1,
			&r.MD5,
			&r.SHA256,
		}

		var props []byte
		if plan.properties {
			dest = append(dest, &props)
		}

		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("item query scan: %w", err)
		}

		if len(props) > 0 {
			if err := json.Unmarshal(props, &r.Properties); err != nil {
				return fmt.Errorf("item query properties: %w", err)
			}
		}

		if err := fn(r); err != nil {
			return err
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("item query rows: %w", err)
	}

	return nil
}

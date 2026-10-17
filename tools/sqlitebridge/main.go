// Command sqlitebridge generates the SQLite implementation of
// database.Querier (internal/platform/database/sqlite_queries.go).
package main

import (
	"bytes"
	"cmp"
	"flag"
	"fmt"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/imports"
)

const (
	databasePkg = "relic/internal/platform/database"
	outFile     = "internal/platform/database/sqlite_queries.go"
)

func main() {
	out := flag.String("o", outFile, "the file to write")
	partial := flag.Bool(
		"partial",
		false,
		"while translating: a query missing from queries/sqlite becomes a method that fails",
	)
	check := flag.Bool(
		"check",
		false,
		"write nothing: map the queries and prepare each against a migrated SQLite database (implies -partial)",
	)

	flag.Parse()

	src, err := generate(".", *partial || *check)
	if err != nil {
		log.Fatal(err)
	}

	if *check {
		if err := prepareAll("."); err != nil {
			log.Fatal(err)
		}

		fmt.Println("ok")

		return
	}

	if err := os.WriteFile(*out, src, 0o600); err != nil {
		log.Fatal(err)
	}
}

type query struct {
	Name   string
	Kind   string
	SQL    string
	Params []string
	File   string
}

var (
	header     = regexp.MustCompile(`(?m)^-- name: (\w+) :(\w+)\s*$`)
	param      = regexp.MustCompile(`@(\w+)|sqlc\.n?arg\('?(\w+)'?\)`)
	positional = regexp.MustCompile(`[$?](\d+)\b`)
)

func generate(root string, partial bool) ([]byte, error) {
	pg, err := readQueries(filepath.Join(root, "queries", "postgresql"))
	if err != nil {
		return nil, err
	}

	lite, err := readQueries(filepath.Join(root, "queries", "sqlite"))
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(filepath.Join(root, outFile))
	if err != nil {
		return nil, err
	}

	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedTypes | packages.NeedName,
		Dir:  root,
		Overlay: map[string][]byte{
			abs: []byte("package database\n\nfunc newSQLiteQuerier(sqliteDBTX) Querier { return nil }\n"),
		},
	}, databasePkg)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", databasePkg, err)
	}

	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("load %s: errors above", databasePkg)
	}

	scope := pkgs[0].Types.Scope()

	querier, ok := scope.Lookup("Querier").Type().Underlying().(*types.Interface)
	if !ok {
		return nil, fmt.Errorf("%s.Querier is not an interface", databasePkg)
	}

	g := &generator{
		qualifier: func(p *types.Package) string {
			if p.Path() == databasePkg {
				return ""
			}

			return p.Name()
		},
	}

	var problems []string

	for i := range querier.NumMethods() {
		m := querier.Method(i)

		pq, ok := pg[m.Name()]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: not in queries/postgresql", m.Name()))

			continue
		}

		lq, ok := lite[m.Name()]
		if !ok && partial {
			g.missing(m)

			continue
		}

		if !ok {
			problems = append(problems, fmt.Sprintf("%s (%s): missing from queries/sqlite", m.Name(), pq.File))

			continue
		}

		if lq.Kind != pq.Kind {
			problems = append(
				problems,
				fmt.Sprintf("%s: :%s in queries/sqlite, :%s in queries/postgresql", m.Name(), lq.Kind, pq.Kind),
			)

			continue
		}

		if err := g.method(m, pq, lq); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", m.Name(), err))
		}
	}

	for name, lq := range lite {
		if _, ok := pg[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s (%s): only in queries/sqlite", name, lq.File))
		}
	}

	if len(problems) > 0 {
		slices.Sort(problems)

		return nil, fmt.Errorf("%d problems:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}

	return g.source()
}

func readQueries(dir string) (map[string]query, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}

	out := map[string]query{}

	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // a path under the repository
		if err != nil {
			return nil, err
		}

		text := string(b)
		heads := header.FindAllStringSubmatchIndex(text, -1)

		for i, h := range heads {
			end := len(text)
			if i+1 < len(heads) {
				end = heads[i+1][0]
			}

			q := query{
				Name: text[h[2]:h[3]],
				Kind: text[h[4]:h[5]],
				SQL:  param.ReplaceAllStringFunc(strings.TrimSpace(text[h[1]:end]), atName),
				File: filepath.Base(f),
			}
			q.Params = paramNames(q.SQL)

			if _, dup := out[q.Name]; dup {
				return nil, fmt.Errorf("%s: %s is defined twice", f, q.Name)
			}

			out[q.Name] = q
		}
	}

	return out, nil
}

func atName(ref string) string {
	m := param.FindStringSubmatch(ref)

	return "@" + cmp.Or(m[1], m[2])
}

func paramNames(sql string) []string {
	var names []string

	for line := range strings.Lines(sql) {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}

		for _, m := range param.FindAllStringSubmatch(line, -1) {
			name := cmp.Or(m[1], m[2])
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}

	return names
}

type generator struct {
	partial   bool
	qualifier types.Qualifier
	body      bytes.Buffer
	queries   bytes.Buffer
}

func (g *generator) typeString(t types.Type) string {
	return types.TypeString(t, g.qualifier)
}

func (g *generator) method(m *types.Func, pq, lq query) error {
	sig, ok := m.Type().(*types.Signature)
	if !ok {
		return fmt.Errorf("not a method signature")
	}

	var (
		argDecl string
		binds   []string
	)

	if sig.Params().Len() == 2 {
		arg := sig.Params().At(1)
		argDecl = ", arg " + g.typeString(arg.Type())

		var err error

		binds, err = g.binds(arg.Type(), pq, lq)
		if err != nil {
			return err
		}
	} else if len(lq.Params) > 0 || positional.MatchString(stripComments(lq.SQL)) {
		return fmt.Errorf("the SQLite query has parameters, the PostgreSQL query none")
	}

	constName := "sqlite" + m.Name()
	// The name comment leads the text, so telemetry labels the query.
	text := fmt.Sprintf("-- name: %s :%s\n%s", m.Name(), lq.Kind, lq.SQL)
	fmt.Fprintf(&g.queries, "const %s = %s\n\n", constName, backquote(text))

	args := strings.Join(append([]string{
		"ctx",
		constName,
	}, binds...), ", ")
	rest := strings.Join(append([]string{
		constName,
	}, binds...), ", ")

	results := sig.Results()

	switch pq.Kind {
	case "exec":
		fmt.Fprintf(&g.body, `func (q *sqliteQueries) %s(ctx context.Context%s) error {
	_, err := q.db.ExecContext(%s)

	return err
}

`, m.Name(), argDecl, args)
	case "execrows":
		fmt.Fprintf(&g.body, `func (q *sqliteQueries) %s(ctx context.Context%s) (int64, error) {
	res, err := q.db.ExecContext(%s)
	if err != nil {
		return 0, err
	}

	return res.RowsAffected()
}

`, m.Name(), argDecl, args)
	case "one", "many":
		elem := results.At(0).Type()
		if pq.Kind == "many" {
			s, ok := elem.(*types.Slice)
			if !ok {
				return fmt.Errorf(":many returns %s, not a slice", g.typeString(elem))
			}

			elem = s.Elem()
		}

		targets, columns, err := g.scanTargets("i", elem)
		if err != nil {
			return err
		}

		cols := "nil"
		if columns != nil {
			cols = fmt.Sprintf("%#v", columns)
		}

		g.rowsMethod(m.Name(), pq.Kind, argDecl, g.typeString(elem), cols, strings.Join(targets, ", "), rest)
	default:
		return fmt.Errorf("query kind :%s is not supported", pq.Kind)
	}

	return nil
}

func (g *generator) missing(m *types.Func) {
	sig := m.Type().(*types.Signature) //nolint:forcetypeassert // a method

	params := []string{
		"context.Context",
	}
	if sig.Params().Len() == 2 {
		params = append(params, g.typeString(sig.Params().At(1).Type()))
	}

	results := make([]string, 0, sig.Results().Len())
	zeros := make([]string, 0, sig.Results().Len())

	for i := range sig.Results().Len() {
		t := sig.Results().At(i).Type()
		results = append(results, g.typeString(t))

		if i < sig.Results().Len()-1 {
			zeros = append(zeros, "*new("+g.typeString(t)+")")
		}
	}

	zeros = append(zeros, fmt.Sprintf("errors.New(%q)", "sqlite: "+m.Name()+" is not translated yet"))
	fmt.Fprintf(&g.body, "func (q *sqliteQueries) %s(%s) (%s) {\n\treturn %s\n}\n\n",
		m.Name(), strings.Join(params, ", "), strings.Join(results, ", "), strings.Join(zeros, ", "))
	g.partial = true
}

func (g *generator) rowsMethod(name, kind, argDecl, elem, cols, targets, rest string) {
	if kind == "one" {
		fmt.Fprintf(&g.body, `func (q *sqliteQueries) %[1]s(ctx context.Context%[2]s) (%[3]s, error) {
	var i %[3]s

	rows, err := q.query(ctx, %[6]q, %[4]s, %[7]s)
	if err != nil {
		return i, err
	}

	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return i, err
		}

		return i, sql.ErrNoRows
	}

	err = rows.Scan(%[5]s)

	return i, err
}

`, name, argDecl, elem, cols, targets, name, rest)

		return
	}

	fmt.Fprintf(&g.body, `func (q *sqliteQueries) %[1]s(ctx context.Context%[2]s) ([]%[3]s, error) {
	rows, err := q.query(ctx, %[6]q, %[4]s, %[7]s)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	items := []%[3]s{}

	for rows.Next() {
		var i %[3]s
		if err := rows.Scan(%[5]s); err != nil {
			return nil, err
		}

		items = append(items, i)
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return items, nil
}

`, name, argDecl, elem, cols, targets, name, rest)
}

func (g *generator) binds(t types.Type, pq, lq query) ([]string, error) {
	var (
		exprs  []string
		fields []string
	)

	st, isStruct := t.Underlying().(*types.Struct)
	if isStruct && !isLeaf(t) {
		for i := range st.NumFields() {
			f := st.Field(i)

			v, err := g.bind("arg."+f.Name(), f.Type())
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name(), err)
			}

			exprs = append(exprs, v)
			fields = append(fields, f.Name())
		}
	} else {
		v, err := g.bind("arg", t)
		if err != nil {
			return nil, err
		}

		exprs = append(exprs, v)
	}

	if len(lq.Params) == 0 {
		return exprs, nil
	}

	if len(pq.Params) > 0 && len(pq.Params) != len(exprs) {
		return nil, fmt.Errorf(
			"%d parameters in the PostgreSQL query %v, %d in the Go method",
			len(pq.Params),
			pq.Params,
			len(exprs),
		)
	}

	if fields == nil && len(lq.Params) > 1 {
		return nil, fmt.Errorf("one argument, but the SQLite query names %v", lq.Params)
	}

	var binds []string

	for _, name := range lq.Params {
		if len(pq.Params) > 0 && !slices.Contains(pq.Params, name) {
			return nil, fmt.Errorf("the SQLite query names @%s, which the PostgreSQL query does not have", name)
		}

		i := 0
		if fields != nil {
			i = slices.IndexFunc(fields, func(f string) bool {
				return normalize(f) == normalize(name)
			})
			if i < 0 {
				return nil, fmt.Errorf("no field of %s matches @%s", g.typeString(t), name)
			}
		}

		binds = append(binds, fmt.Sprintf("sql.Named(%q, %s)", name, exprs[i]))
	}

	return binds, nil
}

func stripComments(sql string) string {
	var b strings.Builder

	for line := range strings.Lines(sql) {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			b.WriteString(line)
		}
	}

	return b.String()
}

func (g *generator) bind(expr string, t types.Type) (string, error) {
	switch g.typeString(t) {
	case "string", "int64", "int32", "bool", "float64", "[]byte",
		"*string", "*int64", "*int32", "*bool", "*float64", "any", "interface{}":
		return expr, nil
	case "time.Time":
		return "sqliteTime(" + expr + ")", nil
	case "*time.Time":
		return "sqliteNullTime(" + expr + ")", nil
	case "uuid.UUID":
		return expr + ".String()", nil
	case "[]time.Time":
		return "sqliteTimes(" + expr + ")", nil
	case "[]string", "[]int64", "[]int32", "[]float64", "[]bool", "[]uuid.UUID":
		return "sqliteJSON(" + expr + ")", nil
	default:
		return "", fmt.Errorf("cannot bind a %s", g.typeString(t))
	}
}

func (g *generator) scanTargets(expr string, t types.Type) ([]string, []string, error) {
	st, ok := t.Underlying().(*types.Struct)
	if !ok || isLeaf(t) {
		target, err := g.scanTarget(expr, t)

		return []string{
			target,
		}, nil, err
	}

	var (
		targets []string
		columns []string
	)

	for i := range st.NumFields() {
		f := st.Field(i)

		fs, ok := f.Type().Underlying().(*types.Struct)
		if ok && !isLeaf(f.Type()) {
			for j := range fs.NumFields() {
				ef := fs.Field(j)

				target, err := g.scanTarget(expr+"."+f.Name()+"."+ef.Name(), ef.Type())
				if err != nil {
					return nil, nil, fmt.Errorf("%s.%s: %w", f.Name(), ef.Name(), err)
				}

				targets = append(targets, target)
				columns = append(columns, normalize(ef.Name()))
			}

			continue
		}

		target, err := g.scanTarget(expr+"."+f.Name(), f.Type())
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f.Name(), err)
		}

		targets = append(targets, target)
		columns = append(columns, normalize(f.Name()))
	}

	return targets, columns, nil
}

func (g *generator) scanTarget(expr string, t types.Type) (string, error) {
	switch g.typeString(t) {
	case "string", "int64", "int32", "bool", "float64", "[]byte",
		"*string", "*int64", "*int32", "*bool", "*float64", "any", "interface{}":
		return "&" + expr, nil
	case "time.Time":
		return "sqliteTimeScanner{&" + expr + "}", nil
	case "*time.Time":
		return "sqliteNullTimeScanner{&" + expr + "}", nil
	case "uuid.UUID":
		return "sqliteUUIDScanner{&" + expr + "}", nil
	case "[]string":
		return "sqliteJSONScanner[[]string]{&" + expr + "}", nil
	case "[]float64":
		return "sqliteJSONScanner[[]float64]{&" + expr + "}", nil
	case "[]int64":
		return "sqliteJSONScanner[[]int64]{&" + expr + "}", nil
	default:
		return "", fmt.Errorf("cannot scan a %s", g.typeString(t))
	}
}

func isLeaf(t types.Type) bool {
	switch types.TypeString(t, nil) {
	case "time.Time", "uuid.UUID":
		return true
	}

	return false
}

func normalize(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

func backquote(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "` + \"`\" + `") + "`"
}

func (g *generator) source() ([]byte, error) {
	var b bytes.Buffer

	b.WriteString(`// Code generated by tools/sqlitebridge. DO NOT EDIT.

package database

import (
	"context"
	"database/sql"
`)

	if g.partial {
		b.WriteString("\t\"errors\"\n")
	}

	b.WriteString(`)

// newSQLiteQuerier returns the queries on db, the SQLite database or one
// of its transactions.
func newSQLiteQuerier(db sqliteDBTX) Querier {
	return &sqliteQueries{
		db: db,
	}
}

`)
	b.Write(g.queries.Bytes())
	b.Write(g.body.Bytes())

	src, err := imports.Process(outFile, b.Bytes(), nil)
	if err != nil {
		return nil, fmt.Errorf("format: %w\n%s", err, b.Bytes())
	}

	return src, nil
}

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"uuid"
)

type sqliteDBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type sqliteQueries struct {
	db sqliteDBTX
}

var checkedColumns sync.Map

func (q *sqliteQueries) query(
	ctx context.Context,
	name string,
	columns []string,
	query string,
	args ...any,
) (*sql.Rows, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	if _, done := checkedColumns.Load(name); done || columns == nil {
		return rows, nil
	}

	got, err := rows.Columns()
	if err != nil {
		_ = rows.Close() //nolint:sqlclosecheck // the rows are returned unless this check fails

		return nil, err
	}

	for i := range got {
		got[i] = strings.ToLower(strings.ReplaceAll(got[i], "_", ""))
	}

	if strings.Join(got, ",") != strings.Join(columns, ",") {
		_ = rows.Close() //nolint:sqlclosecheck // the rows are returned unless this check fails

		return nil, fmt.Errorf("sqlite query %s returns columns %v, want %v", name, got, columns)
	}

	checkedColumns.Store(name, true)

	return rows, nil
}

// Every time has the same width, so text order is time order.
const sqliteTimeFormat = "2006-01-02 15:04:05.000000-07:00"

var sqliteTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02",
}

func sqliteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

func sqliteNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}

	return sqliteTime(*t)
}

func sqliteTimes(ts []time.Time) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = sqliteTime(t)
	}

	return sqliteJSON(out)
}

func sqliteJSON(v any) string {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() {
		return "[]"
	}

	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encode a %T: %v", v, err))
	}

	return string(b)
}

func parseSQLiteTime(src any) (time.Time, error) {
	switch v := src.(type) {
	case time.Time:
		return v.UTC(), nil
	case string:
		return parseSQLiteTimeText(v)
	case []byte:
		return parseSQLiteTimeText(string(v))
	default:
		return time.Time{}, fmt.Errorf("cannot read a %T as a time", src)
	}
}

func parseSQLiteTimeText(s string) (time.Time, error) {
	for _, layout := range sqliteTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("cannot read %q as a time", s)
}

type sqliteTimeScanner struct {
	dst *time.Time
}

func (s sqliteTimeScanner) Scan(src any) error {
	t, err := parseSQLiteTime(src)
	if err != nil {
		return err
	}

	*s.dst = t

	return nil
}

type sqliteNullTimeScanner struct {
	dst **time.Time
}

func (s sqliteNullTimeScanner) Scan(src any) error {
	if src == nil {
		*s.dst = nil

		return nil
	}

	t, err := parseSQLiteTime(src)
	if err != nil {
		return err
	}

	*s.dst = &t

	return nil
}

type sqliteUUIDScanner struct {
	dst *uuid.UUID
}

func (s sqliteUUIDScanner) Scan(src any) error {
	var text string

	switch v := src.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("cannot read a %T as a UUID", src)
	}

	u, err := uuid.Parse(text)
	if err != nil {
		return err
	}

	*s.dst = u

	return nil
}

type sqliteJSONScanner[T any] struct {
	dst *T
}

func (s sqliteJSONScanner[T]) Scan(src any) error {
	var b []byte

	switch v := src.(type) {
	case nil:
		b = []byte("[]")
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		return fmt.Errorf("cannot read a %T as a JSON array", src)
	}

	return json.Unmarshal(b, s.dst)
}

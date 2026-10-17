package itemquery

import (
	"errors"
	"reflect"
	"testing"
)

func TestCompile(t *testing.T) {
	t.Parallel()

	two, three := int64(2), int64(3)

	tests := []struct {
		name      string
		q         Query
		wantWhere string
		wantArgs  []any
		wantTail  string
	}{
		{
			"nothing matches everything",
			Query{},
			`TRUE`,
			nil,
			"",
		},
		{
			"an empty All is true",
			Query{
				Criteria: All{},
			},
			`TRUE`,
			nil,
			"",
		},
		{
			"an empty Any is false",
			Query{
				Criteria: Any{},
			},
			`FALSE`,
			nil,
			"",
		},
		{
			"wildcards become LIKE with escapes",
			Query{
				Criteria: Compare{
					Field: FieldName,
					Op:    OpMatch,
					Value: "a_b*%?.jar",
				},
			},
			`CAST(i.name AS TEXT) LIKE $1 ESCAPE '\'`,
			[]any{
				`a\_b%\%_.jar`,
			},
			"",
		},
		{
			"any, numbers and not-equal",
			Query{
				Criteria: Any{
					Compare{
						Field: FieldSize,
						Op:    OpGreater,
						Value: "10",
					},
					Compare{
						Field: FieldName,
						Op:    OpNotEqual,
						Value: "x",
					},
				},
			},
			`(i.size > $1 OR (i.name IS NULL OR i.name <> $2))`,
			[]any{
				int64(10),
				"x",
			},
			"",
		},
		{
			"not-match keeps missing values, as not-equal does",
			Query{
				Criteria: Compare{
					Field: FieldSHA256,
					Op:    OpNotMatch,
					Value: "ab*",
				},
			},
			`(i.sha256 IS NULL OR CAST(i.sha256 AS TEXT) NOT LIKE $1 ESCAPE '\')`,
			[]any{
				`ab%`,
			},
			"",
		},
		{
			"a whole float is a number",
			Query{
				Criteria: Compare{
					Field: FieldSize,
					Op:    OpGreaterOrEqual,
					Value: float64(5),
				},
			},
			`i.size >= $1`,
			[]any{
				int64(5),
			},
			"",
		},
		{
			"missing values",
			Query{
				Criteria: All{
					Compare{
						Field: FieldSHA256,
						Op:    OpEqual,
						Value: nil,
					},
					Compare{
						Field: FieldMD5,
						Op:    OpNotEqual,
						Value: nil,
					},
				},
			},
			`(i.sha256 IS NULL AND i.md5 IS NOT NULL)`,
			nil,
			"",
		},
		{
			"property criteria; negative ones match items without the value",
			Query{
				Criteria: All{
					PropertyValue{
						Key:   "stage",
						Op:    OpEqual,
						Value: "dev",
					},
					PropertyValue{
						Key:   "team",
						Op:    OpNotMatch,
						Value: "q*",
					},
				},
			},
			`(EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = $2 AND p.value = $1) AND NOT EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = $4 AND CAST(p.value AS TEXT) LIKE $3 ESCAPE '\'))`,
			[]any{
				"dev",
				"stage",
				"q%",
				"team",
			},
			"",
		},
		{
			"one property satisfies every child",
			Query{
				Criteria: SameProperty{
					PropertyValue{
						Key:   "a",
						Op:    OpEqual,
						Value: "1",
					},
					PropertyKey{
						Op:    OpMatch,
						Value: "z*",
					},
				},
			},
			`EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = $2 AND p.value = $1 AND CAST(p.key AS TEXT) LIKE $3 ESCAPE '\')`,
			[]any{
				"1",
				"a",
				"z%",
			},
			"",
		},
		{
			"a negative comparison in SameProperty applies to the row",
			Query{
				Criteria: SameProperty{
					PropertyValue{
						Key:   "k",
						Op:    OpNotEqual,
						Value: "v",
					},
				},
			},
			`EXISTS (SELECT 1 FROM item_properties p WHERE p.item_id = i.id AND p.key = $2 AND (p.value IS NULL OR p.value <> $1))`,
			[]any{
				"v",
				"k",
			},
			"",
		},
		{
			"build criteria of one relation share one EXISTS",
			Query{
				Criteria: All{
					Build{
						Relation: BuildArtifact,
						Field:    BuildName,
						Op:       OpEqual,
						Value:    "b",
					},
					Build{
						Relation: BuildArtifact,
						Field:    BuildNumber,
						Op:       OpEqual,
						Value:    "7",
					},
				},
			},
			`(EXISTS (SELECT 1 FROM build_artifacts x JOIN builds b ON b.id = x.build_id WHERE x.sha1 = i.sha1 AND b.name = $1 AND b.number = $2))`,
			[]any{
				"b",
				"7",
			},
			"",
		},
		{
			"dependency criteria",
			Query{
				Criteria: Build{
					Relation: BuildDependency,
					Field:    BuildName,
					Op:       OpMatch,
					Value:    "b*",
				},
			},
			`EXISTS (SELECT 1 FROM build_dependencies x JOIN builds b ON b.id = x.build_id WHERE x.sha1 = i.sha1 AND CAST(b.name AS TEXT) LIKE $1 ESCAPE '\')`,
			[]any{
				"b%",
			},
			"",
		},
		{
			"sort offset limit",
			Query{
				Criteria: Compare{
					Field: FieldRepository,
					Op:    OpEqual,
					Value: "r",
				},
				Sort: &Sort{
					Desc: true,
					Fields: []Field{
						FieldCreated,
						FieldName,
					},
				},
				Offset: &two,
				Limit:  &three,
			},
			`i.repo_key = $1`,
			[]any{
				"r",
				int64(3),
				int64(2),
			},
			` ORDER BY i.created DESC NULLS FIRST, i.name DESC NULLS FIRST LIMIT $2 OFFSET $3`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan, err := Compile(tt.q)
			if err != nil {
				t.Fatal(err)
			}

			want := "SELECT " + itemColumns + " FROM items i WHERE " + tt.wantWhere + tt.wantTail
			if plan.sql != want {
				t.Errorf("SQL\n got %s\nwant %s", plan.sql, want)
			}

			if !reflect.DeepEqual(plan.args, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", plan.args, tt.wantArgs)
			}
		})
	}
}

func TestCompileRejects(t *testing.T) {
	t.Parallel()

	for name, q := range map[string]Query{
		"unknown field": {
			Criteria: Compare{
				Field: "nope",
				Op:    OpEqual,
				Value: "x",
			},
		},
		"unknown operator": {
			Criteria: Compare{
				Field: FieldName,
				Op:    "near",
				Value: "x",
			},
		},
		"not a number": {
			Criteria: Compare{
				Field: FieldSize,
				Op:    OpEqual,
				Value: "ten",
			},
		},
		"a fractional size": {
			Criteria: Compare{
				Field: FieldSize,
				Op:    OpGreaterOrEqual,
				Value: 5.7,
			},
		},
		"an empty same property": {
			Criteria: SameProperty{},
		},
		"a size past int64": {
			Criteria: Compare{
				Field: FieldSize,
				Op:    OpGreater,
				Value: 1e20,
			},
		},
		"match takes a string": {
			Criteria: Compare{
				Field: FieldName,
				Op:    OpMatch,
				Value: int64(5),
			},
		},
		"not a date": {
			Criteria: Compare{
				Field: FieldCreated,
				Op:    OpGreater,
				Value: "yesterday",
			},
		},
		"greater than missing": {
			Criteria: Compare{
				Field: FieldSize,
				Op:    OpGreater,
				Value: nil,
			},
		},
		"unknown sort field": {
			Sort: &Sort{
				Fields: []Field{
					"nope",
				},
			},
		},
		"unknown build relation": {
			Criteria: Build{
				Relation: "parent",
				Field:    BuildName,
				Op:       OpEqual,
				Value:    "x",
			},
		},
		"a field in SameProperty": {
			Criteria: SameProperty{
				Compare{
					Field: FieldName,
					Op:    OpEqual,
					Value: "x",
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := Compile(q); !errors.Is(err, ErrInvalid) {
				t.Errorf("Compile error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestExpandRepositories(t *testing.T) {
	t.Parallel()

	members := map[string][]string{
		"virtual": {
			"a",
			"b",
		},
		"empty": nil,
	}
	expand := func(name string) []string {
		if m, ok := members[name]; ok {
			return m
		}

		return []string{
			name,
		}
	}
	repo := func(n string) Compare {
		return Compare{
			Field: FieldRepository,
			Op:    OpEqual,
			Value: n,
		}
	}
	got := ExpandRepositories(All{
		repo("virtual"),
		Any{
			repo("plain"),
			repo("empty"),
		},
	}, expand)

	want := All{
		Any{
			repo("a"),
			repo("b"),
		},
		Any{
			repo("plain"),
			repo("empty"),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExpandRepositories = %#v, want %#v", got, want)
	}
}

func TestWildcardToLike(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"*":      "%",
		"a?c":    "a_c",
		`100%_\`: `100\%\_\\`,
		"plain":  "plain",
	} {
		if got := WildcardToLike(in); got != want {
			t.Errorf("WildcardToLike(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDepth(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]int{
		".":   1,
		"a":   2,
		"a/b": 3,
	} {
		if got := (Row{
			Path: path,
		}).Depth(); got != want {
			t.Errorf("Depth(%q) = %d, want %d", path, got, want)
		}
	}
}

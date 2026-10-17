package aql

import (
	"errors"
	"reflect"
	"testing"
	"time"

	iq "relic/internal/artifacts/itemquery"
)

var files = iq.Compare{
	Field: iq.FieldType,
	Op:    iq.OpEqual,
	Value: "file",
}

func TestItemQuery(t *testing.T) {
	t.Parallel()

	limit, offset := int64(3), int64(2)

	tests := []struct {
		name string
		src  string
		want iq.Query
	}{
		{
			"finds files unless a type is named",
			`items.find({"repo":"libs"})`,
			iq.Query{
				Criteria: iq.All{
					iq.Compare{
						Field: iq.FieldRepository,
						Op:    iq.OpEqual,
						Value: "libs",
					},
					files,
				},
			},
		},
		{
			"type any finds every type",
			`items.find({"repo":"libs","type":"any"})`,
			iq.Query{
				Criteria: iq.All{
					iq.Compare{
						Field: iq.FieldRepository,
						Op:    iq.OpEqual,
						Value: "libs",
					},
					iq.All{},
				},
			},
		},
		{
			"fields and operators",
			`items.find({"$or":[{"size":{"$gt":"10"}},{"actual_sha1":{"$ne":"x"}}]})`,
			iq.Query{
				Criteria: iq.All{
					iq.Any{
						iq.Compare{
							Field: iq.FieldSize,
							Op:    iq.OpGreater,
							Value: "10",
						},
						iq.Compare{
							Field: iq.FieldSHA1,
							Op:    iq.OpNotEqual,
							Value: "x",
						},
					},
					files,
				},
			},
		},
		{
			"properties",
			`items.find({"@stage":"dev","property.key":{"$match":"t*"}})`,
			iq.Query{
				Criteria: iq.All{
					iq.All{
						iq.PropertyValue{
							Key:   "stage",
							Op:    iq.OpEqual,
							Value: "dev",
						},
						iq.PropertyKey{
							Op:    iq.OpMatch,
							Value: "t*",
						},
					},
					files,
				},
			},
		},
		{
			"match single property",
			`items.find({"$msp":[{"@a":"1"},{"property.value":{"$match":"z*"}}]})`,
			iq.Query{
				Criteria: iq.All{
					iq.SameProperty{
						iq.PropertyValue{
							Key:   "a",
							Op:    iq.OpEqual,
							Value: "1",
						},
						iq.PropertyValue{
							Op:    iq.OpMatch,
							Value: "z*",
						},
					},
					files,
				},
			},
		},
		{
			"a negative comparison in $msp stays negative",
			`items.find({"$msp":[{"@a":{"$ne":"1"}}]})`,
			iq.Query{
				Criteria: iq.All{
					iq.SameProperty{
						iq.PropertyValue{
							Key:   "a",
							Op:    iq.OpNotEqual,
							Value: "1",
						},
					},
					files,
				},
			},
		},
		{
			"builds",
			`items.find({"artifact.module.build.name":"b","dependency.module.build.number":"7"})`,
			iq.Query{
				Criteria: iq.All{
					iq.All{
						iq.Build{
							Relation: iq.BuildArtifact,
							Field:    iq.BuildName,
							Op:       iq.OpEqual,
							Value:    "b",
						},
						iq.Build{
							Relation: iq.BuildDependency,
							Field:    iq.BuildNumber,
							Op:       iq.OpEqual,
							Value:    "7",
						},
					},
					files,
				},
			},
		},
		{
			"dates",
			`items.find({"type":"file","created":{"$gt":"2026-01-02T03:04:05.000+0000"}})`,
			iq.Query{
				Criteria: iq.All{
					iq.Compare{
						Field: iq.FieldType,
						Op:    iq.OpEqual,
						Value: "file",
					},
					iq.Compare{
						Field: iq.FieldCreated,
						Op:    iq.OpGreater,
						Value: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
					},
				},
			},
		},
		{
			"sort offset limit",
			`items.find({"type":"folder"}).sort({"$desc":["created","name"]}).offset(2).limit(3)`,
			iq.Query{
				Criteria: iq.Compare{
					Field: iq.FieldType,
					Op:    iq.OpEqual,
					Value: "folder",
				},
				Sort: &iq.Sort{
					Desc: true,
					Fields: []iq.Field{
						iq.FieldCreated,
						iq.FieldName,
					},
				},
				Offset: &offset,
				Limit:  &limit,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q, err := Parse(tt.src)
			if err != nil {
				t.Fatal(err)
			}

			got, err := ItemQuery(q)
			if err != nil {
				t.Fatal(err)
			}

			if got.Criteria != nil {
				if c, ok := got.Criteria.(iq.All); ok && len(c) == 2 {
					if cmp, ok := c[1].(iq.Compare); ok && cmp.Field == iq.FieldCreated {
						cmp.Value = cmp.Value.(time.Time).UTC()
						c[1] = cmp
					}
				}
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ItemQuery\n got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestPlanItemsFields(t *testing.T) {
	t.Parallel()

	q, _ := Parse(`items.find().include("name","repo","property","name")`)

	plan, err := PlanItems(q)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(plan.Fields, []string{
		"name",
		"repo",
	}) || !plan.Properties || !plan.Query.Properties {
		t.Errorf("fields = %v, properties = %v", plan.Fields, plan.Properties)
	}

	q, _ = Parse(`items.find().include("size","*","*")`)

	plan, err = PlanItems(q)
	if err != nil {
		t.Fatal(err)
	}

	if plan.Fields[0] != "size" || len(plan.Fields) != len(DefaultItemFields)+6 {
		t.Errorf("fields with size before * = %v", plan.Fields)
	}
}

func TestPlanItemsRejects(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		`builds.find()`,
		`items.find({"nope":"x"})`,
		`items.find().include("nope")`,
		`items.find().sort({"$asc":["nope"]})`,
		`items.find({"size":"ten"})`,
		`items.find({"name":{"$match":5}})`,
		`items.find({"created":{"$gt":"yesterday"}})`,
	} {
		t.Run(src, func(t *testing.T) {
			t.Parallel()

			q, err := Parse(src)
			if err != nil {
				t.Fatal(err)
			}

			_, err = PlanItems(q)
			if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrSyntax) {
				t.Errorf("PlanItems(%s) error = %v", src, err)
			}
		})
	}
}

func TestPlanItemsMatchesATimeAsText(t *testing.T) {
	t.Parallel()

	q, err := Parse(`items.find({"created":{"$match":"2024-01-15T10:30:00.000Z"}})`)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := PlanItems(q); err != nil {
		t.Errorf("PlanItems with $match on a timestamp = %v, want the match planned", err)
	}
}

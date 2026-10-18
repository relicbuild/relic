package aql

import (
	"errors"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want *Query
	}{
		{
			name: "empty find",
			src:  `items.find()`,
			want: &Query{
				Domain: "items",
			},
		},
		{
			name: "implicit and with eq and match",
			src:  `items.find({"repo":"libs","path":{"$match":"a/*"}})`,
			want: &Query{
				Domain: "items",
				Criteria: And{
					Compare{
						Field: "repo",
						Op:    OpEq,
						Value: "libs",
					},
					Compare{
						Field: "path",
						Op:    OpMatch,
						Value: "a/*",
					},
				},
			},
		},
		{
			name: "or of ands with property and modifiers",
			src: `items.find({"$or":[{"$and":[{"repo":"r","name":{"$match":"*.jar"}}]}],"@build.name":"x"})
			      .include("name","repo","property").sort({"$desc":["created","name"]}).offset(5).limit(10)`,
			want: &Query{
				Domain: "items",
				Criteria: And{
					Or{
						And{
							Compare{
								Field: "repo",
								Op:    OpEq,
								Value: "r",
							},
							Compare{
								Field: "name",
								Op:    OpMatch,
								Value: "*.jar",
							},
						},
					},
					Compare{
						Field: "@build.name",
						Op:    OpEq,
						Value: "x",
					},
				},
				Include: []string{
					"name",
					"repo",
					"property",
				},
				Sort: &Sort{
					Desc: true,
					Fields: []string{
						"created",
						"name",
					},
				},
				Offset: new(int64(5)),
				Limit:  new(int64(10)),
			},
		},
		{
			name: "members of an object inside $or are alternatives",
			src:  `items.find({"$or":[{"repo":"a","repo":"b"}]})`,
			want: &Query{
				Domain: "items",
				Criteria: Or{
					Compare{
						Field: "repo",
						Op:    OpEq,
						Value: "a",
					},
					Compare{
						Field: "repo",
						Op:    OpEq,
						Value: "b",
					},
				},
			},
		},
		{
			name: "range operators on one field and numbers",
			src:  `items.find({"size":{"$gt":10,"$lte":20.0}})`,
			want: &Query{
				Domain: "items",
				Criteria: And{
					Compare{
						Field: "size",
						Op:    OpGt,
						Value: float64(10),
					},
					Compare{
						Field: "size",
						Op:    OpLte,
						Value: float64(20),
					},
				},
			},
		},
		{
			name: "msp and escapes",
			src:  `items.find({"$msp":[{"@a":"1"},{"@b":{"$ne":"q\"ü"}}]})`,
			want: &Query{
				Domain: "items",
				Criteria: MatchSingleProperty{
					{
						Field: "@a",
						Op:    OpEq,
						Value: "1",
					},
					{
						Field: "@b",
						Op:    OpNe,
						Value: "q\"ü",
					},
				},
			},
		},
		{
			name: "surrogate pair escape",
			src:  `items.find({"name":"\ud83d\ude00.txt"})`,
			want: &Query{
				Domain: "items",
				Criteria: Compare{
					Field: "name",
					Op:    OpEq,
					Value: "\U0001F600.txt",
				},
			},
		},
		{
			name: "lone surrogate escape",
			src:  `items.find({"name":"\ud83d.txt"})`,
			want: &Query{
				Domain: "items",
				Criteria: Compare{
					Field: "name",
					Op:    OpEq,
					Value: "\uFFFD.txt",
				},
			},
		},
		{
			name: "transitive changes nothing: virtual repositories always cover their members",
			src:  `items.find({"repo":"libs"}).include("name").transitive()`,
			want: &Query{
				Domain: "items",
				Criteria: Compare{
					Field: "repo",
					Op:    OpEq,
					Value: "libs",
				},
				Include: []string{
					"name",
				},
			},
		},
		{
			name: "not equal to a wildcard is a negated match",
			src:  `items.find({"@release":{"$ne":{"$match":"1.*"}}})`,
			want: &Query{
				Domain: "items",
				Criteria: Compare{
					Field: "@release",
					Op:    OpNmatch,
					Value: "1.*",
				},
			},
		},
		{
			name: "equal to a wildcard is a match",
			src:  `items.find({"name":{"$eq":{"$match":"*.jar"}}})`,
			want: &Query{
				Domain: "items",
				Criteria: Compare{
					Field: "name",
					Op:    OpMatch,
					Value: "*.jar",
				},
			},
		},
		{
			name: "not equal to a negated match is a match",
			src:  `items.find({"name":{"$ne":{"$nmatch":"*.jar"}}})`,
			want: &Query{
				Domain: "items",
				Criteria: Compare{
					Field: "name",
					Op:    OpMatch,
					Value: "*.jar",
				},
			},
		},
		{
			name: "space between a dot and find is rejected",
			src:  " items . find ( { \"type\" : \"any\" } ) . distinct ( true ) ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.src)
			if tt.want == nil {
				if err == nil {
					t.Fatalf("Parse(%q) succeeded, want an error", tt.src)
				}

				return
			}

			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%s)\n got %#v\nwant %#v", tt.src, got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		``,
		`items`,
		`items.find(`,
		`items.find({"repo":})`,
		`items.find({"repo":"a"}`,
		`items.find({repo:"a"})`,
		`items.find({"$nope":[]})`,
		`items.find({"name":{"$like":"x"}})`,
		`items.find({"name":["a"]})`,
		`items.find({"name":{"$gt":{"$match":"a*"}}})`,
		`items.find({"name":{"$ne":{"$gt":"a"}}})`,
		`items.find({"name":{"$ne":{"$match":"a*","$nmatch":"b*"}}})`,
		`items.find({"name":{"$ne":{"$match":1}}})`,
		`items.find({"$and":"x"})`,
		`items.find().limit(-1)`,
		`items.find().limit(1.5)`,
		`items.find().sort({"$up":["a"]})`,
		`items.find().frobnicate()`,
		`items.find().transitive(true)`,
		`items.find({"name":"unterminated})`,
		`items.find({"name":"\q"})`,
		`items.find() trailing`,
	} {
		t.Run(src, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse(src); !errors.Is(err, ErrSyntax) {
				t.Errorf("Parse(%q) error = %v, want ErrSyntax", src, err)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`items.find({"repo":"libs","path":{"$match":"a/*"}}).include("name","property").sort({"$asc":["name"]}).offset(1).limit(2)`,
		`items.find({"$or":[{"$and":[{"@k":{"$nmatch":"v*"}}]}]})`,
		`items.find({"$msp":[{"@a":"1"}]})`,
		`items.find({"size":{"$gte":1e3}})`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, src string) {
		q, err := Parse(src)
		if err != nil {
			return
		}

		if q.Domain == "items" {
			_, _ = PlanItems(q)
		}
	})
}

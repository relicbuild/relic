package aql

import (
	"reflect"
	"testing"
)

func TestExpandRepos(t *testing.T) {
	t.Parallel()

	expand := func(key string) []string {
		if key == "virt" {
			return []string{
				"a",
				"b",
			}
		}

		if key == "empty-virt" {
			return []string{}
		}

		return []string{
			key,
		}
	}

	tests := []struct {
		name string
		in   Node
		want Node
	}{
		{
			"nil criteria",
			nil,
			nil,
		},
		{
			"plain repo",
			Compare{
				"repo",
				OpEq,
				"a",
			},
			Compare{
				"repo",
				OpEq,
				"a",
			},
		},
		{
			"virtual inside and",
			And{
				Compare{
					"repo",
					OpEq,
					"virt",
				},
				Compare{
					"path",
					OpEq,
					"x",
				},
			},
			And{
				Or{
					Compare{
						"repo",
						OpEq,
						"a",
					},
					Compare{
						"repo",
						OpEq,
						"b",
					},
				},
				Compare{
					"path",
					OpEq,
					"x",
				},
			},
		},
		{
			"virtual inside or",
			Or{
				Compare{
					"repo",
					OpEq,
					"virt",
				},
				Compare{
					"repo",
					OpEq,
					"c",
				},
			},
			Or{
				Or{
					Compare{
						"repo",
						OpEq,
						"a",
					},
					Compare{
						"repo",
						OpEq,
						"b",
					},
				},
				Compare{
					"repo",
					OpEq,
					"c",
				},
			},
		},
		{
			"virtual without members",
			Compare{
				"repo",
				OpEq,
				"empty-virt",
			},
			Compare{
				"repo",
				OpEq,
				"empty-virt",
			},
		},
		{
			"match is untouched",
			Compare{
				"repo",
				OpMatch,
				"virt",
			},
			Compare{
				"repo",
				OpMatch,
				"virt",
			},
		},
		{
			"other field",
			Compare{
				"name",
				OpEq,
				"virt",
			},
			Compare{
				"name",
				OpEq,
				"virt",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ExpandRepos(tt.in, expand); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExpandRepos = %#v, want %#v", got, tt.want)
			}
		})
	}
}
